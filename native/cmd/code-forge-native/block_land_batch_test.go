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
	"github.com/ricardov03/code-forge/native/proof"
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
if [ -f "$HOME/slow-full" ]; then sleep 30; fi
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
	return f.landWith(nil, ids...)
}

func (f *landBatchFixture) landWith(extra []string, ids ...string) (int, string) {
	f.t.Helper()
	capture, err := os.CreateTemp(f.home, "output-")
	if err != nil {
		f.t.Fatal(err)
	}
	out, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = capture, capture
	defer func() { os.Stdout, os.Stderr = out, stderr }()
	args := append(append(append([]string{}, ids...), "--run", "r-batch-unit"), extra...)
	code := runBlockLand(args)
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

func TestBlockLandBatchNativeScopeReusesFinalTree(t *testing.T) {
	f := newLandBatchFixture(t, 0)
	record, err := state.ReadRun(f.home, "r-batch-unit")
	if err != nil {
		t.Fatal(err)
	}
	blocks := blockObject(record.Get("blocks"))
	ids := []string{"B1", "B2", "B3"}
	for _, id := range ids {
		tree := filepath.Join(f.repo, ".code-forge/worktrees", id)
		if err := os.Mkdir(filepath.Join(tree, "native"), 0700); err != nil {
			t.Fatal(err)
		}
		f.git(tree, "mv", id+".mjs", "native/"+id+".mjs")
		f.git(tree, "commit", "-qm", "native scope")
		entry := blockObject(blocks.Get(id))
		entry.Set("owned_files", []any{"native/" + id + ".mjs"})
		blocks.Set(id, entry)
	}
	record.Set("blocks", blocks)
	if err := state.SaveRun(f.home, record); err != nil {
		t.Fatal(err)
	}
	code, output := f.land(ids...)
	if code != 0 {
		t.Fatalf("native-owned batch exit=%d, want 0: %s", code, output)
	}
	f.checkMain(ids)
	// Native imports cannot be narrowed by the empty-graph planner. Its safe
	// fallback checks each rebased block fully; the last tree is the final
	// merge tree, so B317 must prevent a fourth suite in the trial worktree.
	wantCalls := []string{}
	for _, id := range ids {
		wantCalls = append(wantCalls, proof.Real(filepath.Join(f.repo, ".code-forge/worktrees", id)))
	}
	if calls := f.lines("full-calls"); !reflect.DeepEqual(calls, wantCalls) || len(f.lines("changed-calls")) != 0 {
		t.Errorf("native full calls=%v changed calls=%v, want %v and zero (reuse final tree)", calls, f.lines("changed-calls"), wantCalls)
	}
}

func TestBlockLandBatchRefusesUncheckedBlock(t *testing.T) {
	reasons := map[string]string{"open": "block B2 must be closed before batch landing", "unclaimed": "block B2 has unclaimed changes", "dirty": "block B2 worktree is dirty or unreadable"}
	for _, shape := range []string{"open", "unclaimed", "dirty"} {
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
			}
			blocks.Set("B2", entry)
			record.Set("blocks", blocks)
			if err := state.SaveRun(f.home, record); err != nil {
				t.Fatal(err)
			}
			code, output := f.land(ids...)
			if code == 0 || !strings.Contains(output, "block B2 land stopped at preflight: "+reasons[shape]) || f.git(f.repo, "rev-parse", "main") != f.base || len(f.lines("full-calls")) != 0 {
				t.Errorf("%s refusal: exit=%d main=%s full calls=%d, want nonzero, unchanged main, 0 suites: %s", shape, code, f.git(f.repo, "rev-parse", "main"), len(f.lines("full-calls")), output)
			}
		})
	}
}

func (f *landBatchFixture) entry(id string) ledger.Object {
	f.t.Helper()
	record, err := state.ReadRun(f.home, "r-batch-unit")
	if err != nil {
		f.t.Fatal(err)
	}
	return blockObject(blockObject(record.Get("blocks")).Get(id))
}

func (f *landBatchFixture) heads() map[string]string {
	f.t.Helper()
	heads := map[string]string{}
	for _, id := range []string{"B1", "B2", "B3"} {
		heads[id] = f.git(f.repo, "rev-parse", "block-"+id)
	}
	return heads
}

func (f *landBatchFixture) onMain(sha string) bool {
	f.t.Helper()
	cmd := exec.Command("git", "merge-base", "--is-ancestor", sha, "main")
	cmd.Dir, cmd.Env = f.repo, gitx.ChildEnv()
	return cmd.Run() == nil
}

func TestBlockLandBatchDuplicateIDUsage(t *testing.T) {
	f := newLandBatchFixture(t, 0)
	heads := f.heads()
	code, output := f.land("B1", "B2", "B1")
	if code != 2 || !strings.Contains(output, landArguments) {
		t.Errorf("duplicate ids exit=%d, want usage exit 2: %s", code, output)
	}
	if f.git(f.repo, "rev-parse", "main") != f.base || !reflect.DeepEqual(f.heads(), heads) || len(f.lines("full-calls")) != 0 {
		t.Errorf("duplicate ids must not change main, branches or run a suite: %s", output)
	}
}

