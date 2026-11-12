package main

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/filelock"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/procguard"
	"github.com/ricardov03/code-forge/native/runstatus"
	"github.com/ricardov03/code-forge/native/state"
)

// B454: `code-forge run` in Go (src/cli/run.mjs). status, end and
// start --reattach (with or without --worker-pid) run here; a plain run start,
// start --add-worker and reload keep their hand-over to Node (see the report
// and docs/native-engine.md). B473: reload with a config that loads runs in
// Go too (run_reload.go); odd cases keep named hand-overs.
func init() { routes["run"] = runRun }

const runUsage = "usage: code-forge run start [--cwd <dir>] [--run <id>] [--engine <e>] [--worker-pid <pid>] [--mode plan|brief] | start --add-worker --run <id> | start --reattach --run <id> [--worker-pid <pid>] | status --run <id> | reload --run <id> | end --run <id>\n"

var runSubUsage = map[string]string{
	"start":  "usage: code-forge run start [--cwd <dir>] [--run <id>] [--engine <e>] [--worker-pid <pid>] [--mode plan|brief]\nusage: code-forge run start --add-worker --run <id>\nusage: code-forge run start --reattach --run <id> [--worker-pid <pid>]\n",
	"status": "usage: code-forge run status --run <id>\n",
	"reload": "usage: code-forge run reload --run <id>\n",
	"end":    "usage: code-forge run end --run <id>\n",
}

// runFail is a refusal printed as `run <sub>: <message>`; usage exits 2.
type runFail struct {
	msg   string
	usage bool
}

func (e *runFail) Error() string { return e.msg }

// errRunHandover ends a read-only step that Node must decide (before any write).
var errRunHandover = errors.New("run: hand over to node")

// Seams for tests: the start-time probe (ps -o lstart=) and the SIGTERM.
var (
	runProbe = func(pid float64) *string {
		if pid > math.MaxInt32 {
			return nil // ps refuses a pid this large; Node's probe reads null
		}
		return filelock.ProcessStartTime(int(pid))
	}
	runKill = func(pid int) error { return procguard.Kill(pid, syscall.SIGTERM) }
)

type runFlags struct {
	values   map[string]string
	reattach bool
	add      bool
}

// runParseFlags is parseFlags (src/state/cli-args.mjs) for the run spec.
func runParseFlags(args []string) (runFlags, error) {
	f := runFlags{values: map[string]string{}}
	seen := map[string]bool{}
	valueFlags := map[string]bool{"run": true, "cwd": true, "engine": true, "worker-pid": true, "mode": true}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			continue // positionals are ignored by run
		}
		name, inline, hasInline := strings.Cut(arg[2:], "=")
		boolean := name == "reattach" || name == "add-worker"
		if !boolean && !valueFlags[name] {
			quoted, _ := ledger.Stringify(arg, false)
			return f, &runFail{"unknown flag " + string(quoted), true}
		}
		if seen[name] {
			return f, &runFail{"--" + name + " given more than once", true}
		}
		seen[name] = true
		switch {
		case boolean:
			if hasInline {
				return f, &runFail{"--" + name + " takes no value", true}
			}
			if name == "reattach" {
				f.reattach = true
			} else {
				f.add = true
			}
		case hasInline:
			if inline == "" {
				return f, &runFail{"--" + name + " needs a value", true}
			}
			f.values[name] = inline
		default:
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return f, &runFail{"--" + name + " needs a value", true}
			}
			f.values[name] = args[i+1]
			i++
		}
	}
	return f, nil
}

var runPositiveInt = regexp.MustCompile(`^[1-9][0-9]*$`)

// runIntFlag is intFlag: a positive safe integer with no sign or leading zero.
func runIntFlag(raw, name string) (float64, error) {
	if runPositiveInt.MatchString(raw) {
		if n, err := strconv.ParseFloat(raw, 64); err == nil && n <= 1<<53-1 {
			return n, nil
		}
	}
	return 0, &runFail{"--" + name + " must be a positive integer", true}
}

