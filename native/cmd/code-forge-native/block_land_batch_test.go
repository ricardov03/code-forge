package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

type landBatchFixture struct {
	t          *testing.T
	home, repo string
	base       string
}

func newLandBatchFixture(t *testing.T, bad int) *landBatchFixture {
	t.Helper()
	f := &landBatchFixture{t: t, home: t.TempDir(), repo: t.TempDir()}
	t.Setenv("HOME", f.home)
	t.Setenv("CODE_FORGE_NODE", "/nonexistent")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	oldVersion := reviewWaitNodeVersion
	reviewWaitNodeVersion = "v24.22.1"
	t.Cleanup(func() { reviewWaitNodeVersion = oldVersion })
	f.git(f.repo, "init", "-qb", "main")
	f.git(f.repo, "config", "user.name", "Fixture")
	f.git(f.repo, "config", "user.email", "f@example.invalid")
	f.git(f.repo, "config", "commit.gpgsign", "false")
	f.write(f.repo, ".gitignore", ".code-forge/\n")
	f.write(f.repo, "scoped.test.mjs", "// scoped fixture\n")
	// Scoped checks and the full suite are counted independently. Every suite
	// records main's tree too, so a failed trial cannot briefly reach main.
	command := `if [ "$#" -gt 0 ]; then printf '%s\n' "$PWD" >> "$HOME/changed-calls"; exit 0; fi
printf '%s\n' "$PWD" >> "$HOME/full-calls"
git -C "$HOME/main-path" rev-parse HEAD >> "$HOME/observed-main"
for id in B1 B2 B3; do
  if [ -f "$id.mjs" ] && [ "$(cat "$id.mjs")" = bad ]; then printf 'not ok 1 - %s integration contract\n' "$id"; exit 1; fi
done
printf 'ok 1 - integration contract\n'`
	command = strings.ReplaceAll(command, `"$HOME/main-path"`, closeCommand([]string{f.repo}))
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

func (f *landBatchFixture) write(dir, name, content string) {
	f.t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *landBatchFixture) git(dir string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = dir, gitx.ChildEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *landBatchFixture) land(ids ...string) (int, string) {
	f.t.Helper()
	capture, err := os.CreateTemp(f.home, "output-")
	if err != nil {
		f.t.Fatal(err)
	}
	out, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = capture, capture
	defer func() { os.Stdout, os.Stderr = out, stderr }()
	code := runBlockLand(append(ids, "--run", "r-batch-unit"))
	if err := capture.Close(); err != nil {
		f.t.Fatal(err)
	}
	data, err := os.ReadFile(capture.Name())
	if err != nil {
		f.t.Fatal(err)
	}
	return code, string(data)
}

func (f *landBatchFixture) lines(name string) []string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.home, name))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return strings.Fields(string(data))
}

func (f *landBatchFixture) checkMain(ids []string) {
	f.t.Helper()
	commits := strings.Fields(f.git(f.repo, "rev-list", "--first-parent", "--reverse", f.base+"..main"))
	if len(commits) != len(ids) {
		f.t.Fatalf("main merge count=%d, want %d (%v)", len(commits), len(ids), ids)
	}
	parent := f.base
	for i, commit := range commits {
		parents := strings.Fields(f.git(f.repo, "show", "-s", "--format=%P", commit))
		branch := f.git(f.repo, "rev-parse", "block-"+ids[i])
		if !reflect.DeepEqual(parents, []string{parent, branch}) {
			f.t.Errorf("merge %d (%s) parents=%v, want [%s %s]", i, ids[i], parents, parent, branch)
		}
		parent = commit
	}
	for _, seen := range f.lines("observed-main") {
		if seen != f.base {
			f.t.Errorf("main held %s during the suite, want original %s", seen, f.base)
		}
	}
	if worktrees := f.git(f.repo, "worktree", "list", "--porcelain"); strings.Contains(worktrees, ".merge-gate-") {
		f.t.Errorf("trial worktree count must be 0: %s", worktrees)
	}
}

