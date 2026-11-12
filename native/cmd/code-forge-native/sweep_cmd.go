package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ricardov03/code-forge/native/childreg"
	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B486: `code-forge sweep [--apply] [--json]` in Go (src/cli/sweep.mjs with
// src/util/reaper.mjs sweepProcesses and its idle-worker wrapper). It reuses
// the B472 run sweep: the process table, ownership markers, start-time checks,
// signals and the dead-root sweep. Three cases keep a hand-over to Node, each
// decided before anything is written: HOME unset or relative (sweep-home), a
// trusted temp base that cannot be listed (Node throws there:
// sweep-root-unreadable) and an idle check Go cannot prove equal to Node's
// (a date Date.parse reads outside the ISO subset, a record Go's decoder
// refuses, a workspace that is not a string: sweep-idle-unknown).
func init() { routes["sweep"] = runSweepCmd }

const sweepUsage = "usage: code-forge sweep [--apply] [--json]\nDry run by default: list stale workers, dead-run registry children and marked orphans with RSS and total MB.\n--apply stops proven stale processes with SIGTERM, then SIGKILL after a grace period.\nZombies are reported by parent only; neither they nor their parents are signalled. Live engines have no memory limit.\n"

// sweepNowMs is Date.now() for the idle check (a test seam).
var sweepNowMs = func() float64 { return float64(time.Now().UnixNano()) / 1e6 }

type sweepItem struct {
	p      runProc
	kind   string
	runID  string
	hasRun bool
	action string
}

type sweepZombies struct {
	ppid    int
	command string
	pids    []int
}

type sweepReport struct {
	items     []*sweepItem
	roots     []string
	zombies   []*sweepZombies
	forgeMb   float64
	childMb   float64
	orphanMb  float64
	base      string
	totalRSS  float64
	scannedOK bool
}

func runSweepCmd(args []string) int {
	// The Node router answers help before the tmp root, breadcrumbs and handler.
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprint(os.Stdout, sweepUsage)
		return 0
	}
	home := os.Getenv("HOME")
	if home == "" || !filepath.IsAbs(home) {
		return sweepHandover(args, hSweepHome)
	}
	runApplyTmpRoot()
	apply, asJSON, help, parsed := sweepParseFlags(args)
	var report *sweepReport
	if parsed && !help {
		base, _ := runTmpBase(nil, false)
		var reason handoverReason
		if report, reason = sweepScan(home, base); report == nil {
			return sweepHandover(args, reason)
		}
	}
	before := gatesBreadcrumbName("sweep")
	if !parsed {
		fmt.Fprint(os.Stderr, sweepUsage)
		nativeVerbFailure("sweep", "", args, 2, strings.TrimSpace(sweepUsage), before)
		return 2
	}
	if help {
		fmt.Fprint(os.Stdout, sweepUsage)
		return 0
	}
	if apply {
		sweepApply(report)
	}
	if asJSON {
		data, _ := ledger.Stringify(sweepJSON(report, apply), false)
		fmt.Fprint(os.Stdout, string(data)+"\n")
		return 0
	}
	var out strings.Builder
	for _, item := range report.items {
		verb := "would stop"
		if apply {
			verb = item.action
			if verb == "" {
				verb = "stopped"
			}
		}
		fmt.Fprintf(&out, "%s %s pid %d %s MB %s\n", verb, item.kind, item.p.pid, jsToFixed1(item.p.rss/1024), item.p.command)
	}
	for _, group := range report.zombies {
		fmt.Fprintf(&out, "%d zombies (parent %d %s) — report only\n", len(group.pids), group.ppid, group.command)
	}
	label := "dry run"
	if apply {
		label = "sweep"
	}
	fmt.Fprintf(&out, "%s: %d processes, total %s MB; %d dead-run roots\n", label, len(report.items), jsToFixed1(report.totalRSS), len(report.roots))
	fmt.Fprint(os.Stdout, out.String())
	return 0
}

func sweepHandover(args []string, reason handoverReason) int {
	code, err := handover(append([]string{"sweep"}, args...), reason)
	if err != nil {
		fmt.Fprintln(os.Stderr, "code-forge-native:", err)
	}
	return code
}

