package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/testslot"
)

func init() { routes["queue"] = runQueue }

const queueUsage = "usage: code-forge queue [status] [--json]\n" +
	"       code-forge queue run [--kind full|changed] [--label X] -- <cmd> [args]\n" +
	"  The machine-wide FIFO test queue ($HOME/.code-forge/queue). CODE_FORGE_TEST_QUEUE=off disables it.\n"

func runQueue(args []string) int {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprint(os.Stdout, queueUsage)
		return 0
	}
	if len(args) > 0 && args[0] == "run" {
		return queueRun(args[1:])
	}
	if len(args) > 0 && args[0] == "status" {
		args = args[1:]
	}
	jsonFlag := false
	for _, a := range args {
		switch a {
		case "--json":
			jsonFlag = true
		case "--help", "-h":
			fmt.Fprint(os.Stdout, queueUsage)
			return 0
		default:
			fmt.Fprint(os.Stderr, queueUsage)
			return 2
		}
	}
	return queueStatus(jsonFlag)
}

type queueEntry struct {
	Position   int        `json:"position,omitempty"`
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	PID        int        `json:"pid"`
	Label      string     `json:"label"`
	Cwd        string     `json:"cwd"`
	Run        string     `json:"run,omitempty"`
	Block      string     `json:"block,omitempty"`
	EnqueuedAt time.Time  `json:"enqueued_at"`
	AdmittedAt *time.Time `json:"admitted_at,omitempty"`
	WaitMS     int64      `json:"wait_ms"`
	HeldMS     *int64     `json:"held_ms,omitempty"`
}

func queueEntryOf(t testslot.Ticket, position int, now time.Time) queueEntry {
	e := queueEntry{Position: position, ID: t.ID, Kind: t.Kind, PID: t.PID, Label: t.Label, Cwd: t.Cwd, Run: t.Run, Block: t.Block, EnqueuedAt: t.EnqueuedAt, AdmittedAt: t.AdmittedAt}
	end := now
	if t.AdmittedAt != nil {
		end = *t.AdmittedAt
		held := now.Sub(*t.AdmittedAt).Milliseconds()
		e.HeldMS = &held
	}
	e.WaitMS = end.Sub(t.EnqueuedAt).Milliseconds()
	return e
}

func queueStatus(jsonFlag bool) int {
	q, err := testslot.Default()
	if err != nil {
		fmt.Fprintln(os.Stderr, "queue:", err)
		return 1
	}
	holders, waiters, err := q.Snapshot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "queue:", err)
		return 1
	}
	now := time.Now()
	view := struct {
		Holders []queueEntry `json:"holders"`
		Waiters []queueEntry `json:"waiters"`
		Slots   int          `json:"changed_slots"`
	}{Holders: []queueEntry{}, Waiters: []queueEntry{}, Slots: testslot.Slots(q.Probe())}
	for _, h := range holders {
		view.Holders = append(view.Holders, queueEntryOf(h, 0, now))
	}
	for i, w := range waiters {
		view.Waiters = append(view.Waiters, queueEntryOf(w, i+1, now))
	}
	if jsonFlag {
		data, _ := json.Marshal(view)
		fmt.Fprintln(os.Stdout, string(data))
		return 0
	}
	if len(holders)+len(waiters) == 0 {
		fmt.Fprintf(os.Stdout, "test queue: empty · changed slots %d\n", view.Slots)
		return 0
	}
	fmt.Fprintf(os.Stdout, "test queue: %d holding, %d waiting · changed slots %d\n", len(holders), len(waiters), view.Slots)
	ms := func(n int64) string { return (time.Duration(n) * time.Millisecond).Round(time.Second).String() }
	for _, e := range view.Holders {
		held := int64(0)
		if e.HeldMS != nil {
			held = *e.HeldMS
		}
		fmt.Fprintf(os.Stdout, "holding  %-7s pid %-7d %s · held %s · waited %s\n", e.Kind, e.PID, e.Label, ms(held), ms(e.WaitMS))
	}
	for _, e := range view.Waiters {
		fmt.Fprintf(os.Stdout, "#%-7d %-7s pid %-7d %s · waiting %s\n", e.Position, e.Kind, e.PID, e.Label, ms(e.WaitMS))
	}
	return 0
}

// queueNotices prints the one-line queue messages a waiting run shows.
func queueNotices(w io.Writer) (func(int, []testslot.Ticket), func(time.Duration)) {
	return func(position int, holders []testslot.Ticket) {
			fmt.Fprintf(w, "queued #%d (holders: %s)\n", position, testslot.Describe(holders))
		}, func(waited time.Duration) {
			fmt.Fprintf(w, "queue: admitted after %s\n", waited.Round(100*time.Millisecond))
		}
}

