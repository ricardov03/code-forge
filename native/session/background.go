package session

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/ricardov03/code-forge/native/filelock"
	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/ledger"
)

// B549: spawn.mjs startBackground — a detached child (its own session, as
// Node's detached: true) writing to <session>/session.log and
// session.err.log, its <runRoot>/pids/<pid>.json registry entry and
// <session>/pid.json. The child is released, never waited for or signalled.

// BackgroundProgram is the program a background session would start for
// argv0 in cwd with the child PATH of environ ("" = not found: Node's spawn
// gets no pid and throws, a case the caller leaves to Node).
func BackgroundProgram(argv0, cwd string, environ []string) string {
	env := map[string]string{}
	if p, ok := childPath(childEnv(environ, nil)); ok {
		env["PATH"] = p
	}
	return gates.Executable(cwd, &gates.Command{Argv: []string{argv0}, Env: env})
}

func startBackground(b built, sessionDir, runRoot string, deps Deps) (ledger.Object, error) {
	logPath := filepath.Join(sessionDir, "session.log")
	errLogPath := filepath.Join(sessionDir, "session.err.log")
	pidFile := filepath.Join(sessionDir, "pid.json")
	environ := deps.Environ
	if environ == nil {
		environ = os.Environ()
	}
	env := childEnv(environ, b.env)
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	defer logFile.Close()
	errFile, err := os.OpenFile(errLogPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	defer errFile.Close()
	cmd := exec.Command("/nonexistent")
	if b.stdinFile != "" {
		in, err := os.Open(b.stdinFile)
		if err != nil {
			return nil, err
		}
		defer in.Close()
		cmd.Stdin = in
	}
	cmdEnv := map[string]string{}
	if p, ok := childPath(env); ok {
		cmdEnv["PATH"] = p
	}
	program := gates.Executable(b.cwd, &gates.Command{Argv: b.argv, Env: cmdEnv})
	if program == "" {
		return nil, errors.New("spawn: could not start " + b.cli)
	}
	cmd.Path = program
	cmd.Args = append([]string{}, b.argv...)
	cmd.Dir = b.cwd
	cmd.Env = env
	cmd.Stdout = logFile
	cmd.Stderr = errFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, errors.New("spawn: could not start " + b.cli)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	if err := RegisterPid(filepath.Join(runRoot, "pids"), pid, b.argv[0]); err != nil {
		return nil, err
	}
	start := "unknown"
	if t := filelock.ProcessStartTime(pid); t != nil {
		start = *t
	}
	entry := ledger.Object{{Key: "pid", Value: float64(pid)}, {Key: "start_time", Value: start}, {Key: "argv0", Value: b.argv[0]},
		{Key: "log", Value: logPath}, {Key: "errLog", Value: errLogPath}}
	if b.outPath != "" {
		entry.Set("outPath", b.outPath) // B551r: a Codex session's -o answer file
	}
	data, err := ledger.Stringify(entry, false)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(pidFile, append(data, '\n'), 0o600); err != nil {
		return nil, err
	}
	return ledger.Object{{Key: "pid", Value: float64(pid)}, {Key: "pidFile", Value: pidFile}, {Key: "logPath", Value: logPath},
		{Key: "errLogPath", Value: errLogPath}, {Key: "sessionDir", Value: sessionDir}}, nil
}
