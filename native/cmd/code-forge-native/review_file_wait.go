package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/ledger"
	"gopkg.in/yaml.v3"
)

func init() { routes["review-file"] = runReviewFileWait }

var reviewWaitTicket = regexp.MustCompile(`^[0-9a-f]{24}$`)
var reviewWaitMax = regexp.MustCompile(`^(\d{1,7})(ms|s)?$`)
var reviewWaitSurrogate = regexp.MustCompile(`(?i)\\ud[89a-f][0-9a-f]{2}`)

const reviewWaitUsage = "usage: code-forge review-file [--cwd <dir>] <path> --block <id> [--run <id>] [--fresh] | review-file --wait <ticket> [--max <seconds>s]\n" +
	"       code-forge review-file --wait-block <id> --run <id> [--max <seconds>s]\n" +
	"       code-forge review-file --rebase-check --block <id> --run <id>\n" +
	"       code-forge review-file <answering file> --block <id> --answers <id,id,…> --of <source> [--run <id>]\n" +
	"       --cwd <dir> applies to queueing and both wait modes\n"

// Only wait and its flags are owned here; help, enqueue and block joins stay
// with Node. Keep parser errors separate from validation after root discovery.
func reviewWaitFlags(args []string) (map[string]string, string, bool) {
	flags := map[string]string{}
	wait := false
	for _, arg := range args {
		if arg == "--wait" || strings.HasPrefix(arg, "--wait=") {
			wait = true
		}
	}
	if !wait {
		return nil, "", false
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			return nil, "", false
		}
		name, value, inline := strings.Cut(arg[2:], "=")
		if name != "wait" && name != "max" && name != "cwd" && name != "run" {
			return nil, "", false
		}
		if _, exists := flags[name]; exists {
			return flags, "--" + name + " given more than once", true
		}
		if !inline {
			i++
			if i >= len(args) || strings.HasPrefix(args[i], "--") {
				return flags, "--" + name + " needs a value", true
			}
			value = args[i]
		}
		if inline && value == "" {
			return flags, "--" + name + " needs a value", true
		}
		flags[name] = value
	}
	return flags, "", true
}

func reviewWaitHandover(args []string) int {
	code, err := handover(append([]string{"review-file"}, args...))
	if err != nil {
		fmt.Fprintln(os.Stderr, "code-forge-native:", err)
	}
	return code
}

func runReviewFileWait(args []string) int {
	for _, arg := range args {
		if arg == "--wait-block" || strings.HasPrefix(arg, "--wait-block=") {
			return runReviewWaitBlock(args)
		}
	}
	flags, usage, ok := reviewWaitFlags(args)
	if !ok {
		return reviewWaitHandover(args)
	}
	if usage != "" {
		return reviewWaitUsageError(args, usage)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return reviewWaitHandover(args)
	}
	if dir, exists := flags["cwd"]; exists {
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(cwd, dir)
		}
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			return reviewWaitHandover(args)
		}
		cwd = filepath.Clean(dir)
	}
	ctx, cancel := context.WithCancel(context.Background())
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	var interrupted atomic.Int32
	stopped := make(chan struct{})
	go func() {
		select {
		case sig := <-signals:
			interrupted.Store(int32(128 + sig.(syscall.Signal)))
			cancel()
		case <-stopped:
		}
	}()
	defer func() { signal.Stop(signals); close(stopped); cancel() }()
	root, ok := reviewWaitRoot(ctx, cwd)
	if ctx.Err() != nil {
		return int(interrupted.Load())
	}
	if !ok {
		return reviewWaitHandover(args)
	}
	if !reviewWaitTicket.MatchString(flags["wait"]) {
		return reviewWaitUsageError(args, "a ticket id is 24 lowercase hex characters")
	}
	max := 90 * time.Second
	if raw, exists := flags["max"]; exists {
		match := reviewWaitMax.FindStringSubmatch(raw)
		if match == nil {
			return reviewWaitUsageError(args, "--max must look like 90s or 1500ms")
		}
		n, _ := strconv.ParseInt(match[1], 10, 64)
		max = time.Duration(n) * time.Second
		if match[2] == "ms" {
			max = time.Duration(n) * time.Millisecond
		}
	}
	deadline := time.Now().Add(max)
	ticket := flags["wait"]
	marker := filepath.Join(root, ".code-forge", "queue", ticket+".done")
	down := 0
	for {
		if ctx.Err() != nil {
			return int(interrupted.Load())
		}
		if _, err := os.Stat(marker); err == nil {
			result, matched := reviewWaitResult(root, marker)
			if !matched {
				return reviewWaitHandover(args)
			}
			out := ledger.Object{{Key: "ticket", Value: ticket}, {Key: "status", Value: "done"}, {Key: "result", Value: result}}
			if !reviewWaitSummary(&out, result) {
				return reviewWaitHandover(args)
			}
			return reviewWaitSay(out)
		}
		live, matched := reviewWaitLive(ctx, root, deadline)
		if ctx.Err() != nil {
			return int(interrupted.Load())
		}
		if !matched {
			return reviewWaitHandover(args)
		}
		if live {
			down = 0
		} else {
			down++
		}
		if down >= 2 {
			if _, err := os.Stat(marker); err != nil {
				before := reviewWaitBreadcrumb()
				data, _ := ledger.Stringify(ledger.Object{{Key: "ticket", Value: ticket}, {Key: "status", Value: "worker_down"}}, false)
				fmt.Fprintln(os.Stdout, string(data))
				reviewWaitFailure(args, 3, nil, before)
				return 3
			}
		}
		if !time.Now().Before(deadline) && down == 0 {
			return reviewWaitSay(ledger.Object{{Key: "ticket", Value: ticket}, {Key: "status", Value: "pending"}, {Key: "reason", Value: "wait_timeout"}})
		}
		// Node checks every 200ms, including the second down poll. A live
		// worker's deadline can wake sooner so --max is a hard bound.
		pause := 200 * time.Millisecond
		if down == 0 && time.Until(deadline) < pause {
			pause = maxDuration(0, time.Until(deadline))
		}
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return int(interrupted.Load())
		case <-timer.C:
		}
	}
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

