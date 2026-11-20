package main

import (
	"strings"
	"testing"
)

// B804: a batch land lands only a block commit green CI proved. Main moved
// after B1 and B2 were made, so CI proved their old tips (A) and each rebase
// makes a new, untested sha (B): the land waits for CI on B, lands B, and
// every landed commit is a sha whose CI read answered green.
func TestB804LandsOnlyCIProvedSha(t *testing.T) {
	f := landB910Fixture(t, map[string]any{"require_ci": "github"})
	f.bareRemote()
	heads := f.heads()
	f.write(f.repo, "main-moved.txt", "main moved\n")
	f.git(f.repo, "add", "main-moved.txt")
	f.git(f.repo, "commit", "-qm", "main moved")
	moved := f.git(f.repo, "rev-parse", "main")
	gh := landCIFakeGhSeq(t, map[string]string{
		"runs-" + heads["B1"] + ".json": landCIGreen,
		"runs-" + heads["B2"] + ".json": landCIGreen,
		// Every rebased tip: no run yet, then green.
		"runs-other-1.json": `[]`,
		"runs-other.json":   landCIGreen,
	})
	landCIFakeClock(t)
	code, output := f.land("B1", "B2")
	if code != 0 {
		t.Fatalf("exit=%d, want 0: %s", code, output)
	}
	merges := strings.Fields(f.git(f.repo, "rev-list", "--first-parent", "--reverse", moved+"..main"))
	if len(merges) != 2 {
		t.Fatalf("main merges=%v, want B1 and B2", merges)
	}
	calls := strings.Join(landCICalls(t, gh), "\n")
	for i, id := range []string{"B1", "B2"} {
		landedSha := f.git(f.repo, "rev-parse", merges[i]+"^2")
		if landedSha == heads[id] {
			t.Fatalf("%s landed its old tip; main moved, so the rebase must make a new sha", id)
		}
		if landedSha != f.git(f.repo, "rev-parse", "block-"+id) {
			t.Errorf("%s: landed %s is not the block branch", id, landedSha)
		}
		if strings.Count(calls, "run list --commit "+landedSha+" ") < 2 {
			t.Errorf("%s: the land must wait for green CI on the landed sha %s:\n%s", id, landedSha, calls)
		}
		marker := blockObject(f.entry(id).Get("landed"))
		if got := blockString(marker.Get("ci_sha")); got != landedSha {
			t.Errorf("%s landed marker ci_sha=%q, want the landed, CI-proved sha %s", id, got, landedSha)
		}
	}
}

// Without land.require_ci the landed marker carries no ci_sha.
func TestB804CIOffMarkerHasNoCISha(t *testing.T) {
	f := landB910Fixture(t, nil)
	landCIFakeGh(t, `[]`, nil)
	if code, output := f.land("B1", "B2"); code != 0 {
		t.Fatalf("exit=%d, want 0: %s", code, output)
	}
	for _, id := range []string{"B1", "B2"} {
		if marker := blockObject(f.entry(id).Get("landed")); marker == nil || marker.Get("ci_sha") != nil {
			t.Errorf("%s landed marker=%v, want one without ci_sha", id, marker)
		}
	}
}
