package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/filelock"
	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/humanask"
	"github.com/ricardov03/code-forge/native/ledger"
)

const toolsUsage = "usage: code-forge tools [--json] | tools install [<id>...] [--yes] [--dry-run]\n  (install asks one yes, default no; answering no prints \"cancelled — nothing installed\" and exits 1)\n"

func init() {
	routes["tools"] = smallVerb{
		name: "tools", usage: toolsUsage, subs: []string{"install"},
		subUsage: map[string]string{
			"install": "usage: code-forge tools install [<id>...] [--yes] [--dry-run]\n  (install asks one yes, default no; answering no prints \"cancelled — nothing installed\" and exits 1)\n",
		},
		prepare: prepareToolsRegistered,
	}.run
}

// prepareToolsRegistered is prepareTools with exec's pid registry (B563):
// every version probe and install child is recorded in this process's lazy
// run root (logs report's lrRegisterChild, src/util/exec.mjs + reaper.mjs)
// while it runs, and the root is released once its children have settled
// (tmp.mjs releaseLazyRoot at exit). The tmp root comes from the config
// first, as bin/code-forge.mjs applies it before any verb runs.
func prepareToolsRegistered(args []string) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
	contextApplyTmpRoot()
	lrRoot = ""
	body, reason, ok := prepareTools(args)
	toolsReleaseRoot()
	if body == nil {
		return body, reason, ok
	}
	return func(stdout, stderr io.Writer) int {
		defer toolsReleaseRoot()
		return body(stdout, stderr)
	}, reason, ok
}

// toolsExec is exec in src/util/exec.mjs for the tools rows: runChild with
// the pid registry hook.
func toolsExec(argv []string, timeout time.Duration) childResult {
	return runChildHooked(argv, timeout, lrRegisterChild)
}

// toolsReleaseRoot is releaseLazyRoot: the lazy run root goes once no entry
// is left in its registry; a root kept stays this process's root.
func toolsReleaseRoot() {
	if lrRoot == "" {
		return
	}
	spawnReleaseRoot(lrRoot)
	if _, err := os.Lstat(lrRoot); os.IsNotExist(err) {
		lrRoot = ""
	}
}

// toolRow is one row of TOOLS in src/install/tools.mjs.
type toolRow struct {
	id, command, npm string
	brew             []string
	manual, app      string
	note             string
}

var toolRows = []toolRow{
	{"claude", "claude", "@anthropic-ai/claude-code", nil, "", "", ""},
	{"codex", "codex", "@openai/codex", nil, "", "", ""},
	{"gemini", "gemini", "@google/gemini-cli", nil, "", "", ""},
	{"grok", "grok", "", []string{"--cask", "grok-build"}, "https://x.ai/build", "", ""},
	{"op", "op", "", []string{"1password-cli"}, "https://developer.1password.com/docs/cli/get-started/", "", `turn on 1Password app → Settings → Developer → "Integrate with 1Password CLI"`},
	{"solo", "solo", "", nil, "https://soloterm.com (desktop app, then add its MCP entry)", "Solo.app", ""},
}

func toolIDs() []string {
	ids := make([]string, len(toolRows))
	for i, t := range toolRows {
		ids[i] = t.id
	}
	return ids
}

// toolSystemApps is the machine-wide app folder detectTool looks in (macOS).
// Tests point it at a missing folder so the rows never depend on the apps
// this machine has (B552: Solo.app on a Mac, none on Linux CI).
var toolSystemApps = "/Applications"

// toolOS is the platform the tool rows and install hints follow (Node's
// process.platform). Tests pin it so Mac goldens hold on Linux CI (B560).
var toolOS = runtime.GOOS

