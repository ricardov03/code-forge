package main

// B504: the full doctor run's local rows in Go (src/doctor/local.mjs,
// src/doctor/probes.mjs checkCoderSandbox, src/cli/doctor.mjs post-steps),
// and the full run WITHOUT a loadable project config end to end. That path
// makes no live probe in Node: runDoctor pushes `probes: skipped: no valid
// config` and the signer line instead of the CLI, Jev, isolation and worker
// probes. A config that loads but fails validation, a live worker (daemon
// ping), another native binary at the package path, a worker registry entry
// or a run record Go cannot decode keep named hand-overs.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/contextpack"
	"github.com/ricardov03/code-forge/native/filelock"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/session"
	"github.com/ricardov03/code-forge/native/state"
	"github.com/ricardov03/code-forge/native/worker"
)

var dfArrayIndex = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

// dfJSKeys is Object.keys order: array-index keys ascending, then the rest
// in insertion order.
func dfJSKeys(keys []string) []string {
	var index, rest []string
	for _, k := range keys {
		if n, err := strconv.ParseUint(k, 10, 64); err == nil && dfArrayIndex.MatchString(k) && n < 1<<32-1 {
			index = append(index, k)
		} else {
			rest = append(rest, k)
		}
	}
	sort.SliceStable(index, func(i, j int) bool {
		a, _ := strconv.ParseUint(index[i], 10, 64)
		b, _ := strconv.ParseUint(index[j], 10, 64)
		return a < b
	})
	return append(index, rest...)
}

// dfCommandRows is checkCommands: code-forge on PATH, the Solo line and one
// row per configured gate command.
func dfCommandRows(cfg map[string]any, order config.Ordered, pathEnv string) []doctorRow {
	rows := []doctorRow{}
	if commandOnPath("code-forge", pathEnv) {
		rows = append(rows, doctorRow{"path", "OK", "code-forge", "on PATH"})
	} else {
		rows = append(rows, doctorRow{"path", "WARN", "code-forge", "not on PATH (the skill shim calls it)"})
	}
	rows = append(rows, doctorRow{"solo", "INFO", "solo", "UNVERIFIED (no Solo probe in this version)"})
	gates, _ := cfg["gates"].(map[string]any)
	found := false
	for _, name := range dfJSKeys(order.Keys(gates)) {
		command := gates[name]
		if m, ok := command.(map[string]any); ok {
			command = m["argv"] // B60: {argv, env}
		}
		argv, ok := command.([]any)
		if !ok || len(argv) == 0 {
			continue
		}
		found = true
		first := runJSString(argv[0])
		if argv[0] == nil {
			first = "null"
		}
		if commandOnPath(first, pathEnv) {
			rows = append(rows, doctorRow{"gate." + name, "OK", "gate " + name, first + " on PATH"})
		} else {
			rows = append(rows, doctorRow{"gate." + name, "FAIL", "gate " + name, first + " not on PATH"})
		}
	}
	if !found {
		rows = append(rows, doctorRow{"gates", "WARN", "gates", "no gate command configured"})
	}
	return rows
}

// dfCoderSandboxRows is probes.mjs checkCoderSandbox (B93).
func dfCoderSandboxRows(cfg map[string]any) []doctorRow {
	coder, _ := cfg["coder"].(map[string]any)
	sandbox, ok := coder["sandbox"].(map[string]any)
	if !ok {
		return []doctorRow{}
	}
	profile := "workspace"
	if p, set := sandbox["profile"]; set && p != nil {
		s, isString := p.(string)
		if !isString {
			return []doctorRow{}
		}
		profile = s
	}
	if profile != "workspace" && profile != "toolchain" && profile != "full" {
		return []doctorRow{}
	}
	extra := ""
	if profile == "toolchain" {
		dirs, _ := sandbox["add_dirs"].([]any)
		word := "dirs"
		if len(dirs) == 1 {
			word = "dir"
		}
		extra = fmt.Sprintf(", %d extra writable %s", len(dirs), word)
	}
	rows := []doctorRow{{"coder-sandbox", "INFO", "coder sandbox", "profile " + profile + extra + "; applies to the Codex (openai) coder only"}}
	providers := doctorConfiguredProviders(cfg) // B505, doctor_probes.go
	var others []string
	for _, pair := range [][2]string{{"anthropic", "Claude (anthropic)"}, {"xai", "Grok (xai)"}} {
		for _, p := range providers {
			if p == pair[0] {
				others = append(others, pair[1])
				break
			}
		}
	}
	if len(others) > 0 {
		who := "the " + others[0] + " coder"
		if len(others) > 1 {
			who = "the " + strings.Join(others, " and ") + " coders"
		}
		rows = append(rows, doctorRow{"coder-sandbox-no-effect", "INFO", "coder sandbox", "coder.sandbox has no effect on " + who + ": the argv does not change"})
	}
	if profile == "full" {
		rows = append(rows, doctorRow{"coder-sandbox-full", "WARN", "coder sandbox", "profile full runs the Codex coder with -s danger-full-access: it can write anywhere you can; only the forbidden list, the transcript grep and the gates limit it"})
	}
	return rows
}