// runOut tracks the last non-blank stderr text, which Node logs as the message.
// runOut writes the verb's output; redact, when set, masks each write
// (Node's writeSafe).
type runOut struct {
	last   string
	redact func(string) string
}

func (o *runOut) out(s string) {
	if o.redact != nil {
		s = o.redact(s)
	}
	fmt.Fprint(os.Stdout, s)
}
func (o *runOut) err(s string) {
	if o.redact != nil {
		s = o.redact(s)
	}
	fmt.Fprint(os.Stderr, s)
	if strings.TrimSpace(s) != "" {
		o.last = s
	}
}

func runHandover(args []string, reason handoverReason) int {
	code, err := handover(append([]string{"run"}, args...), reason)
	if err != nil {
		fmt.Fprintln(os.Stderr, "code-forge-native:", err)
	}
	return code
}

func runRun(args []string) int {
	help := func(s string) bool { return s == "--help" || s == "-h" }
	// The Node router answers help before breadcrumbs and handlers.
	if len(args) > 0 && help(args[0]) {
		fmt.Fprint(os.Stdout, runUsage)
		return 0
	}
	if len(args) > 1 && help(args[1]) {
		if usage, ok := runSubUsage[args[0]]; ok {
			fmt.Fprint(os.Stdout, usage)
			return 0
		}
	}
	home := os.Getenv("HOME")
	if home == "" || !filepath.IsAbs(home) {
		return runHandover(args, hRunHome)
	}
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	known := sub == "start" || sub == "status" || sub == "reload" || sub == "end"
	var flags runFlags
	var parseErr error
	var workerPid float64
	var plan func(o *runOut) int
	if known {
		flags, parseErr = runParseFlags(args[1:])
		if parseErr == nil {
			parseErr = runCheckFlags(sub, flags, &workerPid)
		}
		if parseErr == nil {
			runID, hasRun := flags.values["run"]
			switch {
			case flags.add:
				var reason handoverReason
				plan, reason = runAddWorkerPlan(home, runID)
				if plan == nil {
					return runHandover(args, reason)
				}
			case sub == "start" && flags.reattach:
				var reason handoverReason
				plan, reason = runReattachPlan(home, runID, workerPid)
				if plan == nil {
					return runHandover(args, reason)
				}
			case sub == "start":
				var reason handoverReason
				plan, reason = runStartPlan(home, flags, workerPid)
				if plan == nil {
					return runHandover(args, reason)
				}
			case sub == "status" && hasRun:
				var reason handoverReason
				plan, reason = runStatusPlan(home, runID)
				if plan == nil {
					return runHandover(args, reason)
				}
			case sub == "reload" && hasRun:
				var reason handoverReason
				plan, reason = runReloadPlan(home, runID)
				if plan == nil {
					return runHandover(args, reason)
				}
			case sub == "end" && hasRun:
				var reason handoverReason
				plan, reason = runEndPlan(home, runID)
				if plan == nil {
					return runHandover(args, reason)
				}
			}
		}
	}
	crumb, logSub := "run", ""
	if sub == "start" || sub == "status" || sub == "end" {
		crumb, logSub = "run "+sub, sub
	}
	before := gatesBreadcrumbName(crumb)
	o := &runOut{}
	code := 0
	switch {
	case !known:
		o.err(runUsage)
		code = 2
	case parseErr != nil:
		code = runReport(o, sub, parseErr)
	case plan != nil:
		code = plan(o)
	default:
		o.err(runUsage)
		code = 2
	}
	if code != 0 {
		var message any
		if o.last != "" {
			message = strings.TrimSpace(o.last)
		}
		nativeVerbFailure("run", logSub, args, code, message, before)
	}
	return code
}