// sweepParseFlags is parseFlags(args, {booleans: ['apply', 'json', 'help']})
// with no positional allowed; ok is false for any usage error.
func sweepParseFlags(args []string) (apply, asJSON, help, ok bool) {
	seen := map[string]bool{}
	for _, arg := range args {
		if !strings.HasPrefix(arg, "--") {
			return false, false, false, false
		}
		name, _, inline := strings.Cut(arg[2:], "=")
		if name != "apply" && name != "json" && name != "help" {
			return false, false, false, false
		}
		if seen[name] || inline {
			return false, false, false, false
		}
		seen[name] = true
	}
	return seen["apply"], seen["json"], seen["help"], true
}

var sweepForgeCommand = regexp.MustCompile(`^(?:\S*/)?node(?:js)?\s+(?:\S*/)?code-forge\.mjs(?:\s|$)`)

// sweepScan is sweepProcesses({apply}) wrapped by sweepIdleWorkers: workers of
// ended or idle runs, orphans whose owner died, zombies by parent, children
// registered under dead run roots, and the memory totals. A nil report hands
// over (nothing has been written).
func sweepScan(home, base string) (*sweepReport, handoverReason) {
	rows := runReadProcesses()
	uid := os.Getuid()
	own := []runProc{}
	for _, p := range rows {
		if p.uid == uid {
			own = append(own, p)
		}
	}
	blocked := runProtected(rows)
	r := &sweepReport{items: []*sweepItem{}, roots: []string{}, zombies: []*sweepZombies{}, base: base}
	for _, p := range own {
		if !strings.HasPrefix(p.state, "Z") {
			continue
		}
		var group *sweepZombies
		for _, g := range r.zombies {
			if g.ppid == p.ppid {
				group = g
			}
		}
		if group == nil {
			command := "(unreadable)"
			for _, row := range rows {
				if row.pid == p.ppid {
					command = row.command
					break
				}
			}
			group = &sweepZombies{ppid: p.ppid, command: command}
			r.zombies = append(r.zombies, group)
		}
		group.pids = append(group.pids, p.pid)
	}
	listed := func(pid int) bool {
		for _, item := range r.items {
			if item.p.pid == pid {
				return true
			}
		}
		return false
	}
	for _, p := range own {
		if p.pid <= 1 || blocked[p.pid] {
			continue
		}
		if m := runWorkerCommand.FindStringSubmatch(p.command); m != nil {
			env := runReadEnv(p)
			if env == nil || env["HOME"] != home {
				continue
			}
			stale, reason := sweepWorkerStale(home, m[1])
			if reason != 0 {
				return nil, reason
			}
			if stale {
				r.items = append(r.items, &sweepItem{p: p, kind: "worker", runID: m[1], hasRun: true})
			}
			continue
		}
		if p.ppid != 1 || listed(p.pid) {
			continue
		}
		env := runReadEnv(p)
		owner := env["CODE_FORGE_OWNER_PID"]
		if env == nil || !runOwnerPid.MatchString(owner) {
			continue
		}
		ownerPid, err := strconv.ParseFloat(owner, 64)
		if err != nil || ownerPid <= 1 {
			continue
		}
		var live *runProc
		for i := range rows {
			if float64(rows[i].pid) == ownerPid {
				live = &rows[i]
				break
			}
		}
		recorded := env["CODE_FORGE_OWNER_START_TIME"]
		dead := false
		if live == nil {
			dead = !runAlive(ownerPid)
		} else {
			dead = strings.HasPrefix(live.state, "Z") || (recorded != "" && recorded != "unknown" && live.start != recorded)
		}
		if dead {
			runID := env["CODE_FORGE_RUN_ID"]
			r.items = append(r.items, &sweepItem{p: p, kind: "orphan", runID: runID, hasRun: runID != ""})
		}
	}
	if !runUntrusted(base) {
		entries, err := os.ReadDir(base)
		if err != nil {
			return nil, hSweepRoot
		}
		for _, entry := range entries {
			full := filepath.Join(base, entry.Name())
			if !entry.IsDir() || runUntrusted(full) {
				continue
			}
			owner := runOwnerState(full)
			if owner != "dead" && !(owner == "missing" && runAllOld(full)) {
				continue
			}
			r.roots = append(r.roots, full)
			for _, e := range runListEntries(filepath.Join(full, "pids")) {
				find := func(pid int, start string) *runProc {
					for i := range own {
						if own[i].pid == pid && own[i].start == start {
							return &own[i]
						}
					}
					return nil
				}
				p := find(e.pid, e.start)
				if p == nil || blocked[p.pid] {
					continue
				}
				members := append([]runMember{{p.pid, p.start}}, runGroupMembers(p.pid)...)
				for _, member := range members {
					m := find(member.pid, member.start)
					if m != nil && !blocked[member.pid] && !listed(member.pid) {
						r.items = append(r.items, &sweepItem{p: *m, kind: "registry"})
					}
				}
			}
		}
	}
	forge := map[int]bool{}
	for _, p := range own {
		if sweepForgeCommand.MatchString(p.command) || p.pid == os.Getpid() {
			forge[p.pid] = true
		}
	}
	tree := map[int]bool{}
	for pid := range forge {
		tree[pid] = true
	}
	for size := -1; size != len(tree); {
		size = len(tree)
		for _, p := range own {
			if tree[p.ppid] {
				tree[p.pid] = true
			}
		}
	}
	for _, p := range own {
		if forge[p.pid] {
			r.forgeMb += p.rss / 1024
		}
	}
	for _, p := range own {
		if tree[p.pid] && !forge[p.pid] {
			r.childMb += p.rss / 1024
		}
	}
	for _, item := range r.items {
		if item.kind == "orphan" {
			r.orphanMb += item.p.rss / 1024
		}
	}
	for _, item := range r.items {
		r.totalRSS += item.p.rss / 1024
	}
	return r, 0
}

