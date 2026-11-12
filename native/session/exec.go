package session

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ricardov03/code-forge/native/filelock"
	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/ledger"
)

// Port of src/util/exec.mjs for one foreground session: argv only (never a
// shell), its own process group, stdin from the packet bytes or /dev/null,
// a hard timeout and a "no output at all" silence timeout that SIGTERM the
// group (SIGKILL 2 s later), a 10 MiB cap per stream, and a pid-registry
// entry under <runRoot>/pids for as long as the child lives.

// MaxBufferBytes is exec.mjs's per-stream cap.
const MaxBufferBytes = 10 * 1024 * 1024

// KillGraceMs is exec.mjs's SIGTERM → SIGKILL grace.
const KillGraceMs = 2000

type execOpts struct {
	Argv            []string
	Cwd             string
	Env             []string // the child's environment before the owner fields
	Input           []byte   // nil: stdin is /dev/null
	TimeoutMs       float64
	SilenceMs       float64 // 0: none
	PidsDir         string  // "" = no registry entry
	RunIDFromParent string  // process.env.CODE_FORGE_RUN_ID (or the run root's r-* name)
}

type execResult struct {
	OK              bool
	Code            any // float64 exit code, or nil
	Stdout, Stderr  string
	TimedOut        bool
	SilenceTimedOut bool
	Error           string // "" = none ("spawn claude ENOENT", "exec: maxBufferBytes exceeded")
}

type capped struct {
	mu     sync.Mutex
	buf    []byte
	over   bool
	onData func()
	kill   func()
}

func (c *capped) Write(p []byte) (int, error) {
	c.mu.Lock()
	if len(p) > 0 && c.onData != nil {
		c.onData()
	}
	if c.over {
		c.mu.Unlock()
		return len(p), nil
	}
	room := MaxBufferBytes - len(c.buf)
	if len(p) > room {
		c.buf = append(c.buf, p[:room]...)
		c.over = true
		c.mu.Unlock()
		c.kill()
		return len(p), nil
	}
	c.buf = append(c.buf, p...)
	c.mu.Unlock()
	return len(p), nil
}

func (c *capped) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return utf8Text(c.buf)
}

// utf8Text is Buffer#toString('utf8'): each invalid sequence becomes U+FFFD.
func utf8Text(b []byte) string { return strings.ToValidUTF8(string(b), "�") }

func childPath(env []string) (string, bool) {
	for i := len(env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(env[i], "="); ok && k == "PATH" {
			return v, true
		}
	}
	return "", false
}

// registerPid is reaper.mjs registerPid: {"pid","start_time","argv0"}\n, 0600.
func registerPid(dir string, pid int, argv0 string) bool {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false
	}
	start := "unknown"
	if t := filelock.ProcessStartTime(pid); t != nil {
		start = *t
	}
	data, err := ledger.Stringify(ledger.Object{{Key: "pid", Value: float64(pid)}, {Key: "start_time", Value: start}, {Key: "argv0", Value: argv0}}, false)
	if err != nil {
		return false
	}
	return os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)+".json"), append(data, '\n'), 0o600) == nil
}

