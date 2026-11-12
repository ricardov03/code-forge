package worker

// B489: loop.mjs cleanOrphans = reaper.mjs sweepProcesses({apply: true,
// orphansOnly: true, runId, zombies: false}). Only a process of this user
// whose parent is init, whose environment names an owner pid of this run's
// code-forge that is proven dead, is stopped: SIGTERM, a grace, then SIGKILL,
// each signal right after a fresh identity check. Every failure is swallowed.

import (
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ProcInfo is one `ps` row.
type ProcInfo struct {
	UID, PID, PPID int
	State          string
	Start          string
	Command        string
}

var psRow = regexp.MustCompile(`^\s*(\d+)\s+(\d+)\s+(\d+)\s+(\S+)\s+(\d+)\s+(\S+\s+\S+\s+\d+\s+\d+:\d+:\d+\s+\d{4})\s+(.+?)\s*$`)
var spaces = regexp.MustCompile(`\s+`)
var ownerPid = regexp.MustCompile(`^[1-9]\d*$`)

// ParseProcesses is parseProcesses: malformed rows are dropped.
func ParseProcesses(text string) []ProcInfo {
	out := []ProcInfo{}
	for _, l := range strings.Split(text, "\n") {
		m := psRow.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		uid, _ := strconv.Atoi(m[1])
		pid, _ := strconv.Atoi(m[2])
		ppid, _ := strconv.Atoi(m[3])
		out = append(out, ProcInfo{UID: uid, PID: pid, PPID: ppid, State: m[4], Start: spaces.ReplaceAllString(m[6], " "), Command: m[7]})
	}
	return out
}

func psBinary() string {
	if _, err := os.Stat("/bin/ps"); err == nil {
		return "/bin/ps"
	}
	return "ps"
}

func cLocaleEnv() []string { return append(os.Environ(), "LC_ALL=C", "LANG=C") }

func readProcessesPS() []ProcInfo {
	cmd := exec.Command(psBinary(), "-A", "-ww", "-o", "uid=,pid=,ppid=,stat=,rss=,lstart=,command=")
	cmd.Env = cLocaleEnv()
	out, err := outputWithin(cmd, 5*time.Second)
	if err != nil {
		return nil
	}
	return ParseProcesses(out)
}

func outputWithin(cmd *exec.Cmd, limit time.Duration) (string, error) {
	timer := time.AfterFunc(limit, func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	defer timer.Stop()
	out, err := cmd.Output()
	return string(out), err
}

var psEnvHead = regexp.MustCompile(`^\s*(\d+)\s+(\d+)\s+(\S+\s+\S+\s+\d+\s+\d+:\d+:\d+\s+\d{4})\s+`)

// ReadProcessEnv is readProcessEnv: the ownership markers only.
func ReadProcessEnv(p ProcInfo) map[string]string {
	var text string
	switch runtime.GOOS {
	case "linux":
		data, err := os.ReadFile("/proc/" + strconv.Itoa(p.PID) + "/environ")
		if err != nil {
			return nil
		}
		text = string(data)
	case "darwin":
		cmd := exec.Command("/bin/ps", "-E", "-ww", "-o", "pid=,ppid=,lstart=,command=", "-p", strconv.Itoa(p.PID))
		cmd.Env = cLocaleEnv()
		out, err := outputWithin(cmd, 5*time.Second)
		if err != nil {
			return nil
		}
		m := psEnvHead.FindStringSubmatch(out)
		if m == nil || m[1] != strconv.Itoa(p.PID) || m[2] != strconv.Itoa(p.PPID) || spaces.ReplaceAllString(m[3], " ") != p.Start {
			return nil
		}
		text = out[len(m[0]):]
		if !strings.HasPrefix(text, p.Command+" ") {
			return nil
		}
		text = text[len(p.Command)+1:]
	default:
		return nil
	}
	markers := map[string]string{}
	for _, name := range []string{"HOME", "CODE_FORGE_OWNER_PID", "CODE_FORGE_RUN_ID", "CODE_FORGE_OWNER_START_TIME"} {
		if runtime.GOOS == "linux" {
			for _, entry := range strings.Split(text, "\x00") {
				if strings.HasPrefix(entry, name+"=") {
					markers[name] = entry[len(name)+1:]
					break
				}
			}
			continue
		}
		if v, ok := envMarker(text, name); ok {
			markers[name] = v
		}
	}
	return markers
}

// envMarker is /(?:^|\s)NAME=(.*?)(?=\s+[A-Za-z_][A-Za-z0-9_]*=|$)/ (RE2 has
// no lookahead: the value ends before the next whitespace-led NAME=).
func envMarker(text, name string) (string, bool) {
	re := regexp.MustCompile(`(?:^|\s)` + regexp.QuoteMeta(name) + `=`)
	loc := re.FindStringIndex(text)
	if loc == nil {
		return "", false
	}
	rest := text[loc[1]:]
	next := regexp.MustCompile(`\s+[A-Za-z_][A-Za-z0-9_]*=`).FindStringIndex(rest)
	if next == nil {
		return rest, true
	}
	return rest[:next[0]], true
}

// OrphanSweep holds the seams of SweepOrphans.
type OrphanSweep struct {
	RunID string
	// Processes is readProcesses; ReadEnv is readProcessEnv.
	Processes func() []ProcInfo
	ReadEnv   func(ProcInfo) map[string]string
	Alive     func(pid int) bool
	Kill      func(pid int, sig syscall.Signal) error
	UID       int
	Self      int
	Parent    int
	GraceMs   int
	Sleep     func(time.Duration)
	Now       func() time.Time
}

func (s *OrphanSweep) defaults() {
	if s.Processes == nil {
		s.Processes = readProcessesPS
	}
	if s.ReadEnv == nil {
		s.ReadEnv = ReadProcessEnv
	}
	if s.Alive == nil {
		s.Alive = IsAlive
	}
	if s.Kill == nil {
		s.Kill = func(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) }
	}
	if s.Self == 0 {
		s.Self, s.Parent, s.UID = os.Getpid(), os.Getppid(), os.Getuid()
	}
	if s.GraceMs <= 0 {
		s.GraceMs = 2000
	}
	if s.Sleep == nil {
		s.Sleep = time.Sleep
	}
	if s.Now == nil {
		s.Now = time.Now
	}
}

func (s *OrphanSweep) protected(rows []ProcInfo) map[int]bool {
	blocked := map[int]bool{1: true, s.Self: true, s.Parent: true}
	byPid := map[int]ProcInfo{}
	for _, p := range rows {
		byPid[p.PID] = p
	}
	pid := s.Parent
	visited := map[int]bool{}
	for pid > 1 && !visited[pid] {
		visited[pid] = true
		blocked[pid] = true
		if p, ok := byPid[pid]; ok {
			pid = p.PPID
		} else {
			pid = 1
		}
	}
	for _, p := range rows {
		if strings.HasPrefix(p.State, "Z") {
			blocked[p.PID] = true
		}
	}
	return blocked
}

// Orphans lists the proven orphans (sweepProcesses' orphan items).
func (s *OrphanSweep) Orphans() []ProcInfo {
	s.defaults()
	rows := s.Processes()
	blocked := s.protected(rows)
	items := []ProcInfo{}
	for _, p := range rows {
		if p.UID != s.UID || p.PID <= 1 || blocked[p.PID] || p.PPID != 1 {
			continue
		}
		env := s.ReadEnv(p)
		owner := env["CODE_FORGE_OWNER_PID"]
		if env == nil || !ownerPid.MatchString(owner) {
			continue
		}
		n, err := strconv.Atoi(owner)
		if err != nil || n <= 1 || (s.RunID != "" && env["CODE_FORGE_RUN_ID"] != s.RunID) {
			continue
		}
		var live *ProcInfo
		for i := range rows {
			if rows[i].PID == n {
				live = &rows[i]
				break
			}
		}
		recorded := env["CODE_FORGE_OWNER_START_TIME"]
		dead := false
		if live == nil {
			dead = !s.Alive(n)
		} else {
			dead = strings.HasPrefix(live.State, "Z") || (recorded != "" && recorded != "unknown" && live.Start != recorded)
		}
		if dead {
			items = append(items, p)
		}
	}
	return items
}

func (s *OrphanSweep) signal(pid int, started string, sig syscall.Signal) bool {
	if started == "" || started == "unknown" {
		return false
	}
	rows := s.Processes()
	for _, p := range rows {
		if p.PID == pid {
			if p.UID != s.UID || s.protected(rows)[pid] || p.Start != started {
				return false
			}
			return s.Kill(pid, sig) == nil
		}
	}
	return false
}

func (s *OrphanSweep) running(pid int, started string) bool {
	for _, p := range s.Processes() {
		if p.PID == pid && !strings.HasPrefix(p.State, "Z") && p.Start == started {
			return true
		}
	}
	return false
}

// stop is stopMembers for one process.
func (s *OrphanSweep) stop(p ProcInfo) {
	if !s.signal(p.PID, p.Start, syscall.SIGTERM) {
		return
	}
	grace := time.Duration(s.GraceMs) * time.Millisecond
	deadline := s.Now().Add(grace)
	for s.Now().Before(deadline) && s.running(p.PID, p.Start) {
		s.Sleep(25 * time.Millisecond)
	}
	s.signal(p.PID, p.Start, syscall.SIGKILL)
	deadline = s.Now().Add(max(grace, time.Second))
	for s.Now().Before(deadline) {
		s.Sleep(25 * time.Millisecond)
		if !s.running(p.PID, p.Start) {
			break
		}
	}
}

// Sweep stops every proven orphan of the run; it never fails.
func (s *OrphanSweep) Sweep() {
	defer func() { _ = recover() }()
	for _, p := range s.Orphans() {
		s.stop(p)
	}
}
