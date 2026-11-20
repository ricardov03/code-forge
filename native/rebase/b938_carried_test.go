package rebase

import (
	"strings"
	"testing"
)

// B938: a block branch that carries a commit main already has under another
// sha (same patch, as when an earlier block was rebased before it landed)
// replays only its own commits. Replaying the carried one onto main, which
// changed that file since, was a conflict (B882 carrying B795's commit).
func TestB938RebaseDropsCarriedCommit(t *testing.T) {
	f := newOpenFixture(t, []string{"a.txt", "notes.txt"}, map[string]string{"a.txt": "a\n"})
	f.write(f.tree, "notes.txt", "one\n")
	carried := f.commit(f.tree, "B0 notes")
	f.write(f.repo, "other.txt", "main\n")
	f.commit(f.repo, "B8 other")
	f.git(f.repo, "cherry-pick", carried)
	f.write(f.repo, "notes.txt", "uno\n")
	f.commit(f.repo, "B9 notes")
	f.write(f.tree, "a.txt", "a block\n")
	f.commit(f.tree, "A")
	f.save()
	if _, err := RunPlain(f.home, "unit", "A"); err != nil {
		t.Fatalf("rebase refused: %v", err)
	}
	if got := f.git(f.tree, "log", "--format=%s", "main..HEAD"); got != "A" {
		t.Fatalf("replayed commits = %q, want only the block's own A", got)
	}
	if got := f.read(f.tree, "notes.txt"); got != "uno\n" {
		t.Fatalf("notes.txt = %q, want main's", got)
	}
	if got := f.read(f.tree, "a.txt"); got != "a block\n" {
		t.Fatalf("a.txt = %q", got)
	}
	if status := f.git(f.tree, "status", "--porcelain"); status != "" {
		t.Fatalf("worktree not clean: %q", status)
	}
	if n := strings.Count(f.git(f.tree, "log", "--format=%s"), "B0 notes"); n != 1 {
		t.Fatalf("B0 notes in history %d times, want 1", n)
	}
}
