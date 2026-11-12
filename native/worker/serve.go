package worker

// B489: loop.mjs run() and once() in Go — drain, orphan clean-up after a
// drain that completed tickets, the idle exit, the poll wait and the stop.
//
// The ticket split rule (B477 step 3). Go claims a ticket, then RunTicket
// either finishes it (done marker written, claim released) or returns
// ErrUnsupported having written nothing and holding no lock. On the first
// unsupported ticket the loop hands the WHOLE worker to Node:
//   1. the worker stops: no new ticket starts (the claimed ticket's claim is
//      released by the pool with no done marker, so it stays pending);
//   2. the tickets already running in Go finish (each writes its own done
//      marker, so Node's queue scan skips them);
//   3. the worker releases its announcement and daemon, and the CLI replaces
//      this process (same pid, same start time) with the Node worker.
// Node then boots on the same pin and reviews every pending ticket, the
// unported one included, once. A ticket is never lost (its file stays queued)
// and never reviewed twice (a done marker exists only for Go's own reviews,
// and Go takes no ticket after the hand-over begins).

import (
	"errors"
	"math"
	"sync"
	"time"

	"github.com/ricardov03/code-forge/native/autopilot"
	"github.com/ricardov03/code-forge/native/blockops"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// Loop serves one booted worker's queue.
type Loop struct {
	Worker *Worker
	Pool   *Pool
	Runner *TicketRunner
	PollMs int
	Now    func() time.Time
	// Wait sleeps d or until wake fires (nil: a timer).
	Wait func(d time.Duration, wake <-chan struct{})
	// CleanOrphans runs after a drain that completed tickets (nil: none).
	CleanOrphans func()
	// Idle is the idle exit (nil: never).
	Idle *Idle

	mu      sync.Mutex
	handoff string
	wake    chan struct{}
}

// Handoff is the reason the loop stopped to hand the worker to Node ("" none).
func (l *Loop) Handoff() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.handoff
}

// HandOff stops the worker so Node can serve the run; the first reason wins.
func (l *Loop) HandOff(reason string) {
	l.mu.Lock()
	if l.handoff == "" {
		l.handoff = reason
	}
	l.mu.Unlock()
	l.Stop()
}

// Stop is loop.mjs stop(): no new ticket; running ones are awaited.
func (l *Loop) Stop() {
	l.Worker.Stop()
	l.Pool.Wake()
	l.signal()
}

