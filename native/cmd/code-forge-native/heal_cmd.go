package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/ricardov03/code-forge/native/healq"
)

// B429: the heal queue verb. `gates run`, `block rebase` and `block land`
// queue a retryable failure (healWrap); `heal run` retries it.
func init() { routes["heal"] = runHeal }

const healUsage = "usage: code-forge heal list [--run R] [--json] | show <id> [--run R] [--json] | run --run R [--once|--watch] [--max-tries N] [--json]"

// healPoll is how often `heal run --watch` looks for due items.
const healPoll = 15 * time.Second

func healHome() string { return os.Getenv("HOME") }

func runHeal(args []string) int {
	usage := func(message string) int {
		if message != "" {
			fmt.Fprintln(os.Stderr, "heal: "+message)
		}
		fmt.Fprintln(os.Stderr, healUsage)
		return 2
	}
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			fmt.Println(healUsage)
			return 0
		}
	}
	if len(args) == 0 {
		return usage("")
	}
	sub, rest := args[0], args[1:]
	opts := struct {
		run, max          string
		json, once, watch bool
		pos               []string
	}{}
	for i := 0; i < len(rest); i++ {
		switch arg := rest[i]; arg {
		case "--run", "--max-tries":
			if i+1 >= len(rest) || strings.HasPrefix(rest[i+1], "--") {
				return usage(arg + " needs a value")
			}
			if arg == "--run" {
				opts.run = rest[i+1]
			} else {
				opts.max = rest[i+1]
			}
			i++
		case "--json":
			opts.json = true
		case "--once":
			opts.once = true
		case "--watch":
			opts.watch = true
		default:
			if strings.HasPrefix(arg, "-") {
				return usage("unknown flag " + arg)
			}
			opts.pos = append(opts.pos, arg)
		}
	}
	if opts.run != "" && !healq.ValidRun(opts.run) {
		return usage("--run must be a run id")
	}
	home := healHome()
	if home == "" {
		fmt.Fprintln(os.Stderr, "heal: HOME is not set")
		return 1
	}
	switch sub {
	case "list":
		if len(opts.pos) != 0 || opts.once || opts.watch || opts.max != "" {
			return usage("list takes only --run and --json")
		}
		items, err := healItems(home, opts.run)
		if err != nil {
			fmt.Fprintln(os.Stderr, "heal:", err)
			return 1
		}
		if opts.json {
			_ = json.NewEncoder(os.Stdout).Encode(items)
			return 0
		}
		if len(items) == 0 {
			fmt.Println("heal: no items")
			return 0
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tSTATUS\tKIND\tRUN\tBLOCK\tTRIES\tARGV")
		for _, it := range items {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n", it.ID, it.Status, it.Kind, it.Run, it.Block, it.Tries, strings.Join(it.Argv, " "))
		}
		_ = w.Flush()
		return 0
	case "show":
		if len(opts.pos) != 1 || opts.once || opts.watch || opts.max != "" {
			return usage("show takes one item id, --run and --json")
		}
		items, err := healItems(home, opts.run)
		if err != nil {
			fmt.Fprintln(os.Stderr, "heal:", err)
			return 1
		}
		for _, it := range items {
			if it.ID != opts.pos[0] {
				continue
			}
			if opts.json {
				_ = json.NewEncoder(os.Stdout).Encode(it)
				return 0
			}
			fmt.Printf("id: %s\nrun: %s\nblock: %s\nkind: %s\nstatus: %s\ntries: %d\nargv: %s\ncwd: %s\ncreated: %s\nupdated: %s\n", it.ID, it.Run, it.Block, it.Kind, it.Status, it.Tries, strings.Join(it.Argv, " "), it.Cwd, it.CreatedAt, it.UpdatedAt)
			if it.NextAt != "" {
				fmt.Printf("next try: %s\n", it.NextAt)
			}
			fmt.Printf("detail:\n%s\n", it.Detail)
			return 0
		}
		fmt.Fprintf(os.Stderr, "heal: no item %s\n", opts.pos[0])
		return 1
	case "run":
		if len(opts.pos) != 0 || opts.run == "" {
			return usage("run needs --run R")
		}
		if opts.once && opts.watch {
			return usage("--once and --watch cannot go together")
		}
		max := healq.DefaultMaxTries
		if opts.max != "" {
			n, err := strconv.Atoi(opts.max)
			if err != nil || n < 1 || n > 100 {
				return usage("--max-tries must be an integer 1..100")
			}
			max = n
		}
		return healRun(home, opts.run, max, opts.once, opts.watch, opts.json)
	}
	return usage("unknown subcommand " + sub)
}