// runCheckFlags holds run.mjs's checks after parseFlags, in its order.
func runCheckFlags(sub string, f runFlags, workerPid *float64) error {
	if raw, ok := f.values["worker-pid"]; ok {
		n, err := runIntFlag(raw, "worker-pid")
		if err != nil {
			return err
		}
		*workerPid = n
	}
	if mode, ok := f.values["mode"]; ok {
		if sub != "start" || f.reattach || f.add {
			return &runFail{"--mode is set once, by a new run start (the mode is fixed for the run)", true}
		}
		if mode != "plan" && mode != "brief" {
			return &runFail{"--mode must be one of plan, brief", true}
		}
	}
	if f.add {
		if _, hasRun := f.values["run"]; sub != "start" || !hasRun || f.reattach || *workerPid != 0 {
			return &runFail{"run start --add-worker needs --run and cannot use --reattach or --worker-pid", true}
		}
	}
	if sub == "start" && f.reattach {
		if _, hasRun := f.values["run"]; !hasRun {
			return &runFail{"run start --reattach needs --run", true}
		}
	}
	return nil
}

// runReport prints a caught error as run.mjs does and returns its exit code.
func runReport(o *runOut, sub string, err error) int {
	var fail *runFail
	var stateErr *state.Error
	switch {
	case errors.As(err, &fail):
		o.err(fmt.Sprintf("run %s: %s\n", sub, fail.msg))
		if fail.usage {
			return 2
		}
		return 1
	case errors.As(err, &stateErr):
		o.err(fmt.Sprintf("run %s: %s\n", sub, stateErr.Message))
		return 1
	default:
		o.err(fmt.Sprintf("run %s: %s\n", sub, blockIOMessage(err)))
		return 1
	}
}

// runReadRecord is readRun before any write. A record Go cannot decode the way
// Node does (not JSON, not an object) goes to Node; no-run and bad-run-id are
// Node's own refusals.
func runReadRecord(home, runID string) (ledger.Object, error, bool) {
	record, err := state.ReadRun(home, runID)
	if err == nil {
		return record, nil, true
	}
	var stateErr *state.Error
	if errors.As(err, &stateErr) {
		return nil, err, true
	}
	return nil, nil, false
}

func runHas(o ledger.Object, key string) bool {
	for _, field := range o {
		if field.Key == key {
			return true
		}
	}
	return false
}

// runTruthy is JavaScript's Boolean(value) for decoded JSON values.
func runTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0 && !math.IsNaN(x)
	case string:
		return x != ""
	}
	return true
}

func runNumber(v any) string {
	data, _ := ledger.Stringify(v, false)
	return string(data)
}

// runSignalable is isSignalablePid: an integer > 1.
func runSignalable(v any) (float64, bool) {
	n, ok := v.(float64)
	return n, ok && n > 1 && !math.IsInf(n, 0) && math.Trunc(n) == n
}

// runNormalize is normalizeStartTime (trim, collapse whitespace, "" is null).
func runNormalize(s string) *string {
	out := strings.Join(strings.Fields(s), " ")
	if out == "" {
		return nil
	}
	return &out
}

// --- run status --run <id> ---

