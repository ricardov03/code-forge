package gates

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/ricardov03/code-forge/native/filelock"
	"github.com/ricardov03/code-forge/native/ledger"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
)

func merge(o *ledger.Object, more ledger.Object) {
	for _, f := range more {
		o.Set(f.Key, f.Value)
	}
}
func Masks(p Plan) []string {
	out := []string{}
	for _, name := range Order {
		if c := p.Commands[name]; c != nil {
			keys := []string{}
			for k := range c.Env {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				v := c.Env[k]
				if len(utf16.Encode([]rune(v))) >= 8 {
					out = append(out, v)
				}
			}
		}
	}
	for _, c := range p.Custom {
		for _, v := range c.Command.Env {
			if len(utf16.Encode([]rune(v))) >= 8 {
				out = append(out, v)
			}
		}
	}
	return out
}
func Mask(s string, secrets []string) string {
	type span struct{ a, b int }
	ranges := []span{}
	for _, v := range secrets {
		for start := 0; start < len(s); {
			i := strings.Index(s[start:], v)
			if i < 0 {
				break
			}
			i += start
			ranges = append(ranges, span{i, i + len(v)})
			start = i + 1
		}
	}
	if len(ranges) == 0 {
		return s
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].a < ranges[j].a })
	out := ""
	cursor := 0
	a, b := ranges[0].a, ranges[0].b
	for _, r := range ranges[1:] {
		if r.a <= b {
			if r.b > b {
				b = r.b
			}
			continue
		}
		out += s[cursor:a] + "[REDACTED]"
		cursor = b
		a, b = r.a, r.b
	}
	return out + s[cursor:a] + "[REDACTED]" + s[b:]
}
func Redact(v any, secrets []string) any {
	switch v := v.(type) {
	case string:
		return Mask(v, secrets)
	case ledger.Object:
		o := ledger.Object{}
		for _, f := range v {
			k := Mask(f.Key, secrets)
			candidate := k
			for n := 2; hasKey(o, candidate); n++ {
				candidate = k + "#" + strconv.Itoa(n)
			}
			o.Set(candidate, Redact(f.Value, secrets))
		}
		return o
	case []any:
		a := []any{}
		for _, e := range v {
			a = append(a, Redact(e, secrets))
		}
		return a
	case []string:
		a := []string{}
		for _, s := range v {
			a = append(a, Mask(s, secrets))
		}
		return a
	}
	return v
}
func hasKey(o ledger.Object, k string) bool {
	for _, f := range o {
		if f.Key == k {
			return true
		}
	}
	return false
}

type planned struct {
	name   string
	c      *Command
	argv   []string
	scope  ledger.Object
	reason string
}

func Scoped(p Plan) bool {
	for _, c := range p.Commands {
		if c != nil && (Has(c.Argv, "{test_files}") || Has(c.Argv, "{changed_files}")) {
			return true
		}
	}
	return false
}
func prepare(p Plan, s Scope) ([]planned, error) {
	out := []planned{}
	for _, name := range Order {
		c := p.Commands[name]
		item := planned{name: name, c: c}
		if c == nil || (p.Restricted && !Has(p.Names, name)) {
			if c != nil {
				item.reason = "not in gates.kinds." + p.Kind
			}
			item.c = nil
			out = append(out, item)
			continue
		}
		if c.Argv[0] == "{changed_files}" {
			return nil, fmt.Errorf("gates.%s: '{changed_files}' cannot be the command (argv[0])", name)
		}
		scoped := Has(c.Argv, "{test_files}") || Has(c.Argv, "{changed_files}")
		files := s.Files
		if s.Kind == "full" {
			files = []string{}
		}
		for _, f := range files {
			if strings.HasPrefix(f, "-") {
				return nil, fmt.Errorf("expandArgv: test path %q starts with \"-\"", f)
			}
		}
		runner := RunnerFiles(c.Argv, files)
		item.argv = []string{}
		for _, a := range c.Argv {
			switch a {
			case "{test_files}":
				item.argv = append(item.argv, runner...)
			case "{changed_files}":
				if s.Kind == "changed" {
					for _, f := range s.Changed {
						if !SafeFile(f) {
							return nil, fmt.Errorf("expandArgv: unsafe changed path")
						}
					}
					item.argv = append(item.argv, s.Changed...)
				}
			default:
				item.argv = append(item.argv, a)
			}
		}
		if len(item.argv) == 0 {
			quoted := []string{}
			for _, a := range c.Argv {
				quoted = append(quoted, "'"+a+"'")
			}
			return nil, fmt.Errorf("gates.%s: the argv is empty once %s is dropped for the full suite — put the test command before it", name, strings.Join(quoted, " and "))
		}
		if scoped {
			cs := s
			if Has(c.Argv, "{test_files}") && len(files) > 0 && len(runner) == 0 {
				cs = Scope{Kind: "full", Reason: "runner-filter"}
			} else if s.Kind != "full" && Has(c.Argv, "{test_files}") {
				cs.Files = runner
			}
			item.scope = cs.Fields(true)
		}
		out = append(out, item)
	}
	return out, nil
}

