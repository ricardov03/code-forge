package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// b544Zombie starts a child of the test process that exits at once and is
// not reaped until the cleanup: a real zombie in the machine's process table
// with this test's uid, as the owner's emulator leaves them.
func b544Zombie(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() { _ = cmd.Wait() }) // reaps only the child this test started
	for i := 0; i < 200; i++ {
		for _, p := range runReadProcesses() {
			if p.pid == pid && strings.HasPrefix(p.state, "Z") {
				return pid
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("child %d never showed as a zombie in ps", pid)
	return 0
}

// The doctor goldens hold `sweep: 0 stale workers, 0 orphans, 0 zombies`:
// a zombie on the machine must not change them.
func TestB544DoctorGoldenSealedFromMachineZombies(t *testing.T) {
	pid := b544Zombie(t)
	t.Run("notmproot", func(t *testing.T) {
		want, err := os.ReadFile(filepath.Join("testdata", "b525", "notmproot.golden"))
		if err != nil {
			t.Fatal(err)
		}
		e := b525Setup(t, b525Case{mode: "ok", config: "provider: anthropic\n" + b474Levels, fakes: []string{"claude", "grok"}}, "")
		b535Lock(t, e.tmp)
		code, stdout, stderr := runCaptured(t, "doctor")
		got := b535RootName.ReplaceAllString(e.mask(fmt.Sprintf("exit=%d\n--- stdout\n%s--- stderr\n%s", code, stdout, stderr)), "doctor-<PID>-<MS>-<HEX>")
		if got != string(want) {
			t.Fatalf("zombie %d leaked into the golden\n--- got\n%s\n--- want\n%s", pid, got, want)
		}
	})
	t.Run("allok", func(t *testing.T) {
		want, err := os.ReadFile(filepath.Join("testdata", "b525", "allok.golden"))
		if err != nil {
			t.Fatal(err)
		}
		e := b525Setup(t, b525Case{mode: "ok", config: "provider: anthropic\n" + b474Levels, fakes: []string{"claude", "grok"}}, "")
		code, stdout, stderr := runCaptured(t, "doctor")
		if got := e.mask(fmt.Sprintf("exit=%d\n--- stdout\n%s--- stderr\n%s", code, stdout, stderr)); got != string(want) {
			t.Fatalf("zombie %d leaked into the golden\n--- got\n%s\n--- want\n%s", pid, got, want)
		}
		e.assertClean(t)
	})
	// Production still reads the real table: the same zombie is there.
	report, _ := sweepScan(t.TempDir(), t.TempDir())
	found := false
	for _, g := range report.zombies {
		for _, z := range g.pids {
			found = found || z == pid
		}
	}
	if !found {
		t.Fatalf("the real sweep scan does not list zombie %d", pid)
	}
}

// b544SlowPipeHelper writes a script that prints and exits 0 while a
// background child keeps its stdout and stderr open for 300 ms: the same
// late pipe close a loaded machine gives a starved pipe copier.
func b544SlowPipeHelper(t *testing.T, dir, name string) string {
	t.Helper()
	file := filepath.Join(dir, name)
	if err := os.WriteFile(file, []byte("#!/bin/sh\nprintf '%s\\n' \"$PWD\"\n(sleep 0.3) &\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return file
}

// A helper that exited 0 is a success even when its pipes close late; at
// 50 ms Wait returned exec.ErrWaitDelay and the join read it as a failure.
func TestB544WaitCommandLatePipeIsSuccess(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	helper := b544SlowPipeHelper(t, dir, "helper")
	out, _, ok := reviewWaitCommandErr(context.Background(), dir, helper, nil, os.Environ(), 10*time.Second)
	if !ok || out != dir+"\n" {
		t.Fatalf("reviewWaitCommandErr ok=%v out=%q, want ok and %q", ok, out, dir+"\n")
	}
	bin := t.TempDir()
	b544SlowPipeHelper(t, bin, "git")
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	got, exited := reviewTreeGit(context.Background(), dir, nil, "rev-parse", "--show-toplevel")
	if !exited || got != dir {
		t.Fatalf("reviewTreeGit exited=%v out=%q, want exited and %q", exited, got, dir)
	}
}
