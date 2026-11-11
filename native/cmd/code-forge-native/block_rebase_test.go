package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/reviewq"
	"github.com/ricardov03/code-forge/native/state"
)

// B431: `block rebase <id> --run <r>` of an open worktree block runs in Go.
// Node is unreachable here (CODE_FORGE_NODE=/nonexistent): a hand-over fails.
type rebaseCmdFixture struct {
	t                      *testing.T
	home, repo, tree, base string
}

const rebaseCmdChangelog = "# Changelog\n\n## [Unreleased]\n\n- older entry (B1)\n"

func newRebaseCmdFixture(t *testing.T, files map[string]string) *rebaseCmdFixture {
	t.Helper()
	root := t.TempDir()
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	f := &rebaseCmdFixture{t: t, home: filepath.Join(root, "home"), repo: filepath.Join(root, "repo"), tree: filepath.Join(root, "wt")}
	for _, dir := range []string{f.home, f.repo} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", f.home)
	t.Setenv("CODE_FORGE_NODE", "/nonexistent")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	f.git(f.repo, "init", "-qb", "main")
	f.git(f.repo, "config", "user.name", "Fixture")
	f.git(f.repo, "config", "user.email", "f@example.invalid")
	f.git(f.repo, "config", "commit.gpgsign", "false")
	f.write(f.repo, ".gitignore", ".code-forge/\n")
	for name, body := range files {
		f.write(f.repo, name, body)
	}
	f.git(f.repo, "add", ".")
	f.git(f.repo, "commit", "-qm", "base")
	f.base = f.git(f.repo, "rev-parse", "HEAD")
	f.git(f.repo, "worktree", "add", "-qb", "forge/r-rebase-unit/A", f.tree, f.base)
	return f
}

func (f *rebaseCmdFixture) write(dir, name, body string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *rebaseCmdFixture) git(dir string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = dir, gitx.ChildEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *rebaseCmdFixture) save(owned ...any) {
	f.t.Helper()
	blocks := ledger.Object{{Key: "A", Value: ledger.Object{{Key: "status", Value: "open"}, {Key: "owned_files", Value: owned}, {Key: "kind", Value: "code"}, {Key: "base_sha", Value: f.base}, {Key: "base_branch", Value: "main"},
		{Key: "worktree", Value: ledger.Object{{Key: "path", Value: f.tree}, {Key: "branch", Value: "forge/r-rebase-unit/A"}, {Key: "base_sha", Value: f.base}}}}}}
	record := ledger.Object{{Key: "run_id", Value: "r-rebase-unit"}, {Key: "project", Value: "fixture"}, {Key: "workspace", Value: f.repo}, {Key: "status", Value: "active"}, {Key: "blocks", Value: blocks}, {Key: "orphans", Value: []any{}}}
	if err := state.SaveRun(f.home, record); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.home, ".code-forge", "runs", "r-rebase-unit.key"), bytes.Repeat([]byte{7}, 32), 0600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *rebaseCmdFixture) rebase(args ...string) (int, string, string) {
	f.t.Helper()
	files := [2]*os.File{}
	for i := range files {
		file, err := os.CreateTemp(f.home, "output-")
		if err != nil {
			f.t.Fatal(err)
		}
		files[i] = file
	}
	out, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = files[0], files[1]
	code := runBlockRebase(append([]string{"A", "--run", "r-rebase-unit"}, args...))
	os.Stdout, os.Stderr = out, stderr
	texts := [2]string{}
	for i, file := range files {
		if err := file.Close(); err != nil {
			f.t.Fatal(err)
		}
		data, err := os.ReadFile(file.Name())
		if err != nil {
			f.t.Fatal(err)
		}
		texts[i] = string(data)
	}
	return code, texts[0], texts[1]
}

func (f *rebaseCmdFixture) block() ledger.Object {
	f.t.Helper()
	record, err := state.ReadRun(f.home, "r-rebase-unit")
	if err != nil {
		f.t.Fatal(err)
	}
	return blockObject(blockObject(record.Get("blocks")).Get("A"))
}

func (f *rebaseCmdFixture) appendBoth() {
	f.t.Helper()
	f.write(f.repo, "CHANGELOG.md", strings.Replace(rebaseCmdChangelog, "## [Unreleased]\n\n", "## [Unreleased]\n\n- main entry (B2)\n", 1))
	f.git(f.repo, "commit", "-qam", "B2 lands")
	f.write(f.tree, "CHANGELOG.md", strings.Replace(rebaseCmdChangelog, "## [Unreleased]\n\n", "## [Unreleased]\n\n- block entry (A)\n", 1))
	f.write(f.tree, "a.go", "package a\n\nfunc A() {}\n")
	f.git(f.tree, "commit", "-qam", "A")
}

