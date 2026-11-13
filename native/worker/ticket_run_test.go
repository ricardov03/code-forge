package worker

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/filelock"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/sign"
	"github.com/ricardov03/code-forge/native/ticket"
)

// B488: testdata/b488_node_refusals.golden is what Node's createWorker
// (src/worker/loop.mjs processTicket, default appendRow ledger) wrote for the
// same fixture (temp HOME, fake key store, no model), with the worker
// identity, times, durations and macs normalized as below.

var b488Norm = []struct {
	re  *regexp.Regexp
	rep string
}{
	{regexp.MustCompile(`"worker":\{[^}]*\}`), `"worker":W`},
	{regexp.MustCompile(`"(started_at|finished_at|ts)":"[^"]*"`), `"$1":"T"`},
	{regexp.MustCompile(`"duration_ms":[0-9]+`), `"duration_ms":N`},
	{regexp.MustCompile(`"mac":"[0-9a-f]{64}"`), `"mac":"M"`},
}

func b488Normalize(s string) string {
	for _, n := range b488Norm {
		s = n.re.ReplaceAllString(s, n.rep)
	}
	return s
}

type b488Fix struct {
	home, root string
	workspace  string // B507: a run workspace other than root (empty: root)
	key        []byte
	rows       []ledger.Object
}

func newB488Fix(t *testing.T) *b488Fix {
	t.Helper()
	f := newFixture(t, "")
	root, err := filepath.EvalSymlinks(f.root)
	if err != nil {
		t.Fatal(err)
	}
	fx := &b488Fix{home: f.home, root: root, key: bytes.Repeat([]byte{7}, 32)}
	fx.record(t, `{"run_id":"r1","project":"unit","workspace":"`+root+`","status":"active"}`)
	if err := os.WriteFile(filepath.Join(f.home, ".code-forge", "runs", "r1.key"), fx.key, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return fx
}

func (fx *b488Fix) record(t *testing.T, text string) {
	t.Helper()
	writeJSON(t, filepath.Join(fx.home, ".code-forge", "runs", "r1.json"), text)
}

func (fx *b488Fix) runner(clock *time.Time) *TicketRunner {
	return &TicketRunner{
		Home: fx.home, RepoRoot: fx.root, RunID: "r1", Slug: "unit", Key: fx.key,
		Worker: ledger.Object{{Key: "pid", Value: float64(4242)}, {Key: "started_at", Value: "Wed Nov 11 21:00:00 2026"}},
		Now:    func() time.Time { return *clock },
	}
}

// ticket writes a queue ticket the way enqueue does and returns its id.
func (fx *b488Fix) ticket(t *testing.T, run, block, file, hash string, extra ...ledger.Field) string {
	t.Helper()
	p := ticket.Parts{Run: run, Block: block, File: file, ContentHash: hash}
	for _, e := range extra {
		s, _ := e.Value.(string)
		switch e.Key {
		case "tree":
			p.Tree = &s
		case "acceptance_revision":
			p.AcceptanceRevision = &s
		}
	}
	id := ticket.ReviewID(p)
	o := ledger.Object{{Key: "ticket", Value: id}, {Key: "run", Value: run}, {Key: "block", Value: block}, {Key: "file", Value: file}, {Key: "content_hash", Value: hash}}
	o = append(o, extra...)
	o.Set("enqueued_at", "2026-11-11T09:00:00.000Z")
	for _, e := range extra {
		if e.Key == "queued_at" { // after enqueued_at, as the Node fixture wrote it
			o.Delete("queued_at")
			o.Set("queued_at", e.Value)
		}
	}
	data, err := line(o)
	if err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(QueueDir(fx.root), id+".json"), string(data))
	return id
}

func filelockTry(key string) (func(), error) {
	res, err := filelock.TryLock(key, filelock.Options{})
	if err != nil {
		return nil, err
	}
	if !res.OK {
		return nil, errors.New("lock busy")
	}
	return res.Release, nil
}

func sha(s string) string { d := sha256.Sum256([]byte(s)); return hex.EncodeToString(d[:]) }

func (fx *b488Fix) files(t *testing.T, ids []string) string {
	t.Helper()
	var out strings.Builder
	for _, id := range ids {
		out.WriteString(readFile(t, filepath.Join(fx.root, ".code-forge", "reviews", "r1", id+".json")))
		out.WriteString(readFile(t, filepath.Join(QueueDir(fx.root), id+".done")))
		out.WriteString(readFile(t, filepath.Join(QueueDir(fx.root), id+".json")))
	}
	return out.String()
}

