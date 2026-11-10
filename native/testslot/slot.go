// Package testslot is the machine-wide FIFO queue that test runs take before
// they start (B352). There is no daemon and no resident process: every caller
// works on the queue files under one flock, then sleeps in its own process.
//
// Layout under $HOME/.code-forge/queue/:
//
//	(the directory)     flock'd for every read or change of the queue
//	state.json          the fair-skip counter; present only while it counts
//	tickets/<id>.json   one per waiter or holder (pid, process start time,
//	                    kind, cwd, label, enqueued_at, admitted_at once held)
//
// Admission, checked by each waiter under the lock:
//   - FIFO by enqueued_at.
//   - A full run needs the whole machine: no other holder.
//   - Changed runs share up to N = max(1, min(cores/4, freeGB/2)) slots.
//   - While another run holds a slot, admit only when the 1-minute load is
//     below the core count and free memory is above MemFloor (1 GB). With no
//     holder the head waiter starts at once: the load is either not the
//     queue's or left by a run that just ended, so a lone gate run (and every
//     test fixture with a private HOME) never stalls on machine load.
//   - Fair skip: a changed waiter may pass waiting full runs only when a full
//     run cannot start now (other holders exist) and a changed slot is free.
//     The first full waiter is passed at most N times in a row.
//   - A ticket whose pid is gone, is a zombie, or has a different process
//     start time is removed at the next check.
//
// CODE_FORGE_TEST_QUEUE=off disables the queue: Acquire returns at once.
// CODE_FORGE_TEST_QUEUE_HELD is set by callers for the processes they run
// inside a slot, so a nested acquire (a test that runs a gate) never waits on
// its own parent.
package testslot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	EnvSwitch = "CODE_FORGE_TEST_QUEUE"
	EnvHeld   = "CODE_FORGE_TEST_QUEUE_HELD"
	Full      = "full"
	Changed   = "changed"
)

// Resources is one probe of the machine. Known is false when load or free
// memory could not be read; unknown resources never block admission.
type Resources struct {
	Cores     int
	Load1     float64
	FreeBytes uint64
	Known     bool
}

// Ticket is one waiter or holder. AdmittedAt is set once it holds a slot.
type Ticket struct {
	ID         string     `json:"id"`
	PID        int        `json:"pid"`
	Start      string     `json:"start"`
	Kind       string     `json:"kind"`
	Cwd        string     `json:"cwd"`
	Label      string     `json:"label"`
	Run        string     `json:"run,omitempty"`
	Block      string     `json:"block,omitempty"`
	EnqueuedAt time.Time  `json:"enqueued_at"`
	AdmittedAt *time.Time `json:"admitted_at,omitempty"`
}

// Holding reports whether the ticket holds a slot.
func (t Ticket) Holding() bool { return t.AdmittedAt != nil }

// Queue is one queue directory with its admission settings. Probe, MemFloor
// and the wait bounds are fields so tests can inject them.
type Queue struct {
	Dir              string
	Probe            func() Resources
	MemFloor         uint64
	MinWait, MaxWait time.Duration
}

// Request names one acquire. OnWait runs once when the request has to wait
// (position is 1-based among waiters); OnAdmit runs when a waiter is admitted.
type Request struct {
	Kind, Label, Cwd, Run, Block string
	OnWait                       func(position int, holders []Ticket)
	OnAdmit                      func(waited time.Duration)
}

type state struct {
	Head  string `json:"head,omitempty"`
	Skips int    `json:"skips,omitempty"`
}

var sequence atomic.Uint64

// Open returns the queue under home/.code-forge/queue with the default rules.
func Open(home string) *Queue {
	return &Queue{
		Dir:      filepath.Join(home, ".code-forge", "queue"),
		Probe:    SystemResources,
		MemFloor: 1 << 30,
		MinWait:  200 * time.Millisecond,
		MaxWait:  time.Second,
	}
}

// Default opens the queue under $HOME.
func Default() (*Queue, error) {
	home := os.Getenv("HOME")
	if home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return nil, err
		}
	}
	return Open(home), nil
}

