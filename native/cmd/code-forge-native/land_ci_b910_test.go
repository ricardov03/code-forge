package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/state"
)

// B910 tests: with land.require_ci: github a batch land skips the block's
// local test gate when the sha it lands is the sha green CI proved, pushes a
// rebased (new) sha to a CI branch and waits for green CI on it, and still
// runs the fast gates and the merge gate. gh is a fake script on PATH, the
// remote a local bare repo, the clock fake: no network, no real waits.

// landB910Fixture is the CI batch fixture with a lint gate (a fast step that
// logs each run to $HOME/lint-calls) committed on main; B1 and B2 are rebased
// onto that main, so each block tip already contains main.
func landB910Fixture(t *testing.T, land map[string]any) *landBatchFixture {
	t.Helper()
	f := landCIBatch(t, land, "", "")
	path := filepath.Join(f.repo, ".code-forge.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg["gates"].(map[string]any)["lint"] = []any{"/bin/sh", "-c", `printf '%s\n' "$PWD" >> "$HOME/lint-calls"`}
	data, _ = json.Marshal(cfg)
	f.write(f.repo, ".code-forge.yml", string(data))
	f.git(f.repo, "commit", "-qam", "lint gate")
	f.base = f.git(f.repo, "rev-parse", "main")
	record, err := state.ReadRun(f.home, "r-batch-unit")
	if err != nil {
		t.Fatal(err)
	}
	blocks := blockObject(record.Get("blocks"))
	for _, id := range []string{"B1", "B2", "B3"} {
		f.git(filepath.Join(f.repo, ".code-forge/worktrees", id), "rebase", "-q", "main")
		entry := blockObject(blocks.Get(id))
		entry.Set("base_sha", f.base)
		wt := blockObject(entry.Get("worktree"))
		wt.Set("base_sha", f.base)
		entry.Set("worktree", wt)
		blocks.Set(id, entry)
	}
	record.Set("blocks", blocks)
	if err := state.SaveRun(f.home, record); err != nil {
		t.Fatal(err)
	}
	return f
}

// B1's tip contains main and has green CI: no local test run. B2 is rebased
// onto B1's merge (a new sha): it is pushed to ci-b2, the land waits for its
// green CI, then skips its local test too. Lint and the merge gate still run.
func TestB910CIProvenTipSkipsLocalTests(t *testing.T) {
	f := landB910Fixture(t, map[string]any{"require_ci": "github"})
	bare := f.bareRemote()
	heads := f.heads()
	gh := landCIFakeGhSeq(t, map[string]string{
		"runs-" + heads["B1"] + ".json": landCIGreen,
		"runs-" + heads["B2"] + ".json": landCIGreen,
		// The rebased B2 tip: no run yet, then running, then green.
		"runs-other-1.json": `[]`,
		"runs-other-2.json": landCIRunning,
		"runs-other.json":   landCIGreen,
	})
	slept := landCIFakeClock(t)
	code, output := f.land("B1", "B2")
	if code != 0 {
		t.Fatalf("exit=%d, want 0: %s", code, output)
	}
	f.checkMain([]string{"B1", "B2"})
	if calls := f.lines("changed-calls"); len(calls) != 0 {
		t.Errorf("local test gate ran %d times (%v), want none: CI proved every landed tip", len(calls), calls)
	}
	if len(f.lines("merge-calls")) == 0 {
		t.Errorf("the batch merge gate must still run")
	}
	lint := strings.Join(f.lines("lint-calls"), "\n")
	for _, id := range []string{"B1", "B2"} {
		if !strings.Contains(lint, filepath.Join(".code-forge", "worktrees", id)) {
			t.Errorf("fast gate lint must still run in %s's worktree: %q", id, lint)
		}
	}
	rebased := f.git(f.repo, "rev-parse", "block-B2")
	if rebased == heads["B2"] {
		t.Fatalf("B2 must be rebased onto B1's merge (new sha)")
	}
	if got := f.git(bare, "rev-parse", "refs/heads/ci-b2"); got != rebased {
		t.Errorf("remote ci-b2=%s, want the rebased B2 tip %s", got, rebased)
	}
	calls := strings.Join(landCICalls(t, gh), "\n")
	if strings.Count(calls, "run list --commit "+rebased+" ") != 3 {
		t.Errorf("the land must poll CI on the rebased tip until green:\n%s", calls)
	}
	for _, want := range []string{
		"gates: block B1 tip " + heads["B1"][:8] + " has green CI; local test gate skipped (B910)\n",
		"ci: block B2 rebased to " + rebased[:8] + "; CI proved " + heads["B2"][:8] + ", so the new tip needs its own green CI (B910)\n",
		"ci: pushed tip " + rebased[:8] + " to origin/ci-b2 over local\n",
		"ci: tip " + rebased[:8] + " green (1 runs)\n",
		"gates: block B2 tip " + rebased[:8] + " has green CI; local test gate skipped (B910)\n",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("output must contain %q:\n%s", want, output)
		}
	}
	if *slept == 0 {
		t.Errorf("the land must wait (fake clock) for CI on the rebased tip")
	}
}

// Without land.require_ci nothing changes: every block runs its local tests.
func TestB910CIOffKeepsLocalTests(t *testing.T) {
	f := landB910Fixture(t, nil)
	gh := landCIFakeGh(t, `[]`, nil)
	if code, output := f.land("B1", "B2"); code != 0 {
		t.Fatalf("exit=%d, want 0: %s", code, output)
	}
	if calls := f.lines("changed-calls"); len(calls) != 2 {
		t.Errorf("local test gate calls=%v, want one per block with the CI gate off", calls)
	}
	if calls := landCICalls(t, gh); len(calls) != 0 {
		t.Errorf("gh calls=%q, want none with the gate off", calls)
	}
}