// boundedBuffer enforces exec's independent 10 MiB stream limits.
type boundedBuffer struct {
	sync.Mutex
	b    bytes.Buffer
	kill func()
	over bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	n := len(p)
	room := 10*1024*1024 - b.b.Len()
	if n > room {
		b.b.Write(p[:room])
		b.over = true
		b.kill()
	} else {
		b.b.Write(p)
	}
	return n, nil
}
func (b *boundedBuffer) text() string {
	b.Lock()
	defer b.Unlock()
	return strings.ToValidUTF8(b.b.String(), "�")
}
func num(n float64) string { return strconv.FormatFloat(n, 'f', -1, 64) }
func trim(s string) string {
	return strings.TrimRight(s, "\t\n\v\f\r \u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000\ufeff")
}

var prompt = regexp.MustCompile(`(?i)(\?|\(y(/n)?\)|\[y/n\]|yes/no)\s*$`)

func tails(stdout, stderr string) string {
	out := []string{}
	for i, s := range []string{stdout, stderr} {
		s = trim(s)
		if s == "" {
			continue
		}
		lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
		if len(lines) > 10 {
			lines = lines[len(lines)-10:]
		}
		name := "stdout"
		if i == 1 {
			name = "stderr"
		}
		out = append(out, name+" tail:\n"+strings.Join(lines, "\n"))
	}
	return strings.Join(out, "\n")
}

