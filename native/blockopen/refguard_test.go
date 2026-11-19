package blockopen

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/refguard"
	"github.com/ricardov03/code-forge/native/worktree"
)

// B797: block open puts the ref guard on the coder worktree it makes, so
// the coder cannot move main (branch -f, fetch into refs/heads/main).
func TestBlockOpenGuardsTheCoderWorktree(t *testing.T) {
	f := newFitFixture(t)
	t.Setenv(refguard.TokenEnv, "")
	o := f.options(t, "# Screen\n")
	o.Settings = &worktree.Settings{Dir: filepath.Join(t.TempDir(), "wt"), DirSet: true, Branch: "forge/{run}/{block}"}
	result, err := Open(o)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	tree := object(result.Block.Get("worktree")).Get("path").(string)
	git := func(dir string, args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, gitx.ChildEnv()
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	// The main checkout moves to a detached HEAD so only the hook can stop a move.
	if out, err := git(f.repo, "-c", "user.name=F", "-c", "user.email=f@example.invalid", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "next"); err != nil {
		t.Fatal(out)
	}
	next, _ := git(f.repo, "rev-parse", "HEAD")
	if out, err := git(f.repo, "checkout", "-q", "--detach", f.base); err != nil {
		t.Fatal(out)
	}
	if out, err := git(f.repo, "update-ref", "refs/heads/main", f.base); err != nil {
		t.Fatal(out)
	}
	for _, args := range [][]string{{"branch", "-f", "main", next}, {"fetch", "-q", f.repo, next + ":refs/heads/main"}} {
		out, err := git(tree, args...)
		if err == nil || !strings.Contains(out, "refused to move refs/heads/main") {
			t.Fatalf("git %v from the coder worktree: err=%v out=%s", args, err, out)
		}
	}
	if got, _ := git(f.repo, "rev-parse", "refs/heads/main"); got != f.base {
		t.Fatalf("main moved to %s", got)
	}
	if status, detail := refguard.State(f.repo); status != "OK" || !strings.Contains(detail, "1 coder worktree(s) guarded") {
		t.Fatalf("guard state = %s %s", status, detail)
	}
}
