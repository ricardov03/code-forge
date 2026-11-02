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
	for _, kind := range []string{"closed", "custom", "open", "other-run", "missing", "reverted", "rename", "unicode-boundary"} {
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
			sibling := ledger.Object{{Key: "status", Value: status}}
			if kind == "custom" {
				sibling.Set("worktree", ledger.Object{{Key: "branch", Value: branch}})
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
			accepted, err := origins(tree, "r-test", base, g("rev-parse", "HEAD"), blocks, owns)
			if kind == "unicode-boundary" {
				if err != nil || len(accepted) != 0 {
					t.Fatalf("Node ? matches one UTF-16 unit; astral filename is unowned: got %v, %v", accepted, err)
				}
			} else if kind == "closed" || kind == "custom" || kind == "rename" {
				want := []Accepted{{File: file, Blocks: []string{"A"}}}
				if err != nil || !reflect.DeepEqual(accepted, want) {
					t.Fatalf("accepted = %v, %v; want exactly %v", accepted, err, want)
				}
			} else {
				sha := landed
				if foreign != "" {
					sha = foreign
				}
				if err == nil || !strings.Contains(err.Error(), "owned-conflict") || !strings.Contains(err.Error(), file) || !strings.Contains(err.Error(), sha) {
					t.Fatalf("want owned-conflict naming %s and %s; got %v, %v", file, sha, accepted, err)
				}
			}
		})
	}
}
