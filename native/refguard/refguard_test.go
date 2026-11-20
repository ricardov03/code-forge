package refguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// git runs git in dir with a clean environment (no global config), extra
// env appended; it returns combined output and the error.
func git(t *testing.T, dir string, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	base := []string{}
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "GIT_") && !strings.HasPrefix(v, TokenEnv+"=") {
			base = append(base, v)
		}
	}
	cmd.Env = append(append(base, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func must(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := git(t, dir, nil, args...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(out)
}

// fixture is a repo on main with a coder worktree on forge/r1/B1, a second
// block branch forge/r1/B2, and the main checkout detached so git's own
// "branch is checked out" checks never stop a move: only the hook does.
func fixture(t *testing.T) (repo, tree, next string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	repo = filepath.Join(root, "repo")
	must(t, root, "init", "-q", "-b", "main", repo)
	must(t, repo, "config", "user.name", "t")
	must(t, repo, "config", "user.email", "t@example.com")
	must(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	must(t, repo, "branch", "forge/r1/B2")
	must(t, repo, "commit", "-q", "--allow-empty", "-m", "next")
	next = must(t, repo, "rev-parse", "HEAD")
	must(t, repo, "reset", "-q", "--keep", "HEAD~1")
	tree = filepath.Join(root, "wt", "B1")
	must(t, repo, "worktree", "add", "-q", "-b", "forge/r1/B1", tree, "main")
	must(t, repo, "checkout", "-q", "--detach")
	if _, err := Install(tree, "main", "forge/r1/B1", "forge/{run}/{block}"); err != nil {
		t.Fatalf("install: %v", err)
	}
	return repo, tree, next
}

func TestGuardRefusesMainMovesFromCoderWorktree(t *testing.T) {
	repo, tree, next := fixture(t)
	before := must(t, repo, "rev-parse", "refs/heads/main")
	for _, args := range [][]string{
		{"branch", "-f", "main", next},
		{"update-ref", "refs/heads/main", next},
		{"fetch", "-q", repo, next + ":refs/heads/main"},
		{"branch", "-D", "main"},
	} {
		out, err := git(t, tree, nil, args...)
		if err == nil {
			t.Fatalf("git %v from the coder worktree succeeded:\n%s", args, out)
		}
		if !strings.Contains(out, "code-forge ref guard: refused to move refs/heads/main (the main branch)") || !strings.Contains(out, "FETCH_HEAD") {
			t.Fatalf("git %v: refusal text = %q", args, out)
		}
		if got := must(t, repo, "rev-parse", "refs/heads/main"); got != before {
			t.Fatalf("git %v moved main to %s", args, got)
		}
	}
}

func TestGuardRefusesOtherBlockBranchesButNotOwnWork(t *testing.T) {
	repo, tree, next := fixture(t)
	other := must(t, repo, "rev-parse", "refs/heads/forge/r1/B2")
	out, err := git(t, tree, nil, "branch", "-f", "forge/r1/B2", next)
	if err == nil || !strings.Contains(out, "refused to move refs/heads/forge/r1/B2 (another block's branch)") {
		t.Fatalf("moving another block's branch: err=%v out=%q", err, out)
	}
	if got := must(t, repo, "rev-parse", "refs/heads/forge/r1/B2"); got != other {
		t.Fatalf("B2 moved to %s", got)
	}
	if out, err := git(t, tree, nil, "branch", "forge/r2/B9"); err == nil {
		t.Fatalf("creating a block branch succeeded: %s", out)
	}
	// The coder's own branch and a scratch branch still work.
	must(t, tree, "commit", "-q", "--allow-empty", "-m", "coder work")
	must(t, tree, "branch", "scratch")
	must(t, tree, "reset", "-q", "--soft", "HEAD~1")
	if must(t, tree, "rev-parse", "HEAD") != must(t, repo, "rev-parse", "main") {
		t.Fatal("own branch did not move back")
	}
}

func TestGuardLetsTheLandTokenAndTheMainCheckoutMoveMain(t *testing.T) {
	repo, tree, next := fixture(t)
	common := must(t, repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	token, err := os.ReadFile(filepath.Join(common, StateDir, "token"))
	if err != nil {
		t.Fatal(err)
	}
	if out, err := git(t, tree, []string{TokenEnv + "=wrong"}, "branch", "-f", "main", next); err == nil {
		t.Fatalf("a wrong token moved main: %s", out)
	}
	if out, err := git(t, tree, []string{TokenEnv + "=" + strings.TrimSpace(string(token))}, "branch", "-f", "main", next); err != nil {
		t.Fatalf("land token refused: %v\n%s", err, out)
	}
	if must(t, repo, "rev-parse", "main") != next {
		t.Fatal("main did not move with the token")
	}
	// An unmarked checkout (the main tree) is not guarded.
	must(t, repo, "branch", "-f", "main", "HEAD")
	if must(t, repo, "rev-parse", "main") == next {
		t.Fatal("the main checkout could not move main")
	}
	// Authorize puts the token in this process's environment.
	t.Setenv(TokenEnv, "")
	Authorize(tree)
	if os.Getenv(TokenEnv) != strings.TrimSpace(string(token)) {
		t.Fatalf("Authorize set %q", os.Getenv(TokenEnv))
	}
}

func TestStateReportsGuardedWorktrees(t *testing.T) {
	repo, _, _ := fixture(t)
	status, detail := State(repo)
	if status != "OK" || !strings.Contains(detail, "1 coder worktree(s) guarded") {
		t.Fatalf("state = %s %s", status, detail)
	}
	hooks := must(t, repo, "rev-parse", "--path-format=absolute", "--git-path", "hooks")
	if err := os.Remove(filepath.Join(hooks, HookName)); err != nil {
		t.Fatal(err)
	}
	if status, detail := State(repo); status != "WARN" || !strings.Contains(detail, "is missing") {
		t.Fatalf("state after removing the hook = %s %s", status, detail)
	}
}

func TestUnsafeListsMainMovesSinceTheLastLand(t *testing.T) {
	repo, _, next := fixture(t)
	common := must(t, repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	reviewed := ReviewedPath(common)
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(reviewed, old, old); err != nil {
		t.Fatal(err)
	}
	must(t, repo, "checkout", "-q", "main")
	// The fixture reset main back one commit: that is not a land.
	bad, path, err := Unsafe(repo, "main")
	if err != nil || len(bad) != 1 || !strings.HasPrefix(bad[0].Subject, "reset: moving to HEAD~1") || path != reviewed {
		t.Fatalf("after reset: %v %s %v", bad, path, err)
	}
	text := Refusal("main", repo, path, bad)
	if !strings.Contains(text, "1 reflog entry") || !strings.Contains(text, "reset: moving to HEAD~1") || !strings.Contains(text, "touch "+reviewed) {
		t.Fatalf("refusal = %q", text)
	}
	// A land merge marks the point the next check starts from.
	cmd := exec.Command("git", "merge", "-q", "--ff-only", next)
	cmd.Dir = repo
	cmd.Env = append(LandEnv("B1"), "HOME="+os.Getenv("HOME"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("land merge: %v\n%s", err, out)
	}
	if top := must(t, repo, "reflog", "show", "-1", "--format=%gs", "refs/heads/main"); !strings.HasPrefix(top, "code-forge land B1: ") {
		t.Fatalf("land reflog entry = %q", top)
	}
	if bad, _, _ := Unsafe(repo, "main"); len(bad) != 0 {
		t.Fatalf("entries before the land still flagged: %v", bad)
	}
	// A plain fast-forward commit on main is fine.
	must(t, repo, "commit", "-q", "--allow-empty", "-m", "owner commit")
	if bad, _, err := Unsafe(repo, "main"); err != nil || len(bad) != 0 {
		t.Fatalf("plain commit flagged: %v %v", bad, err)
	}
	must(t, repo, "commit", "-q", "--amend", "--allow-empty", "-m", "rewritten")
	if bad, _, _ := Unsafe(repo, "main"); len(bad) != 1 || !strings.HasPrefix(bad[0].Subject, "commit (amend): ") {
		t.Fatalf("amend after land: %v", bad)
	}
	// Touching the reviewed mark accepts what is there.
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(reviewed, later, later); err != nil {
		t.Fatal(err)
	}
	if bad, _, _ := Unsafe(repo, "main"); len(bad) != 0 {
		t.Fatalf("reviewed entries still flagged: %v", bad)
	}
}
