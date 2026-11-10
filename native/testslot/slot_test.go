package testslot

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"
)

// idle is a quiet 8-core machine with 16 GB free: two changed slots.
var idle = Resources{Cores: 8, Load1: 0.5, FreeBytes: 16 << 30, Known: true}

func testQueue(t *testing.T, res Resources) *Queue {
	t.Helper()
	t.Setenv(EnvSwitch, "")
	t.Setenv(EnvHeld, "")
	q := Open(t.TempDir())
	q.Probe = func() Resources { return res }
	q.MinWait, q.MaxWait = 5*time.Millisecond, 20*time.Millisecond
	return q
}

type entrant struct {
	name     string
	admitted chan struct{}
	release  chan struct{}
	done     chan struct{}
}

// recorder tracks admission order and the most holders seen at once.
type recorder struct {
	mu       sync.Mutex
	order    []string
	holding  map[string]string
	maxFull  int
	maxTotal int
}

func (r *recorder) enter(name, kind string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.order = append(r.order, name)
	r.holding[name] = kind
	full := 0
	for _, k := range r.holding {
		if k == Full {
			full++
		}
	}
	if len(r.holding) > r.maxTotal {
		r.maxTotal = len(r.holding)
	}
	if full > 0 && len(r.holding) > r.maxFull {
		r.maxFull = len(r.holding)
	}
}

func (r *recorder) leave(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.holding, name)
}

func (r *recorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.order...)
}

func start(t *testing.T, q *Queue, rec *recorder, name, kind string) *entrant {
	t.Helper()
	e := &entrant{name: name, admitted: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(e.done)
		release, err := q.Acquire(context.Background(), Request{Kind: kind, Label: name})
		if err != nil {
			t.Errorf("%s acquire: %v", name, err)
			close(e.admitted)
			return
		}
		rec.enter(name, kind)
		close(e.admitted)
		<-e.release
		rec.leave(name)
		release()
	}()
	return e
}

func ticketCount(t *testing.T, q *Queue) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(q.Dir, "tickets"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return len(entries)
}

