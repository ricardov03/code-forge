package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// envWithoutGit is the parent environment without GIT_* variables. Tests that
// build a fixture repo give it to every code-forge child they start, so a
// GIT_DIR, GIT_INDEX_FILE or GIT_WORK_TREE from a hook or a worktree cannot
// point the child's git at another repo.
func envWithoutGit() []string {
	env := []string{}
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "GIT_") {
			env = append(env, v)
		}
	}
	return env
}

// gitFixtureTests build a fixture repo with git and start code-forge children.
var gitFixtureTests = []string{
	"TestBlockLandJoinDaemonLoad",
	"TestGatesChangedTextOnlyAffectedGates",
	"TestGatesGoOnlyNodeProductSelectsInGo",
	"TestGatesGoOnlyRunsBlockTestsAndNativeCompat",
	"TestReviewFileBlockScopeUnreadableIsNative",
	"TestReviewFileWaitBlockLocalJoinIsNative",
	"TestReviewFileWaitBlockStoppedReviewHandsOver",
	"TestReviewFileWaitUsageIsNative",
	"TestReviewWaitBlockDaemonRoute",
	"TestReviewWaitWorkerDownLogging",
	"TestStatusDirectSources",
}

func TestEnvWithoutGitDropsOnlyGitVars(t *testing.T) {
	t.Setenv("GIT_DIR", "/nonexistent-git-dir")
	t.Setenv("GIT_INDEX_FILE", "/nonexistent-index")
	t.Setenv("GIT_WORK_TREE", "/nonexistent-wt")
	t.Setenv("B410_KEEP", "kept")
	want := []string{}
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "GIT_") {
			want = append(want, v)
		}
	}
	got := envWithoutGit()
	if len(got) != len(want) {
		t.Fatalf("envWithoutGit has %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry %d = %q, want %q", i, got[i], want[i])
		}
	}
	kept := 0
	for _, v := range got {
		if strings.HasPrefix(v, "GIT_") {
			t.Fatalf("envWithoutGit kept %q", v)
		}
		if v == "B410_KEEP=kept" {
			kept++
		}
	}
	if kept != 1 {
		t.Fatalf("B410_KEEP=kept appears %d times, want 1", kept)
	}
}

// TestGitFixturesIgnoreInheritedGitEnv reruns the fixture-repo tests in a
// child with GIT_DIR, GIT_INDEX_FILE and GIT_WORK_TREE pointing nowhere; each
// must still pass.
func TestGitFixturesIgnoreInheritedGitEnv(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.count=1", "-test.v", "-test.run=^("+strings.Join(gitFixtureTests, "|")+")$")
	cmd.Env = append(envWithoutGit(), "GIT_DIR=/nonexistent-git-dir", "GIT_INDEX_FILE=/nonexistent-index", "GIT_WORK_TREE=/nonexistent-wt")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture tests fail with inherited GIT_* env: %v\n%s", err, out)
	}
	for _, name := range gitFixtureTests {
		if n := strings.Count(string(out), "--- PASS: "+name+" "); n != 1 {
			t.Fatalf("%s passed %d times in the child, want 1\n%s", name, n, out)
		}
	}
}
