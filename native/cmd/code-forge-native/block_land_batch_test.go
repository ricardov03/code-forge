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
	"time"

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
	f.write(f.repo, gatesGoTest, "// go build, vet and test\n")
	// Each block's import-graph dependent: it imports the file the block adds.
	for i := 1; i <= 3; i++ {
		f.write(f.repo, fmt.Sprintf("test/B%d.test.mjs", i), fmt.Sprintf("import '../src/B%d.mjs';\n", i))
	}
	// B404: every gate gets its selected test files. Calls are counted by
	// where they run (block worktree or merge-gate trial); a call with no file
	// is a full suite and must never happen. Only the merged trial sees the
	// integration contract, and it records main's tree so a failed trial cannot
	// briefly reach main.
	command := `case "$PWD" in *.merge-gate-*) log=merge ;; *) log=changed ;; esac
if [ "$#" -eq 0 ]; then printf '%s\n' "$PWD" >> "$HOME/full-calls"; fi
printf '%s\n' "$PWD" >> "$HOME/$log-calls"
printf '%s\n' "$*" >> "$HOME/$log-args"
if [ "$log" = changed ]; then exit 0; fi
if [ -f "$HOME/slow-full" ]; then sleep 30; fi
if [ -f "$HOME/slow-3" ]; then sleep 3; fi
git -C "$HOME/main-path" rev-parse HEAD >> "$HOME/observed-main"
for id in B1 B2 B3; do
  for file in "src/$id.mjs" "native/$id.mjs"; do
    if [ -f "$file" ] && [ "$(cat "$file")" = bad ]; then printf 'not ok 1 - %s integration contract\n' "$id"; exit 1; fi
  done
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
		// Unique content keeps Git rename detection from pairing two blocks' files.
		content := "green " + id + "\n"
		if i == bad {
			content = "bad\n"
		}
		// src/ is a Node product path; the merge gate runs its selected tests (B404).
		f.write(tree, "src/"+id+".mjs", content)
		f.git(tree, "add", ".")
		f.git(tree, "commit", "-qm", id)
		blocks.Set(id, ledger.Object{{Key: "status", Value: "closed"}, {Key: "owned_files", Value: []any{"src/" + id + ".mjs"}}, {Key: "kind", Value: "code"}, {Key: "base_sha", Value: f.base}, {Key: "base_branch", Value: "main"}, {Key: "test_files", Value: []any{"scoped.test.mjs"}}, {Key: "worktree", Value: ledger.Object{{Key: "path", Value: tree}, {Key: "branch", Value: "block-" + id}, {Key: "base_sha", Value: f.base}}}})
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

// argLines are the test-file arguments of each logged call, one line per call.
func (f *landBatchFixture) argLines(name string) []string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.home, name))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
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
	if calls := f.lines("merge-calls"); len(calls) != 1 || !strings.Contains(calls[0], ".merge-gate-") {
		t.Errorf("merge gate calls=%v, want exactly one in the trial", calls)
	}
	if calls := f.lines("changed-calls"); len(calls) != 3 {
		t.Errorf("changed gate count=%d, want 3", len(calls))
	}
	// B404: no gate runs the full suite; each gets its import-graph tests.
	if calls := f.lines("full-calls"); len(calls) != 0 {
		t.Errorf("full suite calls=%v, want none", calls)
	}
	if got, want := f.argLines("changed-args"), []string{"scoped.test.mjs test/B3.test.mjs", "scoped.test.mjs test/B1.test.mjs", "scoped.test.mjs test/B2.test.mjs"}; !reflect.DeepEqual(got, want) {
		t.Errorf("per-block gate files=%q, want %q", got, want)
	}
	if got, want := f.argLines("merge-args"), []string{"scoped.test.mjs test/B1.test.mjs test/B2.test.mjs test/B3.test.mjs"}; !reflect.DeepEqual(got, want) {
		t.Errorf("merge gate files=%q, want %q", got, want)
	}
	if !strings.Contains(output, "merge gate: 4 selected test files (import_graph, node_product)\n") {
		t.Errorf("want the merge gate selection line: %s", output)
	}
	rows, err := ledger.ReadAllRows(filepath.Join(f.home, ".code-forge/ledger"), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	timings, batches, done, deferred := []string{}, 0, 0, 0
	for _, value := range rows {
		row := value.(map[string]any)
		switch row["event"] {
		case "gate.done":
			done++
		case "gate.deferred":
			deferred++
		}
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
	if done != 3 || deferred != 0 {
		t.Errorf("native-safe blocks gate.done rows=%d gate.deferred rows=%d, want 3 and 0", done, deferred)
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
			calls := f.lines("merge-calls")
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
				f.write(tree, "src/B2.mjs", "green\n")
				f.git(tree, "commit", "-qam", "repair")
				code, output = f.land("B2", "B3")
				if code != 0 || strings.Count(output, " landed\n") != 2 || len(f.lines("merge-calls")) != 4 {
					t.Errorf("red then green retry exit=%d suite calls=%d, want 0 and 4: %s", code, len(f.lines("merge-calls")), output)
				}
				if count := f.git(f.repo, "rev-list", "--count", "--first-parent", f.base+"..main"); count != "3" {
					t.Errorf("retry main merge count=%s, want 3", count)
				}
			}
		})
	}
}

// nativeScope moves each block's file under native/, so its changed gates
// need the full fallback. A block named in node also owns a bin/ file (bin/, not src/, so Git does
// not read the src/ to native/ moves as a directory rename).
func (f *landBatchFixture) nativeScope(ids []string, node string) {
	f.t.Helper()
	record, err := state.ReadRun(f.home, "r-batch-unit")
	if err != nil {
		f.t.Fatal(err)
	}
	blocks := blockObject(record.Get("blocks"))
	for _, id := range ids {
		tree := filepath.Join(f.repo, ".code-forge/worktrees", id)
		if err := os.Mkdir(filepath.Join(tree, "native"), 0700); err != nil {
			f.t.Fatal(err)
		}
		f.git(tree, "mv", "src/"+id+".mjs", "native/"+id+".mjs")
		owned := []any{"native/" + id + ".mjs"}
		if id == node {
			f.write(tree, "bin/"+id+"-node.mjs", "export {}\n")
			f.git(tree, "add", ".")
			owned = append(owned, "bin/"+id+"-node.mjs")
		}
		f.git(tree, "commit", "-qm", "native scope")
		entry := blockObject(blocks.Get(id))
		entry.Set("owned_files", owned)
		blocks.Set(id, entry)
	}
	record.Set("blocks", blocks)
	if err := state.SaveRun(f.home, record); err != nil {
		f.t.Fatal(err)
	}
}

func (f *landBatchFixture) rows(event string) []map[string]any {
	f.t.Helper()
	rows, err := ledger.ReadAllRows(filepath.Join(f.home, ".code-forge/ledger"), "fixture")
	if err != nil {
		f.t.Fatal(err)
	}
	found := []map[string]any{}
	for _, value := range rows {
		if row := value.(map[string]any); row["event"] == event {
			found = append(found, row)
		}
	}
	return found
}

func TestBlockLandBatchNativeScopeRunsSelectedTests(t *testing.T) {
	f := newLandBatchFixture(t, 0)
	ids := []string{"B1", "B2", "B3"}
	f.nativeScope(ids, "B1")
	code, output := f.land(ids...)
	if code != 0 {
		t.Fatalf("native-owned batch exit=%d, want 0: %s", code, output)
	}
	f.checkMain(ids)
	// B404: a native/ change no longer defers to a full suite. Each block runs
	// its own selection and the merge gate runs the batch's selection once.
	if calls := f.lines("merge-calls"); len(calls) != 1 || !strings.Contains(calls[0], ".merge-gate-") || len(f.lines("changed-calls")) != 3 || len(f.lines("full-calls")) != 0 {
		t.Errorf("merge calls=%v changed calls=%v full calls=%v, want 1, 3 and 0", calls, f.lines("changed-calls"), f.lines("full-calls"))
	}
	// The moved src/Bn.mjs stem-pairs (and was imported by) test/Bn.test.mjs.
	if got, want := f.argLines("merge-args"), []string{"scoped.test.mjs test/B1.test.mjs test/B2.test.mjs test/B3.test.mjs test/compat/go.test.mjs"}; !reflect.DeepEqual(got, want) {
		t.Errorf("merge gate files=%q, want %q", got, want)
	}
	done := []string{}
	for _, row := range f.rows("gate.done") {
		done = append(done, row["block"].(string))
	}
	if !reflect.DeepEqual(done, ids) || len(f.rows("gate.deferred")) != 0 {
		t.Errorf("gate.done blocks=%v gate.deferred rows=%d, want %v and 0", done, len(f.rows("gate.deferred")), ids)
	}
	if batch := f.rows("land.batch"); len(batch) != 1 || batch[0]["merge_gate_reason"] != "node_product" {
		t.Errorf("land.batch rows=%v, want one with merge_gate_reason node_product", batch)
	}
}

func TestBlockLandBatchDeferredBisectNamesCulprit(t *testing.T) {
	f := newLandBatchFixture(t, 2)
	ids := []string{"B1", "B2", "B3"}
	f.nativeScope(ids, "B1")
	code, output := f.land(ids...)
	if code != 1 || strings.Count(output, "block B2 land stopped at merge-gate: full suite failed:\nB2 integration contract") != 1 {
		t.Fatalf("exit=%d, want 1 naming B2 at merge-gate: %s", code, output)
	}
	f.checkMain(ids[:1])
	if strings.Count(output, "block B1 landed\n") != 1 || strings.Count(output, "block B3 not landed (after B2)\n") != 1 || strings.Contains(output, "block B2 landed") {
		t.Errorf("want B1 landed once, B2 not landed, B3 not landed after B2: %s", output)
	}
	calls := f.lines("merge-calls")
	if len(calls) != 3 || len(f.lines("full-calls")) != 0 {
		t.Errorf("bisect calls=%v full calls=%v, want exactly 3 scoped and no full suite", calls, f.lines("full-calls"))
	}
	for _, call := range calls {
		if !strings.Contains(call, ".merge-gate-") {
			t.Errorf("suite cwd=%s, want only the merge-gate trial", call)
		}
	}
}

func TestBlockLandBatchGoOnlyRunsGoTest(t *testing.T) {
	f := newLandBatchFixture(t, 0)
	ids := []string{"B1", "B2"}
	f.nativeScope(ids, "")
	code, output := f.land(ids...)
	if code != 0 || strings.Count(output, "merge gate: 4 selected test files (go_only, go_only)\n") != 1 {
		t.Fatalf("Go-only batch exit=%d, want 0 with one selection line: %s", code, output)
	}
	f.checkMain(ids)
	// B407/B404: a Go-only merge gate still runs go.test.mjs (go build, vet
	// and test), never the full Node suite.
	if got, want := f.argLines("merge-args"), []string{"scoped.test.mjs test/B1.test.mjs test/B2.test.mjs test/compat/go.test.mjs"}; !reflect.DeepEqual(got, want) || len(f.lines("full-calls")) != 0 {
		t.Errorf("merge gate files=%q full calls=%v, want %q and none", got, f.lines("full-calls"), want)
	}
	for _, id := range ids {
		if strings.Count(output, "block "+id+" landed\n") != 1 {
			t.Errorf("%s landed lines != 1: %s", id, output)
		}
	}
	if batch := f.rows("land.batch"); len(batch) != 1 || batch[0]["merge_gate_reason"] != "go_only" {
		t.Errorf("land.batch rows=%v, want one with merge_gate_reason go_only", batch)
	}
}

func TestBlockLandBatchEmptySelectionSkipsMergeGate(t *testing.T) {
	f := newLandBatchFixture(t, 0)
	record, err := state.ReadRun(f.home, "r-batch-unit")
	if err != nil {
		t.Fatal(err)
	}
	blocks := blockObject(record.Get("blocks"))
	for _, id := range []string{"B1", "B2"} {
		tree := filepath.Join(f.repo, ".code-forge/worktrees", id)
		// A Node product file nothing imports and no test names.
		f.write(tree, "bin/"+id+".txt", "notes "+id+"\n")
		if err := os.Remove(filepath.Join(tree, "src", id+".mjs")); err != nil {
			t.Fatal(err)
		}
		f.git(tree, "add", "-A")
		f.git(tree, "commit", "-qm", "untested file only")
		entry := blockObject(blocks.Get(id))
		entry.Set("owned_files", []any{"bin/" + id + ".txt"})
		entry.Set("test_files", []any{})
		blocks.Set(id, entry)
	}
	record.Set("blocks", blocks)
	if err := state.SaveRun(f.home, record); err != nil {
		t.Fatal(err)
	}
	code, output := f.land("B1", "B2")
	if code != 0 || strings.Count(output, "merge gate: no test selected (node_product); test gate skipped\n") != 1 {
		t.Fatalf("untested-file batch exit=%d, want 0 with one skip line: %s", code, output)
	}
	f.checkMain([]string{"B1", "B2"})
	if len(f.lines("merge-calls")) != 0 || len(f.lines("changed-calls")) != 0 || len(f.lines("full-calls")) != 0 {
		t.Errorf("merge=%v changed=%v full=%v, want no test command", f.lines("merge-calls"), f.lines("changed-calls"), f.lines("full-calls"))
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
			if code == 0 || !strings.Contains(output, "block B2 land stopped at preflight: "+reasons[shape]) || f.git(f.repo, "rev-parse", "main") != f.base || len(f.lines("merge-calls")) != 0 {
				t.Errorf("%s refusal: exit=%d main=%s full calls=%d, want nonzero, unchanged main, 0 suites: %s", shape, code, f.git(f.repo, "rev-parse", "main"), len(f.lines("merge-calls")), output)
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
	if f.git(f.repo, "rev-parse", "main") != f.base || !reflect.DeepEqual(f.heads(), heads) || len(f.lines("merge-calls")) != 0 {
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
	calls := len(f.lines("merge-calls"))
	code, output := f.land("B1", "B2")
	if code == 0 || !strings.Contains(output, "block B1 has already landed") || strings.Contains(output, "block B1 landed\n") || strings.Contains(output, "block B2 landed\n") {
		t.Errorf("relanding exit=%d, want refusal naming B1: %s", code, output)
	}
	if f.git(f.repo, "rev-parse", "main") != main || len(f.lines("merge-calls")) != calls {
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

// B426: a batch land holds no run lock through its merge gate. While its test
// command sleeps 3 s, a block write on another block of the same run succeeds
// within 1 s, and the land's final commit keeps that write.
func TestBlockLandBatchMergeGateLeavesRunUnlocked(t *testing.T) {
	f := newLandBatchFixture(t, 0)
	record, err := state.ReadRun(f.home, "r-batch-unit")
	if err != nil {
		t.Fatal(err)
	}
	blocks := record.Get("blocks").(ledger.Object)
	blocks.Set("B9", ledger.Object{{Key: "status", Value: "open"}, {Key: "owned_files", Value: []any{"src/B9.mjs"}}, {Key: "kind", Value: "code"}, {Key: "base_sha", Value: f.base}})
	record.Set("blocks", blocks)
	if err := state.SaveRun(f.home, record); err != nil {
		t.Fatal(err)
	}
	f.write(f.home, "slow-3", "")
	type landed struct {
		code   int
		output string
	}
	done := make(chan landed, 1)
	go func() {
		code, output := f.land("B1", "B2")
		done <- landed{code, output}
	}()
	// The merge gate logs its call before it sleeps.
	for deadline := time.Now().Add(60 * time.Second); len(f.lines("merge-calls")) == 0; {
		if time.Now().After(deadline) {
			t.Fatal("the merge gate never started")
		}
		select {
		case r := <-done:
			t.Fatalf("land ended before its merge gate: exit %d: %s", r.code, r.output)
		case <-time.After(20 * time.Millisecond):
		}
	}
	started := time.Now()
	err = state.UpdateBlock(f.home, "r-batch-unit", "B9", func(_, block *ledger.Object) ([]ledger.Object, error) {
		block.Set("note", "written during the merge gate")
		return nil, nil
	})
	elapsed := time.Since(started)
	if err != nil || elapsed > time.Second {
		t.Fatalf("write on block B9 during the merge gate: err=%v after %v, want success within 1s", err, elapsed)
	}
	select {
	case r := <-done:
		t.Fatalf("land ended before the other write finished (exit %d), the test proves nothing: %s", r.code, r.output)
	default:
	}
	r := <-done
	if r.code != 0 {
		t.Fatalf("land exit %d: %s", r.code, r.output)
	}
	after, err := state.ReadRun(f.home, "r-batch-unit")
	if err != nil {
		t.Fatal(err)
	}
	final := after.Get("blocks").(ledger.Object)
	if note := final.Get("B9").(ledger.Object).Get("note"); note != "written during the merge gate" {
		t.Fatalf("B9 note = %v after the land; the land's commit lost the other write", note)
	}
	for _, id := range []string{"B1", "B2"} {
		if final.Get(id).(ledger.Object).Get("landed") == nil {
			t.Fatalf("block %s has no landed marker: %v", id, final.Get(id))
		}
	}
}
