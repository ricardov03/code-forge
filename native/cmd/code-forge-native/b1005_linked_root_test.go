package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B1005: a run started in a linked git worktree (`run start --cwd .` there,
// B969) has that worktree as its root. The callers that read the run's
// files (brief, config, queue, worker registry) read them there, not in the
// main working tree. Real `git worktree add`, temp HOME.

// b1005Lane: the b553 fixture, a linked worktree "lane" and a run started
// in it. extra is written as the lane's .code-forge.yml tail before the run
// starts (its config snapshot).
func b1005Lane(t *testing.T, extra string) (string, b553Fix, string, []string, string) {
	t.Helper()
	bin := b553Binary(t)
	f := b553Fixture(t, "approve")
	lane := b969Lane(t, f)
	laneFix := f
	laneFix.repo = lane
	t.Cleanup(func() { b553Reap(t, laneFix); b553Reap(t, f) })
	if extra != "" {
		if err := os.WriteFile(filepath.Join(lane, ".code-forge.yml"), []byte(b553Config+extra), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env := b553Env(f, "CODE_FORGE_HANDOVER_LOG=1", "CODE_FORGE_NODE="+filepath.Join(f.dir, "missing-node"))
	run := "r-b1005"
	if code, out, errText := b969Exec(t, lane, env, bin, "run", "start", "--cwd", ".", "--run", run); code != 0 {
		t.Fatalf("run start = %d\n%s%s", code, out, errText)
	}
	return bin, f, lane, env, run
}

func b1005Must(t *testing.T, dir string, env []string, argv ...string) string {
	t.Helper()
	code, out, errText := b969Exec(t, dir, env, argv...)
	if code != 0 {
		t.Fatalf("%v = %d\n%s%s", argv[1:], code, out, errText)
	}
	return out + errText
}

// context pack reads the block brief and the config of the lane: a brief
// that exists only in the lane is in the pack.
func TestB1005ContextPackInLinkedWorktree(t *testing.T) {
	bin, f, lane, env, run := b1005Lane(t, "")
	if err := os.MkdirAll(filepath.Join(lane, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lane, "notes", "b1.md"), []byte("LANE ONLY BRIEF\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lane, ".code-forge", "b1005-acc.yml"), []byte("- clause: lane brief\n  kind: human_check\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b1005Must(t, lane, env, bin, "block", "open", "B1", "--run", run, "--level", "L1", "--acceptance", ".code-forge/b1005-acc.yml", "--brief", "notes/b1.md", "--owned", "src/a.txt")
	if text := b1005Must(t, lane, env, bin, "context", "pack", "--run", run, "--block", "B1"); !strings.Contains(text, "LANE ONLY BRIEF") {
		t.Fatalf("the context pack has no lane brief:\n%s", text)
	}
	if h := b460Handovers(t, f.home); len(h) != 0 {
		t.Fatalf("hand-overs: %v", h)
	}
}

// doctor reads the worker registry of the lane it runs in: an unreadable
// worker entry there is a worker-registry WARN row.
func TestB1005DoctorWorkerRegistryInLinkedWorktree(t *testing.T) {
	bin, f, lane, env, _ := b1005Lane(t, "")
	if err := os.MkdirAll(filepath.Join(lane, ".code-forge", "workers"), 0o755); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(lane, ".code-forge", "workers", "b1005-bad.json")
	if err := os.WriteFile(bad, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, out, errText := b969Exec(t, lane, env, bin, "doctor", "--json")
	if !strings.Contains(out+errText, "b1005-bad.json") {
		t.Fatalf("doctor in the lane does not name the lane's bad worker entry:\n%s%s", out, errText)
	}
	if h := b460Handovers(t, f.home); len(h) != 0 {
		t.Fatalf("hand-overs: %v", h)
	}
}

// land's review join with native.daemon: false (landJoinStep) finds the
// run's live worker in the lane, where it serves the queue, and answers
// approved, not worker_down.
func TestB1005LandJoinInLinkedWorktree(t *testing.T) {
	bin, _, lane, env, run := b1005Lane(t, "native:\n  daemon: false\n")
	if err := os.WriteFile(filepath.Join(lane, ".code-forge", "b1005-acc.yml"), []byte("- clause: lane join\n  kind: human_check\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b1005Must(t, lane, env, bin, "block", "open", "B1", "--run", run, "--level", "L1", "--acceptance", ".code-forge/b1005-acc.yml", "--owned", "src/a.txt")
	b1005Must(t, lane, env, bin, "review-file", "src/a.txt", "--block", "B1", "--run", run)
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok {
			t.Setenv(k, v)
		}
	}
	t.Setenv("CODE_FORGE_RUN_ID", "")
	var stdout, stderr bytes.Buffer
	code := landJoinStep(false, "B1", run, lane, "60s", 60*time.Second, &landStep{stdout: &stdout, stderr: &stderr})
	if code != 0 || !strings.Contains(stdout.String(), "approved") {
		t.Fatalf("land join in the lane = %d\n%s%s", code, stdout.String(), stderr.String())
	}
}

// review-file with no --run (the hand-over guard) lists the workers of the
// lane it runs in.
func TestB1005HandoverWorkerRunsInLinkedWorktree(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo, lane := filepath.Join(dir, "repo"), filepath.Join(dir, "lane")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	b460Git(t, repo, "init", "-q")
	b460Write(t, filepath.Join(repo, "a.txt"), "a\n")
	b460Git(t, repo, "add", "-A")
	b460Git(t, repo, "commit", "-qm", "base")
	b460Git(t, repo, "worktree", "add", "-q", "--detach", lane, "HEAD")
	b460Write(t, filepath.Join(repo, ".code-forge", "workers", "main.json"), `{"run":"r-main"}`)
	if err := os.MkdirAll(filepath.Join(lane, ".code-forge", "queue"), 0o755); err != nil {
		t.Fatal(err)
	}
	b460Write(t, filepath.Join(lane, ".code-forge", "workers", "lane.json"), `{"run":"r-lane"}`)
	b460Chdir(t, lane)
	if runs := hudRegisteredWorkerRuns(); !slices.Equal(runs, []string{"r-lane"}) {
		t.Fatalf("worker runs in the lane = %v, want [r-lane]", runs)
	}
	b460Chdir(t, repo)
	if runs := hudRegisteredWorkerRuns(); !slices.Equal(runs, []string{"r-main"}) {
		t.Fatalf("worker runs in the main tree = %v, want [r-main]", runs)
	}
}

// a code-forge block worktree of a lane run is the lane's: its scope check
// (reviewBlockTree) passes with the lane as root, as with the main tree.
func TestB1005BlockTreeOfLaneRun(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo, lane, tree := filepath.Join(dir, "repo"), filepath.Join(dir, "lane"), filepath.Join(dir, "tree")
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	b460Git(t, repo, "init", "-q")
	b460Write(t, filepath.Join(repo, "src", "a.txt"), "a\n")
	b460Git(t, repo, "add", "-A")
	b460Git(t, repo, "commit", "-qm", "base")
	b460Git(t, repo, "worktree", "add", "-q", "--detach", lane, "HEAD")
	base := strings.TrimSpace(b460Git(t, lane, "rev-parse", "HEAD"))
	b460Git(t, lane, "worktree", "add", "-q", "-b", "forge/r-lane/B1", tree, "HEAD")
	record := ledger.Object{}
	record.Set("workspace", lane)
	entry := ledger.Object{}
	wt := ledger.Object{}
	wt.Set("path", tree)
	wt.Set("branch", "forge/r-lane/B1")
	wt.Set("base_sha", base)
	entry.Set("worktree", wt)
	ctx := context.Background()
	if !reviewBlockTree(ctx, lane, record, entry) {
		t.Fatal("the block worktree of a lane run is refused with the lane as root")
	}
	if !reviewBlockTree(ctx, repo, record, entry) {
		t.Fatal("the block worktree of a lane run is refused with the main tree as root")
	}
	if reviewBlockTree(ctx, filepath.Join(dir, "other"), record, entry) {
		t.Fatal("a root of no repository passed")
	}
}

// spawn --run --block of a lane run reads the block brief in the lane: a
// brief that exists only there reaches the coder's task.
func TestB1005SpawnCoderInLinkedWorktree(t *testing.T) {
	w := b549Fixture(t, "ok", "")
	repo, lane := filepath.Join(w, "repo"), filepath.Join(w, "lane")
	b460Git(t, repo, "add", "-A")
	b460Git(t, repo, "commit", "-qm", "base")
	b460Git(t, repo, "worktree", "add", "-q", "--detach", lane, "HEAD")
	b460Write(t, filepath.Join(lane, "notes", "lane.md"), "LANE ONLY BRIEF\n")
	record := strings.ReplaceAll(b549RecordHead, "@REPO@", lane) + `"blocks":{"B1":{"brief":{"path":"notes/lane.md"},"acceptance":[{"clause":"it builds","tests":["TestBuild"]}]}}}`
	runs := filepath.Join(w, "home", ".code-forge", "runs")
	if err := os.WriteFile(filepath.Join(runs, "r1.json"), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runs, "r1.key"), bytes.Repeat([]byte{7}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	b460Chdir(t, lane)
	code, out, errText := b460Call(t, "spawn", "--level", "L1", "--role", "coder", "--brief", "brief.md", "--run", "r1", "--block", "B1")
	if code != 0 || !strings.Contains(out, `"status":"ok"`) {
		t.Fatalf("spawn coder in the lane = %d\n%s%s", code, out, errText)
	}
	prompt, err := os.ReadFile(filepath.Join(w, "log", "prompt.txt"))
	if err != nil || !strings.Contains(string(prompt), "LANE ONLY BRIEF") {
		t.Fatalf("the coder's prompt has no lane brief (%v):\n%s", err, prompt)
	}
}
