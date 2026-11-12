package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/filelock"
	"github.com/ricardov03/code-forge/native/procguard"
)

// b529Foreign starts `sleep 30` the way a stranger's daemon runs: its shell
// exits at once, so the sleep's parent is init and it is no descendant of
// this test. The test (not the code under test) kills it at cleanup.
func b529Foreign(t *testing.T) (int, string) {
	t.Helper()
	out, err := exec.Command("/bin/sh", "-c", "/bin/sleep 30 >/dev/null 2>&1 </dev/null & echo $!").Output()
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("pid %q: %v", out, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	deadline := time.Now().Add(5 * time.Second)
	for {
		ppid, _ := exec.Command("/bin/ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
		if strings.TrimSpace(string(ppid)) == "1" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pid %d still has parent %q", pid, ppid)
		}
		time.Sleep(10 * time.Millisecond)
	}
	started := filelock.ProcessStartTime(pid)
	if started == nil {
		t.Fatal("no start time for the foreign process")
	}
	return pid, *started
}

// b529Alive: pid is still the same live, non-zombie process.
func b529Alive(pid int, started string) bool {
	out, err := exec.Command("/bin/ps", "-o", "state=", "-p", strconv.Itoa(pid)).Output()
	now := filelock.ProcessStartTime(pid)
	return err == nil && !strings.HasPrefix(strings.TrimSpace(string(out)), "Z") && now != nil && *now == started
}

// TestB529GuardRefusesForeignAllowsOwn: under go test a signal to a process
// outside the test's tree is refused (EPERM, nothing sent); one to the
// test's own child goes through; the test binary names itself as the root
// for every process it starts.
func TestB529GuardRefusesForeignAllowsOwn(t *testing.T) {
	if got := os.Getenv(procguard.Env); got != strconv.Itoa(os.Getpid()) {
		t.Fatalf("%s=%q, want this test's pid %d", procguard.Env, got, os.Getpid())
	}
	pid, started := b529Foreign(t)
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatal(err)
	}
	// the foreign sleep shares a group with `go test` itself, which is no
	// descendant of this test either
	if procguard.Allowed(pid, syscall.SIGTERM) || procguard.Allowed(-pgid, syscall.SIGTERM) {
		t.Fatal("a signal to a foreign process or its group is allowed")
	}
	if err := procguard.Kill(pid, syscall.SIGTERM); err != syscall.EPERM {
		t.Fatalf("kill foreign = %v, want EPERM", err)
	}
	for _, p := range []int{0, -1} {
		if procguard.Allowed(p, syscall.SIGTERM) {
			t.Fatalf("kill(%d) is allowed", p)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if !b529Alive(pid, started) {
		t.Fatal("the foreign process was stopped")
	}
	if !procguard.Allowed(pid, 0) {
		t.Fatal("a liveness probe is refused")
	}

	own := exec.Command("/bin/sleep", "30")
	own.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := own.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = own.Process.Kill(); _, _ = own.Process.Wait() })
	if err := procguard.Kill(-own.Process.Pid, syscall.SIGTERM); err != nil {
		t.Fatalf("kill own group = %v", err)
	}
	state, _ := own.Process.Wait()
	if status, ok := state.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Fatalf("own child not stopped by SIGTERM: %v", state)
	}

	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	if err := procguard.Kill(gone.Process.Pid, syscall.SIGTERM); err != syscall.ESRCH {
		t.Fatalf("kill of a gone pid = %v, want ESRCH", err)
	}
}

// TestB529SweepApplyLeavesForeignProcess: `sweep --apply` with the real
// signal path, against a process table that shows a foreign process as the
// stale worker of an ended run: the output still names it, but it is alive.
func TestB529SweepApplyLeavesForeignProcess(t *testing.T) {
	pid, started := b529Foreign(t)
	command := "node /x/bin/code-forge.mjs worker --run r-old"
	home, _ := b486Env(t, []runProc{{uid: os.Getuid(), pid: pid, ppid: 4242, state: "S", start: started, command: command, rss: 1024}}, nil)
	runReadEnv = func(p runProc) map[string]string {
		if p.pid == pid {
			return map[string]string{"HOME": home}
		}
		return nil
	}
	b486Record(t, home, "r-old", `{"run_id":"r-old","status":"ended"}`)
	code, out, errText, handed := b486Call(t, "--apply")
	want := fmt.Sprintf("skipped worker pid %d 1.0 MB %s\nsweep: 1 processes, total 1.0 MB; 0 dead-run roots\n", pid, command)
	if handed != nil || code != 0 || errText != "" || out != want {
		t.Fatalf("code=%d err=%q handed=%v out:\n%s\nwant:\n%s", code, errText, handed, out, want)
	}
	time.Sleep(50 * time.Millisecond)
	if !b529Alive(pid, started) {
		t.Fatal("the sweep stopped a process outside the test")
	}
}

// TestB529BuiltBinaryRefusesWithoutEnv: the binary the test helper builds
// keeps the guard even when it runs with an environment that does not name
// the test root: `run end` on a run pinned to a foreign process does not
// stop it.
func TestB529BuiltBinaryRefusesWithoutEnv(t *testing.T) {
	binary := b511BuildNative(t)
	pid, started := b529Foreign(t)
	f := newRunFixture(t)
	f.record(t, "r-b529", "active", fmt.Sprintf(`,"worker":{"pid":%d,"started_at":%q},"blocks":{}`, pid, started))
	cmd := exec.Command(binary, "run", "end", "--run", "r-b529")
	cmd.Env = []string{"HOME=" + f.home, "PATH=/usr/bin:/bin", "CODE_FORGE_NODE=" + filepath.Join(f.home, "missing-node"), "CODE_FORGE_NO_ERROR_LOG=1"}
	out, _ := cmd.CombinedOutput()
	time.Sleep(50 * time.Millisecond)
	if !b529Alive(pid, started) {
		t.Fatalf("the built binary stopped a foreign process; output:\n%s", out)
	}
	if !strings.Contains(string(out), "EPERM") {
		t.Fatalf("output does not report the refused kill:\n%s", out)
	}
}
