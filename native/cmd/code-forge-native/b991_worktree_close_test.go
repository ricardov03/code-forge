package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B991: the whole block flow of a run started in a linked git worktree
// (`run start --cwd .` there): block open, review-file, --wait-block and
// block close (--dry-run first) use the worktree as the run's root and never
// refuse it as other-workspace. A run of another workspace is still refused.
// Real `git worktree add`, fake claude on PATH, temp HOME (b553/b969 fixture).
func TestB991CloseInLinkedWorktree(t *testing.T) {
	bin := b553Binary(t)
	f := b553Fixture(t, "approve")
	lane := b969Lane(t, f)
	laneFix := f
	laneFix.repo = lane
	t.Cleanup(func() { b553Reap(t, laneFix); b553Reap(t, f) })
	env := b553Env(f, "CODE_FORGE_HANDOVER_LOG=1", "CODE_FORGE_NODE="+filepath.Join(f.dir, "missing-node"))
	run := "r-b991-lane"
	step := func(dir string, argv ...string) (int, string) {
		t.Helper()
		code, out, errText := b969Exec(t, dir, env, append([]string{bin}, argv...)...)
		return code, out + errText
	}
	must := func(argv ...string) string {
		t.Helper()
		code, text := step(lane, argv...)
		if code != 0 {
			t.Fatalf("%v = %d\n%s", argv, code, text)
		}
		if strings.Contains(text, "other-workspace") || strings.Contains(text, "another workspace") {
			t.Fatalf("%v refused the lane as another workspace:\n%s", argv, text)
		}
		return text
	}
	must("run", "start", "--cwd", ".", "--run", run)
	acc := filepath.Join(lane, ".code-forge", "b991-acceptance.yml")
	if err := os.WriteFile(acc, []byte("- clause: lane close\n  kind: human_check\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	must("block", "open", "B1", "--run", run, "--level", "L2", "--acceptance", ".code-forge/b991-acceptance.yml", "--owned", "src/a.txt")
	must("review-file", "src/a.txt", "--block", "B1", "--run", run)
	if text := must("review-file", "--wait-block", "B1", "--run", run, "--max", "60s"); !strings.Contains(text, "approved") {
		t.Fatalf("wait-block did not approve src/a.txt:\n%s", text)
	}
	// the close's requeue check: the lane is the run's root, another
	// repository is still another workspace
	other := filepath.Join(f.dir, "other")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if code, out, errText := b969Exec(t, other, []string{"PATH=/usr/bin:/bin", "HOME=" + f.home}, "git", "init", "-q"); code != 0 {
		t.Fatalf("git init = %d\n%s%s", code, out, errText)
	}
	check := func(root string) error {
		_, err := reviewRequeue{home: f.home, repoRoot: root, run: run, block: "B1", tree: lane, files: []string{"src/a.txt"}, print: io.Discard}.pass(true)
		return err
	}
	if err := check(lane); err != nil {
		t.Fatalf("requeue check of the lane = %v, want nil", err)
	}
	if err := check(other); err == nil || !strings.Contains(err.Error(), "another workspace") {
		t.Fatalf("requeue check of another repository = %v, want other-workspace", err)
	}
	must("block", "close", "B1", "--run", run, "--dry-run", "--json")
	// a change after the approval: close restarts the review and joins it
	// in the lane, where the run's worker serves the queue
	if err := os.WriteFile(filepath.Join(lane, "src", "a.txt"), []byte("hello\nLANE AGAIN\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	must("block", "close", "B1", "--run", run)
	if text := must("run", "status", "--run", run); !strings.Contains(text, "B1") {
		t.Fatalf("run status has no B1:\n%s", text)
	}

	if h := b460Handovers(t, f.home); len(h) != 0 {
		t.Fatalf("hand-overs: %v", h)
	}
}
