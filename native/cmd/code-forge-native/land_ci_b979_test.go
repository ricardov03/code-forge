package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/state"
)

// B979 tests: a single-block `block land --merge` with land.require_ci:
// github gets the batch's B910 test skip and B803 tip check. The fixture is
// the B789 in-process land (open block in a worktree, review approved); gh is
// a fake script on PATH and the CI clock is fake: no network, no real waits.

// landB979Fixture is the B789 open-block fixture whose test gate logs each
// run to $HOME/test-calls, with a lint gate logging to $HOME/lint-calls, and
// land.require_ci: github in the run's config snapshot.
func landB979Fixture(t *testing.T) *b789Fixture {
	t.Helper()
	f := newB789Fixture(t, true, "open")
	cfg := landTestConfig([]any{"/bin/sh", "-c", `printf '%s\n' "$PWD" >> "$HOME/test-calls"`})
	cfg["gates"].(map[string]any)["lint"] = []any{"/bin/sh", "-c", `printf '%s\n' "$PWD" >> "$HOME/lint-calls"`}
	cfg["proof"] = map[string]any{"isolation": "worktree"}
	cfg["native"] = map[string]any{"daemon": false}
	yml, err := json.Marshal(cfg)
	f.must(err)
	f.must(os.WriteFile(filepath.Join(f.repo, ".code-forge.yml"), yml, 0o600))
	f.git(f.repo, "add", ".code-forge.yml")
	f.git(f.repo, "commit", "-qm", "gates")
	f.git(f.tree, "rebase", "-q", "main")
	f.base = f.git(f.repo, "rev-parse", "main")
	landCISnapshot(t, f.home, f.run, cfg, map[string]any{"require_ci": "github"})
	record, err := state.ReadRun(f.home, f.run)
	f.must(err)
	blocks := blockObject(record.Get("blocks"))
	entry := blockObject(blocks.Get("B1"))
	entry.Set("base_sha", f.base)
	wt := blockObject(entry.Get("worktree"))
	wt.Set("base_sha", f.base)
	entry.Set("worktree", wt)
	blocks.Set("B1", entry)
	record.Set("blocks", blocks)
	f.must(state.SaveRun(f.home, record))
	f.record, f.entry = record, blockObject(blockObject(record.Get("blocks")).Get("B1"))
	t.Setenv(landCIRedBlocksEnv, "")
	return f
}

func (f *b789Fixture) lines(name string) []string {
	data, err := os.ReadFile(filepath.Join(f.home, name))
	if err != nil {
		return nil
	}
	return strings.Fields(string(data))
}

// The block tip has green CI: land runs lint but not the local test gate,
// says so, and merges the tip CI proved.
func TestB979SingleLandCIProvenSkipsLocalTest(t *testing.T) {
	f := landB979Fixture(t)
	tip := f.git(f.tree, "rev-parse", "HEAD")
	landCIFakeGhSeq(t, map[string]string{"runs-" + tip + ".json": landCIGreen})
	landCIFakeClock(t)
	f.approve()
	code, output, handed := f.land(map[string]any{"merge": true, "no-merge-gate": true})
	if handed != "" || code != 0 {
		t.Fatalf("land = %d handed=%q, want 0:\n%s", code, handed, output)
	}
	if calls := f.lines("test-calls"); len(calls) != 0 {
		t.Errorf("local test gate ran %v, want none: CI proved the tip", calls)
	}
	if calls := f.lines("lint-calls"); len(calls) == 0 {
		t.Errorf("fast gate lint must still run:\n%s", output)
	}
	want := "gates: block B1 tip " + tip[:8] + " has green CI; local test gate skipped (B910)\n"
	if !strings.Contains(output, want) {
		t.Errorf("output must contain %q:\n%s", want, output)
	}
	if got := f.git(f.repo, "rev-parse", "main^2"); got != tip {
		t.Errorf("merged %s, want the CI-proved tip %s", got, tip)
	}
}

// The block tip moves while land waits (with no lock) for its CI: land
// refuses at ci and main is unchanged.
func TestB979SingleLandTipMovedDuringWaitRefused(t *testing.T) {
	f := landB979Fixture(t)
	tip := f.git(f.tree, "rev-parse", "HEAD")
	landCIFakeGhSeq(t, map[string]string{
		"runs-" + tip + "-1.json": landCIRunning,
		"runs-" + tip + ".json":   landCIGreen,
	})
	landCIFakeClock(t)
	fakeSleep, moved := landCISleep, false
	landCISleep = func(d time.Duration) {
		if !moved {
			moved = true
			f.git(f.tree, "commit", "-q", "--allow-empty", "-m", "late change")
		}
		fakeSleep(d)
	}
	f.approve()
	main := f.git(f.repo, "rev-parse", "main")
	code, output, _ := f.land(map[string]any{"merge": true, "no-merge-gate": true})
	if code == 0 {
		t.Fatalf("exit=0, want a refusal:\n%s", output)
	}
	if !moved {
		t.Fatalf("the land never waited for CI:\n%s", output)
	}
	if !strings.Contains(output, "block B1 land stopped at ci: block B1 tip moved from "+tip[:8]) {
		t.Errorf("refusal must name the moved tip:\n%s", output)
	}
	if got := f.git(f.repo, "rev-parse", "main"); got != main {
		t.Errorf("main=%s, want unchanged %s", got, main)
	}
	if calls := f.lines("test-calls"); len(calls) != 0 {
		t.Errorf("no gate may run after the refusal: %v", calls)
	}
}
