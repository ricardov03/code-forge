// Package blockend runs the owner's block-end hook without a shell. Publication
// and run locking belong to the caller, after the end's rows and done file.
package blockend

import (
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ricardov03/code-forge/native/gosafe"
)

type Hook struct {
	Argv    []string
	Timeout time.Duration
}

// FromConfig matches blockEndHook's malformed-snapshot floor and timeout bounds.
func FromConfig(config map[string]any) *Hook {
	hooks, _ := config["hooks"].(map[string]any)
	hook, _ := hooks["on_block_end"].(map[string]any)
	args, ok := hook["argv"].([]any)
	if !ok || len(args) == 0 {
		return nil
	}
	argv := make([]string, len(args))
	for i, arg := range args {
		s, ok := arg.(string)
		if !ok || s == "" {
			return nil
		}
		argv[i] = s
	}
	seconds := float64(60)
	if value, present := hook["timeout_s"]; present {
		var ok bool
		seconds, ok = value.(float64)
		if !ok || math.IsNaN(seconds) || math.Trunc(seconds) != seconds || seconds < 1 || seconds > 600 {
			return nil
		}
	}
	return &Hook{argv, time.Duration(seconds) * time.Second}
}

type Result struct {
	Exit       any
	Signal     any
	TimedOut   bool
	NotFound   bool
	Error      string
	DurationMS int64
}

func (r Result) Warning() string {
	if r.TimedOut {
		return "timed out"
	}
	if r.NotFound {
		return "not found"
	}
	if r.Error != "" {
		return "failed to start (" + r.Error + ")"
	}
	if code, ok := r.Exit.(int); ok {
		if code == 0 {
			return ""
		}
		return "exit " + strconv.Itoa(code)
	}
	if signal, ok := r.Signal.(string); ok {
		return "signal " + signal
	}
	return "failed to start (UNKNOWN)"
}

// ErrorCode never exposes an argv, path or message in a warning.
func ErrorCode(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		for _, pair := range []struct {
			code syscall.Errno
			name string
		}{
			{syscall.EACCES, "EACCES"}, {syscall.EPERM, "EPERM"}, {syscall.ENOENT, "ENOENT"}, {syscall.ENOTDIR, "ENOTDIR"},
			{syscall.EISDIR, "EISDIR"}, {syscall.ENOEXEC, "ENOEXEC"}, {syscall.EINVAL, "EINVAL"}, {syscall.E2BIG, "E2BIG"},
			{syscall.ENAMETOOLONG, "ENAMETOOLONG"}, {syscall.ELOOP, "ELOOP"}, {syscall.ENOMEM, "ENOMEM"},
			{syscall.EMFILE, "EMFILE"}, {syscall.ENFILE, "ENFILE"}, {syscall.EAGAIN, "EAGAIN"}, {syscall.ETXTBSY, "ETXTBSY"},
			{syscall.EEXIST, "EEXIST"}, {syscall.ENOSPC, "ENOSPC"}, {syscall.EROFS, "EROFS"}, {syscall.EIO, "EIO"},
			{syscall.EFBIG, "EFBIG"}, {syscall.EDQUOT, "EDQUOT"}, {syscall.EINTR, "EINTR"}, {syscall.EBUSY, "EBUSY"},
		} {
			if pair.code == errno {
				return pair.name
			}
		}
	}
	if errors.Is(err, exec.ErrNotFound) {
		return "ENOENT"
	}
	return "UNKNOWN"
}

