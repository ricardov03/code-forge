package main

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestB754Sleeper is the helper process of the tests below (this test
// binary, not a platform binary: macOS ps -E shows no environment of a
// system binary such as /bin/sleep). It only sleeps.
func TestB754Sleeper(t *testing.T) {
	if os.Getenv("B754_SLEEPER") != "1" {
		t.Skip("helper process only")
	}
	time.Sleep(30 * time.Second)
}

// b754Orphan starts a real orphan of this uid (parent init) whose
// CODE_FORGE_OWNER_PID names a dead process and whose HOME is home ("" for
// no HOME, B746): the kind run start's sweep in that home stops. The test
// does not own it as a descendant, so a binary built for a test may not
// signal it.
func b754Orphan(t *testing.T, home string) int {
	t.Helper()
	gone := exec.Command("/usr/bin/true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", `"$B754_SELF" -test.run='^TestB754Sleeper$' >/dev/null 2>&1 </dev/null & echo $!`)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "B754_SELF=" + os.Args[0], "B754_SLEEPER=1", "CODE_FORGE_OWNER_PID=" + strconv.Itoa(gone.Process.Pid), "CODE_FORGE_OWNER_START_TIME=unknown"}
	if home != "" {
		cmd.Env = append(cmd.Env, "HOME="+home)
	}
	out, err := cmd.Output()
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
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatalf("pid %d still has parent %q", pid, ppid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// b754Worker starts a child of this test that ps shows as the Go worker of
// a run with no record (`code-forge-native worker --run r-b754-none`) under
// home: the kind run start's sweep stops.
func b754Worker(t *testing.T, home string) int {
	t.Helper()
	cmd := exec.Command("/bin/bash", "-c", `exec -a "code-forge-native worker --run r-b754-none" "$B754_SELF" -test.run='^TestB754Sleeper$'`)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "B754_SELF=" + os.Args[0], "B754_SLEEPER=1"}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		command, _ := exec.Command("/bin/ps", "-ww", "-o", "command=", "-p", strconv.Itoa(cmd.Process.Pid)).Output()
		if strings.HasPrefix(string(command), "code-forge-native worker --run r-b754-none") {
			return cmd.Process.Pid
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker look-alike shows %q", command)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestB754SweepScopedToTestDescendants: run start's worker and orphan search
// on the real process table. In a binary built for a test (which may signal
// only the test's descendants) it reads no other process's environment and
// lists only the test's own stale worker, however many processes the
// machine runs; a product binary still reads them all and lists the real
// orphan whose owner died.
func TestB754SweepScopedToTestDescendants(t *testing.T) {
	home := t.TempDir()
	orphan := b754Orphan(t, home)
	worker := b754Worker(t, home)
	before, beforeScoped := runReadEnv, runSweepScoped
	t.Cleanup(func() { runReadEnv, runSweepScoped = before, beforeScoped })
	var mu sync.Mutex
	read := map[int]bool{}
	runReadEnv = func(p runProc) map[string]string {
		mu.Lock()
		read[p.pid] = true
		mu.Unlock()
		return runProcessEnv(p)
	}
	listed := func(items []runProc, pid int) bool {
		for _, item := range items {
			if item.pid == pid {
				return true
			}
		}
		return false
	}

	runSweepScoped = func() bool { return true }
	items := runSweepItems(home)
	if !listed(items, worker) {
		t.Errorf("test build: own stale worker %d not listed: %v", worker, items)
	}
	if listed(items, orphan) || read[orphan] {
		t.Errorf("test build: foreign orphan %d listed=%v env read=%v", orphan, listed(items, orphan), read[orphan])
	}
	initParented := 0
	for _, p := range runReadProcesses() {
		if read[p.pid] && p.ppid == 1 {
			initParented++
		}
	}
	if initParented != 0 || len(read) != 1 {
		t.Errorf("test build read %d environments (%d of init-parented processes), want only the worker's", len(read), initParented)
	}

	read = map[int]bool{}
	runSweepScoped = func() bool { return false }
	items = runSweepItems(home)
	if !listed(items, orphan) || !listed(items, worker) {
		t.Errorf("product: orphan %d listed=%v, worker %d listed=%v", orphan, listed(items, orphan), worker, listed(items, worker))
	}
	if !read[orphan] {
		t.Errorf("product: orphan %d environment not read", orphan)
	}
}
