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

	"github.com/ricardov03/code-forge/native/state"
)

// B468: the process sweep a plain `run start` runs first (src/util/reaper.mjs
// sweepProcesses({apply: true, root, zombies: false}) and tmp.mjs sweepRoots).
// Go makes Node's read-only decisions; when Node would signal a process (a
// finished run's worker, an orphan whose owner died, a registered child of a
// dead run root), the whole start goes to Node before anything is written.
// Otherwise the only effect is sweepRoots' removal of dead run roots, done here.

type runProc struct {
	uid, pid, ppid int
	state, start   string
	command        string
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
	runReadEnv = runProcessEnv
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
		rows = append(rows, runProc{uid, pid, ppid, m[4], strings.Join(strings.Fields(m[6]), " "), m[7]})
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

var (
	runPsEnvHead  = regexp.MustCompile(`^\s*(\d+)\s+(\d+)\s+(\S+\s+\S+\s+\d+\s+\d+:\d+:\d+\s+\d{4})\s+`)
	runEnvMarkers = []string{"HOME", "CODE_FORGE_OWNER_PID", "CODE_FORGE_RUN_ID", "CODE_FORGE_OWNER_START_TIME"}
)

// runProcessEnv is readProcessEnv: only the ownership markers, nil when unreadable.
func runProcessEnv(p runProc) map[string]string {
	var text string
	switch runtime.GOOS {
	case "linux":
		data, err := os.ReadFile("/proc/" + strconv.Itoa(p.pid) + "/environ")
		if err != nil {
			return nil
		}
		markers := map[string]string{}
		for _, name := range runEnvMarkers {
			for _, entry := range strings.Split(string(data), "\x00") {
				if strings.HasPrefix(entry, name+"=") {
					markers[name] = entry[len(name)+1:]
					break
				}
			}
		}
		return markers
	case "darwin":
		out, ok := runPsOutput("-E", "-ww", "-o", "pid=,ppid=,lstart=,command=", "-p", strconv.Itoa(p.pid))
		if !ok {
			return nil
		}
		m := runPsEnvHead.FindStringSubmatch(out)
		if m == nil || m[1] != strconv.Itoa(p.pid) || m[2] != strconv.Itoa(p.ppid) || strings.Join(strings.Fields(m[3]), " ") != p.start {
			return nil
		}
		text = out[len(m[0]):]
		if !strings.HasPrefix(text, p.command+" ") {
			return nil
		}
		text = text[len(p.command)+1:]
	default:
		return nil
	}
	markers := map[string]string{}
	for _, name := range runEnvMarkers {
		if value, ok := runEnvMarker(text, name); ok {
			markers[name] = value
		}
	}
	return markers
}

func runJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

func runLineTerminator(r rune) bool { return r == '\n' || r == '\r' || r == 0x2028 || r == 0x2029 }

// runEnvMarker is /(?:^|\s)NAME=(.*?)(?=\s+[A-Za-z_][A-Za-z0-9_]*=|$)/ (no
// lookahead in Go's regexp): the first start that reaches a terminator wins.
func runEnvMarker(text, name string) (string, bool) {
	runes := []rune(text)
	key := []rune(name + "=")
	identStart := func(r rune) bool { return r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') }
	ident := func(r rune) bool { return identStart(r) || (r >= '0' && r <= '9') }
	// next variable: \s+ then an identifier then '='
	nextVar := func(j int) bool {
		k := j
		for k < len(runes) && runJSSpace(runes[k]) {
			k++
		}
		if k == j || k >= len(runes) || !identStart(runes[k]) {
			return false
		}
		for k < len(runes) && ident(runes[k]) {
			k++
		}
		return k < len(runes) && runes[k] == '='
	}
	for i := 0; i+len(key) <= len(runes); i++ {
		if i > 0 && !runJSSpace(runes[i-1]) {
			continue
		}
		if string(runes[i:i+len(key)]) != string(key) {
			continue
		}
		start := i + len(key)
		for j := start; ; j++ {
			if j == len(runes) || nextVar(j) {
				return string(runes[start:j]), true
			}
			if runLineTerminator(runes[j]) {
				break
			}
		}
	}
	return "", false
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
	runWorkerCommand = regexp.MustCompile(`^(?:\S*/)?node(?:js)?\s+(?:\S*/)?code-forge\.mjs\s+worker\s+--run(?:=|\s+)([a-z0-9][a-z0-9-]{0,63})(?:\s|$)`)
	runOwnerPid      = regexp.MustCompile(`^[1-9]\d*$`)
)

// runSweepWouldSignal is sweepProcesses' item search. true means Node would
// stop at least one process (or Go cannot tell): the caller hands over.
func runSweepWouldSignal(home, root string) bool {
	rows := runReadProcesses()
	uid := os.Getuid()
	own := []runProc{}
	for _, p := range rows {
		if p.uid == uid {
			own = append(own, p)
		}
	}
	blocked := runProtected(rows)
	for _, p := range own {
		if p.pid <= 1 || blocked[p.pid] {
			continue
		}
		if m := runWorkerCommand.FindStringSubmatch(p.command); m != nil {
			env := runReadEnv(p)
			if env == nil || env["HOME"] != home {
				continue
			}
			record, err := state.ReadRun(home, m[1])
			if err != nil {
				var stateErr *state.Error
				if !errors.As(err, &stateErr) || stateErr.Code == "no-run" {
					return true
				}
				continue
			}
			if status, ok := record.Get("status").(string); ok && status != "open" && status != "running" && status != "active" {
				return true
			}
			continue
		}
		if p.ppid != 1 {
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
			return true
		}
	}
	if runUntrusted(root) {
		return false
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return true // readdirSync throws in Node: its error, its message
	}
	for _, entry := range entries {
		full := filepath.Join(root, entry.Name())
		if !entry.IsDir() || runUntrusted(full) {
			continue
		}
		owner := runOwnerState(full)
		reap := owner == "dead"
		if owner == "missing" {
			reap = runAllOld(full)
		}
		if !reap {
			continue
		}
		for _, e := range runListEntries(filepath.Join(full, "pids")) {
			for _, p := range own {
				if p.pid == e.pid && p.start == e.start && !blocked[p.pid] {
					return true
				}
			}
		}
	}
	return false
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

// runSweepRoots is sweepRoots({root}) after runSweepWouldSignal found no
// registered child to stop: dead roots (and ownerless ones idle for ten
// minutes) are claimed as tombstones and removed; stale pid entries are dropped.
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

// runSweepPids is reaper.mjs sweep(dir) for entries that need no signal: a
// dead pid's entry is dropped; a live one whose start time differs is not ours.
func runSweepPids(dir string) {
	rows := runReadProcesses()
	blocked := runProtected(rows)
	uid := os.Getuid()
	for _, e := range runListEntries(dir) {
		if blocked[e.pid] {
			continue
		}
		alive := runAlive(float64(e.pid))
		ours := false
		for _, p := range rows {
			if p.pid == e.pid && p.uid == uid {
				ours = true
			}
		}
		if alive && !ours {
			continue
		}
		if !alive {
			_ = os.Remove(filepath.Join(dir, strconv.Itoa(e.pid)+".json"))
			continue
		}
		live := runStartTime(float64(e.pid))
		if e.start == "unknown" || live == nil {
			continue
		}
		if *live != e.start {
			_ = os.Remove(filepath.Join(dir, strconv.Itoa(e.pid)+".json"))
		}
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