// Run uses /dev/null for all stdio and a new session/process group, just like
// Node's detached spawn. Wait has no inherited pipes to keep it alive.
func Run(hook *Hook, cwd string, env []string) (result Result) {
	started := time.Now()
	defer func() {
		result.DurationMS = int64(math.Round(float64(time.Since(started)) / float64(time.Millisecond)))
	}()
	info, err := os.Stat(cwd)
	if err != nil {
		result.Error = ErrorCode(err)
		return
	}
	if !info.IsDir() {
		result.Error = "ENOTDIR"
		return
	}
	for _, arg := range hook.Argv {
		if strings.ContainsRune(arg, 0) {
			result.Error = "ERR_INVALID_ARG_VALUE"
			return
		}
	}
	executable, err := resolve(cwd, hook.Argv[0], env)
	if err != nil {
		result.NotFound = ErrorCode(err) == "ENOENT"
		if !result.NotFound {
			result.Error = ErrorCode(err)
		}
		return
	}
	cmd := &exec.Cmd{Path: executable, Args: hook.Argv, Dir: cwd, Env: env, SysProcAttr: &syscall.SysProcAttr{Setsid: true}}
	if err = cmd.Start(); err != nil {
		result.NotFound = ErrorCode(err) == "ENOENT"
		if !result.NotFound {
			result.Error = ErrorCode(err)
		}
		return
	}
	done := make(chan error, 1)
	gosafe.Go("end-hook-wait", func() { done <- cmd.Wait() })
	timer := time.NewTimer(hook.Timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		result.TimedOut = true
		if syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) != nil {
			_ = cmd.Process.Kill()
		}
		<-done
	}
	if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		result.Signal = signalName(status.Signal())
	} else {
		result.Exit = cmd.ProcessState.ExitCode()
	}
	return
}

// Resolve PATH relative to the hook cwd (including empty and relative entries),
// without Go's ErrDot restriction or shell interpretation.
func resolve(cwd, name string, env []string) (string, error) {
	if strings.ContainsRune(name, '/') {
		if !filepath.IsAbs(name) {
			name = filepath.Join(cwd, name)
		}
		return name, nil
	}
	paths := "/usr/bin:/bin"
	for _, pair := range env {
		if strings.HasPrefix(pair, "PATH=") {
			paths = strings.TrimPrefix(pair, "PATH=")
		}
	}
	denied := false
	for _, dir := range strings.Split(paths, string(os.PathListSeparator)) {
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(cwd, dir)
		}
		file := filepath.Join(dir, name)
		info, err := os.Stat(file)
		if err == nil {
			if info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
				return file, nil
			}
			denied = true
		} else if errors.Is(err, syscall.EACCES) {
			denied = true
		} else if !errors.Is(err, syscall.ENOENT) && !errors.Is(err, syscall.ENOTDIR) {
			return "", err
		}
	}
	if denied {
		return "", syscall.EACCES
	}
	return "", syscall.ENOENT
}

func signalName(sig syscall.Signal) string {
	names := map[syscall.Signal]string{
		syscall.SIGHUP: "SIGHUP", syscall.SIGINT: "SIGINT", syscall.SIGQUIT: "SIGQUIT", syscall.SIGILL: "SIGILL", syscall.SIGTRAP: "SIGTRAP", syscall.SIGABRT: "SIGABRT",
		syscall.SIGBUS: "SIGBUS", syscall.SIGFPE: "SIGFPE", syscall.SIGKILL: "SIGKILL", syscall.SIGUSR1: "SIGUSR1", syscall.SIGSEGV: "SIGSEGV", syscall.SIGUSR2: "SIGUSR2",
		syscall.SIGPIPE: "SIGPIPE", syscall.SIGALRM: "SIGALRM", syscall.SIGTERM: "SIGTERM", syscall.SIGCHLD: "SIGCHLD", syscall.SIGCONT: "SIGCONT", syscall.SIGSTOP: "SIGSTOP",
		syscall.SIGTSTP: "SIGTSTP", syscall.SIGTTIN: "SIGTTIN", syscall.SIGTTOU: "SIGTTOU", syscall.SIGURG: "SIGURG", syscall.SIGXCPU: "SIGXCPU", syscall.SIGXFSZ: "SIGXFSZ",
		syscall.SIGVTALRM: "SIGVTALRM", syscall.SIGPROF: "SIGPROF", syscall.SIGWINCH: "SIGWINCH", syscall.SIGIO: "SIGIO", syscall.SIGSYS: "SIGSYS",
	}
	if name := names[sig]; name != "" {
		return name
	}
	if runtime.GOOS == "darwin" {
		return map[syscall.Signal]string{7: "SIGEMT", 29: "SIGINFO"}[sig]
	}
	return map[syscall.Signal]string{16: "SIGSTKFLT", 30: "SIGPWR"}[sig]
}
