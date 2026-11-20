package refguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// b809LogHook is a foreign reference-transaction hook: it logs its argument
// and every stdin line to $B809_LOG, then exits with $B809_EXIT_<state>
// (default 0).
const b809LogHook = `#!/bin/sh
while read -r old new ref; do echo "$1 $ref" >> "$B809_LOG"; done
eval "code=\${B809_EXIT_$1:-0}"
exit $code
`

// b809Chained is the name suffix of the kept foreign hook.
const b809Chained = ".code-forge-chained"

func b809Repo(t *testing.T) (root, repo, next string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root = t.TempDir()
	repo = filepath.Join(root, "repo")
	must(t, root, "init", "-q", "-b", "main", repo)
	must(t, repo, "config", "user.name", "t")
	must(t, repo, "config", "user.email", "t@example.com")
	must(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	return root, repo, ""
}

func b809Log(t *testing.T, path string) string {
	t.Helper()
	data, _ := os.ReadFile(path)
	return string(data)
}

// A foreign hook in the hooks folder is kept and runs after the guard, in
// the coder worktree and in the main checkout; main moves are still refused.
func TestB809ForeignHookChained(t *testing.T) {
	root, repo, _ := b809Repo(t)
	log := filepath.Join(root, "foreign.log")
	t.Setenv("B809_LOG", log)
	hook := filepath.Join(repo, ".git", "hooks", HookName)
	if err := os.WriteFile(hook, []byte(b809LogHook), 0o755); err != nil {
		t.Fatal(err)
	}
	must(t, repo, "commit", "-q", "--allow-empty", "-m", "next")
	next := must(t, repo, "rev-parse", "HEAD")
	must(t, repo, "reset", "-q", "--keep", "HEAD~1")
	tree := filepath.Join(root, "wt", "B1")
	must(t, repo, "worktree", "add", "-q", "-b", "forge/r1/B1", tree, "main")
	must(t, repo, "checkout", "-q", "--detach")
	got, err := Install(tree, "main", "forge/r1/B1", "forge/{run}/{block}")
	if realGot, _ := filepath.EvalSymlinks(got); err != nil || realGot != must(t, repo, "rev-parse", "--path-format=absolute", "--git-path", "hooks/"+HookName) {
		t.Fatalf("install over a foreign hook: %s %v", got, err)
	}
	if data, _ := os.ReadFile(hook + b809Chained); string(data) != b809LogHook {
		t.Fatalf("foreign hook not kept as %s: %q", hook+b809Chained, data)
	}
	if err := os.Truncate(log, 0); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	before := must(t, repo, "rev-parse", "refs/heads/main")
	out, err := git(t, tree, []string{"B809_LOG=" + log}, "branch", "-f", "main", next)
	if err == nil || !strings.Contains(out, "refused to move refs/heads/main (the main branch)") {
		t.Fatalf("main move from the coder worktree: err=%v out=%q", err, out)
	}
	if must(t, repo, "rev-parse", "refs/heads/main") != before {
		t.Fatal("main moved")
	}
	if l := b809Log(t, log); !strings.Contains(l, "prepared refs/heads/main\n") {
		t.Fatalf("foreign hook did not run after the refusal, with the same stdin: %q", l)
	}
	// Own work in the coder worktree and any move in the main checkout pass
	// both hooks.
	must(t, tree, "commit", "-q", "--allow-empty", "-m", "coder work")
	if _, err := git(t, repo, []string{"B809_LOG=" + log}, "branch", "scratch"); err != nil {
		t.Fatal(err)
	}
	if l := b809Log(t, log); !strings.Contains(l, "committed refs/heads/forge/r1/B1\n") || !strings.Contains(l, "committed refs/heads/scratch\n") {
		t.Fatalf("foreign hook log = %q", l)
	}
	// A second install keeps the chained hook as it is.
	if _, err := Install(tree, "main", "forge/r1/B1", "forge/{run}/{block}"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(hook + b809Chained); string(data) != b809LogHook {
		t.Fatalf("second install changed the chained hook: %q", data)
	}
}

// The chained hook's failure fails the move: the exit status is combined.
func TestB809ForeignHookFailureFailsTheMove(t *testing.T) {
	root, repo, _ := b809Repo(t)
	log := filepath.Join(root, "foreign.log")
	hook := filepath.Join(repo, ".git", "hooks", HookName)
	if err := os.WriteFile(hook, []byte(b809LogHook), 0o755); err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(root, "wt", "B1")
	must(t, repo, "worktree", "add", "-q", "-b", "forge/r1/B1", tree, "main")
	if _, err := Install(tree, "main", "forge/r1/B1", "forge/{run}/{block}"); err != nil {
		t.Fatal(err)
	}
	env := []string{"B809_LOG=" + log, "B809_EXIT_prepared=1"}
	if out, err := git(t, repo, env, "branch", "nope"); err == nil {
		t.Fatalf("a move the foreign hook refuses passed in the main checkout: %s", out)
	}
	if out, err := git(t, tree, env, "branch", "nope2"); err == nil {
		t.Fatalf("a move the foreign hook refuses passed in the coder worktree: %s", out)
	}
	if out, err := git(t, tree, []string{"B809_LOG=" + log}, "branch", "fine"); err != nil {
		t.Fatalf("both hooks pass, still refused: %v %s", err, out)
	}
}

// husky: core.hooksPath inside the work tree. The work tree is not written;
// the coder worktree gets its own hooks folder that runs the guard, then the
// husky reference-transaction hook, and forwards the other husky hooks.
func TestB809HuskyHooksPathChained(t *testing.T) {
	root, repo, _ := b809Repo(t)
	log := filepath.Join(root, "husky.log")
	t.Setenv("B809_LOG", log)
	husky := filepath.Join(repo, ".husky", "_")
	if err := os.MkdirAll(husky, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(husky, HookName), []byte(b809LogHook), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(husky, "pre-commit"), []byte("#!/bin/sh\necho \"pre-commit $(pwd -P)\" >> \"$B809_LOG\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	must(t, repo, "add", ".husky")
	must(t, repo, "commit", "-q", "-m", "husky")
	must(t, repo, "config", "core.hooksPath", ".husky/_")
	must(t, repo, "commit", "-q", "--allow-empty", "-m", "next")
	next := must(t, repo, "rev-parse", "HEAD")
	must(t, repo, "reset", "-q", "--keep", "HEAD~1")
	tree := filepath.Join(root, "wt", "B1")
	must(t, repo, "worktree", "add", "-q", "-b", "forge/r1/B1", tree, "main")
	must(t, repo, "checkout", "-q", "--detach")
	hook, err := Install(tree, "main", "forge/r1/B1", "forge/{run}/{block}")
	if err != nil {
		t.Fatalf("install with husky core.hooksPath: %v", err)
	}
	if !Installed(hook) || strings.HasPrefix(hook, tree) || strings.HasPrefix(hook, repo+string(filepath.Separator)+".husky") {
		t.Fatalf("guard hook at %s", hook)
	}
	before := must(t, repo, "rev-parse", "refs/heads/main")
	out, err := git(t, tree, []string{"B809_LOG=" + log}, "branch", "-f", "main", next)
	if err == nil || !strings.Contains(out, "refused to move refs/heads/main (the main branch)") {
		t.Fatalf("main move from the coder worktree: err=%v out=%q", err, out)
	}
	if must(t, repo, "rev-parse", "refs/heads/main") != before {
		t.Fatal("main moved")
	}
	if l := b809Log(t, log); !strings.Contains(l, "prepared refs/heads/main\n") {
		t.Fatalf("husky reference-transaction did not run: %q", l)
	}
	// The husky pre-commit still runs in the coder worktree, from there.
	must(t, tree, "commit", "-q", "--allow-empty", "-m", "coder work")
	realTree, _ := filepath.EvalSymlinks(tree)
	if l := b809Log(t, log); !strings.Contains(l, "pre-commit "+realTree+"\n") || !strings.Contains(l, "committed refs/heads/forge/r1/B1\n") {
		t.Fatalf("husky hooks in the coder worktree: %q", l)
	}
	// Nothing written in either work tree; the main checkout keeps husky.
	for _, dir := range []string{repo, tree} {
		if st := must(t, dir, "status", "--porcelain"); st != "" {
			t.Fatalf("%s is dirty: %s", dir, st)
		}
	}
	if got := must(t, repo, "config", "core.hooksPath"); got != ".husky/_" {
		t.Fatalf("main checkout core.hooksPath = %s", got)
	}
	if _, err := git(t, repo, []string{"B809_LOG=" + log}, "branch", "-f", "main", next); err != nil {
		t.Fatal("the main checkout could not move main")
	}
	// A second install (the next block open) still works.
	if again, err := Install(tree, "main", "forge/r1/B1", "forge/{run}/{block}"); err != nil || again != hook {
		t.Fatalf("second install: %s %v", again, err)
	}
}

// doctor's view: the chained guard and the per-worktree guard are on.
func TestB809DoctorShowsChainedGuard(t *testing.T) {
	root, repo, _ := b809Repo(t)
	hook := filepath.Join(repo, ".git", "hooks", HookName)
	if err := os.WriteFile(hook, []byte(b809LogHook), 0o755); err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(root, "wt", "B1")
	must(t, repo, "worktree", "add", "-q", "-b", "forge/r1/B1", tree, "main")
	if _, err := Install(tree, "main", "forge/r1/B1", "forge/{run}/{block}"); err != nil {
		t.Fatal(err)
	}
	realHook := must(t, repo, "rev-parse", "--path-format=absolute", "--git-path", "hooks/"+HookName)
	if status, detail := State(repo); status != "OK" || detail != "on: hook "+realHook+"; runs "+realHook+b809Chained+" after the guard; 1 coder worktree(s) guarded" {
		t.Fatalf("chained state = %s %s", status, detail)
	}
	// husky in a second repository
	root2, repo2, _ := b809Repo(t)
	if err := os.MkdirAll(filepath.Join(repo2, ".husky", "_"), 0o755); err != nil {
		t.Fatal(err)
	}
	must(t, repo2, "config", "core.hooksPath", ".husky/_")
	tree2 := filepath.Join(root2, "wt", "B1")
	must(t, repo2, "worktree", "add", "-q", "-b", "forge/r1/B1", tree2, "main")
	if _, err := Install(tree2, "main", "forge/r1/B1", "forge/{run}/{block}"); err != nil {
		t.Fatal(err)
	}
	if status, detail := State(repo2); status != "OK" || !strings.HasPrefix(detail, "on: per-worktree hooks (core.hooksPath ") || !strings.HasSuffix(detail, "/.husky/_ is in a work tree; its reference-transaction runs after the guard); 1 coder worktree(s) guarded") {
		t.Fatalf("husky state = %s %s", status, detail)
	}
}
