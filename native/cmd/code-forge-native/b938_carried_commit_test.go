package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B938: in a batch, a block that carries a commit main already has by
// patch (another sha) lands only its own commits; the carried one is not
// replayed onto main's later change of that file.
func TestB938BatchLandDropsCarriedCommit(t *testing.T) {
	f := newLandBatchFixture(t, 0)
	tree := filepath.Join(f.repo, ".code-forge/worktrees", "B1")
	f.write(tree, "notes.txt", "one\n")
	f.git(tree, "add", "notes.txt")
	f.git(tree, "-c", "commit.gpgsign=false", "commit", "-qm", "B0 notes")
	carried := f.git(tree, "rev-parse", "HEAD")
	f.write(f.repo, "other.txt", "main\n")
	f.git(f.repo, "add", "other.txt")
	f.git(f.repo, "-c", "commit.gpgsign=false", "commit", "-qm", "B8 other")
	f.git(f.repo, "-c", "commit.gpgsign=false", "cherry-pick", carried)
	f.write(f.repo, "notes.txt", "uno\n")
	f.git(f.repo, "-c", "commit.gpgsign=false", "commit", "-qam", "B9 notes")
	if err := state.UpdateRun(f.home, "r-batch-unit", func(record *ledger.Object) error {
		blocks := blockObject(record.Get("blocks"))
		entry := blockObject(blocks.Get("B1"))
		entry.Set("owned_files", []any{"src/B1.mjs", "notes.txt"})
		blocks.Set("B1", entry)
		record.Set("blocks", blocks)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := f.git(f.repo, "rev-parse", "main")
	code, output := f.land("B1", "B2")
	if code != 0 {
		t.Fatalf("batch exit=%d, want 0: %s", code, output)
	}
	if merges := strings.Fields(f.git(f.repo, "rev-list", "--first-parent", before+"..main")); len(merges) != 2 {
		t.Fatalf("main got %d commits, want the 2 block merges: %s", len(merges), output)
	}
	if got := f.git(f.repo, "log", "--format=%s", "--no-merges", before+"..main"); got != "B2\nB1" {
		t.Fatalf("landed commits = %q, want only B1 and B2 (the carried one dropped)", got)
	}
	if got := f.git(f.repo, "show", "main:notes.txt"); got != "uno" {
		t.Fatalf("main notes.txt = %q, want main's", got)
	}
	if n := strings.Count(f.git(f.repo, "log", "--format=%s", "main"), "B0 notes"); n != 1 {
		t.Fatalf("B0 notes on main %d times, want 1", n)
	}
}

// B938: a generated doc both blocks changed is regenerated on the batch
// rebase (land.auto_resolve.generated, B815), not merged as rows or text.
func TestB938BatchLandRegeneratesGeneratedDoc(t *testing.T) {
	f := newLandBatchFixture(t, 0)
	var cfg map[string]any
	if err := json.Unmarshal([]byte(f.git(f.repo, "show", "main:.code-forge.yml")), &cfg); err != nil {
		t.Fatal(err)
	}
	cfg["land"] = map[string]any{"auto_resolve": map[string]any{"generated": []any{map[string]any{"path": "docs/gen.md", "argv": []any{"sh", "-c", "ls src > docs/gen.md"}}}}}
	hash := config.Hash(cfg)
	for _, id := range []string{"B1", "B2"} {
		tree := filepath.Join(f.repo, ".code-forge/worktrees", id)
		f.write(tree, "docs/gen.md", "hand edit "+id+"\n")
		f.git(tree, "add", "docs/gen.md")
		f.git(tree, "-c", "commit.gpgsign=false", "commit", "-qm", id+" gen")
	}
	if err := state.UpdateRun(f.home, "r-batch-unit", func(record *ledger.Object) error {
		blocks := blockObject(record.Get("blocks"))
		for _, id := range []string{"B1", "B2"} {
			entry := blockObject(blocks.Get(id))
			entry.Set("owned_files", []any{"src/" + id + ".mjs", "docs/gen.md"})
			blocks.Set(id, entry)
		}
		record.Set("blocks", blocks)
		record.Set("config", map[string]any{"hash": hash, "snapshots": map[string]any{hash: cfg}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	code, output := f.land("B1", "B2")
	if code != 0 {
		t.Fatalf("batch exit=%d, want 0: %s", code, output)
	}
	if got := f.git(f.repo, "show", "main:docs/gen.md"); got != "B1.mjs\nB2.mjs" {
		t.Fatalf("main docs/gen.md = %q, want the generator's output: %s", got, output)
	}
}
