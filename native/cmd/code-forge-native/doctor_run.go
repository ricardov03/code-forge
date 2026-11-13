package main

// B525: the full `doctor` run with a valid config in Go (src/doctor/index.mjs
// runDoctor + src/cli/doctor.mjs runDoctorCli), wired from the B504 local
// rows, the B505 Jev and CLI probes, the B523 pings and the B524 worker probe.
//
// No Go side effect before a hand-over: prepareDoctorRun decides every case
// Go cannot serve before the body runs. It reads files, scans the process
// table and runs each provider CLI's --version/--help (read-only, as the
// presence probe must, to know which CLIs are missing), and asks the ping,
// isolation and path-deny probes with planOnly, which stops each one before
// its first file write or session. The body then makes the run root, runs
// the worker probe first (the one probe that can still meet a case Go cannot
// judge, a worker registry entry, and that removes all it made before it
// says so, B532), then the key, the Jev call, the ledger directory and the
// model-call probes.
//
// B535: the body finishes the I/O errors like Node. A run root that cannot
// be made is tried again under runDoctor's own name, then becomes Node's
// `probes: no temp root (<message>)` row with the signer row and no probe;
// an error Node's probe catches is its row (`ping=error(EACCES)`); an error
// Node throws out of the doctor (the work dir, a prompt file, the canary, a
// temp repo, the Codex rules file, an empty Jev key) is the launcher's crash
// line, exit 1, with the run root removed.

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/daemonclient"
	"github.com/ricardov03/code-forge/native/jev"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
	"github.com/ricardov03/code-forge/native/worker"
)

// drunNow is the clock of the probe run id (tests).
var drunNow = time.Now

// drunNewRunID is newRunId(): r-<UTC yyyymmdd-hhmmss>-<6 hex>.
func drunNewRunID() string {
	var b [3]byte
	_, _ = dfRandom(b[:])
	return "r-" + drunNow().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}

// drunProvider is `cfg.levels?.<level>?.provider ?? cfg.provider` as a
// missing-set key: ok false when the value is not a string (never missing).
func drunProvider(cfg map[string]any, level string) (string, bool) {
	var v any
	if levels, ok := cfg["levels"].(map[string]any); ok {
		if l, ok := levels[level].(map[string]any); ok {
			v = l["provider"]
		}
	}
	if v == nil {
		v = cfg["provider"]
	}
	s, ok := v.(string)
	return s, ok
}

