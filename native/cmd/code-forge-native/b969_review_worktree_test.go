package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// B969: a run started in a linked git worktree (`run start --cwd .`) and
// `review --run` / `review-file --run` from that worktree use one root: the
// worktree's top. The worker serves the queue there, and the files reviewed
// are the worktree's files, not the main tree's (fake claude on PATH, temp
// HOME, a real `git worktree add`).

// b969Lane adds a linked worktree "lane" of the b553 fixture's repository at
// its committed base and changes src/a.txt there only; the main tree is
// reset to the base, so a review of the main tree has nothing to review.
func b969Lane(t *testing.T, f b553Fix) string {
	t.Helper()
	git := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + f.home}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git(f.repo, "checkout", "-q", "--", "src")
	lane := filepath.Join(f.dir, "lane")
	git(f.repo, "worktree", "add", "-q", "--detach", lane, "HEAD")
	if err := os.WriteFile(filepath.Join(lane, "src", "a.txt"), []byte("hello\nLANE\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return lane
}

func b969Exec(t *testing.T, dir string, env []string, argv ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		code = exit.ExitCode()
	}
	return code, stdout.String(), stderr.String()
}

func TestB969ReviewInLinkedWorktree(t *testing.T) {
	bin := b553Binary(t)
	f := b553Fixture(t, "approve")
	lane := b969Lane(t, f)
	laneFix := f
	laneFix.repo = lane
	t.Cleanup(func() { b553Reap(t, laneFix); b553Reap(t, f) })
	env := b553Env(f, "CODE_FORGE_HANDOVER_LOG=1", "CODE_FORGE_NODE="+filepath.Join(f.dir, "missing-node"))
	run := "r-b969-lane"
	if code, out, errText := b969Exec(t, lane, env, bin, "run", "start", "--cwd", ".", "--run", run); code != 0 {
		t.Fatalf("run start = %d\n%s\n%s", code, out, errText)
	}
	code, out, errText := b969Exec(t, lane, env, bin, "review", "--run", run, "--files", "src/a.txt", "--json", "--max", "60", "--keep-run")
	if code != 0 || !strings.Contains(out, `"ok":true`) || !strings.Contains(out, `{"file":"src/a.txt","result":"approved"`) {
		t.Fatalf("review in the lane = %d\nstdout %s\nstderr %s", code, out, errText)
	}
	if strings.Contains(out+errText, "worker_down") {
		t.Fatalf("worker_down in a lane review:\n%s\n%s", out, errText)
	}
	// B998 (B969 review): review-file --run from the lane queues the lane's
	// file to the lane's worker and its wait reads the same queue.
	block := regexp.MustCompile(`"block":"([^"]+)"`).FindStringSubmatch(out)
	if block == nil {
		t.Fatalf("review names no block: %s", out)
	}
	// a new change in the lane only, so the review is new work, not a cache hit
	laneBody := []byte("hello\nLANE again\nthree\n")
	if err := os.WriteFile(filepath.Join(lane, "src", "a.txt"), laneBody, 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errText = b969Exec(t, lane, env, bin, "review-file", "src/a.txt", "--block", block[1], "--run", run)
	ticket := regexp.MustCompile(`"ticket":"([0-9a-f]+)"`).FindStringSubmatch(out)
	if code != 0 || ticket == nil || strings.Contains(out+errText, "worker_down") {
		t.Fatalf("review-file in the lane = %d\nstdout %s\nstderr %s", code, out, errText)
	}
	code, out, errText = b969Exec(t, lane, env, bin, "review-file", "--wait", ticket[1], "--run", run, "--max", "60s")
	if code != 0 || !strings.Contains(out, `"file":"src/a.txt"`) || !strings.Contains(out, `"status":"reviewed","approved":true`) || strings.Contains(out+errText, "worker_down") {
		t.Fatalf("review-file --wait in the lane = %d\nstdout %s\nstderr %s", code, out, errText)
	}
	if sum := sha256.Sum256(laneBody); !strings.Contains(out, `"content_hash":"`+hex.EncodeToString(sum[:])+`"`) {
		t.Fatalf("review-file reviewed another src/a.txt than the lane's:\n%s", out)
	}
	// the queue and the worker live in the lane, not in the main tree
	if _, err := os.Stat(filepath.Join(f.repo, ".code-forge", "queue")); err == nil {
		t.Fatalf("the main tree got a queue: %s", filepath.Join(f.repo, ".code-forge", "queue"))
	}
	if h := b460Handovers(t, f.home); len(h) != 0 {
		t.Fatalf("hand-overs: %v", h)
	}
}
