package worker

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/state"
)

func ticketIDN(n int) string { return fmt.Sprintf("%024x", n) }

func writeTicket(t *testing.T, root string, n int, at string) string {
	t.Helper()
	id := ticketIDN(n)
	writeJSON(t, filepath.Join(QueueDir(root), id+".json"), `{"id":"`+id+`","enqueued_at":"`+at+`"}`)
	return id
}

// waitFor spins (no sleeps) until cond holds; a hang fails by the test timeout.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestB485PoolSettingsBounds(t *testing.T) {
	for _, c := range []struct {
		v      any
		parTix int
		parPro int
	}{
		{nil, 6, 4}, {float64(1), 1, 1}, {float64(16), 16, 16}, {float64(17), 6, 4},
		{float64(0), 6, 4}, {float64(2.5), 6, 4}, {"3", 6, 4}, {float64(-1), 6, 4}, {float64(1e300), 6, 4},
	} {
		cfg := map[string]any{"review": map[string]any{"parallel_tickets": c.v, "provider_parallel": c.v}}
		if got := ParallelTickets(cfg); got != c.parTix {
			t.Errorf("parallel_tickets %v: %d, want %d", c.v, got, c.parTix)
		}
		if got := ProviderParallel(cfg); got != c.parPro {
			t.Errorf("provider_parallel %v: %d, want %d", c.v, got, c.parPro)
		}
	}
	if ParallelTickets(nil) != 6 || ProviderParallel(nil) != 4 || FairShare(nil) {
		t.Fatal("nil config defaults")
	}
	if !FairShare(map[string]any{"review": map[string]any{"fair_share": true}}) || FairShare(map[string]any{"review": map[string]any{"fair_share": "true"}}) {
		t.Fatal("fair_share is true only for boolean true")
	}
}

func TestB485PendingOrderAndClaims(t *testing.T) {
	f := newFixture(t, "")
	if got := Pending(f.root); got != nil {
		t.Fatalf("missing queue is empty, got %v", got)
	}
	late := writeTicket(t, f.root, 1, "2026-11-11T10:00:02.000Z")
	early := writeTicket(t, f.root, 2, "2026-11-11T10:00:01.000Z")
	tieB := writeTicket(t, f.root, 4, "2026-11-11T10:00:01.500Z")
	tieA := writeTicket(t, f.root, 3, "2026-11-11T10:00:01.500Z")
	done := writeTicket(t, f.root, 5, "2026-11-11T10:00:00.000Z")
	writeJSON(t, filepath.Join(QueueDir(f.root), done+".done"), `{}`)
	claimed := writeTicket(t, f.root, 6, "2026-11-11T10:00:00.000Z")
	writeJSON(t, filepath.Join(QueueDir(f.root), claimed+".done.123.abcdef01.fresh"), `{}`)
	writeJSON(t, filepath.Join(QueueDir(f.root), "notaticket.json"), `{}`)
	// unreadable enqueued_at sorts first (""), like Node
	broken := ticketIDN(7)
	writeJSON(t, filepath.Join(QueueDir(f.root), broken+".json"), `{`)
	want := []string{broken, early, tieA, tieB, late}
	if got := Pending(f.root); !reflect.DeepEqual(got, want) {
		t.Fatalf("pending %v, want %v", got, want)
	}
	if !IsDone(f.root, done) || IsDone(f.root, early) || IsDone(f.root, "../x") {
		t.Fatal("IsDone")
	}
}

func TestB485ClaimTicketIsExclusive(t *testing.T) {
	newFixture(t, "")
	release, ok, err := ClaimTicket("r1", ticketIDN(1))
	if err != nil || !ok {
		t.Fatalf("first claim: ok=%v err=%v", ok, err)
	}
	if _, ok2, err := ClaimTicket("r1", ticketIDN(1)); err != nil || ok2 {
		t.Fatalf("second claim while held: ok=%v err=%v", ok2, err)
	}
	if r, ok3, err := ClaimTicket("r2", ticketIDN(1)); err != nil || !ok3 {
		t.Fatalf("other run's claim: ok=%v err=%v", ok3, err)
	} else {
		r()
	}
	release()
	if r, ok4, err := ClaimTicket("r1", ticketIDN(1)); err != nil || !ok4 {
		t.Fatalf("claim after release: ok=%v err=%v", ok4, err)
	} else {
		r()
	}
}

type fakeRuns struct {
	mu       sync.Mutex
	gates    map[string]chan struct{}
	started  []string
	running  map[string]bool
	maxLive  int
	overlaps []string
	keys     map[string]string
}

func newFakeRuns() *fakeRuns {
	return &fakeRuns{gates: map[string]chan struct{}{}, running: map[string]bool{}, keys: map[string]string{}}
}