// prepareDoctorRun is the full run when .code-forge.yml loads and validates
// at cwd; rows are the config rows.
func prepareDoctorRun(args []string, cwd, home string, rows []doctorRow, cfg map[string]any, jsonOut bool) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
	_, order, err := config.LoadOrdered(cwd)
	if err != nil {
		return nil, hDoctorFullRun, false
	}
	snap, err := config.LoadSnapshot(cwd)
	if err != nil || snap.Ordered == nil {
		return nil, hDoctorRunWorkerProbe, false
	}
	var tmpRoot *string
	if tmp, ok := cfg["tmp"].(map[string]any); ok {
		if root, ok := tmp["root"].(string); ok {
			if !filepath.IsAbs(root) {
				// Node prints `probes: no temp root (...)` and skips every
				// probe; the sweep and review rows read the bad root too.
				return nil, hDoctorRunTmpRoot, false
			}
			tmpRoot = &root
		}
	}
	if !filepath.IsAbs(home) {
		return nil, hDoctorRunWorkerProbe, false
	}
	if _, err := runWorkerBinary(); err != nil {
		return nil, hDoctorRunWorkerProbe, false
	}
	now := doctorNow()
	pathEnv := os.Getenv("PATH")

	rows = append(rows, doctorLinksRow(home))
	resolveKeys, keyPlan, reason := doctorKeysResolve(cfg, home, now)
	if reason != 0 {
		return nil, reason, false
	}
	if keyPlan.maybe {
		// checkJev throws on an empty key or a config the risk payload refuses.
		if _, ok := jev.RiskPayload(cfg); !ok || keyPlan.empty {
			return nil, hDoctorRunJev, false
		}
	}
	keysAt := len(rows)
	rows = append(rows, doctorRow{})
	if slug := cfg["project"]; slug != nil {
		lines, ok := doctorReviewerLines(cfg, home, now)
		if !ok {
			return nil, hDoctorLedgerShape, false
		}
		for i, line := range lines {
			at := strings.Index(line, ": ")
			rows = append(rows, doctorRow{"reviewer." + strconv.Itoa(i), "INFO", line[:at], line[at+2:]})
		}
	}
	rows = append(rows, dfCommandRows(cfg, order, pathEnv)...)
	jevAt := len(rows)
	rows = append(rows, doctorRow{})
	rows = append(rows, dfToolsRows()...)
	ledgerAt := len(rows)
	rows = append(rows, doctorRow{})
	// The run root. Node's CLI makes and adopts its root (setRunRoot) before
	// runDoctor, so exec records every child in its pids/, the locks
	// check's git included; a root it cannot make leaves the children to the
	// lazy root (proc-<pid>-<ms>), released at exit (B567). Every hand-over
	// from here removes the root and releases it, so it writes nothing.
	if !routerHintReadable() {
		return nil, hRouterHintState, false // smallVerb.run's check, before the root
	}
	contextApplyTmpRoot() // Node applies it at process start (the lazy root's base)
	var bytes [3]byte
	_, _ = dfRandom(bytes[:])
	root, err := contextMakeRunRoot(fmt.Sprintf("doctor-%d-%s", os.Getpid(), hex.EncodeToString(bytes[:])), tmpRoot)
	adopted := err == nil
	lrRoot = ""
	if adopted {
		lrRoot = root
	}
	release := func() {
		if adopted {
			lrRoot = ""
		} else {
			toolsReleaseRoot()
		}
	}
	if err != nil {
		// the CLI's root failed: runDoctor makes its own (doctorRoot)
		_, _ = dfRandom(bytes[:])
		root, err = contextMakeRunRoot(fmt.Sprintf("doctor-%d-%d-%s", os.Getpid(), drunNow().UnixMilli(), hex.EncodeToString(bytes[:])), tmpRoot)
	}
	noRoot := ""
	if err != nil {
		noRoot, root = err.Error(), ""
	}
	handover := func(reason handoverReason) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
		if root != "" {
			_ = os.RemoveAll(root)
		}
		release()
		return nil, reason, false
	}
	locks, reason := dfLocksRows(physicalCwd())
	if reason != 0 {
		return handover(reason)
	}
	rows = append(rows, locks...)
	envBase := dfTmpBase()
	cfgRoot, cfgSet := "", tmpRoot != nil
	sweepBase := envBase
	if cfgSet {
		cfgRoot, sweepBase = *tmpRoot, *tmpRoot
	}
	rows = append(rows, dfTmpRows(cfgRoot, cfgSet, envBase)...)
	sweep, reason := dfSweepRows(home, sweepBase)
	if reason != 0 {
		return handover(reason)
	}
	rows = append(rows, sweep...)
	review, runKeys, reason := dfReviewStateRows(home, cfgRoot, cfgSet, envBase)
	if reason != 0 {
		return handover(reason)
	}
	rows = append(rows, review...)
	rows = append(rows, dfReviewConcurrencyRow(cfg, order))
	rows = append(rows, dfCoderSandboxRows(cfg)...)

	// The CLI presence probes (read-only: --version and --help). Node runs
	// them after the run root exists: a doctor with no root has none. The
	// presence is decided here (no child); the --version and --help children
	// run once the run root exists (B567).
	cliAt := len(rows)
	missing := map[string]bool{}
	providers := doctorConfiguredProviders(cfg)
	var clis []drunCli
	for _, provider := range providers {
		cli := doctorCLINames[provider]
		if cli == "" {
			clis = append(clis, drunCli{rows: []doctorRow{{"cli." + provider, "FAIL", provider, "no CLI mapping for this provider"}}})
			missing[provider] = true
			continue
		}
		found, path, present := doctorProbeCliFind(cli, cli, pathEnv)
		clis = append(clis, drunCli{rows: found, cli: cli, path: path})
		if !present {
			missing[provider] = true
		}
	}
	plan := doctorProbeCtx{cfg: cfg, cwd: cwd, environ: os.Environ(), planOnly: true}
	if _, ok := doctorPingRoles(plan, missing); !ok {
		return handover(hDoctorRunPing)
	}
	reviewer, isString := drunProvider(cfg, "L2")
	skipIsolation := isString && missing[reviewer]
	if !skipIsolation {
		if _, ok := doctorProbeIsolation(plan); !ok {
			return handover(hDoctorRunIsolation)
		}
	}
	coder, isString := drunProvider(cfg, "L1")
	skipPathDeny := isString && missing[coder]
	if !skipPathDeny {
		if _, ok := doctorProbePathDeny(plan, ""); !ok {
			return handover(hDoctorRunPathDeny)
		}
	}
	codexRules := false
	for _, p := range providers {
		codexRules = codexRules || p == "openai"
	}

	// The CLI's post-steps: the native binary, the worktree indexing warning
	// (darwin) and the daemon row.
	native, reason := dfNativeRow()
	if reason != 0 {
		return handover(reason)
	}
	tail := []doctorRow{native}
	daemon, indexing := "off", false
	inForce, liveRun := cfg, ""
	live, err := worker.Live(config.ProjectRootFor(cwd), worker.LiveOpts{}, worker.Probes{})
	switch {
	case errors.Is(err, worker.ErrUndecided):
		return handover(hDoctorWorkerRegistry)
	case err == nil && live != nil:
		// B617: the live worker's run config in force (else the loaded
		// one); its daemon is pinged in the body
		run, snapshot, ok := drunLiveConfig(home, live)
		if !ok {
			break // Node's catch: daemon off, no indexing row
		}
		if snapshot != nil {
			inForce = snapshot
		}
		liveRun = run
		if daemonclient.Enabled(inForce) {
			daemon = "fallback"
		}
		indexing = drunPlatform == "darwin"
	case err == nil:
		if daemonclient.Enabled(cfg) {
			daemon = "fallback"
		}
		// the indexing check's git child runs in the body, after the
		// probes, while the run root is adopted (B567)
		indexing = drunPlatform == "darwin"
	}

	// The --version and --help children run last, so a version line Go
	// cannot cut hands over before the worker, the keys, the Jev call or
	// HOME is touched.
	var cliRows []doctorRow
	if root != "" {
		for _, c := range clis {
			cliRows = append(cliRows, c.rows...)
			if c.path == "" {
				continue
			}
			probed, ok := doctorProbeCliRun(c.cli, c.cli, c.path)
			if !ok {
				return handover(hDoctorRunCliVersion)
			}
			cliRows = append(cliRows, probed...)
		}
	}

	return func(stdout, stderr io.Writer) int {
		nativeReportedKind = ""
		defer release()
		crash := func(msg string) int {
			if root != "" {
				_ = os.RemoveAll(root)
			}
			fmt.Fprint(stderr, "code-forge: verb \"doctor\" failed: "+msg+"\n")
			return 1
		}
		d := drunCtx{cfg: cfg, snap: snap, cwd: cwd, home: home, root: root, missing: missing,
			skipIsolation: skipIsolation, skipPathDeny: skipPathDeny, codexRules: codexRules}
		var workerRows []doctorRow
		if root != "" {
			d.workDir = filepath.Join(root, "doctor")
			if err := contextMkdirAll(d.workDir); err != nil {
				return crash("Error: " + err.Error()) // mkdirSync(workDir) throws
			}
			var ok bool
			workerRows, ok = doctorWorkerProbe(dwpCtx{cfg: d.snap, configFile: d.snap.Path, workDir: d.workDir, runRootDir: root,
				env: os.Environ(), runID: drunNewRunID(), home: home, register: lrRegisterChild})
			if !ok {
				_ = os.RemoveAll(root)
				return smallVerbHandover("doctor", args, hDoctorWorkerRegistry) // B532: a registry entry Go cannot judge
			}
		}
		keys := resolveKeys()
		secrets := keys.secrets
		rows[keysAt] = keys.row
		jevRow, ok := doctorJevRow(cfg, keys.key, keys.hasKey)
		switch {
		case !ok && keys.key == "":
			return crash("TypeError: askJev: key must be a non-empty string")
		case !ok:
			// unreachable: prepare checked the risk payload, and the call
			// errs only on an empty key
			_ = os.RemoveAll(root)
			return smallVerbHandover("doctor", args, hDoctorRunJev)
		}
		rows[jevAt] = jevRow
		rows[ledgerAt] = dfLedgerRow(home)
		var probes []doctorRow
		if root == "" {
			rows = rows[:cliAt]
			probes = []doctorRow{{"probes", "FAIL", "probes", "no temp root (" + noRoot + ")"}, dwpSignerRow}
		} else {
			rows = append(rows[:cliAt], cliRows...)
			var thrown string
			var reason handoverReason
			probes, thrown, reason = drunProbes(d)
			switch {
			case thrown != "":
				return crash("Error: " + thrown)
			case reason != 0:
				_ = os.RemoveAll(root)
				return smallVerbHandover("doctor", args, reason)
			}
			probes = append(probes, workerRows...)
		}
		if daemon == "fallback" && liveRun != "" && drunDaemonPing(liveRun) {
			daemon = "live"
		}
		if indexing {
			tail = append(tail, drunIndexingRows(cwd, snap.Path, inForce, liveRun)...)
		}
		tail = append(tail, doctorRow{"native_daemon", "INFO", "native daemon", daemon})
		if root != "" {
			_ = os.RemoveAll(root)
		}
		all := append(append(append([]doctorRow{}, rows...), probes...), tail...)
		secrets = append(secrets, runKeys...)
		redactOne := func(text string) string { return redactSecret(text, secrets...) }
		var out string
		if jsonOut {
			out = redactOne(doctorJSON(all)) + "\n"
		} else {
			var b strings.Builder
			for _, r := range all {
				b.WriteString(redactOne(fmt.Sprintf("%-4s %s: %s", r.status, r.label, r.detail)) + "\n")
			}
			out = b.String()
		}
		fmt.Fprint(stdout, redactOne(out))
		c := doctorCounts(all)
		fmt.Fprint(stderr, redactOne(fmt.Sprintf("doctor: %d ok, %d warn, %d fail\n", c["OK"], c["WARN"], c["FAIL"])))
		if c["FAIL"] > 0 {
			return 1
		}
		nativeReportedKind = ""
		return 0
	}, 0, true
}

