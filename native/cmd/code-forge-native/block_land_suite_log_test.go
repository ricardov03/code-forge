package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// Lines 4774-4800 of plans/tools/forge/b370/full-suite-pm4.log: the node spec
// reporter's failing-tests block from a real merged full suite.
const landSuiteSpecSample = `✖ failing tests:


test at test/cli/verb-help.test.mjs:81:1
✖ every discovered verb prints its usage for either leading help flag without effects (1773.4505ms)
  AssertionError [ERR_ASSERTION]: cloud-coder --help: code-forge: unknown verb "cloud-coder"
  Verbs:
    author
    autopilot
    block
    context
    doctor
    facts
    gates
    help
    init
    jev
    keys
    land
    ledger
    list
    logs
    models
    native-status
    outcome
    pipeline
    plan
    proof
`

// The running output repeats each failing name before the summary block; only
// the summary block may be counted.
const landSuiteRunningLine = "✖ every discovered verb prints its usage for either leading help flag without effects (1773.4505ms)\n"

const landSuiteSampleName = "every discovered verb prints its usage for either leading help flag without effects (test/cli/verb-help.test.mjs:81)"

// The batch fixture's suite prints $HOME/suite-output and fails when a block
// file holds "bad"; scoped checks do the same when $HOME/changed-fail exists.
func newLandSuiteLogFixture(t *testing.T, bad int, output string) *landBatchFixture {
	t.Helper()
	f := &landBatchFixture{t: t, home: t.TempDir(), repo: t.TempDir()}
	t.Setenv("HOME", f.home)
	t.Setenv("CODE_FORGE_NODE", "/nonexistent")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	oldVersion := reviewWaitNodeVersion
	reviewWaitNodeVersion = "v24.22.1"
	t.Cleanup(func() { reviewWaitNodeVersion = oldVersion })
	f.write(f.home, "suite-output", output)
	f.git(f.repo, "init", "-qb", "main")
	f.git(f.repo, "config", "user.name", "Fixture")
	f.git(f.repo, "config", "user.email", "f@example.invalid")
	f.git(f.repo, "config", "commit.gpgsign", "false")
	f.write(f.repo, ".gitignore", ".code-forge/\n")
	f.write(f.repo, "scoped.test.mjs", "// scoped fixture\n")
	command := `if [ "$#" -gt 0 ]; then
  if [ -f "$HOME/changed-fail" ]; then cat "$HOME/suite-output"; exit 1; fi
  exit 0
fi
for id in B1 B2 B3; do
  if [ -f "$id.mjs" ] && [ "$(cat "$id.mjs")" = bad ]; then cat "$HOME/suite-output"; exit 1; fi
done
printf 'ok 1 - integration contract\n'`
	cfg := landTestConfig([]string{"/bin/sh", "-c", command, "gate", "{test_files}"})
	cfg["gates"].(map[string]any)["rerun_failed"] = 0
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.write(f.repo, ".code-forge.yml", string(data))
	f.git(f.repo, "add", ".")
	f.git(f.repo, "commit", "-qm", "base")
	f.base = f.git(f.repo, "rev-parse", "HEAD")
	blocks := ledger.Object{}
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("B%d", i)
		tree := filepath.Join(f.repo, ".code-forge/worktrees", id)
		f.git(f.repo, "worktree", "add", "-qb", "block-"+id, tree, f.base)
		content := "green\n"
		if i == bad {
			content = "bad\n"
		}
		f.write(tree, id+".mjs", content)
		f.git(tree, "add", ".")
		f.git(tree, "commit", "-qm", id)
		blocks.Set(id, ledger.Object{{Key: "status", Value: "closed"}, {Key: "owned_files", Value: []any{id + ".mjs"}}, {Key: "kind", Value: "code"}, {Key: "base_sha", Value: f.base}, {Key: "base_branch", Value: "main"}, {Key: "test_files", Value: []any{"scoped.test.mjs"}}, {Key: "worktree", Value: ledger.Object{{Key: "path", Value: tree}, {Key: "branch", Value: "block-" + id}, {Key: "base_sha", Value: f.base}}}})
	}
	record := ledger.Object{{Key: "run_id", Value: "r-batch-unit"}, {Key: "project", Value: "fixture"}, {Key: "workspace", Value: f.repo}, {Key: "status", Value: "active"}, {Key: "blocks", Value: blocks}, {Key: "orphans", Value: []any{}}}
	if err := state.SaveRun(f.home, record); err != nil {
		t.Fatal(err)
	}
	f.write(f.home, ".code-forge/runs/r-batch-unit.key", string(make([]byte, 32)))
	return f
}