func (r *fakeRuns) gate(id string) chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	g, ok := r.gates[id]
	if !ok {
		g = make(chan struct{})
		r.gates[id] = g
	}
	return g
}

func (r *fakeRuns) run(root string) func(string) (int, bool) {
	return func(id string) (int, bool) {
		r.mu.Lock()
		r.started = append(r.started, id)
		for other := range r.running {
			if k := r.keys[id]; k != "" && r.keys[other] == k {
				r.overlaps = append(r.overlaps, id+"/"+other)
			}
		}
		r.running[id] = true
		if len(r.running) > r.maxLive {
			r.maxLive = len(r.running)
		}
		r.mu.Unlock()
		<-r.gate(id)
		// the done marker is written before the claim is released
		_ = os.WriteFile(filepath.Join(QueueDir(root), id+".done"), []byte("{}"), 0o600)
		r.mu.Lock()
		delete(r.running, id)
		r.mu.Unlock()
		return 1, false
	}
}

func (r *fakeRuns) startedN() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.started)
}

func (r *fakeRuns) startedList() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.started...)
}

func TestB485DrainCapsTicketsAndSerializesFileKeys(t *testing.T) {
	f := newFixture(t, "")
	runs := newFakeRuns()
	var ids []string
	for i := 1; i <= 5; i++ {
		ids = append(ids, writeTicket(t, f.root, i, fmt.Sprintf("2026-11-11T10:00:0%d.000Z", i)))
	}
	// tickets 1 and 2 touch the same (block, file)
	runs.keys[ids[0]], runs.keys[ids[1]] = "B1\x00a.go", "B1\x00a.go"
	cfg := map[string]any{"review": map[string]any{"parallel_tickets": float64(2)}}
	p := &Pool{
		Root: f.root, Run: "r1", PollMs: 5, RunOne: runs.run(f.root),
		Settings: func() (map[string]any, any) { return cfg, nil },
		FileKey: func(id string) (FileKey, bool) {
			if k := runs.keys[id]; k != "" {
				return FileKey{k, "B1"}, true
			}
			return FileKey{id, "B" + id}, true
		},
	}
	result := make(chan int, 1)
	go func() {
		n, err := p.Drain()
		if err != nil {
			t.Error(err)
		}
		result <- n
	}()
	// ticket 2 waits (same file as 1) without a slot, so 1 and 3 start
	waitFor(t, "two tickets started", func() bool { return runs.startedN() == 2 })
	if got := runs.startedList(); !reflect.DeepEqual(got, []string{ids[0], ids[2]}) && !reflect.DeepEqual(got, []string{ids[2], ids[0]}) {
		t.Fatalf("first two started %v, want tickets 1 and 3", got)
	}
	if lim, prov := p.Limits(); lim != 2 || prov != 4 {
		t.Fatalf("limits %d/%d", lim, prov)
	}
	close(runs.gate(ids[0]))
	waitFor(t, "ticket 2 after ticket 1", func() bool { return runs.startedN() == 3 })
	for _, id := range ids[1:] {
		select {
		case <-runs.gate(id):
		default:
			close(runs.gate(id))
		}
	}
	if n := <-result; n != 5 {
		t.Fatalf("drain completed %d, want 5", n)
	}
	if runs.maxLive > 2 {
		t.Fatalf("max live tickets %d > parallel_tickets 2", runs.maxLive)
	}
	if len(runs.overlaps) != 0 {
		t.Fatalf("same (block, file) ran together: %v", runs.overlaps)
	}
	if len(runs.started) != 5 {
		t.Fatalf("each ticket starts once: %v", runs.started)
	}
	if p.Activity == 0 {
		t.Fatal("drain with work records activity")
	}
}

func TestB485DrainFairShare(t *testing.T) {
	for _, fair := range []bool{false, true} {
		t.Run(fmt.Sprint(fair), func(t *testing.T) {
			f := newFixture(t, "")
			runs := newFakeRuns()
			a1 := writeTicket(t, f.root, 1, "2026-11-11T10:00:01.000Z")
			a2 := writeTicket(t, f.root, 2, "2026-11-11T10:00:02.000Z")
			b1 := writeTicket(t, f.root, 3, "2026-11-11T10:00:03.000Z")
			block := map[string]string{a1: "A", a2: "A", b1: "B"}
			cfg := map[string]any{"review": map[string]any{"parallel_tickets": float64(2), "fair_share": fair}}
			p := &Pool{
				Root: f.root, Run: "r1", PollMs: 5, RunOne: runs.run(f.root),
				Settings: func() (map[string]any, any) { return cfg, nil },
				FileKey:  func(id string) (FileKey, bool) { return FileKey{block[id] + "\x00" + id, block[id]}, true },
			}
			result := make(chan int, 1)
			go func() { n, _ := p.Drain(); result <- n }()
			waitFor(t, "two started", func() bool { return runs.startedN() == 2 })
			want := []string{a1, a2}
			if fair {
				want = []string{a1, b1}
			}
			// B519: the two tickets start in parallel, so their start order is not
			// fixed; the pick (which two tickets) is the behaviour under test.
			got := append([]string(nil), runs.startedList()...)
			sort.Strings(got)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("fair=%v started %v, want %v", fair, got, want)
			}
			for _, id := range []string{a1, a2, b1} {
				close(runs.gate(id))
			}
			if n := <-result; n != 3 {
				t.Fatalf("completed %d", n)
			}
		})
	}
}

