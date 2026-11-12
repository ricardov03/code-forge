package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/filelock"
)

// sweepChild is a process this test started (its own process group), shown
// to the sweep in a fake process table with the command and environment the
// test gives it. Nothing outside the test's own children is ever listed.
type sweepChild struct {
	pid     int
	start   string
	ppid    int
	command string
	env     map[string]string
	done    chan struct{}
}

type sweepWorld struct {
	mu       sync.Mutex
	children []*sweepChild
	signals  []string
	clock    time.Time
}

func (w *sweepWorld) alive(c *sweepChild) bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

// spawn starts `sleep 30` in its own group; ignoreTerm makes it ignore SIGTERM.
func (w *sweepWorld) spawn(t *testing.T, ignoreTerm bool, ppid int, command string, env map[string]string) *sweepChild {
	t.Helper()
	script := "exec sleep 30"
	if ignoreTerm {
		script = "trap '' TERM; exec sleep 30"
	}
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := &sweepChild{pid: cmd.Process.Pid, ppid: ppid, command: command, env: env, done: make(chan struct{})}
	go func() { _, _ = cmd.Process.Wait(); close(c.done) }()
	t.Cleanup(func() { _ = syscall.Kill(c.pid, syscall.SIGKILL); <-c.done })
	// the start time once the shell has become sleep (exec keeps the pid)
	for i := 0; i < 200; i++ {
		if out, err := exec.Command("/bin/ps", "-o", "comm=", "-p", strconv.Itoa(c.pid)).Output(); err == nil && len(out) > 0 && filepath.Base(string(out[:len(out)-1])) == "sleep" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	started := filelock.ProcessStartTime(c.pid)
	if started == nil {
		t.Fatal("no start time for the child")
	}
	c.start = *started
	w.mu.Lock()
	w.children = append(w.children, c)
	w.mu.Unlock()
	return c
}

// install swaps the sweep's seams for this world: the table lists live
// children only, signals go only to them (anything else fails the test), and
// the clock moves only when the sweep sleeps.
func (w *sweepWorld) install(t *testing.T) {
	t.Helper()
	w.clock = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	before := []any{runReadProcesses, runReadEnv, runSignal, runGroupMembers, runSweepNow, runSweepSleep}
	runReadProcesses = func() []runProc {
		w.mu.Lock()
		defer w.mu.Unlock()
		rows := []runProc{}
		for _, c := range w.children {
			if w.alive(c) {
				rows = append(rows, runProc{uid: os.Getuid(), pid: c.pid, ppid: c.ppid, state: "S", start: c.start, command: c.command})
			}
		}
		return rows
	}
	runReadEnv = func(p runProc) map[string]string {
		w.mu.Lock()
		defer w.mu.Unlock()
		for _, c := range w.children {
			if c.pid == p.pid {
				return c.env
			}
		}
		return nil
	}
	runSignal = func(pid int, sig syscall.Signal) error {
		w.mu.Lock()
		defer w.mu.Unlock()
		target := pid
		if target < 0 {
			target = -target
		}
		for _, c := range w.children {
			if c.pid == target {
				w.signals = append(w.signals, fmt.Sprintf("%d:%s", pid, sig))
				return syscall.Kill(pid, sig)
			}
		}
		t.Errorf("signal %v to %d, not a child of this test", sig, pid)
		return syscall.EPERM
	}
	runGroupMembers = func(int) []runMember { return []runMember{} }
	runSweepNow = func() time.Time {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.clock
	}
	runSweepSleep = func(d time.Duration) {
		w.mu.Lock()
		w.clock = w.clock.Add(d)
		w.mu.Unlock()
		time.Sleep(time.Millisecond) // lets the kernel deliver and the child be reaped
	}
	t.Cleanup(func() {
		runReadProcesses = before[0].(func() []runProc)
		runReadEnv = before[1].(func(runProc) map[string]string)
		runSignal = before[2].(func(int, syscall.Signal) error)
		runGroupMembers = before[3].(func(int) []runMember)
		runSweepNow = before[4].(func() time.Time)
		runSweepSleep = before[5].(func(time.Duration))
	})
}

func (w *sweepWorld) sent() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string{}, w.signals...)
}

func waitGone(t *testing.T, c *sweepChild) {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		t.Fatalf("pid %d still running", c.pid)
	}
}

func deadPid(t *testing.T) int {
	t.Helper()
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	return gone.Process.Pid
}