// dfReviewConcurrencyRow is checkReviewConcurrency (B44).
func dfReviewConcurrencyRow(cfg map[string]any, order config.Ordered) doctorRow {
	n := worker.ParallelTickets(cfg)
	providers := []string{"anthropic", "openai", "xai"}
	review, _ := cfg["review"].(map[string]any)
	if pc, ok := review["provider_concurrency"].(map[string]any); ok {
		for _, p := range dfJSKeys(order.Keys(pc)) {
			if p != "anthropic" && p != "openai" && p != "xai" {
				providers = append(providers, p)
			}
		}
	}
	slots := make([]string, len(providers))
	for i, p := range providers {
		slots[i] = fmt.Sprintf("%s %d", p, session.ProviderLimit(cfg, p))
	}
	word := "tickets"
	if n == 1 {
		word = "ticket"
	}
	return doctorRow{"review-concurrency", "INFO", "review concurrency", fmt.Sprintf("%d %s at once; provider slots %s", n, word, strings.Join(slots, " · "))}
}

// dfToolsRows is checkTools: one INFO row naming the missing recommended tools.
func dfToolsRows() []doctorRow {
	var missing []string
	for _, tool := range toolRows {
		if !detectTool(tool) {
			missing = append(missing, tool.id)
		}
	}
	if len(missing) == 0 {
		return []doctorRow{}
	}
	return []doctorRow{{"tools", "INFO", "tools", "missing recommended tools: " + strings.Join(missing, ", ") + " — run code-forge tools install"}}
}

// dfLedgerRow is checkLedger: it makes the ledger directory (0700) and
// checks it is writable.
func dfLedgerRow(home string) doctorRow {
	dir := filepath.Join(home, ".code-forge", "ledger")
	err := os.MkdirAll(dir, 0o700)
	if st, statErr := os.Stat(dir); err != nil && statErr == nil && !st.IsDir() {
		return doctorRow{"ledger", "FAIL", "ledger", "not writable (EEXIST)"} // mkdir -p on a file
	}
	if err == nil {
		err = dfAccessW(dir)
	}
	if err != nil {
		code := contextpack.Errno(err)
		if code == "" {
			code = "error"
		}
		return doctorRow{"ledger", "FAIL", "ledger", "not writable (" + code + ")"}
	}
	return doctorRow{"ledger", "OK", "ledger", "writable"}
}

// dfLocksRows is checkLocks(process.cwd()): bad lock files, then the worker
// registry entries of the main working tree that hold no valid worker.
func dfLocksRows(procCwd string) ([]doctorRow, handoverReason) {
	failed := func(err error) []doctorRow {
		code := ""
		var fe *filelock.Error
		if errors.As(err, &fe) {
			code = fe.Code
		} else {
			code = contextpack.Errno(err)
		}
		if code == "" {
			code = "error"
		}
		return []doctorRow{{"locks", "WARN", "locks", "could not list locks (" + code + ")"}}
	}
	paths, err := filelock.ListBadLocks()
	if err != nil {
		return failed(err), 0
	}
	rows := []doctorRow{}
	if len(paths) == 0 {
		rows = append(rows, doctorRow{"locks", "OK", "locks", "no unreadable or malformed locks"})
	}
	for _, p := range paths {
		rows = append(rows, doctorRow{"locks", "WARN", "locks", p})
	}
	repoRoot := procCwd
	// the main-root git children are recorded in the pid registry (B567)
	reviewWaitStarted = lrRegisterChild
	root, refusal, ok := reviewMainRoot(dfContext(), procCwd)
	reviewWaitStarted = nil
	if ok && refusal == nil && root != "" {
		repoRoot = root
	}
	// liveness does not matter for these rows; no process is probed
	entries, err := worker.Registry(repoRoot, false, worker.Probes{Alive: func(int) bool { return false }})
	if errors.Is(err, worker.ErrUndecided) {
		return nil, hDoctorWorkerRegistry
	}
	if err != nil {
		return failed(err), 0
	}
	for _, e := range entries {
		if e.Worker == nil {
			rows = append(rows, doctorRow{"worker-registry", "WARN", "worker registry", e.File})
		}
	}
	return rows, 0
}