func waitTickets(t *testing.T, q *Queue, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for ticketCount(t, q) != n {
		if time.Now().After(deadline) {
			t.Fatalf("want %d tickets, have %d", n, ticketCount(t, q))
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func admittedWithin(e *entrant, d time.Duration) bool {
	select {
	case <-e.admitted:
		return true
	case <-time.After(d):
		return false
	}
}

func finish(t *testing.T, e *entrant) {
	t.Helper()
	close(e.release)
	<-e.done
}

func TestSlotFullRunsOneAtATimeInArrivalOrder(t *testing.T) {
	q := testQueue(t, idle)
	rec := &recorder{holding: map[string]string{}}
	a := start(t, q, rec, "A", Full)
	if !admittedWithin(a, 2*time.Second) {
		t.Fatal("A: the first full run on an idle queue must be admitted")
	}
	waitTickets(t, q, 1)
	b := start(t, q, rec, "B", Full)
	waitTickets(t, q, 2)
	c := start(t, q, rec, "C", Full)
	waitTickets(t, q, 3)
	if admittedWithin(b, 150*time.Millisecond) || admittedWithin(c, 10*time.Millisecond) {
		t.Fatalf("B and C must wait while A holds the machine; order %v", rec.snapshot())
	}
	finish(t, a)
	if !admittedWithin(b, 2*time.Second) {
		t.Fatal("B: the first waiter must be admitted once A releases")
	}
	if admittedWithin(c, 150*time.Millisecond) {
		t.Fatalf("C must wait while B holds the machine; order %v", rec.snapshot())
	}
	finish(t, b)
	if !admittedWithin(c, 2*time.Second) {
		t.Fatal("C: must be admitted once B releases")
	}
	finish(t, c)
	if got := rec.snapshot(); len(got) != 3 || got[0] != "A" || got[1] != "B" || got[2] != "C" {
		t.Fatalf("admission order = %v, want exactly [A B C]", got)
	}
	if rec.maxTotal != 1 {
		t.Fatalf("most holders at once = %d, want exactly 1 for full runs", rec.maxTotal)
	}
	if n := ticketCount(t, q); n != 0 {
		t.Fatalf("tickets left after three releases = %d, want 0", n)
	}
}

// TestSlotHelperHold is a child process that takes a full slot and holds it.
func TestSlotHelperHold(t *testing.T) {
	if os.Getenv("TESTSLOT_HELPER") != "hold" {
		return
	}
	q := Open(os.Getenv("HOME"))
	q.Probe = func() Resources { return idle }
	if _, err := q.Acquire(context.Background(), Request{Kind: Full, Label: "helper"}); err != nil {
		os.Exit(3)
	}
	os.Stdout.WriteString("held\n")
	time.Sleep(time.Minute)
	os.Exit(0)
}

func TestSlotKilledHolderFreesWithinTwoSeconds(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvSwitch, "")
	t.Setenv(EnvHeld, "")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestSlotHelperHold$")
	cmd.Env = append(os.Environ(), "HOME="+home, "TESTSLOT_HELPER=hold")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != "held\n" {
		t.Fatalf("helper did not take the slot: %q %v", line, err)
	}
	// Default 200 ms -> 1 s backoff: the 2 s bound is the real wait loop's.
	q := Open(home)
	q.Probe = func() Resources { return idle }
	rec := &recorder{holding: map[string]string{}}
	w := start(t, q, rec, "W", Full)
	waitTickets(t, q, 2)
	if admittedWithin(w, 300*time.Millisecond) {
		t.Fatal("W must wait while the helper process holds the slot")
	}
	killed := time.Now()
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if !admittedWithin(w, 2*time.Second) {
		t.Fatalf("W not admitted within 2 s of kill -9 (%v)", time.Since(killed))
	}
	t.Logf("admitted %v after kill -9", time.Since(killed).Round(time.Millisecond))
	finish(t, w)
	if n := ticketCount(t, q); n != 0 {
		t.Fatalf("tickets left = %d, want 0 (dead holder removed, W released)", n)
	}
}

func TestSlotReusedPidWithOtherStartTimeIsDead(t *testing.T) {
	q := testQueue(t, idle)
	// A live pid (this process) whose recorded start time differs is a
	// recycled pid: its ticket must be removed at the next check.
	stale := Ticket{ID: "00000000000000000001-1-1", PID: os.Getpid(), Start: "darwin:1.000001", Kind: Full, Label: "recycled", EnqueuedAt: time.Now().Add(-time.Minute)}
	admitted := stale.EnqueuedAt
	stale.AdmittedAt = &admitted
	if err := q.writeTicket(stale); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{holding: map[string]string{}}
	w := start(t, q, rec, "W", Full)
	if !admittedWithin(w, 2*time.Second) {
		t.Fatal("W must be admitted: the stale holder's start time does not match")
	}
	if _, err := os.Stat(filepath.Join(q.Dir, "tickets", stale.ID+".json")); !os.IsNotExist(err) {
		t.Fatalf("stale ticket must be removed, stat err = %v", err)
	}
	finish(t, w)
}

func TestSlotChangedPassesWaitingFullOnlyWhenSlotMathAllows(t *testing.T) {
	q := testQueue(t, idle)
	if n := Slots(idle); n != 2 {
		t.Fatalf("Slots(8 cores, 16 GB) = %d, want 2", n)
	}
	rec := &recorder{holding: map[string]string{}}
	h := start(t, q, rec, "H", Changed)
	if !admittedWithin(h, 2*time.Second) {
		t.Fatal("H: first changed run must be admitted")
	}
	waitTickets(t, q, 1)
	f := start(t, q, rec, "F", Full)
	waitTickets(t, q, 2)
	if admittedWithin(f, 100*time.Millisecond) {
		t.Fatal("F must wait while H holds a slot")
	}
	// Skip 1: a full run cannot start now (H holds) and a changed slot is free.
	c1 := start(t, q, rec, "C1", Changed)
	if !admittedWithin(c1, 2*time.Second) {
		t.Fatal("C1 must pass the waiting full run: H holds and 1 of 2 slots is free")
	}
	// Both slots taken: C2 must wait even though it may skip.
	c2 := start(t, q, rec, "C2", Changed)
	waitTickets(t, q, 4)
	if admittedWithin(c2, 150*time.Millisecond) {
		t.Fatalf("C2 must wait: both changed slots are held; order %v", rec.snapshot())
	}
	finish(t, h)
	// Skip 2: one slot free again, C1 still holds, skips 1 < N=2.
	if !admittedWithin(c2, 2*time.Second) {
		t.Fatal("C2 must pass the full run as the second skip")
	}
	finish(t, c1)
	// Skips 2 == N: C3 must now wait behind F although a slot is free.
	c3 := start(t, q, rec, "C3", Changed)
	waitTickets(t, q, 3)
	if admittedWithin(c3, 150*time.Millisecond) {
		t.Fatalf("C3 must not pass F after N=2 skips in a row; order %v", rec.snapshot())
	}
	finish(t, c2)
	if !admittedWithin(f, 2*time.Second) {
		t.Fatal("F must be admitted once no slot is held")
	}
	if admittedWithin(c3, 150*time.Millisecond) {
		t.Fatal("C3 must wait while the full run F holds the machine")
	}
	finish(t, f)
	if !admittedWithin(c3, 2*time.Second) {
		t.Fatal("C3 must be admitted after F")
	}
	finish(t, c3)
	if got := rec.snapshot(); len(got) != 5 || got[0] != "H" || got[1] != "C1" || got[2] != "C2" || got[3] != "F" || got[4] != "C3" {
		t.Fatalf("admission order = %v, want exactly [H C1 C2 F C3]", got)
	}
	if rec.maxFull != 1 || rec.maxTotal != 2 {
		t.Fatalf("holders with a full run = %d (want 1), most holders = %d (want 2)", rec.maxFull, rec.maxTotal)
	}
}

func TestSlotIdleAcquireIsFastAndLeavesNothingBehind(t *testing.T) {
	t.Setenv(EnvSwitch, "")
	t.Setenv(EnvHeld, "")
	q := Open(t.TempDir()) // default probe and backoff: the real gate path
	goroutines := runtime.NumGoroutine()
	const rounds = 50
	var total, worst time.Duration
	for i := 0; i < rounds; i++ {
		began := time.Now()
		release, err := q.Acquire(context.Background(), Request{Kind: Full, Label: "perf"})
		if err != nil {
			t.Fatal(err)
		}
		release()
		took := time.Since(began)
		total += took
		if took > worst {
			worst = took
		}
	}
	mean := total / rounds
	t.Logf("idle acquire+release: mean %v, worst %v over %d rounds", mean, worst, rounds)
	if mean >= 20*time.Millisecond {
		t.Fatalf("idle acquire+release mean = %v, want < 20ms", mean)
	}
	if n := ticketCount(t, q); n != 0 {
		t.Fatalf("tickets left after release = %d, want 0", n)
	}
	if n := runtime.NumGoroutine(); n > goroutines {
		t.Fatalf("goroutines after release = %d, want <= %d (nothing resident)", n, goroutines)
	}
}

func TestSlotOffSwitchReturnsAtOnce(t *testing.T) {
	q := testQueue(t, idle)
	release, err := q.Acquire(context.Background(), Request{Kind: Full, Label: "held"})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	t.Setenv(EnvSwitch, "off")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	second, err := q.Acquire(ctx, Request{Kind: Full, Label: "off"})
	if err != nil {
		t.Fatalf("CODE_FORGE_TEST_QUEUE=off must admit at once despite a holder: %v", err)
	}
	second()
	if n := ticketCount(t, q); n != 1 {
		t.Fatalf("tickets = %d, want 1 (the off request writes none)", n)
	}
}