// detectTool is detectTool: the command on PATH, or (macOS) its app bundle.
func detectTool(tool toolRow) bool {
	if commandOnPath(tool.command, os.Getenv("PATH")) {
		return true
	}
	if tool.app == "" || toolOS != "darwin" {
		return false
	}
	places := []string{filepath.Join(toolSystemApps, tool.app)}
	if home := os.Getenv("HOME"); home != "" {
		places = append(places, filepath.Join(home, "Applications", tool.app))
	}
	for _, p := range places {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// toolInstallPlan is installPlan: an argv to run, else a manual hint.
func toolInstallPlan(tool toolRow) (argv []string, hint string) {
	path := os.Getenv("PATH")
	if tool.npm != "" {
		argv = []string{"npm", "install", "-g", tool.npm}
		if commandOnPath("npm", path) {
			return argv, ""
		}
		return nil, "install Node.js/npm first, then: " + strings.Join(argv, " ")
	}
	if tool.brew != nil && toolOS == "darwin" {
		if commandOnPath("brew", path) {
			return append([]string{"brew", "install"}, tool.brew...), ""
		}
		return nil, "install Homebrew first: https://brew.sh"
	}
	if tool.manual == "" {
		return nil, "no install known"
	}
	return nil, tool.manual
}

// childResult is what src/util/exec.mjs resolves with, as far as the tools
// verb reads it.
type childResult struct {
	ok, timedOut   bool
	code           *int
	signal         string
	stdout, stderr string
}

const childMaxBuffer = 10 * 1024 * 1024

type cappedBuffer struct {
	mu   sync.Mutex
	data []byte
	over bool
	kill func()
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.over {
		return len(p), nil
	}
	if room := childMaxBuffer - len(b.data); len(p) > room {
		b.data = append(b.data, p[:room]...)
		b.over = true
		b.kill()
		return len(p), nil
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

// runChild is exec in src/util/exec.mjs for argv with no stdin: its own
// process group, the owner variables, SIGTERM then SIGKILL on timeout,
// remaining group members stopped once the leader exits.
func runChild(argv []string, timeout time.Duration) childResult {
	return runChildHooked(argv, timeout, nil)
}

// runChildHooked is runChild with exec's spawn hook: started runs with the
// child's pid right after a successful spawn, and the function it returns
// (when not nil) runs once the child has settled: leader reaped, remaining
// group members stopped, output drained (exec.mjs settle).
func runChildHooked(argv []string, timeout time.Duration, started func(pid int, argv0 string) func()) childResult {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CODE_FORGE_OWNER_PID=") && !strings.HasPrefix(kv, "CODE_FORGE_OWNER_START_TIME=") {
			env = append(env, kv)
		}
	}
	env = append(env, "CODE_FORGE_OWNER_PID="+strconv.Itoa(os.Getpid()))
	if start := filelock.ProcessStartTime(os.Getpid()); start != nil {
		env = append(env, "CODE_FORGE_OWNER_START_TIME="+*start)
	}
	cmd.Env = env
	kill := func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}
	stdout, stderr := &cappedBuffer{kill: kill}, &cappedBuffer{kill: kill}
	outRead, outWrite, err1 := os.Pipe()
	errRead, errWrite, err2 := os.Pipe()
	if err1 != nil || err2 != nil {
		return childResult{}
	}
	defer outRead.Close()
	defer errRead.Close()
	cmd.Stdout, cmd.Stderr = outWrite, errWrite
	var drains sync.WaitGroup
	drains.Add(2)
	go func() { defer drains.Done(); _, _ = io.Copy(stdout, outRead) }()
	go func() { defer drains.Done(); _, _ = io.Copy(stderr, errRead) }()
	err := cmd.Start()
	outWrite.Close()
	errWrite.Close()
	if err != nil {
		drains.Wait()
		return childResult{}
	}
	if started != nil {
		if settled := started(cmd.Process.Pid, argv[0]); settled != nil {
			defer settled()
		}
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	timedOut := false
	timer := time.NewTimer(timeout)
	select {
	case <-done:
	case <-timer.C:
		timedOut = true
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			kill()
			<-done
		}
	}
	timer.Stop()
	gates.StopRemaining(cmd.Process.Pid)
	drains.Wait()
	result := childResult{timedOut: timedOut, stdout: string(stdout.data), stderr: string(stderr.data)}
	status := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if status.Signaled() {
		result.signal = gates.SignalName(status.Signal())
	} else {
		code := status.ExitStatus()
		result.code = &code
	}
	result.ok = !timedOut && !stdout.over && !stderr.over && result.code != nil && *result.code == 0
	return result
}

// jsSlice16 is String.prototype.slice(0, n) on UTF-16 code units; ok is
// false when the cut splits a surrogate pair.
func jsSlice16(s string, n int) (string, bool) {
	units := utf16.Encode([]rune(s))
	if len(units) <= n {
		return s, true
	}
	if utf16.IsSurrogate(rune(units[n-1])) && units[n-1] < 0xdc00 {
		return "", false
	}
	return string(utf16.Decode(units[:n])), true
}

// toolVersion is toolVersion: the first non-empty trimmed line of stdout,
// else of stderr, cut to 120 UTF-16 units; ok is false when that cut splits
// a surrogate pair (Node then prints a lone surrogate).
func toolVersion(tool toolRow) (version string, found, ok bool) {
	res := toolsExec([]string{tool.command, "--version"}, 10*time.Second)
	if !res.ok {
		return "", false, true
	}
	first := func(text string) string {
		for _, line := range strings.Split(text, "\n") {
			if line = jsTrim(line); line != "" {
				return line
			}
		}
		return ""
	}
	line := first(nodeUTF8(res.stdout))
	if line == "" {
		line = first(nodeUTF8(res.stderr))
	}
	if line == "" {
		return "", false, true
	}
	cut, ok := jsSlice16(line, 120)
	return cut, ok, ok
}

func jsTrim(s string) string {
	return strings.Trim(s, "\t\n\v\f\r \u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000\ufeff")
}

// toolsFlags is parseFlags with only boolean flags: the flags given, the
// positionals, and Node's usage message for a bad flag.
func toolsFlags(args []string, booleans ...string) (map[string]bool, []string, string) {
	flags := map[string]bool{}
	var positionals []string
	for _, arg := range args {
		if !strings.HasPrefix(arg, "--") {
			positionals = append(positionals, arg)
			continue
		}
		name, _, hasValue := strings.Cut(arg[2:], "=")
		known := false
		for _, b := range booleans {
			known = known || b == name
		}
		switch {
		case !known:
			return nil, nil, "unknown flag " + jsQuote(arg)
		case flags[name]:
			return nil, nil, "--" + name + " given more than once"
		case hasValue:
			return nil, nil, "--" + name + " takes no value"
		}
		flags[name] = true
	}
	return flags, positionals, ""
}

func padEnd(s string, n int) string {
	if count := utf8.RuneCountInString(s); count < n {
		return s + strings.Repeat(" ", n-count)
	}
	return s
}

func prepareTools(args []string) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
	usageError := func(message string) func(_, stderr io.Writer) int {
		return func(_, stderr io.Writer) int {
			fmt.Fprintf(stderr, "tools: %s\n%s", message, toolsUsage)
			return 2
		}
	}
	if len(args) > 0 && args[0] == "install" {
		return prepareToolsInstall(args[1:], usageError)
	}
	flags, positionals, problem := toolsFlags(args, "json")
	if problem != "" {
		return usageError(problem), 0, true
	}
	if len(positionals) > 0 {
		return usageError("unknown subcommand " + jsQuote(positionals[0])), 0, true
	}
	statuses := []ledger.Object{}
	for _, tool := range toolRows {
		if detectTool(tool) {
			version, found, ok := toolVersion(tool)
			if !ok {
				return nil, hToolsUnsupported, false
			}
			var v, note any
			if found {
				v = version
			}
			if tool.note != "" {
				note = tool.note
			}
			statuses = append(statuses, ledger.Object{{Key: "id", Value: tool.id}, {Key: "status", Value: "installed"}, {Key: "version", Value: v},
				{Key: "install", Value: nil}, {Key: "hint", Value: nil}, {Key: "note", Value: note}})
			continue
		}
		argv, hint := toolInstallPlan(tool)
		var install, h any
		if argv != nil {
			list := []any{}
			for _, a := range argv {
				list = append(list, a)
			}
			install = list
		} else {
			h = hint
		}
		statuses = append(statuses, ledger.Object{{Key: "id", Value: tool.id}, {Key: "status", Value: "missing"}, {Key: "version", Value: nil},
			{Key: "install", Value: install}, {Key: "hint", Value: h}, {Key: "note", Value: nil}})
	}
	return func(stdout, _ io.Writer) int {
		var b strings.Builder
		if flags["json"] {
			list := make([]any, len(statuses))
			for i, s := range statuses {
				list[i] = s
			}
			out, _ := ledger.Stringify(ledger.Object{{Key: "tools", Value: list}}, false)
			b.Write(out)
			b.WriteString("\n")
		} else {
			for _, s := range statuses {
				id := padEnd(s.Get("id").(string), 7)
				if s.Get("status") == "installed" {
					version := "version unknown"
					if v, ok := s.Get("version").(string); ok {
						version = v
					}
					note := ""
					if n, ok := s.Get("note").(string); ok {
						note = " — " + n
					}
					fmt.Fprintf(&b, "%s installed  %s%s\n", id, version, note)
				} else if install, ok := s.Get("install").([]any); ok {
					parts := make([]string, len(install))
					for i, a := range install {
						parts[i] = a.(string)
					}
					fmt.Fprintf(&b, "%s missing    install: %s\n", id, strings.Join(parts, " "))
				} else {
					fmt.Fprintf(&b, "%s missing    manual: %s\n", id, s.Get("hint").(string))
				}
			}
		}
		fmt.Fprint(stdout, b.String())
		return 0
	}, 0, true
}