// dfTmpBase is tmpBase(undefined) as Node sees it after the router's
// applyTmpRoot, which Go runs only after prepare.
func dfTmpBase() string {
	if os.Getenv("CODE_FORGE_TMP_ROOT") == "" {
		if cwd, err := os.Getwd(); err == nil {
			if loaded, err := config.Load(cwd); err == nil {
				if root, ok := blockMap(loaded["tmp"])["root"].(string); ok && root != "" {
					if !filepath.IsAbs(root) {
						root = filepath.Join(cwd, root)
					}
					return filepath.Clean(root)
				}
			}
		}
	}
	base, _ := runTmpBase(nil, false)
	return base
}

// dfTmpRows is checkTmp: stale run roots (dead owner) under the temp base
// and their registered pids. root/set is cfg.tmp.root when it is a string.
func dfTmpRows(root string, set bool, envBase string) []doctorRow {
	base := envBase
	if set {
		if !filepath.IsAbs(root) {
			return []doctorRow{{"tmp", "FAIL", "tmp", "tmp.root is not an absolute path"}}
		}
		base = root
	}
	entries, _ := os.ReadDir(base)
	roots, pids := 0, 0
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(base, e.Name())
		if runOwnerState(dir) != "dead" {
			continue
		}
		roots++
		pids += len(runListEntries(filepath.Join(dir, "pids")))
	}
	detail := fmt.Sprintf("%d stale roots, %d stale pids", roots, pids)
	if roots+pids > 0 {
		return []doctorRow{{"tmp", "WARN", "tmp", detail + " (the next code-forge run start removes them)"}}
	}
	return []doctorRow{{"tmp", "OK", "tmp", detail}}
}

// dfSweepRows is checkSweep with the CLI's sweepIdleWorkers overlay: Node's
// raw stale workers are a subset of the idle-aware ones, so the final count
// is the idle-aware scan's. Read-only: nothing is signalled.
func dfSweepRows(home, base string) ([]doctorRow, handoverReason) {
	report, reason := sweepScan(home, base)
	if reason == hSweepRoot {
		return []doctorRow{{"sweep", "WARN", "sweep", "process inspection unavailable"}, {"memory", "INFO", "memory", "process inspection unavailable"}}, 0
	}
	if report == nil {
		return nil, reason
	}
	var workers []string
	orphans, zombies := 0, 0
	for _, item := range report.items {
		switch item.kind {
		case "worker":
			workers = append(workers, fmt.Sprintf("pid %d run %s", item.p.pid, item.runID))
		case "orphan":
			orphans++
		}
	}
	var parents []string
	for _, g := range report.zombies {
		zombies += len(g.pids)
		parents = append(parents, fmt.Sprintf("parent %d %s", g.ppid, g.command))
	}
	detail := fmt.Sprintf("%d stale workers, %d orphans, %d zombies", len(workers), orphans, zombies)
	if len(parents) > 0 {
		detail += " (" + strings.Join(parents, "; ") + ")"
	}
	if len(workers) > 0 {
		detail += " (" + strings.Join(workers, ", ") + ")"
	}
	status := "OK"
	if len(workers)+orphans+zombies > 0 {
		status = "WARN"
	}
	memory := fmt.Sprintf("code-forge %s MB, children %s MB, orphans %s MB", jsToFixed1(report.forgeMb), jsToFixed1(report.childMb), jsToFixed1(report.orphanMb))
	return []doctorRow{{"sweep", status, "sweep", detail}, {"memory", "INFO", "memory", memory}}, 0
}

// dfReviewStateListed is LISTED_MAX.
const dfReviewStateListed = 10

