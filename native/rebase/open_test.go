package rebase

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/queue"
	"github.com/ricardov03/code-forge/native/state"
)

// openFixture is a real repository with one open worktree block A of run
// "unit" (B431). base holds every file given to newOpenFixture; the test then
// commits on main (repo) and on the block (tree) before calling Run.
type openFixture struct {
	t                *testing.T
	root, repo, tree string
	home, base       string
	closed           ledger.Object
	owned            []string
}

func newOpenFixture(t *testing.T, owned []string, files map[string]string) *openFixture {
	t.Helper()
	root := t.TempDir()
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	f := &openFixture{t: t, root: root, repo: filepath.Join(root, "repo"), tree: filepath.Join(root, "wt"), home: filepath.Join(root, "home")}
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
	f.owned = owned
	return f
}

func (f *openFixture) git(dir string, args ...string) string {
	f.t.Helper()
	out, err := git(dir, append([]string{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	if err != nil {
		f.t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(out)
}

func (f *openFixture) write(dir, name, body string) {
	f.t.Helper()
	file := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(body), 0644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *openFixture) read(dir, name string) string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(data)
}

func (f *openFixture) commit(dir, message string) string {
	f.t.Helper()
	f.git(dir, "add", "-A")
	f.git(dir, "commit", "-qm", message)
	return f.git(dir, "rev-parse", "HEAD")
}

// save writes the run record and its signer key; call it after the commits.
func (f *openFixture) save() {
	f.t.Helper()
	owned := []any{}
	for _, file := range f.owned {
		owned = append(owned, file)
	}
	blocks := ledger.Object{
		{Key: "A", Value: ledger.Object{{Key: "status", Value: "open"}, {Key: "owned_files", Value: owned}, {Key: "base_sha", Value: f.base}, {Key: "base_branch", Value: "main"},
			{Key: "worktree", Value: ledger.Object{{Key: "path", Value: f.tree}, {Key: "branch", Value: "forge/unit/A"}, {Key: "base_sha", Value: f.base}}}}},
	}
	for _, field := range f.closed {
		blocks = append(blocks, field)
	}
	record := ledger.Object{{Key: "run_id", Value: "unit"}, {Key: "workspace", Value: f.repo}, {Key: "project", Value: "unit"}, {Key: "status", Value: "active"}, {Key: "blocks", Value: blocks}}
	if err := state.SaveRun(f.home, record); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.home, ".code-forge", "runs", "unit.key"), bytes.Repeat([]byte{7}, 32), 0600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *openFixture) blockBase() any {
	f.t.Helper()
	record, err := state.ReadRun(f.home, "unit")
	if err != nil {
		f.t.Fatal(err)
	}
	return record.Get("blocks").(ledger.Object).Get("A").(ledger.Object).Get("base_sha")
}

func (f *openFixture) rows() []map[string]any {
	f.t.Helper()
	rows, err := ledger.ReadRows(filepath.Join(f.home, ".code-forge", "ledger"), "unit")
	if err != nil {
		f.t.Fatal(err)
	}
	out := []map[string]any{}
	for _, row := range rows {
		if m, ok := row.(map[string]any); ok && m["event"] == "block.rebase" {
			out = append(out, m)
		}
	}
	return out
}

func (f *openFixture) queued() []string {
	f.t.Helper()
	idx := queue.Index{Dir: filepath.Join(f.repo, ".code-forge", "queue")}
	if err := idx.Refresh(); err != nil {
		f.t.Fatal(err)
	}
	tree := f.tree
	files := []string{}
	for _, ticket := range idx.Block("unit", "A", &tree, nil) {
		files = append(files, fmt.Sprint(ticket["file"]))
	}
	sort.Strings(files)
	return files
}

func strs(v any) []string {
	out := []string{}
	items, _ := v.([]any)
	for _, item := range items {
		out = append(out, fmt.Sprint(item))
	}
	return out
}

// requireRebased checks the base moved and the one block.rebase row lists
// upstream as upstream_owned.
func (f *openFixture) requireRebased(err error, upstream []string) map[string]any {
	f.t.Helper()
	if err != nil {
		f.t.Fatalf("rebase refused: %v", err)
	}
	if got, want := f.blockBase(), f.git(f.repo, "rev-parse", "main"); got != want {
		f.t.Fatalf("base_sha = %v, want main %s", got, want)
	}
	rows := f.rows()
	if len(rows) != 1 {
		f.t.Fatalf("block.rebase rows = %v, want one", rows)
	}
	if got := strs(rows[0]["upstream_owned"]); !reflect.DeepEqual(got, upstream) {
		f.t.Fatalf("row upstream_owned = %v, want %v (row %v)", got, upstream, rows[0])
	}
	if status := f.git(f.tree, "status", "--porcelain"); status != "" {
		f.t.Fatalf("worktree not clean after the rebase: %q", status)
	}
	if out := f.git(f.tree, "merge-base", "--is-ancestor", "main", "HEAD"); out != "" {
		f.t.Fatalf("unexpected output %q", out)
	}
	return rows[0]
}

// requireConflict checks a rebase-conflict naming files left everything as at entry.
func (f *openFixture) requireConflict(err error, head string, files ...string) {
	f.t.Helper()
	if err == nil || !strings.Contains(err.Error(), "rebase-conflict") {
		f.t.Fatalf("err = %v, want rebase-conflict", err)
	}
	for _, file := range files {
		if !strings.Contains(err.Error(), file) {
			f.t.Fatalf("refusal %q does not name %s", err, file)
		}
	}
	if strings.Contains(err.Error(), "\n") {
		f.t.Fatalf("refusal is not one line: %q", err)
	}
	if got := f.git(f.tree, "rev-parse", "HEAD"); got != head {
		f.t.Fatalf("HEAD = %s, want the entry HEAD %s", got, head)
	}
	if status := f.git(f.tree, "status", "--porcelain"); status != "" {
		f.t.Fatalf("worktree not clean after the refusal: %q", status)
	}
	for _, ref := range []string{"CHERRY_PICK_HEAD", "REBASE_HEAD"} {
		if _, err := git(f.tree, "rev-parse", "-q", "--verify", ref); err == nil {
			f.t.Fatalf("%s left in progress", ref)
		}
	}
	for _, dir := range []string{"rebase-merge", "rebase-apply"} {
		p := f.git(f.tree, "rev-parse", "--git-path", dir)
		if !filepath.IsAbs(p) {
			p = filepath.Join(f.tree, p)
		}
		if _, err := os.Stat(p); err == nil {
			f.t.Fatalf("%s left in progress", dir)
		}
	}
	if got := f.blockBase(); got != f.base {
		f.t.Fatalf("base_sha = %v after the refusal, want %s", got, f.base)
	}
	if rows := f.rows(); len(rows) != 0 {
		f.t.Fatalf("refusal wrote rows %v", rows)
	}
}

const changelogBase = "# Changelog\n\n## [Unreleased]\n\n- older entry (B1)\n\n## [0.1.0]\n\n- first release\n"

func withLine(body, after, line string) string {
	return strings.Replace(body, after, after+line, 1)
}

// Brief test 1: the block owns CHANGELOG.md and a.go; main adds a line under
// [Unreleased], the block adds another there. Old code: owned-conflict.
func TestOpenBlockReplaysChangelogAppend(t *testing.T) {
	f := newOpenFixture(t, []string{"CHANGELOG.md", "a.go"}, map[string]string{"CHANGELOG.md": changelogBase, "a.go": "package a\n"})
	f.write(f.repo, "CHANGELOG.md", withLine(changelogBase, "## [Unreleased]\n\n", "- main entry (B2)\n"))
	f.commit(f.repo, "B2 lands")
	f.write(f.tree, "CHANGELOG.md", withLine(changelogBase, "## [Unreleased]\n\n", "- block entry (A)\n"))
	f.write(f.tree, "a.go", "package a\n\nfunc A() {}\n")
	f.commit(f.tree, "A")
	f.save()

	_, err := Run(f.home, "unit", "A")
	f.requireRebased(err, []string{"CHANGELOG.md"})
	want := withLine(changelogBase, "## [Unreleased]\n\n", "- main entry (B2)\n- block entry (A)\n")
	if got := f.read(f.tree, "CHANGELOG.md"); got != want {
		t.Fatalf("CHANGELOG.md =\n%s\nwant main's line first, then the block's:\n%s", got, want)
	}
	if got := f.read(f.tree, "a.go"); got != "package a\n\nfunc A() {}\n" {
		t.Fatalf("a.go = %q, the block's change was lost", got)
	}
	// Only files whose content the rebase changed go back to review.
	if got := f.queued(); !reflect.DeepEqual(got, []string{"CHANGELOG.md"}) {
		t.Fatalf("queued for re-review = %v, want [CHANGELOG.md]", got)
	}
}

func numbered(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

// Brief tests 2 and 4: main edits x.go lines 1-3 and y.txt, the block edits
// x.go line 50 only and never touches y.txt. Old code: owned-conflict.
func TestOpenBlockReplaysDisjointEdits(t *testing.T) {
	f := newOpenFixture(t, []string{"x.go", "y.txt"}, map[string]string{"x.go": numbered(60), "y.txt": "schema v1\n"})
	mainX := strings.Replace(numbered(60), "line 1\nline 2\nline 3\n", "main 1\nmain 2\nmain 3\n", 1)
	f.write(f.repo, "x.go", mainX)
	f.write(f.repo, "y.txt", "schema v2\n")
	f.commit(f.repo, "main edits x.go 1-3 and y.txt")
	f.write(f.tree, "x.go", strings.Replace(numbered(60), "line 50\n", "block 50\n", 1))
	f.commit(f.tree, "A edits x.go 50")
	f.save()

	_, err := Run(f.home, "unit", "A")
	f.requireRebased(err, []string{"x.go", "y.txt"})
	if got, want := f.read(f.tree, "x.go"), strings.Replace(mainX, "line 50\n", "block 50\n", 1); got != want {
		t.Fatalf("x.go =\n%s\nwant both edits:\n%s", got, want)
	}
	if got := f.read(f.tree, "y.txt"); got != "schema v2\n" {
		t.Fatalf("y.txt = %q, want main's change", got)
	}
}

// Brief test 3: main and the block change the same line of x.go.
func TestOpenBlockSameLineRefusesRebaseConflict(t *testing.T) {
	f := newOpenFixture(t, []string{"x.go"}, map[string]string{"x.go": numbered(10)})
	f.write(f.repo, "x.go", strings.Replace(numbered(10), "line 5\n", "main 5\n", 1))
	f.commit(f.repo, "main edits line 5")
	f.write(f.tree, "x.go", strings.Replace(numbered(10), "line 5\n", "block 5\n", 1))
	head := f.commit(f.tree, "A edits line 5")
	f.save()

	_, err := Run(f.home, "unit", "A")
	f.requireConflict(err, head, "x.go")
	if got := f.queued(); len(got) != 0 {
		t.Fatalf("refusal queued %v", got)
	}
}

// Brief test 5 (accept-landed, rebase.Run): a closed block C of the run landed
// c.go on main, and a commit outside closed blocks changed z.go. Both are
// owned by A. Old code: owned-conflict naming z.go.
func TestAcceptLandedOutsideCommitReplays(t *testing.T) {
	setup := func(t *testing.T, blockZ string) (*openFixture, string) {
		f := newOpenFixture(t, []string{"a.go", "c.go", "z.go"}, map[string]string{"a.go": "a\n", "c.go": "c\n", "z.go": numbered(10)})
		f.write(f.repo, "c.go", "c landed\n")
		f.commit(f.repo, "C lands")
		f.git(f.repo, "branch", "forge/unit/C")
		f.closed = ledger.Object{{Key: "C", Value: ledger.Object{{Key: "status", Value: "closed"}, {Key: "base_sha", Value: f.base}}}}
		f.write(f.repo, "z.go", strings.Replace(numbered(10), "line 2\n", "outside 2\n", 1))
		f.commit(f.repo, "outside commit")
		f.write(f.tree, "a.go", "a block\n")
		if blockZ != "" {
			f.write(f.tree, "z.go", strings.Replace(numbered(10), "line 2\n", blockZ, 1))
		}
		head := f.commit(f.tree, "A")
		f.save()
		return f, head
	}
	t.Run("clean replay goes on and re-reviews", func(t *testing.T) {
		f, _ := setup(t, "")
		result, err := Run(f.home, "unit", "A")
		row := f.requireRebased(err, []string{"z.go"})
		want := []Accepted{{File: "c.go", Blocks: []string{"C"}}}
		if !reflect.DeepEqual(result.Accepted, want) {
			t.Fatalf("accepted_landed = %v, want %v (the outside commit is not credited)", result.Accepted, want)
		}
		if got := fmt.Sprint(row["accepted_landed"]); !strings.Contains(got, "c.go") || strings.Contains(got, "z.go") {
			t.Fatalf("row accepted_landed = %v", got)
		}
		if got := f.queued(); !reflect.DeepEqual(got, []string{"c.go", "z.go"}) {
			t.Fatalf("queued for re-review = %v, want [c.go z.go]", got)
		}
		if got := f.read(f.tree, "z.go"); !strings.Contains(got, "outside 2\n") {
			t.Fatalf("z.go = %q, main's change missing", got)
		}
	})
	t.Run("real conflict refuses naming the file", func(t *testing.T) {
		f, head := setup(t, "block 2\n")
		_, err := Run(f.home, "unit", "A")
		f.requireConflict(err, head, "z.go")
	})
}
