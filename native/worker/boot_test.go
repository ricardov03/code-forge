package worker

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
)

var fixedNow = time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)

// fakeProcs: pid -> start time; a pid not in the map is dead.
func fakeProbes(procs map[int]string) Probes {
	return Probes{
		Alive: func(pid int) bool { _, ok := procs[pid]; return ok },
		StartTime: func(pid int) *string {
			s, ok := procs[pid]
			if !ok {
				return nil
			}
			return &s
		},
		Now: func() time.Time { return fixedNow },
	}
}

type fixture struct {
	home, root string
}

func newFixture(t *testing.T, record string) fixture {
	t.Helper()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	root := filepath.Join(base, "repo")
	for _, d := range []string{filepath.Join(home, ".code-forge", "runs"), root} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home) // filelock's lock dir
	if record != "" {
		record = strings.ReplaceAll(record, "@WS@", root)
		if err := os.WriteFile(filepath.Join(home, ".code-forge", "runs", "r1.json"), []byte(record), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, ".code-forge", "runs", "r1.key"), bytes.Repeat([]byte{7}, 32), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return fixture{home, root}
}

const activeRecord = `{"run_id":"r1","project":"proj","workspace":"@WS@","status":"active"}`

func readFile(t *testing.T, file string) string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func writeJSON(t *testing.T, file, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestB485AnnounceWritesNodeBytes(t *testing.T) {
	f := newFixture(t, "")
	p := fakeProbes(map[int]string{4242: "Wed Nov 11 09:00:00 2026"})
	self, err := Announce(f.root, 4242, "r1", p)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"pid":4242,"start_time":"Wed Nov 11 09:00:00 2026","run":"r1","started_at":"2026-11-11T10:00:00.000Z","heartbeat_at":"2026-11-11T10:00:00.000Z"}` + "\n"
	entry := filepath.Join(f.root, ".code-forge", "workers", "4242-Wed Nov 11 09:00:00 2026.json")
	if got := readFile(t, entry); got != want {
		t.Fatalf("registry entry\n got %q\nwant %q", got, want)
	}
	if got := readFile(t, filepath.Join(f.root, ".code-forge", "queue", "worker.json")); got != want {
		t.Fatalf("worker.json\n got %q\nwant %q", got, want)
	}
	info, _ := os.Stat(entry)
	dir, _ := os.Stat(filepath.Dir(entry))
	if info.Mode().Perm() != 0o600 || dir.Mode().Perm() != 0o700 {
		t.Fatalf("modes: entry %v dir %v", info.Mode().Perm(), dir.Mode().Perm())
	}
	if self.Get("run") != "r1" {
		t.Fatalf("self %v", self)
	}
	live, err := Live(f.root, LiveOpts{}, p)
	if err != nil || live == nil || live.Get("pid") != float64(4242) {
		t.Fatalf("live %v %v", live, err)
	}
}

func TestB485AnnounceKeepsLiveLegacyOwnerAndBeatsOnlyItsOwn(t *testing.T) {
	f := newFixture(t, "")
	p := fakeProbes(map[int]string{4242: "S1", 5151: "S2"})
	legacy := `{"pid":5151,"start_time":"S2","run":"r0","started_at":"2026-11-11T09:59:00.000Z","heartbeat_at":"2026-11-11T09:59:00.000Z"}` + "\n"
	legacyFile := filepath.Join(f.root, ".code-forge", "queue", "worker.json")
	writeJSON(t, legacyFile, legacy)
	self, err := Announce(f.root, 4242, "r1", p)
	if err != nil {
		t.Fatal(err)
	}
	if readFile(t, legacyFile) != legacy {
		t.Fatal("a live legacy owner's worker.json was replaced")
	}
	later := fixedNow.Add(7 * time.Second)
	written, err := Beat(f.root, self, later)
	if err != nil || !written {
		t.Fatalf("beat %v %v", written, err)
	}
	entry := filepath.Join(f.root, ".code-forge", "workers", "4242-S1.json")
	want := `{"pid":4242,"start_time":"S1","run":"r1","started_at":"2026-11-11T10:00:00.000Z","heartbeat_at":"2026-11-11T10:00:07.000Z"}` + "\n"
	if got := readFile(t, entry); got != want {
		t.Fatalf("beat entry\n got %q\nwant %q", got, want)
	}
	if readFile(t, legacyFile) != legacy {
		t.Fatal("beat overwrote another owner's worker.json")
	}
	// Another worker took the registry file over: no beat is written.
	writeJSON(t, entry, `{"pid":4242,"start_time":"OTHER","run":"r1"}`)
	written, err = Beat(f.root, self, later)
	if err != nil || written {
		t.Fatalf("beat over another owner: %v %v", written, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(entry))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temp file left: %s", e.Name())
		}
	}
}

func TestB485RegistryPruneKeepsBadEntriesOccupied(t *testing.T) {
	f := newFixture(t, "")
	p := fakeProbes(map[int]string{4242: "S1"})
	dir := filepath.Join(f.root, ".code-forge", "workers")
	writeJSON(t, filepath.Join(dir, "9999-S9.json"), `{"pid":9999,"start_time":"S9","run":"r1","started_at":"2026-11-11T10:00:00.000Z","heartbeat_at":"2026-11-11T10:00:00.000Z"}`)
	writeJSON(t, filepath.Join(dir, "4242-OLD.json"), `{"pid":4242,"start_time":"OLD","run":"r1","started_at":"2026-11-11T10:00:00.000Z","heartbeat_at":"2026-11-11T10:00:00.000Z"}`)
	writeJSON(t, filepath.Join(dir, "garbage.json"), `not json`)
	entries, err := Registry(f.root, true, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Worker != nil || !entries[0].Live || filepath.Base(entries[0].File) != "garbage.json" {
		t.Fatalf("entries %+v", entries)
	}
	for _, gone := range []string{"9999-S9.json", "4242-OLD.json"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Fatalf("%s not pruned", gone)
		}
	}
}

func TestB485RegistryUndecidedDate(t *testing.T) {
	f := newFixture(t, "")
	writeJSON(t, filepath.Join(f.root, ".code-forge", "workers", "4242-S1.json"), `{"pid":4242,"start_time":"S1","run":"r1","started_at":"Nov 11 2026","heartbeat_at":"2026-11-11T10:00:00.000Z"}`)
	if _, err := Registry(f.root, true, fakeProbes(nil)); !IsUndecided(err) {
		t.Fatalf("want undecided, got %v", err)
	}
}

func bootOpts(f fixture, pid int, procs map[int]string) Opts {
	return Opts{Home: f.home, RunID: "r1", Root: f.root, Cfg: map[string]any{}, Pid: pid, Probes: fakeProbes(procs), HeartbeatMs: 100000}
}

func TestB485BootAnnouncesAndCleanStopRetracts(t *testing.T) {
	f := newFixture(t, activeRecord)
	w := New(bootOpts(f, 4242, map[int]string{4242: "S1"}))
	if err := w.Boot(); err != nil {
		t.Fatal(err)
	}
	if w.Repinned != nil {
		t.Fatalf("no pins, but repinned %+v", w.Repinned)
	}
	entry := filepath.Join(f.root, ".code-forge", "workers", "4242-S1.json")
	legacy := filepath.Join(f.root, ".code-forge", "queue", "worker.json")
	for _, file := range []string{entry, legacy} {
		if _, err := os.Stat(file); err != nil {
			t.Fatalf("not announced: %v", err)
		}
	}
	w.Stop()
	select {
	case <-w.Done():
	default:
		t.Fatal("Done not closed by Stop")
	}
	if err := w.Release(); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{entry, legacy} {
		if _, err := os.Stat(file); !os.IsNotExist(err) {
			t.Fatalf("%s left after a clean stop", file)
		}
	}
	// The boot lock was released: a second boot can take it.
	again := New(bootOpts(f, 4242, map[int]string{4242: "S1"}))
	if err := again.Boot(); err != nil {
		t.Fatal(err)
	}
	again.Stop()
	_ = again.Release()
}

func TestB485BootRefusesLiveWorkers(t *testing.T) {
	cases := []struct {
		name, file, body, cfg, want string
	}{
		{"same run at limit 1", "workers/5151-S2.json", `{"pid":5151,"start_time":"S2","run":"r1","started_at":"2026-11-11T10:00:00.000Z","heartbeat_at":"2026-11-11T10:00:00.000Z"}`, "", "review.workers limit 1: a live or unreadable worker entry already serves this queue"},
		{"other run", "workers/5151-S2.json", `{"pid":5151,"start_time":"S2","run":"r0","started_at":"2026-11-11T10:00:00.000Z","heartbeat_at":"2026-11-11T10:00:00.000Z"}`, "3", "review.workers limit 3: a live or unreadable worker entry already serves this queue"},
		{"unreadable entry", "workers/x.json", `{`, "3", "review.workers limit 3: a live or unreadable worker entry already serves this queue"},
		{"legacy other run", "queue/worker.json", `{"pid":5151,"start_time":"S2","run":"r0","heartbeat_at":"2026-11-11T10:00:00.000Z"}`, "3", "review.workers limit 3: a live or unreadable worker entry already serves this queue"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, activeRecord)
			writeJSON(t, filepath.Join(f.root, ".code-forge", c.file), c.body)
			o := bootOpts(f, 4242, map[int]string{4242: "S1", 5151: "S2"})
			if c.cfg != "" {
				o.Cfg = map[string]any{"review": map[string]any{"workers": float64(3)}}
			}
			w := New(o)
			err := w.Boot()
			var we *Error
			if !errors.As(err, &we) || we.Code != "worker-running" || we.Message != c.want {
				t.Fatalf("got %v", err)
			}
			if _, err := os.Stat(filepath.Join(f.root, ".code-forge", "workers", "4242-S1.json")); !os.IsNotExist(err) {
				t.Fatal("a refused boot announced itself")
			}
		})
	}
	// A second worker of the same run is admitted under review.workers 2.
	f := newFixture(t, activeRecord)
	writeJSON(t, filepath.Join(f.root, ".code-forge", "workers", "5151-S2.json"), `{"pid":5151,"start_time":"S2","run":"r1","started_at":"2026-11-11T10:00:00.000Z","heartbeat_at":"2026-11-11T10:00:00.000Z"}`)
	o := bootOpts(f, 4242, map[int]string{4242: "S1", 5151: "S2"})
	o.Cfg = map[string]any{"review": map[string]any{"workers": float64(2)}}
	var counted int
	o.AfterCount = func(n int) { counted = n }
	w := New(o)
	if err := w.Boot(); err != nil {
		t.Fatal(err)
	}
	if counted != 1 {
		t.Fatalf("counted %d", counted)
	}
	w.Stop()
	_ = w.Release()
}

func TestB485BootPins(t *testing.T) {
	t.Run("live pin refuses", func(t *testing.T) {
		f := newFixture(t, `{"run_id":"r1","project":"proj","workspace":"@WS@","status":"active","workers":[{"pid":5151,"started_at":"S2"}]}`)
		w := New(bootOpts(f, 4242, map[int]string{4242: "S1", 5151: "S2"}))
		err := w.Boot()
		var we *Error
		if !errors.As(err, &we) || we.Message != "another worker (pid 5151) is pinned to run r1 and still alive" {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("dead pins repin to self", func(t *testing.T) {
		f := newFixture(t, `{"run_id":"r1","project":"proj","workspace":"@WS@","status":"active","workers":[{"pid":5151,"started_at":"S2"}],"worker":{"pid":5151,"started_at":"S2"}}`)
		w := New(bootOpts(f, 4242, map[int]string{4242: "S1"}))
		if err := w.Boot(); err != nil {
			t.Fatal(err)
		}
		defer func() { w.Stop(); _ = w.Release() }()
		if w.Repinned == nil || len(w.Repinned.From) != 1 || w.Repinned.From[0] != 5151 || w.Repinned.To != 4242 {
			t.Fatalf("repinned %+v", w.Repinned)
		}
		record, err := ledger.DecodeObject([]byte(readFile(t, filepath.Join(f.home, ".code-forge", "runs", "r1.json"))))
		if err != nil {
			t.Fatal(err)
		}
		pins, _ := ledger.Stringify(ledger.Object{{Key: "workers", Value: record.Get("workers")}, {Key: "worker", Value: record.Get("worker")}, {Key: "worker_history", Value: record.Get("worker_history")}}, false)
		if want := `{"workers":[{"pid":4242,"started_at":"S1"}],"worker":{"pid":4242,"started_at":"S1"},"worker_history":[{"pid":5151,"started_at":"S2"}]}`; string(pins) != want {
			t.Fatalf("pins %s", pins)
		}
		rows, err := ledger.ReadRows(filepath.Join(f.home, ".code-forge", "ledger"), "proj")
		if err != nil || len(rows) != 1 {
			t.Fatalf("rows %v %v", rows, err)
		}
		row, _ := rows[0].(map[string]any)
		if row["event"] != "worker.reattach" || row["pid"] != float64(4242) || row["run"] != "r1" || row["mac"] == nil {
			t.Fatalf("row %v", row)
		}
	})
	t.Run("own live pin changes nothing", func(t *testing.T) {
		f := newFixture(t, `{"run_id":"r1","project":"proj","workspace":"@WS@","status":"active","workers":[{"pid":4242,"started_at":"S1"},{"pid":5151,"started_at":"S2"}]}`)
		w := New(bootOpts(f, 4242, map[int]string{4242: "S1"}))
		if err := w.Boot(); err != nil {
			t.Fatal(err)
		}
		defer func() { w.Stop(); _ = w.Release() }()
		if w.Repinned != nil {
			t.Fatalf("repinned %+v", w.Repinned)
		}
	})
}

func TestB485BootStartsDaemonBeforeAnnounceAndStopsItOnRefusal(t *testing.T) {
	f := newFixture(t, `{"run_id":"r1","project":"proj","workspace":"@WS@","status":"active","workers":[{"pid":5151,"started_at":"S2"}]}`)
	var started, stopped int32
	o := bootOpts(f, 4242, map[int]string{4242: "S1"})
	o.StartDaemon = func() (func(), error) {
		atomic.AddInt32(&started, 1)
		if _, err := os.Stat(filepath.Join(f.root, ".code-forge", "queue", "worker.json")); !os.IsNotExist(err) {
			t.Error("announced before the daemon started")
		}
		return func() { atomic.AddInt32(&stopped, 1) }, nil
	}
	w := New(o)
	if err := w.Boot(); err != nil {
		t.Fatal(err)
	}
	w.Stop()
	_ = w.Release()
	if started != 1 || stopped != 1 {
		t.Fatalf("daemon started %d stopped %d", started, stopped)
	}
	// A failing re-pin (run ended under the lock) stops the daemon and announces nothing.
	f2 := newFixture(t, `{"run_id":"r1","project":"proj","workspace":"@WS@","status":"active","workers":[{"pid":5151,"started_at":"S2"}]}`)
	o2 := bootOpts(f2, 4242, map[int]string{})
	stopped = 0
	o2.StartDaemon = func() (func(), error) { return func() { atomic.AddInt32(&stopped, 1) }, nil }
	err := New(o2).Boot()
	if err == nil || err.Error() != "worker pid 4242 is not running" || stopped != 1 {
		t.Fatalf("got %v, stopped %d", err, stopped)
	}
}

func TestB485HeartbeatBeatsOnItsOwnTimerAndStops(t *testing.T) {
	f := newFixture(t, activeRecord)
	var clock atomic.Int64
	clock.Store(fixedNow.UnixMilli())
	o := bootOpts(f, 4242, map[int]string{4242: "S1"})
	o.HeartbeatMs = MinHeartbeatMs
	o.Probes.Now = func() time.Time { return time.UnixMilli(clock.Add(1000)).UTC() }
	w := New(o)
	if err := w.Boot(); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(f.root, ".code-forge", "workers", "4242-S1.json")
	first := readFile(t, entry)
	deadline := time.Now().Add(20 * time.Second)
	for readFile(t, entry) == first {
		if time.Now().After(deadline) {
			t.Fatal("no heartbeat written")
		}
		time.Sleep(20 * time.Millisecond)
	}
	w.Stop()
	if err := w.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(entry); !os.IsNotExist(err) {
		t.Fatal("entry left after release")
	}
	// The heartbeat goroutine has ended: nothing re-creates the entry.
	time.Sleep(3 * MinHeartbeatMs * time.Millisecond)
	if _, err := os.Stat(entry); !os.IsNotExist(err) {
		t.Fatal("a beat after release re-created the entry")
	}
}

func TestB485ReviewWorkersUndecided(t *testing.T) {
	if n, err := ReviewWorkers(map[string]any{"review": map[string]any{"workers": nil}}); err != nil || n != 1 {
		t.Fatalf("%v %v", n, err)
	}
	if _, err := ReviewWorkers(map[string]any{"review": map[string]any{"workers": "2"}}); !IsUndecided(err) {
		t.Fatalf("got %v", err)
	}
}