func run(o execOpts) execResult {
	name := o.Argv[0]
	env := append([]string{}, o.Env...)
	env = setEnv(env, "CODE_FORGE_OWNER_PID", strconv.Itoa(os.Getpid()))
	if start := filelock.ProcessStartTime(os.Getpid()); start != nil {
		env = setEnv(env, "CODE_FORGE_OWNER_START_TIME", *start)
	} else {
		env = unsetEnv(env, "CODE_FORGE_OWNER_START_TIME")
	}
	if _, has := lookupEnv(env, "CODE_FORGE_RUN_ID"); !has && o.RunIDFromParent != "" {
		env = setEnv(env, "CODE_FORGE_RUN_ID", o.RunIDFromParent)
	}
	cmdEnv := map[string]string{}
	if p, ok := childPath(env); ok {
		cmdEnv["PATH"] = p
	}
	program := gates.Executable(o.Cwd, &gates.Command{Argv: o.Argv, Env: cmdEnv})
	if program == "" {
		code := "ENOENT"
		if found, _ := lookExists(o.Cwd, name, cmdEnv); found {
			code = "EACCES"
		}
		return execResult{Error: "spawn " + name + " " + code}
	}
	cmd := exec.Command(program, o.Argv[1:]...)
	cmd.Args[0] = name
	cmd.Dir = o.Cwd
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdinWrite *os.File
	if o.Input != nil {
		r, w, err := os.Pipe()
		if err != nil {
			return execResult{Error: err.Error()}
		}
		cmd.Stdin = r
		stdinWrite = w
		defer r.Close()
	}
	var mu sync.Mutex
	var pid int
	var timedOut, silenceTimed, exited bool
	signalGroup := func(sig syscall.Signal) {
		mu.Lock()
		p := pid
		done := exited
		mu.Unlock()
		if p > 0 && !done {
			_ = syscall.Kill(-p, sig)
		}
	}
	var silenceOnce sync.Once
	silenceStop := make(chan struct{})
	onData := func() { silenceOnce.Do(func() { close(silenceStop) }) }
	stdout := &capped{onData: onData, kill: func() { signalGroup(syscall.SIGKILL) }}
	stderr := &capped{onData: onData, kill: func() { signalGroup(syscall.SIGKILL) }}
	outRead, outWrite, err1 := os.Pipe()
	errRead, errWrite, err2 := os.Pipe()
	if err1 != nil || err2 != nil {
		return execResult{Error: "pipe error"}
	}
	defer outRead.Close()
	defer errRead.Close()
	cmd.Stdout, cmd.Stderr = outWrite, errWrite
	var drains sync.WaitGroup
	drains.Add(2)
	go func() { defer drains.Done(); _, _ = io.Copy(stdout, outRead) }()
	go func() { defer drains.Done(); _, _ = io.Copy(stderr, errRead) }()
	if err := cmd.Start(); err != nil {
		outWrite.Close()
		errWrite.Close()
		if stdinWrite != nil {
			stdinWrite.Close()
		}
		drains.Wait()
		code := "ENOENT"
		var errno syscall.Errno
		if errors.As(err, &errno) && errno != syscall.ENOENT {
			code = errno.Error()
			if errno == syscall.EACCES {
				code = "EACCES"
			}
		}
		return execResult{Error: "spawn " + name + " " + code}
	}
	outWrite.Close()
	errWrite.Close()
	mu.Lock()
	pid = cmd.Process.Pid
	mu.Unlock()
	registered := o.PidsDir != "" && registerPid(o.PidsDir, pid, name)
	if stdinWrite != nil {
		go func() {
			_, _ = stdinWrite.Write(o.Input) // EPIPE is the child's business
			stdinWrite.Close()
		}()
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); mu.Lock(); exited = true; mu.Unlock(); close(done) }()
	timeOut := func(silent bool) {
		mu.Lock()
		if timedOut || exited {
			mu.Unlock()
			return
		}
		timedOut, silenceTimed = true, silent
		mu.Unlock()
		signalGroup(syscall.SIGTERM)
		go func() {
			select {
			case <-done:
			case <-time.After(KillGraceMs * time.Millisecond):
				signalGroup(syscall.SIGKILL)
			}
		}()
	}
	hard := time.NewTimer(msDuration(o.TimeoutMs))
	defer hard.Stop()
	var silence <-chan time.Time
	if o.SilenceMs > 0 {
		t := time.NewTimer(msDuration(o.SilenceMs))
		defer t.Stop()
		silence = t.C
	}
	for waiting := true; waiting; {
		select {
		case <-done:
			waiting = false
		case <-hard.C:
			timeOut(false)
		case <-silence:
			silence = nil
			timeOut(true)
		case <-silenceStop:
			silence = nil
			silenceStop = nil
		}
	}
	stopMembers(pid)
	drained := make(chan struct{})
	go func() { drains.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(100 * time.Millisecond):
		outRead.Close()
		errRead.Close()
		<-drained
	}
	if registered {
		_ = os.Remove(filepath.Join(o.PidsDir, strconv.Itoa(pid)+".json"))
	}
	res := execResult{Stdout: stdout.text(), Stderr: stderr.text(), TimedOut: timedOut, SilenceTimedOut: timedOut && silenceTimed}
	if code := cmd.ProcessState.ExitCode(); code >= 0 {
		res.Code = float64(code)
	}
	if stdout.over || stderr.over {
		res.Error = "exec: maxBufferBytes exceeded"
	}
	res.OK = res.Error == "" && !timedOut && res.Code == 0.0
	return res
}

// stopMembers is exec.mjs stopMembers: group members left after the leader
// exited get SIGTERM, then SIGKILL after the grace.
func stopMembers(pgid int) {
	if pgid <= 0 || syscall.Kill(-pgid, 0) != nil {
		return
	}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	deadline := time.Now().Add(KillGraceMs * time.Millisecond)
	for time.Now().Before(deadline) {
		if syscall.Kill(-pgid, 0) != nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

func msDuration(ms float64) time.Duration {
	if ms < 1 {
		ms = 1
	}
	return time.Duration(ms * float64(time.Millisecond))
}

func lookExists(cwd, name string, env map[string]string) (bool, error) {
	if strings.ContainsRune(name, '/') {
		p := name
		if !filepath.IsAbs(p) {
			p = filepath.Join(cwd, p)
		}
		_, err := os.Stat(p)
		return err == nil, err
	}
	paths, ok := env["PATH"]
	if !ok {
		paths = "/usr/bin:/bin"
	}
	for _, dir := range filepath.SplitList(paths) {
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(cwd, dir)
		}
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true, nil
		}
	}
	return false, nil
}

func lookupEnv(env []string, key string) (string, bool) {
	for i := len(env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(env[i], "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}

func unsetEnv(env []string, key string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); k != key {
			out = append(out, kv)
		}
	}
	return out
}

func setEnv(env []string, key, value string) []string {
	return append(unsetEnv(env, key), key+"="+value)
}