func TestB485DrainSkipsClaimedDoneAndPoisoned(t *testing.T) {
	f := newFixture(t, "")
	owned := writeTicket(t, f.root, 1, "2026-11-11T10:00:01.000Z")
	raced := writeTicket(t, f.root, 2, "2026-11-11T10:00:02.000Z")
	bad := writeTicket(t, f.root, 3, "2026-11-11T10:00:03.000Z")
	var mu sync.Mutex
	var ran []string
	p := &Pool{
		Root: f.root, Run: "r1", PollMs: 5,
		Claim: func(run, id string) (func(), bool, error) {
			switch id {
			case owned:
				return nil, false, nil // another process owns it
			case raced:
				// another worker finished it after the pending snapshot
				_ = os.WriteFile(filepath.Join(QueueDir(f.root), id+".done"), []byte("{}"), 0o600)
			}
			return func() {}, true, nil
		},
		RunOne: func(id string) (int, bool) {
			mu.Lock()
			ran = append(ran, id)
			mu.Unlock()
			return 0, id == bad
		},
	}
	if n, err := p.Drain(); err != nil || n != 0 {
		t.Fatalf("drain: %d %v", n, err)
	}
	if !reflect.DeepEqual(ran, []string{bad}) {
		t.Fatalf("ran %v, want only the poisoned ticket", ran)
	}
	if !p.Poisoned(bad) || p.Poisoned(owned) {
		t.Fatal("poison set")
	}
	ran = nil
	if _, err := p.Drain(); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 0 {
		t.Fatalf("a poisoned ticket never runs again; ran %v", ran)
	}
	// a claim error fails the drain like Node's tryLock throw
	boom := errors.New("lock dir")
	p2 := &Pool{Root: f.root, Run: "r1", PollMs: 5, RunOne: p.RunOne,
		Claim: func(string, string) (func(), bool, error) { return nil, false, boom }}
	if _, err := p2.Drain(); !errors.Is(err, boom) {
		t.Fatalf("claim error: %v", err)
	}
}

func TestB485DrainGuardMissingStops(t *testing.T) {
	f := newFixture(t, "")
	writeTicket(t, f.root, 1, "2026-11-11T10:00:01.000Z")
	stopped := false
	var warns []string
	ran := 0
	p := &Pool{
		Root: f.root, Run: "r1", PollMs: 5,
		GuardPath: filepath.Join(f.home, ".code-forge", "runs", "gone.json"),
		Stopping:  func() bool { return stopped },
		Stop:      func() { stopped = true },
		Warn:      func(w, m string) { warns = append(warns, w+": "+m) },
		RunOne:    func(string) (int, bool) { ran++; return 1, false },
	}
	if n, err := p.Drain(); err != nil || n != 0 || ran != 0 {
		t.Fatalf("drain %d %v ran %d", n, err, ran)
	}
	want := "worker_orphan_stop: run record missing: " + p.GuardPath
	if !stopped || !reflect.DeepEqual(warns, []string{want}) {
		t.Fatalf("stopped=%v warns=%v", stopped, warns)
	}
	// a present record never stops
	stopped, warns = false, nil
	p.GuardPath = f.root
	if n, _ := p.Drain(); n != 1 || stopped || warns != nil {
		t.Fatalf("present guard: n=%d stopped=%v warns=%v", n, stopped, warns)
	}
}

type kindErr struct{}

func (kindErr) Error() string { return "secret /path" }
func (kindErr) Kind() string  { return "TypeError" }

