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
// the test root. Signal 0 (a liveness probe) always passes.
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
		if !descends(rows, p, top) {
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
