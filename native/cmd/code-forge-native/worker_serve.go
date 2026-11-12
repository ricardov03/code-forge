package main

// B489: `code-forge worker --run <id>` serves the queue in Go (src/cli/
// worker.mjs runWorker + src/worker/loop.mjs bootWorker). Go serves a run
// only when every part it reaches is ported; the rest goes to Node as a whole
// worker, before anything is written:
//   - the System 1 (Jev) key comes from a source Go does not read like
//     Node (B533: the OS keychain, a live 1Password read, a ref Node throws
//     on, a store entry Go cannot judge); an env or file-store key is
//     resolved here and the review round asks Jev in Go;
//   - the boot config turns on the commit watch (review.file_loop: watch);
//   - the run has no config snapshot (loadProjectConfig is not ported here);
//   - a read Go cannot judge like Node (record shape, key, registry).
// While serving, the first ticket RunTicket leaves to Node (or an autopilot
// approval / grant Go does not decide) stops the Go worker: running tickets
// finish, no new one starts, the announcement is withdrawn and this process
// becomes the Node worker (same pid and start time), which then serves every
// ticket still pending. See native/worker/serve.go for the split rule.

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/ricardov03/code-forge/native/blockclose"
	"github.com/ricardov03/code-forge/native/daemonclient"
	"github.com/ricardov03/code-forge/native/filelock"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/jev"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/session"
	"github.com/ricardov03/code-forge/native/state"
	"github.com/ricardov03/code-forge/native/worker"
)

// Seams for tests: the daemon start, the Jev probe and the signals.
var (
	workerStartDaemon = startNativeDaemon
	workerJevProbe    = worker.JevProbe{}
	workerSignals     = func(c chan<- os.Signal) func() {
		signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
		return func() { signal.Stop(c) }
	}
	// workerServing runs once the Go loop serves (tests stop it from here).
	workerServing = func(*worker.Loop) {}
	// workerOrphans is the orphan clean-up after a drain (reaper sweep).
	workerOrphans = func(run string) func() { return (&worker.OrphanSweep{RunID: run}).Sweep }
	// workerNow is the key resolver's clock; workerJevOpts are askJev's
	// options other than the key (B533 tests point URL at a fake Jev).
	workerNow     = time.Now
	workerJevOpts = jev.Opts{}
)

type workerServeArgs struct {
	runID, cwd string
	hasCwd     bool
	pollMs     int
	once       bool
}

// workerServePlan is bootWorker up to createWorker: refusals Node makes print
// as Node prints them; a case Go does not serve returns its hand-over reason.
// The last result is the Jev key resolver (B533): it runs resolveJevKey's
// side effects (an expired entry deleted, an entry cut to the cache hours)
// and gives the key ("" none) and every value Node registers for redaction.
func workerServePlan(home string, a workerServeArgs) (*worker.TicketRunner, map[string]any, func(o *runOut) int, handoverReason, func() (string, []string)) {
	fail := func(message string) func(o *runOut) int {
		return func(o *runOut) int { o.err("worker: " + message + "\n"); return 1 }
	}
	record, err := state.ReadRun(home, a.runID)
	if err != nil {
		return nil, nil, nil, hWorkerRecord, nil
	}
	if status, _ := record.Get("status").(string); status != "active" {
		return nil, nil, fail("run " + a.runID + " has ended"), 0, nil
	}
	dir := a.cwd
	if !a.hasCwd {
		workspace, isString := record.Get("workspace").(string)
		if !isString {
			return nil, nil, nil, hWorkerShape, nil
		}
		dir = workspace
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return nil, nil, nil, hWorkerShape, nil
	}
	root, err := gitx.RepoTop(dir)
	if err != nil {
		return nil, nil, fail("not inside a git repository"), 0, nil
	}
	slug, isString := record.Get("project").(string)
	if !isString {
		return nil, nil, nil, hWorkerShape, nil
	}
	runner := &worker.TicketRunner{Home: home, RunID: a.runID, RepoRoot: root, Slug: slug}
	cfg, bad, err := runner.ConfigInForce()
	if err != nil {
		return nil, nil, nil, hWorkerRecord, nil
	}
	if bad {
		return nil, nil, fail("run " + a.runID + ": the config snapshot in the run record is missing or altered"), 0, nil
	}
	if cfg == nil {
		return nil, nil, nil, hWorkerConfig, nil
	}
	if review, _ := cfg["review"].(map[string]any); review["file_loop"] == "watch" {
		return nil, nil, nil, hWorkerWatch, nil
	}
	jevKey, reason := workerJevResolver(cfg, home)
	if reason != 0 {
		return nil, nil, nil, reason, nil
	}
	runner.Cfg = cfg
	return runner, cfg, nil, 0, jevKey
}