func TestBlockLandBatchOrderedSuite(t *testing.T) {
	f := newLandBatchFixture(t, 0)
	// Deliberately differ from lexical order.
	ids := []string{"B3", "B1", "B2"}
	code, output := f.land(ids...)
	if code != 0 {
		t.Fatalf("batch exit=%d, want 0: %s", code, output)
	}
	f.checkMain(ids)
	if calls := f.lines("full-calls"); len(calls) != 1 || !strings.Contains(calls[0], ".merge-gate-") {
		t.Errorf("full suite calls=%v, want exactly one in the trial", calls)
	}
	if calls := f.lines("changed-calls"); len(calls) != 3 {
		t.Errorf("changed gate count=%d, want 3", len(calls))
	}
	rows, err := ledger.ReadAllRows(filepath.Join(f.home, ".code-forge/ledger"), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	timings, batches := []string{}, 0
	for _, value := range rows {
		row := value.(map[string]any)
		if row["event"] == "land.timing" {
			timings = append(timings, row["block"].(string))
		}
		if row["event"] == "land.batch" {
			batches++
			wantTree := f.git(f.repo, "rev-parse", "main^{tree}")
			if got, _ := json.Marshal(row["blocks"]); string(got) != `["B3","B1","B2"]` || row["tree"] != wantTree || row["suite_ms"] == nil {
				t.Errorf("batch row=%v, want ordered blocks, tree=%s and suite_ms", row, wantTree)
			}
		}
	}
	if !reflect.DeepEqual(timings, ids) || batches != 1 {
		t.Errorf("timing blocks=%v batch rows=%d, want %v and 1", timings, batches, ids)
	}
}

func TestBlockLandBatchBisectPrefix(t *testing.T) {
	for _, bad := range []int{2, 1, 3} {
		t.Run(fmt.Sprintf("failure-%d", bad), func(t *testing.T) {
			f := newLandBatchFixture(t, bad)
			ids := []string{"B1", "B2", "B3"}
			code, output := f.land(ids...)
			culprit := ids[bad-1]
			if code != 1 || strings.Count(output, "block "+culprit+" land stopped at merge-gate: full suite failed:\n"+culprit+" integration contract") != 1 {
				t.Fatalf("exit=%d, want 1 identifying %s and its failing test: %s", code, culprit, output)
			}
			f.checkMain(ids[:bad-1])
			for i, id := range ids {
				want := 0
				if i < bad-1 {
					want = 1
				}
				if got := strings.Count(output, "block "+id+" landed\n"); got != want {
					t.Errorf("%s landed lines=%d, want %d: %s", id, got, want, output)
				}
				if i >= bad && strings.Count(output, "block "+id+" not landed (after "+culprit+")\n") != 1 {
					t.Errorf("%s must be reported once as not landed after %s: %s", id, culprit, output)
				}
			}
			calls := f.lines("full-calls")
			wantCalls := 3
			if bad == 1 {
				wantCalls = 2
			}
			if len(calls) != wantCalls {
				t.Errorf("bisect calls=%v, want exactly %d", calls, wantCalls)
			}
			for _, call := range calls {
				if call != calls[0] {
					t.Errorf("bisect cwd=%s, want one trial worktree %s", call, calls[0])
				}
			}
			if bad == 2 {
				// Repair and retry the remaining batch: the failed trial must not
				// poison a subsequent green invocation or merge B1 twice.
				tree := filepath.Join(f.repo, ".code-forge/worktrees/B2")
				f.write(tree, "B2.mjs", "green\n")
				f.git(tree, "commit", "-qam", "repair")
				code, output = f.land("B2", "B3")
				if code != 0 || strings.Count(output, " landed\n") != 2 || len(f.lines("full-calls")) != 4 {
					t.Errorf("red then green retry exit=%d suite calls=%d, want 0 and 4: %s", code, len(f.lines("full-calls")), output)
				}
				if count := f.git(f.repo, "rev-list", "--count", "--first-parent", f.base+"..main"); count != "3" {
					t.Errorf("retry main merge count=%s, want 3", count)
				}
			}
		})
	}
}

func TestBlockLandBatchRefusesUncheckedBlock(t *testing.T) {
	for _, shape := range []string{"open", "unclaimed", "dirty", "duplicate"} {
		t.Run(shape, func(t *testing.T) {
			f := newLandBatchFixture(t, 0)
			ids := []string{"B1", "B2", "B3"}
			record, err := state.ReadRun(f.home, "r-batch-unit")
			if err != nil {
				t.Fatal(err)
			}
			blocks := blockObject(record.Get("blocks"))
			entry := blockObject(blocks.Get("B2"))
			switch shape {
			case "open":
				entry.Set("status", "open")
			case "unclaimed":
				entry.Set("owned_files", []any{})
			case "dirty":
				f.write(filepath.Join(f.repo, ".code-forge/worktrees/B2"), "B2.mjs", "uncommitted\n")
			case "duplicate":
				ids[2] = "B2"
			}
			blocks.Set("B2", entry)
			record.Set("blocks", blocks)
			if err := state.SaveRun(f.home, record); err != nil {
				t.Fatal(err)
			}
			code, output := f.land(ids...)
			if code == 0 || !strings.Contains(output, "B2") || f.git(f.repo, "rev-parse", "main") != f.base || len(f.lines("full-calls")) != 0 {
				t.Errorf("%s refusal: exit=%d main=%s full calls=%d, want nonzero, unchanged main, 0 suites: %s", shape, code, f.git(f.repo, "rev-parse", "main"), len(f.lines("full-calls")), output)
			}
		})
	}
}