// drunLiveConfig is configInForce(await readRun(worker.run)) for the live
// worker: the run id and the snapshot in force (nil for a record from
// before snapshots or a JSON value that is not an object). ok is false where
// Node throws (a bad or missing run, unreadable JSON, a snapshot that does
// not match its hash): the CLI's catch leaves the daemon off.
func drunLiveConfig(home string, live ledger.Object) (run string, snapshot map[string]any, ok bool) {
	run, isString := live.Get("run").(string)
	if !isString {
		return "", nil, false
	}
	record, err := state.ReadRun(home, run)
	if err != nil {
		data, readErr := os.ReadFile(filepath.Join(home, ".code-forge", "runs", run+".json"))
		if readErr != nil {
			return "", nil, false
		}
		if _, jsonErr := ledger.DecodeValue(data); jsonErr != nil {
			return "", nil, false
		}
		return run, nil, true // valid JSON, not an object: no snapshot
	}
	cfg, _, valid := sweepConfigInForce(record)
	if !valid {
		return "", nil, false
	}
	if cfg != nil {
		snapshot, _ = runDeepMap(cfg).(map[string]any)
	}
	return run, snapshot, true
}

// drunDaemonPing is daemonRequest(run, {method: 'ping'}): true when the run's
// daemon answers the version-negotiated ping.
func drunDaemonPing(run string) bool {
	daemonclient.Version = version
	_, err := daemonclient.Request(run, map[string]any{"method": "ping"}, 0)
	return err == nil
}