func prepareToolsInstall(args []string, usageError func(string) func(_, stderr io.Writer) int) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
	flags, positionals, problem := toolsFlags(args, "yes", "dry-run")
	if problem != "" {
		return usageError(problem), 0, true
	}
	var ids, unknown []string
	seen := map[string]bool{}
	valid := strings.Join(toolIDs(), ", ")
	for _, id := range positionals {
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
		if !slices.Contains(toolIDs(), id) {
			unknown = append(unknown, id)
		}
	}
	if len(unknown) > 0 {
		return usageError("unknown tool id(s): " + strings.Join(unknown, ", ") + " — valid ids: " + valid), 0, true
	}
	named := len(ids) > 0
	type run struct {
		id   string
		argv []string
	}
	type row struct{ id, result string }
	var table []row
	var runs []run
	var manual []row
	for _, tool := range toolRows {
		if named && !seen[tool.id] {
			continue
		}
		if detectTool(tool) {
			if named {
				table = append(table, row{tool.id, "already installed"})
			}
			continue
		}
		argv, hint := toolInstallPlan(tool)
		if argv != nil {
			runs = append(runs, run{tool.id, argv})
		} else {
			manual = append(manual, row{tool.id, hint})
		}
	}
	if len(runs) > 0 && !flags["yes"] && !flags["dry-run"] && toolsStdinTTY() {
		return nil, hToolsConfirm, false
	}
	printTable := func(b *strings.Builder) {
		for _, t := range table {
			fmt.Fprintf(b, "%s %s\n", padEnd(t.id, 7), t.result)
		}
	}
	return func(stdout, stderr io.Writer) int {
		var b strings.Builder
		flush := func() { fmt.Fprint(stdout, b.String()); b.Reset() }
		if len(runs) == 0 && len(manual) == 0 {
			if !named {
				b.WriteString("all recommended tools are installed\n")
			}
			printTable(&b)
			flush()
			return 0
		}
		b.WriteString("plan:\n")
		for _, r := range runs {
			fmt.Fprintf(&b, "  %s: %s\n", r.id, strings.Join(r.argv, " "))
		}
		for _, m := range manual {
			fmt.Fprintf(&b, "  %s: manual — %s\n", m.id, m.result)
		}
		if flags["dry-run"] {
			b.WriteString("dry run: nothing installed\n")
			flush()
			return 0
		}
		flush()
		if len(runs) > 0 && !flags["yes"] {
			fmt.Fprint(stderr, "tools: no terminal to confirm; nothing installed — pass --yes to install the plan above\n")
			return 2
		}
		failed := 0
		for _, r := range runs {
			fmt.Fprintf(stdout, "\n$ %s\n", strings.Join(r.argv, " "))
			res := toolsExec(r.argv, 600*time.Second)
			if res.stdout != "" {
				fmt.Fprint(stdout, ensureNewline(nodeUTF8(res.stdout)))
			}
			if res.stderr != "" {
				fmt.Fprint(stderr, ensureNewline(nodeUTF8(res.stderr)))
			}
			if res.ok {
				result := "installed (not on PATH yet — open a new shell)"
				for _, tool := range toolRows {
					if tool.id == r.id && detectTool(tool) {
						result = "installed"
					}
				}
				table = append(table, row{r.id, result})
				continue
			}
			failed++
			reason := "did not start"
			switch {
			case res.timedOut:
				reason = "timed out"
			case res.signal != "":
				reason = "signal " + res.signal
			case res.code != nil:
				reason = "exit " + strconv.Itoa(*res.code)
			}
			table = append(table, row{r.id, "failed (" + reason + ")"})
		}
		for _, m := range manual {
			table = append(table, row{m.id, "skipped: manual — " + m.result})
		}
		b.WriteString("\n")
		printTable(&b)
		flush()
		if failed > 0 {
			return 1
		}
		return 0
	}, 0, true
}