func runStatusPlan(home, runID string) (func(o *runOut) int, handoverReason) {
	record, readErr, ok := runReadRecord(home, runID)
	if !ok {
		return nil, hRunRecord
	}
	if readErr != nil {
		return func(o *runOut) int { return runReport(o, "status", readErr) }, 0
	}
	blocks, isObject := record.Get("blocks").(ledger.Object)
	if !isObject {
		return nil, hRunShape // Object.keys throws or walks a non-record value
	}
	awaiting := []string{}
	for _, field := range blocks {
		entry, isEntry := field.Value.(ledger.Object)
		if !isEntry {
			return nil, hRunShape
		}
		if entry.Get("status") == "awaiting_human_check" {
			awaiting = append(awaiting, field.Key)
		}
	}
	slug, isSlug := record.Get("project").(string)
	if !isSlug {
		return nil, hRunShape // readAllRows(undefined) is Node's to decide
	}
	rows, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge", "ledger"), slug)
	if err != nil {
		return nil, hRunLedger
	}
	spent, unknown, ok := runstatus.RunSpend(rows, runID)
	if !ok {
		return nil, hRunLedger
	}
	var budget any
	if workspace, isString := record.Get("workspace").(string); isString {
		if cfg, err := config.Load(workspace); err == nil {
			if b, isNumber := cfg["budget"].(map[string]any)["usd"].(float64); isNumber && b > 0 && !math.IsInf(b, 0) {
				budget = b
			}
		}
	}
	out := ledger.Object{}
	for _, key := range []string{"run_id", "status", "engine"} {
		if runHas(record, key) {
			out.Set(key, record.Get(key))
		}
	}
	out.Set("worker_pinned", runTruthy(record.Get("worker")))
	out.Set("blocks", blocks)
	if runHas(record, "orphans") {
		out.Set("orphans", record.Get("orphans"))
	}
	out.Set("spent_usd", spent)
	out.Set("budget_usd", budget)
	out.Set("unknown_usd_sessions", unknown)
	if len(awaiting) > 0 {
		sort.Strings(awaiting)
		list := make([]any, len(awaiting))
		for i, id := range awaiting {
			list[i] = id
		}
		out.Set("awaiting_human_check", list)
	}
	data, err := ledger.Stringify(out, true)
	if err != nil {
		return nil, hRunShape
	}
	return func(o *runOut) int {
		o.out(string(data) + "\n")
		if unknown > 0 {
			if unknown == 1 {
				o.err("1 session has no known price; the budget does not count it\n")
			} else {
				o.err(fmt.Sprintf("%d sessions have no known price; the budget does not count them\n", unknown))
			}
		}
		return 0
	}, 0
}

// --- run end --run <id> ---

type runPinCheck struct {
	ok     bool
	event  string
	reason string
}

// runCheckPin is checkWorkerPin with no live pid. ok is false for a shape
// Node would throw on (the caller hands over before any write).
func runCheckPin(workers any, hasWorkers bool, worker any) (runPinCheck, bool) {
	if list, isList := workers.([]any); hasWorkers && isList && len(list) > 1 {
		for _, p := range list {
			if p == nil {
				return runPinCheck{}, false // null.pid throws in Node
			}
			pin, _ := p.(ledger.Object) // a non-object pin has no pid
			if _, ok := runSignalable(pin.Get("pid")); !ok {
				return runPinCheck{false, "worker.replaced", "the live worker does not match a pinned worker"}, true
			}
		}
		live := false
		for _, p := range list {
			pin := p.(ledger.Object)
			pid, _ := runSignalable(pin.Get("pid"))
			started, isString := pin.Get("started_at").(string)
			if !isString {
				return runPinCheck{}, false
			}
			now := runProbe(pid)
			want := runNormalize(started)
			if now != nil && want != nil && *now == *want {
				live = true
			}
		}
		if live {
			return runPinCheck{ok: true}, true
		}
		return runPinCheck{false, "worker.down", "no pinned worker is running"}, true
	}
	if !runTruthy(worker) {
		return runPinCheck{false, "worker.down", "no worker is pinned for this run"}, true
	}
	pin, _ := worker.(ledger.Object) // a truthy non-object pin has no pid
	pid, ok := runSignalable(pin.Get("pid"))
	if !ok {
		return runPinCheck{false, "worker.replaced", "the pinned worker pid is not an integer > 1"}, true
	}
	now := runProbe(pid)
	if now == nil {
		return runPinCheck{false, "worker.down", fmt.Sprintf("pinned worker pid %s is not running", runNumber(pid))}, true
	}
	started, isString := pin.Get("started_at").(string)
	if !isString {
		return runPinCheck{}, false
	}
	if want := runNormalize(started); want == nil || *now != *want {
		return runPinCheck{false, "worker.replaced", fmt.Sprintf("pid %s was restarted (start time differs from the pin)", runNumber(pid))}, true
	}
	return runPinCheck{ok: true}, true
}