func TestB485DrainWarnsExpiryOncePerStreakAndWatchFailures(t *testing.T) {
	f := newFixture(t, "")
	var stderr bytes.Buffer
	var rows []string
	fail := true
	p := &Pool{
		Root: f.root, Run: "r1", PollMs: 5, Stderr: &stderr,
		RunOne: func(string) (int, bool) { return 1, false },
		Warn:   func(w, m string) { rows = append(rows, w+"|"+m) },
		ExpiryCheck: func() error {
			if fail {
				return &os.PathError{Op: "open", Path: "/secret", Err: syscall.EACCES}
			}
			return nil
		},
		WatchTick: func(map[string]bool) error { return kindErr{} },
	}
	for _, step := range []bool{true, true, false, true} {
		fail = step
		if _, err := p.Drain(); err != nil {
			t.Fatal(err)
		}
	}
	expiry := "worker: WARN " + ExpiryCheckFailed + " (EACCES)\n"
	watch := "worker: WARN watch tick failed (TypeError)\n"
	want := expiry + watch + watch + watch + expiry + watch
	if stderr.String() != want {
		t.Fatalf("stderr:\n%s\nwant:\n%s", stderr.String(), want)
	}
	if rows[0] != "autopilot_expiry_check_failed|"+ExpiryCheckFailed+" (EACCES)" || rows[1] != "watch_tick_failed|watch tick failed (TypeError)" || len(rows) != 6 {
		t.Fatalf("rows %v", rows)
	}
	if strings.Contains(stderr.String(), "secret") {
		t.Fatal("an error message leaked")
	}
}

func TestB485ErrorKind(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{&os.PathError{Op: "open", Path: "x", Err: syscall.ENOENT}, "ENOENT"},
		{&state.Error{Code: "record-unknown", Message: "no"}, "StateError:record-unknown"},
		{kindErr{}, "TypeError"},
		{errors.New("x"), "Error"},
		{fmt.Errorf("wrap: %w", syscall.EACCES), "EACCES"},
	} {
		if got := ErrorKind(c.err); got != c.want {
			t.Errorf("%v: %q, want %q", c.err, got, c.want)
		}
	}
}

func TestB485SessionCapsPerProviderAndTicket(t *testing.T) {
	newFixture(t, "")
	p := &Pool{BootCfg: map[string]any{"review": map[string]any{"parallel_tickets": float64(1)}}}
	if lim, _ := p.Limits(); lim != 1 {
		t.Fatalf("boot ticket limit %d, want 1", lim)
	}
	p.providerParallel = 1
	relA := p.AcquireSession("A", "anthropic")
	queued := func(n int) func() bool {
		return func() bool { p.mu.Lock(); defer p.mu.Unlock(); return len(p.queue) == n }
	}
	// a second ticket waits for the ticket limit even on another provider
	gotB := make(chan func(), 1)
	go func() { gotB <- p.AcquireSession("B", "openai") }()
	waitFor(t, "B queued", queued(1))
	// ticket A's second session waits for the provider cap, not the ticket limit
	gotA2 := make(chan func(), 1)
	go func() { gotA2 <- p.AcquireSession("A", "anthropic") }()
	waitFor(t, "A2 queued", queued(2))
	select {
	case <-gotB:
		t.Fatal("B started past the ticket limit")
	case <-gotA2:
		t.Fatal("A's second session started past the provider cap")
	default:
	}
	relA()
	relA() // releasing twice is a no-op
	// FIFO, like Node's dispatchSessions: A has no running session now, so the
	// first waiter (B) takes the ticket slot and A2 waits for it
	relB := <-gotB
	waitFor(t, "A2 still queued", queued(1))
	select {
	case <-gotA2:
		t.Fatal("A2 started past the ticket limit")
	default:
	}
	relB()
	relA2 := <-gotA2
	relA2()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.tickets) != 0 || p.providerActive["anthropic"] != 0 || p.providerActive["openai"] != 0 || len(p.queue) != 0 {
		t.Fatalf("state not freed: %v %v %v", p.tickets, p.providerActive, p.queue)
	}
}

func TestB485SessionCfgAndProvider(t *testing.T) {
	p := &Pool{}
	p.Limits()
	p.providerParallel = 3
	cfg := map[string]any{"provider": "openai", "review": map[string]any{"x": "y", "provider_concurrency": map[string]any{"xai": float64(1)}},
		"levels": map[string]any{"L2": map[string]any{"provider": "xai"}}}
	got := p.SessionCfg(cfg)
	want := map[string]any{"anthropic": float64(3), "openai": float64(2), "xai": float64(1)}
	review := got["review"].(map[string]any)
	if !reflect.DeepEqual(review["provider_concurrency"], want) || review["x"] != "y" {
		t.Fatalf("capped review %v", review)
	}
	if _, touched := cfg["review"].(map[string]any)["provider_concurrency"].(map[string]any)["anthropic"]; touched {
		t.Fatal("the input config was mutated")
	}
	p.providerParallel = 4
	if same := p.SessionCfg(cfg); !reflect.DeepEqual(same, cfg) || same["review"].(map[string]any)["provider_concurrency"].(map[string]any)["anthropic"] != nil {
		t.Fatal("a config already within the cap is kept as is")
	}
	if SessionProvider(cfg, "L2") != "xai" || SessionProvider(cfg, "L1") != "openai" || SessionProvider(nil, "L1") != "unresolved" {
		t.Fatal("SessionProvider")
	}
}
