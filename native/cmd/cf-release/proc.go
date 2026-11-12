package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// app carries the output streams and the seams the tests replace.
type app struct {
	stdout io.Writer
	stderr io.Writer
	now    func() time.Time
	sleep  func(time.Duration)
	pid    int
}

func newApp(stdout, stderr io.Writer) *app {
	return &app{stdout: stdout, stderr: stderr, now: time.Now, sleep: time.Sleep, pid: os.Getpid()}
}

func (a *app) out(s string) { io.WriteString(a.stdout, s) }
func (a *app) err(s string) { io.WriteString(a.stderr, s) }

// result is spawnSync's answer: code -1 and a message when the child could not run or timed out.
type result struct {
	code   int
	stdout string
	stderr string
	errMsg string
}

func (r result) failed() bool { return r.errMsg != "" }

// repoEnvKeys are the git variables that pick a repository (git hooks export them).
var repoEnvKeys = []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_PREFIX", "GIT_OBJECT_DIRECTORY",
	"GIT_COMMON_DIR", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_IMPLICIT_WORK_TREE"}

// envWith copies the environment, drops `drop` and sets `set` (later keys win).
func envWith(drop []string, set ...[2]string) []string {
	dropped := map[string]bool{}
	for _, k := range drop {
		dropped[k] = true
	}
	for _, kv := range set {
		dropped[kv[0]] = true
	}
	var out []string
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		if !dropped[k] {
			out = append(out, e)
		}
	}
	for _, kv := range set {
		out = append(out, kv[0]+"="+kv[1])
	}
	return out
}

// gitChildEnv is the environment of every git child: no repository selectors, no prompts.
func gitChildEnv() []string {
	return envWith(repoEnvKeys, [2]string{"GIT_TERMINAL_PROMPT", "0"})
}

// spawn runs an argv array (never a shell) and collects its output, like spawnSync.
func spawn(cwd string, argv []string, timeout time.Duration, env []string) result {
	ctx := context.Background()
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = cwd
	if env != nil {
		cmd.Env = env
	}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err := cmd.Run()
	r := result{stdout: so.String(), stderr: se.String()}
	if ctx.Err() == context.DeadlineExceeded {
		r.code = -1
		r.errMsg = "spawnSync " + argv[0] + " ETIMEDOUT"
		return r
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			r.code = exit.ExitCode()
			return r
		}
		r.code = -1
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			r.errMsg = "spawnSync " + argv[0] + " ENOENT"
		} else {
			r.errMsg = "spawnSync " + argv[0] + " " + err.Error()
		}
		return r
	}
	return r
}

// ansiRe is Node's stripVTControlCharacters pattern.
var ansiRe = regexp.MustCompile(`[\x{1B}\x{9B}][[\]()#;?]*` +
	`(?:(?:(?:(?:;[-a-zA-Z\d\/#&.:=?%@~_]+)*` +
	`|[a-zA-Z\d]+(?:;[-a-zA-Z\d\/#&.:=?%@~_]*)*)?` +
	`(?:\x{07}|\x{1B}\\|\x{9C}))` +
	`|(?:(?:\d{1,4}(?:;\d{0,4})*)?[\dA-PR-TZcf-ntqry=><~]))`)

func stripVT(s string) string { return ansiRe.ReplaceAllString(s, "") }

// jsTrim is String#trim; jsTrimEnd is String#trimEnd.
func jsTrim(s string) string    { return strings.TrimFunc(s, jsSpace) }
func jsTrimEnd(s string) string { return strings.TrimRightFunc(s, jsSpace) }

// jsSpace is JavaScript's whitespace and line terminators (\s).
func jsSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

// jsSplitWS is text.split(/\s/): one piece per single whitespace character.
func jsSplitWS(s string) []string {
	var out []string
	start := 0
	for i, r := range s {
		if jsSpace(r) {
			out = append(out, s[start:i])
			start = i + len(string(r))
		}
	}
	return append(out, s[start:])
}

// jsFields is text.split(/\s+/).filter(Boolean).
func jsFields(s string) []string { return strings.FieldsFunc(s, jsSpace) }

// firstLine is text.split('\n')[0].
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// nonEmpty is `a || b` for strings.
func or(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func splitNul(s string) []string {
	var out []string
	for _, p := range strings.Split(s, "\x00") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