func TestBlockLandBatchNotLandedKeepsOriginalBranch(t *testing.T) {
	f := newLandBatchFixture(t, 2)
	before := f.heads()
	code, output := f.land("B1", "B2", "B3")
	if code != 1 || !strings.Contains(output, "block B3 not landed (after B2)") {
		t.Fatalf("exit=%d, want 1 with B3 not landed after B2: %s", code, output)
	}
	f.checkMain([]string{"B1"})
	// B3 was rebased onto a trial merge holding failing B2; it must return to
	// its own commits and recorded base, never carrying B2 or an off-main base.
	if got := f.git(f.repo, "rev-parse", "block-B3"); got != before["B3"] {
		t.Errorf("B3 head=%s, want original %s", got, before["B3"])
	}
	b3 := f.entry("B3")
	if b3.Get("base_sha") != f.base || blockObject(b3.Get("worktree")).Get("base_sha") != f.base || b3.Get("landed") != nil {
		t.Errorf("B3 record base=%v worktree base=%v landed=%v, want original %s and not landed", b3.Get("base_sha"), blockObject(b3.Get("worktree")).Get("base_sha"), b3.Get("landed"), f.base)
	}
	for _, id := range []string{"B1", "B2", "B3"} {
		entry := f.entry(id)
		if base := blockString(entry.Get("base_sha")); !f.onMain(base) {
			t.Errorf("%s recorded base %s is not on main", id, base)
		}
		if (entry.Get("landed") != nil) != (id == "B1") || entry.Get("status") != "closed" {
			t.Errorf("%s status=%v landed=%v, want closed and landed only for B1", id, entry.Get("status"), entry.Get("landed"))
		}
	}
}

func TestBlockLandBatchMarksLandedAndRefusesRelanding(t *testing.T) {
	f := newLandBatchFixture(t, 0)
	ids := []string{"B1", "B2", "B3"}
	if code, output := f.land(ids...); code != 0 {
		t.Fatalf("batch exit=%d, want 0: %s", code, output)
	}
	main := f.git(f.repo, "rev-parse", "main")
	for _, id := range ids {
		entry := f.entry(id)
		landed := blockObject(entry.Get("landed"))
		if entry.Get("status") != "closed" || landed == nil || landed.Get("main") != main || !f.onMain(blockString(landed.Get("merge"))) {
			t.Errorf("%s status=%v landed=%v, want closed with landed main=%s", id, entry.Get("status"), entry.Get("landed"), main)
		}
	}
	calls := len(f.lines("full-calls"))
	code, output := f.land("B1", "B2")
	if code == 0 || !strings.Contains(output, "block B1 has already landed") || strings.Contains(output, "block B1 landed\n") || strings.Contains(output, "block B2 landed\n") {
		t.Errorf("relanding exit=%d, want refusal naming B1: %s", code, output)
	}
	if f.git(f.repo, "rev-parse", "main") != main || len(f.lines("full-calls")) != calls {
		t.Errorf("relanding must not move main or run a suite: %s", output)
	}
}

func TestBlockLandBatchTimeoutRefusesWholeBatch(t *testing.T) {
	f := newLandBatchFixture(t, 0)
	before := f.heads()
	f.write(f.home, "slow-full", "1\n")
	code, output := f.landWith([]string{"--max", "10s"}, "B1", "B2", "B3")
	if code != 1 || !strings.Contains(output, "block B1 B2 B3 land stopped at merge-gate: batch timed out after 10000ms") {
		t.Errorf("timeout exit=%d, want 1 refusing the whole batch: %s", code, output)
	}
	if strings.Contains(output, "not landed") || strings.Contains(output, "block B1 landed") || strings.Contains(output, "deadline exceeded") {
		t.Errorf("a timeout must name no culprit and land nothing: %s", output)
	}
	if f.git(f.repo, "rev-parse", "main") != f.base {
		t.Errorf("main moved after a timeout: %s", output)
	}
	if got := f.heads(); !reflect.DeepEqual(got, before) {
		t.Errorf("block heads=%v, want original %v after a refused batch", got, before)
	}
}

func TestBlockLandBatchRefusesZeroMaxAtPreflight(t *testing.T) {
	f := newLandBatchFixture(t, 0)
	before := f.heads()
	code, output := f.landWith([]string{"--max", "0s"}, "B1", "B2", "B3")
	if code == 0 || !strings.Contains(output, "land stopped at preflight: --max must be a positive duration") {
		t.Errorf("--max 0s exit=%d, want preflight refusal: %s", code, output)
	}
	if f.git(f.repo, "rev-parse", "main") != f.base || !reflect.DeepEqual(f.heads(), before) || len(f.lines("changed-calls")) != 0 {
		t.Errorf("--max 0s must not rebase, gate or merge: %s", output)
	}
	if code, output := f.landWith([]string{"--max", "soon"}, "B1", "B2"); code != 2 {
		t.Errorf("--max soon exit=%d, want usage 2: %s", code, output)
	}
}
