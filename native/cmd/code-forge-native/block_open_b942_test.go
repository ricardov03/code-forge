package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/state"
)

// B942 tests: a new coder worktree starts from the newest base branch (the
// newer of local main and origin's main), and block ready lists stale_base
// with the rebase command once main moved past the block's base.

// b942Fixture is the batch fixture with proof.isolation: worktree, a green
// fake gh (B920) and, when origin is true, a bare origin holding main.
func b942Fixture(t *testing.T, origin bool) (*landBatchFixture, string) {
	t.Helper()
	f := newLandBatchFixture(t, 0)
	data, err := os.ReadFile(filepath.Join(f.repo, ".code-forge.yml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg["proof"] = map[string]any{"isolation": "worktree"}
	landCISnapshot(t, f.home, "r-batch-unit", cfg, landCIOn)
	landCIFakeGh(t, landCIGreen, nil)
	bare := ""
	if origin {
		bare = filepath.Join(t.TempDir(), "origin.git")
		f.git(f.repo, "init", "-q", "--bare", bare)
		f.git(f.repo, "remote", "add", "origin", bare)
		f.git(f.repo, "push", "-q", "origin", "main")
	}
	return f, bare
}

// b942Commit makes a commit on top of parent without moving any branch.
func b942Commit(f *landBatchFixture, parent, name string) string {
	f.t.Helper()
	tree := f.git(f.repo, "rev-parse", parent+"^{tree}")
	return f.git(f.repo, "commit-tree", tree, "-p", parent, "-m", name)
}

func b942Base(t *testing.T, f *landBatchFixture) string {
	t.Helper()
	record, err := state.ReadRun(f.home, "r-batch-unit")
	if err != nil {
		t.Fatal(err)
	}
	return blockString(blockObject(blockObject(blockObject(record.Get("blocks")).Get("B4")).Get("worktree")).Get("base_sha"))
}

func TestB942OpenStartsFromNewerOrigin(t *testing.T) {
	f, _ := b942Fixture(t, true)
	ahead := b942Commit(f, f.base, "landed elsewhere")
	f.git(f.repo, "push", "-q", "origin", ahead+":refs/heads/main")
	code, output := b920Open(t, f)
	if code != 0 {
		t.Fatalf("exit=%d: %s", code, output)
	}
	if got := b942Base(t, f); got != ahead {
		t.Errorf("worktree base=%s, want origin's newer main %s (local main %s)", got, ahead, f.base)
	}
	if got := f.git(f.repo, "rev-parse", "refs/heads/main"); got != f.base {
		t.Errorf("local main moved to %s, want it left at %s", got, f.base)
	}
}

func TestB942OpenKeepsNewerLocalMain(t *testing.T) {
	f, _ := b942Fixture(t, true)
	ahead := b942Commit(f, f.base, "local only")
	f.git(f.repo, "update-ref", "refs/heads/main", ahead)
	code, output := b920Open(t, f)
	if code != 0 {
		t.Fatalf("exit=%d: %s", code, output)
	}
	if got := b942Base(t, f); got != ahead {
		t.Errorf("worktree base=%s, want the newer local main %s", got, ahead)
	}
}

func TestB942OpenRefusesDivergedMain(t *testing.T) {
	f, _ := b942Fixture(t, true)
	remote := b942Commit(f, f.base, "origin side")
	f.git(f.repo, "push", "-q", "origin", remote+":refs/heads/main")
	local := b942Commit(f, f.base, "local side")
	f.git(f.repo, "update-ref", "refs/heads/main", local)
	code, output := b920Open(t, f)
	if code != 1 || !strings.Contains(output, "local main ("+local[:12]+") and origin/main ("+remote[:12]+") diverged") || !strings.Contains(output, "next: git -C ") {
		t.Fatalf("exit=%d output=%q, want the diverged refusal with its next step", code, output)
	}
	if got := b920Status(t, f); got != nil {
		t.Errorf("B4 status=%v, want nothing recorded", got)
	}
}

// b942Ready runs block ready --json for B4 and returns its stale_base row.
func b942Ready(t *testing.T) (map[string]any, string) {
	t.Helper()
	code, out, errText := runCaptured(t, "block", "ready", "B4", "--run", "r-batch-unit", "--json")
	var result struct {
		Blockers []map[string]any `json:"blockers"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("ready exit=%d out=%q err=%q: %v", code, out, errText, err)
	}
	for _, b := range result.Blockers {
		if b["code"] == "stale_base" {
			return b, out
		}
	}
	return nil, out
}

func TestB942ReadyListsStaleBaseUntilRebase(t *testing.T) {
	f, _ := b942Fixture(t, false)
	if code, output := b920Open(t, f); code != 0 {
		t.Fatalf("open exit=%d: %s", code, output)
	}
	if b, out := b942Ready(t); b != nil {
		t.Fatalf("fresh block has stale_base: %s", out)
	}
	moved := b942Commit(f, f.base, "main moved")
	f.git(f.repo, "update-ref", "refs/heads/main", moved)
	b, out := b942Ready(t)
	if b == nil {
		t.Fatalf("ready after main moved lacks stale_base: %s", out)
	}
	if fix := b["fix"]; fix != "code-forge block rebase B4 --run r-batch-unit --accept-landed" {
		t.Errorf("fix=%v, want the rebase command", fix)
	}
	if detail, _ := b["detail"].(string); !strings.Contains(detail, "rebase on the newest main before the CI push (B942)") {
		t.Errorf("detail=%q", detail)
	}
	if code, out, errText := runCaptured(t, "block", "rebase", "B4", "--run", "r-batch-unit", "--accept-landed"); code != 0 {
		t.Fatalf("rebase exit=%d out=%q err=%q", code, out, errText)
	}
	if b, out := b942Ready(t); b != nil {
		t.Fatalf("stale_base after rebase: %s", out)
	}
}

func TestB942PackAndPromptSayRebaseBeforeCI(t *testing.T) {
	config := "version: 1\ncontext:\n  commands:\n    test: [go, test]\n"
	newContextFixture(t, map[string]any{"B1": map[string]any{"status": "open", "level": "L1", "base_branch": "main", "worktree": map[string]any{"branch": "forge/r-ctx/B1"}}}, config)
	code, out, errText := runCaptured(t, "context", "pack", "--run", "r-ctx", "--block", "B1")
	if code != 0 || errText != "" {
		t.Fatalf("code=%d stderr=%q", code, errText)
	}
	if want := "- rebase on the newest main just before the CI push: code-forge block rebase B1 --run r-ctx (block ready lists stale_base while main moved past your base)\n"; !strings.Contains(out, want) {
		t.Fatalf("pack lacks %q:\n%s", want, out)
	}
	w := b549Fixture(t, "ok", "")
	if code, out, errText := b460Call(t, "spawn", "--level", "L1", "--role", "coder", "--brief", "brief.md", "--block", "B1"); code != 0 {
		t.Fatalf("spawn code=%d out=%q err=%q", code, out, errText)
	}
	prompt, err := os.ReadFile(filepath.Join(w, "log", "prompt.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "\n\nRebase on the newest base branch just before the CI push (code-forge block ready lists stale_base with the rebase command while it moved past your base).\n\n"; !strings.Contains(string(prompt), want) {
		t.Fatalf("prompt lacks %q:\n%s", want, prompt)
	}
}