// Run waits for every child. Cancelling kills its process group as well as the
// direct process, so neither inherited pipes nor descendants can outlive us.
func reviewWaitCommand(ctx context.Context, cwd, name string, args []string, env []string, limit time.Duration) (string, bool) {
	childCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	cmd := exec.CommandContext(childCtx, name, args...)
	cmd.Dir, cmd.Env = cwd, env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 50 * time.Millisecond
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if cmd.Process != nil {
		// A helper could exit before descendants close their inherited pipes.
		// WaitDelay bounds the pipe wait; retire the group in that case too.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return out.String(), err == nil
}

func reviewWaitRoot(ctx context.Context, cwd string) (string, bool) {
	env := []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			env = append(env, entry)
		}
	}
	env = append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	git := func(args ...string) (string, bool) {
		return reviewWaitCommand(ctx, cwd, "git", args, env, 10*time.Second)
	}
	top, ok := git("rev-parse", "--show-toplevel")
	if !ok || strings.TrimSpace(top) == "" {
		return "", false
	}
	top, err := filepath.EvalSymlinks(strings.TrimSpace(top))
	if err != nil {
		return "", false
	}
	dirs, ok := git("rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir")
	parts := strings.Split(dirs, "\n")
	if !ok || len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	real := func(s string) string {
		if p, err := filepath.EvalSymlinks(s); err == nil {
			return p
		}
		return filepath.Clean(s)
	}
	if real(parts[0]) == real(parts[1]) {
		return top, true
	}
	list, ok := git("worktree", "list", "--porcelain", "-z")
	if !ok {
		return "", false
	}
	first := strings.Split(strings.Split(list, "\x00\x00")[0], "\x00")
	main := ""
	for _, field := range first {
		if field == "bare" {
			return "", false
		}
		if strings.HasPrefix(field, "worktree ") {
			main = strings.TrimPrefix(field, "worktree ")
		}
	}
	if main == "" {
		return "", false
	}
	main, err = filepath.EvalSymlinks(main)
	return main, err == nil
}

// The existing ledger encoder preserves JS property order and scalar bytes.
// Unpaired UTF-16 surrogates and numbers outside its decoder's range stay on
// Node; silently replacing either would break JSON.parse/stringify parity.
func reviewWaitJSON(file string) (ledger.Object, bool) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, true
	}
	if !json.Valid(data) {
		return nil, true
	}
	if !utf8.Valid(data) || reviewWaitSurrogate.Match(data) {
		return nil, false
	}
	obj, err := ledger.DecodeObject(data)
	if err != nil {
		if len(bytes.TrimSpace(data)) > 0 && bytes.TrimSpace(data)[0] == '{' {
			return nil, false
		}
		return nil, true
	}
	return obj, true
}

func reviewWaitResult(root, marker string) (any, bool) {
	m, ok := reviewWaitJSON(marker)
	if !ok {
		return nil, false
	}
	rel, _ := m.Get("result").(string)
	if !strings.HasPrefix(rel, ".code-forge"+string(filepath.Separator)+"reviews"+string(filepath.Separator)) {
		return nil, true
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == ".." {
			return nil, true
		}
	}
	obj, ok := reviewWaitJSON(filepath.Join(root, rel))
	if obj == nil {
		return nil, ok
	}
	return obj, ok
}