// drunPlatform is the platform the indexing check is gated on (Node's
// deps.platform ?? process.platform, src/cli/doctor.mjs); tests set it (B597).
var drunPlatform = runtime.GOOS

// drunIndexingRows is the CLI's darwin worktree indexing check for a loaded
// config with no live worker: top is the repo top (realpath), else the config
// file's folder. A non-string proof.worktree.dir makes path.resolve throw;
// Node's catch prints no row.
func drunIndexingRows(cwd, configPath string, cfg map[string]any, runID string) []doctorRow {
	top := filepath.Dir(configPath)
	if out, ok := sweepGitHooked(cwd, lrRegisterChild, "rev-parse", "--show-toplevel"); ok {
		if real, err := filepath.EvalSymlinks(strings.TrimSpace(out)); err == nil {
			top = real
		}
	}
	wt, _ := objAt(cfg, "proof", "worktree")
	var dir *string
	switch v := wt["dir"].(type) {
	case nil:
	case string:
		dir = &v
	default:
		return nil
	}
	// wt.branch ?? DEFAULT_BRANCH_TEMPLATE; only a live worker's run reads
	// it, where a non-string branch throws in replaceAll (no row)
	branch := "forge/{run}/{block}"
	if v, has := wt["branch"]; has && v != nil {
		b, isString := v.(string)
		if !isString && runID != "" && dir == nil {
			return nil
		}
		branch = b
	}
	rows, ok := dfWorktreeIndexingRow(top, dir, branch, runID)
	if !ok {
		return nil
	}
	return rows
}