// workerJevResolver is loop.mjs resolveJevKey decided before any write: the
// env variable and the file store (CODE_FORGE_KEY_BACKEND=file) run in Go
// (doctor's jevKeyChain); with the default backends a key Go proves absent
// is none, any other goes to Node (the keychain is not read here).
func workerJevResolver(cfg map[string]any, home string) (func() (string, []string), handoverReason) {
	chain, plan, reason := jevKeyChain(cfg, home, float64(workerNow().UnixMilli()), false)
	switch {
	case plan.badRef:
		return nil, hWorkerJevRef
	case chain != nil:
		return func() (string, []string) {
			r := chain()
			return r.key, r.secrets
		}, 0
	case reason == hDoctorKeyStore && os.Getenv("CODE_FORGE_KEY_BACKEND") != "file":
		if worker.JevKeyAbsent(cfg, workerJevProbe) {
			return func() (string, []string) { return "", nil }, 0
		}
		return nil, hWorkerJevKeychain
	}
	return nil, hWorkerJevStore
}

// workerRunRoot is tmp.mjs runRoot: the run's root, made if missing, with
// this process recorded in owner.json (never reused as found).
func workerRunRoot(run string, cfg map[string]any) (string, error) {
	var root *string
	if tmp, ok := cfg["tmp"].(map[string]any); ok {
		if s, ok := tmp["root"].(string); ok {
			root = &s
		}
	}
	return contextMakeRunRoot(run, root)
}

// startNativeDaemon is native.mjs startNativeDaemon: a daemon already
// answering is left alone; else this binary serves one until stop.
func startNativeDaemon(run string, cfg map[string]any) (func(), error) {
	noop := func() {}
	if !daemonclient.Enabled(cfg) {
		return noop, nil
	}
	if answer, _ := daemonclient.Request(run, map[string]any{"method": "ping"}, 0); answer != nil {
		return noop, nil
	}
	binary, err := os.Executable()
	if err != nil {
		return noop, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return noop, nil
	}
	child := exec.Command(binary, "internal", "serve", "--home", home, "--run", run)
	if err := child.Start(); err != nil {
		return noop, nil
	}
	exited := make(chan struct{})
	go func() { _ = child.Wait(); close(exited) }()
	for i := 0; i < 100; i++ {
		select {
		case <-exited:
			return noop, nil
		default:
		}
		if answer, _ := daemonclient.Request(run, map[string]any{"method": "ping"}, 100*time.Millisecond); answer != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return func() {
		select {
		case <-exited:
			return
		default:
		}
		_ = child.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(2 * time.Second):
			_ = child.Process.Kill()
			<-exited
		}
	}, nil
}

