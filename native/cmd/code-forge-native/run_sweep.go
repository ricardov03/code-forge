package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ricardov03/code-forge/native/procguard"
	"github.com/ricardov03/code-forge/native/state"
	"github.com/ricardov03/code-forge/native/worker"
)

// B468: the process sweep a plain `run start` runs first (src/util/reaper.mjs
// sweepProcesses({apply: true, root, zombies: false}) and tmp.mjs sweepRoots).
// B472: Go also stops what Node stops (a finished run's worker, an orphan
// whose owner died, a registered child of a dead run root) with Node's checks:
// SIGTERM, a grace, SIGKILL, each signal after a fresh start-time match.

type runProc struct {
	uid, pid, ppid int
	state, start   string
	command        string
	rss            float64 // KB, as ps prints it (B486: sweep's rssMb is rss/1024)
}

var runPsLine = regexp.MustCompile(`^\s*(\d+)\s+(\d+)\s+(\d+)\s+(\S+)\s+(\d+)\s+(\S+\s+\S+\s+\d+\s+\d+:\d+:\d+\s+\d{4})\s+(.+?)\s*$`)

func runPsEnv() []string {
	env := []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "LC_ALL=") && !strings.HasPrefix(entry, "LANG=") {
			env = append(env, entry)
		}
	}
	return append(env, "LC_ALL=C", "LANG=C")
}

func runPsBinary() string {
	if _, err := os.Stat("/bin/ps"); err == nil {
		return "/bin/ps"
	}
	return "ps"
}

func runPsOutput(args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, runPsBinary(), args...)
	cmd.Env = runPsEnv()
	out, err := cmd.Output()
	return string(out), err == nil
}

// Seams for tests: the process table and a process's ownership markers.
var (
	runReadProcesses = func() []runProc {
		out, ok := runPsOutput("-A", "-ww", "-o", "uid=,pid=,ppid=,stat=,rss=,lstart=,command=")
		if !ok {
			return nil
		}
		return runParseProcesses(out)
	}
	// runReadEnv reads one process's markers. nil (the default) reads them
	// for every candidate at once (runProcessEnvs: one ps -E call on darwin,
	// B748); a test that sets it is asked per process.
	runReadEnv func(runProc) map[string]string
	// runSweepScoped (B754): run start's sweep looks only at processes this
	// binary may signal; true only in a binary built for a test.
	runSweepScoped = procguard.TestBuild
)

func runParseProcesses(text string) []runProc {
	rows := []runProc{}
	for _, line := range strings.Split(text, "\n") {
		m := runPsLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		uid, _ := strconv.Atoi(m[1])
		pid, _ := strconv.Atoi(m[2])
		ppid, _ := strconv.Atoi(m[3])
		rss, _ := strconv.ParseFloat(m[5], 64)
		rows = append(rows, runProc{uid, pid, ppid, m[4], strings.Join(strings.Fields(m[6]), " "), m[7], rss})
	}
	return rows
}

// runProtected is protectedPids: 1, this process, its ancestors and zombies.
func runProtected(rows []runProc) map[int]bool {
	blocked := map[int]bool{1: true, os.Getpid(): true, os.Getppid(): true}
	byPid := map[int]runProc{}
	for _, p := range rows {
		if _, seen := byPid[p.pid]; !seen {
			byPid[p.pid] = p
		}
	}
	visited := map[int]bool{}
	for pid := os.Getppid(); pid > 1 && !visited[pid]; {
		visited[pid] = true
		blocked[pid] = true
		if p, ok := byPid[pid]; ok {
			pid = p.ppid
		} else {
			pid = 1
		}
	}
	for _, p := range rows {
		if strings.HasPrefix(p.state, "Z") {
			blocked[p.pid] = true
		}
	}
	return blocked
}

// runPsEnvHead is the head of one `ps -E` row (B757: worker.PsEnvHead).
var runPsEnvHead = worker.PsEnvHead