func TestB488RefusalsMatchNodeBytes(t *testing.T) {
	fx := newB488Fix(t)
	real := sha("hello\n")
	ids := []string{
		fx.ticket(t, "r1", "B1", "a.txt", strings.Repeat("0", 64), ledger.Field{Key: "queued_at", Value: "2026-11-11T09:30:00.000Z"}),
		fx.ticket(t, "r1", "B1", "gone.txt", strings.Repeat("1", 64)),
		fx.ticket(t, "r2", "B1", "a.txt", real),
	}
	bad := strings.Repeat("a", 24)
	writeJSON(t, filepath.Join(QueueDir(fx.root), bad+".json"), `{"ticket":"`+bad+`","run":"r1","block":"B1","file":".git/config","content_hash":"x","enqueued_at":"2026-11-11T09:00:00.000Z"}`+"\n")
	notJSON := strings.Repeat("b", 24)
	writeJSON(t, filepath.Join(QueueDir(fx.root), notJSON+".json"), "not json\n")
	ids = append(ids, bad, notJSON, fx.ticket(t, "r1", "B1", "a.txt", real, ledger.Field{Key: "tree", Value: "/nonexistent-b488-tree"}))
	if err := os.WriteFile(filepath.Join(fx.root, "locked.txt"), []byte("x"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(fx.root, "locked.txt"), 0o644) })
	ids = append(ids, fx.ticket(t, "r1", "B1", "locked.txt", strings.Repeat("2", 64)))
	if err := os.Symlink("a.txt", filepath.Join(fx.root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	ids = append(ids, fx.ticket(t, "r1", "B1", "link.txt", strings.Repeat("3", 64)))
	wtGone := fx.ticket(t, "r1", "B4", "a.txt", real)
	wtBad := fx.ticket(t, "r1", "B5", "a.txt", real)
	snapshot := fx.ticket(t, "r1", "B2", "a.txt", real)
	unreadable := fx.ticket(t, "r1", "B3", "a.txt", real)

	clock := fixedNow
	w := fx.runner(&clock)
	run := func(id string) {
		t.Helper()
		n, poison, err := w.RunTicket(id)
		if err != nil || n != 1 || poison {
			t.Fatalf("RunTicket(%s) = %d, %v, %v; want 1, false, nil", id, n, poison, err)
		}
	}
	for _, id := range ids {
		run(id)
	}
	fx.record(t, `{"run_id":"r1","project":"unit","workspace":"`+fx.root+`","status":"active","blocks":{"B4":{"worktree":{"path":"/nonexistent-b488-wt","branch":"b","base_sha":"`+strings.Repeat("a", 40)+`"}},"B5":{"worktree":{"path":"rel","branch":"b","base_sha":"`+strings.Repeat("a", 40)+`"}}}}`)
	run(wtGone)
	run(wtBad)
	ids = append(ids, wtGone, wtBad)
	fx.record(t, `{"run_id":"r1","project":"unit","workspace":"`+fx.root+`","status":"active","config":{"hash":"`+strings.Repeat("f", 64)+`","snapshots":{}}}`)
	run(snapshot)
	fx.record(t, `{bad`)
	run(unreadable)
	ids = append(ids, snapshot, unreadable)

	ledgerText := readFile(t, filepath.Join(fx.home, ".code-forge", "ledger", "unit.jsonl"))
	got := b488Normalize(fx.files(t, ids) + ledgerText)
	want := readFile(t, filepath.Join("testdata", "b488_node_refusals.golden"))
	if got != want {
		gl, wl := strings.Split(got, "\n"), strings.Split(want, "\n")
		for i := range wl {
			if i >= len(gl) || gl[i] != wl[i] {
				g := "<missing>"
				if i < len(gl) {
					g = gl[i]
				}
				t.Fatalf("line %d differs from Node\n got: %s\nwant: %s", i+1, g, wl[i])
			}
		}
		t.Fatalf("Go wrote %d lines, Node %d", len(gl), len(wl))
	}
	// every signed result and ledger row verifies with the run key
	for _, id := range ids {
		obj, err := ledger.DecodeObject([]byte(readFile(t, filepath.Join(fx.root, ".code-forge", "reviews", "r1", id+".json"))))
		if err != nil {
			t.Fatal(err)
		}
		if res, err := sign.VerifyRow(obj.Map(), fx.key); err != nil || !res.OK {
			t.Fatalf("result %s does not verify: %+v %v", id, res, err)
		}
	}
	for _, l := range strings.Split(strings.TrimSpace(ledgerText), "\n") {
		obj, err := ledger.DecodeObject([]byte(l))
		if err != nil {
			t.Fatal(err)
		}
		m := obj.Map()
		delete(m, "tokens_source")
		delete(m, "cost_source")
		if res, err := sign.VerifyRow(m, fx.key); err != nil || !res.OK {
			t.Fatalf("ledger row does not verify: %s", l)
		}
	}
}

// The fixed clock gives Node's whole-millisecond duration and ISO stamps.
func TestB488TimingsUseTheClock(t *testing.T) {
	fx := newB488Fix(t)
	id := fx.ticket(t, "r1", "B1", "a.txt", strings.Repeat("0", 64))
	clock := fixedNow
	w := fx.runner(&clock)
	calls := 0
	w.Now = func() time.Time { calls++; return fixedNow.Add(time.Duration(calls) * 1500 * time.Microsecond) }
	if n, _, err := w.RunTicket(id); n != 1 || err != nil {
		t.Fatalf("RunTicket = %d, %v", n, err)
	}
	done := readFile(t, filepath.Join(QueueDir(fx.root), id+".done"))
	// runOne start (1.5ms), lock start (3.0ms), finish (4.5ms → 4ms): 4 - 3 = 1
	want := `{"ticket":"` + id + `","status":"stale","queued_at":"2026-11-11T09:00:00.000Z","started_at":"2026-11-11T10:00:00.003Z","finished_at":"2026-11-11T10:00:00.004Z","duration_ms":1,"result":".code-forge/reviews/r1/` + id + `.json"}` + "\n"
	if done != want {
		t.Fatalf("done marker\n got: %s\nwant: %s", done, want)
	}
}

func (fx *b488Fix) snapshotFiles(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, dir := range []string{filepath.Join(fx.root, ".code-forge"), filepath.Join(fx.home, ".code-forge", "ledger")} {
		_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				data, _ := os.ReadFile(p)
				out[p] = string(data)
			}
			return nil
		})
	}
	return out
}