func queueRun(args []string) int {
	kind, label := testslot.Changed, ""
	var argv []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		value := func() (string, bool) {
			if i+1 >= len(args) || args[i+1] == "--" {
				return "", false
			}
			i++
			return args[i], true
		}
		ok := true
		switch {
		case a == "--":
			argv = args[i+1:]
			i = len(args)
		case a == "--kind":
			kind, ok = value()
		case strings.HasPrefix(a, "--kind="):
			kind = strings.TrimPrefix(a, "--kind=")
		case a == "--label":
			label, ok = value()
		case strings.HasPrefix(a, "--label="):
			label = strings.TrimPrefix(a, "--label=")
		case a == "--help" || a == "-h":
			fmt.Fprint(os.Stdout, queueUsage)
			return 0
		default:
			ok = false
		}
		if !ok {
			fmt.Fprint(os.Stderr, queueUsage)
			return 2
		}
	}
	if len(argv) == 0 || (kind != testslot.Full && kind != testslot.Changed) {
		fmt.Fprint(os.Stderr, queueUsage)
		return 2
	}
	if label == "" {
		label = filepath.Base(argv[0])
	}
	q, err := testslot.Default()
	if err != nil {
		fmt.Fprintln(os.Stderr, "queue run:", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	onWait, onAdmit := queueNotices(os.Stderr)
	release, err := q.Acquire(ctx, testslot.Request{Kind: kind, Label: label, OnWait: onWait, OnAdmit: onAdmit})
	interrupted := ctx.Err() != nil // read before stop(), which cancels ctx
	stop()
	if err != nil {
		if interrupted {
			return 130
		}
		fmt.Fprintln(os.Stderr, "queue run:", err)
		return 1
	}
	defer release()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	// The Node verb's loop guard is for its own handover only: drop it so a
	// nested `code-forge queue` inside the command still reaches this binary.
	cmd.Env = []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CODE_FORGE_QUEUE_HANDOVER=") && !strings.HasPrefix(kv, testslot.EnvHeld+"=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, testslot.EnvHeld+"=1")
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "queue run:", err)
		return 127
	}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-signals:
				_ = cmd.Process.Signal(s)
			case <-done:
				return
			}
		}
	}()
	err = cmd.Wait()
	close(done)
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal())
		}
		return exit.ExitCode()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "queue run:", err)
		return 1
	}
	return 0
}

// gatesQueueSlot takes the machine test slot around one gate run: full when
// the test scope is full, changed for changed, related or text-only scope.
// Gates without a test command take no slot. The gate's children run with
// CODE_FORGE_TEST_QUEUE_HELD=1 so nested runs never wait on this slot.
func gatesQueueSlot(ctx context.Context, p gates.Plan, s gates.Scope, cwd, run, block string, stderr io.Writer) (func(), error) {
	if p.Commands["test"] == nil || (p.Restricted && !gates.Has(p.Names, "test")) || testslot.Disabled() {
		return func() {}, nil
	}
	kind := testslot.Changed
	if s.Kind == "full" && p.Light == nil && !p.ReuseTest && s.Reason != "text_only" {
		kind = testslot.Full
	}
	q, err := testslot.Default()
	if err != nil {
		return nil, fmt.Errorf("test queue: %w", err)
	}
	label := "gates"
	if run != "" {
		label += " " + run + "/" + block
	}
	onWait, onAdmit := queueNotices(stderr)
	release, err := q.Acquire(ctx, testslot.Request{Kind: kind, Label: label, Cwd: cwd, Run: run, Block: block, OnWait: onWait, OnAdmit: onAdmit})
	if err != nil {
		return nil, fmt.Errorf("test queue: %w", err)
	}
	before, had := os.LookupEnv(testslot.EnvHeld)
	_ = os.Setenv(testslot.EnvHeld, "1")
	return func() {
		if had {
			_ = os.Setenv(testslot.EnvHeld, before)
		} else {
			_ = os.Unsetenv(testslot.EnvHeld)
		}
		release()
	}, nil
}

// statusQueueLines names this run's gates that wait in the test queue, e.g.
// "queued #1 (gates B1)". It reads nothing when the queue was never used.
func statusQueueLines(home, run string) []string {
	q := testslot.Open(home)
	if _, err := os.Stat(filepath.Join(q.Dir, "tickets")); err != nil {
		return nil
	}
	_, waiters, err := q.Snapshot()
	if err != nil {
		return nil
	}
	lines := []string{}
	for i, w := range waiters {
		if w.Run == run {
			lines = append(lines, fmt.Sprintf("queued #%d (gates %s)", i+1, w.Block))
		}
	}
	return lines
}
