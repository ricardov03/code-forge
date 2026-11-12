package worker

import (
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ricardov03/code-forge/native/filelock"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// Error is Node's WorkerError: a stable code and the message the CLI prints
// as `worker: <message>` with exit 1.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

// Opts describe one worker of one run.
type Opts struct {
	Home  string
	RunID string
	Root  string         // realpath'd main tree top level
	Cfg   map[string]any // the boot config (snapshot in force, else the file)
	Pid   int            // default os.Getpid()
	// HeartbeatMs: an integer >= MinHeartbeatMs, else HeartbeatMs.
	HeartbeatMs int
	Probes      Probes
	// StartDaemon starts the native daemon and returns its stop; nil starts none.
	StartDaemon func() (stop func(), err error)
	// Hooks for deterministic boot races (loop.mjs deps.hooks).
	BeforeLock func()
	AfterCount func(count int)
}

// Repinned is B355's boot re-pin: every pinned worker was dead.
type Repinned struct {
	From []float64
	To   int
}

// Worker is one worker's lifecycle: boot, announce, heartbeat, stop, release.
type Worker struct {
	o           Opts
	mu          sync.Mutex
	self        ledger.Object
	stopping    bool
	beat        chan struct{}
	beatDone    chan struct{}
	stopDaemon  func()
	daemonStart bool
	done        chan struct{}
	// Repinned is set by Boot when it re-pinned the run to this process.
	Repinned *Repinned
}

// New prepares a worker without touching the disk.
func New(o Opts) *Worker {
	if o.Pid == 0 {
		o.Pid = os.Getpid()
	}
	if o.HeartbeatMs < MinHeartbeatMs {
		o.HeartbeatMs = HeartbeatMs
	}
	return &Worker{o: o, done: make(chan struct{})}
}

// Self is the announcement this worker wrote, or nil.
func (w *Worker) Self() ledger.Object {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.self.Clone()
}

// Stopping reports whether Stop was called.
func (w *Worker) Stopping() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stopping
}

// Done is closed by Stop.
func (w *Worker) Done() <-chan struct{} { return w.done }

// ReviewWorkers is config/validate.mjs reviewWorkers: review.workers ?? 1.
// A value Go cannot compare as Node does is undecided.
func ReviewWorkers(cfg map[string]any) (float64, error) {
	review, _ := cfg["review"].(map[string]any)
	v, present := review["workers"]
	if !present || v == nil {
		return 1, nil
	}
	n, ok := v.(float64)
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n {
		return 0, ErrUndecided
	}
	return n, nil
}

// Boot is the boot half of loop.mjs bootWorker, under the shared
// `worker-boot:<repo>` lock: the registry admission (review.workers), the
// B355 pin check and re-pin, the daemon, then the announcement. A refusal is
// an *Error; ErrUndecided means Node must serve the invocation.
func (w *Worker) Boot() error {
	if w.o.BeforeLock != nil {
		w.o.BeforeLock()
	}
	return filelock.WithLock("worker-boot:"+w.o.Root, func() error {
		p := w.o.Probes
		entries, err := Registry(w.o.Root, true, p)
		if err != nil {
			return err
		}
		count := 0
		for _, e := range entries {
			if e.Live {
				count++
			}
		}
		// Older workers have only worker.json; do not boot over a live legacy owner.
		legacy, err := Live(w.o.Root, LiveOpts{}, p)
		if err != nil {
			return err
		}
		if legacy != nil {
			listed := false
			for _, e := range entries {
				if e.Worker != nil && e.Worker.Get("pid") == legacy.Get("pid") && e.Worker.Get("start_time") == legacy.Get("start_time") {
					listed = true
				}
			}
			if !listed {
				count++
			}
		}
		if w.o.AfterCount != nil {
			w.o.AfterCount(count)
		}
		limit, err := ReviewWorkers(w.o.Cfg)
		if err != nil {
			return err
		}
		refuse := float64(count) >= limit
		if legacy != nil && legacy.Get("run") != w.o.RunID {
			refuse = true
		}
		for _, e := range entries {
			if e.Live && (e.Worker == nil || e.Worker.Get("run") != w.o.RunID) {
				refuse = true
			}
		}
		if refuse {
			return &Error{"worker-running", fmt.Sprintf("review.workers limit %s: a live or unreadable worker entry already serves this queue", jsNumber(limit))}
		}
		alive, dead, err := w.pinAtBoot()
		if err != nil {
			return err
		}
		if alive != nil {
			return &Error{"worker-running", fmt.Sprintf("another worker (pid %s) is pinned to run %s and still alive", jsNumber(*alive), w.o.RunID)}
		}
		// Startup must finish before worker.json signals readiness.
		err = func() error {
			if err := w.StartDaemon(); err != nil {
				return err
			}
			if len(dead) > 0 {
				if err := w.repin(); err != nil {
					return err
				}
				w.Repinned = &Repinned{From: dead, To: w.o.Pid}
			}
			return w.Announce()
		}()
		if err != nil {
			w.Stop()
			w.Release()
		}
		return err
	}, filelock.Options{})
}