// drunCli is one configured provider's CLI probe decided in prepare: rows
// given without a child, then (path != "") the --version and --help calls
// the body runs.
type drunCli struct {
	rows      []doctorRow
	cli, path string
}

// drunCtx is what the body's probes need.
type drunCtx struct {
	cfg                         map[string]any
	snap                        config.Snapshot
	cwd, home, root, workDir    string
	missing                     map[string]bool
	skipIsolation, skipPathDeny bool
	codexRules                  bool
}

// drunProbes is runDoctor's probe block after the worker probe: the pings,
// isolation, path deny and Codex rules, in Node's order. thrown is the
// message of an error Node throws out of the doctor (B535). reason != 0 is
// a probe that refused after prepare's planOnly pass accepted it (none is
// known: the refusals depend on the config only): hand over.
func drunProbes(d drunCtx) (rows []doctorRow, thrown string, reason handoverReason) {
	c := doctorProbeCtx{cfg: d.cfg, cwd: d.cwd, workDir: d.workDir, runRootDir: d.root, environ: os.Environ(), crash: &thrown, pidsRoot: lrProcRoot}
	rows, ok := doctorPingRoles(c, d.missing)
	if !ok {
		return nil, thrown, hDoctorRunPing
	}
	if d.skipIsolation {
		rows = append(rows, doctorRow{"isolation", "FAIL", "isolation", "skipped (reviewer CLI missing)"})
	} else {
		more, ok := doctorProbeIsolation(c)
		if !ok {
			return nil, thrown, hDoctorRunIsolation
		}
		rows = append(rows, more...)
	}
	if d.skipPathDeny {
		rows = append(rows, doctorRow{"path-deny", "WARN", "path deny", "skipped (coder CLI missing)"})
	} else {
		more, ok := doctorProbePathDeny(c, filepath.Join(d.home, ".code-forge", "runs"))
		if !ok {
			return nil, thrown, hDoctorRunPathDeny
		}
		rows = append(rows, more...)
	}
	if d.codexRules {
		more, ok := doctorProbeCodexRules(c, d.root)
		if !ok {
			return nil, thrown, hDoctorRunCodexRules
		}
		rows = append(rows, more...)
	}
	return rows, "", 0
}