// workerServe boots and serves; handover is the reason to give the worker to
// Node instead ("" none: the code is final).
func workerServe(home string, args []string, a workerServeArgs, o *runOut) (int, handoverReason) {
	runner, cfg, refuse, reason, jevKey := workerServePlan(home, a)
	if refuse != nil {
		return refuse(o), 0
	}
	if runner == nil {
		return 0, reason
	}
	contextApplyTmpRoot()
	runRootDir, err := workerRunRoot(a.runID, cfg)
	if err != nil {
		o.err("worker: " + err.Error() + "\n")
		return 1, 0
	}
	key, err := blockclose.ReadKey(home, a.runID)
	if err != nil {
		var stateErr *state.Error
		if errors.As(err, &stateErr) {
			o.err("worker: " + stateErr.Message + "\n")
			return 1, 0
		}
		return 0, hWorkerBoot
	}
	pid := os.Getpid()
	var started any
	if t := filelock.ProcessStartTime(pid); t != nil {
		started = *t
	}
	identity := ledger.Object{{Key: "pid", Value: float64(pid)}, {Key: "started_at", Value: started}}
	runner.Key = key
	runner.Worker = identity
	runner.RunRootDir = runRootDir
	runner.Secrets = []string{hex.EncodeToString(key)}
	// loop.mjs createWorker: resolveJevKey before the boot lock (B533)
	jevValue, jevSecrets := jevKey()
	runner.JevKey = jevValue
	runner.JevOpts = workerJevOpts
	runner.Secrets = append(runner.Secrets, jevSecrets...)
	// B489 step 3: Node's worker sets its process root to the run root
	// (setRunRoot), so a session's child pid entry lands in <run root>/pids.
	runner.SessionDeps = session.Deps{Stderr: os.Stderr, PidsRoot: func() (string, error) { return runRootDir, nil }}
	spawnWarnVerb = "worker"
	runner.Warn = spawnLogWarning

	wk := worker.New(worker.Opts{Home: home, RunID: a.runID, Root: runner.RepoRoot, Cfg: cfg,
		StartDaemon: func() (func(), error) { return workerStartDaemon(a.runID, cfg) }})
	if err := wk.Boot(); err != nil {
		var werr *worker.Error
		if errors.As(err, &werr) {
			o.err("worker: " + werr.Message + "\n")
			return 1, 0
		}
		return 0, hWorkerBoot
	}
	if r := wk.Repinned; r != nil {
		from := ""
		for i, p := range r.From {
			if i > 0 {
				from += ","
			}
			from += strconv.FormatFloat(p, 'f', -1, 64)
		}
		o.err(fmt.Sprintf("worker: re-pinned run %s from dead pid %s to %d\n", a.runID, from, r.To))
	}
	runner.Stopping = wk.Stopping
	pool := &worker.Pool{Root: runner.RepoRoot, Run: a.runID, PollMs: a.pollMs, BootCfg: cfg,
		Settings: runner.Settings, FileKey: runner.FileKey, ProviderHint: runner.ProviderHint,
		Warn: runner.Warn, Stderr: os.Stderr, Stopping: wk.Stopping,
		GuardPath: filepath.Join(home, ".code-forge", "runs", a.runID+".json")}
	runner.Pool = pool
	loop := &worker.Loop{Worker: wk, Pool: pool, Runner: runner, PollMs: a.pollMs,
		CleanOrphans: workerOrphans(a.runID)}
	pool.RunOne = loop.RunOne
	pool.Stop = loop.Stop
	pool.ExpiryCheck = loop.ExpiryCheck
	if a.once {
		done, err := loop.Once()
		if loop.Handoff() != "" {
			return 0, hWorkerTicket
		}
		if err != nil {
			o.err("worker: " + err.Error() + "\n")
			return 1, 0
		}
		o.err(fmt.Sprintf("worker: drained %d ticket(s)\n", done))
		return 0, 0
	}
	loop.Idle = &worker.Idle{RepoRoot: runner.RepoRoot,
		Activity: worker.ActivityDeps{Trees: map[string]string{}, ReadRows: runner.LedgerRows},
		Hours: func() (float64, error) {
			inForce, _ := runner.Settings()
			return worker.IdleExitHours(inForce), nil
		},
		Record: runner.RunRecord, Row: runner.Ledger, Identity: identity}
	signals := make(chan os.Signal, 2)
	stopSignals := workerSignals(signals)
	go func() {
		for range signals {
			loop.Stop()
		}
	}()
	jevState := "absent (fallback)"
	if jevValue != "" {
		jevState = "resolved"
	}
	o.err(fmt.Sprintf("worker: pid %d serving run %s · jev key %s\n", pid, a.runID, jevState))
	workerServing(loop)
	loop.Run()
	stopSignals()
	if loop.Handoff() != "" {
		return 0, hWorkerTicket
	}
	o.err("worker: stopped\n")
	return 0, 0
}