// runStopPinned is stopPinnedWorker after a passing pin check: re-probe, then
// SIGTERM; a worker gone in between (ESRCH) is not stopped, never an error.
func runStopPinned(pin ledger.Object) (bool, error) {
	pid, ok := runSignalable(pin.Get("pid"))
	if !ok {
		return false, nil
	}
	now := runProbe(pid)
	started, _ := pin.Get("started_at").(string)
	if want := runNormalize(started); now == nil || want == nil || *now != *want {
		return false, nil
	}
	if err := runKill(int(pid)); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return false, nil
		}
		if errno, isErrno := err.(syscall.Errno); isErrno {
			return false, &runFail{"kill " + blockErrno(errno), false}
		}
		return false, err
	}
	return true, nil
}

func runEndPlan(home, runID string) (func(o *runOut) int, handoverReason) {
	if _, readErr, ok := runReadRecord(home, runID); !ok {
		return nil, hRunRecord
	} else if readErr != nil {
		return func(o *runOut) int { return runReport(o, "end", readErr) }, 0
	}
	return func(o *runOut) int {
		var ended ledger.Object
		err := state.WithRunLock(home, runID, func() error {
			current, err := state.ReadRun(home, runID)
			if err != nil {
				var stateErr *state.Error
				if !errors.As(err, &stateErr) {
					return errRunHandover
				}
				return err
			}
			if current.Get("status") != "active" {
				return &runFail{fmt.Sprintf("run %s has already ended", runID), false}
			}
			blocks, isObject := current.Get("blocks").(ledger.Object)
			if !isObject {
				return errRunHandover
			}
			awaiting, open := []string{}, []string{}
			for _, field := range blocks {
				entry, isEntry := field.Value.(ledger.Object)
				if !isEntry {
					return errRunHandover
				}
				switch entry.Get("status") {
				case "awaiting_human_check":
					awaiting = append(awaiting, field.Key)
				case "open":
					open = append(open, field.Key)
				}
			}
			if len(awaiting) > 0 {
				return &runFail{fmt.Sprintf("run %s has blocks awaiting human check: %s; human-pass or stop them first", runID, strings.Join(awaiting, ", ")), false}
			}
			if len(open) > 0 {
				return &runFail{fmt.Sprintf("run %s still has open blocks: %s", runID, strings.Join(open, ", ")), false}
			}
			workers := current.Get("workers")
			hasWorkers := workers != nil
			pin, ok := runCheckPin(workers, hasWorkers, current.Get("worker"))
			if !ok {
				return errRunHandover
			}
			// current.workers ?? (current.worker ? [current.worker] : [])
			pins := []any{}
			if hasWorkers {
				list, isList := workers.([]any)
				if !isList {
					return errRunHandover
				}
				pins = list
			} else if runTruthy(current.Get("worker")) {
				pins = []any{current.Get("worker")}
			}
			checks := make([]bool, len(pins))
			for i, worker := range pins {
				check, ok := runCheckPin(nil, false, worker)
				if !ok {
					return errRunHandover
				}
				checks[i] = check.ok
			}
			if !pin.ok {
				if _, err := state.WriteSigned(home, runID, ledger.Object{{Key: "event", Value: pin.event}, {Key: "reason", Value: pin.reason}}); err != nil {
					return err
				}
			}
			stopped := false
			for i, worker := range pins {
				if !checks[i] {
					continue
				}
				done, err := runStopPinned(worker.(ledger.Object))
				if err != nil {
					return err
				}
				stopped = done || stopped
			}
			current.Set("status", "ended")
			current.Set("ended_at", time.Now().UTC().Format("2006-01-02T15:04:05.000Z"))
			check := any("ok")
			if !pin.ok {
				check = pin.event
			}
			if _, err := state.WriteSigned(home, runID, ledger.Object{{Key: "event", Value: "run.end"}, {Key: "worker_check", Value: check}, {Key: "worker_stopped", Value: stopped}}); err != nil {
				return err
			}
			if err := state.SaveRun(home, current); err != nil {
				return err
			}
			ended = current
			return nil
		})
		if errors.Is(err, errRunHandover) {
			return runHandover([]string{"end", "--run", runID}, hRunShape)
		}
		if err != nil {
			return runReport(o, "end", err)
		}
		o.out(fmt.Sprintf("run %s ended\n", runString(ended.Get("run_id"))))
		return 0
	}, 0
}

