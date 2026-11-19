package worker

// B489: loop.mjs run() and once() in Go — drain, orphan clean-up after a
// drain that completed tickets, the idle exit, the poll wait and the stop.
//
// B884: a ticket RunTicket cannot serve (ErrUnsupported) fails only that
// ticket: it is closed as `unsupported` (TicketRunner.Unsupported: done
// marker, signed review.unsupported row) and the worker goes on serving.
// An autopilot grant or approval shape Go cannot read stops the autopilot
// work of the drain only (one signed worker.autopilot_unsupported row per
// streak), never the worker. Before, either stopped the whole worker (the
// B477 hand-over, a plain exit since B690a) and a ticket left queued killed
// every new worker the same way. The commit watch hand-over (B534) is the
// one hand-over left; the loop records why it stopped (ExitReason) so the
// CLI writes one signed worker.exit row.

import (
	"errors"
	"fmt"
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
	// ApprovalsServed tells, before any write, whether CheckApprovals serves
	// checkApprovals on record at now; CheckApprovals runs it and reports a
	// hand-over it found before writing (B616). nil: an approval that is not
	// quiet hands the worker to Node.
	ApprovalsServed func(record ledger.Object, now time.Time) bool
	CheckApprovals  func(now time.Time) (handOver bool, err error)

	mu      sync.Mutex
	handoff string
	wake    chan struct{}
	// exitReason/exitDetail: why the loop stopped (the first reason wins).
	exitReason, exitDetail string
	// autopilotStuck is the autopilot shape reported this streak ("" none).
	autopilotStuck string
}

// StopWith stops the loop and records why (the first reason wins).
func (l *Loop) StopWith(reason, detail string) {
	l.mu.Lock()
	if l.exitReason == "" {
		l.exitReason, l.exitDetail = reason, detail
	}
	l.mu.Unlock()
	l.Stop()
}

// ExitReason is why the loop stopped: the hand-over, else StopWith's reason
// ("" when nothing recorded one).
func (l *Loop) ExitReason() (reason, detail string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.handoff != "" {
		return "handover", l.handoff
	}
	return l.exitReason, l.exitDetail
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

// RunOne is the pool's runOne over RunTicket. B884: a ticket Go cannot
// serve is closed as unsupported (done marker, signed row) and counts as
// completed; when even that cannot be written it is skipped for this
// worker's life. The worker never stops for it.
func (l *Loop) RunOne(id string) (int, bool) {
	n, poison, err := l.Runner.RunTicket(id)
	if err != nil {
		if l.Runner.Unsupported(id, err.Error()) != nil {
			return 0, true
		}
		return 1, false
	}
	return n, poison
}

// AutopilotUnsupportedEvent is the signed row of an autopilot shape the
// worker cannot read (B884).
const AutopilotUnsupportedEvent = "worker.autopilot_unsupported"

// autopilotUnsupported skips the autopilot work of this drain: one signed
// worker.autopilot_unsupported row per streak, the worker keeps serving.
func (l *Loop) autopilotUnsupported(reason string) {
	l.mu.Lock()
	repeat := l.autopilotStuck == reason
	l.autopilotStuck = reason
	l.mu.Unlock()
	if repeat {
		return
	}
	_ = l.Runner.Ledger(ledger.Object{{Key: "event", Value: AutopilotUnsupportedEvent}, {Key: "ts", Value: ISO(l.now())},
		{Key: "reason", Value: reason}, {Key: "worker", Value: l.Runner.Worker}})
}

func (l *Loop) autopilotServed() {
	l.mu.Lock()
	l.autopilotStuck = ""
	l.mu.Unlock()
}

// The reasons of worker.autopilot_unsupported.
const (
	AutopilotGrantUnsupported     = "autopilot grant: the run's grant has a shape the Go worker cannot read; grant expiry is skipped until it is fixed (autopilot stop, then start a new grant)"
	AutopilotApprovalsUnsupported = "autopilot approvals: an approval the Go worker cannot decide; approval expiry and restore are skipped until it is fixed (autopilot approve or revert it by hand)"
)

// ExpiryCheck is expiryChecks for the pool (B48): the grant's lazy expiry,
// then checkApprovals and its restore (B616: both in Go). A grant until or
// an approval Go does not read like Node skips the autopilot work of the
// drain, decided before anything is written (B884: never the worker).
func (l *Loop) ExpiryCheck() error {
	home, run := l.Runner.Home, l.Runner.RunID
	now := l.now()
	record, err := state.ReadRun(home, run)
	if err != nil {
		if autopilot.IsNoRun(err) {
			return nil
		}
		return err
	}
	if !autopilot.ExpireSupported(record, now) {
		l.autopilotUnsupported(AutopilotGrantUnsupported)
		return nil
	}
	if !approvalsQuiet(record) && (l.CheckApprovals == nil || l.ApprovalsServed == nil || !l.ApprovalsServed(record, now)) {
		l.autopilotUnsupported(AutopilotApprovalsUnsupported)
		return nil
	}
	if err := autopilot.ExpireAny(home, run, now); err != nil {
		if errors.Is(err, autopilot.ErrUnsupported) {
			l.autopilotUnsupported(AutopilotGrantUnsupported) // the record changed between the reads
			return nil
		}
		if !autopilot.IsNoRun(err) {
			return err
		}
	}
	if l.CheckApprovals == nil {
		return nil
	}
	handOver, err := l.CheckApprovals(now)
	if handOver {
		l.autopilotUnsupported(AutopilotApprovalsUnsupported)
		return nil
	}
	if err == nil {
		l.autopilotServed()
	}
	return err
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

// release withdraws the announcement; a worker handing its run to Node
// leaves the hand-over announcement until Node's boot prunes it (B561).
func (l *Loop) release() {
	if l.Handoff() != "" {
		_ = l.Worker.HandOver()
		return
	}
	_ = l.Worker.Release()
}

// Run serves the queue until Stop (run()). Go has no queue watcher, so it
// polls every PollMs: loop.mjs's interval when fs.watch is unavailable.
func (l *Loop) Run() {
	defer l.release()
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
		l.release()
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
	l.StopWith("idle", fmt.Sprintf("no activity for %v hour(s) and no pending ticket", hours))
	if c.Row != nil {
		_ = c.Row(ledger.Object{{Key: "event", Value: "worker.idle_exit"}, {Key: "ts", Value: ISO(at)}, {Key: "idle_hours", Value: hours}, {Key: "worker", Value: c.Identity}})
	}
}