func healItems(home, run string) ([]healq.Item, error) {
	runs := []string{run}
	if run == "" {
		var err error
		if runs, err = healq.Runs(home); err != nil {
			return nil, err
		}
	}
	all := []healq.Item{}
	for _, r := range runs {
		items, err := healq.Load(home, r)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
	}
	return all, nil
}

func healRun(home, run string, max int, once, watch, asJSON bool) int {
	release, err := healq.Acquire(run)
	if err != nil {
		var busy *healq.ErrBusy
		if errors.As(err, &busy) {
			fmt.Fprintln(os.Stderr, "heal run:", busy.Error())
			return 1
		}
		fmt.Fprintln(os.Stderr, "heal run:", err)
		return 1
	}
	defer release()
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "heal run: locate the code-forge binary:", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	r := &healq.Runner{Home: home, Run: run, Exe: exe, MaxTries: max, Log: os.Stderr}
	switch {
	case watch:
		err = r.Watch(ctx, healPoll)
	case once:
		err = r.Pass(ctx)
	default:
		err = r.Drain(ctx)
	}
	if err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "heal run:", err)
		return 1
	}
	if asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"run": run, "healed": r.Healed, "needs_coder": r.NeedsCoder})
	} else {
		fmt.Printf("heal run %s: %d healed, %d need a coder\n", run, r.Healed, r.NeedsCoder)
	}
	if r.NeedsCoder > 0 {
		return 1
	}
	return 0
}

// healWrap queues a retryable failure of a verb that names a run (B429). The
// verb runs unchanged; its stderr is copied through and kept, and on a
// non-zero exit whose text is retryable (healq.Classify) one item with the
// verb's own argv is added and `heal: queued <id>` is printed. The exit code
// stays the verb's. A retry child (healq.RetryEnv) never queues again.
func healWrap(verb []string, fn func([]string) int) func([]string) int {
	return func(args []string) int {
		argv := append(append([]string{}, verb...), args...)
		run, block := healTarget(argv)
		if run == "" || os.Getenv(healq.RetryEnv) != "" || os.Getenv("HOME") == "" {
			return fn(args)
		}
		stop := healTee()
		code := fn(args)
		text := stop()
		if code == 0 {
			return code
		}
		kind, ok := healq.Classify(argv, text)
		if !ok {
			return code
		}
		cwd, _ := os.Getwd()
		item, err := healq.Enqueue(os.Getenv("HOME"), run, block, kind, argv, cwd, text, time.Now())
		if err != nil {
			fmt.Fprintf(os.Stderr, "heal: not queued: %s\n", err)
			return code
		}
		fmt.Fprintf(os.Stderr, "heal: queued %s\n", item.ID)
		return code
	}
}

// healTarget reads the run and block of a hooked argv, or "" when the call is
// help or names no valid run.
func healTarget(argv []string) (run, block string) {
	for _, a := range argv {
		if a == "--help" || a == "-h" {
			return "", ""
		}
	}
	switch {
	case len(argv) >= 2 && argv[0] == "gates" && argv[1] == "run":
		run, _, _ = gatesFlag(argv[2:], "--run")
		block, _, _ = gatesFlag(argv[2:], "--block")
	case len(argv) >= 2 && argv[0] == "block" && argv[1] == "land":
		flags, pos, message := landFlags(argv[2:])
		if message != "" {
			return "", ""
		}
		run = blockString(flags["run"])
		block = strings.Join(pos, ",")
	case len(argv) >= 3 && argv[0] == "block" && argv[1] == "rebase":
		run, _, _ = gatesFlag(argv[3:], "--run")
		block = argv[2]
	}
	if !healq.ValidRun(run) {
		return "", ""
	}
	return run, block
}

// healTee points os.Stderr at a pipe that copies to the real stderr and keeps
// the text; stop restores os.Stderr and returns the kept text. A child that
// still holds the pipe after the verb returned is not waited for long.
func healTee() (stop func() string) {
	real := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		return func() string { return "" }
	}
	os.Stderr = w
	kept := &healKept{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(io.MultiWriter(real, kept), r)
	}()
	return func() string {
		os.Stderr = real
		_ = w.Close()
		select {
		case <-done:
			_ = r.Close()
		case <-time.After(2 * time.Second):
		}
		return kept.String()
	}
}

// healKept keeps the last 64 KiB of the copied stderr.
type healKept struct {
	mu  sync.Mutex
	buf []byte
}

func (k *healKept) Write(p []byte) (int, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.buf = append(k.buf, p...)
	if over := len(k.buf) - 64*1024; over > 0 {
		k.buf = append([]byte{}, k.buf[over:]...)
	}
	return len(p), nil
}

func (k *healKept) String() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return string(k.buf)
}