// runString is `${value}` for the decoded values a run record holds.
func runString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return "null"
	case float64, bool:
		return runNumber(x)
	}
	return fmt.Sprint(v)
}

// --- run start --reattach --run <id> [--worker-pid <pid>] ---

func runReattachPlan(home, runID string, workerPid float64) (func(o *runOut) int, handoverReason) {
	record, readErr, ok := runReadRecord(home, runID)
	if !ok {
		return nil, hRunRecord
	}
	if readErr != nil {
		return func(o *runOut) int { return runReport(o, "start", readErr) }, 0
	}
	if workerPid == 0 {
		// repinWorker: the live worker the queue announces, for this run only.
		workspace, isString := record.Get("workspace").(string)
		if !isString {
			return nil, hRunShape
		}
		root, err := gitx.RepoTop(workspace)
		if err != nil {
			fail := &runFail{"not inside a git repository", false}
			return func(o *runOut) int { return runReport(o, "start", fail) }, 0
		}
		live, decided := reviewEnqueueWorker(root)
		if !decided {
			return nil, hRunRegistry
		}
		if live == nil {
			fail := &runFail{"no live worker serves this queue", false}
			return func(o *runOut) int { return runReport(o, "start", fail) }, 0
		}
		if serves, _ := live.Get("run").(string); serves != runID {
			fail := &runFail{"the live worker serves run " + serves, false}
			return func(o *runOut) int { return runReport(o, "start", fail) }, 0
		}
		workerPid, _ = live.Get("pid").(float64)
	}
	pid := workerPid
	return func(o *runOut) int {
		if _, ok := runSignalable(pid); !ok {
			return runReport(o, "start", &runFail{"worker pid must be an integer > 1", false})
		}
		err := state.WithRunLock(home, runID, func() error {
			current, err := state.ReadRun(home, runID)
			if err != nil {
				var stateErr *state.Error
				if !errors.As(err, &stateErr) {
					return errRunHandover
				}
				return err
			}
			if current.Get("status") != "active" {
				return &runFail{fmt.Sprintf("run %s has ended", runID), false}
			}
			started := runProbe(pid)
			if started == nil {
				return &runFail{fmt.Sprintf("worker pid %s is not running", runNumber(pid)), false}
			}
			pin := ledger.Object{{Key: "pid", Value: pid}, {Key: "started_at", Value: *started}}
			// pins = current.workers ?? (current.worker ? [current.worker] : [])
			pins := []any{}
			if workers := current.Get("workers"); workers != nil {
				list, isList := workers.([]any)
				if !isList {
					return errRunHandover
				}
				pins = list
			} else if runTruthy(current.Get("worker")) {
				pins = []any{current.Get("worker")}
			}
			history := []any{}
			if old := current.Get("worker_history"); old != nil {
				list, isList := old.([]any)
				if !isList {
					return errRunHandover
				}
				history = append(history, list...)
			}
			current.Set("worker_history", append(history, pins...))
			current.Set("workers", []any{pin})
			current.Set("worker", pin)
			if _, err := state.WriteSigned(home, runID, ledger.Object{{Key: "event", Value: "worker.reattach"}, {Key: "pid", Value: pid}}); err != nil {
				return err
			}
			return state.SaveRun(home, current)
		})
		if errors.Is(err, errRunHandover) {
			return runHandover([]string{"start", "--reattach", "--run", runID, "--worker-pid", runNumber(pid)}, hRunShape)
		}
		if err != nil {
			return runReport(o, "start", err)
		}
		o.out(fmt.Sprintf("run %s: worker re-pinned to pid %s\n", runID, runNumber(pid)))
		return 0
	}, 0
}
