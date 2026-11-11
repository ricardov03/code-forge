package reviewq

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B431: review-file --rebase-check of an open worktree block replays first.
type checkFixture struct {
	t                      *testing.T
	repo, tree, home, base string
}

func newCheckFixture(t *testing.T, files map[string]string) *checkFixture {
	t.Helper()
	root := t.TempDir()
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	f := &checkFixture{t: t, repo: filepath.Join(root, "repo"), tree: filepath.Join(root, "wt"), home: filepath.Join(root, "home")}
	if err := os.MkdirAll(f.repo, 0755); err != nil {
		t.Fatal(err)
	}
	f.git(f.repo, "init", "-qb", "main")
	f.write(f.repo, ".gitignore", ".code-forge/\n")
	for name, body := range files {
		f.write(f.repo, name, body)
	}
	f.base = f.commit(f.repo, "base")
	f.git(f.repo, "worktree", "add", "-qb", "forge/unit/A", f.tree, f.base)
	return f
}

func (f *checkFixture) git(dir string, args ...string) string {
	f.t.Helper()
	out, err := gitx.WithEnv("", gitx.ChildEnv(), append([]string{"-C", dir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	if err != nil {
		f.t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(out)
}

func (f *checkFixture) write(dir, name, body string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *checkFixture) commit(dir, message string) string {
	f.t.Helper()
	f.git(dir, "add", "-A")
	f.git(dir, "commit", "-qm", message)
	return f.git(dir, "rev-parse", "HEAD")
}

func (f *checkFixture) save(owned ...any) {
	f.t.Helper()
	blocks := ledger.Object{{Key: "A", Value: ledger.Object{{Key: "status", Value: "open"}, {Key: "owned_files", Value: owned}, {Key: "base_sha", Value: f.base}, {Key: "base_branch", Value: "main"},
		{Key: "worktree", Value: ledger.Object{{Key: "path", Value: f.tree}, {Key: "branch", Value: "forge/unit/A"}, {Key: "base_sha", Value: f.base}}}}}}
	record := ledger.Object{{Key: "run_id", Value: "unit"}, {Key: "workspace", Value: f.repo}, {Key: "project", Value: "unit"}, {Key: "status", Value: "active"}, {Key: "blocks", Value: blocks}}
	if err := state.SaveRun(f.home, record); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.home, ".code-forge", "runs", "unit.key"), bytes.Repeat([]byte{7}, 32), 0600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *checkFixture) check() (ledger.Object, error) {
	return RebaseCheck(f.home, f.repo, "unit", "A", func(string) (string, bool) { return "", true })
}

func (f *checkFixture) block() ledger.Object {
	f.t.Helper()
	record, err := state.ReadRun(f.home, "unit")
	if err != nil {
		f.t.Fatal(err)
	}
	return record.Get("blocks").(ledger.Object).Get("A").(ledger.Object)
}

const checkChangelog = "# Changelog\n\n## [Unreleased]\n\n- older entry (B1)\n"

func TestRebaseCheckOpenBlockReplaysUpstreamOwned(t *testing.T) {
	f := newCheckFixture(t, map[string]string{"CHANGELOG.md": checkChangelog, "y.txt": "schema v1\n", "a.go": "a\n"})
	f.write(f.repo, "CHANGELOG.md", strings.Replace(checkChangelog, "## [Unreleased]\n\n", "## [Unreleased]\n\n- main entry (B2)\n", 1))
	f.write(f.repo, "y.txt", "schema v2\n")
	f.commit(f.repo, "B2 lands")
	f.write(f.tree, "CHANGELOG.md", strings.Replace(checkChangelog, "## [Unreleased]\n\n", "## [Unreleased]\n\n- block entry (A)\n", 1))
	f.write(f.tree, "a.go", "a block\n")
	f.commit(f.tree, "A")
	f.save("CHANGELOG.md", "y.txt", "a.go")

	result, err := f.check()
	if err != nil {
		t.Fatalf("rebase check refused: %v", err)
	}
	queued := []string{}
	items, _ := result.Get("files").([]any)
	for _, item := range items {
		row := item.(ledger.Object)
		queued = append(queued, fmt.Sprint(row.Get("file"))+":"+fmt.Sprint(row.Get("status")))
	}
	if want := []string{"CHANGELOG.md:queued", "y.txt:queued"}; !reflect.DeepEqual(queued, want) {
		t.Fatalf("queued = %v, want %v", queued, want)
	}
	data, err := os.ReadFile(filepath.Join(f.tree, "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(checkChangelog, "## [Unreleased]\n\n", "## [Unreleased]\n\n- main entry (B2)\n- block entry (A)\n", 1); string(data) != want {
		t.Fatalf("CHANGELOG.md =\n%s\nwant:\n%s", data, want)
	}
	if got := f.block().Get("rebase_check"); got == nil {
		t.Fatal("no pending rebase checkpoint was signed")
	}
	if status := f.git(f.tree, "status", "--porcelain"); status != "" {
		t.Fatalf("worktree not clean: %q", status)
	}
}

func TestRebaseCheckOpenBlockConflictNamesFiles(t *testing.T) {
	f := newCheckFixture(t, map[string]string{"x.go": "one\ntwo\nthree\n"})
	f.write(f.repo, "x.go", "one\nmain\nthree\n")
	f.commit(f.repo, "main edits x.go")
	f.write(f.tree, "x.go", "one\nblock\nthree\n")
	head := f.commit(f.tree, "A edits x.go")
	f.save("x.go")

	_, err := f.check()
	if e, ok := err.(*state.Error); !ok || e.Code != "rebase-conflict" || !strings.Contains(e.Message, "x.go") {
		t.Fatalf("err = %#v, want rebase-conflict naming x.go", err)
	}
	if got := f.git(f.tree, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD = %s, want entry HEAD %s", got, head)
	}
	if status := f.git(f.tree, "status", "--porcelain"); status != "" {
		t.Fatalf("worktree not clean: %q", status)
	}
	if _, err := gitx.WithEnv("", gitx.ChildEnv(), "-C", f.tree, "rev-parse", "-q", "--verify", "CHERRY_PICK_HEAD"); err == nil {
		t.Fatal("cherry-pick left in progress")
	}
	if got := f.block().Get("rebase_check"); got != nil {
		t.Fatalf("refusal signed a checkpoint: %v", got)
	}
}