// sweepApply is sweepProcesses' apply: each worker and orphan on its own
// (SIGTERM, grace, SIGKILL), then sweepRoots, then each registry child is
// reported stopped when it is gone or its pid now names another process.
func sweepApply(r *sweepReport) {
	for _, item := range r.items {
		if item.kind == "registry" {
			continue
		}
		item.action = "skipped"
		if runStopMember(runMember{item.p.pid, item.p.start}) {
			item.action = "stopped"
		}
	}
	runSweepRoots(r.base)
	for _, item := range r.items {
		if item.kind != "registry" {
			continue
		}
		started := runStartTime(float64(item.p.pid))
		item.action = "skipped"
		if !runAlive(float64(item.p.pid)) || (started != nil && *started != item.p.start) {
			item.action = "stopped"
		}
	}
}

func sweepJSON(r *sweepReport, apply bool) ledger.Object {
	items := []any{}
	for _, item := range r.items {
		o := ledger.Object{
			{Key: "uid", Value: float64(item.p.uid)}, {Key: "pid", Value: float64(item.p.pid)},
			{Key: "ppid", Value: float64(item.p.ppid)}, {Key: "state", Value: item.p.state},
			{Key: "rssMb", Value: item.p.rss / 1024}, {Key: "start_time", Value: item.p.start},
			{Key: "command", Value: item.p.command}, {Key: "kind", Value: item.kind},
		}
		if item.hasRun {
			o = append(o, ledger.Field{Key: "runId", Value: item.runID})
		}
		if item.action != "" {
			o = append(o, ledger.Field{Key: "action", Value: item.action})
		}
		items = append(items, o)
	}
	roots := []any{}
	for _, root := range r.roots {
		roots = append(roots, root)
	}
	zombies := []any{}
	for _, g := range r.zombies {
		pids := []any{}
		for _, pid := range g.pids {
			pids = append(pids, float64(pid))
		}
		zombies = append(zombies, ledger.Object{
			{Key: "ppid", Value: float64(g.ppid)}, {Key: "command", Value: g.command},
			{Key: "count", Value: float64(len(g.pids))}, {Key: "pids", Value: pids},
		})
	}
	return ledger.Object{
		{Key: "apply", Value: apply}, {Key: "items", Value: items},
		{Key: "registryRoots", Value: roots}, {Key: "zombies", Value: zombies},
		{Key: "totalRssMb", Value: r.totalRSS},
		{Key: "memory", Value: ledger.Object{
			{Key: "codeForgeMb", Value: r.forgeMb}, {Key: "childrenMb", Value: r.childMb},
			{Key: "orphansMb", Value: r.orphanMb},
		}},
	}
}

