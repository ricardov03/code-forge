package main

import "testing"

// TestB746SweepOrphansOfThisHomeOnly: two temp HOMEs, a real orphan whose
// owner died in each, and one with no HOME. run start's worker and orphan
// search in HOME B lists only HOME B's orphan: a live process of HOME A (a
// test temp HOME, a second setup) and one whose HOME is unknown are not its
// to stop. Product search (unscoped), so the orphans' markers are read; the
// test only lists, it signals nothing.
func TestB746SweepOrphansOfThisHomeOnly(t *testing.T) {
	homeA, homeB := t.TempDir(), t.TempDir()
	orphanA := b754Orphan(t, homeA)
	orphanB := b754Orphan(t, homeB)
	noHome := b754Orphan(t, "")
	before, beforeScoped := runReadEnv, runSweepScoped
	t.Cleanup(func() { runReadEnv, runSweepScoped = before, beforeScoped })
	runReadEnv = nil
	runSweepScoped = func() bool { return false }
	listed := map[int]bool{}
	for _, item := range runSweepItems(homeB) {
		listed[item.pid] = true
	}
	if !listed[orphanB] {
		t.Errorf("HOME B's orphan %d not listed", orphanB)
	}
	if listed[orphanA] {
		t.Errorf("HOME B's run start lists HOME A's live orphan %d", orphanA)
	}
	if listed[noHome] {
		t.Errorf("HOME B's run start lists orphan %d with no HOME", noHome)
	}
}
