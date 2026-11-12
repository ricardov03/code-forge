package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/filelock"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B468: a plain `run start` (record, key, signed rows, worker launch and pin),
// `run start --add-worker` and the refusals of `run reload` in Go
// (src/cli/run.mjs, src/state/run.mjs startRun, src/worker/loop.mjs
// launchWorker/repinWorker). B511/B512: the worker they launch is this
// binary's `worker --run <id>` (the Go worker, B489), so neither needs node.

// Seams for tests: how long a launched worker has to announce itself, and the clock.
var (
	runLaunchTimeout = 15 * time.Second
	runNow           = time.Now
)

var (
	runIDPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	runSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	runSlugStrip   = regexp.MustCompile(`[^a-z0-9-]+`)
)

// runRealCwd is process.cwd(): the kernel's path, never $PWD.
func runRealCwd() (string, error) { return syscall.Getwd() }

// runResolve is path.resolve(cwd, p).
func runResolve(cwd, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(cwd, p)
}

// runApplyTmpRoot is bin/code-forge.mjs applyTmpRoot: the process cwd's
// tmp.root names the temp base for this process and the worker it launches.
func runApplyTmpRoot() {
	if os.Getenv("CODE_FORGE_TMP_ROOT") != "" {
		return
	}
	cwd, err := runRealCwd()
	if err != nil {
		return
	}
	snap, err := config.LoadSnapshot(cwd)
	if err != nil {
		return
	}
	tmp, _ := snap.Ordered.Get("tmp").(ledger.Object)
	if root, ok := tmp.Get("root").(string); ok && root != "" {
		_ = os.Setenv("CODE_FORGE_TMP_ROOT", runResolve(cwd, root))
	}
}

// runTmpBase is tmp.mjs tmpBase(root).
func runTmpBase(root any, set bool) (string, error) {
	if !set {
		if env := os.Getenv("CODE_FORGE_TMP_ROOT"); env != "" && filepath.IsAbs(env) {
			return env, nil
		}
		return filepath.Join(os.TempDir(), "code-forge"), nil
	}
	s, ok := root.(string)
	if !ok || !filepath.IsAbs(s) {
		return "", &runFail{"tmp: tmp.root must be an absolute path", false}
	}
	return s, nil
}

// runJSString is String(value) for decoded JSON values.
func runJSString(v any) string {
	switch x := v.(type) {
	case ledger.Object, map[string]any:
		return "[object Object]"
	case []any:
		parts := make([]string, len(x))
		for i, item := range x {
			if item != nil {
				parts[i] = runJSString(item)
			}
		}
		return strings.Join(parts, ",")
	}
	return runString(v)
}

// runJSTrim is String.prototype.trim.
func runJSTrim(s string) string { return strings.TrimFunc(s, runJSSpace) }

// runSlug is slugFor(cfg, workspace).
func runSlug(cfg ledger.Object, workspace string) string {
	project, _ := cfg.Get("project").(ledger.Object)
	if slug, ok := project.Get("slug").(string); ok && slug != "" {
		return slug
	}
	derived := runSlugStrip.ReplaceAllString(strings.ToLower(filepath.Base(config.ProjectRootFor(workspace))), "-")
	derived = strings.Trim(derived, "-")
	if derived == "" {
		return "project"
	}
	return derived
}

// runDeepMap turns an ordered value into the plain JSON form configHash hashes.
func runDeepMap(v any) any {
	switch x := v.(type) {
	case ledger.Object:
		m := make(map[string]any, len(x))
		for _, field := range x {
			m[field.Key] = runDeepMap(field.Value)
		}
		return m
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = runDeepMap(item)
		}
		return out
	}
	return v
}

// runWorkerBinary is the binary `run start` launches as the worker (B511):
// this one, symlinks resolved. Its `worker --run <id>` serves the queue in Go
// and, for a run Go does not serve (B489's split rule), becomes the Node
// worker in place (exec: same pid and start time), so the announce wait and
// the pin below see one process either way. A test points it at a stand-in.
var runWorkerBinary = func() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(executable)
}

