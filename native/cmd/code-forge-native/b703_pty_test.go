package main

import (
	"errors"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// B703: a pseudo-terminal test driver (Go standard library and system
// calls only, macOS and Linux). It runs a program on a real terminal of a
// given size, types each key once the program's output has been quiet for
// a while, and returns every byte the terminal showed plus the exit code.

type b703PTYRun struct {
	argv       []string
	env        []string
	dir        string
	cols, rows int
	keys       []string
	// idle is how long the output must be quiet before the next key
	idle time.Duration
	// timeout kills the program's process group
	timeout time.Duration
}

// b703RunPTY runs r and returns the terminal bytes and the exit code
// (-1 when a signal or the timeout ended the program).
func b703RunPTY(t *testing.T, r b703PTYRun) ([]byte, int) {
	t.Helper()
	if r.idle == 0 {
		r.idle = 400 * time.Millisecond
	}
	if r.timeout == 0 {
		r.timeout = 2 * time.Minute
	}
	master, slave, err := b703OpenPTY()
	if err != nil {
		t.Fatalf("open pty: %v", err)
	}
	defer master.Close()
	ws := struct{ row, col, x, y uint16 }{uint16(r.rows), uint16(r.cols), 0, 0}
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, slave.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws))); errno != 0 {
		t.Fatalf("set pty size: %v", errno)
	}
	cmd := exec.Command(r.argv[0], r.argv[1:]...)
	cmd.Env, cmd.Dir = r.env, r.dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		_ = slave.Close()
		t.Fatalf("start %v: %v", r.argv, err)
	}
	_ = slave.Close()

	var mu sync.Mutex
	var out []byte
	last := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				mu.Lock()
				out = append(out, buf[:n]...)
				last = time.Now()
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.After(r.timeout)
	kill := func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	var waitErr error
	keys := r.keys
	finished := false
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for !finished {
		select {
		case waitErr = <-exited:
			finished = true
		case <-deadline:
			kill()
			waitErr = <-exited
			t.Errorf("%v: timed out with %d keys left", r.argv, len(keys))
			finished = true
		case <-tick.C:
			if len(keys) == 0 {
				continue
			}
			mu.Lock()
			quiet := len(out) > 0 && time.Since(last) >= r.idle
			mu.Unlock()
			if quiet {
				if _, err := master.Write([]byte(keys[0])); err != nil {
					t.Errorf("write key %q: %v", keys[0], err)
				}
				keys = keys[1:]
				mu.Lock()
				last = time.Now()
				mu.Unlock()
			}
		}
	}
	// the slave is closed in every process once the program is gone; a
	// helper that keeps it open is killed with the group
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		kill()
		_ = master.Close()
		<-done
	}
	if len(keys) > 0 {
		t.Logf("%v: exited with %d keys unsent", r.argv, len(keys))
	}
	mu.Lock()
	defer mu.Unlock()
	code := 0
	var exit *exec.ExitError
	if errors.As(waitErr, &exit) {
		code = exit.ExitCode()
	} else if waitErr != nil {
		t.Fatalf("wait: %v", waitErr)
	}
	return out, code
}

// TestB703PTYDriver: the driver gives the program a terminal of the asked
// size on all three standard streams, types keys after the prompt, and
// returns the echo, the output and the exit code.
func TestB703PTYDriver(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	script := `stty size; [ -t 0 ] && [ -t 1 ] && [ -t 2 ] || exit 9; printf 'ready> '; read line; echo "got:$line" >&2; exit 3`
	out, code := b703RunPTY(t, b703PTYRun{
		argv: []string{sh, "-c", script}, env: []string{"PATH=/bin:/usr/bin", "TERM=xterm-256color"},
		dir: t.TempDir(), cols: 70, rows: 30, keys: []string{"hello\r"}, idle: 200 * time.Millisecond, timeout: 30 * time.Second,
	})
	if want := "30 70\r\nready> hello\r\ngot:hello\r\n"; string(out) != want || code != 3 {
		t.Fatalf("pty run: code %d out %q; want 3 %q", code, out, want)
	}
}