// Disabled reports whether this process must not queue: the queue is off,
// or the process already runs inside a slot.
func Disabled() bool {
	return strings.EqualFold(os.Getenv(EnvSwitch), "off") || os.Getenv(EnvHeld) != ""
}

// Slots is the number of changed runs that may hold at once:
// max(1, min(cores/4, freeGB/2)). Unknown memory counts cores only.
func Slots(r Resources) int {
	n := r.Cores / 4
	if r.Known {
		if byMemory := int(r.FreeBytes>>30) / 2; byMemory < n {
			n = byMemory
		}
	}
	if n < 1 {
		n = 1
	}
	return n
}

// Describe renders holders for one status line, e.g. "full pid 42 gates r/B1".
func Describe(holders []Ticket) string {
	if len(holders) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(holders))
	for _, h := range holders {
		part := fmt.Sprintf("%s pid %d", h.Kind, h.PID)
		if h.Label != "" {
			part += " " + h.Label
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ", ")
}

// Acquire blocks until the request is admitted and returns its release
// function. A cancelled context removes the ticket and returns ctx.Err().
func (q *Queue) Acquire(ctx context.Context, r Request) (func(), error) {
	if Disabled() {
		return func() {}, nil
	}
	if r.Kind != Full && r.Kind != Changed {
		return nil, fmt.Errorf("test queue: kind must be full or changed, got %q", r.Kind)
	}
	cwd := r.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	me := Ticket{
		PID:   os.Getpid(),
		Start: selfStart(),
		Kind:  r.Kind, Cwd: cwd, Label: r.Label, Run: r.Run, Block: r.Block,
	}
	var (
		admitted bool
		position int
		holders  []Ticket
	)
	check := func() error {
		return q.locked(func(st *state) (err error) {
			now := time.Now()
			if me.ID == "" {
				// Stamped under the lock, so arrival order is lock order.
				me.ID = fmt.Sprintf("%020d-%d-%d", now.UnixNano(), me.PID, sequence.Add(1))
				me.EnqueuedAt = now
			}
			admitted, position, holders, err = q.check(&me, st, now)
			return err
		})
	}
	if err := check(); err != nil {
		if me.ID != "" {
			q.drop(me.ID)
		}
		return nil, err
	}
	wait, waited := max(q.MinWait, 10*time.Millisecond), false
	for !admitted {
		if !waited && r.OnWait != nil {
			r.OnWait(position, holders)
		}
		waited = true
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			q.drop(me.ID)
			return nil, ctx.Err()
		case <-timer.C:
		}
		if wait = wait * 3 / 2; wait > q.MaxWait {
			wait = q.MaxWait
		}
		if err := check(); err != nil {
			q.drop(me.ID)
			return nil, err
		}
	}
	if waited && r.OnAdmit != nil {
		r.OnAdmit(time.Since(me.EnqueuedAt))
	}
	var once sync.Once
	return func() { once.Do(func() { q.drop(me.ID) }) }, nil
}

// Snapshot returns live holders (oldest admission first) and waiters (queue
// order), removing dead tickets on the way.
func (q *Queue) Snapshot() (holders, waiters []Ticket, err error) {
	err = q.locked(func(*state) error {
		live, e := q.live("")
		holders, waiters = split(live)
		return e
	})
	return holders, waiters, err
}

// check runs under the lock: prune dead tickets, place me, decide admission.
func (q *Queue) check(me *Ticket, st *state, now time.Time) (bool, int, []Ticket, error) {
	others, err := q.live(me.ID)
	if err != nil {
		return false, 0, nil, err
	}
	holders, waiters := split(append(others, *me))
	position := 0
	for i, w := range waiters {
		if w.ID == me.ID {
			position = i + 1
		}
	}
	admit := q.decide(me, holders, waiters, position-1, st)
	if admit {
		at := now
		me.AdmittedAt = &at
	}
	// Always rewrite my ticket: it may be new, admitted, or removed by hand.
	if err := q.writeTicket(*me); err != nil {
		return false, 0, nil, err
	}
	return admit, position, holders, nil
}

