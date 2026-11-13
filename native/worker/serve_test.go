package worker

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
)

// b489Loop wires a loop over the B497 fixture the way the CLI does.
func b489Loop(t *testing.T, fx *b488Fix, w *TicketRunner, parallel int) *Loop {
	t.Helper()
	w.SessionDeps.Stderr = &lockedWriter{w: w.SessionDeps.Stderr}
	wk := New(Opts{Home: fx.home, RunID: "r1", Root: fx.root, Cfg: w.Cfg, Probes: fakeProbes(nil)})
	w.Stopping = wk.Stopping
	pool := &Pool{Root: fx.root, Run: "r1", PollMs: 5, BootCfg: map[string]any{"review": map[string]any{"parallel_tickets": float64(parallel)}},
		Settings: w.Settings, FileKey: w.FileKey, ProviderHint: w.ProviderHint, Stopping: wk.Stopping}
	w.Pool = pool
	l := &Loop{Worker: wk, Pool: pool, Runner: w, PollMs: 5}
	pool.RunOne = l.RunOne
	pool.Stop = l.Stop
	return l
}

// lockedWriter: concurrent sessions share the test's buffer (os.Stderr in the CLI).
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func b489Unported(t *testing.T, fx *b488Fix, n int) string {
	t.Helper()
	id := strings.Repeat(string(rune('c'+n)), 24)
	writeJSON(t, filepath.Join(QueueDir(fx.root), id+".json"), `{"ticket":"`+id+`","run":"r1","block":"B1","file":"a.txt","content_hash":"`+sha("hello\nworld\n")+`","reopened_at":"Nov 11 2026","enqueued_at":"2026-11-11T09:00:00.000Z"}`+"\n")
	return id
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// reviewRows counts the review rows Go wrote for a ticket's file.
func reviewResults(t *testing.T, fx *b488Fix, id string) int {
	data, err := os.ReadFile(filepath.Join(fx.home, ".code-forge", "ledger", "unit.jsonl"))
	if err != nil {
		return 0
	}
	n := 0
	for _, l := range strings.Split(string(data), "\n") {
		if strings.Contains(l, `"ticket":"`+id+`"`) && strings.Contains(l, `"event":"review.result"`) {
			n++
		}
	}
	return n
}

// The split rule: a queue holding ported and unported tickets. Go reviews
// the ported ones it starts (done marker, one result row), meets the
// unported one, stops taking tickets and hands the worker to Node. The
// tickets left are exactly the ones without a done marker, none is claimed,
// and Node's own queue scan (Pending) serves each of them once.
func TestB489SplitRuleEachTicketReviewedOnce(t *testing.T) {
	for _, parallel := range []int{1, 4} {
		t.Run(map[int]string{1: "serial", 4: "parallel"}[parallel], func(t *testing.T) {
			clock := fixedNow
			fx, w, _ := b497Fixture(t, &clock)
			ported := []string{
				fx.ticket(t, "r1", "B1", "a.txt", sha("hello\nworld\n")),
				fx.ticket(t, "r1", "B2", "b.txt", sha("one\nWARNME\n")),
			}
			unported := []string{b489Unported(t, fx, 0), b489Unported(t, fx, 1)}
			all := append(append([]string{}, ported...), unported...)
			l := b489Loop(t, fx, w, parallel)
			if _, err := l.Once(); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(l.Handoff(), "ticket ") || !strings.Contains(l.Handoff(), "not ported to Go: reopened_at Date.parse undecided") {
				t.Fatalf("handoff = %q, want an unsupported ticket", l.Handoff())
			}
			var goDone, left []string
			for _, id := range all {
				if exists(filepath.Join(QueueDir(fx.root), id+".done")) {
					goDone = append(goDone, id)
					if n := reviewResults(t, fx, id); n != 1 {
						t.Errorf("ticket %s: %d result rows, want 1", id, n)
					}
				} else {
					left = append(left, id)
					if n := reviewResults(t, fx, id); n != 0 {
						t.Errorf("left ticket %s has %d result rows", id, n)
					}
					if !exists(filepath.Join(QueueDir(fx.root), id+".json")) {
						t.Errorf("left ticket %s lost its file", id)
					}
				}
			}
			for _, id := range unported {
				if exists(filepath.Join(QueueDir(fx.root), id+".done")) {
					t.Errorf("unported %s was marked done by Go", id)
				}
			}
			// Node's queue scan after the hand-over: exactly the tickets left,
			// each claimable (no lock held by Go).
			pending := Pending(fx.root)
			sort.Strings(pending)
			sort.Strings(left)
			if strings.Join(pending, ",") != strings.Join(left, ",") {
				t.Fatalf("Node would see %v, Go left %v", pending, left)
			}
			for _, id := range pending {
				release, ok, err := ClaimTicket("r1", id)
				if err != nil || !ok {
					t.Fatalf("ticket %s is still claimed after the hand-over: %v", id, err)
				}
				release()
			}
			if len(goDone)+len(left) != len(all) {
				t.Fatalf("done %v + left %v != %v", goDone, left, all)
			}
		})
	}
}

// Without an unported ticket the loop serves every ticket in Go and never
// hands over.
func TestB489LoopServesPortedTickets(t *testing.T) {
	clock := fixedNow
	fx, w, _ := b497Fixture(t, &clock)
	ids := []string{
		fx.ticket(t, "r1", "B1", "a.txt", sha("hello\nworld\n")),
		fx.ticket(t, "r1", "B2", "b.txt", sha("one\nWARNME\n")),
	}
	l := b489Loop(t, fx, w, 2)
	n, err := l.Once()
	if err != nil || n != 2 || l.Handoff() != "" {
		t.Fatalf("Once = %d, %v, handoff %q", n, err, l.Handoff())
	}
	for _, id := range ids {
		if !exists(filepath.Join(QueueDir(fx.root), id+".done")) || reviewResults(t, fx, id) != 1 {
			t.Fatalf("ticket %s not reviewed once", id)
		}
	}
	if len(Pending(fx.root)) != 0 {
		t.Fatal("tickets left")
	}
}

// Run serves new tickets until Stop; Stop during the wait ends it at once.
func TestB489RunStopsCleanly(t *testing.T) {
	clock := fixedNow
	fx, w, _ := b497Fixture(t, &clock)
	l := b489Loop(t, fx, w, 2)
	ended := make(chan struct{})
	go func() { l.Run(); close(ended) }()
	id := fx.ticket(t, "r1", "B1", "a.txt", sha("hello\nworld\n"))
	waitFor(t, "the ticket's done marker", func() bool { return exists(filepath.Join(QueueDir(fx.root), id+".done")) })
	l.Stop()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not end after Stop")
	}
	if l.Handoff() != "" {
		t.Fatalf("handoff %q", l.Handoff())
	}
}

