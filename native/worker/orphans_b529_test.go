package worker

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// B529: the orphan sweep with its real signal path (no Kill seam) never stops
// a process outside this test, even one its table proves an orphan of the
// run: a `sleep 30` whose parent is init.
func TestB529OrphanSweepLeavesForeignProcess(t *testing.T) {
	out, err := exec.Command("/bin/sh", "-c", "/bin/sleep 30 >/dev/null 2>&1 </dev/null & echo $!").Output()
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	start := ""
	for i := 0; i < 500 && start == ""; i++ {
		ppid, _ := exec.Command("/bin/ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
		lstart, _ := exec.Command("/bin/ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
		if strings.TrimSpace(string(ppid)) == "1" {
			start = strings.Join(strings.Fields(string(lstart)), " ")
		} else {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if start == "" {
		t.Fatal("the foreign sleep never got init as parent")
	}
	row := ProcInfo{UID: os.Getuid(), PID: pid, PPID: 1, State: "S", Start: start, Command: "sleep 30"}
	s := &OrphanSweep{
		RunID:     "r-b529",
		Processes: func() []ProcInfo { return []ProcInfo{row} },
		ReadEnv: func(ProcInfo) map[string]string {
			return map[string]string{"CODE_FORGE_OWNER_PID": "999999", "CODE_FORGE_RUN_ID": "r-b529"}
		},
		Alive:   func(int) bool { return false },
		GraceMs: 1,
		Sleep:   func(time.Duration) {},
	}
	if got := s.Orphans(); len(got) != 1 || got[0].PID != pid {
		t.Fatalf("orphans = %+v", got)
	}
	s.Sweep()
	time.Sleep(50 * time.Millisecond)
	if syscall.Kill(pid, 0) != nil {
		t.Fatal("the orphan sweep stopped a process outside the test")
	}
	state, _ := exec.Command("/bin/ps", "-o", "state=", "-p", strconv.Itoa(pid)).Output()
	if strings.HasPrefix(strings.TrimSpace(string(state)), "Z") || len(strings.TrimSpace(string(state))) == 0 {
		t.Fatalf("foreign process state %q", state)
	}
}
