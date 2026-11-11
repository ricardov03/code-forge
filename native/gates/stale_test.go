package gates

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
)

func TestStaleBaseOnlyWhenTheBranchMovedPastTheRecordedBase(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = gitx.ChildEnv()
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
	block := func(status, recorded string) ledger.Object {
		return ledger.Object{{Key: "status", Value: status}, {Key: "base_branch", Value: "main"},
			{Key: "base_sha", Value: recorded}, {Key: "worktree", Value: ledger.Object{{Key: "base_sha", Value: recorded}}}}
	}
	git("init", "-qb", "main")
	base := commit("base")
	if got := StaleBase(repo, block("open", base)); got != nil {
		t.Fatalf("equal base must not be stale: %+v", got)
	}
	tip := commit("moved")
	got := StaleBase(repo, block("open", base))
	if got == nil || got.Branch != "main" || got.Recorded != base || got.Tip != tip {
		t.Fatalf("moved main: want {main %s %s}, got %+v", base, tip, got)
	}
	if got := StaleBase(repo, block("closed", base)); got != nil {
		t.Fatalf("a closed block is not checked: %+v", got)
	}
	noTree := ledger.Object{{Key: "status", Value: "open"}, {Key: "base_branch", Value: "main"}, {Key: "base_sha", Value: base}}
	if got := StaleBase(repo, noTree); got != nil {
		t.Fatalf("a block without a worktree is not checked: %+v", got)
	}
	// a recorded base that is not an ancestor of the tip (diverged) is not stale
	git("checkout", "-qb", "side", base)
	side := commit("side")
	git("checkout", "-q", "main")
	if got := StaleBase(repo, block("open", side)); got != nil {
		t.Fatalf("a diverged base must not be stale: %+v", got)
	}
}
