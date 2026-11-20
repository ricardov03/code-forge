package main

import (
	"strings"
	"testing"
)

// B980: with land.require_ci: github a batch merge gate whose tree is the
// tree of a block tip green CI proved skips the full suite (CI ran it on the
// same tree) and says so; with the gate off the merge gate still runs it.
func TestB980MergeGateSkippedOnCIProvedTree(t *testing.T) {
	t.Run("ci-proved", func(t *testing.T) {
		f := landB910Fixture(t, map[string]any{"require_ci": "github"})
		f.bareRemote()
		heads := f.heads()
		landCIFakeGhSeq(t, map[string]string{
			"runs-" + heads["B1"] + ".json": landCIGreen,
			"runs-" + heads["B2"] + ".json": landCIGreen,
			// B2 rebased onto B1's merge: a new sha, green.
			"runs-other.json": landCIGreen,
		})
		landCIFakeClock(t)
		code, output := f.land("B1", "B2")
		if code != 0 {
			t.Fatalf("exit=%d, want 0: %s", code, output)
		}
		f.checkMain([]string{"B1", "B2"})
		if calls := f.lines("merge-calls"); len(calls) != 0 {
			t.Errorf("merge gate suite ran %v, want none on a CI-proved tree", calls)
		}
		tree := f.git(f.repo, "rev-parse", heads["B1"]+"^{tree}")
		want := "merge gate: tree " + tree[:12] + " is the tree green CI proved at " + heads["B1"][:8] + "; full suite skipped (B980)\n"
		if !strings.Contains(output, want) {
			t.Errorf("output must contain %q:\n%s", want, output)
		}
		rebased := f.git(f.repo, "rev-parse", "block-B2")
		last := f.git(f.repo, "rev-parse", rebased+"^{tree}")
		want = "merge gate: tree " + last[:12] + " is the tree green CI proved at " + rebased[:8] + "; full suite skipped (B980)\n"
		if !strings.Contains(output, want) {
			t.Errorf("output must contain %q:\n%s", want, output)
		}
		if got := f.git(f.repo, "rev-parse", "main^{tree}"); got != last {
			t.Errorf("main tree=%s, want the CI-proved tree %s", got, last)
		}
	})
	// A tree no proved tip has (a different tree, an unproved block, no
	// tree) gets no CI proof: the merge gate runs its suite.
	t.Run("tree-match", func(t *testing.T) {
		f := landB910Fixture(t, nil)
		heads := f.heads()
		tree := func(sha string) string { return f.git(f.repo, "rev-parse", sha+"^{tree}") }
		proved := []*landBatchBlock{{id: "B1", ci: heads["B1"]}, {id: "B2"}}
		if got := landBatchCITree(f.repo, tree(heads["B1"]), proved); got != heads["B1"] {
			t.Errorf("tree of the proved B1 tip: got %q, want %s", got, heads["B1"])
		}
		for name, other := range map[string]string{"unproved B2 tree": tree(heads["B2"]), "main tree": tree("main"), "no tree": ""} {
			if got := landBatchCITree(f.repo, other, proved); got != "" {
				t.Errorf("%s: got proof %q, want none", name, got)
			}
		}
	})
	t.Run("ci-off", func(t *testing.T) {
		f := landB910Fixture(t, nil)
		landCIFakeGh(t, `[]`, nil)
		code, output := f.land("B1", "B2")
		if code != 0 {
			t.Fatalf("exit=%d, want 0: %s", code, output)
		}
		if calls := f.lines("merge-calls"); len(calls) == 0 {
			t.Errorf("merge gate suite must run without land.require_ci:\n%s", output)
		}
		if strings.Contains(output, "(B980)") {
			t.Errorf("no CI proof without the gate:\n%s", output)
		}
	})
}