func assertSameFiles(t *testing.T, before, after map[string]string) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("files changed: %d before, %d after", len(before), len(after))
	}
	for p, v := range before {
		if after[p] != v {
			t.Fatalf("%s changed", p)
		}
	}
}

// A ticket Node would review (or one Go cannot decide) is left for Node with
// nothing written: no timings, no result, no done marker, no ledger row.
func TestB488UnsupportedWritesNothing(t *testing.T) {
	fx := newB488Fix(t)
	real := sha("hello\n")
	cases := map[string]func() string{
		"review engine (no snapshot: the boot config)": func() string { return fx.ticket(t, "r1", "B1", "a.txt", real) },
		"reopened_at Date.parse undecided": func() string {
			id := strings.Repeat("c", 24)
			writeJSON(t, filepath.Join(QueueDir(fx.root), id+".json"), `{"ticket":"`+id+`","run":"r1","block":"B1","file":"a.txt","content_hash":"`+real+`","reopened_at":"Nov 11 2026"}`+"\n")
			return id
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			id := mk()
			before := fx.snapshotFiles(t)
			clock := fixedNow
			n, poison, err := fx.runner(&clock).RunTicket(id)
			if !IsUnsupported(err) || n != 0 || poison {
				t.Fatalf("RunTicket = %d, %v, %v; want ErrUnsupported", n, poison, err)
			}
			assertSameFiles(t, before, fx.snapshotFiles(t))
		})
	}
}

