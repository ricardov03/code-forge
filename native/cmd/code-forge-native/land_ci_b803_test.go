package main

import (
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/state"
)

// B803: a batch land waits for CI with no block lock held. While the land
// waits (the fake CI clock sleeps), another command takes each block's lock
// at once; the land result is the same as before.
func TestB803CIWaitHoldsNoBlockLock(t *testing.T) {
	f := landB910Fixture(t, map[string]any{"require_ci": "github"})
	f.bareRemote()
	heads := f.heads()
	landCIFakeGhSeq(t, map[string]string{
		// B1's tip: no run yet, then running, then green (the first wait).
		"runs-" + heads["B1"] + "-1.json": `[]`,
		"runs-" + heads["B1"] + "-2.json": landCIRunning,
		"runs-" + heads["B1"] + ".json":   landCIGreen,
		"runs-" + heads["B2"] + ".json":   landCIGreen,
		// B2 rebased onto B1's merge: running, then green (the second wait).
		"runs-other-1.json": landCIRunning,
		"runs-other.json":   landCIGreen,
	})
	landCIFakeClock(t)
	fakeSleep := landCISleep
	waits, busy := 0, []string{}
	landCISleep = func(d time.Duration) {
		waits++
		for _, id := range []string{"B1", "B2"} {
			if err := state.WithBlockLock(f.home, "r-batch-unit", id, func() error { return nil }); err != nil {
				busy = append(busy, id+": "+err.Error())
			}
		}
		fakeSleep(d)
	}
	code, output := f.land("B1", "B2")
	if code != 0 {
		t.Fatalf("exit=%d, want 0: %s", code, output)
	}
	f.checkMain([]string{"B1", "B2"})
	if waits < 2 {
		t.Fatalf("waits=%d, want the land to wait for CI on B1's tip and on B2's rebased tip:\n%s", waits, output)
	}
	if len(busy) > 0 {
		t.Errorf("a block lock was held while the land waited for CI:\n%s", strings.Join(busy, "\n"))
	}
	rebased := f.git(f.repo, "rev-parse", "block-B2")
	for _, want := range []string{
		"land: blocks before B2 land first; B2 waits for CI with no land lock held (B803)\n",
		"ci: tip " + rebased[:8] + " green (1 runs)\n",
		"block B1 landed\n",
		"block B2 landed\n",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("output must contain %q:\n%s", want, output)
		}
	}
}

// B803: a tip that moves while the land waited (with no lock) for its CI is
// refused at ci; nothing lands.
func TestB803TipMovedDuringWaitRefused(t *testing.T) {
	f := landB910Fixture(t, map[string]any{"require_ci": "github"})
	heads := f.heads()
	landCIFakeGhSeq(t, map[string]string{
		"runs-" + heads["B1"] + "-1.json": landCIRunning,
		"runs-" + heads["B1"] + ".json":   landCIGreen,
		"runs-" + heads["B2"] + ".json":   landCIGreen,
	})
	landCIFakeClock(t)
	fakeSleep := landCISleep
	moved := false
	landCISleep = func(d time.Duration) {
		if !moved {
			moved = true
			wt := f.repo + "/.code-forge/worktrees/B1"
			f.git(wt, "commit", "-q", "--allow-empty", "-m", "late change")
		}
		fakeSleep(d)
	}
	code, output := f.land("B1", "B2")
	if code == 0 {
		t.Fatalf("exit=0, want a refusal: %s", output)
	}
	if !strings.Contains(output, "block B1 land stopped at ci: block B1 tip moved from "+heads["B1"][:8]) {
		t.Errorf("refusal must name the moved tip:\n%s", output)
	}
	if got := f.git(f.repo, "rev-parse", "main"); got != f.base {
		t.Errorf("main=%s, want unchanged %s", got, f.base)
	}
}

// B998 (B803 review): main moves (another land) while the batch land waits,
// with no lock, for CI on B1's tip. The land must not merge a block onto the
// stale base: B1 is rebased onto the moved main (its new tip waits for its
// own green CI), and both merges sit on top of the new main commit.
func TestB998MainMovedDuringCIWait(t *testing.T) {
	f := landB910Fixture(t, map[string]any{"require_ci": "github"})
	f.bareRemote()
	heads := f.heads()
	landCIFakeGhSeq(t, map[string]string{
		"runs-" + heads["B1"] + "-1.json": landCIRunning,
		"runs-" + heads["B1"] + ".json":   landCIGreen,
		"runs-" + heads["B2"] + ".json":   landCIGreen,
		"runs-other.json":                 landCIGreen, // rebased tips
	})
	landCIFakeClock(t)
	fakeSleep := landCISleep
	moved := ""
	landCISleep = func(d time.Duration) {
		if moved == "" {
			f.write(f.repo, "other-land.txt", "landed while B1 waited\n")
			f.git(f.repo, "add", "other-land.txt")
			f.git(f.repo, "commit", "-qm", "another land")
			moved = f.git(f.repo, "rev-parse", "main")
		}
		fakeSleep(d)
	}
	code, output := f.land("B1", "B2")
	if moved == "" {
		t.Fatalf("the land never waited for CI:\n%s", output)
	}
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (B1 rebased onto the moved main, CI proves it, it lands): %s", code, output)
	}
	for _, id := range []string{"B1", "B2"} {
		if !strings.Contains(output, "block "+id+" landed\n") {
			t.Errorf("output must say block %s landed:\n%s", id, output)
		}
	}
	merges := strings.Fields(f.git(f.repo, "rev-list", "--first-parent", "--reverse", moved+"..main"))
	if len(merges) != 2 {
		t.Fatalf("main merges after the moved commit=%v, want B1 and B2 on top of it:\n%s", merges, output)
	}
	if first := f.git(f.repo, "rev-parse", merges[0]+"^1"); first != moved {
		t.Errorf("B1 merged onto %s, want the moved main %s", first, moved)
	}
	if !strings.Contains(output, "ci: block B1 rebased to ") {
		t.Errorf("B1 must be rebased onto the moved main before it lands:\n%s", output)
	}
}
