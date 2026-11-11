package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B449: every form of `block rebase` runs in Go. Node is unreachable in these
// tests (CODE_FORGE_NODE=/nonexistent), so a hand-over cannot pass them.

func (f *rebaseCmdFixture) call(args ...string) (int, string, string) {
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
	code := runBlockRebase(args)
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

// setBlock changes block A of the saved record.
func (f *rebaseCmdFixture) setBlock(change func(block *ledger.Object)) {
	f.t.Helper()
	err := state.UpdateRun(f.home, "r-rebase-unit", func(record *ledger.Object) error {
		blocks := blockObject(record.Get("blocks"))
		block := blockObject(blocks.Get("A"))
		change(&block)
		blocks.Set("A", block)
		record.Set("blocks", blocks)
		return nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *rebaseCmdFixture) rows() []map[string]any {
	f.t.Helper()
	all, err := ledger.ReadAllRows(filepath.Join(f.home, ".code-forge", "ledger"), "fixture")
	if err != nil {
		f.t.Fatal(err)
	}
	out := []map[string]any{}
	for _, row := range all {
		if m, ok := row.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func TestBlockRebaseHelpRunsInGo(t *testing.T) {
	f := newRebaseCmdFixture(t, map[string]string{"a.go": "package a\n"})
	want := "usage: code-forge block rebase <id> --run <r> [--head <ref>] [--accept-changed <file>… --reason <text>] [--accept-landed] [--json] [--help|-h]\n" +
		"  --help, -h  Print this usage and exit without changing state.\n" +
		"  --json      Print one JSON line on success instead of text.\n"
	for _, args := range [][]string{{"--help"}, {"A", "-h"}, {"A", "--bogus", "--help"}} {
		code, stdout, stderr := f.call(args...)
		if code != 0 || stdout != want || stderr != "" {
			t.Fatalf("%v: code=%d stdout=%q stderr=%q", args, code, stdout, stderr)
		}
	}
}

func TestBlockRebaseUsageErrorsRunInGo(t *testing.T) {
	f := newRebaseCmdFixture(t, map[string]string{"a.go": "package a\n"})
	f.save("a.go")
	cases := []struct {
		args []string
		text string
	}{
		{[]string{}, "block rebase: a block id and --run are required\n"},
		{[]string{"A"}, "block rebase: a block id and --run are required\n"},
		{[]string{"A", "--run", "r-rebase-unit", "--bogus"}, "block rebase: unknown flag \"--bogus\"\n"},
		{[]string{"A", "--run", "r-rebase-unit", "--json=1"}, "block rebase: --json takes no value\n"},
		{[]string{"A", "--run", "r-rebase-unit", "--accept-landed", "--head", "main"}, "block rebase: --accept-landed takes one block id, --run and optional --json\n"},
		{[]string{"A", "--run", "r-rebase-unit", "--remove-worktree"}, "block rebase: --remove-worktree is a block close flag\n"},
		{[]string{"A", "--run", "r-rebase-unit", "--accept-changed", "a.go"}, "block rebase: --accept-changed needs one block id and a nonblank --reason without control characters\n"},
		{[]string{"A", "--run", "r-rebase-unit", "--accept-changed", "a.go", "--reason", "  "}, "block rebase: --accept-changed needs one block id and a nonblank --reason without control characters\n"},
		{[]string{"A", "--run", "r-rebase-unit", "--accept-changed", "../a.go", "--reason", "ok"}, "block rebase: --file must be repo-root-relative\n"},
	}
	for _, c := range cases {
		code, stdout, stderr := f.call(c.args...)
		if code != 2 || stdout != "" || stderr != c.text {
			t.Fatalf("%v: code=%d stdout=%q stderr=%q", c.args, code, stdout, stderr)
		}
	}
	code, _, stderr := f.call("A", "--run", "r-missing")
	if code != 1 || !strings.HasPrefix(stderr, "block rebase: ") || strings.Contains(stderr, "code-forge-native") {
		t.Fatalf("missing run: code=%d stderr=%q", code, stderr)
	}
}

func TestBlockRebaseAcceptLandedRunsInGo(t *testing.T) {
	f := newRebaseCmdFixture(t, map[string]string{"a.go": "package a\n", "b.go": "package b\n"})
	f.write(f.repo, "b.go", "package b\n\nfunc B() {}\n")
	f.git(f.repo, "commit", "-qam", "B lands")
	head := f.git(f.repo, "rev-parse", "HEAD")
	f.write(f.tree, "a.go", "package a\n\nfunc A() {}\n")
	f.git(f.tree, "commit", "-qam", "A")
	f.save("a.go")

	code, stdout, stderr := f.call("A", "--run", "R_bad", "--accept-landed")
	if code != 2 || stdout != "" || stderr != "code-forge-native: block-rebase-landed requires --home <dir> and a valid --run <id>\n" {
		t.Fatalf("bad run: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, stderr = f.call("A", "--run", "r-rebase-unit", "--accept-landed")
	if code != 0 || stderr != "" || stdout != "block A rebased "+f.base[:12]+" → "+head[:12]+"\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if got := f.block().Get("base_sha"); got != head {
		t.Fatalf("base_sha=%v want %s", got, head)
	}
	if got := f.git(f.tree, "show", "HEAD:b.go"); !strings.Contains(got, "func B()") {
		t.Fatalf("worktree not replayed onto main: %q", got)
	}
}

func TestBlockRebaseAcceptLandedJSONRunsInGo(t *testing.T) {
	f := newRebaseCmdFixture(t, map[string]string{"a.go": "package a\n", "b.go": "package b\n"})
	f.write(f.repo, "b.go", "package b\n\nfunc B() {}\n")
	f.git(f.repo, "commit", "-qam", "B lands")
	head := f.git(f.repo, "rev-parse", "HEAD")
	f.save("a.go")
	code, stdout, stderr := f.call("A", "--run", "r-rebase-unit", "--accept-landed", "--json")
	var got map[string]any
	if code != 0 || stderr != "" || strings.Count(stdout, "\n") != 1 || json.Unmarshal([]byte(stdout), &got) != nil {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if got["block"] != "A" || got["event"] != "rebased" || got["old_base"] != f.base || got["new_base"] != head {
		t.Fatalf("json=%v", got)
	}
}

// A block without a worktree moves its recorded base to --head.
func TestBlockRebaseNoWorktreeBlockRunsInGo(t *testing.T) {
	f := newRebaseCmdFixture(t, map[string]string{"a.go": "package a\n", "b.go": "package b\n"})
	f.save("a.go")
	f.setBlock(func(block *ledger.Object) { block.Delete("worktree") })
	f.write(f.repo, "b.go", "package b\n\nfunc B() {}\n")
	f.git(f.repo, "commit", "-qam", "B lands")
	head := f.git(f.repo, "rev-parse", "HEAD")

	code, stdout, stderr := f.call("A", "--run", "r-rebase-unit", "--head", "-x")
	if code != 1 || stdout != "" || stderr != "block rebase: a git ref must be non-empty and not start with \"-\" (got \"-x\")\n" {
		t.Fatalf("bad ref: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, stderr = f.call("A", "--run", "r-rebase-unit", "--head", "nope")
	if code != 1 || stderr != "block rebase: git rev-parse failed (exit 128)\n" {
		t.Fatalf("unknown ref: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, stderr = f.call("A", "--run", "r-rebase-unit", "--json")
	if code != 0 || stderr != "" || stdout != `{"block":"A","event":"rebased","old_base":"`+f.base+`","new_base":"`+head+`"}`+"\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if got := f.block().Get("base_sha"); got != head {
		t.Fatalf("base_sha=%v", got)
	}
	rows := f.rows()
	last := rows[len(rows)-1]
	if last["event"] != "block.rebase" || last["block"] != "A" || last["old_base"] != f.base || last["new_base"] != head || last["mac"] == nil {
		t.Fatalf("row=%v", last)
	}
	// Main now changes the owned file: a refusal, nothing moves.
	f.write(f.repo, "a.go", "package a\n\nfunc Main() {}\n")
	f.git(f.repo, "commit", "-qam", "main touches a")
	code, stdout, stderr = f.call("A", "--run", "r-rebase-unit", "--head", "main")
	if code != 1 || stdout != "" || stderr != "block rebase: block A: commits since its base changed owned files: a.go\n" {
		t.Fatalf("owned conflict: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if got := f.block().Get("base_sha"); got != head {
		t.Fatalf("base moved on refusal: %v", got)
	}
}

// closedFixture: block A closed, its tree changed the last line of a.go, main
// changed the first line of a.go (a clean replay that changes the owned blob).
func closedFixture(t *testing.T, status string) (*rebaseCmdFixture, string) {
	f := newRebaseCmdFixture(t, map[string]string{"a.go": "package a\n\n// one\n// two\n// three\n", "b.go": "package b\n"})
	f.write(f.tree, "a.go", "package a\n\n// one\n// two\n// three A\n")
	f.git(f.tree, "commit", "-qam", "A")
	f.write(f.repo, "a.go", "package a // main\n\n// one\n// two\n// three\n")
	f.git(f.repo, "commit", "-qam", "main touches a")
	head := f.git(f.repo, "rev-parse", "HEAD")
	f.save("a.go")
	f.setBlock(func(block *ledger.Object) { block.Set("status", status) })
	return f, head
}

func TestBlockRebaseClosedBlockRunsInGo(t *testing.T) {
	for _, status := range []string{"closed", "awaiting_human_check"} {
		f := newRebaseCmdFixture(t, map[string]string{"a.go": "package a\n", "b.go": "package b\n"})
		f.write(f.tree, "a.go", "package a\n\nfunc A() {}\n")
		f.git(f.tree, "commit", "-qam", "A")
		f.write(f.repo, "b.go", "package b\n\nfunc B() {}\n")
		f.git(f.repo, "commit", "-qam", "B lands")
		head := f.git(f.repo, "rev-parse", "HEAD")
		f.save("a.go")
		f.setBlock(func(block *ledger.Object) { block.Set("status", status) })

		code, stdout, stderr := f.call("A", "--run", "r-rebase-unit")
		if code != 0 || stderr != "" || stdout != "block A rebased "+f.base[:12]+" → "+head[:12]+"\n" {
			t.Fatalf("%s: code=%d stdout=%q stderr=%q", status, code, stdout, stderr)
		}
		block := f.block()
		if block.Get("status") != status || block.Get("base_sha") != head || blockObject(block.Get("worktree")).Get("base_sha") != head {
			t.Fatalf("%s: block=%v", status, block)
		}
		rows := f.rows()
		last := rows[len(rows)-1]
		if last["event"] != "block.rebase" || last["closed"] != true || last["new_base"] != head || len(last["changed_reviewed"].([]any)) != 0 {
			t.Fatalf("%s: row=%v", status, last)
		}
		if got := f.git(f.tree, "show", "HEAD:b.go"); !strings.Contains(got, "func B()") {
			t.Fatalf("%s: tree not on main: %q", status, got)
		}
	}
}

func TestBlockRebaseClosedChangedFileNeedsReview(t *testing.T) {
	f, _ := closedFixture(t, "closed")
	oldHead := f.git(f.tree, "rev-parse", "HEAD")
	code, stdout, stderr := f.call("A", "--run", "r-rebase-unit")
	want := "block rebase: rebase-changed-reviewed a.go\nRe-review: code-forge review-file --rebase-check --block A --run r-rebase-unit\nHuman accept (repeat --accept-changed for each file): code-forge block rebase A --run r-rebase-unit --accept-changed <file> --reason <text>\n"
	if code != 1 || stdout != "" || stderr != want {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if got := f.git(f.tree, "rev-parse", "HEAD"); got != oldHead {
		t.Fatalf("head not restored: %s want %s", got, oldHead)
	}
	if got := f.block().Get("base_sha"); got != f.base {
		t.Fatalf("base moved on refusal: %v", got)
	}
}
