package main

import (
	"strings"
	"testing"
)

func TestB1007BatchRestoreDistinguishesCIStop(t *testing.T) {
	t.Setenv("CODE_FORGE_RUN_ID", "")
	t.Run("ordinary failure restores first unlanded block", func(t *testing.T) {
		f := newLandBatchFixture(t, 2)
		before := f.heads()
		code, output := f.land("B1", "B2", "B3")
		if code != 1 || !strings.Contains(output, "block B2 land stopped") {
			t.Fatalf("exit=%d: %s", code, output)
		}
		f.checkMain([]string{"B1"})
		for _, id := range []string{"B2", "B3"} {
			if got := f.git(f.repo, "rev-parse", "block-"+id); got != before[id] {
				t.Errorf("%s head=%s, want original %s", id, got, before[id])
			}
			entry := f.entry(id)
			if entry.Get("base_sha") != f.base || blockObject(entry.Get("worktree")).Get("base_sha") != f.base || entry.Get("landed") != nil {
				t.Errorf("%s must have its original bases and no landed marker: %v", id, entry)
			}
		}
	})
	t.Run("CI stopped tip survives refusal and retry", func(t *testing.T) {
		f := landB910Fixture(t, map[string]any{"require_ci": "github"})
		bare := f.bareRemote()
		before := f.heads()
		gh := landCIFakeGhSeq(t, map[string]string{
			"runs-" + before["B1"] + ".json": landCIGreen,
			"runs-" + before["B2"] + ".json": landCIGreen,
			"runs-" + before["B3"] + ".json": landCIGreen,
			"runs-other-1.json":              `[]`,
			"runs-other.json":                landCIRed,
			"jobs-7.json":                    `{"jobs":[]}`,
			"log-7.txt":                      landCIRedLog,
		})
		landCIFakeClock(t)
		code, output := f.land("B1", "B2")
		if code != 1 || !strings.Contains(output, "CI is red") {
			t.Fatalf("exit=%d, want rebased CI refusal: %s", code, output)
		}
		f.checkMain([]string{"B1"})
		rebased := f.git(f.repo, "rev-parse", "block-B2")
		if rebased == before["B2"] {
			t.Fatal("CI stopped rebase was undone")
		}
		if got := f.git(bare, "rev-parse", "refs/heads/ci-b2"); got != rebased {
			t.Errorf("CI branch=%s, want rebased %s", got, rebased)
		}
		main := f.git(f.repo, "rev-parse", "main")
		entry := f.entry("B2")
		if entry.Get("base_sha") != main || blockObject(entry.Get("worktree")).Get("base_sha") != main || entry.Get("landed") != nil {
			t.Fatalf("B2 must retain the landed prefix base and no landed marker: %v", entry)
		}
		f.write(gh, "runs-other.json", landCIGreen)
		if code, output := f.land("B2", "B3"); code != 0 {
			t.Fatalf("retry exit=%d: %s", code, output)
		}
		if got := blockObject(f.entry("B2").Get("landed")).Get("ci_sha"); got != rebased {
			t.Errorf("landed ci_sha=%v, want preserved %s", got, rebased)
		}
	})
}