// jsToFixed1 is Number.prototype.toFixed(1) for a finite value: the exact
// binary value rounded half away from zero (Go's 'f' rounds a tie to even).
func jsToFixed1(x float64) string {
	if math.IsNaN(x) {
		return "NaN"
	}
	if math.Abs(x) >= 1e21 || math.IsInf(x, 0) {
		return strconv.FormatFloat(x, 'g', -1, 64)
	}
	exact := new(big.Float).SetPrec(0).SetFloat64(x).Text('f', 1100)
	neg := strings.HasPrefix(exact, "-")
	exact = strings.TrimPrefix(exact, "-")
	dot := strings.IndexByte(exact, '.')
	head := exact[:dot+2] // integer part, '.', one digit
	if exact[dot+2] >= '5' {
		digits := []byte(strings.Replace(head, ".", "", 1))
		i := len(digits) - 1
		for ; i >= 0 && digits[i] == '9'; i-- {
			digits[i] = '0'
		}
		if i < 0 {
			digits = append([]byte{'1'}, digits...)
		} else {
			digits[i]++
		}
		head = string(digits[:len(digits)-1]) + "." + string(digits[len(digits)-1])
	}
	if neg {
		return "-" + head
	}
	return head
}

// --- the idle-worker wrapper (sweepIdleWorkers / runActivityAt) ---

// sweepWorkerStale is the worker branch of sweepProcesses behind
// sweepIdleWorkers' readRun: a missing record, an ended run, or an
// open/running/active run idle longer than worker.idle_exit_hours.
func sweepWorkerStale(home, runID string) (bool, handoverReason) {
	record, err := state.ReadRun(home, runID)
	if err != nil {
		var stateErr *state.Error
		if errors.As(err, &stateErr) {
			return stateErr.Code == "no-run", 0
		}
		data, readErr := os.ReadFile(filepath.Join(home, ".code-forge", "runs", runID+".json"))
		var parsed map[string]any
		if readErr != nil || json.Unmarshal(data, &parsed) != nil || parsed == nil {
			return false, 0
		}
		status, ok := parsed["status"].(string)
		if ok && (status == "open" || status == "running" || status == "active") {
			return false, hSweepIdle // the idle check needs a record Go's decoder refused
		}
		return ok, 0
	}
	status, ok := record.Get("status").(string)
	if !ok {
		return false, 0
	}
	if status != "open" && status != "running" && status != "active" {
		return true, 0
	}
	return sweepIdle(home, record)
}

// sweepIdle: configInForce (or the workspace config), idleExitHours, then
// Date.now() - runActivityAt(record) > hours. Anything that throws in Node
// keeps the worker (not idle).
func sweepIdle(home string, record ledger.Object) (bool, handoverReason) {
	cfg, snapshot, valid := sweepConfigInForce(record)
	if !valid {
		return false, 0
	}
	if !snapshot {
		workspace, ok := record.Get("workspace").(string)
		if !ok {
			return false, hSweepIdle
		}
		snap, err := config.LoadSnapshot(workspace)
		if err != nil {
			return false, 0
		}
		cfg = snap.Ordered
	}
	hours := 24.0
	if worker, ok := cfg.Get("worker").(ledger.Object); ok {
		if h, ok := worker.Get("idle_exit_hours").(float64); ok && !math.IsInf(h, 0) && !math.IsNaN(h) && h >= 0 {
			hours = h
		}
	}
	if !(hours > 0) {
		return false, 0
	}
	now := sweepNowMs()
	latest, ok, reason := sweepActivityAt(home, record)
	if reason != 0 {
		return false, reason
	}
	return ok && now-latest > hours*3600000, 0
}

