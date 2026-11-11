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

func reviewWaitHandover(args []string, reason handoverReason) int {
	code, err := handover(append([]string{"review-file"}, args...), reason)
	if err != nil {
		fmt.Fprintln(os.Stderr, "code-forge-native:", err)
	}
	return code
}

func runReviewFileWait(args []string) int {
	return runReviewFileWaitWith(args, reviewWaitLive, reviewWaitSleep)
}

// reviewArgText decodes one argv entry as Node does (WHATWG UTF-8): each
// maximal subpart of an ill-formed sequence becomes one U+FFFD.
func reviewArgText(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if r != utf8.RuneError || n > 1 {
			b.WriteString(s[i : i+n])
			i += n
			continue
		}
		c, need, lo, hi := s[i], 0, byte(0x80), byte(0xbf)
		switch {
		case c >= 0xc2 && c <= 0xdf:
			need = 1
		case c == 0xe0:
			need, lo = 2, 0xa0
		case c == 0xed:
			need, hi = 2, 0x9f
		case c >= 0xe1 && c <= 0xef:
			need = 2
		case c == 0xf0:
			need, lo = 3, 0x90
		case c == 0xf4:
			need, hi = 3, 0x8f
		case c >= 0xf1 && c <= 0xf3:
			need = 3
		}
		j := i + 1
		for k := 0; k < need && j < len(s) && s[j] >= lo && s[j] <= hi; k++ {
			lo, hi = 0x80, 0xbf
			j++
		}
		b.WriteRune(utf8.RuneError)
		i = j
	}
	return b.String()
}

// reviewParseMax is parseMax: 90s, 90 or 1500ms.
func reviewParseMax(raw string, exists bool) (time.Duration, bool) {
	if !exists {
		return 90 * time.Second, true
	}
	match := reviewWaitMax.FindStringSubmatch(raw)
	if match == nil {
		return 0, false
	}
	n, _ := strconv.ParseInt(match[1], 10, 64)
	if match[2] == "ms" {
		return time.Duration(n) * time.Millisecond, true
	}
	return time.Duration(n) * time.Second, true
}

// runReviewFileWaitWith is runReviewFile's dispatch: one parser, then --cwd,
// the main tree, --rebase-check, --wait-block, --wait and enqueue in Node's
// order with Node's usage text, refusals and exit codes. Polling and sleeping
// stay injectable so tests can check the down-poll count and the delays.
func runReviewFileWaitWith(args []string, poll func(context.Context, string, time.Time) (bool, bool), sleep func(context.Context, time.Duration) bool) int {
	decoded := make([]string, len(args))
	for i, arg := range args {
		decoded[i] = reviewArgText(arg)
	}
	args = decoded
	// The router prints a verb's help before any breadcrumb or log.
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprint(os.Stdout, reviewWaitUsage)
		return 0
	}
	flags, paths, usage := reviewEnqueueFlags(args)
	if usage != "" {
		// The wait logger handles fixed usage text and ordinary flag names.
		// Unknown flags containing arbitrary values need Node's full scrubber.
		if reviewWaitLogging() && strings.HasPrefix(usage, "unknown flag ") {
			var arg string
			_ = json.Unmarshal([]byte(strings.TrimPrefix(usage, "unknown flag ")), &arg)
			if arg != "--" && !reviewEnqueueSafeUnknown.MatchString(arg) {
				return reviewWaitHandover(args, hEnqueueFlagRedaction)
			}
		}
		return reviewWaitUsageError(args, usage)
	}
	cwd, err := os.Getwd()
	if err != nil {
		// Node fails before the verb runs (process.cwd() throws): exit 1.
		fmt.Fprintln(os.Stderr, "review-file: the current directory cannot be read:", err)
		return 1
	}
	if dir, exists := flags["cwd"]; exists {
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(cwd, dir)
		}
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			return reviewWaitUsageError(args, "--cwd must name an existing directory")
		}
		cwd = filepath.Clean(dir)
	}
	// A ticket wait answers SIGINT/SIGTERM with 130/143 from its first git
	// child on, so root discovery already runs under the signal context.
	ctx := context.Background()
	var interrupted func() int
	_, waiting := flags["wait"]
	_, joining := flags["wait-block"]
	if waiting && !joining && flags["rebase-check"] != "true" {
		var stop func()
		ctx, interrupted, stop = reviewWaitSignals()
		defer stop()
	}
	root, refusal, ok := reviewMainRoot(ctx, cwd)
	if ctx.Err() != nil {
		return interrupted()
	}
	if !ok {
		return reviewWaitHandover(args, hReviewRoot)
	}
	if refusal != nil {
		return reviewEnqueueSay(args, refusal, 1)
	}
	if flags["rebase-check"] == "true" {
		return reviewEnqueueRebase(args, flags, paths, root)
	}
	if _, exists := flags["wait-block"]; exists {
		return reviewWaitBlockDirect(args, flags, paths, cwd, root)
	}
	if _, exists := flags["wait"]; !exists {
		return reviewEnqueueWith(args, nil, &reviewFileScope{flags: flags, paths: paths, cwd: cwd, root: root})
	}
	for _, key := range []string{"block", "answers", "of"} {
		if _, exists := flags[key]; exists {
			return reviewWaitUsageError(args, "--wait takes only a ticket and --max")
		}
	}
	if len(paths) > 0 || flags["fresh"] == "true" {
		return reviewWaitUsageError(args, "--wait takes only a ticket and --max")
	}
	if !reviewWaitTicket.MatchString(flags["wait"]) {
		return reviewWaitUsageError(args, "a ticket id is 24 lowercase hex characters")
	}
	maxWait, valid := reviewParseMax(flags["max"], reviewHas(flags, "max"))
	if !valid {
		return reviewWaitUsageError(args, "--max must look like 90s or 1500ms")
	}
	return reviewWaitTicketLoop(ctx, interrupted, args, flags["wait"], root, maxWait, poll, sleep)
}