// The idle exit: after idle_exit_hours with no activity and nothing pending
// the loop stops and writes one signed worker.idle_exit row; a pending
// ticket or recent activity keeps it alive.
func TestB489IdleExit(t *testing.T) {
	clock := fixedNow
	fx := newB488Fix(t)
	fx.record(t, `{"run_id":"r1","project":"unit","workspace":"`+fx.root+`","status":"active","started_at":"2026-11-11T07:00:00.000Z"}`)
	w := fx.runner(&clock)
	l := b489Loop(t, fx, w, 1)
	var rows []ledger.Object
	idle := &Idle{RepoRoot: fx.root,
		Hours:    func() (float64, error) { return 2, nil },
		Record:   func() (ledger.Object, error) { return w.RunRecord() },
		Row:      func(r ledger.Object) error { rows = append(rows, r); return w.Ledger(r) },
		Identity: w.Worker}
	l.Now = func() time.Time { return clock }
	// 3h since start, but a ticket of the run waits: activity is now.
	id := fx.ticket(t, "r1", "B1", "a.txt", sha("x"))
	idle.Check(l)
	if l.Worker.Stopping() {
		t.Fatal("stopped with a pending ticket")
	}
	if err := os.Rename(filepath.Join(QueueDir(fx.root), id+".json"), filepath.Join(fx.root, "moved.json")); err != nil {
		t.Fatal(err)
	}
	// Within the minute: no new check.
	clock = clock.Add(30 * time.Second)
	idle.Check(l)
	if l.Worker.Stopping() {
		t.Fatal("checked twice within a minute")
	}
	clock = clock.Add(31 * time.Second)
	// lastActivity (now - 61s) is less than 2h ago: still alive.
	idle.Check(l)
	if l.Worker.Stopping() {
		t.Fatal("stopped before idle_exit_hours")
	}
	clock = clock.Add(2*time.Hour + time.Minute)
	idle.Check(l)
	if !l.Worker.Stopping() || len(rows) != 1 {
		t.Fatalf("stopping %v rows %v", l.Worker.Stopping(), rows)
	}
	got := string(mustLine(t, rows[0]))
	want := `{"event":"worker.idle_exit","ts":"2026-11-11T12:02:01.000Z","idle_hours":2,"worker":{"pid":4242,"started_at":"Wed Nov 11 21:00:00 2026"}}`
	if got != want {
		t.Fatalf("row %s\nwant %s", got, want)
	}
	led := readFile(t, filepath.Join(fx.home, ".code-forge", "ledger", "unit.jsonl"))
	if !strings.Contains(led, `{"run":"r1","event":"worker.idle_exit","ts":"2026-11-11T12:02:01.000Z","idle_hours":2,`) || !strings.Contains(led, `"mac":"`) {
		t.Fatalf("ledger %s", led)
	}
}

func mustLine(t *testing.T, o ledger.Object) []byte {
	t.Helper()
	b, err := line(o)
	if err != nil {
		t.Fatal(err)
	}
	return []byte(strings.TrimSuffix(string(b), "\n"))
}

func TestB489IdleExitHours(t *testing.T) {
	for _, c := range []struct {
		cfg  map[string]any
		want float64
	}{
		{nil, 24},
		{map[string]any{"worker": map[string]any{"idle_exit_hours": float64(0)}}, 0},
		{map[string]any{"worker": map[string]any{"idle_exit_hours": 1.5}}, 1.5},
		{map[string]any{"worker": map[string]any{"idle_exit_hours": float64(-1)}}, 24},
		{map[string]any{"worker": map[string]any{"idle_exit_hours": "3"}}, 24},
	} {
		if got := IdleExitHours(c.cfg); got != c.want {
			t.Errorf("IdleExitHours(%v) = %v, want %v", c.cfg, got, c.want)
		}
	}
}