// pinAtBoot is loop.mjs pinAtBoot (B355).
func (w *Worker) pinAtBoot() (alive *float64, dead []float64, err error) {
	record, err := state.ReadRun(w.o.Home, w.o.RunID)
	if err != nil {
		return nil, nil, err
	}
	pins, err := pinList(record)
	if err != nil {
		return nil, nil, err
	}
	served := false
	for _, raw := range pins {
		pin, isObject := raw.(ledger.Object)
		if !isObject {
			// pin?.pid of a non-object: Node pushes undefined into the joined list.
			return nil, nil, ErrUndecided
		}
		pid, signalable := intPid(pin.Get("pid"))
		var startedAt *string
		if signalable {
			startedAt = w.o.Probes.startTime(pidInt(pid))
		}
		var pinnedAt *string
		if s, ok := pin.Get("started_at").(string); ok {
			pinnedAt = normalize(s)
		}
		if startedAt == nil || pinnedAt == nil || *startedAt != *pinnedAt {
			n, isNumber := pin.Get("pid").(float64)
			if !isNumber {
				return nil, nil, ErrUndecided
			}
			dead = append(dead, n)
			continue
		}
		if pidInt(pid) == w.o.Pid {
			served = true
			continue
		}
		live, err := Live(w.o.Root, LiveOpts{Pid: pidInt(pid)}, w.o.Probes)
		if err != nil {
			return nil, nil, err
		}
		if live != nil && live.Get("run") == w.o.RunID {
			served = true
		} else if alive == nil {
			n := pid
			alive = &n
		}
	}
	if alive != nil || served {
		return alive, nil, nil
	}
	return nil, dead, nil
}

// pinList is `record.workers ?? (record.worker ? [record.worker] : [])`.
func pinList(record ledger.Object) ([]any, error) {
	if workers := record.Get("workers"); workers != nil {
		list, ok := workers.([]any)
		if !ok {
			return nil, ErrUndecided
		}
		return list, nil
	}
	switch v := record.Get("worker").(type) {
	case nil, bool:
		if v == true {
			return []any{v}, nil
		}
		return nil, nil
	case string:
		if v == "" {
			return nil, nil
		}
	case float64:
		if v == 0 {
			return nil, nil
		}
	}
	return []any{record.Get("worker")}, nil
}

func normalize(s string) *string {
	out := strings.Join(strings.Fields(s), " ")
	if out == "" {
		return nil
	}
	return &out
}

// repin is state/run.mjs reattachWorker for this process (worker.reattach).
func (w *Worker) repin() error {
	return state.WithRunLock(w.o.Home, w.o.RunID, func() error {
		current, err := state.ReadRun(w.o.Home, w.o.RunID)
		if err != nil {
			return err
		}
		if current.Get("status") != "active" {
			return &state.Error{Code: "run-ended", Message: fmt.Sprintf("run %s has ended", w.o.RunID)}
		}
		started := w.o.Probes.startTime(w.o.Pid)
		if started == nil {
			return &state.Error{Code: "worker-not-running", Message: fmt.Sprintf("worker pid %d is not running", w.o.Pid)}
		}
		pins, err := pinList(current)
		if err != nil {
			return err
		}
		history := []any{}
		if old := current.Get("worker_history"); old != nil {
			list, ok := old.([]any)
			if !ok {
				return ErrUndecided
			}
			history = append(history, list...)
		}
		pin := ledger.Object{{Key: "pid", Value: float64(w.o.Pid)}, {Key: "started_at", Value: *started}}
		current.Set("worker_history", append(history, pins...))
		current.Set("workers", []any{pin})
		current.Set("worker", pin)
		if _, err := state.WriteSigned(w.o.Home, w.o.RunID, ledger.Object{{Key: "event", Value: "worker.reattach"}, {Key: "pid", Value: float64(w.o.Pid)}}); err != nil {
			return err
		}
		return state.SaveRun(w.o.Home, current)
	})
}

