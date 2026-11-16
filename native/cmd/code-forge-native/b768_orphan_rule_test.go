package main

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"

	"github.com/ricardov03/code-forge/native/worker"
)

// TestB768ThreeSweepsAgree: the three Go sweeps (the worker's orphan sweep,
// the sweep verb's scan, run start's worker and orphan search) take the same
// orphans on one table of marker cases: another HOME, no HOME, a bad owner
// pid, and a good owner of this HOME whose owner died. The process table and
// the markers are fakes (seams); only listing runs, nothing is signalled.
func TestB768ThreeSweepsAgree(t *testing.T) {
	home, other := t.TempDir(), t.TempDir()
	gone := exec.Command("/usr/bin/true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	dead := strconv.Itoa(gone.Process.Pid)
	cases := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"other HOME", map[string]string{"HOME": other, "CODE_FORGE_OWNER_PID": dead}, false},
		{"no HOME", map[string]string{"CODE_FORGE_OWNER_PID": dead}, false},
		{"empty HOME", map[string]string{"HOME": "", "CODE_FORGE_OWNER_PID": dead}, false},
		{"no markers", nil, false},
		{"no owner pid", map[string]string{"HOME": home}, false},
		{"owner pid 0", map[string]string{"HOME": home, "CODE_FORGE_OWNER_PID": "0"}, false},
		{"owner pid leading zero", map[string]string{"HOME": home, "CODE_FORGE_OWNER_PID": "0" + dead}, false},
		{"owner pid negative", map[string]string{"HOME": home, "CODE_FORGE_OWNER_PID": "-" + dead}, false},
		{"owner pid text", map[string]string{"HOME": home, "CODE_FORGE_OWNER_PID": "abc"}, false},
		{"owner pid with space", map[string]string{"HOME": home, "CODE_FORGE_OWNER_PID": dead + " "}, false},
		{"good owner", map[string]string{"HOME": home, "CODE_FORGE_OWNER_PID": dead, "CODE_FORGE_OWNER_START_TIME": "unknown"}, true},
		{"good owner, HOME with slash", map[string]string{"HOME": home + "/", "CODE_FORGE_OWNER_PID": dead}, false},
	}
	uid := os.Getuid()
	const base = 990001
	start := "Sun Nov 15 10:00:00 2026"
	rows := []runProc{}
	wrows := []worker.ProcInfo{}
	envOf := map[int]map[string]string{}
	for i, c := range cases {
		pid := base + i
		rows = append(rows, runProc{uid: uid, pid: pid, ppid: 1, state: "S", start: start, command: "b768-orphan"})
		wrows = append(wrows, worker.ProcInfo{UID: uid, PID: pid, PPID: 1, State: "S", Start: start, Command: "b768-orphan"})
		envOf[pid] = c.env
	}
	beforeRows, beforeEnv, beforeScoped := runReadProcesses, runReadEnv, runSweepScoped
	t.Cleanup(func() { runReadProcesses, runReadEnv, runSweepScoped = beforeRows, beforeEnv, beforeScoped })
	runReadProcesses = func() []runProc { return append([]runProc{}, rows...) }
	runReadEnv = func(p runProc) map[string]string { return envOf[p.pid] }
	runSweepScoped = func() bool { return false }

	runStart := map[int]bool{}
	for _, p := range runSweepItems(home) {
		runStart[p.pid] = true
	}
	verb := map[int]bool{}
	report, reason := sweepScan(home, t.TempDir())
	if report == nil {
		t.Fatalf("sweepScan handed over: %v", reason)
	}
	for _, item := range report.items {
		if item.kind == "orphan" {
			verb[item.p.pid] = true
		}
	}
	sweep := &worker.OrphanSweep{
		Home:      home,
		Processes: func() []worker.ProcInfo { return append([]worker.ProcInfo{}, wrows...) },
		ReadEnv:   func(p worker.ProcInfo) map[string]string { return envOf[p.PID] },
		Kill: func(pid int, sig syscall.Signal) error {
			t.Errorf("signal %v to %d: the test only lists", sig, pid)
			return nil
		},
		UID:    uid,
		Self:   os.Getpid(),
		Parent: os.Getppid(),
	}
	workerSweep := map[int]bool{}
	for _, p := range sweep.Orphans() {
		workerSweep[p.PID] = true
	}
	for i, c := range cases {
		pid := base + i
		_, rule := worker.OrphanOwner(c.env, home)
		got := map[string]bool{"OrphanOwner": rule, "worker sweep": workerSweep[pid], "sweep verb": verb[pid], "run start": runStart[pid]}
		for site, listed := range got {
			if listed != c.want {
				t.Errorf("%s: %s took=%v, want %v", c.name, site, listed, c.want)
			}
		}
	}
	if n := len(runStart); n != 1 || len(verb) != 1 || len(workerSweep) != 1 {
		t.Errorf("listed run start %v, sweep verb %v, worker sweep %v; want only the good owner", runStart, verb, workerSweep)
	}
}