// runProcessEnv is readProcessEnv: only the ownership markers, nil when unreadable.
func runProcessEnv(p runProc) map[string]string {
	switch runtime.GOOS {
	case "linux":
		data, err := os.ReadFile("/proc/" + strconv.Itoa(p.pid) + "/environ")
		if err != nil {
			return nil
		}
		return worker.EnvFromEnviron(string(data))
	case "darwin":
		out, ok := runPsOutput("-E", "-ww", "-o", "pid=,ppid=,lstart=,command=", "-p", strconv.Itoa(p.pid))
		if !ok {
			return nil
		}
		return runEnvFromPs(p, out)
	default:
		return nil
	}
}

// runEnvFromPs reads the markers from `ps -E` output for p alone: the row
// must still be p (pid, ppid, start time, command) as in readProcessEnv.
// B757: the worker's reader (one parser for both).
func runEnvFromPs(p runProc, out string) map[string]string {
	return worker.EnvFromPs(p.pid, p.ppid, p.start, p.command, out)
}

// runPsEnvCall is the batched `ps -E` call (a seam for tests).
var runPsEnvCall = func(pids []string) (string, bool) {
	return runPsOutput("-E", "-ww", "-o", "pid=,ppid=,lstart=,command=", "-p", strings.Join(pids, ","))
}

// runProcessEnvs is runProcessEnv for many processes (B748): on darwin one
// `ps -E` call for all of them instead of one per process, cut into each
// process's own output. A missing row is a process that has gone (nil, as a
// per-process call reads). When the call fails, or a row cannot be told apart
// (a candidate's header seen twice), every process is read on its own.
func runProcessEnvs(cands []runProc) map[runProc]map[string]string {
	envs := map[runProc]map[string]string{}
	if len(cands) == 0 {
		return envs
	}
	perProcess := func() map[runProc]map[string]string {
		for _, p := range cands {
			envs[p] = runProcessEnv(p)
		}
		return envs
	}
	if runtime.GOOS != "darwin" {
		return perProcess()
	}
	want := map[string]bool{}
	pids := []string{}
	for _, p := range cands {
		id := strconv.Itoa(p.pid)
		if !want[id] {
			want[id] = true
			pids = append(pids, id)
		}
	}
	out, ok := runPsEnvCall(pids)
	if !ok {
		return perProcess()
	}
	blocks := map[string]string{}
	cur, start := "", -1
	for at := 0; at < len(out); {
		next := len(out)
		if end := strings.IndexByte(out[at:], '\n'); end >= 0 {
			next = at + end + 1
		}
		if m := runPsEnvHead.FindStringSubmatch(out[at:next]); m != nil && want[m[1]] {
			if start >= 0 {
				blocks[cur] = out[start:at]
			}
			if _, seen := blocks[m[1]]; seen {
				return perProcess()
			}
			cur, start = m[1], at
		}
		at = next
	}
	if start >= 0 {
		blocks[cur] = out[start:]
	}
	for _, p := range cands {
		if text, found := blocks[strconv.Itoa(p.pid)]; found {
			envs[p] = runEnvFromPs(p, text)
		} else {
			envs[p] = nil
		}
	}
	return envs
}

// runEnvReader returns the marker reader for cands: the test seam when set,
// else the markers read for all candidates at once.
func runEnvReader(cands []runProc) func(runProc) map[string]string {
	if runReadEnv != nil {
		return runReadEnv
	}
	envs := runProcessEnvs(cands)
	return func(p runProc) map[string]string {
		if env, ok := envs[p]; ok {
			return env
		}
		return runProcessEnv(p)
	}
}

// runEnvCandidates: the processes the worker/orphan search reads markers of
// (a worker command, or a child of pid 1), in table order.
func runEnvCandidates(own []runProc, blocked map[int]bool) []runProc {
	cands := []runProc{}
	for _, p := range own {
		if p.pid <= 1 || blocked[p.pid] {
			continue
		}
		if runWorkerCommand.MatchString(p.command) || p.ppid == 1 {
			cands = append(cands, p)
		}
	}
	return cands
}

func runJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

func runAlive(pid float64) bool {
	if pid <= 1 || pid != math.Trunc(pid) || pid > math.MaxInt32 {
		return false
	}
	err := syscall.Kill(int(pid), 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func runStartTime(pid float64) *string {
	if pid <= 1 || pid != math.Trunc(pid) {
		return nil
	}
	return runProbe(pid)
}

var (
	// Node's worker, and (B511) the Go worker run start launches:
	// `<dir>/code-forge-native worker --run <id>`.
	runWorkerCommand = regexp.MustCompile(`^(?:(?:\S*/)?node(?:js)?\s+(?:\S*/)?code-forge\.mjs|(?:\S*/)?code-forge-native)\s+worker\s+--run(?:=|\s+)([a-z0-9][a-z0-9-]{0,63})(?:\s|$)`)
	runOwnerPid      = regexp.MustCompile(`^[1-9]\d*$`)
)

// runSweepRootUnreadable: sweepProcesses reads the trusted temp base with
// readdirSync, which throws in Node (its error, its message): that start
// stays with Node, decided before anything is written.
func runSweepRootUnreadable(root string) bool {
	if runUntrusted(root) {
		return false
	}
	_, err := os.ReadDir(root)
	return err != nil
}

// runSweepItems is sweepProcesses' worker and orphan search: a worker of a
// run that has ended (or has no record) and an orphan whose owner died, in
// the process table's order. Registry children are not listed: apply only
// reports them; sweepRoots' sweep is what stops them (runSweepPids).
func runSweepItems(home string) []runProc {
	rows := runReadProcesses()
	uid := os.Getuid()
	// B754: a binary built for a test can signal only the test's own
	// descendants (procguard), so it reads no other process's environment:
	// its sweep no longer depends on how many processes the machine runs.
	scoped := runSweepScoped()
	ppids := map[int]int{}
	if scoped {
		for _, p := range rows {
			if _, seen := ppids[p.pid]; !seen {
				ppids[p.pid] = p.ppid
			}
		}
	}
	own := []runProc{}
	for _, p := range rows {
		if p.uid == uid && (!scoped || procguard.MaySignal(ppids, p.pid)) {
			own = append(own, p)
		}
	}
	blocked := runProtected(rows)
	readEnv := runEnvReader(runEnvCandidates(own, blocked))
	items := []runProc{}
	listed := func(pid int) bool {
		for _, item := range items {
			if item.pid == pid {
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
			env := readEnv(p)
			if env == nil || env["HOME"] != home {
				continue
			}
			if runWorkerRunEnded(home, m[1]) {
				items = append(items, p)
			}
			continue
		}
		if p.ppid != 1 || listed(p.pid) {
			continue
		}
		env := readEnv(p)
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
			items = append(items, p)
		}
	}
	return items
}

// runWorkerRunEnded: no run record, or a record whose status is a string
// other than open, running or active. A record that cannot be read or parsed
// (or is not an object) authorizes nothing, as in Node's catch.
func runWorkerRunEnded(home, runID string) bool {
	record, err := state.ReadRun(home, runID)
	if err != nil {
		var stateErr *state.Error
		if errors.As(err, &stateErr) {
			return stateErr.Code == "no-run"
		}
		// Go's decoder refused it; read it the way JSON.parse would
		data, readErr := os.ReadFile(filepath.Join(home, ".code-forge", "runs", runID+".json"))
		var parsed map[string]any
		if readErr != nil || json.Unmarshal(data, &parsed) != nil || parsed == nil {
			return false
		}
		status, ok := parsed["status"].(string)
		return ok && status != "open" && status != "running" && status != "active"
	}
	status, ok := record.Get("status").(string)
	return ok && status != "open" && status != "running" && status != "active"
}

// Seams for tests: signals, process groups, the clock and the grace between
// SIGTERM and SIGKILL (reaper.mjs DEFAULT_GRACE_MS, POLL_MS).
var (
	runSignal        = procguard.Kill
	runGroupMembers  = runPsGroupMembers
	runSweepNow      = time.Now
	runSweepSleep    = time.Sleep
	runSweepGrace    = 2000 * time.Millisecond
	runSweepPoll     = 25 * time.Millisecond
	runPsGroupLine   = regexp.MustCompile(`^\s*(\d+)\s+(\d+)\s+(.+?)\s*$`)
	runSweepSpaceRun = regexp.MustCompile(`\s+`)
)

type runMember struct {
	pid   int
	start string
}

// runPsGroupMembers is groupMembers: every OTHER member of group pgid (not
// this process) with its start time, in ps order; empty when ps fails.
func runPsGroupMembers(pgid int) []runMember {
	out, ok := runPsOutput("-A", "-o", "pid=,pgid=,lstart=")
	members := []runMember{}
	if !ok {
		return members
	}
	for _, line := range strings.Split(out, "\n") {
		m := runPsGroupLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		pid, _ := strconv.Atoi(m[1])
		group, _ := strconv.Atoi(m[2])
		if group == pgid && pid != pgid && pid != os.Getpid() {
			members = append(members, runMember{pid, runSweepSpaceRun.ReplaceAllString(m[3], " ")})
		}
	}
	return members
}

// runSignalMember is signalMember: a fresh process-table check (our uid, not
// protected, same start time) right before a bare-pid signal.
func runSignalMember(pid int, started string, sig syscall.Signal) bool {
	if started == "" || started == "unknown" {
		return false
	}
	rows := runReadProcesses()
	var p *runProc
	for i := range rows {
		if rows[i].pid == pid {
			p = &rows[i]
			break
		}
	}
	if p == nil || p.uid != os.Getuid() || runProtected(rows)[pid] || p.start != started {
		return false
	}
	return runSignal(pid, sig) == nil
}

// runStillRunning: any sent pid still in the table, not a zombie, same start.
func runStillRunning(sent []runMember) bool {
	live := runReadProcesses()
	for _, s := range sent {
		for _, p := range live {
			if p.pid == s.pid && !strings.HasPrefix(p.state, "Z") && p.start == s.start {
				return true
			}
		}
	}
	return false
}

// runStopItems is sweepProcesses' apply for workers and orphans: each item is
// stopMembers(new Map([[pid, start]])) on its own: SIGTERM, a grace for it to
// exit, SIGKILL if it did not, then a wait for SIGKILL to take effect.
func runStopItems(items []runProc) {
	for _, item := range items {
		runStopMember(runMember{item.pid, item.start})
	}
}

// runStopMember reports whether SIGTERM was sent (stopMembers' sent.has(pid)).
func runStopMember(m runMember) bool {
	if !runSignalMember(m.pid, m.start, syscall.SIGTERM) {
		return false
	}
	sent := []runMember{m}
	deadline := runSweepNow().Add(runSweepGrace)
	for runSweepNow().Before(deadline) {
		if !runStillRunning(sent) {
			break
		}
		runSweepSleep(runSweepPoll)
	}
	runSignalMember(m.pid, m.start, syscall.SIGKILL)
	wait := runSweepGrace
	if wait < time.Second {
		wait = time.Second
	}
	killed := runSweepNow().Add(wait)
	for runSweepNow().Before(killed) {
		runSweepSleep(runSweepPoll)
		if !runStillRunning(sent) {
			break
		}
	}
	return true
}

// runSignalIfOurs is reaper.mjs signalIfOurs: the group gets the signal only
// when every member is ours and the leader's live start time matches.
func runSignalIfOurs(pid int, recorded string, sig syscall.Signal) string {
	rows := runReadProcesses()
	blocked := runProtected(rows)
	var leader *runProc
	for i := range rows {
		if rows[i].pid == pid {
			leader = &rows[i]
			break
		}
	}
	uid := os.Getuid()
	if leader == nil || leader.uid != uid || blocked[pid] {
		return "eperm"
	}
	members := runGroupMembers(pid)
	safe := true
	for _, member := range members {
		found := false
		for _, p := range rows {
			if p.pid == member.pid && p.uid == uid && !blocked[member.pid] && p.start == member.start {
				found = true
				break
			}
		}
		if !found {
			safe = false
			break
		}
	}
	live := runStartTime(float64(pid))
	if live == nil || *live != recorded {
		return "mismatch"
	}
	if !safe {
		sent := runSignalMember(pid, recorded, sig)
		for _, member := range members {
			runSignalMember(member.pid, member.start, sig)
		}
		if sent {
			return "sent"
		}
		return "eperm"
	}
	err := runSignal(-pid, sig)
	if err == nil {
		return "sent"
	}
	if errors.Is(err, syscall.EPERM) {
		return "eperm"
	}
	if !errors.Is(err, syscall.ESRCH) {
		return "gone"
	}
	err = runSignal(pid, sig)
	if err == nil {
		return "sent"
	}
	if errors.Is(err, syscall.EPERM) {
		return "eperm"
	}
	return "gone"
}

// runWaitForDeath is waitForDeath: poll isAlive until the deadline.
func runWaitForDeath(pid int, within time.Duration) bool {
	deadline := runSweepNow().Add(within)
	for runSweepNow().Before(deadline) {
		if !runAlive(float64(pid)) {
			return true
		}
		runSweepSleep(runSweepPoll)
	}
	return !runAlive(float64(pid))
}

// runAllOld: sweepProcesses' ownerless grace (root, pids/ and every entry older
// than 600000 ms; any lstat failure keeps the root).
func runAllOld(dir string) bool {
	files := []string{dir}
	pids := filepath.Join(dir, "pids")
	if _, err := os.Stat(pids); err == nil {
		names, err := os.ReadDir(pids)
		if err != nil {
			return false
		}
		files = append(files, pids)
		for _, name := range names {
			files = append(files, filepath.Join(pids, name.Name()))
		}
	}
	now := time.Now()
	for _, file := range files {
		info, err := os.Lstat(file)
		if err != nil || now.Sub(info.ModTime()) <= 600*time.Second {
			return false
		}
	}
	return true
}

// runUntrusted is untrustedReason(dir) !== null (mask 022).
func runUntrusted(dir string) bool {
	info, err := os.Lstat(dir)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return true
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return true
	}
	return info.Mode().Perm()&0o022 != 0
}

// runOwnerState is tmp.mjs ownerState.
func runOwnerState(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "owner.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "missing"
		}
		return "invalid"
	}
	var owner any
	if json.Unmarshal(data, &owner) != nil {
		return "invalid"
	}
	m, _ := owner.(map[string]any)
	pid, ok := m["pid"].(float64)
	if !ok || pid != math.Trunc(pid) || math.IsInf(pid, 0) || pid <= 1 {
		return "invalid"
	}
	if !runAlive(pid) {
		return "dead"
	}
	if start, ok := m["start_time"].(string); ok && start != "unknown" {
		if live := runStartTime(pid); live != nil && *live != start {
			return "dead"
		}
	}
	return "alive"
}

