package healq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/ricardov03/code-forge/native/filelock"
)

// DefaultBackoff is the wait after the 1st, 2nd and 3rd (and later) failed try.
var DefaultBackoff = []time.Duration{15 * time.Second, 60 * time.Second, 240 * time.Second}

// DefaultMaxTries is how many retries an item gets before it needs a coder.
const DefaultMaxTries = 3

// Runner retries the open items of one run. Exe runs each stored argv as a
// child process (the same binary in production).
type Runner struct {
	Home, Run string
	Exe       string
	MaxTries  int
	Backoff   []time.Duration
	Now       func() time.Time
	Sleep     func(context.Context, time.Duration) error
	Log       io.Writer
	// Healed and NeedsCoder count the items that ended in this runner.
	Healed, NeedsCoder int
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) logf(format string, args ...any) {
	if r.Log != nil {
		fmt.Fprintf(r.Log, format+"\n", args...)
	}
}

func (r *Runner) sleep(ctx context.Context, d time.Duration) error {
	if r.Sleep != nil {
		return r.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func pending(item Item) bool { return item.Status == StatusOpen || item.Status == StatusRetrying }

func (r *Runner) dueAt(item Item) time.Time {
	if item.NextAt == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, item.NextAt)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Pass retries each pending item that is due, once, one by one.
func (r *Runner) Pass(ctx context.Context) error {
	items, err := Load(r.Home, r.Run)
	if err != nil {
		return err
	}
	for _, item := range items {
		if !pending(item) || r.dueAt(item).After(r.now()) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.retry(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

// Drain runs passes until no item is pending, sleeping until the next one is
// due in between.
func (r *Runner) Drain(ctx context.Context) error {
	for {
		if err := r.Pass(ctx); err != nil {
			return err
		}
		items, err := Load(r.Home, r.Run)
		if err != nil {
			return err
		}
		var next time.Time
		left := false
		for _, item := range items {
			if pending(item) {
				at := r.dueAt(item)
				if !left || at.Before(next) {
					next = at
				}
				left = true
			}
		}
		if !left {
			return nil
		}
		if wait := next.Sub(r.now()); wait > 0 {
			if err := r.sleep(ctx, wait); err != nil {
				return err
			}
		}
	}
}

// Watch runs a pass every poll until ctx ends.
func (r *Runner) Watch(ctx context.Context, poll time.Duration) error {
	for {
		if err := r.Pass(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if r.sleep(ctx, poll) != nil {
			return nil
		}
	}
}

func (r *Runner) retry(ctx context.Context, item Item) error {
	max := r.MaxTries
	if max <= 0 {
		max = DefaultMaxTries
	}
	if item.Kind == KindLock {
		if path, outcome := Reap(r.Home, item.Detail); path != "" {
			r.logf("heal: %s lock %s: %s", item.ID, path, outcome)
		}
	}
	item.Tries++
	item.Status, item.NextAt, item.UpdatedAt = StatusRetrying, "", stamp(r.now())
	if err := Append(r.Home, item); err != nil {
		return err
	}
	code, out := r.exec(ctx, item)
	if ctx.Err() != nil {
		// Stopped mid-try: the item stays pending for the next heal run.
		item.Status, item.UpdatedAt = StatusOpen, stamp(r.now())
		_ = Append(r.Home, item)
		return ctx.Err()
	}
	now := r.now()
	item.UpdatedAt = stamp(now)
	if code == 0 {
		item.Status = StatusHealed
		r.Healed++
		r.logf("heal: %s healed on try %d/%d", item.ID, item.Tries, max)
		return Append(r.Home, item)
	}
	if text := Trim(out); text != "" {
		item.Detail = text
	}
	_, retryable := Classify(item.Argv, out)
	switch {
	case item.Tries >= max:
		item.Status = StatusNeedsCoder
		r.NeedsCoder++
		r.logf("heal: %s needs a coder after %d tries (exit %d): %s", item.ID, item.Tries, code, lastLine(item.Detail))
	case !retryable:
		item.Status = StatusNeedsCoder
		r.NeedsCoder++
		r.logf("heal: %s needs a coder: try %d failed for a reason a retry does not fix (exit %d): %s", item.ID, item.Tries, code, lastLine(item.Detail))
	default:
		backoff := r.Backoff
		if len(backoff) == 0 {
			backoff = DefaultBackoff
		}
		wait := backoff[min(item.Tries-1, len(backoff)-1)]
		item.Status, item.NextAt = StatusOpen, stamp(now.Add(wait))
		r.logf("heal: %s try %d/%d failed (exit %d); next try in %s: %s", item.ID, item.Tries, max, code, wait, lastLine(item.Detail))
	}
	return Append(r.Home, item)
}

func lastLine(text string) string {
	text = strings.TrimSpace(text)
	if i := strings.LastIndexByte(text, '\n'); i >= 0 {
		return text[i+1:]
	}
	return text
}

// tail keeps the last 64 KiB written to it.
type tail struct{ buf []byte }

func (t *tail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - 64*1024; over > 0 {
		t.buf = append([]byte{}, t.buf[over:]...)
	}
	return len(p), nil
}

func (r *Runner) exec(ctx context.Context, item Item) (int, string) {
	if len(item.Argv) == 0 {
		return 2, "heal: the item has no argv"
	}
	cmd := exec.CommandContext(ctx, r.Exe, item.Argv...)
	if st, err := os.Stat(item.Cwd); err == nil && st.IsDir() {
		cmd.Dir = item.Cwd
	}
	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, RetryEnv+"=") {
			env = append(env, kv)
		}
	}
	cmd.Env = append(env, RetryEnv+"="+item.ID)
	var out tail
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	err := cmd.Run()
	if err == nil {
		return 0, string(out.buf)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if code := exit.ExitCode(); code > 0 {
			return code, string(out.buf)
		}
		return 1, string(out.buf) + "\n" + err.Error()
	}
	return 127, string(out.buf) + "\nheal: " + err.Error()
}

// ErrBusy means another heal run holds the run.
type ErrBusy struct {
	Run string
	PID int
}

func (e *ErrBusy) Error() string {
	if e.PID > 0 {
		return fmt.Sprintf("busy: another heal run (pid %d) is healing run %s; this one stops", e.PID, e.Run)
	}
	return fmt.Sprintf("busy: another heal run is healing run %s; this one stops", e.Run)
}

// Acquire takes the one-heal-run-per-run lock ("heal-run-<run>"). A dead
// holder's lock is reclaimed by the file lock rules.
func Acquire(run string) (func(), error) {
	key := "heal-run-" + run
	lock, err := filelock.TryLock(key, filelock.Options{})
	if err != nil {
		return nil, err
	}
	if lock.OK {
		return lock.Release, nil
	}
	busy := &ErrBusy{Run: run}
	if file, err := filelock.Path(key); err == nil {
		if data, err := os.ReadFile(file); err == nil {
			var owner struct {
				PID int `json:"pid"`
			}
			if json.Unmarshal(data, &owner) == nil {
				busy.PID = owner.PID
			}
		}
	}
	return nil, busy
}
