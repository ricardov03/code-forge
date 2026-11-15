package main

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// B737: the worker's wait for its native daemon (startNativeDaemon) runs
// inside run start's 15 s launch window. Under load each ping and pause
// stretched (84 pings over 14.4 s on a 100k-row ledger) and the window ran
// out. These tests drive the wait with a fake clock: a ping costs what the
// test says, the pause advances the clock, nothing really sleeps.

type b737Clock struct {
	mu        sync.Mutex
	now       time.Time
	pings     int // pings after the daemon was spawned
	preflight int // the "already answering?" ping before the spawn
}

func (c *b737Clock) advance(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }
func (c *b737Clock) read() time.Time         { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

// b737Setup points the daemon start at a child that never listens (it only
// sleeps), a temp HOME, and the fake clock. pingCost is what one ping takes;
// answerAt is the post-spawn ping that answers (0: none does).
func b737Setup(t *testing.T, pingCost time.Duration, answerAt int) *b737Clock {
	t.Helper()
	dir := t.TempDir()
	child := filepath.Join(dir, "fake-daemon")
	if err := os.WriteFile(child, []byte("#!/bin/sh\nexec sleep 30\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODE_FORGE_NATIVE", "")
	clock := &b737Clock{now: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)}
	oldBinary, oldPing, oldSleep, oldNow := daemonStartBinary, daemonStartPing, daemonStartSleep, daemonStartNow
	daemonStartBinary = func() (string, error) { return child, nil }
	daemonStartPing = func(run string, timeout time.Duration) bool {
		if timeout == 0 {
			clock.preflight++
			return false
		}
		clock.pings++
		clock.advance(pingCost)
		return answerAt > 0 && clock.pings == answerAt
	}
	daemonStartSleep = func(d time.Duration) { clock.advance(d) }
	daemonStartNow = clock.read
	t.Cleanup(func() {
		daemonStartBinary, daemonStartPing, daemonStartSleep, daemonStartNow = oldBinary, oldPing, oldSleep, oldNow
	})
	return clock
}

func b737Start(t *testing.T, clock *b737Clock) time.Duration {
	t.Helper()
	before := clock.read()
	stop, err := startNativeDaemon("r-b737", map[string]any{})
	if err != nil {
		t.Fatalf("startNativeDaemon: %v", err)
	}
	waited := clock.read().Sub(before)
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("stop did not end the daemon child")
	}
	return waited
}

// A daemon that does not answer while every ping is slow (load): the wait
// ends within its 2 s budget (plus the attempt in flight), not after 100
// slow pings, so the worker still announces inside run start's window.
func TestB737DaemonWaitBoundedUnderLoad(t *testing.T) {
	clock := b737Setup(t, 300*time.Millisecond, 0)
	waited := b737Start(t, clock)
	if clock.preflight != 1 {
		t.Fatalf("preflight pings = %d, want 1", clock.preflight)
	}
	if limit := 2*time.Second + 320*time.Millisecond; waited > limit {
		t.Fatalf("worker waited %v for a silent daemon (%d pings), want <= %v", waited, clock.pings, limit)
	}
	if clock.pings < 1 {
		t.Fatalf("pings = %d, want at least one", clock.pings)
	}
}

// Nothing slow: the 100-attempt cap Node has (native.mjs startNativeDaemon)
// is what ends the wait, as before.
func TestB737DaemonWaitKeepsHundredTriesWhenFast(t *testing.T) {
	clock := b737Setup(t, 0, 0)
	waited := b737Start(t, clock)
	if clock.pings != 100 {
		t.Fatalf("pings = %d, want 100", clock.pings)
	}
	if waited != 100*20*time.Millisecond {
		t.Fatalf("waited %v, want 2s", waited)
	}
}

// A daemon that answers stops the wait at that ping.
func TestB737DaemonWaitStopsAtAnswer(t *testing.T) {
	clock := b737Setup(t, 300*time.Millisecond, 3)
	waited := b737Start(t, clock)
	if clock.pings != 3 {
		t.Fatalf("pings = %d, want 3", clock.pings)
	}
	if want := 3*300*time.Millisecond + 2*20*time.Millisecond; waited != want {
		t.Fatalf("waited %v, want %v", waited, want)
	}
}