// Run executes every built-in gate independently, reaping the process group on
// deadline and cancellation. No shell interprets configured argv.
func Run(ctx context.Context, p Plan, cwd, run string, timeout float64, s Scope) ([]any, bool, error) {
	if p.Light != nil {
		s = Scope{Kind: "related", Files: p.Light}
	}
	planned, e := prepare(p, s)
	if e != nil {
		return nil, false, e
	}
	results := []any{}
	all := true
	for _, item := range planned {
		if ctx.Err() != nil {
			break
		}
		r := ledger.Object{{Key: "gate", Value: item.name}, {Key: "duration_ms", Value: 0}, {Key: "argv", Value: nil}, {Key: "skipped", Value: true}, {Key: "ok", Value: true}, {Key: "code", Value: nil}, {Key: "stdout", Value: ""}, {Key: "stderr", Value: ""}}
		if item.c == nil {
			if item.reason != "" {
				r.Set("reason", item.reason)
			}
			results = append(results, r)
			continue
		}
		c := item.c
		if item.name == "test" && p.ReuseTest {
			r = ledger.Object{{Key: "gate", Value: item.name}, {Key: "duration_ms", Value: int64(0)}, {Key: "argv", Value: item.argv}, {Key: "skipped", Value: false}, {Key: "ok", Value: true}, {Key: "code", Value: 0}, {Key: "stdout", Value: ""}, {Key: "stderr", Value: ""}, {Key: "scope", Value: "full"}, {Key: "reused", Value: true}, {Key: "reuse_reason", Value: p.ReuseReason}}
			if reason := item.scope.Get("reason"); reason != nil {
				r.Set("reason", reason)
			}
			results = append(results, r)
			continue
		}
		r = ledger.Object{{Key: "gate", Value: item.name}, {Key: "duration_ms", Value: int64(0)}, {Key: "argv", Value: item.argv}, {Key: "skipped", Value: false}}
		merge(&r, item.scope)
		started := time.Now()
		outcome := execute(ctx, item.argv, c.Env, cwd, run, timeout, c.FailOnStdout)
		merge(&r, outcome)
		if item.name == "test" && p.ReuseReason != "" {
			r.Set("reuse_reason", p.ReuseReason)
		}
		if len(c.Env) > 0 {
			keys := []string{}
			for k := range c.Env {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			r.Set("env_keys", keys)
		}
		if item.name == "test" && r.Get("ok") != true && r.Get("code") != nil && outcome.Get("reason") == nil && p.Rerun && ctx.Err() == nil {
			files := failedTestFiles(fmt.Sprint(r.Get("stdout"))+"\n"+fmt.Sprint(r.Get("stderr")), cwd)
			reruns := [][]string{}
			for _, f := range files {
				a := failedFileArgv(c.Argv, f)
				if a == nil {
					reruns = nil
					break
				}
				reruns = append(reruns, a)
			}
			recovered := len(reruns) > 0
			for _, a := range reruns {
				if ctx.Err() != nil {
					recovered = false
					break
				}
				if execute(ctx, a, c.Env, cwd, run, timeout, c.FailOnStdout).Get("ok") != true {
					recovered = false
				}
			}
			if recovered && ctx.Err() == nil {
				r.Set("ok", true)
				r.Set("code", 0)
				r.Set("flaky", files)
			}
		}
		if item.name == "test" && r.Get("ok") == true && p.Repeat != nil && ctx.Err() == nil {
			skip := func(message string) {
				if p.Repeat.OnSkip != nil {
					p.Repeat.OnSkip(message)
				}
			}
			full := item.scope.Get("scope") == "full" && item.scope.Get("reason") == "runner-filter"
			if !Has(c.Argv, "{test_files}") {
				skip("repeat_new_tests: skipped (gates.test has no {test_files})")
			} else if full {
				skip("repeat_new_tests: skipped (no changed tests for this runner)")
			} else {
				changed, safe := RepeatFiles(cwd, p.Repeat)
				if !safe {
					return nil, false, fmt.Errorf("repeat_new_tests: unsupported changed test selection")
				}
				if p.Light != nil {
					selected := []string{}
					for _, f := range changed {
						if Has(p.Light, f) {
							selected = append(selected, f)
						}
					}
					changed = selected
				}
				files := RunnerFiles(c.Argv, changed)
				if len(changed) > 0 && len(files) == 0 {
					skip("repeat_new_tests: skipped (no changed tests for this runner)")
				}
				if len(files) > 0 {
					repeats := ledger.Object{{Key: "n", Value: p.Repeat.N}, {Key: "files", Value: files}, {Key: "failed", Value: false}}
					r.Set("repeats", repeats)
					plan, e := prepare(Plan{Commands: map[string]*Command{"test": c}}, Scope{Kind: "related", Files: files})
					if e != nil {
						return nil, false, e
					}
					a := plan[0].argv
					for i := 0; i < p.Repeat.N && ctx.Err() == nil; i++ {
						repeated := execute(ctx, a, c.Env, cwd, run, timeout, c.FailOnStdout)
						if repeated.Get("ok") != true && repeats.Get("failed") != true {
							repeats.Set("failed", true)
							r.Set("reason", "flaky-test")
							r.Set("argv", a)
							merge(&r, repeated)
						}
					}
					r.Set("repeats", repeats)
				}
			}
		}
		r.Set("duration_ms", int64(math.Round(float64(time.Since(started))/float64(time.Millisecond))))
		results = append(results, r)
		if r.Get("ok") != true {
			all = false
		}
	}
	for _, custom := range p.Custom {
		if ctx.Err() != nil {
			break
		}
		c := custom.Command
		if p.Restricted && !Has(p.Names, custom.Name) {
			results = append(results, ledger.Object{{Key: "gate", Value: custom.Name}, {Key: "duration_ms", Value: 0}, {Key: "custom", Value: true}, {Key: "argv", Value: nil}, {Key: "skipped", Value: true}, {Key: "ok", Value: true}, {Key: "code", Value: nil}, {Key: "stdout", Value: ""}, {Key: "stderr", Value: ""}, {Key: "reason", Value: "not in gates.kinds." + p.Kind}, {Key: "evidence", Value: []any{}}})
			continue
		}
		started := time.Now()
		outcome := execute(ctx, c.Argv, c.Env, cwd, run, timeout, false)
		evidence, e := collectEvidence(cwd, custom.Evidence)
		if e != nil {
			return nil, false, e
		}
		r := ledger.Object{{Key: "gate", Value: custom.Name}, {Key: "custom", Value: true}, {Key: "argv", Value: c.Argv}, {Key: "skipped", Value: false}, {Key: "duration_ms", Value: int64(math.Round(float64(time.Since(started)) / float64(time.Millisecond)))}}
		merge(&r, outcome)
		if len(c.Env) > 0 {
			keys := []string{}
			for k := range c.Env {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			r.Set("env_keys", keys)
		}
		merge(&r, evidence)
		results = append(results, r)
		if r.Get("ok") != true {
			all = false
		}
	}
	return results, all && ctx.Err() == nil, nil
}

// gateNice is the CPU priority (nice value) of every gate child.
const gateNice = 10

func execute(ctx context.Context, argv []string, env map[string]string, cwd, run string, timeout float64, failOnStdout bool) ledger.Object {
	program, resolveErr := resolveExecutable(cwd, &Command{Argv: argv, Env: env})
	// Always give exec.Command an absolute path. It must never search the
	// parent's PATH when the child's PATH did not resolve the executable.
	cmd := exec.Command(program, argv[1:]...)
	cmd.Err = resolveErr
	cmd.Args[0] = argv[0]
	cmd.Dir = cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	vars := map[string]string{}
	for _, s := range os.Environ() {
		k, v, _ := strings.Cut(s, "=")
		vars[k] = v
	}
	vars["CI"] = "1"
	vars["npm_config_yes"] = "false"
	for k, v := range env {
		vars[k] = v
	}
	vars["CODE_FORGE_OWNER_PID"] = strconv.Itoa(os.Getpid())
	if start := filelock.ProcessStartTime(os.Getpid()); start != nil {
		vars["CODE_FORGE_OWNER_START_TIME"] = *start
	} else {
		delete(vars, "CODE_FORGE_OWNER_START_TIME")
	}
	if _, present := vars["CODE_FORGE_RUN_ID"]; !present {
		vars["CODE_FORGE_RUN_ID"] = run
	}
	for k, v := range vars {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	kill := func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}
	stdout, stderr := &boundedBuffer{kill: kill}, &boundedBuffer{kill: kill}
	// Keep pipe draining independent of Wait: the leader can exit while a
	// surviving group member still holds its inherited output descriptors.
	outRead, outWrite, outErr := os.Pipe()
	errRead, errWrite, errErr := os.Pipe()
	if outErr != nil || errErr != nil {
		if outRead != nil {
			outRead.Close()
			outWrite.Close()
		}
		if errRead != nil {
			errRead.Close()
			errWrite.Close()
		}
		return ledger.Object{{Key: "ok", Value: false}, {Key: "code", Value: nil}, {Key: "stdout", Value: ""}, {Key: "stderr", Value: "gate terminated: pipe error\n"}, {Key: "reason", Value: "spawn-error"}, {Key: "message", Value: "gate terminated: pipe error"}}
	}
	defer outRead.Close()
	defer errRead.Close()
	cmd.Stdout = outWrite
	cmd.Stderr = errWrite
	var drains sync.WaitGroup
	drains.Add(2)
	go func() { defer drains.Done(); _, _ = io.Copy(stdout, outRead) }()
	go func() { defer drains.Done(); _, _ = io.Copy(stderr, errRead) }()
	var code any
	reason, diagnostic, message := "", "", ""
	timed := false
	if e := cmd.Start(); e != nil {
		outWrite.Close()
		errWrite.Close()
		reason = "spawn-error"
		diagnostic = "gate terminated: spawn " + argv[0] + " " + spawnErrorCode(e)
	} else {
		// Gate commands (the full test suite above all) run at nice 10 so a
		// gate cannot starve the rest of the machine. The child leads its own
		// process group, so lowering the group also covers early forks.
		// Unprivileged callers cannot lower a higher nice; errors are ignored.
		_ = syscall.Setpriority(syscall.PRIO_PGRP, cmd.Process.Pid, gateNice)
		outWrite.Close()
		errWrite.Close()
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		duration := time.Duration(timeout * float64(time.Millisecond))
		if timeout > float64(math.MaxInt64)/float64(time.Millisecond) {
			duration = time.Duration(math.MaxInt64)
		}
		if duration < time.Millisecond {
			duration = time.Millisecond
		}
		timer := time.NewTimer(duration)
		var err error
		select {
		case err = <-done:
		case <-timer.C:
			timed = true
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			select {
			case err = <-done:
			case <-time.After(2 * time.Second):
				kill()
				err = <-done
			}
		case <-ctx.Done():
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			select {
			case err = <-done:
			case <-time.After(2 * time.Second):
				kill()
				err = <-done
			}
		}
		timer.Stop()
		stopRemaining(cmd.Process.Pid)
		if cmd.ProcessState.ExitCode() >= 0 {
			code = cmd.ProcessState.ExitCode()
		} else {
			sig := cmd.ProcessState.Sys().(syscall.WaitStatus).Signal()
			reason = signalName(sig)
			if reason == "" {
				reason = "terminated"
				diagnostic = "gate terminated: no exit status (no signal reported)"
			} else {
				diagnostic = "gate terminated: " + reason
			}
		}
		_ = err
	}
	// Node destroys both streams after timeout/cancellation and remaining-member
	// cleanup. An escaped descendant (or failed ps) must not hold this run open.
	// Allow queued bytes to drain, then close the read ends to unblock io.Copy.
	drained := make(chan struct{})
	go func() { drains.Wait(); close(drained) }()
	drainTimer := time.NewTimer(100 * time.Millisecond)
	select {
	case <-drained:
	case <-drainTimer.C:
		outRead.Close()
		errRead.Close()
		<-drained
	}
	drainTimer.Stop()
	out, errout := stdout.text(), stderr.text()
	if timed {
		reason = "timeout"
		diagnostic = "gate timeout after " + num(timeout/1000) + " seconds; raise gates.timeout_s (or --timeout-ms)"
		message = "TIMEOUT after " + num(timeout/1000) + " s (not a test failure); rerun with --timeout-ms " + num(timeout*2) + " or raise gates.timeout_s"
	} else if code == 0 {
		for _, s := range []string{out, errout} {
			lines := strings.Split(strings.ReplaceAll(trim(s), "\r\n", "\n"), "\n")
			if prompt.MatchString(lines[len(lines)-1]) {
				reason = "prompt"
				diagnostic = "gate ended on an interactive prompt; install the tool or make the command non-interactive"
			}
		}
	}
	if diagnostic != "" {
		if message == "" {
			message = diagnostic
		}
		if tail := tails(out, errout); tail != "" {
			message += "\n" + tail
		}
		if errout != "" && !strings.HasSuffix(errout, "\n") {
			errout += "\n"
		}
		errout += diagnostic + "\n"
	}
	r := ledger.Object{{Key: "ok", Value: code == 0 && reason == "" && ctx.Err() == nil && !stdout.over && !stderr.over && (!failOnStdout || strings.TrimSpace(out) == "")}, {Key: "code", Value: code}, {Key: "stdout", Value: out}, {Key: "stderr", Value: errout}}
	if timed {
		r.Set("timed_out", true)
	}
	if reason != "" {
		r.Set("reason", reason)
	}
	if diagnostic != "" {
		r.Set("message", message)
	}
	return r
}

// Executable resolves the child's PATH, rather than Go's parent-side LookPath.
// Its original spelling remains in the result argv and in the child's argv[0].
func Executable(cwd string, c *Command) string {
	p, e := resolveExecutable(cwd, c)
	if e != nil {
		return ""
	}
	st, e := os.Stat(p)
	if e != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0111 == 0 {
		return ""
	}
	// Node/execvp runs unrecognized executable files through /bin/sh on
	// ENOEXEC; Go does not. Delegate these before taking the proof slot.
	f, e := os.Open(p)
	if e != nil {
		return ""
	}
	defer f.Close()
	header := make([]byte, 4)
	n, _ := f.Read(header)
	if n >= 2 && string(header[:2]) == "#!" {
		return p
	}
	if n == 4 {
		switch string(header) {
		case "\x7fELF", "\xfe\xed\xfa\xce", "\xce\xfa\xed\xfe", "\xfe\xed\xfa\xcf", "\xcf\xfa\xed\xfe", "\xca\xfe\xba\xbe", "\xbe\xba\xfe\xca", "\xca\xfe\xba\xbf", "\xbf\xba\xfe\xca":
			return p
		}
	}
	return ""
}

func resolveExecutable(cwd string, c *Command) (string, error) {
	cwd, _ = filepath.Abs(cwd)
	name := c.Argv[0]
	if strings.ContainsRune(name, '/') {
		if !filepath.IsAbs(name) {
			name = filepath.Join(cwd, name)
		}
		return name, nil // let Start retain the real errno, including EACCES
	}
	paths, present := os.LookupEnv("PATH")
	if !present {
		paths = "/usr/bin:/bin"
	}
	if v, ok := c.Env["PATH"]; ok {
		paths = v
	}
	var denied bool
	for _, dir := range strings.Split(paths, string(os.PathListSeparator)) {
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(cwd, dir)
		}
		p := filepath.Join(dir, name)
		st, e := os.Stat(p)
		if e == nil {
			if st.Mode().IsRegular() && st.Mode().Perm()&0111 != 0 {
				return p, nil
			}
			denied = true
		} else if errors.Is(e, syscall.EACCES) {
			denied = true
		} else if !errors.Is(e, syscall.ENOENT) && !errors.Is(e, syscall.ENOTDIR) {
			return p, e
		}
	}
	if denied {
		return "", syscall.EACCES
	}
	return "", exec.ErrNotFound
}

// These are libuv/Node's canonical names, never Go's human descriptions.
func signalName(sig syscall.Signal) string {
	names := map[syscall.Signal]string{
		syscall.SIGHUP: "SIGHUP", syscall.SIGINT: "SIGINT", syscall.SIGQUIT: "SIGQUIT",
		syscall.SIGILL: "SIGILL", syscall.SIGTRAP: "SIGTRAP", syscall.SIGABRT: "SIGABRT",
		syscall.SIGBUS: "SIGBUS", syscall.SIGFPE: "SIGFPE", syscall.SIGKILL: "SIGKILL",
		syscall.SIGUSR1: "SIGUSR1", syscall.SIGSEGV: "SIGSEGV", syscall.SIGUSR2: "SIGUSR2",
		syscall.SIGPIPE: "SIGPIPE", syscall.SIGALRM: "SIGALRM", syscall.SIGTERM: "SIGTERM",
		syscall.SIGCHLD: "SIGCHLD", syscall.SIGCONT: "SIGCONT", syscall.SIGSTOP: "SIGSTOP",
		syscall.SIGTSTP: "SIGTSTP", syscall.SIGTTIN: "SIGTTIN", syscall.SIGTTOU: "SIGTTOU",
		syscall.SIGURG: "SIGURG", syscall.SIGXCPU: "SIGXCPU", syscall.SIGXFSZ: "SIGXFSZ",
		syscall.SIGVTALRM: "SIGVTALRM", syscall.SIGPROF: "SIGPROF", syscall.SIGWINCH: "SIGWINCH",
		syscall.SIGIO: "SIGIO", syscall.SIGSYS: "SIGSYS",
	}
	if name := names[sig]; name != "" {
		return name
	}
	// Platform-only signals are not all defined in Go's syscall package on
	// both release targets. Their numbers are fixed by the respective ABIs.
	if runtime.GOOS == "darwin" {
		return map[syscall.Signal]string{7: "SIGEMT", 29: "SIGINFO"}[sig]
	}
	if runtime.GOOS == "linux" {
		return map[syscall.Signal]string{16: "SIGSTKFLT", 30: "SIGPWR"}[sig]
	}
	return ""
}

func spawnErrorCode(err error) string {
	if errors.Is(err, exec.ErrNotFound) {
		return "ENOENT"
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		names := map[syscall.Errno]string{
			syscall.EACCES: "EACCES", syscall.ENOENT: "ENOENT", syscall.ENOTDIR: "ENOTDIR",
			syscall.ELOOP: "ELOOP", syscall.E2BIG: "E2BIG", syscall.ENAMETOOLONG: "ENAMETOOLONG",
			syscall.ENOMEM: "ENOMEM", syscall.EAGAIN: "EAGAIN", syscall.ETXTBSY: "ETXTBSY",
			syscall.EIO: "EIO", syscall.EMFILE: "EMFILE", syscall.ENFILE: "ENFILE",
			syscall.EPERM: "EPERM", syscall.EINVAL: "EINVAL", syscall.ENOEXEC: "ENOEXEC",
			syscall.EFAULT: "EFAULT", syscall.EISDIR: "EISDIR", syscall.EINTR: "EINTR", syscall.ENOSYS: "ENOSYS",
		}
		if name := names[errno]; name != "" {
			return name
		}
		return fmt.Sprintf("Unknown system error -%d", errno)
	}
	return err.Error()
}

// Like Node's groupMembers/stopMembers, signal surviving members only after
// checking the identity captured when the leader exited.
func stopRemaining(pgid int) {
	cmd := exec.Command("/bin/ps", "-A", "-o", "pid=,pgid=,lstart=")
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	data, e := cmd.Output()
	if e != nil {
		return
	}
	members := map[int]string{}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, _ := strconv.Atoi(f[0])
		group, _ := strconv.Atoi(f[1])
		if group == pgid && pid != pgid && pid != os.Getpid() {
			members[pid] = strings.Join(f[2:], " ")
		}
	}
	alive := func(pid int, start string) bool {
		cmd := exec.Command("/bin/ps", "-o", "state=,lstart=", "-p", strconv.Itoa(pid))
		cmd.Env = append(os.Environ(), "LC_ALL=C")
		b, e := cmd.Output()
		f := strings.Fields(string(b))
		return e == nil && len(f) > 1 && !strings.HasPrefix(f[0], "Z") && strings.Join(f[1:], " ") == start
	}
	sent := map[int]string{}
	for pid, start := range members {
		if alive(pid, start) {
			_ = syscall.Kill(pid, syscall.SIGTERM)
			sent[pid] = start
		}
	}
	wait := func(deadline time.Time) {
		for len(sent) > 0 && time.Now().Before(deadline) {
			for pid, start := range sent {
				if !alive(pid, start) {
					delete(sent, pid)
				}
			}
			if len(sent) > 0 {
				time.Sleep(25 * time.Millisecond)
			}
		}
	}
	wait(time.Now().Add(2 * time.Second))
	for pid, start := range sent {
		if alive(pid, start) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	wait(time.Now().Add(2 * time.Second))
}
