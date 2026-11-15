package worker

import (
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// B747: the worker's orphan sweep read the LAST environment variable of a
// process with ps's row end ("\n") in its value, so a run-id filtered sweep
// never matched when CODE_FORGE_RUN_ID came last.

// TestB747Helper is the child process the tests below start.
func TestB747Helper(t *testing.T) {
	if os.Getenv("B747_HELPER") != "1" {
		t.Skip("child process only")
	}
	time.Sleep(30 * time.Second)
}

// b747Spawn starts the test binary (ps -E shows no environment of a platform
// binary) with env exactly last, and returns its real process row.
func b747Spawn(t *testing.T, env ...string) ProcInfo {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("markers are read on darwin and linux only")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestB747Helper$")
	cmd.Env = append([]string{"B747_HELPER=1", "PATH=/usr/bin:/bin"}, env...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	for i := 0; i < 200; i++ {
		for _, p := range readProcessesPS() {
			if p.PID == cmd.Process.Pid && ReadProcessEnv(p) != nil {
				return p
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pid %d not in the process table", cmd.Process.Pid)
	return ProcInfo{}
}

// TestB747ReadProcessEnvLastVar: each marker is read whole when it is last,
// a value with spaces included.
func TestB747ReadProcessEnvLastVar(t *testing.T) {
	start := "Sun Nov 15 10:00:00 2026"
	cases := []struct {
		env  []string
		want map[string]string
	}{
		{[]string{"HOME=/h/run", "B747_SPACED=a b  c", "CODE_FORGE_OWNER_PID=4747", "CODE_FORGE_OWNER_START_TIME=" + start, "CODE_FORGE_RUN_ID=r-b747-last"},
			map[string]string{"HOME": "/h/run", "CODE_FORGE_OWNER_PID": "4747", "CODE_FORGE_OWNER_START_TIME": start, "CODE_FORGE_RUN_ID": "r-b747-last"}},
		{[]string{"CODE_FORGE_RUN_ID=r-b747-start", "HOME=/h/start", "CODE_FORGE_OWNER_PID=4748", "CODE_FORGE_OWNER_START_TIME=" + start},
			map[string]string{"HOME": "/h/start", "CODE_FORGE_OWNER_PID": "4748", "CODE_FORGE_OWNER_START_TIME": start, "CODE_FORGE_RUN_ID": "r-b747-start"}},
		{[]string{"HOME=/h/pid", "CODE_FORGE_RUN_ID=r-b747-pid", "CODE_FORGE_OWNER_PID=4749"},
			map[string]string{"HOME": "/h/pid", "CODE_FORGE_OWNER_PID": "4749", "CODE_FORGE_RUN_ID": "r-b747-pid"}},
	}
	for i, c := range cases {
		got := ReadProcessEnv(b747Spawn(t, c.env...))
		if len(got) != len(c.want) {
			t.Fatalf("case %d: markers %#v, want %#v", i, got, c.want)
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Fatalf("case %d: %s = %q, want %q", i, k, got[k], v)
			}
		}
	}
}

// TestB747RunIDSweepFindsLastVarOrphan: a run-id filtered sweep proves the
// orphan whose CODE_FORGE_RUN_ID is its last variable (the process is shown
// to the sweep as init-parented; its markers are read from the real process).
func TestB747RunIDSweepFindsLastVarOrphan(t *testing.T) {
	real := b747Spawn(t, "HOME=/h/orphan", "CODE_FORGE_OWNER_PID=99999999", "CODE_FORGE_RUN_ID=r-b747-sweep")
	shown := real
	shown.PPID = 1
	s := &OrphanSweep{
		RunID:     "r-b747-sweep",
		Processes: func() []ProcInfo { return []ProcInfo{shown} },
		ReadEnv: func(p ProcInfo) map[string]string {
			p.PPID = real.PPID
			return ReadProcessEnv(p)
		},
		Alive: func(int) bool { return false },
		Kill:  func(int, syscall.Signal) error { return nil },
		UID:   real.UID, Self: -1, Parent: -1,
	}
	got := s.Orphans()
	if len(got) != 1 || got[0].PID != real.PID {
		t.Fatalf("orphans %+v, want pid %d", got, real.PID)
	}
}
