package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B363: block land gates a stale base on with the --allow-stale-base WARN line.
func TestGatesStaleWarningNamesTheFixOnlyForAStaleBase(t *testing.T) {
	home, repo := t.TempDir(), t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(text string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		git("add", ".")
		git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", text)
		return git("rev-parse", "HEAD")
	}
	git("init", "-qb", "main")
	base := commit("base")
	run := "r-stale-warn"
	entry := ledger.Object{{Key: "status", Value: "open"}, {Key: "base_sha", Value: base}, {Key: "base_branch", Value: "main"},
		{Key: "worktree", Value: ledger.Object{{Key: "path", Value: repo}, {Key: "branch", Value: "main"}, {Key: "base_sha", Value: base}}}}
	record := ledger.Object{{Key: "run_id", Value: run}, {Key: "project", Value: "fixture"}, {Key: "workspace", Value: repo},
		{Key: "status", Value: "active"}, {Key: "blocks", Value: ledger.Object{{Key: "B1", Value: entry}}}, {Key: "orphans", Value: []any{}}}
	if err := state.SaveRun(home, record); err != nil {
		t.Fatal(err)
	}
	if got := gatesStaleWarning(home, run, "B1"); got != "" {
		t.Fatalf("equal base: want no line, got %q", got)
	}
	tip := commit("moved")
	got := gatesStaleWarning(home, run, "B1")
	want := "gates run: WARN block B1 of run r-stale-warn gates on a stale base (main moved " + base[:12] + " → " + tip[:12] +
		") because of --allow-stale-base; fix: code-forge block rebase B1 --run r-stale-warn --accept-landed\n"
	if got != want {
		t.Fatalf("stale base:\nwant %q\ngot  %q", want, got)
	}
	if got := gatesStaleWarning(home, "r-missing", "B1"); got != "" {
		t.Fatalf("missing run: want no line, got %q", got)
	}
}
