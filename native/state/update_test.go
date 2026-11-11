package state

import (
	"errors"
	"sync"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

func updateFixture(t *testing.T) (home string) {
	t.Helper()
	home, workspace := t.TempDir(), t.TempDir()
	blocks := ledger.Object{
		{Key: "A", Value: ledger.Object{{Key: "status", Value: "open"}, {Key: "base_sha", Value: "a0"}}},
		{Key: "B", Value: ledger.Object{{Key: "status", Value: "open"}, {Key: "base_sha", Value: "b0"}}},
	}
	record := ledger.Object{{Key: "run_id", Value: "unit"}, {Key: "workspace", Value: workspace}, {Key: "project", Value: "unit"}, {Key: "status", Value: "active"}, {Key: "blocks", Value: blocks}}
	if err := SaveRun(home, record); err != nil {
		t.Fatal(err)
	}
	return home
}

func blockOf(t *testing.T, home, id string) ledger.Object {
	t.Helper()
	record, err := ReadRun(home, "unit")
	if err != nil {
		t.Fatal(err)
	}
	return record.Get("blocks").(ledger.Object).Get(id).(ledger.Object)
}

// Two verbs that read the same snapshot and commit different blocks at the
// same time both persist: neither save erases the other block's change.
func TestCommitBlocksDifferentBlocksBothPersist(t *testing.T) {
	home := updateFixture(t)
	for round := 0; round < 20; round++ {
		snapA, snapB := CloneObject(blockOf(t, home, "A")), CloneObject(blockOf(t, home, "B"))
		nextA, nextB := CloneObject(snapA), CloneObject(snapB)
		nextA.Set("base_sha", "a"+string(rune('a'+round)))
		nextB.Set("base_sha", "b"+string(rune('a'+round)))
		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			errs[0] = CommitBlocks(home, "unit", map[string]ledger.Object{"A": snapA}, map[string]ledger.Object{"A": nextA})
		}()
		go func() {
			defer wg.Done()
			errs[1] = CommitBlocks(home, "unit", map[string]ledger.Object{"B": snapB}, map[string]ledger.Object{"B": nextB})
		}()
		wg.Wait()
		if errs[0] != nil || errs[1] != nil {
			t.Fatalf("round %d: both commits must succeed, got %v and %v", round, errs[0], errs[1])
		}
		if a, b := blockOf(t, home, "A").Get("base_sha"), blockOf(t, home, "B").Get("base_sha"); a != nextA.Get("base_sha") || b != nextB.Get("base_sha") {
			t.Fatalf("round %d: want A=%v B=%v persisted, got A=%v B=%v", round, nextA.Get("base_sha"), nextB.Get("base_sha"), a, b)
		}
	}
}

// A verb whose own block was changed by another verb while it worked gets
// block-changed naming the block, and the other verb's change stays.
func TestCommitBlocksSameBlockChangedRefuses(t *testing.T) {
	home := updateFixture(t)
	snap := CloneObject(blockOf(t, home, "A"))
	other := CloneObject(snap)
	other.Set("base_sha", "moved-by-other")
	if err := CommitBlocks(home, "unit", map[string]ledger.Object{"A": snap}, map[string]ledger.Object{"A": other}); err != nil {
		t.Fatal(err)
	}
	mine := CloneObject(snap)
	mine.Set("base_sha", "mine")
	err := CommitBlocks(home, "unit", map[string]ledger.Object{"A": snap}, map[string]ledger.Object{"A": mine})
	var se *Error
	if !errors.As(err, &se) || se.Code != "block-changed" || se.Message != "block-changed: block A of run unit was changed by another verb while this one ran; nothing was saved for it; run the verb again" {
		t.Fatalf("want block-changed naming block A, got %v", err)
	}
	if got := blockOf(t, home, "A").Get("base_sha"); got != "moved-by-other" {
		t.Fatalf("A base_sha = %v, want the other verb's moved-by-other", got)
	}
	if got := blockOf(t, home, "B").Get("base_sha"); got != "b0" {
		t.Fatalf("B base_sha = %v, want untouched b0", got)
	}
}
