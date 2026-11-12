package worker

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ricardov03/code-forge/native/contextpack"
	"github.com/ricardov03/code-forge/native/gates"
)

type factsGolden struct {
	Acceptance []struct {
		Name       string         `json:"name"`
		Acceptance any            `json:"acceptance"`
		Cfg        map[string]any `json:"cfg"`
		Secrets    []string       `json:"secrets"`
		Want       string         `json:"want"`
	} `json:"acceptance"`
	Digest []struct {
		Name    string         `json:"name"`
		Cfg     map[string]any `json:"cfg"`
		Secrets []string       `json:"secrets"`
		Want    *string        `json:"want"`
		Error   *string        `json:"error"`
	} `json:"digest"`
	RootFiles map[string]string `json:"root_files"`
}

func readFactsGolden(t *testing.T) factsGolden {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "facts_golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var g factsGolden
	if err := json.Unmarshal(data, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

// TestAcceptanceExcerptMatchesNode: engine.mjs acceptanceExcerpt's text on
// Node's own outputs (fold, redact, the byte cut and the truncation marker).
func TestAcceptanceExcerptMatchesNode(t *testing.T) {
	for _, c := range readFactsGolden(t).Acceptance {
		t.Run(c.Name, func(t *testing.T) {
			if got := AcceptanceExcerpt(c.Acceptance, c.Cfg, c.Secrets); got != c.Want {
				t.Errorf("got  %q\nNode %q", got, c.Want)
			}
		})
	}
}

// TestFactsForWorktreeLine: a block with a worktree names it (redacted).
func TestFactsForWorktreeLine(t *testing.T) {
	got := FactsFor(nil, &WorktreeEntry{Path: "/w/tree-SECRET", Branch: "b1"}, nil, []string{"SECRET"})
	want := "Acceptance clauses (what the change must do):\n(acceptance unavailable)\nWorktree: the file is reviewed as it is in the block's worktree /w/tree-[REDACTED] (branch b1), against the block base."
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// TestReviewDigestMatchesNode: pack.mjs reviewDigest's text, and a
// ContextError where Node throws one (the ticket's `context-unreadable`).
func TestReviewDigestMatchesNode(t *testing.T) {
	g := readFactsGolden(t)
	root, err := filepath.EvalSymlinks(t.TempDir()) // the worker's RepoRoot is realpath'd
	if err != nil {
		t.Fatal(err)
	}
	for rel, text := range g.RootFiles {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, rel), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range g.Digest {
		t.Run(c.Name, func(t *testing.T) {
			got, err := contextpack.ReviewDigest(root, c.Cfg, func(s string) string { return gates.Mask(s, c.Secrets) })
			if c.Error != nil {
				var ce *contextpack.Error
				if !errors.As(err, &ce) {
					t.Fatalf("got %q, %v; Node throws ContextError %s", got, err, *c.Error)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != *c.Want {
				t.Errorf("got  %q\nNode %q", got, *c.Want)
			}
		})
	}
}