type runPidEntry struct {
	pid   int
	start string
}

var runPidFile = regexp.MustCompile(`^(\d+)\.json$`)

// runListEntries is reaper.mjs listEntries: well-formed entries only.
func runListEntries(dir string) []runPidEntry {
	names, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := []runPidEntry{}
	for _, name := range names {
		m := runPidFile.FindStringSubmatch(name.Name())
		if m == nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name.Name()))
		if err != nil {
			continue
		}
		var parsed map[string]any
		if json.Unmarshal(data, &parsed) != nil {
			continue
		}
		pid, ok := parsed["pid"].(float64)
		want, _ := strconv.ParseFloat(m[1], 64)
		start, isString := parsed["start_time"].(string)
		if !ok || pid != math.Trunc(pid) || pid <= 1 || pid != want || !isString || pid > math.MaxInt32 {
			continue
		}
		out = append(out, runPidEntry{int(pid), start})
	}
	return out
}

func runLastActivity(dir string) time.Time {
	info, err := os.Lstat(dir)
	if err != nil {
		return time.Now()
	}
	newest := info.ModTime()
	pids := filepath.Join(dir, "pids")
	files := []string{pids}
	if names, err := os.ReadDir(pids); err == nil {
		for _, name := range names {
			files = append(files, filepath.Join(pids, name.Name()))
		}
	}
	for _, file := range files {
		if info, err := os.Lstat(file); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest
}

// runSweepRoots is sweepRoots({root}): dead roots (and ownerless ones idle for
// ten minutes) are claimed as tombstones, their registered children stopped
// (runSweepPids) and the tombstones removed.
func runSweepRoots(base string) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	baseTrusted := !runUntrusted(base)
	for _, entry := range entries {
		if !(entry.IsDir() || entry.Type()&os.ModeSymlink != 0) {
			continue
		}
		dir := filepath.Join(base, entry.Name())
		if !baseTrusted || runUntrusted(dir) {
			continue
		}
		owner := runOwnerState(dir)
		if owner == "alive" {
			continue
		}
		if owner != "dead" && !(owner == "missing" && time.Since(runLastActivity(dir)) > 10*time.Minute) {
			continue
		}
		suffix := make([]byte, 6)
		_, _ = rand.Read(suffix)
		tomb := filepath.Join(base, ".reaping-"+hex.EncodeToString(suffix))
		if os.Rename(dir, tomb) != nil {
			continue
		}
		if runOwnerState(tomb) == "alive" {
			runUnclaim(tomb, dir)
			continue
		}
		runSweepPids(filepath.Join(tomb, "pids"))
		_ = os.RemoveAll(tomb)
	}
}

