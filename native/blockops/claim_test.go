package blockops

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

func TestClaimAutoSymlinkedWorkspaceOverlap(t *testing.T) {
	home, repo := t.TempDir(), t.TempDir()
	cmd := exec.Command("git", "init", "-q", repo)
	cmd.Env = gitx.ChildEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	workspace := filepath.Join(repo, "project")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "shared.mjs"), []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "workspace")
	if err := os.Symlink(workspace, alias); err != nil {
		t.Fatal(err)
	}
	const run = "r-symlink-overlap"
	for _, ws := range []string{workspace, alias} {
		t.Run(ws, func(t *testing.T) {
			record := ledger.Object{
				{Key: "run_id", Value: run}, {Key: "project", Value: "fixture"},
				{Key: "workspace", Value: ws}, {Key: "status", Value: "active"}, {Key: "orphans", Value: []any{}},
				{Key: "blocks", Value: ledger.Object{
					{Key: "B1", Value: ledger.Object{{Key: "status", Value: "open"}, {Key: "owned_files", Value: []any{}}}},
					{Key: "B2", Value: ledger.Object{{Key: "status", Value: "open"}, {Key: "owned_files", Value: []any{"shared.mjs"}}}},
				}},
			}
			if err := state.SaveRun(home, record); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(home, ".code-forge/runs", run+".json")
			before, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			err = ClaimAuto(home, run, "B1", "project/shared.mjs", nil, "", func(ledger.Object) bool { return true }, true)
			var refusal *state.Error
			if !errors.As(err, &refusal) || refusal.Code != "unowned_change" || !strings.Contains(refusal.Message, "owned by open block B2") {
				t.Fatalf("want sibling overlap refusal, got %v", err)
			}
			after, err := os.ReadFile(file)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("refused claim changed run bytes: %v", err)
			}
			if _, err := os.Stat(filepath.Join(home, ".code-forge/ledger")); !os.IsNotExist(err) {
				t.Fatalf("refused claim created ledger: %v", err)
			}
		})
	}
}
