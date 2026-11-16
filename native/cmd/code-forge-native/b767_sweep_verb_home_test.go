package main

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// b767Orphan starts a real orphan of this uid (parent init, the test binary
// sleeping as TestB754Sleeper) whose CODE_FORGE_OWNER_PID names a dead
// process and whose HOME is home ("" for no HOME).
func b767Orphan(t *testing.T, home string) int {
	t.Helper()
	gone := exec.Command("/usr/bin/true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", `"$B767_SELF" -test.run='^TestB754Sleeper$' >/dev/null 2>&1 </dev/null & echo $!`)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "B767_SELF=" + os.Args[0], "B754_SLEEPER=1", "CODE_FORGE_OWNER_PID=" + strconv.Itoa(gone.Process.Pid), "CODE_FORGE_OWNER_START_TIME=unknown"}
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
		for _, p := range runReadProcesses() {
			if p.pid == pid && p.ppid == 1 && runProcessEnv(p)["CODE_FORGE_OWNER_PID"] != "" {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("pid %d never showed as an orphan with its markers", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestB767SweepVerbOrphansOfThisHomeOnly: two temp HOMEs, a real orphan
// whose owner died in each, and one with no HOME. `code-forge sweep`'s scan
// in HOME B (markers read from the real processes) lists only HOME B's
// orphan: a live process of HOME A or of no HOME is not its to stop. The
// scan only lists (no --apply); nothing is signalled.
func TestB767SweepVerbOrphansOfThisHomeOnly(t *testing.T) {
	homeA, homeB := t.TempDir(), t.TempDir()
	orphanA := b767Orphan(t, homeA)
	orphanB := b767Orphan(t, homeB)
	noHome := b767Orphan(t, "")
	ours := map[int]bool{orphanA: true, orphanB: true, noHome: true}
	beforeRows, beforeEnv := runReadProcesses, runReadEnv
	t.Cleanup(func() { runReadProcesses, runReadEnv = beforeRows, beforeEnv })
	runReadEnv = nil
	// the live table narrowed to the test's orphans: no other process is judged
	runReadProcesses = func() []runProc {
		rows := []runProc{}
		for _, p := range beforeRows() {
			if ours[p.pid] {
				rows = append(rows, p)
			}
		}
		return rows
	}
	scan := func(home string) map[int]string {
		report, reason := sweepScan(home, t.TempDir())
		if report == nil {
			t.Fatalf("sweepScan(%s) handed over: %v", home, reason)
		}
		listed := map[int]string{}
		for _, item := range report.items {
			listed[item.p.pid] = item.kind
		}
		return listed
	}
	listed := scan(homeB)
	if listed[orphanB] != "orphan" {
		t.Errorf("HOME B's orphan %d not listed: %v", orphanB, listed)
	}
	if _, ok := listed[orphanA]; ok {
		t.Errorf("HOME B's sweep lists HOME A's live orphan %d", orphanA)
	}
	if _, ok := listed[noHome]; ok {
		t.Errorf("HOME B's sweep lists orphan %d with no HOME", noHome)
	}
	if listed := scan(homeA); listed[orphanA] != "orphan" || len(listed) != 1 {
		t.Errorf("HOME A's sweep lists %v, want only %d", listed, orphanA)
	}
}