// runSweepPids is reaper.mjs sweep(dir): a registered child whose live start
// time still matches is stopped (its group when every member is ours), a dead
// pid's entry is dropped, anything unproven is left alone.
func runSweepPids(dir string) {
	entries := runListEntries(dir)
	if len(entries) == 0 {
		return // B748: nothing registered, nothing to check (no ps call)
	}
	rows := runReadProcesses()
	blocked := runProtected(rows)
	uid := os.Getuid()
	unregister := func(pid int) { _ = os.Remove(filepath.Join(dir, strconv.Itoa(pid)+".json")) }
	for _, e := range entries {
		if blocked[e.pid] {
			continue
		}
		ours := false
		for _, p := range rows {
			if p.pid == e.pid && p.uid == uid {
				ours = true
			}
		}
		if runAlive(float64(e.pid)) && !ours {
			continue
		}
		if !runAlive(float64(e.pid)) {
			unregister(e.pid)
			continue
		}
		if e.start == "unknown" || runStartTime(float64(e.pid)) == nil {
			continue
		}
		members := runGroupMembers(e.pid)
		switch runSignalIfOurs(e.pid, e.start, syscall.SIGTERM) {
		case "mismatch":
			unregister(e.pid)
			continue
		case "eperm":
			continue
		}
		if !runWaitForDeath(e.pid, runSweepGrace) {
			if runSignalIfOurs(e.pid, e.start, syscall.SIGKILL) != "sent" {
				continue
			}
			runWaitForDeath(e.pid, runSweepGrace)
		}
		for _, member := range members {
			runSignalMember(member.pid, member.start, syscall.SIGKILL)
		}
		unregister(e.pid)
	}
}

func runUnclaim(tomb, dir string) {
	if os.Rename(tomb, dir) == nil {
		return
	}
	pids := filepath.Join(dir, "pids")
	_ = os.MkdirAll(pids, 0o700)
	if names, err := os.ReadDir(filepath.Join(tomb, "pids")); err == nil {
		for _, name := range names {
			_ = os.Rename(filepath.Join(tomb, "pids", name.Name()), filepath.Join(pids, name.Name()))
		}
	}
	_ = os.Rename(filepath.Join(tomb, "owner.json"), filepath.Join(dir, "owner.json"))
	_ = os.RemoveAll(tomb)
}
