package worker

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// b767Orphan starts a real orphan of this uid (parent init, the test binary
// sleeping as TestB747Helper) whose CODE_FORGE_OWNER_PID names a dead
// process, run r-b767, and whose HOME is home ("" for no HOME).
func b767Orphan(t *testing.T, home string) int {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("markers are read on darwin and linux only")
	}
	gone := exec.Command("/usr/bin/true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", `"$B767_SELF" -test.run='^TestB747Helper$' >/dev/null 2>&1 </dev/null & echo $!`)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "B767_SELF=" + os.Args[0], "B747_HELPER=1", "CODE_FORGE_OWNER_PID=" + strconv.Itoa(gone.Process.Pid), "CODE_FORGE_OWNER_START_TIME=unknown", "CODE_FORGE_RUN_ID=r-b767"}
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
		for _, p := range readProcessesPS() {
			if p.PID == pid && p.PPID == 1 && ReadProcessEnv(p)["CODE_FORGE_OWNER_PID"] != "" {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("pid %d never showed as an orphan with its markers", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestB767WorkerSweepOrphansOfThisHomeOnly: two temp HOMEs, a real orphan
// whose owner died in each, and one with no HOME. The worker's orphan sweep
// in HOME B (markers read from the real processes) lists only HOME B's
// orphan: a live process of HOME A or of no HOME is not its to stop. A
// worker with no HOME of its own takes none. The sweep only lists; any
// signal fails the test.
func TestB767WorkerSweepOrphansOfThisHomeOnly(t *testing.T) {
	homeA, homeB := t.TempDir(), t.TempDir()
	orphanA := b767Orphan(t, homeA)
	orphanB := b767Orphan(t, homeB)
	noHome := b767Orphan(t, "")
	ours := map[int]bool{orphanA: true, orphanB: true, noHome: true}
	sweep := func(home, runID string) map[int]bool {
		s := &OrphanSweep{
			RunID: runID,
			Home:  home,
			// the live table narrowed to the test's orphans: no other process is judged
			Processes: func() []ProcInfo {
				rows := []ProcInfo{}
				for _, p := range readProcessesPS() {
					if ours[p.PID] {
						rows = append(rows, p)
					}
				}
				return rows
			},
			Kill: func(pid int, sig syscall.Signal) error { t.Fatalf("signal %v to %d", sig, pid); return nil },
		}
		listed := map[int]bool{}
		for _, p := range s.Orphans() {
			listed[p.PID] = true
		}
		return listed
	}
	for _, runID := range []string{"", "r-b767"} {
		listed := sweep(homeB, runID)
		if !listed[orphanB] {
			t.Errorf("run %q: HOME B's orphan %d not listed", runID, orphanB)
		}
		if listed[orphanA] {
			t.Errorf("run %q: HOME B's sweep lists HOME A's live orphan %d", runID, orphanA)
		}
		if listed[noHome] {
			t.Errorf("run %q: HOME B's sweep lists orphan %d with no HOME", runID, noHome)
		}
	}
	if listed := sweep(homeA, ""); !listed[orphanA] || listed[orphanB] || listed[noHome] {
		t.Errorf("HOME A's sweep lists %v, want only %d", listed, orphanA)
	}
	t.Setenv("HOME", "")
	if listed := sweep("", ""); len(listed) != 0 {
		t.Errorf("a sweep with no HOME lists %v, want none", listed)
	}
}

// TestB767OrphanOwner: the shared rule on markers alone.
func TestB767OrphanOwner(t *testing.T) {
	cases := []struct {
		env   map[string]string
		home  string
		owner string
		ok    bool
	}{
		{map[string]string{"HOME": "/h/b", "CODE_FORGE_OWNER_PID": "42"}, "/h/b", "42", true},
		{map[string]string{"HOME": "/h/a", "CODE_FORGE_OWNER_PID": "42"}, "/h/b", "", false},
		{map[string]string{"CODE_FORGE_OWNER_PID": "42"}, "/h/b", "", false},
		{map[string]string{"HOME": "", "CODE_FORGE_OWNER_PID": "42"}, "", "", false},
		{map[string]string{"CODE_FORGE_OWNER_PID": "42"}, "", "", false},
		{map[string]string{"HOME": "/h/b", "CODE_FORGE_OWNER_PID": "042"}, "/h/b", "", false},
		{map[string]string{"HOME": "/h/b"}, "/h/b", "", false},
		{nil, "/h/b", "", false},
	}
	for i, c := range cases {
		if owner, ok := OrphanOwner(c.env, c.home); owner != c.owner || ok != c.ok {
			t.Errorf("case %d: OrphanOwner(%v, %q) = %q, %v; want %q, %v", i, c.env, c.home, owner, ok, c.owner, c.ok)
		}
	}
}
