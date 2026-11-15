package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// B734: a SIGTERM that reaches a serving worker during its orphan sweep (the
// clean-up after a drain that completed a ticket) is seen by the sweep, so
// the sweep ends instead of reading every init-parented process first.
func TestB734WorkerSweepSeesSIGTERM(t *testing.T) {
	f := newB489Fix(t)
	f.ticket(t, "B1", "a.txt", "hello\nworld\n")
	var signals chan<- os.Signal
	workerSignals = func(c chan<- os.Signal) func() {
		signals = c
		return func() {}
	}
	var before, after bool
	sweeps := 0
	workerOrphans = func(run string, stopping func() bool) func() {
		return func() {
			sweeps++
			before = stopping()
			signals <- syscall.SIGTERM // the stop lands inside the sweep
			deadline := time.Now().Add(10 * time.Second)
			for !stopping() && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			after = stopping()
		}
	}
	code, _, errText := b460Call(t, "worker", "--run", "r1", "--poll-ms", "10")
	want := fmt.Sprintf("worker: pid %d serving run r1 · jev key absent (fallback)\n", os.Getpid())
	if code != 0 || !strings.Contains(errText, want) || !strings.HasSuffix(errText, "worker: stopped\n") {
		t.Fatalf("worker = %d %q", code, errText)
	}
	if sweeps != 1 || before || !after {
		t.Fatalf("sweeps %d, stopping at sweep start %v (want false), after SIGTERM %v (want true)", sweeps, before, after)
	}
}
