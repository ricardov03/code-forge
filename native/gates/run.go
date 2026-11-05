package gates

import (
	"bytes"
	"context"
	"fmt"
	"github.com/ricardov03/code-forge/native/filelock"
	"github.com/ricardov03/code-forge/native/ledger"
	"math"
	"os"
	"os/exec"
	"regexp"
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
		r = ledger.Object{{Key: "gate", Value: item.name}, {Key: "duration_ms", Value: int64(0)}, {Key: "argv", Value: item.argv}, {Key: "skipped", Value: false}}
		merge(&r, item.scope)
		started := time.Now()
		outcome := execute(ctx, item.argv, c.Env, cwd, run, timeout, c.FailOnStdout)
		merge(&r, outcome)
		if len(c.Env) > 0 {
			keys := []string{}
			for k := range c.Env {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			r.Set("env_keys", keys)
		}
		r.Set("duration_ms", int64(math.Round(float64(time.Since(started))/float64(time.Millisecond))))
		results = append(results, r)
		if r.Get("ok") != true {
			all = false
		}
	}
	return results, all && ctx.Err() == nil, nil
}
func execute(ctx context.Context, argv []string, env map[string]string, cwd, run string, timeout float64, failOnStdout bool) ledger.Object {
	cmd := exec.Command(argv[0], argv[1:]...)
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
	if vars["CODE_FORGE_RUN_ID"] == "" {
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
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	var code any
	reason, diagnostic, message := "", "", ""
	timed := false
	if e := cmd.Start(); e != nil {
		reason = "spawn-error"
		diagnostic = "gate terminated: spawn " + argv[0] + " ENOENT"
	} else {
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
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		if cmd.ProcessState.ExitCode() >= 0 {
			code = cmd.ProcessState.ExitCode()
		} else {
			sig := cmd.ProcessState.Sys().(syscall.WaitStatus).Signal()
			reason = "SIG" + strings.ToUpper(sig.String())
			switch sig {
			case syscall.SIGTERM:
				reason = "SIGTERM"
			case syscall.SIGINT:
				reason = "SIGINT"
			case syscall.SIGKILL:
				reason = "SIGKILL"
			}
			diagnostic = "gate terminated: " + reason
		}
		_ = err
	}
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