// toolsStdinTTY is process.stdin.isTTY; tests stand in for a terminal.
var toolsStdinTTY = func() bool { return humanask.IsTTY(os.Stdin) }

func ensureNewline(s string) string {
	if strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}

// nodeUTF8 is Buffer.toString('utf8'): each maximal invalid subpart of a
// sequence becomes one U+FFFD, as the WHATWG decoder does.
func nodeUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r != utf8.RuneError || size > 1 {
			b.WriteString(s[i : i+size])
			i += size
			continue
		}
		b.WriteRune(utf8.RuneError)
		i += 1 + validTail(s[i:])
	}
	return b.String()
}

// validTail is how many continuation bytes after s[0] still fit a well-formed
// sequence that lead byte starts (Unicode table 3-7).
func validTail(s string) int {
	lo, hi, need := byte(0x80), byte(0xbf), 0
	switch c := s[0]; {
	case c >= 0xc2 && c <= 0xdf:
		need = 1
	case c == 0xe0:
		lo, need = 0xa0, 2
	case c == 0xed:
		hi, need = 0x9f, 2
	case c >= 0xe1 && c <= 0xef:
		need = 2
	case c == 0xf0:
		lo, need = 0x90, 3
	case c == 0xf4:
		hi, need = 0x8f, 3
	case c >= 0xf1 && c <= 0xf3:
		need = 3
	}
	n := 0
	for n < need && 1+n < len(s) && s[1+n] >= lo && s[1+n] <= hi {
		n++
		lo, hi = 0x80, 0xbf
	}
	return n
}