// --- the worker registry (src/worker/queue.mjs) ---

type runRegEntry struct {
	file   string
	worker ledger.Object
	live   bool
}

func runHeartbeatFresh(w ledger.Object) bool {
	at, ok := reviewWaitDate(w.Get("heartbeat_at"))
	if !ok {
		return false
	}
	age := time.Since(at)
	return age <= 60*time.Second && age >= -5*time.Second
}

// runWorkerLiveAtBoot is workerLiveAtBoot (the orchestrator trusts ps).
func runWorkerLiveAtBoot(w ledger.Object) bool {
	pid, _ := w.Get("pid").(float64)
	if !runAlive(pid) {
		return false
	}
	start, _ := w.Get("start_time").(string)
	if live := runStartTime(pid); live != nil && start != "unknown" {
		return *live == start
	}
	return runHeartbeatFresh(w) || start == "unknown"
}

// runWorkerRegistry is workerRegistry(repoRoot, {prune}); false means a shape
// only Node can judge (the caller hands over).
func runWorkerRegistry(root string, prune bool) ([]runRegEntry, bool) {
	dir := filepath.Join(root, ".code-forge", "workers")
	names, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return nil, false
	}
	ctx, deadline := context.Background(), time.Now().Add(5*time.Second)
	out := []runRegEntry{}
	for _, name := range names {
		if !strings.HasSuffix(name.Name(), ".json") {
			continue
		}
		file := filepath.Join(dir, name.Name())
		w, matched := reviewWaitJSON(file)
		if !matched {
			return nil, false
		}
		for _, key := range []string{"started_at", "heartbeat_at"} {
			if _, isString := w.Get(key).(string); isString {
				if _, ok := reviewWaitDate(w.Get(key)); !ok {
					return nil, false
				}
			}
		}
		var worker ledger.Object
		if pid, start, shape := reviewWaitWorkerShape(w); shape && name.Name() == fmt.Sprintf("%d-%s.json", pid, start) {
			worker = w
		}
		live := true
		if worker != nil {
			if prune {
				live = runWorkerLiveAtBoot(worker)
			} else {
				var ok bool
				if live, ok = reviewWaitWorkerLive(ctx, worker, deadline); !ok {
					return nil, false
				}
			}
		}
		if !live && prune {
			_ = os.Remove(file)
			continue
		}
		out = append(out, runRegEntry{file, worker, live})
	}
	return out, true
}

// runLiveWorker is liveWorker(repoRoot, {pid, registeredOnly}); pid 0 is any.
func runLiveWorker(root string, pid float64, registeredOnly bool) (ledger.Object, bool) {
	entries, ok := runWorkerRegistry(root, false)
	if !ok {
		return nil, false
	}
	for _, e := range entries {
		if e.live && e.worker != nil && (pid == 0 || e.worker.Get("pid") == pid) {
			return e.worker, true
		}
	}
	if registeredOnly {
		return nil, true
	}
	w, matched := reviewWaitJSON(filepath.Join(root, ".code-forge", "queue", "worker.json"))
	if !matched {
		return nil, false
	}
	wpid, _ := w.Get("pid").(float64)
	if _, run := w.Get("run").(string); wpid <= 1 || wpid != math.Trunc(wpid) || !run || (pid != 0 && wpid != pid) {
		return nil, true
	}
	live, ok := reviewWaitWorkerLive(context.Background(), w, time.Now().Add(5*time.Second))
	if !ok {
		return nil, false
	}
	if !live {
		return nil, true
	}
	return w, true
}

