package main

import (
	"runtime"
	"strconv"
	"testing"
)

// TestB758ScopedSweepOneBatchedEnvRead: with the default marker reader
// (B748: one `ps -E` call for every candidate) a binary built for a test
// names only its own descendants in that one call, so the foreign orphan's
// environment is never read and only the test's stale worker is listed; a
// product binary's one call still names the orphan and lists both.
func TestB758ScopedSweepOneBatchedEnvRead(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("ps -E batching is darwin only")
	}
	home := t.TempDir()
	orphan := b754Orphan(t, home)
	worker := b754Worker(t, home)
	beforeRead, beforeScoped, beforeCall := runReadEnv, runSweepScoped, runPsEnvCall
	t.Cleanup(func() { runReadEnv, runSweepScoped, runPsEnvCall = beforeRead, beforeScoped, beforeCall })
	runReadEnv = nil
	calls := [][]string{}
	runPsEnvCall = func(pids []string) (string, bool) {
		calls = append(calls, append([]string{}, pids...))
		return beforeCall(pids)
	}
	listed := func(items []runProc, pid int) bool {
		for _, item := range items {
			if item.pid == pid {
				return true
			}
		}
		return false
	}
	named := func(pid int) bool {
		for _, call := range calls {
			for _, p := range call {
				if p == strconv.Itoa(pid) {
					return true
				}
			}
		}
		return false
	}

	runSweepScoped = func() bool { return true }
	items := runSweepItems(home)
	if len(calls) != 1 || len(calls[0]) != 1 || !named(worker) {
		t.Errorf("test build: ps -E calls %v, want one call naming only the worker %d", calls, worker)
	}
	if named(orphan) || listed(items, orphan) {
		t.Errorf("test build: foreign orphan %d named=%v listed=%v", orphan, named(orphan), listed(items, orphan))
	}
	if !listed(items, worker) {
		t.Errorf("test build: own stale worker %d not listed: %v", worker, items)
	}

	calls = [][]string{}
	runSweepScoped = func() bool { return false }
	items = runSweepItems(home)
	if len(calls) != 1 || !named(orphan) || !named(worker) {
		t.Errorf("product: ps -E calls %d (orphan named=%v, worker named=%v), want one call naming both", len(calls), named(orphan), named(worker))
	}
	if !listed(items, orphan) || !listed(items, worker) {
		t.Errorf("product: orphan %d listed=%v, worker %d listed=%v", orphan, listed(items, orphan), worker, listed(items, worker))
	}
}
