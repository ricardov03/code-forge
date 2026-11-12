// Package childreg is src/util/exec.mjs's pid registry for the children Go
// spawns outside the verbs that record their own (B573): every child Node
// spawns through exec is recorded in <run-root>/pids/<pid>.json while it
// runs and gets the owner variables (CODE_FORGE_OWNER_PID, its start time,
// CODE_FORGE_RUN_ID). The CLI process sets Started and Env once at start;
// with both nil (tests, Go-only verbs) every helper here is the plain
// os/exec call.
package childreg

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
)

// Started, when set, is called with each child's pid right after it starts;
// the func it returns (when not nil) runs once the child has been waited for.
var Started func(pid int, argv0 string) func()

// Env, when set, gives a child's environment from the one it would get (the
// process environment when the command sets none): exec.mjs's owner and run
// variables.
var Env func(env []string) []string

// Pick is own when set, else Started: a call site's own hook wins.
func Pick(own func(pid int, argv0 string) func()) func(pid int, argv0 string) func() {
	if own != nil {
		return own
	}
	return Started
}

// Begin records a started child (pid, argv0) through hook (nil = Started)
// and returns the func that removes its entry (never nil).
func Begin(hook func(pid int, argv0 string) func(), pid int, argv0 string) func() {
	if hook = Pick(hook); hook != nil {
		if done := hook(pid, argv0); done != nil {
			return done
		}
	}
	return func() {}
}

// ApplyEnv sets cmd.Env through Env (a nil cmd.Env is the process
// environment, as os/exec reads it).
func ApplyEnv(cmd *exec.Cmd) {
	if Env == nil {
		return
	}
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = Env(env)
}

// ChildEnv is env through Env (unchanged when Env is nil).
func ChildEnv(env []string) []string {
	if Env == nil {
		return env
	}
	if env == nil {
		env = os.Environ()
	}
	return Env(env)
}

// Run is cmd.Run with the registry entry and the owner variables.
func Run(cmd *exec.Cmd) error {
	ApplyEnv(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	done := Begin(nil, cmd.Process.Pid, argv0(cmd))
	err := cmd.Wait()
	done()
	return err
}

// Output is cmd.Output with the registry entry and the owner variables (an
// exit error carries the child's stderr when the command set no Stderr).
func Output(cmd *exec.Cmd) ([]byte, error) {
	if cmd.Stdout != nil {
		return nil, errors.New("exec: Stdout already set")
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	var stderr *bytes.Buffer
	if cmd.Stderr == nil {
		stderr = &bytes.Buffer{}
		cmd.Stderr = stderr
	}
	err := Run(cmd)
	var exit *exec.ExitError
	if stderr != nil && errors.As(err, &exit) {
		exit.Stderr = stderr.Bytes()
	}
	return stdout.Bytes(), err
}

// CombinedOutput is cmd.CombinedOutput with the registry entry and the owner
// variables.
func CombinedOutput(cmd *exec.Cmd) ([]byte, error) {
	if cmd.Stdout != nil || cmd.Stderr != nil {
		return nil, errors.New("exec: Stdout or Stderr already set")
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := Run(cmd)
	return out.Bytes(), err
}

// argv0 is the command as the caller named it (exec.mjs's `command`).
func argv0(cmd *exec.Cmd) string {
	if len(cmd.Args) > 0 {
		return cmd.Args[0]
	}
	return cmd.Path
}