// dfReviewStateRows is the review-state part of checkReviewState (B69): one
// WARN row per active run whose latest review.state anchors name missing
// state files. cfgRoot/cfgSet is cfg.tmp.root when it is a string; envBase is
// tmpBase(undefined). It also returns the run keys Node registers as secrets.
func dfReviewStateRows(home, cfgRoot string, cfgSet bool, envBase string) ([]doctorRow, []string, handoverReason) {
	rows := []doctorRow{}
	var secrets []string
	runs := filepath.Join(home, ".code-forge", "runs")
	list, err := os.ReadDir(runs)
	if err != nil {
		return rows, nil, 0
	}
	var names []string
	for _, e := range list {
		if strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Slice(names, func(i, j int) bool { return lessUTF16(names[i], names[j]) })
	ledgers := map[string][]ledger.Object{}
	ledgerDir := filepath.Join(home, ".code-forge", "ledger")
	for _, name := range names {
		runID := strings.TrimSuffix(name, ".json")
		if !dfRunID.MatchString(runID) {
			continue
		}
		record, err := state.ReadRun(home, runID)
		if err != nil {
			var se *state.Error
			if errors.As(err, &se) {
				continue
			}
			data, readErr := os.ReadFile(filepath.Join(runs, name))
			var parsed any
			if readErr == nil && json.Unmarshal(data, &parsed) == nil {
				if m, ok := parsed.(map[string]any); ok && m["status"] == "active" {
					if _, ok := m["project"].(string); ok {
						return nil, nil, hDoctorReviewStateRecord // JSON.parse reads what Go's decoder refused
					}
				}
			}
			continue
		}
		project, ok := record.Get("project").(string)
		if record.Get("status") != "active" || !ok {
			continue
		}
		key, err := dfLoadKey(home, runID)
		if err != nil {
			continue
		}
		secrets = append(secrets, hex.EncodeToString(key))
		rowsOf, cached := ledgers[project]
		if !cached {
			raw, err := ledger.ReadAllOrdered(ledgerDir, project)
			if err != nil {
				continue
			}
			rowsOf = []ledger.Object{}
			for _, r := range raw {
				if o, ok := r.(ledger.Object); ok {
					rowsOf = append(rowsOf, o)
				}
			}
			ledgers[project] = rowsOf
		}
		atNew, ok := worker.MissingStateFiles(home, runID, key, rowsOf, "")
		if !ok {
			rows = append(rows, doctorRow{"review-state", "WARN", "review state", "run " + runID + ": review.state anchors do not verify; review state not checked"})
			continue
		}
		root, rootSet := "", false
		if snap, _, valid := sweepConfigInForce(record); valid && snap != nil {
			if tmp, ok := snap.Get("tmp").(ledger.Object); ok {
				root, rootSet = tmp.Get("root").(string)
			}
		}
		if !rootSet && cfgSet {
			root, rootSet = cfgRoot, true
		}
		runRootDir := filepath.Join(envBase, runID)
		if rootSet {
			if !filepath.IsAbs(root) {
				if len(atNew) > 0 {
					rows = append(rows, doctorRow{"review-state", "WARN", "review state", "run " + runID + ": the old temp location could not be checked (tmp.root is not an absolute path); review state files are not counted"})
				}
				continue
			}
			runRootDir = filepath.Join(root, runID)
		}
		missing, ok := worker.MissingStateFiles(home, runID, key, rowsOf, runRootDir)
		if !ok || len(missing) == 0 {
			continue
		}
		rebuildable := 0
		var shown []string
		for i, m := range missing {
			word := "lost"
			if m.Rebuildable {
				rebuildable++
				word = "rebuildable"
			}
			if i < dfReviewStateListed {
				shown = append(shown, m.Block+" "+m.File+" ("+word+")")
			}
		}
		more := ""
		if len(missing) > dfReviewStateListed {
			more = fmt.Sprintf(" … and %d more", len(missing)-dfReviewStateListed)
		}
		rows = append(rows, doctorRow{"review-state", "WARN", "review state", fmt.Sprintf("run %s: %d state file(s) missing — %d rebuildable from the ledger, %d lost (needs code-forge block reopen): %s%s", runID, len(missing), rebuildable, len(missing)-rebuildable, strings.Join(shown, ", "), more)})
	}
	return rows, secrets, 0
}

var dfRunID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// dfLoadKey is signer.mjs loadKey: a 32-byte key file no other user can read.
func dfLoadKey(home, runID string) ([]byte, error) {
	file := filepath.Join(home, ".code-forge", "runs", runID+".key")
	st, err := os.Stat(file)
	if err != nil {
		return nil, err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("bad-key-mode")
	}
	key, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("bad-key")
	}
	return key, nil
}

// dfNativeArch is process.arch for this build.
func dfNativeArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	case "386":
		return "ia32"
	}
	return runtime.GOARCH
}

