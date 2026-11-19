// Package procguard is the one door for signals to a pid this process did not
// just start (B529). In production it is syscall.Kill. Under a test it lets a
// signal through only when every target is a descendant of the test process,
// so no test can stop a process of the person running it (on 2026-11-12 a
// test run of `run start` swept the real process table).
//
// The guard is on when any of these holds:
//   - this code runs inside a `go test` binary (testing.Testing());
//   - the binary was built by a test helper with
//     -ldflags "-X github.com/ricardov03/code-forge/native/procguard.builtForTest=1";
//   - the environment names a test root (Env), which a test binary sets for
//     every process it starts.
//
// The guard is never read from .code-forge.yml or any other config.
package procguard

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// Env holds the pid of the test process whose descendants a guarded process
// may signal. A test binary sets it to its own pid at start.
const Env = "CODE_FORGE_TEST_SIGNAL_ROOT"

// OwnedEnv marks a detached process that guarded code-forge code launched on
// purpose and still owns, such as the worker of `run start` (B882). Its value
// is the test root of the launcher. Such a process is started with Setsid and
// outlives its launcher, so init becomes its parent and it no longer descends
// from the test root; a later `run end` of the same test may still stop it.
// A process that merely inherited the test environment (a stranger's daemon
// started by a test's shell) does not carry it.
const OwnedEnv = "CODE_FORGE_TEST_SIGNAL_OWNED"

// builtForTest is set at link time by test helpers that build the binary.
var builtForTest string

func init() {
	if testing.Testing() && os.Getenv(Env) == "" {
		_ = os.Setenv(Env, strconv.Itoa(os.Getpid()))
	}
}

// Guarded reports whether signals are limited to the test's own processes.
func Guarded() bool {
	return testing.Testing() || builtForTest != "" || os.Getenv(Env) != ""
}

// root is the test process whose descendants may be signalled.
func root() int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(Env))); err == nil && n > 1 {
		return n
	}
	return os.Getpid()
}

// LaunchEnv is env for a detached process this code launches and owns: under
// the guard it carries OwnedEnv set to the test root (any older value is
// dropped); otherwise env is returned unchanged.
func LaunchEnv(env []string) []string {
	if !Guarded() {
		return env
	}
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, OwnedEnv+"=") {
			out = append(out, kv)
		}
	}
	return append(out, OwnedEnv+"="+strconv.Itoa(root()))
}

// ownedBy reads OwnedEnv from the environment of pid ("" when it is not set
// or cannot be read): /proc/<pid>/environ where it exists, else `ps -E`,
// which prints the environment after the command line.
var ownedBy = func(pid int) string {
	if _, err := os.Stat("/proc/self/environ"); err == nil {
		data, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
		for _, kv := range strings.Split(string(data), "\x00") {
			if strings.HasPrefix(kv, OwnedEnv+"=") {
				return kv[len(OwnedEnv)+1:]
			}
		}
		return ""
	}
	cmd := exec.Command("/bin/ps", "-E", "-ww", "-o", "command=", "-p", strconv.Itoa(pid))
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	for _, field := range strings.Fields(string(out)) {
		if strings.HasPrefix(field, OwnedEnv+"=") {
			return field[len(OwnedEnv)+1:]
		}
	}
	return ""
}

// owned reports whether pid is a detached process launched under this test
// root (OwnedEnv names the root).
func owned(pid, top int) bool {
	return ownedBy(pid) == strconv.Itoa(top)
}

type proc struct{ ppid, pgid int }

// table is the live process table: pid -> parent and group; ok false when
// ps fails.
var table = func() (map[int]proc, bool) {
	cmd := exec.Command("/bin/ps", "-A", "-o", "pid=,ppid=,pgid=")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	rows := map[int]proc{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		pgid, e3 := strconv.Atoi(f[2])
		if e1 == nil && e2 == nil && e3 == nil {
			rows[pid] = proc{ppid, pgid}
		}
	}
	return rows, true
}

// TestBuild reports whether this binary was built by a test helper (the
// builtForTest link flag). Such a binary refuses every signal to a process
// that does not descend from the test root (B529), so a scan for processes
// to stop can skip the others (B754). Never true for a product binary or for
// an in-process test.
func TestBuild() bool { return builtForTest != "" }

// MaySignal reports whether a guarded process may signal pid, judged on a
// process table given as pid -> ppid: pid descends from the test root
// (B754). It reads no process table of its own.
func MaySignal(ppids map[int]int, pid int) bool {
	rows := make(map[int]proc, len(ppids))
	for p, ppid := range ppids {
		rows[p] = proc{ppid: ppid}
	}
	return descends(rows, pid, root())
}

// descends reports whether pid's parent chain reaches top (pid != top).
func descends(rows map[int]proc, pid, top int) bool {
	seen := map[int]bool{}
	for cur := pid; cur > 1 && !seen[cur]; {
		seen[cur] = true
		p, ok := rows[cur]
		if !ok {
			return false
		}
		if p.ppid == top {
			return true
		}
		cur = p.ppid
	}
	return false
}

// Allowed reports whether a guarded process may send a real signal to pid
// (a negative pid is the process group -pid): every target must descend from
// the test root, or carry OwnedEnv naming the test root (a detached process
// guarded code-forge code launched, B882). Signal 0 (a liveness probe)
// always passes.
func Allowed(pid int, sig syscall.Signal) bool {
	if !Guarded() || sig == 0 {
		return true
	}
	if pid == 0 || pid == -1 {
		return false // the caller's own group, or every process
	}
	rows, ok := table()
	if !ok {
		return false
	}
	top := root()
	targets := []int{pid}
	if pid < 0 {
		targets = targets[:0]
		for p, row := range rows {
			if row.pgid == -pid {
				targets = append(targets, p)
			}
		}
		if len(targets) == 0 {
			return true // no such group: the kernel answers ESRCH, nothing is hit
		}
	}
	for _, p := range targets {
		if !descends(rows, p, top) && !owned(p, top) {
			return false
		}
	}
	return true
}

// Kill is syscall.Kill behind the guard: a refused signal is not sent and
// reads as EPERM, the answer the kernel gives for a process we may not touch.
func Kill(pid int, sig syscall.Signal) error {
	if !Allowed(pid, sig) {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return syscall.ESRCH // already gone: the answer the caller always got
		}
		return syscall.EPERM
	}
	return syscall.Kill(pid, sig)
}