// A matching snapshot (pinned for the ticket) is a review: Node's; a ticket
// pin to a snapshot that does not match is config-snapshot.
func TestB488SnapshotPins(t *testing.T) {
	fx := newB488Fix(t)
	real := sha("hello\n")
	id := fx.ticket(t, "r1", "B1", "a.txt", real)
	cfg := `{"version":1}`
	good := sha(cfg) // canonical JSON of {"version":1}
	fx.record(t, `{"run_id":"r1","project":"unit","workspace":"`+fx.root+`","status":"active","config":{"hash":"`+good+`","snapshots":{"`+good+`":`+cfg+`},"pins":{"`+id+`":"`+strings.Repeat("e", 64)+`"}}}`)
	clock := fixedNow
	if n, _, err := fx.runner(&clock).RunTicket(id); n != 1 || err != nil {
		t.Fatalf("pinned to a missing snapshot: %d, %v", n, err)
	}
	if got := readFile(t, filepath.Join(QueueDir(fx.root), id+".done")); !strings.Contains(got, `"status":"unavailable"`) {
		t.Fatalf("done = %s", got)
	}
	id2 := fx.ticket(t, "r1", "B1", "a.txt", real, ledger.Field{Key: "acceptance_revision", Value: "rev1"})
	// B492: the block entry the engine reads (a record with no blocks is run-record-unreadable)
	fx.record(t, `{"run_id":"r1","project":"unit","workspace":"`+fx.root+`","status":"active","blocks":{"B1":{"base_sha":"`+strings.Repeat("a", 40)+`","acceptance_revision":"rev1"}},"config":{"hash":"`+good+`","snapshots":{"`+good+`":`+cfg+`}}}`)
	if _, _, err := fx.runner(&clock).RunTicket(id2); !IsUnsupported(err) {
		t.Fatalf("the snapshot in force matches: want ErrUnsupported (a review), got %v", err)
	}
}

func TestB488StoppingAbandonsUnavailable(t *testing.T) {
	fx := newB488Fix(t)
	id := fx.ticket(t, "r1", "B1", "a.txt", sha("hello\n"), ledger.Field{Key: "tree", Value: "/nonexistent-b488-tree"})
	clock := fixedNow
	w := fx.runner(&clock)
	w.Stopping = func() bool { return true }
	n, poison, err := w.RunTicket(id)
	if n != 0 || poison || err != nil {
		t.Fatalf("RunTicket = %d, %v, %v; want abandoned", n, poison, err)
	}
	if IsDone(fx.root, id) {
		t.Fatal("an abandoned ticket has no done marker")
	}
	// Node wrote the start timings before the stop: the ticket keeps them
	got := readFile(t, filepath.Join(QueueDir(fx.root), id+".json"))
	if !strings.HasSuffix(got, `"started_at":"2026-11-11T10:00:00.000Z","finished_at":null,"duration_ms":null}`+"\n") {
		t.Fatalf("ticket = %s", got)
	}
	// a stale outcome while stopping is still recorded
	id2 := fx.ticket(t, "r1", "B1", "a.txt", strings.Repeat("0", 64))
	if n, _, _ := w.RunTicket(id2); n != 1 || !IsDone(fx.root, id2) {
		t.Fatal("a real outcome is recorded while stopping")
	}
}

// runOne's poison rule: a ticket whose failure cannot be recorded either.
func TestB488PoisonWhenNothingCanBeWritten(t *testing.T) {
	fx := newB488Fix(t)
	clock := fixedNow
	w := fx.runner(&clock)
	if n, poison, err := w.RunTicket("not-a-ticket"); n != 0 || !poison || err != nil {
		t.Fatalf("bad id = %d, %v, %v; want poisoned", n, poison, err)
	}
	id := fx.ticket(t, "r1", "B1", "a.txt", strings.Repeat("0", 64))
	q := QueueDir(fx.root)
	if err := os.Chmod(q, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(q, 0o755) })
	if n, poison, err := w.RunTicket(id); n != 0 || !poison || err != nil {
		t.Fatalf("read-only queue = %d, %v, %v; want poisoned", n, poison, err)
	}
	// Node's start timings write fails first, and again in the failure's complete: nothing written
	if _, err := os.Stat(filepath.Join(fx.root, ".code-forge", "reviews", "r1", id+".json")); !os.IsNotExist(err) {
		t.Fatalf("result: %v", err)
	}
	if IsDone(fx.root, id) {
		t.Fatal("no done marker in a read-only queue")
	}
}

// The ticket runs under Node's (run, block, file) lock: a held lock waits.
func TestB488HoldsTheFileLock(t *testing.T) {
	fx := newB488Fix(t)
	id := fx.ticket(t, "r1", "B1", "a.txt", strings.Repeat("0", 64))
	held, err := filelockTry("r1\x00" + FileLockKey("B1", "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	clock := fixedNow
	w := fx.runner(&clock)
	done := make(chan struct{})
	go func() { _, _, _ = w.RunTicket(id); close(done) }()
	select {
	case <-done:
		t.Fatal("ran while Node's file lock was held")
	case <-time.After(150 * time.Millisecond):
	}
	held()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("never ran after the lock was released")
	}
	if !IsDone(fx.root, id) {
		t.Fatal("not recorded")
	}
}
