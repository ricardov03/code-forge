package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/refguard"
)

// B797: every context pack carries the generated git rules: the moves the
// ref guard refuses, the other forbidden git actions and the one push form.
func TestB797ContextPackListsGitRules(t *testing.T) {
	config := "version: 1\ncontext:\n  commands:\n    test: [go, test]\n"
	f := newContextFixture(t, map[string]any{"B1": map[string]any{"status": "open", "level": "L1", "base_branch": "main", "worktree": map[string]any{"branch": "forge/r-ctx/B1"}}}, config)
	remote := exec.Command("git", "remote", "add", "origin", "git@github.com:acme/widgets.git")
	remote.Dir, remote.Env = f.workspace, gitx.ChildEnv()
	if out, err := remote.CombinedOutput(); err != nil {
		t.Fatalf("remote add: %s %v", out, err)
	}
	code, out, errText := runCaptured(t, "context", "pack", "--run", "r-ctx", "--block", "B1")
	if code != 0 || errText != "" {
		t.Fatalf("code=%d stderr=%q", code, errText)
	}
	for _, want := range []string{
		"## git rules (code-forge)\n",
		"- never move main: no git branch -f, git update-ref, git reset or git fetch into it; read the remote with git fetch origin, then use FETCH_HEAD",
		"- never move or delete another block's branch; commit only on forge/r-ctx/B1\n",
		"never force push", "never rm -rf", "never git stash", "never git reset --hard",
		"- push only with: git -c credential.helper= -c credential.helper='!gh auth git-credential' push https://github.com/acme/widgets.git forge/r-ctx/B1:forge/r-ctx/B1\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("pack lacks %q:\n%s", want, out)
		}
	}
}

// B797: doctor shows the guard state once a coder worktree was opened: on
// with the guarded count, or WARN with why it is off. A repository that
// never had one shows no row (the b525 goldens).
func TestB797DoctorShowsTheRefGuard(t *testing.T) {
	e := b525Setup(t, b525Case{mode: "ok", config: "provider: anthropic\n" + b474Levels, fakes: []string{"claude", "grok"}}, "")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = e.repo, gitx.ChildEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "base")
	tree := filepath.Join(e.base, "wt", "B1")
	git("worktree", "add", "-q", "-b", "forge/r1/B1", tree)
	hook, err := refguard.Install(tree, "main", "forge/r1/B1", "forge/{run}/{block}")
	if err != nil {
		t.Fatal(err)
	}
	_, out, _ := runCaptured(t, "doctor")
	if want := "OK   ref guard: on: hook " + hook + "; 1 coder worktree(s) guarded\nINFO native daemon: "; !strings.Contains(out, want) {
		t.Fatalf("doctor lacks %q:\n%s", want, out)
	}
	// Another tool's hook in its place: the guard is off and doctor warns.
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := refguard.Install(tree, "main", "forge/r1/B1", "forge/{run}/{block}"); err == nil {
		t.Fatal("install over a foreign hook succeeded")
	}
	_, out, _ = runCaptured(t, "doctor")
	if want := "WARN ref guard: off: " + hook + " already holds another hook; the guard is off\n"; !strings.Contains(out, want) {
		t.Fatalf("doctor lacks %q:\n%s", want, out)
	}
	e.assertClean(t)
}