func TestBlockRebaseOpenBlockRunsNativeReplay(t *testing.T) {
	f := newRebaseCmdFixture(t, map[string]string{"CHANGELOG.md": rebaseCmdChangelog, "a.go": "package a\n"})
	f.appendBoth()
	f.save("CHANGELOG.md", "a.go")

	code, stdout, stderr := f.rebase("--json")
	if code != 0 {
		t.Fatalf("exit=%d, want 0; stdout=%q stderr=%q", code, stdout, stderr)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not one JSON line: %q", stdout)
	}
	newBase := f.git(f.repo, "rev-parse", "main")
	want := map[string]any{"block": "A", "event": "rebased", "old_base": f.base, "new_base": newBase, "upstream_owned": []any{"CHANGELOG.md"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("json = %v, want %v", got, want)
	}
	if base := f.block().Get("base_sha"); base != newBase {
		t.Fatalf("base_sha = %v, want %s", base, newBase)
	}
	data, err := os.ReadFile(filepath.Join(f.tree, "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "- main entry (B2)\n- block entry (A)\n") {
		t.Fatalf("CHANGELOG.md = %q, want both lines", data)
	}
}

func TestBlockRebaseOpenBlockConflictText(t *testing.T) {
	f := newRebaseCmdFixture(t, map[string]string{"x.go": "one\ntwo\nthree\n"})
	f.write(f.repo, "x.go", "one\nmain\nthree\n")
	f.git(f.repo, "commit", "-qam", "main edits x.go")
	f.write(f.tree, "x.go", "one\nblock\nthree\n")
	f.git(f.tree, "commit", "-qam", "A edits x.go")
	head := f.git(f.tree, "rev-parse", "HEAD")
	f.save("x.go")
	before := f.block()
	files := f.stateFiles()

	code, stdout, stderr := f.rebase()
	if code != 1 || stdout != "" || !strings.HasPrefix(stderr, "block rebase: rebase-conflict: ") || !strings.Contains(stderr, "x.go") || strings.Count(stderr, "\n") != 1 {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want exit 1 and one rebase-conflict line naming x.go", code, stdout, stderr)
	}
	if got := f.git(f.tree, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD = %s, want %s", got, head)
	}
	if base := f.block().Get("base_sha"); base != f.base {
		t.Fatalf("base_sha = %v, want unchanged %s", base, f.base)
	}
	if status := f.git(f.tree, "status", "--porcelain"); status != "" {
		t.Fatalf("worktree not clean after the refusal: %q", status)
	}
	verify := exec.Command("git", "-C", f.tree, "rev-parse", "-q", "--verify", "CHERRY_PICK_HEAD")
	verify.Env = gitx.ChildEnv()
	if out, err := verify.CombinedOutput(); err == nil {
		t.Fatalf("cherry-pick left in progress: CHERRY_PICK_HEAD=%s", out)
	}
	for _, dir := range []string{"rebase-merge", "rebase-apply"} {
		p := f.git(f.tree, "rev-parse", "--path-format=absolute", "--git-path", dir)
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("rebase left in progress: %s exists (err=%v)", p, err)
		}
	}
	if after := f.block(); !reflect.DeepEqual(after, before) {
		t.Fatalf("block changed by the refusal:\nbefore %#v\nafter  %#v", before, after)
	}
	if after := f.stateFiles(); !reflect.DeepEqual(after, files) {
		t.Fatalf("the refusal changed the run state files:\nbefore %q\nafter  %q", files, after)
	}
}

// stateFiles reads every file under <home>/.code-forge (run record, ledger,
// key) and <workspace>/.code-forge (run mirror, review queue) as bytes, by
// path. The rebase writes its state only there.
func (f *rebaseCmdFixture) stateFiles() map[string]string {
	f.t.Helper()
	found := map[string]string{}
	for _, root := range []string{filepath.Join(f.home, ".code-forge"), filepath.Join(f.repo, ".code-forge")} {
		err := filepath.WalkDir(root, func(file string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			data, err := os.ReadFile(file)
			found[file] = string(data)
			return err
		})
		if err != nil && !os.IsNotExist(err) {
			f.t.Fatal(err)
		}
	}
	if _, ok := found[filepath.Join(f.home, ".code-forge", "runs", "r-rebase-unit.json")]; !ok {
		f.t.Fatalf("no run record among the state files: %q", found)
	}
	return found
}

// A pending checkpoint from review-file --rebase-check is finished by the
// plain rebase: the base moves and the checkpoint is gone.
func TestBlockRebaseFinishesPendingCheck(t *testing.T) {
	f := newRebaseCmdFixture(t, map[string]string{"CHANGELOG.md": rebaseCmdChangelog, "a.go": "package a\n"})
	f.appendBoth()
	f.save("CHANGELOG.md", "a.go")
	if _, err := reviewq.RebaseCheck(f.home, f.repo, "r-rebase-unit", "A", func(string) (string, bool) { return "", true }); err != nil {
		t.Fatalf("rebase check: %v", err)
	}
	if f.block().Get("rebase_check") == nil {
		t.Fatal("no pending checkpoint to finish")
	}

	code, stdout, stderr := f.rebase()
	if code != 0 || !strings.HasPrefix(stdout, "block A rebased ") || !strings.Contains(stdout, "upstream_owned: CHANGELOG.md") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	block := f.block()
	if block.Get("rebase_check") != nil || block.Get("base_sha") != f.git(f.repo, "rev-parse", "main") {
		t.Fatalf("block after rebase: rebase_check=%v base=%v", block.Get("rebase_check"), block.Get("base_sha"))
	}
}