func reviewWaitSummary(out *ledger.Object, value any) bool {
	r, ok := value.(ledger.Object)
	if !ok {
		return true
	}
	for _, key := range []string{"queued_at", "started_at", "finished_at", "duration_ms"} {
		for _, field := range r {
			if field.Key == key {
				out.Set(key, field.Value)
			}
		}
	}
	if r.Get("approved") == true {
		return true
	}
	findings, array := r.Get("findings").([]any)
	answers := r.Get("kind") == "answers" || r.Get("status") == "answers"
	if array && (answers || len(findings) > 0) {
		list := []any{}
		for _, value := range findings {
			f, _ := value.(ledger.Object)
			get := func(key string, fallback any) any {
				if v := f.Get(key); v != nil {
					return v
				}
				return fallback
			}
			if answers {
				list = append(list, ledger.Object{{Key: "id", Value: get("id", nil)}, {Key: "verdict", Value: get("verdict", "not_resolved")}, {Key: "reason", Value: get("reason", "")}})
				continue
			}
			line := func(key string) (string, bool) {
				v := get(key, "?")
				if s, ok := v.(string); ok {
					return s, true
				}
				switch v.(type) {
				case float64, bool:
					b, err := ledger.Stringify(v, false)
					return string(b), err == nil
				}
				return "", false
			}
			start, a := line("line_start")
			end, b := line("line_end")
			if !a || !b {
				return false
			}
			list = append(list, ledger.Object{{Key: "id", Value: get("id", nil)}, {Key: "severity", Value: get("severity", nil)}, {Key: "lines", Value: start + "-" + end}, {Key: "claim", Value: get("claim", "")}, {Key: "fix", Value: get("fix", "")}})
		}
		key := "fix_list"
		if answers {
			key = "answers"
		}
		out.Set(key, list)
	}
	if answers {
		return true
	}
	stop, stopped := r.Get("stopped").(string)
	if stopped {
		out.Set("stop", stop)
		out.Set("exits", []any{"block reopen", "block " + "waive", "review-file --answers", "re-decompose"})
	} else if r.Get("status") == "split_required" {
		stop = "split_required"
		out.Set("stop", stop)
	}
	if stop == "split_required" {
		for _, key := range []string{"tokens_in", "budget"} {
			var value any
			if n, ok := r.Get(key).(float64); ok {
				value = n
			}
			out.Set(key, value)
		}
		if section, ok := r.Get("section").(string); ok {
			out.Set("section", section)
		}
	}
	return true
}

func reviewWaitLive(ctx context.Context, root string, deadline time.Time) (bool, bool) {
	dir := filepath.Join(root, ".code-forge", "workers")
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return false, false
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		w, matched := reviewWaitJSON(filepath.Join(dir, entry.Name()))
		if !matched {
			return false, false
		}
		for _, key := range []string{"started_at", "heartbeat_at"} {
			if _, stringDate := w.Get(key).(string); stringDate {
				if _, supported := reviewWaitDate(w.Get(key)); !supported {
					return false, false
				}
			}
		}
		pid, start, shape := reviewWaitWorkerShape(w)
		if !shape || entry.Name() != strconv.Itoa(pid)+"-"+start+".json" {
			continue
		}
		if live, ok := reviewWaitWorkerLive(ctx, w, deadline); live || !ok {
			return live, ok
		}
	}
	w, matched := reviewWaitJSON(filepath.Join(root, ".code-forge", "queue", "worker.json"))
	if !matched {
		return false, false
	}
	pid, _ := w.Get("pid").(float64)
	_, run := w.Get("run").(string)
	if pid <= 1 || pid != float64(int(pid)) || !run {
		return false, true
	}
	return reviewWaitWorkerLive(ctx, w, deadline)
}

