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
// says so, B532), then the model-call probes, the key, the Jev call and the
// ledger directory. What is left to hand over from the body is an I/O error
// Node reports differently (a prompt file that cannot be written, a run root
// that cannot be made): the run root is removed before the hand-over.

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
	"github.com/ricardov03/code-forge/native/jev"
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
	locks, reason := dfLocksRows(physicalCwd())
	if reason != 0 {
		return nil, reason, false
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
		return nil, reason, false
	}
	rows = append(rows, sweep...)
	review, runKeys, reason := dfReviewStateRows(home, cfgRoot, cfgSet, envBase)
	if reason != 0 {
		return nil, reason, false
	}
	rows = append(rows, review...)
	rows = append(rows, dfReviewConcurrencyRow(cfg, order))
	rows = append(rows, dfCoderSandboxRows(cfg)...)

	// The CLI presence probes (read-only: --version and --help).
	missing := map[string]bool{}
	providers := doctorConfiguredProviders(cfg)
	for _, provider := range providers {
		cli := doctorCLINames[provider]
		if cli == "" {
			rows = append(rows, doctorRow{"cli." + provider, "FAIL", provider, "no CLI mapping for this provider"})
			missing[provider] = true
			continue
		}
		probed, present, ok := doctorProbeCli(cli, cli, pathEnv)
		if !ok {
			return nil, hDoctorRunCliVersion, false
		}
		rows = append(rows, probed...)
		if !present {
			missing[provider] = true
		}
	}
	plan := doctorProbeCtx{cfg: cfg, cwd: cwd, environ: os.Environ(), planOnly: true}
	if _, ok := doctorPingRoles(plan, missing); !ok {
		return nil, hDoctorRunPing, false
	}
	reviewer, isString := drunProvider(cfg, "L2")
	skipIsolation := isString && missing[reviewer]
	if !skipIsolation {
		if _, ok := doctorProbeIsolation(plan); !ok {
			return nil, hDoctorRunIsolation, false
		}
	}
	coder, isString := drunProvider(cfg, "L1")
	skipPathDeny := isString && missing[coder]
	if !skipPathDeny {
		if _, ok := doctorProbePathDeny(plan, ""); !ok {
			return nil, hDoctorRunPathDeny, false
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
		return nil, reason, false
	}
	tail := []doctorRow{native}
	daemon := "off"
	live, err := worker.Live(config.ProjectRootFor(cwd), worker.LiveOpts{}, worker.Probes{})
	switch {
	case errors.Is(err, worker.ErrUndecided):
		return nil, hDoctorWorkerRegistry, false
	case err == nil && live != nil:
		return nil, hDoctorFullLiveWorker, false // the run's config may ping the daemon
	case err == nil:
		native, _ := cfg["native"].(map[string]any)
		if os.Getenv("CODE_FORGE_NATIVE") != "0" && native["daemon"] != false {
			daemon = "fallback"
		}
		if runtime.GOOS == "darwin" {
			tail = append(tail, drunIndexingRows(cwd, snap.Path, cfg)...)
		}
	}
	tail = append(tail, doctorRow{"native_daemon", "INFO", "native daemon", daemon})

	return func(stdout, stderr io.Writer) int {
		nativeReportedKind = ""
		var bytes [3]byte
		_, _ = dfRandom(bytes[:])
		root, err := contextMakeRunRoot(fmt.Sprintf("doctor-%d-%s", os.Getpid(), hex.EncodeToString(bytes[:])), tmpRoot)
		if err != nil {
			return smallVerbHandover("doctor", args, hDoctorRunRoot) // Node's `probes: no temp root` row
		}
		probes, reason := drunProbes(drunCtx{cfg: cfg, snap: snap, cwd: cwd, home: home, root: root, missing: missing,
			skipIsolation: skipIsolation, skipPathDeny: skipPathDeny, codexRules: codexRules})
		var secrets []string
		if reason == 0 {
			keys := resolveKeys()
			secrets = keys.secrets
			rows[keysAt] = keys.row
			jevRow, ok := doctorJevRow(cfg, keys.key, keys.hasKey)
			if !ok {
				reason = hDoctorRunJev
			}
			rows[jevAt] = jevRow
		}
		if reason != 0 {
			_ = os.RemoveAll(root)
			return smallVerbHandover("doctor", args, reason)
		}
		rows[ledgerAt] = dfLedgerRow(home)
		_ = os.RemoveAll(root)
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

// drunIndexingRows is the CLI's darwin worktree indexing check for a loaded
// config with no live worker: top is the repo top (realpath), else the config
// file's folder. A non-string proof.worktree.dir makes path.resolve throw;
// Node's catch prints no row.
func drunIndexingRows(cwd, configPath string, cfg map[string]any) []doctorRow {
	top := filepath.Dir(configPath)
	if out, ok := sweepGit(cwd, "rev-parse", "--show-toplevel"); ok {
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
	branch, _ := wt["branch"].(string)
	if branch == "" {
		branch = "forge/{run}/{block}"
	}
	rows, ok := dfWorktreeIndexingRow(top, dir, branch, "")
	if !ok {
		return nil
	}
	return rows
}

// drunCtx is what the body's probes need.
type drunCtx struct {
	cfg                         map[string]any
	snap                        config.Snapshot
	cwd, home, root             string
	missing                     map[string]bool
	skipIsolation, skipPathDeny bool
	codexRules                  bool
}

// drunProbes is runDoctor's probe block: the work dir, the worker probe
// (first: see the file comment), the pings, isolation, path deny and Codex
// rules. The rows come back in Node's order. reason != 0: hand over (the
// work dir is left for the caller's run root removal).
func drunProbes(d drunCtx) ([]doctorRow, handoverReason) {
	workDir := filepath.Join(d.root, "doctor")
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return nil, hDoctorRunRoot
	}
	environ := os.Environ()
	workerRows, ok := doctorWorkerProbe(dwpCtx{cfg: d.snap, configFile: d.snap.Path, workDir: workDir, runRootDir: d.root,
		env: environ, runID: drunNewRunID(), home: d.home})
	if !ok {
		return nil, hDoctorWorkerRegistry // B532: a registry entry Go cannot judge
	}
	c := doctorProbeCtx{cfg: d.cfg, cwd: d.cwd, workDir: workDir, runRootDir: d.root, environ: environ}
	rows, ok := doctorPingRoles(c, d.missing)
	if !ok {
		return nil, hDoctorRunPing
	}
	if d.skipIsolation {
		rows = append(rows, doctorRow{"isolation", "FAIL", "isolation", "skipped (reviewer CLI missing)"})
	} else {
		more, ok := doctorProbeIsolation(c)
		if !ok {
			return nil, hDoctorRunIsolation
		}
		rows = append(rows, more...)
	}
	if d.skipPathDeny {
		rows = append(rows, doctorRow{"path-deny", "WARN", "path deny", "skipped (coder CLI missing)"})
	} else {
		more, ok := doctorProbePathDeny(c, filepath.Join(d.home, ".code-forge", "runs"))
		if !ok {
			return nil, hDoctorRunPathDeny
		}
		rows = append(rows, more...)
	}
	if d.codexRules {
		more, ok := doctorProbeCodexRules(c, d.root)
		if !ok {
			return nil, hDoctorRunCodexRules
		}
		rows = append(rows, more...)
	}
	return append(rows, workerRows...), 0
}