// reviewWaitSignals cancels the returned context on SIGINT or SIGTERM;
// interrupted then reports the conventional 128+signal exit code.
func reviewWaitSignals() (context.Context, func() int, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	var code atomic.Int32
	stopped := make(chan struct{})
	go func() {
		select {
		case sig := <-signals:
			code.Store(int32(128 + sig.(syscall.Signal)))
			cancel()
		case <-stopped:
		}
	}()
	return ctx, func() int { return int(code.Load()) }, func() { signal.Stop(signals); close(stopped); cancel() }
}

func reviewHas(flags map[string]string, key string) bool {
	_, exists := flags[key]
	return exists
}

func reviewWaitTicketLoop(ctx context.Context, interrupted func() int, args []string, ticket, root string, maxWait time.Duration, poll func(context.Context, string, time.Time) (bool, bool), sleep func(context.Context, time.Duration) bool) int {
	deadline := time.Now().Add(maxWait)
	marker := filepath.Join(root, ".code-forge", "queue", ticket+".done")
	down := 0
	for {
		if ctx.Err() != nil {
			return interrupted()
		}
		if _, err := os.Stat(marker); err == nil {
			result, matched := reviewWaitResult(root, marker)
			if !matched {
				return reviewWaitHandover(args, hWaitResult)
			}
			out := ledger.Object{{Key: "ticket", Value: ticket}, {Key: "status", Value: "done"}, {Key: "result", Value: result}}
			if !reviewWaitSummary(&out, result) {
				return reviewWaitHandover(args, hWaitSummary)
			}
			return reviewWaitSay(out)
		}
		live, matched := poll(ctx, root, deadline)
		if ctx.Err() != nil {
			return interrupted()
		}
		if !matched {
			return reviewWaitHandover(args, hWaitWorker)
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
		if !sleep(ctx, pause) {
			return interrupted()
		}
	}
}

func reviewWaitSleep(ctx context.Context, pause time.Duration) bool {
	timer := time.NewTimer(pause)
	select {
	case <-ctx.Done():
		timer.Stop()
		return false
	case <-timer.C:
		return true
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
	out, _, ok := reviewWaitCommandErr(ctx, cwd, name, args, env, limit)
	return out, ok
}

func reviewWaitCommandErr(ctx context.Context, cwd, name string, args []string, env []string, limit time.Duration) (string, string, bool) {
	childCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	cmd := exec.CommandContext(childCtx, name, args...)
	cmd.Dir, cmd.Env = cwd, env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 50 * time.Millisecond
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if cmd.Process != nil {
		// A helper could exit before descendants close their inherited pipes.
		// WaitDelay bounds the pipe wait; retire the group in that case too.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return out.String(), errOut.String(), err == nil
}

func reviewWaitRoot(ctx context.Context, cwd string) (string, bool) {
	root, refusal, ok := reviewMainRoot(ctx, cwd)
	return root, ok && refusal == nil
}

// reviewMainRoot is mainRootOf in src/worker/ticket.mjs: the realpath of the
// main working tree, or Node's refusal for it. ok=false: a realpath of git's
// own top level failed, which Node reports with a raw error.
func reviewMainRoot(ctx context.Context, cwd string) (string, ledger.Object, bool) {
	env := []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			env = append(env, entry)
		}
	}
	env = append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	git := func(args ...string) (string, string, bool) {
		return reviewWaitCommandErr(ctx, cwd, "git", args, env, 10*time.Second)
	}
	refuse := func(code, message string) (string, ledger.Object, bool) {
		return "", reviewEnqueueRefusal(code, message), true
	}
	top, _, ok := git("rev-parse", "--show-toplevel")
	top = strings.TrimSpace(top)
	if !ok || top == "" {
		return refuse("no-repo", "not inside a git repository")
	}
	top, err := filepath.EvalSymlinks(top)
	if err != nil {
		return "", nil, false
	}
	dirs, stderr, ok := git("rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir")
	if !ok {
		return refuse("no-repo", "not inside a git repository, or git is older than 2.36 (code-forge needs git >= 2.36): "+strings.TrimSpace(stderr))
	}
	parts := strings.Split(dirs, "\n")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return refuse("no-repo", "git rev-parse printed no git directory")
	}
	real := func(s string) string {
		if p, err := filepath.EvalSymlinks(s); err == nil {
			return p
		}
		return filepath.Clean(s)
	}
	if real(parts[0]) == real(parts[1]) {
		return top, nil, true
	}
	list, stderr, ok := git("worktree", "list", "--porcelain", "-z")
	if !ok {
		return refuse("no-repo", "the main working tree of the repository cannot be listed (code-forge needs git >= 2.36): "+strings.TrimSpace(stderr))
	}
	first := strings.Split(strings.Split(list, "\x00\x00")[0], "\x00")
	main, bare := "", false
	for _, field := range first {
		if field == "bare" {
			bare = true
		}
		if strings.HasPrefix(field, "worktree ") && main == "" {
			main = strings.TrimPrefix(field, "worktree ")
		}
	}
	if main == "" {
		return refuse("no-repo", "git worktree list printed no main working tree")
	}
	if bare {
		return refuse("bare-main", "the repository's main entry "+main+" is bare: it has no main working tree for the review queue")
	}
	resolved, err := filepath.EvalSymlinks(main)
	if err != nil {
		return refuse("no-main-tree", "the repository's main working tree "+main+" does not exist (moved or removed): the review queue lives there")
	}
	return resolved, nil, true
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
