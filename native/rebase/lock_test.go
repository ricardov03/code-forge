package rebase

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B426: a block rebase holds no run lock while it works in the worktree. A
// slow fake build (a post-commit hook that sleeps 3 s on every replayed
// commit) keeps the rebase busy; a block write on another block of the same
// run succeeds within 1 s meanwhile, and the rebase's commit keeps it.
func TestRunLeavesRunUnlockedDuringSlowRebase(t *testing.T) {
	root := t.TempDir()
	repo, tree, home := filepath.Join(root, "repo"), filepath.Join(root, "wt"), filepath.Join(root, "home")
	g := func(dir string, args ...string) string {
		t.Helper()
		out, err := git(dir, append([]string{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(out)
	}
	write := func(file, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatal(err)
	}
	g(repo, "init", "-qb", "main")
	write(filepath.Join(repo, ".gitignore"), ".code-forge/\n")
	write(filepath.Join(repo, "a.go"), "base\n")
	g(repo, "add", ".")
	g(repo, "commit", "-qm", "base")
	base := g(repo, "rev-parse", "HEAD")
	g(repo, "worktree", "add", "-qb", "forge/unit/A", tree, base)
	write(filepath.Join(tree, "a.go"), "block A\n")
	g(tree, "commit", "-qam", "A")
	write(filepath.Join(repo, "m.go"), "main moved\n")
	g(repo, "add", "m.go")
	g(repo, "commit", "-qm", "main moved")
	// The fake build: logs its start, then sleeps 3 s.
	started := filepath.Join(root, "build-started")
	hook := filepath.Join(root, "hooks", "post-commit")
	write(hook, "#!/bin/sh\ntouch '"+started+"'\nsleep 3\n")
	if err := os.Chmod(hook, 0755); err != nil {
		t.Fatal(err)
	}
	g(repo, "config", "core.hooksPath", filepath.Dir(hook))

	blocks := ledger.Object{
		{Key: "A", Value: ledger.Object{{Key: "status", Value: "open"}, {Key: "owned_files", Value: []any{"a.go"}}, {Key: "base_sha", Value: base}, {Key: "base_branch", Value: "main"},
			{Key: "worktree", Value: ledger.Object{{Key: "path", Value: tree}, {Key: "branch", Value: "forge/unit/A"}, {Key: "base_sha", Value: base}}}}},
		{Key: "B", Value: ledger.Object{{Key: "status", Value: "open"}, {Key: "owned_files", Value: []any{"b.go"}}, {Key: "base_sha", Value: base}}},
	}
	record := ledger.Object{{Key: "run_id", Value: "unit"}, {Key: "workspace", Value: repo}, {Key: "project", Value: "unit"}, {Key: "status", Value: "active"}, {Key: "blocks", Value: blocks}}
	if err := state.SaveRun(home, record); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".code-forge", "runs", "unit.key"), bytes.Repeat([]byte{7}, 32), 0600); err != nil {
		t.Fatal(err)
	}

	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := Run(home, "unit", "A")
		done <- outcome{result, err}
	}()
	for deadline := time.Now().Add(30 * time.Second); ; {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the slow build never started")
		}
		select {
		case o := <-done:
			t.Fatalf("rebase ended before its slow build: %v", o.err)
		case <-time.After(20 * time.Millisecond):
		}
	}
	begin := time.Now()
	err := state.UpdateBlock(home, "unit", "B", func(_, block *ledger.Object) ([]ledger.Object, error) {
		block.Set("note", "written during the rebase")
		return nil, nil
	})
	elapsed := time.Since(begin)
	if err != nil || elapsed > time.Second {
		t.Fatalf("write on block B during the rebase: err=%v after %v, want success within 1s", err, elapsed)
	}
	select {
	case o := <-done:
		t.Fatalf("rebase ended before the other write finished (%v), the test proves nothing", o.err)
	default:
	}
	o := <-done
	if o.err != nil {
		t.Fatalf("rebase: %v", o.err)
	}
	newBase := g(repo, "rev-parse", "main")
	after, err := state.ReadRun(home, "unit")
	if err != nil {
		t.Fatal(err)
	}
	final := after.Get("blocks").(ledger.Object)
	if got := final.Get("A").(ledger.Object).Get("base_sha"); got != newBase || o.result.NewBase != newBase {
		t.Fatalf("A base_sha = %v (result %s), want the new main %s", got, o.result.NewBase, newBase)
	}
	if note := final.Get("B").(ledger.Object).Get("note"); note != "written during the rebase" {
		t.Fatalf("B note = %v after the rebase; its commit lost the other write", note)
	}
}