// sweepConfigInForce is configInForce(record) with snapshotFor's pin under the
// empty ticket key: snapshot false for a record from before snapshots; valid
// false when the snapshot in force does not match its hash (Node throws).
func sweepConfigInForce(record ledger.Object) (cfg ledger.Object, snapshot, valid bool) {
	st, ok := record.Get("config").(ledger.Object)
	if !ok {
		return nil, false, true
	}
	hash, ok := st.Get("hash").(string)
	if !ok {
		return nil, false, true
	}
	if pins, ok := st.Get("pins").(ledger.Object); ok && runHas(pins, "") {
		if pin, ok := pins.Get("").(string); ok {
			hash = pin
		}
	}
	snaps, _ := st.Get("snapshots").(ledger.Object)
	cfg, isMapping := snaps.Get(hash).(ledger.Object)
	if !isMapping || !runHas(snaps, hash) || config.Hash(runDeepMap(cfg)) != hash {
		return nil, true, false
	}
	return cfg, true, true
}

// sweepStrictEqual is === between two decoded JSON values (absent is undefined).
func sweepStrictEqual(a any, aHas bool, b any, bHas bool) bool {
	if !aHas || !bHas {
		return aHas == bHas
	}
	switch x := a.(type) {
	case nil:
		return b == nil
	case string:
		y, ok := b.(string)
		return ok && x == y
	case float64:
		y, ok := b.(float64)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	}
	return false // objects and arrays from two parses are never the same reference
}

func sweepField(v any, key string) (any, bool, bool) {
	switch x := v.(type) {
	case nil:
		return nil, false, false // property read of null throws
	case map[string]any:
		value, has := x[key]
		return value, has, true
	case ledger.Object:
		return x.Get(key), runHas(x, key), true
	}
	return nil, false, true
}

func sweepMs(t time.Time) float64 { return float64(t.Unix())*1000 + float64(t.Nanosecond())/1e6 }

func sweepMissing(err error) bool {
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ENOTDIR)
}

var sweepTicketName = regexp.MustCompile(`^[a-f0-9]{24}\.json$`)

// sweepGit is runActivityAt's exec: git without GIT_* from the environment,
// with no system or global config, a 10 s timeout and exec's 10 MiB cap.
func sweepGit(dir string, args ...string) (string, bool) {
	return sweepGitHooked(dir, nil, args...)
}

// sweepGitHooked is sweepGit with exec.mjs's pid entry: started (when not
// nil) records the child and returns what clears it (B567).
func sweepGitHooked(dir string, started func(pid int, argv0 string) func(), args ...string) (string, bool) {
	env := []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			env = append(env, entry)
		}
	}
	env = append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = childreg.ChildEnv(env)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Start()
	if err == nil {
		settled := childreg.Begin(started, cmd.Process.Pid, "git") // else the CLI's registry (B573)
		err = cmd.Wait()
		settled()
	}
	if err != nil || out.Len() > 10*1024*1024 {
		return "", false
	}
	return out.String(), true
}