func (l *Loop) signal() {
	l.mu.Lock()
	if l.wake == nil {
		l.wake = make(chan struct{}, 1)
	}
	wake := l.wake
	l.mu.Unlock()
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (l *Loop) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// RunOne is the pool's runOne over RunTicket; an unsupported ticket starts
// the hand-over and counts as not completed.
func (l *Loop) RunOne(id string) (int, bool) {
	n, poison, err := l.Runner.RunTicket(id)
	if err != nil {
		l.HandOff("ticket " + id + ": " + err.Error())
		return 0, false
	}
	return n, poison
}

// ExpiryCheck is expiryChecks for the pool: the autopilot grant expiry in Go;
// an approval or a grant Go does not decide hands the worker to Node.
func (l *Loop) ExpiryCheck() error {
	home, run := l.Runner.Home, l.Runner.RunID
	record, err := state.ReadRun(home, run)
	if err != nil {
		var se *state.Error
		if errors.As(err, &se) && (se.Code == "no-run" || se.Code == "bad-run-id") {
			return nil
		}
		return err
	}
	if _, has := record.Get("autopilot_approvals").([]any); record.Get("autopilot_approvals") != nil && (!has || !approvalsQuiet(record)) {
		l.HandOff("autopilot approvals")
		return nil
	}
	if _, err := autopilot.CheckExpiry(home, run, l.now()); err != nil {
		var se *state.Error
		if errors.As(err, &se) && (se.Code == "no-run" || se.Code == "bad-run-id") {
			return nil
		}
		if errors.Is(err, autopilot.ErrUnsupported) {
			l.HandOff("autopilot grant")
			return nil
		}
		return err
	}
	return nil
}

// approvalsQuiet: no approval is active (checkApprovals has nothing to do).
func approvalsQuiet(record ledger.Object) bool {
	without := record.Clone()
	without.Delete("autopilot")
	return blockops.QuietExpiry(without)
}

func (l *Loop) waitFor(d time.Duration) {
	l.mu.Lock()
	if l.wake == nil {
		l.wake = make(chan struct{}, 1)
	}
	wake := l.wake
	l.mu.Unlock()
	if l.Wait != nil {
		l.Wait(d, wake)
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-wake:
	}
}

// Run serves the queue until Stop (run()). Go has no queue watcher, so it
// polls every PollMs: loop.mjs's interval when fs.watch is unavailable.
func (l *Loop) Run() {
	defer func() { _ = l.Worker.Release() }()
	poll := l.PollMs
	if poll <= 0 {
		poll = DefaultPollMs
	}
	for !l.Worker.Stopping() {
		if done, err := l.Pool.Drain(); err == nil {
			if done > 0 && l.CleanOrphans != nil {
				l.CleanOrphans()
			}
			if l.Idle != nil {
				l.Idle.Check(l)
			}
		}
		if l.Worker.Stopping() {
			break
		}
		l.waitFor(time.Duration(poll) * time.Millisecond)
	}
}

// Once drains the queue once, then stops and releases (once()).
func (l *Loop) Once() (int, error) {
	defer func() {
		l.Stop()
		_ = l.Worker.Release()
	}()
	done, err := l.Pool.Drain()
	if err != nil {
		return done, err
	}
	if done > 0 && l.CleanOrphans != nil {
		l.CleanOrphans()
	}
	return done, nil
}

// Idle is loop.mjs checkIdle.
type Idle struct {
	RepoRoot string
	// Activity is runActivityAt's deps (Trees, Git, ReadRows); NowMs and
	// Ignored are set per check.
	Activity ActivityDeps
	// Hours is idleExitHours of the config in force (an error: no idle exit).
	Hours func() (float64, error)
	// Record reads the run record (an error: no idle exit).
	Record func() (ledger.Object, error)
	// Row writes the signed worker.idle_exit row (failures are swallowed).
	Row      func(ledger.Object) error
	Identity ledger.Object

	lastCheck    float64
	checked      bool
	lastActivity float64
}

// Check runs at most once per minute; it stops the loop after idle_exit_hours
// without activity and with no pending ticket (poisoned ones aside).
func (c *Idle) Check(l *Loop) {
	at := l.now()
	nowMs := float64(at.UnixMilli())
	if c.checked && nowMs-c.lastCheck < 60000 {
		return
	}
	c.checked, c.lastCheck = true, nowMs
	if l.Pool.TakeTicketSinceCheck() {
		return
	}
	record, err := c.Record()
	if err != nil {
		return
	}
	hours, err := c.Hours()
	if err != nil || hours == 0 {
		return
	}
	if last := l.Pool.LastActivityAt(); !last.IsZero() {
		c.lastActivity = math.Max(c.lastActivity, float64(last.UnixMilli()))
	}
	deps := c.Activity
	deps.NowMs = nowMs
	deps.RepoRoot = c.RepoRoot
	deps.Ignored = l.Pool.Poisoned
	activity, err := RunActivityAt(record, l.Runner.RunID, deps)
	if err != nil {
		return
	}
	c.lastActivity = math.Max(c.lastActivity, activity)
	if l.Worker.Stopping() || nowMs-c.lastActivity < hours*3600000 {
		return
	}
	for _, id := range Pending(c.RepoRoot) {
		if !l.Pool.Poisoned(id) {
			return
		}
	}
	l.Stop()
	if c.Row != nil {
		_ = c.Row(ledger.Object{{Key: "event", Value: "worker.idle_exit"}, {Key: "ts", Value: ISO(at)}, {Key: "idle_hours", Value: hours}, {Key: "worker", Value: c.Identity}})
	}
}