var landFullLogLine = regexp.MustCompile(`(?m)^full log: (.+)$`)

// checkFullLog wants exactly one "full log: <path>" line naming a file under
// the run's land-logs folder for block id that holds the whole suite output.
func checkFullLog(t *testing.T, f *landBatchFixture, output, id, suite string) {
	t.Helper()
	lines := landFullLogLine.FindAllStringSubmatch(output, -1)
	if len(lines) != 1 {
		t.Fatalf("full log lines=%d, want exactly 1: %s", len(lines), output)
	}
	path := lines[0][1]
	dir := filepath.Join(f.home, ".code-forge", "runs", "r-batch-unit", "land-logs")
	if filepath.Dir(path) != dir || !strings.HasPrefix(filepath.Base(path), id+"-") || !strings.HasSuffix(path, ".log") {
		t.Errorf("full log path=%s, want %s/%s-<time>.log", path, dir, id)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("full log %s unreadable: %v", path, err)
	}
	if strings.Count(string(data), suite) != 1 {
		t.Errorf("full log %s holds the whole suite output %d times, want 1:\n%s", path, strings.Count(string(data), suite), data)
	}
}

func TestLandSuiteLogBatchMergeGateNamesSpecFailures(t *testing.T) {
	suite := landSuiteRunningLine + "not ok 3 - tap contract\n" + landSuiteSpecSample
	f := newLandSuiteLogFixture(t, 2, suite)
	code, output := f.land("B1", "B2", "B3")
	want := "block B2 land stopped at merge-gate: full suite failed:\ntap contract\n" + landSuiteSampleName + "\nfull log: "
	if code != 1 || strings.Count(output, want) != 1 {
		t.Fatalf("exit=%d, want 1 and exactly one block %q: %s", code, want, output)
	}
	if n := strings.Count(output, "every discovered verb prints its usage"); n != 1 {
		t.Errorf("sample test named %d times, want 1: %s", n, output)
	}
	checkFullLog(t, f, output, "B2", suite)
}

func TestLandSuiteLogBatchChangedGatesKeepWholeLog(t *testing.T) {
	f := newLandSuiteLogFixture(t, 0, landSuiteSpecSample)
	f.write(f.home, "changed-fail", "1\n")
	code, output := f.land("B1", "B2")
	want := "block B1 land stopped at gates: changed gates failed: full suite failed:\n" + landSuiteSampleName + "\nfull log: "
	if code != 1 || strings.Count(output, want) != 1 {
		t.Fatalf("exit=%d, want 1 and exactly one block %q: %s", code, want, output)
	}
	checkFullLog(t, f, output, "B1", landSuiteSpecSample)
}

func TestLandSuiteLogNamesAtMostTenOnce(t *testing.T) {
	var suite strings.Builder
	suite.WriteString("✖ failing tests:\n\n")
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(&suite, "test at test/t%d.test.mjs:%d:1\n✖ case %d (%d.5ms)\n  AssertionError: case %d\n\n", i, i, i, i, i)
	}
	// A repeated entry (same name and place) is named once.
	suite.WriteString("test at test/t1.test.mjs:1:1\n✖ case 1 (9.5ms)\n")
	f := newLandSuiteLogFixture(t, 2, suite.String())
	code, output := f.land("B1", "B2")
	names := []string{}
	for i := 1; i <= 10; i++ {
		names = append(names, fmt.Sprintf("case %d (test/t%d.test.mjs:%d)", i, i, i))
	}
	want := "block B2 land stopped at merge-gate: full suite failed:\n" + strings.Join(names, "\n") + "\nfull log: "
	if code != 1 || strings.Count(output, want) != 1 {
		t.Fatalf("exit=%d, want 1 and exactly one block %q: %s", code, want, output)
	}
	for _, extra := range []string{"case 11", "case 12"} {
		if strings.Contains(output, extra) {
			t.Errorf("%s named, want only the first 10: %s", extra, output)
		}
	}
	checkFullLog(t, f, output, "B2", suite.String())
}