// runLaunchWorker is launchWorker with this binary as the worker:
// `<binary> worker --run <id>` detached in the repository (the caller's env,
// no stdio), until the queue announces that pid.
func runLaunchWorker(runID, workspace, binary string) (int, string, error) {
	repoRoot, err := gitx.RepoTop(workspace)
	if err != nil {
		return 0, "", &runFail{"not inside a git repository", false}
	}
	cmd := exec.Command(binary, "worker", "--run", runID)
	cmd.Dir = repoRoot
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	pid := 0
	exited := make(chan struct{})
	if cmd.Start() == nil {
		pid = cmd.Process.Pid
		go func() { _ = cmd.Wait(); close(exited) }()
	}
	deadline := time.Now().Add(runLaunchTimeout)
	for time.Now().Before(deadline) {
		if pid != 0 {
			if w, _ := runLiveWorker(repoRoot, float64(pid), true); w != nil {
				return pid, repoRoot, nil
			}
			select {
			case <-exited:
				deadline = time.Now()
				continue
			default:
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if pid != 0 {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
	if other, _ := runLiveWorker(repoRoot, 0, false); other != nil {
		if run, _ := other.Get("run").(string); run != runID {
			return 0, "", &runFail{fmt.Sprintf("the worker did not start: run %s still has a live worker (pid %s); end it with: code-forge run end --run %s", run, runNumber(other.Get("pid")), run), false}
		}
	}
	return 0, "", &runFail{"the worker did not start (run `code-forge worker --run <id>` by hand to see why)", false}
}

// runPinLocked is reattachWorker under the run lock (add: worker.add, the pin
// joins the list and history is kept).
func runPinLocked(home, runID string, pid float64, add bool) (ledger.Object, error) {
	if _, ok := runSignalable(pid); !ok {
		return nil, &runFail{"worker pid must be an integer > 1", false}
	}
	var saved ledger.Object
	err := state.WithRunLock(home, runID, func() error {
		current, err := state.ReadRun(home, runID)
		if err != nil {
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
		event := "worker.reattach"
		if add {
			event = "worker.add"
			current.Set("workers", append(append([]any{}, pins...), pin))
		} else {
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
		}
		current.Set("worker", current.Get("workers").([]any)[0])
		if _, err := state.WriteSigned(home, runID, ledger.Object{{Key: "event", Value: event}, {Key: "pid", Value: pid}}); err != nil {
			return err
		}
		if err := state.SaveRun(home, current); err != nil {
			return err
		}
		saved = current
		return nil
	})
	return saved, err
}

// runRepin is repinWorker with expectPid: pin only the launched worker.
func runRepin(home, runID, repoRoot string, expect float64, add bool) (ledger.Object, error) {
	live, ok := runLiveWorker(repoRoot, expect, true)
	if ok && live == nil {
		live, ok = runLiveWorker(repoRoot, 0, false)
	}
	if !ok {
		return nil, errRunHandover
	}
	if live == nil {
		return nil, &runFail{"no live worker serves this queue", false}
	}
	if run, _ := live.Get("run").(string); run != runID {
		return nil, &runFail{"the live worker serves run " + runString(live.Get("run")), false}
	}
	if live.Get("pid") != expect {
		return nil, &runFail{fmt.Sprintf("the queue announces worker pid %s, not the launched pid %s", runNumber(live.Get("pid")), runNumber(expect)), false}
	}
	return runPinLocked(home, runID, expect, add)
}

// runStopLaunched is stopLaunched: stop a launched worker that was not pinned
// (start time re-checked first) and say so on stderr.
func runStopLaunched(o *runOut, pid int, startedAt *string) {
	reason := ""
	stopped := false
	if startedAt == nil {
		reason = "not running"
	} else if now := runProbe(float64(pid)); now == nil {
		reason = "not running"
	} else if want := runNormalize(*startedAt); want == nil || *now != *want {
		reason = "pid reused"
	} else if err := runKill(pid); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			reason = "already exited"
		} else if errno, isErrno := err.(syscall.Errno); isErrno {
			reason = "kill " + blockErrno(errno)
		} else {
			reason = err.Error()
		}
	} else {
		stopped = true
	}
	if stopped {
		o.err(fmt.Sprintf("run start: launched worker pid %d was not pinned; stopped it\n", pid))
	} else {
		o.err(fmt.Sprintf("run start: launched worker pid %d was not pinned; not stopped (%s)\n", pid, reason))
	}
}

// --- run start (plain) ---

func runStartPlan(home string, flags runFlags, workerPid float64) (func(o *runOut) int, handoverReason) {
	runApplyTmpRoot()
	cwd, err := runRealCwd()
	if err != nil {
		return nil, hRunStart
	}
	if dir, ok := flags.values["cwd"]; ok {
		cwd = runResolve(cwd, dir)
	}
	workspace := config.ProjectRootFor(cwd)
	snap, err := config.LoadSnapshot(workspace)
	if err != nil {
		var cfgErr *config.Error
		if !errors.As(err, &cfgErr) || runYAMLSyntax(cfgErr) {
			return nil, hRunStart
		}
		return func(o *runOut) int {
			o.err("run start: " + cfgErr.Message + "\n")
			return 2
		}, 0
	}
	cfg := snap.Ordered
	project := runSlug(cfg, workspace)
	tmp, _ := cfg.Get("tmp").(ledger.Object)
	tmpRoot, tmpSet := tmp.Get("root").(string)
	base, baseErr := runTmpBase(tmpRoot, tmpSet)
	if baseErr != nil {
		return func(o *runOut) int { return runReport(o, "start", baseErr) }, 0
	}
	var binary string
	if workerPid == 0 {
		if binary, err = runWorkerBinary(); err != nil {
			return nil, hRunStart
		}
	}
	if runSweepRootUnreadable(base) {
		return nil, hRunSweep
	}
	engine := any("auto")
	if flag, ok := flags.values["engine"]; ok {
		engine = flag
	} else if v := cfg.Get("engine"); v != nil {
		engine = v
	}
	return func(o *runOut) int {
		runStopItems(runSweepItems(home))
		runSweepRoots(base)
		now := runNow().UTC()
		runID, given := flags.values["run"]
		if !given {
			suffix := make([]byte, 3)
			_, _ = rand.Read(suffix)
			runID = "r-" + now.Format("20060102-150405") + "-" + hex.EncodeToString(suffix)
		}
		fail := func(msg string) int { return runReport(o, "start", &runFail{msg, false}) }
		if !runIDPattern.MatchString(runID) {
			return fail("run id must match /^[a-z0-9][a-z0-9-]{0,63}$/")
		}
		if !runSlugPattern.MatchString(project) {
			return fail("project slug must match /^[a-z0-9][a-z0-9-]*$/ (config key project.slug)")
		}
		ws, err := canonicalPath(workspace)
		if err != nil {
			return runReport(o, "start", err)
		}
		h, err := canonicalPath(home)
		if err != nil {
			return runReport(o, "start", err)
		}
		if rel, err := filepath.Rel(ws, filepath.Join(h, ".code-forge", "runs")); err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
			return fail(fmt.Sprintf("the workspace %s contains %s — run code-forge from a project directory, not $HOME or above", ws, filepath.Join(home, ".code-forge", "runs")))
		}
		review, _ := cfg.Get("review").(ledger.Object)
		external, _ := review.Get("external").(ledger.Object)
		reviewer := ""
		if external.Get("required") == true {
			name, isString := external.Get("name").(string)
			if !isString || runJSTrim(name) == "" {
				return fail("review.external.required is true but review.external.name is missing or blank")
			}
			reviewer = runJSTrim(name)
		}
		var worker any
		if workerPid != 0 {
			if _, ok := runSignalable(workerPid); !ok {
				return fail("worker pid must be an integer > 1")
			}
			started := runProbe(workerPid)
			if started == nil {
				return fail(fmt.Sprintf("worker pid %s is not running", runNumber(workerPid)))
			}
			worker = ledger.Object{{Key: "pid", Value: workerPid}, {Key: "started_at", Value: *started}}
		}
		workers := []any{}
		if worker != nil {
			workers = []any{worker}
		}
		fallback := any("rules")
		if system1, ok := cfg.Get("system1").(ledger.Object); ok && system1.Get("fallback") != nil {
			fallback = system1.Get("fallback")
		}
		provider, levels := cfg.Get("provider"), cfg.Get("levels")
		hash := config.Hash(runDeepMap(cfg))
		record := ledger.Object{
			{Key: "version", Value: float64(1)},
			{Key: "run_id", Value: runID},
			{Key: "project", Value: project},
			{Key: "workspace", Value: ws},
			{Key: "engine", Value: engine},
			{Key: "provider", Value: provider},
			{Key: "levels", Value: levels},
			{Key: "fallback_mode", Value: fallback},
			{Key: "started_at", Value: now.Format("2006-01-02T15:04:05.000Z")},
			{Key: "orchestrator_pid", Value: float64(os.Getpid())},
			{Key: "solo_pid", Value: nil},
			{Key: "worker", Value: worker},
			{Key: "workers", Value: workers},
			{Key: "status", Value: "active"},
			{Key: "blocks", Value: ledger.Object{}},
			{Key: "orphans", Value: []any{}},
		}
		brief := flags.values["mode"] == "brief"
		if brief {
			record.Set("mode", "brief")
		}
		record.Set("config", ledger.Object{
			{Key: "hash", Value: hash},
			{Key: "snapshots", Value: ledger.Object{{Key: hash, Value: cfg}}},
			{Key: "pins", Value: ledger.Object{}},
		})
		err = state.WithRunLock(home, runID, func() error {
			if _, err := state.ReadRun(home, runID); err == nil {
				return &runFail{fmt.Sprintf("run %s already exists", runID), false}
			} else {
				var stateErr *state.Error
				if !errors.As(err, &stateErr) {
					return errRunHandover
				}
				if stateErr.Code != "no-run" {
					return err
				}
			}
			if err := runGenerateKey(home, runID); err != nil {
				return err
			}
			if err := state.SaveRun(home, record); err != nil {
				return err
			}
			rows := []ledger.Object{{{Key: "event", Value: "run.start"}, {Key: "engine", Value: engine}, {Key: "worker_pinned", Value: worker != nil}}}
			if brief {
				rows = append(rows, ledger.Object{{Key: "event", Value: "run.mode"}, {Key: "mode", Value: "brief"}, {Key: "plan", Value: "none"}})
			}
			if reviewer != "" {
				rows = append(rows, ledger.Object{{Key: "event", Value: "review.external"}, {Key: "required", Value: true}, {Key: "reviewer", Value: reviewer}})
			}
			for _, row := range rows {
				if _, err := state.WriteSigned(home, runID, row); err != nil {
					return err
				}
			}
			return nil
		})
		if errors.Is(err, errRunHandover) {
			return runHandover(append([]string{"start"}, runStartArgs(flags, workerPid)...), hRunShape)
		}
		if err != nil {
			return runReport(o, "start", err)
		}
		pinned := worker
		if worker == nil {
			pid, repoRoot, err := runLaunchWorker(runID, workspace, binary)
			if err != nil {
				return runReport(o, "start", err)
			}
			startedAt := runProbe(float64(pid))
			saved, err := runRepin(home, runID, repoRoot, float64(pid), false)
			if err != nil {
				runStopLaunched(o, pid, startedAt)
				if errors.Is(err, errRunHandover) {
					err = &runFail{"the worker registry could not be read", false}
				}
				return runReport(o, "start", err)
			}
			pinned = saved.Get("worker")
		}
		line := fmt.Sprintf("run %s started · engine %s", runID, runJSString(engine))
		if brief {
			line += " · mode brief (no plan)"
		}
		if runTruthy(pinned) {
			pin, _ := pinned.(ledger.Object)
			line += " · worker pid " + runString(pin.Get("pid"))
		} else {
			line += " · worker not pinned"
		}
		o.out(line + "\n")
		if !runTruthy(pinned) {
			o.err("run start: no worker pinned — start the worker, then `run start --reattach --run <id> --worker-pid <pid>`\n")
		}
		return 0
	}, 0
}

func runStartArgs(flags runFlags, workerPid float64) []string {
	args := []string{}
	for _, name := range []string{"cwd", "run", "engine", "mode"} {
		if v, ok := flags.values[name]; ok {
			args = append(args, "--"+name, v)
		}
	}
	if workerPid != 0 {
		args = append(args, "--worker-pid", runNumber(workerPid))
	}
	return args
}

func canonicalPath(p string) (string, error) {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Abs(resolved)
	}
	return filepath.Abs(p)
}

// runGenerateKey is signer.mjs generateKey: 32 random bytes, 0600, never overwritten.
func runGenerateKey(home, runID string) error {
	dir := filepath.Join(home, ".code-forge", "runs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	file := filepath.Join(dir, runID+".key")
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return &runFail{fmt.Sprintf("run %s already has a key", runID), false}
		}
		return err
	}
	_, writeErr := f.Write(key)
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	return os.Chmod(file, 0o600)
}