// sweepActivityAt is runActivityAt(record) without deps: ok is false where
// Node throws or returns now (either way the worker is not idle).
func sweepActivityAt(home string, record ledger.Object) (latest float64, ok bool, reason handoverReason) {
	workspace, isString := record.Get("workspace").(string)
	if !isString {
		return 0, false, hSweepIdle // exec would run git in the current directory
	}
	top, good := sweepGit(workspace, "rev-parse", "--show-toplevel")
	root := strings.TrimSpace(top)
	if !good || root == "" {
		return 0, false, 0
	}
	runID, runHasID := record.Get("run_id"), runHas(record, "run_id")
	queue := filepath.Join(root, ".code-forge", "queue")
	entries, err := os.ReadDir(queue)
	if err != nil && !sweepMissing(err) {
		return 0, false, 0
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name()] = true
	}
	for _, e := range entries {
		name := e.Name()
		if !sweepTicketName.MatchString(name) || names[name[:24]+".done"] {
			continue
		}
		data, err := os.ReadFile(filepath.Join(queue, name))
		var ticket any
		if err != nil || json.Unmarshal(data, &ticket) != nil {
			return 0, false, 0
		}
		run, has, readable := sweepField(ticket, "run")
		if !readable || sweepStrictEqual(run, has, runID, runHasID) {
			return 0, false, 0 // a throw, or a pending ticket of this run: active now
		}
	}
	started, startedHas := record.Get("started_at"), runHas(record, "started_at")
	if obj, isObj := started.(ledger.Object); isObj {
		started = obj.Map()
	}
	at, parsed, known := gatecheck.JSDateParse(started, startedHas)
	if !known {
		return 0, false, hSweepIdle
	}
	if !parsed {
		return 0, false, 0
	}
	latest = at
	slug, isSlug := record.Get("project").(string)
	if !isSlug || !sweepSlug.MatchString(slug) {
		return 0, false, 0
	}
	rows, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge", "ledger"), slug)
	if err != nil {
		return 0, false, 0
	}
	now := sweepNowMs()
	for _, row := range rows {
		run, has, readable := sweepField(row, "run")
		if !readable {
			return 0, false, 0
		}
		if !sweepStrictEqual(run, has, runID, runHasID) {
			continue
		}
		ts, tsHas, _ := sweepField(row, "ts")
		at, parsed, known := gatecheck.JSDateParse(ts, tsHas)
		if !known {
			return 0, false, hSweepIdle
		}
		if !parsed {
			at = now
		}
		latest = math.Max(latest, at)
	}
	var blocks []any
	switch b := record.Get("blocks").(type) {
	case ledger.Object:
		for _, f := range b {
			blocks = append(blocks, f.Value)
		}
	case []any:
		blocks = b
	}
	for _, entry := range blocks {
		status, _, readable := sweepField(entry, "status")
		if !readable {
			return 0, false, 0
		}
		if status != "open" {
			continue
		}
		worktree, _, _ := sweepField(entry, "worktree")
		tree := root
		if wt, isObj := worktree.(ledger.Object); isObj && wt.Get("path") != nil {
			path, isPath := wt.Get("path").(string)
			if !isPath {
				return 0, false, hSweepIdle
			}
			tree = path
		}
		info, err := os.Stat(tree)
		if err != nil {
			if runTruthy(worktree) && sweepMissing(err) {
				continue
			}
			return 0, false, 0
		}
		latest = math.Max(latest, sweepMs(info.ModTime()))
		listedOut, okList := sweepGit(tree, "ls-files", "--cached", "--others", "--exclude-standard", "-z", "--", ".", ":!.code-forge")
		head, okHead := sweepGit(tree, "log", "-1", "--format=%H:%ct")
		if !okList || !okHead {
			return 0, false, 0
		}
		ct := 0.0
		if parts := strings.Split(strings.TrimSpace(head), ":"); len(parts) > 1 {
			if n, err := strconv.ParseFloat(parts[1], 64); err == nil && sweepDigits.MatchString(parts[1]) {
				ct = n * 1000
			}
		}
		latest = math.Max(latest, ct)
		dirs := []string{tree}
		seen := map[string]bool{tree: true}
		for _, file := range strings.Split(listedOut, "\x00") {
			if file == "" {
				continue
			}
			full := filepath.Join(tree, file)
			for parent := filepath.Dir(full); parent != tree && strings.HasPrefix(parent, tree+"/"); parent = filepath.Dir(parent) {
				if !seen[parent] {
					seen[parent] = true
					dirs = append(dirs, parent)
				}
			}
			stat, err := os.Stat(full)
			if err != nil {
				if !sweepMissing(err) {
					return 0, false, 0
				}
				continue
			}
			latest = math.Max(latest, math.Max(sweepMs(stat.ModTime()), sweepCtimeMs(stat)))
		}
		for _, dir := range dirs {
			stat, err := os.Stat(dir)
			if err != nil {
				if !sweepMissing(err) {
					return 0, false, 0
				}
				continue
			}
			latest = math.Max(latest, sweepMs(stat.ModTime()))
		}
	}
	return latest, true, 0
}

var (
	sweepSlug   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	sweepDigits = regexp.MustCompile(`^\d+$`)
)