// TestRunStartSweepStops: run start stops, in Go, what Node's
// sweepProcesses({apply: true}) stops, with Node's checks and signals, then
// starts the run as usual; processes it may not touch get no signal.
func TestRunStartSweepStops(t *testing.T) {
	const config = "project:\n  slug: unit\ntmp:\n  root: TMPROOT\n"
	worker := func(run string) string { return "node /x/bin/code-forge.mjs worker --run " + run }
	start := func(t *testing.T, f startFixture) {
		t.Helper()
		sleeper, _ := startSleeper(t)
		code, out, errs := runVerbCall(t, "start", "--cwd", f.repo, "--run", "r-new", "--worker-pid", strconv.Itoa(sleeper.Process.Pid))
		if code != 0 || errs != "" || out == "" {
			t.Fatalf("start = %d %q %q", code, out, errs)
		}
		if _, err := os.Stat(filepath.Join(f.home, ".code-forge", "runs", "r-new.json")); err != nil {
			t.Fatalf("no run record: %v", err)
		}
	}

	t.Run("workers of ended or unknown runs and dead owners' orphans", func(t *testing.T) {
		f := newStartFixture(t, config)
		f.workspace = f.repo
		f.record(t, "r-old", "ended", `,"blocks":{}`)
		f.record(t, "r-live", "active", `,"blocks":{}`)
		bad := filepath.Join(f.home, ".code-forge", "runs", "r-bad.json")
		if err := os.WriteFile(bad, []byte("{not json"), 0600); err != nil {
			t.Fatal(err)
		}
		w := &sweepWorld{}
		home := map[string]string{"HOME": f.home}
		ended := w.spawn(t, false, 4242, worker("r-old"), home)
		missing := w.spawn(t, false, 4242, worker("r-none"), home)
		active := w.spawn(t, false, 4242, worker("r-live"), home)
		unreadable := w.spawn(t, false, 4242, worker("r-bad"), home)
		otherHome := w.spawn(t, false, 4242, worker("r-old"), map[string]string{"HOME": t.TempDir()})
		noEnv := w.spawn(t, false, 4242, worker("r-old"), nil)
		orphan := w.spawn(t, false, 1, "sh -c long-task", map[string]string{"CODE_FORGE_OWNER_PID": strconv.Itoa(deadPid(t))})
		owned := w.spawn(t, false, 1, "sh -c long-task", map[string]string{"CODE_FORGE_OWNER_PID": strconv.Itoa(os.Getpid())})
		notOrphan := w.spawn(t, false, 4242, "sh -c long-task", map[string]string{"CODE_FORGE_OWNER_PID": strconv.Itoa(deadPid(t))})
		w.install(t)
		start(t, f)
		for _, c := range []*sweepChild{ended, missing, orphan} {
			waitGone(t, c)
		}
		want := []string{
			fmt.Sprintf("%d:terminated", ended.pid),
			fmt.Sprintf("%d:terminated", missing.pid),
			fmt.Sprintf("%d:terminated", orphan.pid),
		}
		if got := w.sent(); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("signals = %v, want %v", got, want)
		}
		for _, c := range []*sweepChild{active, unreadable, otherHome, noEnv, owned, notOrphan} {
			if !w.alive(c) {
				t.Fatalf("pid %d (%s) was stopped", c.pid, c.command)
			}
		}
	})

	t.Run("SIGKILL after the grace when SIGTERM is ignored", func(t *testing.T) {
		f := newStartFixture(t, config)
		w := &sweepWorld{}
		stubborn := w.spawn(t, true, 1, "sh -c long-task", map[string]string{"CODE_FORGE_OWNER_PID": strconv.Itoa(deadPid(t))})
		w.install(t)
		start(t, f)
		waitGone(t, stubborn)
		want := fmt.Sprintf("[%d:terminated %d:killed]", stubborn.pid, stubborn.pid)
		if got := fmt.Sprint(w.sent()); got != want {
			t.Fatalf("signals = %s, want %s", got, want)
		}
		if elapsed := w.clock.Sub(time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)); elapsed < runSweepGrace {
			t.Fatalf("SIGKILL before the grace: %v", elapsed)
		}
	})

	t.Run("registered children of a dead run root", func(t *testing.T) {
		f := newStartFixture(t, config)
		w := &sweepWorld{}
		child := w.spawn(t, false, 4242, "codex exec", nil)
		stubborn := w.spawn(t, true, 4242, "codex exec", nil)
		reused := w.spawn(t, false, 4242, "codex exec", nil)
		live := w.spawn(t, false, 4242, "codex exec", nil)
		dead := filepath.Join(f.tmpRoot, "b1-dead")
		keep := filepath.Join(f.tmpRoot, "b2-live")
		register := func(dir string, pid int, start string) {
			if err := os.WriteFile(filepath.Join(dir, "pids", strconv.Itoa(pid)+".json"), []byte(fmt.Sprintf(`{"pid":%d,"start_time":%q,"argv0":"codex"}`, pid, start)), 0600); err != nil {
				t.Fatal(err)
			}
		}
		for dir, owner := range map[string]int{dead: deadPid(t), keep: os.Getpid()} {
			if err := os.MkdirAll(filepath.Join(dir, "pids"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "owner.json"), []byte(fmt.Sprintf(`{"pid":%d,"start_time":"unknown"}`, owner)), 0600); err != nil {
				t.Fatal(err)
			}
		}
		register(dead, child.pid, child.start)
		register(dead, stubborn.pid, stubborn.start)
		register(dead, reused.pid, "Mon Jan  1 00:00:00 2001") // the pid names another process now
		register(keep, live.pid, live.start)
		w.install(t)
		start(t, f)
		waitGone(t, child)
		waitGone(t, stubborn)
		got := map[string]bool{}
		for _, s := range w.sent() {
			got[s] = true
		}
		for _, s := range []string{
			fmt.Sprintf("-%d:terminated", child.pid),
			fmt.Sprintf("-%d:terminated", stubborn.pid),
			fmt.Sprintf("-%d:killed", stubborn.pid),
		} {
			if !got[s] {
				t.Fatalf("signals = %v, missing %s", w.sent(), s)
			}
		}
		if len(got) != 3 || !w.alive(reused) || !w.alive(live) {
			t.Fatalf("signals = %v; reused alive %v, live-root child alive %v", w.sent(), w.alive(reused), w.alive(live))
		}
		if _, err := os.Stat(dead); !os.IsNotExist(err) {
			t.Fatalf("dead root kept: %v", err)
		}
		if _, err := os.Stat(filepath.Join(keep, "pids", strconv.Itoa(live.pid)+".json")); err != nil {
			t.Fatalf("live root's entry removed: %v", err)
		}
	})
}