func reviewWaitWorkerShape(w ledger.Object) (int, string, bool) {
	pid, _ := w.Get("pid").(float64)
	start, _ := w.Get("start_time").(string)
	run, _ := w.Get("run").(string)
	_, started := reviewWaitDate(w.Get("started_at"))
	_, heartbeat := reviewWaitDate(w.Get("heartbeat_at"))
	return int(pid), start, pid > 1 && pid == float64(int(pid)) && start != "" && !strings.ContainsAny(start, `/\`) && run != "" && started && heartbeat
}

func reviewWaitDate(value any) (time.Time, bool) {
	s, ok := value.(string)
	if !ok {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, s)
	return at, err == nil
}

func reviewWaitWorkerLive(ctx context.Context, w ledger.Object, deadline time.Time) (bool, bool) {
	pid, _ := w.Get("pid").(float64)
	err := syscall.Kill(int(pid), 0)
	if err != nil && err != syscall.EPERM {
		return false, true
	}
	if at, ok := reviewWaitDate(w.Get("heartbeat_at")); ok {
		age := time.Since(at)
		if age <= 60*time.Second && age >= -5*time.Second {
			return true, true
		}
	} else if w.Get("heartbeat_at") != nil {
		// Date.parse accepts additional formats; leave those to Node.
		return false, false
	}
	start, _ := w.Get("start_time").(string)
	if start == "unknown" {
		return true, true
	}
	env := append(os.Environ(), "LC_ALL=C", "LANG=C")
	ps := "/bin/ps"
	if _, err := os.Stat(ps); err != nil {
		ps = "ps"
	}
	// Even a hung process probe cannot extend the requested wait deadline.
	limit := time.Until(deadline)
	if limit <= 0 {
		limit = 200 * time.Millisecond
	}
	if limit > 5*time.Second {
		limit = 5 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	out, ok := reviewWaitCommand(probeCtx, "", ps, []string{"-o", "lstart=", "-p", strconv.Itoa(int(pid))}, env, limit)
	if !ok && probeCtx.Err() == context.DeadlineExceeded {
		return true, true
	}
	return ok && strings.Join(strings.Fields(out), " ") == start, true
}

func reviewWaitLogging() bool {
	flag := os.Getenv("CODE_FORGE_NO_ERROR_LOG")
	return flag == "" || flag == "0"
}

func reviewWaitSay(out ledger.Object) int {
	data, err := ledger.Stringify(out, false)
	if err != nil {
		return 1
	}
	reviewWaitBreadcrumb()
	fmt.Fprintln(os.Stdout, string(data))
	return 0
}

var reviewWaitCrumb = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}( [a-z][a-z0-9-]{0,39})?$`)

// Return the commands before this invocation, as Node's noteVerb does.
func reviewWaitBreadcrumb() []any {
	if !reviewWaitLogging() || os.Getenv("HOME") == "" {
		return []any{}
	}
	dir := filepath.Join(os.Getenv("HOME"), ".code-forge", "logs")
	file := filepath.Join(dir, "breadcrumbs.json")
	before := []any{}
	if data, err := os.ReadFile(file); err == nil {
		_ = json.Unmarshal(data, &before)
	}
	next := []any{}
	for _, v := range before {
		if s, ok := v.(string); ok && reviewWaitCrumb.MatchString(s) {
			next = append(next, s)
		}
	}
	if len(next) > 5 {
		next = next[len(next)-5:]
	}
	previous := append([]any{}, next...)
	next = append(next, "review-file")
	if len(next) > 5 {
		next = next[len(next)-5:]
	}
	if os.MkdirAll(dir, 0700) != nil {
		return previous
	}
	_ = os.Chmod(dir, 0700)
	tmp := file + ".tmp-" + strconv.Itoa(os.Getpid())
	defer os.Remove(tmp)
	data, _ := ledger.Stringify(next, false)
	if os.WriteFile(tmp, append(data, '\n'), 0600) == nil {
		_ = os.Rename(tmp, file)
	}
	return previous
}

func reviewWaitUsageError(args []string, message string) int {
	before := reviewWaitBreadcrumb()
	text := "review-file: " + message + "\n" + reviewWaitUsage
	fmt.Fprint(os.Stderr, text)
	reviewWaitFailure(args, 2, strings.TrimSpace(text), before)
	return 2
}

// The release builder supplies the Node compatibility runtime version without
// launching Node during a native wait. Plain go builds have no Node metadata.
var reviewWaitNodeVersion = "unknown"

var reviewWaitLogFlag = regexp.MustCompile(`^--([A-Za-z][A-Za-z0-9-]{0,39})(=|$)`)
var reviewWaitLogShort = regexp.MustCompile(`^-([A-Za-z])`)
var reviewWaitLogWords = regexp.MustCompile(`\s+`)
var reviewWaitLogQuotes = regexp.MustCompile("\"[^\"\n]*\"|'[^'\n]*'|`[^`\n]*`")
var reviewWaitLogPlaceholders = regexp.MustCompile(`<[^<>\n]*>`)
var reviewWaitLogPaths = regexp.MustCompile(`\S*/\S*`)

// Native failures contain only fixed messages and flag names, never user values
// or stacks. Match buildEntry/appendLine in src/util/error-log.mjs for this shape.
func reviewWaitFailure(args []string, code int, message any, before []any) {
	if !reviewWaitLogging() || os.Getenv("HOME") == "" {
		return
	}
	cleaned := []any{}
	slug := ""
	var config map[string]any
	if cwd, err := os.Getwd(); err == nil {
		if data, err := os.ReadFile(filepath.Join(cwd, ".code-forge.yml")); err == nil && yaml.Unmarshal(data, &config) == nil {
			if project, ok := config["project"].(map[string]any); ok {
				slug, _ = project["slug"].(string)
			}
		}
	}
	slugCount := 0
	clean := func(s string) string {
		if slug == "" {
			return s
		}
		// The fixed fields have no home paths, emails, op refs or item IDs.
		// A configured project slug can still occur in a message or flag name.
		pattern := regexp.MustCompile(regexp.QuoteMeta(slug))
		matches := pattern.FindAllStringIndex(s, -1)
		word := func(c byte) bool {
			return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
		}
		for i := len(matches) - 1; i >= 0; i-- {
			start, end := matches[i][0], matches[i][1]
			if start > 0 && (word(s[start-1]) || s[start-1] == '<') || end < len(s) && (word(s[end]) || s[end] == '>') || strings.HasPrefix(s[end:], "://") {
				continue
			}
			s = s[:start] + "<slug>" + s[end:]
			slugCount++
		}
		return s
	}
	if text, ok := message.(string); ok {
		text = clean(text)
		if len(text) > 2048 {
			end := 2048
			for !utf8.RuneStart(text[end]) {
				end--
			}
			text = text[:end]
		}
		message = text
	}
	flags := []any{}
	afterLong := false
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if m := reviewWaitLogFlag.FindStringSubmatch(arg); m != nil {
			flags = append(flags, clean("--"+m[1]))
			afterLong = m[2] == ""
			continue
		}
		if m := reviewWaitLogShort.FindStringSubmatch(arg); m != nil && !afterLong {
			flags = append(flags, clean("-"+m[1]))
		}
		afterLong = false
	}
	verb := clean("review-file")
	if slugCount > 0 {
		cleaned = append(cleaned, ledger.Object{{Key: "rule", Value: "project_name"}, {Key: "count", Value: slugCount}})
	}
	kind := "error"
	if code == 2 {
		kind = "usage"
	}
	normalized, _ := message.(string)
	normalized = reviewWaitLogQuotes.ReplaceAllString(normalized, " ")
	normalized = reviewWaitLogPlaceholders.ReplaceAllString(normalized, " ")
	normalized = reviewWaitLogPaths.ReplaceAllString(normalized, " ")
	normalized = strings.TrimSpace(reviewWaitLogWords.ReplaceAllString(normalized, " "))
	fp := sha256.Sum256([]byte(verb + "\n\n" + kind + "\n" + normalized))
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	entry := ledger.Object{
		{Key: "ts", Value: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")},
		{Key: "version", Value: version}, {Key: "node", Value: reviewWaitNodeVersion},
		{Key: "platform", Value: runtime.GOOS}, {Key: "arch", Value: arch},
		{Key: "verb", Value: verb}, {Key: "sub", Value: nil}, {Key: "flags", Value: flags},
		{Key: "exit", Value: code}, {Key: "kind", Value: kind}, {Key: "message", Value: message},
		{Key: "stack", Value: nil}, {Key: "cleaned", Value: cleaned},
		{Key: "fp", Value: fmt.Sprintf("%x", fp[:6])}, {Key: "before", Value: before},
	}
	data, err := ledger.Stringify(entry, false)
	if err != nil {
		return
	}
	file := filepath.Join(os.Getenv("HOME"), ".code-forge", "logs", "errors.jsonl")
	if os.MkdirAll(filepath.Dir(file), 0700) != nil {
		return
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	_, err = f.Write(append(data, '\n'))
	_ = f.Close()
	if err != nil {
		return
	}
	info, err := os.Stat(file)
	if err != nil || info.Size() <= 1024*1024 {
		return
	}
	all, err := os.ReadFile(file)
	if err != nil {
		return
	}
	lines := []string{}
	for _, line := range strings.Split(string(all), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	used, start := int64(0), len(lines)
	for start > 0 {
		size := int64(len(lines[start-1]) + 1)
		if start < len(lines) && used+size > info.Size()/2 {
			break
		}
		used += size
		start--
	}
	tmp := file + ".tmp-" + strconv.Itoa(os.Getpid())
	defer os.Remove(tmp)
	if os.WriteFile(tmp, []byte(strings.Join(lines[start:], "\n")+"\n"), 0600) == nil {
		_ = os.Rename(tmp, file)
	}
}
