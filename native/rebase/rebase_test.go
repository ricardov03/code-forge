package rebase

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

func TestOrigins(t *testing.T) {
	for _, kind := range []string{"closed", "custom", "open", "other-run", "missing", "reverted", "rename", "unicode-boundary", "block-base", "worktree-base", "missing-base", "invalid-base", "invalid-worktree-base"} {
		t.Run(kind, func(t *testing.T) {
			tree := t.TempDir()
			g := func(args ...string) string {
				t.Helper()
				out, err := git(tree, args...)
				if err != nil {
					t.Fatal(err)
				}
				return strings.TrimSpace(out)
			}
			commit := func(name string) string {
				g("add", "-A")
				g("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", name)
				return g("rev-parse", "HEAD")
			}
			write := func(file, content string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(tree, file), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			g("init", "-qb", "main")
			file := "owned.go"
			if kind == "unicode-boundary" {
				file = "😀.go"
			}
			write(file, "base\n")
			base := commit("base")
			write(file, "landed\n")
			if kind == "rename" {
				g("mv", file, "elsewhere.go")
			}
			landed := commit("landed")
			branch := "forge/r-test/A"
			if kind == "custom" {
				branch = "custom-sibling"
			}
			if kind == "other-run" {
				branch = "forge/r-other/A"
			}
			if kind != "missing" {
				g("branch", branch)
			}
			status := "closed"
			if kind == "open" {
				status = "open"
			}
			sibling := ledger.Object{{Key: "status", Value: status}, {Key: "base_sha", Value: base}}
			if kind == "custom" {
				sibling.Set("worktree", ledger.Object{{Key: "branch", Value: branch}})
			}
			switch kind {
			case "block-base":
				sibling.Set("base_sha", landed)
			case "worktree-base":
				sibling.Set("worktree", ledger.Object{{Key: "base_sha", Value: landed}})
			case "missing-base":
				sibling.Delete("base_sha")
			case "invalid-base":
				sibling.Set("base_sha", "missing-base")
			case "invalid-worktree-base":
				sibling.Set("worktree", ledger.Object{{Key: "base_sha", Value: "missing-base"}})
			}
			blocks := ledger.Object{{Key: "A", Value: sibling}}
			foreign := ""
			if kind == "reverted" {
				write(file, "foreign\n")
				foreign = commit("foreign")
				write(file, "landed\n")
				commit("reverted")
			}
			owned := []any{file}
			if kind == "unicode-boundary" {
				owned = []any{"?.go"}
			}
			owns := func(file string) bool { return ownsFile(owned, nil, file) }
			accepted, upstream, err := origins(tree, "r-test", base, g("rev-parse", "HEAD"), blocks, owns)
			if kind == "unicode-boundary" {
				if err != nil || len(accepted) != 0 || len(upstream) != 0 {
					t.Fatalf("Node ? matches one UTF-16 unit; astral filename is unowned: got %v, %v, %v", accepted, upstream, err)
				}
			} else if kind == "closed" || kind == "custom" || kind == "rename" {
				want := []Accepted{{File: file, Blocks: []string{"A"}}}
				if err != nil || !reflect.DeepEqual(accepted, want) || len(upstream) != 0 {
					t.Fatalf("accepted = %v, upstream = %v, %v; want exactly %v", accepted, upstream, err, want)
				}
			} else {
				// B431: an uncredited change is no owned-conflict up front; the
				// file is reported as upstream for the replay to settle.
				// A reverted foreign change still counts as upstream even though
				// the closed sibling's landing is credited for the same file.
				wantAccepted := 0
				if foreign != "" {
					wantAccepted = 1
				}
				if err != nil || len(accepted) != wantAccepted || !reflect.DeepEqual(upstream, []string{file}) {
					t.Fatalf("want no refusal, nothing accepted, upstream [%s]; got %v, %v, %v", file, accepted, upstream, err)
				}
			}
		})
	}
}
