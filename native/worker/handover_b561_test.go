package worker

import (
	"os"
	"path/filepath"
	"testing"
)

// B561: a worker handing its run to Node swaps its announcements for the
// hand-over one. A waiter (Live) still sees the queue served; Node's boot
// (Registry with prune) finds nothing live and removes it.
func TestB561HandOverAnnouncement(t *testing.T) {
	root := t.TempDir()
	probes := fakeProbes(map[int]string{4242: "Thu Nov 12 13:40:00 2026"})
	w := New(Opts{RunID: "r1", Root: root, Pid: 4242, Probes: probes})
	if err := w.Announce(); err != nil {
		t.Fatal(err)
	}
	if err := w.HandOver(); err != nil {
		t.Fatal(err)
	}
	names, _ := os.ReadDir(WorkersDir(root))
	if len(names) != 1 || names[0].Name() != "4242-Thu Nov 12 13:40:00 2026 hand-over.json" {
		t.Fatalf("registry %v", names)
	}
	data, _ := os.ReadFile(filepath.Join(WorkersDir(root), names[0].Name()))
	want := `{"pid":4242,"start_time":"Thu Nov 12 13:40:00 2026 hand-over","run":"r1","started_at":"` + ISO(fixedNow) + `","heartbeat_at":"` + ISO(fixedNow) + `"}` + "\n"
	if string(data) != want {
		t.Fatalf("hand-over entry %q, want %q", data, want)
	}
	if info, _ := os.Stat(filepath.Join(WorkersDir(root), names[0].Name())); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
	if exists(filepath.Join(QueueDir(root), WorkerFile)) {
		t.Fatal("worker.json kept: Node's boot would count it")
	}
	live, err := Live(root, LiveOpts{}, probes)
	if err != nil || live == nil || live.Get("run") != "r1" {
		t.Fatalf("waiter sees %v %v, want the hand-over entry live", live, err)
	}
	entries, err := Registry(root, true, probes)
	if err != nil || len(entries) != 0 {
		t.Fatalf("Node's boot sees %v %v, want nothing live", entries, err)
	}
	if names, _ := os.ReadDir(WorkersDir(root)); len(names) != 0 {
		t.Fatalf("boot did not prune %v", names)
	}
}

// Without a ps start time Node's boot could not prune the entry: HandOver is
// Release. A failed exec withdraws the hand-over entry.
func TestB561HandOverFallbacks(t *testing.T) {
	root := t.TempDir()
	start := "Thu Nov 12 13:40:00 2026"
	procs := map[int]string{4242: start}
	probes := fakeProbes(procs)
	w := New(Opts{RunID: "r1", Root: root, Pid: 4242, Probes: probes})
	if err := w.Announce(); err != nil {
		t.Fatal(err)
	}
	if err := w.HandOver(); err != nil {
		t.Fatal(err)
	}
	w.RetractHandOver()
	if names, _ := os.ReadDir(WorkersDir(root)); len(names) != 0 {
		t.Fatalf("after a failed exec %v", names)
	}

	probes.StartTime = func(int) *string { return nil }
	w = New(Opts{RunID: "r1", Root: root, Pid: 4242, Probes: probes})
	if err := w.Announce(); err != nil {
		t.Fatal(err)
	}
	if err := w.HandOver(); err != nil {
		t.Fatal(err)
	}
	if names, _ := os.ReadDir(WorkersDir(root)); len(names) != 0 {
		t.Fatalf("no ps start time: %v", names)
	}
	if exists(filepath.Join(QueueDir(root), WorkerFile)) {
		t.Fatal("worker.json kept")
	}
}

// Loop.Once that hands over leaves the hand-over entry; a plain drain
// releases everything.
func TestB561OnceHandOverKeepsAnnouncement(t *testing.T) {
	clock := fixedNow
	fx, w, _ := b497Fixture(t, &clock)
	b489Unported(t, fx, 0)
	l := b489Loop(t, fx, w, 1)
	l.Worker.o.Pid = 4242
	l.Worker.o.Probes = fakeProbes(map[int]string{4242: "Thu Nov 12 13:40:00 2026"})
	if err := l.Worker.Announce(); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Once(); err != nil {
		t.Fatal(err)
	}
	if l.Handoff() == "" {
		t.Fatal("no hand-over")
	}
	names, _ := os.ReadDir(WorkersDir(fx.root))
	if len(names) != 1 || names[0].Name() != "4242-Thu Nov 12 13:40:00 2026 hand-over.json" {
		t.Fatalf("registry %v", names)
	}
}