// StartDaemon starts the daemon once; a stop that came first stops it again.
func (w *Worker) StartDaemon() error {
	w.mu.Lock()
	if w.daemonStart || w.o.StartDaemon == nil {
		w.daemonStart = true
		w.mu.Unlock()
		return nil
	}
	w.daemonStart = true
	w.mu.Unlock()
	stop, err := w.o.StartDaemon()
	if err != nil {
		return err
	}
	w.mu.Lock()
	w.stopDaemon = stop
	stopping := w.stopping
	w.mu.Unlock()
	if stopping {
		w.StopDaemon()
	}
	return nil
}

// StopDaemon stops the daemon this worker started, once.
func (w *Worker) StopDaemon() {
	w.mu.Lock()
	stop := w.stopDaemon
	w.stopDaemon = nil
	w.mu.Unlock()
	if stop != nil {
		stop()
	}
}

// Announce writes the announcement and starts the heartbeat (B30), which runs
// on its own timer, independent of any ticket. A second call is a no-op.
func (w *Worker) Announce() error {
	w.mu.Lock()
	if w.self != nil {
		w.mu.Unlock()
		return nil
	}
	w.mu.Unlock()
	self, err := Announce(w.o.Root, w.o.Pid, w.o.RunID, w.o.Probes)
	if err != nil {
		return err
	}
	w.mu.Lock()
	w.self = self
	w.mu.Unlock()
	w.stopBeating()
	beat, beatDone := make(chan struct{}), make(chan struct{})
	w.mu.Lock()
	w.beat, w.beatDone = beat, beatDone
	w.mu.Unlock()
	interval := time.Duration(w.o.HeartbeatMs) * time.Millisecond
	go func() {
		defer close(beatDone)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-beat:
				return
			case <-ticker.C:
			}
			// another worker announced itself over us: stop beating, never
			// overwrite its file; an fs error is retried on the next tick
			written, err := Beat(w.o.Root, self, w.o.Probes.now())
			if err == nil && !written {
				return
			}
		}
	}()
	return nil
}

func (w *Worker) stopBeating() {
	w.mu.Lock()
	beat, beatDone := w.beat, w.beatDone
	w.beat, w.beatDone = nil, nil
	w.mu.Unlock()
	if beat != nil {
		close(beat)
		<-beatDone
	}
}

// Stop starts no new work: it marks the worker stopping and stops the daemon.
func (w *Worker) Stop() {
	w.mu.Lock()
	first := !w.stopping
	w.stopping = true
	w.mu.Unlock()
	if first {
		close(w.done)
	}
	w.StopDaemon()
}

// Release stops the heartbeat and withdraws this process's announcements if
// they are still its own.
func (w *Worker) Release() error {
	w.StopDaemon()
	w.stopBeating()
	w.mu.Lock()
	self := w.self
	w.self = nil
	w.mu.Unlock()
	if self == nil {
		return nil
	}
	start, _ := self.Get("start_time").(string)
	return Retract(w.o.Root, w.o.Pid, &start, w.o.Probes)
}

// HandOver is Release for a worker that hands its run to Node in this same
// process (B561): the hand-over announcement is written first, then this
// worker's own announcements are withdrawn, so a waiter never sees the queue
// unserved while Node starts. Without a hand-over announcement it is Release.
func (w *Worker) HandOver() error {
	w.StopDaemon()
	w.stopBeating()
	w.mu.Lock()
	self := w.self
	w.self = nil
	w.mu.Unlock()
	if self == nil {
		return nil
	}
	_, _ = AnnounceHandOver(w.o.Root, self, w.o.Probes)
	start, _ := self.Get("start_time").(string)
	return Retract(w.o.Root, w.o.Pid, &start, w.o.Probes)
}

// RetractHandOver withdraws the hand-over announcement when the hand-over
// to Node failed (this process is about to exit).
func (w *Worker) RetractHandOver() { RetractHandOver(w.o.Root, w.o.Pid, w.o.Probes) }

// IsUndecided reports an input Node must judge.
func IsUndecided(err error) bool { return errors.Is(err, ErrUndecided) }