// --- run start --add-worker --run <id> ---

// runConfigInForce is configInForce(record): nil for a record without a usable
// config block; an error when the snapshot in force does not match its hash.
func runConfigInForce(record ledger.Object) (ledger.Object, error) {
	cfgState, ok := record.Get("config").(ledger.Object)
	if !ok {
		return nil, nil
	}
	hash, ok := cfgState.Get("hash").(string)
	if !ok {
		return nil, nil
	}
	snaps, _ := cfgState.Get("snapshots").(ledger.Object)
	cfg, isMapping := snaps.Get(hash).(ledger.Object)
	if !isMapping || !runHas(snaps, hash) || config.Hash(runDeepMap(cfg)) != hash {
		return nil, &runFail{fmt.Sprintf("run %s: the config snapshot in force does not match its hash", runString(record.Get("run_id"))), false}
	}
	return cfg, nil
}

func runAddWorkerPlan(home, runID string) (func(o *runOut) int, handoverReason) {
	runApplyTmpRoot()
	binary, err := runWorkerBinary()
	if err != nil {
		return nil, hRunAddWorker
	}
	return func(o *runOut) int {
		var line string
		err := filelock.WithLock("worker-add:"+runID, func() error {
			record, err := state.ReadRun(home, runID)
			if err != nil {
				var stateErr *state.Error
				if !errors.As(err, &stateErr) {
					return errRunHandover
				}
				return err
			}
			if record.Get("status") != "active" {
				return &runFail{fmt.Sprintf("run %s has ended", runID), false}
			}
			workspace, isString := record.Get("workspace").(string)
			if !isString {
				return errRunHandover
			}
			repoRoot, err := gitx.RepoTop(workspace)
			if err != nil {
				return &runFail{"not inside a git repository", false}
			}
			var cfg ledger.Object
			if runTruthy(record.Get("config")) {
				if cfg, err = runConfigInForce(record); err != nil {
					return err
				}
			} else {
				snap, err := config.LoadSnapshot(workspace)
				if err != nil {
					var cfgErr *config.Error
					if !errors.As(err, &cfgErr) {
						return errRunHandover
					}
					return &runFail{"the project config cannot be loaded", false}
				}
				cfg = snap.Ordered
			}
			limit := float64(1)
			review, _ := cfg.Get("review").(ledger.Object)
			if v := review.Get("workers"); v != nil {
				n, isNumber := v.(float64)
				if !isNumber {
					return errRunHandover
				}
				limit = n
			}
			entries, ok := runWorkerRegistry(repoRoot, true)
			if !ok {
				return errRunHandover
			}
			legacy, ok := runLiveWorker(repoRoot, 0, false)
			if !ok {
				return errRunHandover
			}
			count := 0.0
			for _, e := range entries {
				if e.live {
					count++
				}
			}
			if legacy != nil {
				counted := false
				for _, e := range entries {
					if e.worker != nil && e.worker.Get("pid") == legacy.Get("pid") && e.worker.Get("start_time") == legacy.Get("start_time") {
						counted = true
					}
				}
				if !counted {
					count++
				}
			}
			if count >= limit {
				return &runFail{fmt.Sprintf("review.workers limit %s reached", runNumber(limit)), false}
			}
			pid, launchedRoot, err := runLaunchWorker(runID, workspace, binary)
			if err != nil {
				return err
			}
			startedAt := runProbe(float64(pid))
			if _, err := runRepin(home, runID, launchedRoot, float64(pid), true); err != nil {
				runStopLaunched(o, pid, startedAt)
				if errors.Is(err, errRunHandover) {
					return &runFail{"the worker registry could not be read", false}
				}
				return err
			}
			line = fmt.Sprintf("run %s: added worker pid %d (review.workers limit %s)\n", runID, pid, runNumber(limit))
			return nil
		}, filelock.Options{})
		if errors.Is(err, errRunHandover) {
			return runHandover([]string{"start", "--add-worker", "--run", runID}, hRunShape)
		}
		if err != nil {
			return runReport(o, "start", err)
		}
		o.out(line)
		return 0
	}, 0
}

