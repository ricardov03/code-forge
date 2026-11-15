package main

import (
	"os"
	"path/filepath"
	"testing"
)

// B735: a landed rebase whose review queue cannot be listed after replay
// fails with queue-unreadable (Node's WorkerError in blockTickets and
// reviewIdentity, src/worker/queue.mjs), restores HEAD and keeps the base.
// Only block close prints the raw scandir error (B718).
func TestB735RebaseLandedQueueFileIsQueueUnreadable(t *testing.T) {
	f := newRebaseCmdFixture(t, map[string]string{"a.go": "package a\n", "b.go": "package b\n"})
	f.write(f.repo, "b.go", "package b\n\nfunc B() {}\n")
	f.git(f.repo, "commit", "-qam", "B lands")
	f.write(f.tree, "a.go", "package a\n\nfunc A() {}\n")
	f.git(f.tree, "commit", "-qam", "A")
	treeHead := f.git(f.tree, "rev-parse", "HEAD")
	f.save("a.go")
	if err := os.MkdirAll(filepath.Join(f.repo, ".code-forge"), 0700); err != nil {
		t.Fatal(err)
	}
	queue := filepath.Join(f.repo, ".code-forge", "queue")
	f.write(filepath.Join(f.repo, ".code-forge"), "queue", "queue cannot be read as a directory\n")
	rowsBefore := len(f.rows())

	code, stdout, stderr := f.call("A", "--run", "r-rebase-unit", "--accept-landed", "--json")
	if code != 1 || stdout != "" || stderr != "code-forge-native: queue-unreadable\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if got := f.git(f.tree, "rev-parse", "HEAD"); got != treeHead {
		t.Fatalf("HEAD not restored: %s want %s", got, treeHead)
	}
	if got := f.git(f.tree, "status", "--porcelain"); got != "" {
		t.Fatalf("tree not clean: %q", got)
	}
	if got := f.block().Get("base_sha"); got != f.base {
		t.Fatalf("base_sha moved: %v", got)
	}
	if got := len(f.rows()); got != rowsBefore {
		t.Fatalf("rows %d, want %d", got, rowsBefore)
	}

	// The queue is a folder again: the retry rebases.
	if err := os.Remove(queue); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = f.call("A", "--run", "r-rebase-unit", "--accept-landed", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("retry: code=%d stderr=%q", code, stderr)
	}
}