// dfNativeRow is the CLI's `native binary` row: Node runs the package's
// native binary with `internal version`. When that binary is this one, its
// answer is this build's version.
func dfNativeRow() (doctorRow, handoverReason) {
	missing := doctorRow{"native", "INFO", "native binary", "not found, using Node"}
	if os.Getenv("CODE_FORGE_NATIVE") == "0" {
		return missing, 0
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return doctorRow{}, hDoctorNativeBinary
	}
	pkg := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(exe))))
	binary := filepath.Join(pkg, "native", "bin", runtime.GOOS+"-"+dfNativeArch(), "code-forge-native")
	st, err := os.Stat(binary)
	if err != nil || !st.Mode().IsRegular() || dfAccessX(binary) != nil {
		return missing, 0
	}
	if real, err := filepath.EvalSymlinks(binary); err != nil || real != exe {
		return doctorRow{}, hDoctorNativeBinary // another binary would answer
	}
	return doctorRow{"native", "OK", "native binary", binary + ", version " + version}, 0
}

// dfDaemonRow is the CLI's `native daemon` row without a loadable config:
// with no live worker the config in force is {native: {daemon: false}}.
func dfDaemonRow(cwd string) (doctorRow, handoverReason) {
	off := doctorRow{"native_daemon", "INFO", "native daemon", "off"}
	w, err := worker.Live(config.ProjectRootFor(cwd), worker.LiveOpts{}, worker.Probes{})
	if errors.Is(err, worker.ErrUndecided) {
		return doctorRow{}, hDoctorWorkerRegistry
	}
	if err != nil || w == nil || os.Getenv("CODE_FORGE_NATIVE") == "0" {
		return off, 0 // an unreadable registry leaves native acceleration off
	}
	return doctorRow{}, hDoctorFullLiveWorker // the run's config may ping the daemon
}

// dfNoindex is isNoindexPath.
func dfNoindex(dir string) bool {
	for _, part := range strings.Split(dir, string(filepath.Separator)) {
		if strings.HasSuffix(part, ".noindex") {
			return true
		}
	}
	return false
}

// dfRealAsFarAsExists is worktree-paths.mjs realAsFarAsExists.
func dfRealAsFarAsExists(p string) string {
	var rest []string
	head := p
	for filepath.Dir(head) != head {
		if _, err := os.Lstat(head); err == nil {
			break
		}
		rest = append([]string{filepath.Base(head)}, rest...)
		head = filepath.Dir(head)
	}
	if real, err := filepath.EvalSymlinks(head); err == nil {
		head = real
	}
	return filepath.Join(append([]string{head}, rest...)...)
}

// dfWorktreeIndexingRow is the CLI's darwin `worktree indexing` WARN:
// worktreeDirectory for the project (dir nil when proof.worktree.dir is
// unset) and a row when that folder is not under a .noindex folder. ok is
// false when git cannot list the worktrees (Node skips the row).
func dfWorktreeIndexingRow(top string, dir *string, branch, runID string) ([]doctorRow, bool) {
	old := dfRealAsFarAsExists(filepath.Join(filepath.Dir(top), filepath.Base(top)+"-worktrees"))
	where := dfRealAsFarAsExists(filepath.Join(filepath.Dir(top), filepath.Base(top)+"-worktrees.noindex"))
	switch {
	case dir != nil:
		d := *dir
		if !filepath.IsAbs(d) {
			d = filepath.Join(top, d)
		}
		where = dfRealAsFarAsExists(filepath.Clean(d))
	case runID != "":
		cmd := exec.Command("git", "worktree", "list", "--porcelain", "-z")
		cmd.Dir = top
		cmd.Env = append(dfGitEnv(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
		out, err := cmd.Output()
		if err != nil {
			return nil, false
		}
		at := ""
		for _, line := range strings.Split(string(out), "\x00") {
			if strings.HasPrefix(line, "worktree ") {
				at = line[len("worktree "):]
			} else if strings.HasPrefix(line, "branch refs/heads/") && at != "" &&
				dfRealAsFarAsExists(filepath.Dir(at)) == old &&
				line[len("branch refs/heads/"):] == strings.NewReplacer("{run}", runID, "{block}", filepath.Base(at)).Replace(branch) {
				where = old
				break
			}
		}
	}
	if dfNoindex(where) {
		return []doctorRow{}, true
	}
	return []doctorRow{{"worktree-indexing", "WARN", "worktree indexing", where + " may be indexed by Spotlight.\nproof.worktree.dir: ../" + filepath.Base(top) + "-worktrees.noindex\nTo move the old folder, finish or end the runs, then mv the folder to the .noindex path; or remove it."}}, true
}

func dfGitEnv() []string {
	env := []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			env = append(env, entry)
		}
	}
	return env
}

