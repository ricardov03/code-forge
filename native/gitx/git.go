// Package gitx supplies the Node-compatible git environment and write-kit helpers.
package gitx

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Error struct{ Code, Message string }

func (e *Error) Error() string { return e.Message }

// ChildEnv removes every inherited GIT_* variable, including hook exports.
func ChildEnv() []string {
	env := []string{}
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "GIT_") {
			env = append(env, v)
		}
	}
	return append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
}

// CommandError retains stdout, stderr and status for callers needing Node's
// exec result. Error's text preserves the existing rebase runner's diagnostics.
type CommandError struct {
	Args   []string
	Code   int
	Stderr string
	Cause  error
}

func (e *CommandError) Error() string {
	verb := ""
	if len(e.Args) > 0 {
		verb = e.Args[0]
	}
	return fmt.Sprintf("git %s: %v: %s", verb, e.Cause, e.Stderr)
}
func (e *CommandError) Unwrap() error { return e.Cause }

func WithEnv(tree string, env []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = tree
	cmd.Env = env
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		code := -1
		if e, ok := err.(*exec.ExitError); ok {
			code = e.ExitCode()
		}
		return string(out), &CommandError{args, code, stderr.String(), err}
	}
	return string(out), nil
}

func Run(tree string, args ...string) (string, error) { return WithEnv(tree, ChildEnv(), args...) }

func blockGit(tree string, args ...string) (string, error) {
	out, err := Run(tree, args...)
	if err != nil {
		code := -1
		if e, ok := err.(*CommandError); ok {
			code = e.Code
		}
		return "", &Error{"git-failed", fmt.Sprintf("git %s failed (exit %d)", args[0], code)}
	}
	return out, nil
}

func RepoTop(cwd string) (string, error) {
	out, err := blockGit(cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(strings.TrimSpace(out))
}
