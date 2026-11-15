package worker

import (
	"os"
	"runtime"
	"syscall"
	"testing"
)

// B757: the worker's ps -E reader let a marker value run over a line end
// (to the next " NAME=" anywhere in the text), while the run sweep (and Node's
// readProcessEnv) stop a value at a line end. The testdata files are the exact
// bytes `ps -E -ww -o pid=,ppid=,lstart=,command=` printed on darwin
// (LC_ALL=C) for a real process 34871 whose parent is init (b757-ps-E-row.txt),
// and for the same process batched with a second one, 34900
// (b757-ps-E-batch.txt); b757-ps-table.txt is the `ps -A` table of both and of
// the live owner 34838.

func b757Read(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func b757Rows(t *testing.T) (orphan, owner ProcInfo, rows []ProcInfo) {
	t.Helper()
	rows = ParseProcesses(b757Read(t, "b757-ps-table.txt"))
	for _, p := range rows {
		switch p.PID {
		case 34871:
			orphan = p
		case 34838:
			owner = p
		}
	}
	if orphan.PID == 0 || owner.PID == 0 {
		t.Fatalf("table rows %+v", rows)
	}
	return orphan, owner, rows
}

// b757PS makes the worker's ps -E call answer out.
func b757PS(t *testing.T, out string) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("ps -E is read on darwin only")
	}
	old := psEnvOutput
	psEnvOutput = func(int) (string, error) { return out, nil }
	t.Cleanup(func() { psEnvOutput = old })
}

// Node's readProcessEnv on the same bytes (node, the reaper.mjs regex):
// row  -> all four markers; batch -> no CODE_FORGE_OWNER_START_TIME (its value
// reaches a line end before the text ends).
var b757Row = map[string]string{"HOME": "/h/b757", "CODE_FORGE_OWNER_PID": "34838", "CODE_FORGE_RUN_ID": "r-b757", "CODE_FORGE_OWNER_START_TIME": "Sun Nov 15 17:39:11 2026"}
var b757Batch = map[string]string{"HOME": "/h/b757", "CODE_FORGE_OWNER_PID": "34838", "CODE_FORGE_RUN_ID": "r-b757"}

func b757Same(t *testing.T, label string, got, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: markers %q, want %q", label, got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: %s = %q, want %q", label, k, got[k], v)
		}
	}
}

// TestB757ReadProcessEnvStopsAtLineEnd: a marker value never crosses into the
// next row.
func TestB757ReadProcessEnvStopsAtLineEnd(t *testing.T) {
	orphan, _, _ := b757Rows(t)
	b757PS(t, b757Read(t, "b757-ps-E-row.txt"))
	b757Same(t, "row", ReadProcessEnv(orphan), b757Row)
	b757PS(t, b757Read(t, "b757-ps-E-batch.txt"))
	b757Same(t, "batch", ReadProcessEnv(orphan), b757Batch)
}

// TestB757RunIDSweepKeepsLiveOwnersChild: the sweep by run id does not prove
// 34871 an orphan: its owner 34838 is alive and started when the marker says
// (a start time read across the row end differed and made it "dead").
func TestB757RunIDSweepKeepsLiveOwnersChild(t *testing.T) {
	orphan, _, rows := b757Rows(t)
	for _, name := range []string{"b757-ps-E-row.txt", "b757-ps-E-batch.txt"} {
		b757PS(t, b757Read(t, name))
		s := &OrphanSweep{
			RunID:     "r-b757",
			Processes: func() []ProcInfo { return rows },
			Alive:     func(int) bool { return true },
			Kill:      func(int, syscall.Signal) error { t.Fatal("signal sent"); return nil },
			UID:       orphan.UID, Self: -1, Parent: -1,
		}
		if got := s.Orphans(); len(got) != 0 {
			t.Fatalf("%s: orphans %+v, want none (owner alive)", name, got)
		}
	}
}

// TestB757EnvMarkerNodeRule: the shared parser gives what Node's regex gives
// (each want was printed by node with the reaper.mjs regex on the same text).
func TestB757EnvMarkerNodeRule(t *testing.T) {
	cases := []struct {
		text, name, want string
		ok               bool
	}{
		{"A=1 CODE_FORGE_RUN_ID=r1\nB=2", "CODE_FORGE_RUN_ID", "r1", true},
		{"CODE_FORGE_RUN_ID=r1\nx CODE_FORGE_RUN_ID=r2", "CODE_FORGE_RUN_ID", "r2", true},
		{"HOME=/h\rx PATH=/bin", "HOME", "", false},
		{"HOME=/h PATH=/b\nHOME=/h2", "HOME", "/h", true},
		{"X=a CODE_FORGE_RUN_ID=r3 Y=1", "CODE_FORGE_RUN_ID", "r3", true},
		{"HOME=a b", "HOME", "", false},
		{"XHOME=1 HOME=2", "HOME", "2", true},
	}
	for _, c := range cases {
		got, ok := EnvMarker(c.text, c.name)
		if ok != c.ok || got != c.want {
			t.Fatalf("EnvMarker(%q, %s) = %q %v, want %q %v", c.text, c.name, got, ok, c.want, c.ok)
		}
	}
}

// TestB757EnvFromPsRecordedBytes: the shared ps -E reader on the recorded
// bytes, and nil when the row is not the process.
func TestB757EnvFromPsRecordedBytes(t *testing.T) {
	const start, command = "Sun Nov 15 17:39:12 2026", "./helper/b757-sleeper"
	b757Same(t, "row", EnvFromPs(34871, 1, start, command, b757Read(t, "b757-ps-E-row.txt")), b757Row)
	b757Same(t, "batch", EnvFromPs(34871, 1, start, command, b757Read(t, "b757-ps-E-batch.txt")), b757Batch)
	if got := EnvFromPs(34871, 2, start, command, b757Read(t, "b757-ps-E-row.txt")); got != nil {
		t.Fatalf("other ppid read %q", got)
	}
	if got := EnvFromPs(34871, 1, start, "./other", b757Read(t, "b757-ps-E-row.txt")); got != nil {
		t.Fatalf("other command read %q", got)
	}
}