// --- run reload --run <id>: the refusals before a valid config ---

var (
	runReloadErrno = regexp.MustCompile(`could not read .*: (E[A-Z]+)$`)
)

// runYAMLSyntax is a YAML syntax error Node's parser words (and counts its
// line) its own way, so that report stays with Node. A document that parses
// to something other than a mapping, a duplicate key and a tag that does not
// resolve are worded the same by both (B472: compared on broken fixtures).
func runYAMLSyntax(e *config.Error) bool {
	if e.Kind != "parse-error" {
		return false
	}
	for _, same := range []string{"config root must be a mapping", "DUPLICATE_KEY: ", "TAG_RESOLVE_FAILED: "} {
		if strings.HasPrefix(e.Message, same) {
			return false
		}
	}
	return true
}

// runYAMLLine is Node's reload line: the line of "(line N, column M)" at the
// end of the parser's message, never the message itself.
var runYAMLLine = regexp.MustCompile(`\(line (\d+), column \d+\)$`)

func runReloadPlan(home, runID string) (func(o *runOut) int, handoverReason) {
	record, readErr, ok := runReadRecord(home, runID)
	if !ok {
		return nil, hRunRecord
	}
	if readErr != nil {
		return func(o *runOut) int { return runReport(o, "reload", readErr) }, 0
	}
	if record.Get("status") != "active" {
		fail := &runFail{fmt.Sprintf("run %s has ended", runID), false}
		return func(o *runOut) int { return runReport(o, "reload", fail) }, 0
	}
	workspace, isString := record.Get("workspace").(string)
	if !isString {
		return nil, hRunShape
	}
	_, err := config.LoadSnapshot(workspace)
	if err == nil {
		// B473: a config that loads is validated, diffed and guarded in Go
		return runReloadLoadedPlan(home, runID, record, workspace)
	}
	var cfgErr *config.Error
	if !errors.As(err, &cfgErr) || runYAMLSyntax(cfgErr) {
		// Node's parser words a YAML syntax error (and counts its line)
		return nil, hRunReload
	}
	var text string
	switch cfgErr.Kind {
	case "not-found":
		text = "no .code-forge.yml in the workspace"
	case "parse-error":
		text = "the config could not be parsed (.code-forge.yml)" // a root that is not a mapping has no line
		if m := runYAMLLine.FindStringSubmatch(cfgErr.Message); m != nil {
			text = "the config could not be parsed (.code-forge.yml:" + m[1] + ")"
		}
	case "migrate-error":
		text = ".code-forge.yml could not be migrated to the current format"
	default:
		text = ".code-forge.yml could not be read"
		if m := runReloadErrno.FindStringSubmatch(cfgErr.Message); m != nil {
			text += " (" + m[1] + ")"
		}
	}
	return func(o *runOut) int {
		o.err("run reload: " + text + " — nothing changed\n")
		return 1
	}, 0
}
