package gates

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExecuteSignalNames(t *testing.T) {
	for _, signal := range []string{"HUP", "USR1", "USR2", "PIPE", "ALRM", "TERM", "INT", "KILL"} {
		t.Run(signal, func(t *testing.T) {
			r := execute(context.Background(), []string{"/bin/sh", "-c", "kill -" + signal + " $$"}, nil, t.TempDir(), "", 1000, false)
			want := "gate terminated: SIG" + signal
			if r.Get("reason") != "SIG"+signal || r.Get("message") != want || r.Get("stderr") != want+"\n" || r.Get("code") != nil {
				t.Fatalf("signal %s: %v", signal, r)
			}
		})
	}
}

func TestExecuteSpawnErrors(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "no-execute")
	if err := os.WriteFile(file, []byte("#!/bin/sh\nexit 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, code string }{{file, "EACCES"}, {filepath.Join(dir, "missing"), "ENOENT"}, {file + "/child", "ENOTDIR"}} {
		r := execute(context.Background(), []string{tc.name}, nil, dir, "", 1000, false)
		want := "gate terminated: spawn " + tc.name + " " + tc.code
		if r.Get("reason") != "spawn-error" || r.Get("message") != want || r.Get("stderr") != want+"\n" || r.Get("code") != nil {
			t.Errorf("%s: %v", tc.code, r)
		}
	}
}

func TestSpawnErrorCode(t *testing.T) {
	for code, errno := range map[string]syscall.Errno{"EFAULT": syscall.EFAULT, "EISDIR": syscall.EISDIR, "EINTR": syscall.EINTR, "ENOSYS": syscall.ENOSYS} {
		err := &os.PathError{Op: "fork/exec", Path: "gate", Err: errno}
		if got := spawnErrorCode(err); got != code {
			t.Errorf("%s: got %s", code, got)
		}
	}
}

func TestExecuteDoesNotSearchParentPATH(t *testing.T) {
	r := execute(context.Background(), []string{"sh", "-c", "printf parent-path-was-used"}, map[string]string{"PATH": t.TempDir()}, t.TempDir(), "", 1000, false)
	if r.Get("reason") != "spawn-error" || r.Get("stdout") != "" || r.Get("stderr") != "gate terminated: spawn sh ENOENT\n" {
		t.Fatalf("child PATH must be authoritative: %v", r)
	}
}

func niceOf(t *testing.T, pid int) int {
	t.Helper()
	out, err := exec.Command("/bin/ps", "-o", "nice=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestExecuteRunsTestChildAtNice10(t *testing.T) {
	// The child reads its own nice value once, as its first step, and reports
	// it in a file. The parent's post-start step is held until that report
	// exists, so a slow parent can never set the value before the child reads
	// it: only a child that started at nice 10 passes. A parent already above
	// 10 keeps its value.
	want := 10
	if parent := niceOf(t, os.Getpid()); parent > want {
		want = parent
	}
	dir := t.TempDir()
	report := filepath.Join(dir, "nice")
	calls, reported := 0, ""
	old := afterGateStart
	afterGateStart = func(pid int) {
		calls++
		// Wait for the report event; the cap only ends a child that never reports.
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if data, err := os.ReadFile(report); err == nil {
				reported = string(data)
				return
			}
		}
		reported = "no report"
	}
	defer func() { afterGateStart = old }()
	script := "n=$(/bin/ps -o nice= -p $$ | tr -d ' '); printf %s \"$n\" > \"$0.tmp\"; mv \"$0.tmp\" \"$0\"; printf %s \"$n\""
	p := Plan{Commands: map[string]*Command{"test": {Argv: []string{"/bin/sh", "-c", script, report}}}}
	results, _, err := Run(context.Background(), p, dir, "fixture", 30000, Scope{})
	if err != nil {
		t.Fatal(err)
	}
	r := results[0].(interface{ Get(string) any })
	if calls != 1 || reported != strconv.Itoa(want) {
		t.Fatalf("post-start step: calls=%d child reported nice %q before it, want 1 call and %d", calls, reported, want)
	}
	if r.Get("gate") != "test" || r.Get("ok") != true || r.Get("stdout") != strconv.Itoa(want) {
		t.Fatalf("test gate child nice: gate=%v ok=%v stdout=%q, want %d", r.Get("gate"), r.Get("ok"), r.Get("stdout"), want)
	}
}

func TestExecuteHelper(t *testing.T) {
	if os.Getenv("CF_PIPE_HELPER") != "1" {
		return
	}
	// Leave the gate's process group while retaining both inherited pipe ends.
	child := exec.Command("/bin/sleep", "30")
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if err := child.Start(); err != nil {
		os.Exit(98)
	}
	if err := os.WriteFile(os.Getenv("CF_PIPE_PID"), []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
		os.Exit(99)
	}
	// Stay alive until the gate deadline terminates us, leaving the detached
	// descendant holding the output pipes after Wait has returned.
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

func TestRunTimeoutWithLeakedDescendant(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "descendant.pid")
	defer func() {
		data, _ := os.ReadFile(pidFile)
		if pid, _ := strconv.Atoi(strings.TrimSpace(string(data))); pid > 1 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}()
	const timeoutMs = 500.0
	// Includes TERM grace and bounded pipe drain. A run that waited on the
	// leaked descendant would take its full 30 s sleep, so 10 s still tells
	// the two apart while leaving room for a loaded machine (B539).
	deadline := 10 * time.Second
	done := make(chan error, 1)
	started := time.Now()
	go func() {
		p := Plan{Commands: map[string]*Command{"test": {Argv: []string{exe, "-test.run=^TestExecuteHelper$"}, Env: map[string]string{"CF_PIPE_HELPER": "1", "CF_PIPE_PID": pidFile}}}}
		results, ok, err := Run(context.Background(), p, dir, "fixture", timeoutMs, Scope{})
		if err == nil && (ok || len(results) != 4) {
			err = fmt.Errorf("Run returned ok=%v, %d results; want failed and four results", ok, len(results))
		}
		if err == nil {
			// Run returns ledger.Objects, including the timed-out test gate.
			r := results[0].(interface{ Get(string) any })
			if r.Get("reason") != "timeout" || r.Get("timed_out") != true {
				err = fmt.Errorf("gate outcome: reason=%v timed_out=%v", r.Get("reason"), r.Get("timed_out"))
			}
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(pidFile)
		if err != nil {
			t.Fatal("descendant never held the pipes:", err)
		}
		pid, err := strconv.Atoi(string(data))
		if err != nil || syscall.Kill(pid, 0) != nil {
			t.Fatal("descendant must still be alive and holding the pipes when the run returns")
		}
		t.Logf("gate deadline %g ms, leaked-pipe run ended in %s", timeoutMs, time.Since(started))
	case <-time.After(deadline):
		t.Fatal("gate run exceeded its bounded timeout with a leaked descendant holding both pipes")
	}
}