// prepareDoctorNoConfig is the full run when .code-forge.yml cannot be
// loaded at cwd: every row is local, the signer line replaces the probes.
// Everything read-only runs here; the body makes the run root and the
// ledger directory, resolves the key, prints and removes the run root.
func prepareDoctorNoConfig(cwd, home string, rows []doctorRow, jsonOut bool) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
	now := doctorNow()
	rows = append(rows, doctorLinksRow(home))
	resolveKeys, reason := doctorKeysRow(nil, home, now)
	if reason != 0 {
		return nil, reason, false
	}
	keysAt := len(rows)
	rows = append(rows, doctorRow{})
	rows = append(rows, dfToolsRows()...)
	ledgerAt := len(rows)
	rows = append(rows, doctorRow{})
	// B567: the CLI makes and adopts its root before runDoctor, so the
	// locks check's git and an op child are recorded in its pids/; without
	// it the children go in the lazy root, released at exit. Every hand-over
	// from here removes the root and releases it, so it writes nothing.
	// The router hint and the tmp root are smallVerb.run's, before the root
	// (Node applies the tmp root at process start).
	if !routerHintReadable() {
		return nil, hRouterHintState, false
	}
	contextApplyTmpRoot()
	var bytes [3]byte
	_, _ = dfRandom(bytes[:])
	root, err := contextMakeRunRoot(fmt.Sprintf("doctor-%d-%s", os.Getpid(), hex.EncodeToString(bytes[:])), nil)
	lrRoot = ""
	if err == nil {
		lrRoot = root
	}
	release := func() {
		if err == nil {
			_ = os.RemoveAll(root)
			lrRoot = ""
		} else {
			toolsReleaseRoot()
		}
	}
	handover := func(reason handoverReason) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
		release()
		return nil, reason, false
	}
	procCwd := physicalCwd()
	locks, reason := dfLocksRows(procCwd)
	if reason != 0 {
		return handover(reason)
	}
	rows = append(rows, locks...)
	base := dfTmpBase()
	rows = append(rows, dfTmpRows("", false, base)...)
	sweep, reason := dfSweepRows(home, base)
	if reason != 0 {
		return handover(reason)
	}
	rows = append(rows, sweep...)
	review, keys, reason := dfReviewStateRows(home, "", false, base)
	if reason != 0 {
		return handover(reason)
	}
	rows = append(rows, review...)
	rows = append(rows, dfReviewConcurrencyRow(nil, config.Ordered{}))
	rows = append(rows, doctorRow{"probes", "FAIL", "probes", "skipped: no valid config"}, doctorRow{"signer", "INFO", "signer", "same-user boundary only"})
	native, reason := dfNativeRow()
	if reason != 0 {
		return handover(reason)
	}
	daemon, reason := dfDaemonRow(cwd)
	if reason != 0 {
		return handover(reason)
	}
	rows = append(rows, native, daemon)
	return func(stdout, stderr io.Writer) int {
		nativeReportedKind = ""
		defer release()
		keyRow, secrets := resolveKeys()
		rows[keysAt] = keyRow
		rows[ledgerAt] = dfLedgerRow(home)
		secrets = append(secrets, keys...)
		redactOne := func(text string) string { return redactSecret(text, secrets...) }
		var out string
		if jsonOut {
			out = redactOne(doctorJSON(rows)) + "\n"
		} else {
			var b strings.Builder
			for _, r := range rows {
				b.WriteString(redactOne(fmt.Sprintf("%-4s %s: %s", r.status, r.label, r.detail)) + "\n")
			}
			out = b.String()
		}
		fmt.Fprint(stdout, redactOne(out))
		c := doctorCounts(rows)
		fmt.Fprint(stderr, redactOne(fmt.Sprintf("doctor: %d ok, %d warn, %d fail\n", c["OK"], c["WARN"], c["FAIL"])))
		if c["FAIL"] > 0 {
			return 1
		}
		nativeReportedKind = ""
		return 0
	}, 0, true
}

func dfAccessW(p string) error { return dfAccess(p, 2) }
func dfAccessX(p string) error { return dfAccess(p, 1) }

func dfAccess(p string, mode uint32) error {
	if err := syscall.Access(p, mode); err != nil {
		return &os.PathError{Op: "access", Path: p, Err: err}
	}
	return nil
}

func dfContext() context.Context { return context.Background() }

func dfRandom(b []byte) (int, error) { return rand.Read(b) }