func (q *Queue) decide(me *Ticket, holders, waiters []Ticket, index int, st *state) bool {
	// Load and memory only hold a run back beside another holder: with no
	// holder the load is either not ours or left by a run that just ended.
	slots := 1
	if len(holders) > 0 {
		res := q.Probe()
		slots = Slots(res)
		if res.Known && (res.Load1 >= float64(res.Cores) || res.FreeBytes <= q.MemFloor) {
			return false
		}
	}
	fullHeld, changedHeld := false, 0
	for _, h := range holders {
		if h.Kind == Full {
			fullHeld = true
		} else {
			changedHeld++
		}
	}
	fits := len(holders) == 0
	if me.Kind == Changed {
		fits = !fullHeld && changedHeld < slots
	}
	if index == 0 {
		if fits && st.Head == me.ID {
			*st = state{}
		}
		return fits
	}
	// Someone waits ahead: only a changed run may pass, only full runs, and
	// only while a full run cannot start anyway.
	if me.Kind != Changed || !fits || len(holders) == 0 {
		return false
	}
	for _, w := range waiters[:index] {
		if w.Kind != Full {
			return false
		}
	}
	head := waiters[0].ID
	skips := 0
	if st.Head == head {
		skips = st.Skips
	}
	if skips >= slots {
		return false
	}
	*st = state{Head: head, Skips: skips + 1}
	return true
}

// live reads every ticket except skip, removing dead and unreadable ones.
func (q *Queue) live(skip string) ([]Ticket, error) {
	dir := filepath.Join(q.Dir, "tickets")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := []Ticket{}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".json") || name == skip+".json" {
			continue
		}
		file := filepath.Join(dir, name)
		data, err := os.ReadFile(file)
		if err != nil {
			continue // released between the listing and the read
		}
		var t Ticket
		if json.Unmarshal(data, &t) != nil || t.ID+".json" != name || !alive(t) {
			_ = os.Remove(file)
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

func split(tickets []Ticket) (holders, waiters []Ticket) {
	for _, t := range tickets {
		if t.Holding() {
			holders = append(holders, t)
		} else {
			waiters = append(waiters, t)
		}
	}
	sort.Slice(holders, func(i, j int) bool {
		if !holders[i].AdmittedAt.Equal(*holders[j].AdmittedAt) {
			return holders[i].AdmittedAt.Before(*holders[j].AdmittedAt)
		}
		return holders[i].ID < holders[j].ID
	})
	sort.Slice(waiters, func(i, j int) bool {
		if !waiters[i].EnqueuedAt.Equal(waiters[j].EnqueuedAt) {
			return waiters[i].EnqueuedAt.Before(waiters[j].EnqueuedAt)
		}
		return waiters[i].ID < waiters[j].ID
	})
	return holders, waiters
}

// locked runs fn under an exclusive flock on the queue directory and saves a
// changed state. The lock sits on the directory, not on state.json, so the
// state file can be removed while it is empty: an idle queue leaves no file
// behind (a Go gate run leaves HOME as a Node gate run does).
func (q *Queue) locked(fn func(*state) error) error {
	if err := os.MkdirAll(filepath.Join(q.Dir, "tickets"), 0o700); err != nil {
		return err
	}
	dir, err := os.Open(q.Dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := lockFile(dir); err != nil {
		return err
	}
	defer unlockFile(dir)
	file := filepath.Join(q.Dir, "state.json")
	var st state
	if data, err := os.ReadFile(file); err == nil {
		_ = json.Unmarshal(data, &st)
	}
	before := st
	if err := fn(&st); err != nil {
		return err
	}
	if st == before {
		return nil
	}
	if st == (state{}) {
		if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	data, _ := json.Marshal(st)
	tmp := filepath.Join(q.Dir, ".state.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

func (q *Queue) writeTicket(t Ticket) error {
	dir := filepath.Join(q.Dir, "tickets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+t.ID+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, t.ID+".json"))
}

func (q *Queue) drop(id string) { _ = os.Remove(filepath.Join(q.Dir, "tickets", id+".json")) }

var (
	selfOnce  sync.Once
	selfValue string
)

// selfStart is this process's start token. It is "" where no start-time probe
// exists (or the probe failed); such tickets fall back to the pid check alone,
// so a reused pid is not detected for them.
func selfStart() string {
	selfOnce.Do(func() { selfValue, _ = processStart(os.Getpid()) })
	return selfValue
}
