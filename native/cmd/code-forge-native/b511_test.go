package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// B511/B512: plain `run start` and `run start --add-worker` launch this
// binary's `worker --run <id>` (the Go worker) and pin it; node is never
// needed. These tests build the real binary, point run start at it and check
// the process that serves the queue, the record, the signed rows and the stop.

// b511BuildNative builds this package into a temp dir (call before a chdir).
func b511BuildNative(t *testing.T) string {
	t.Helper()
	pkg, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "code-forge-native")
	// B529: a binary built for a test refuses every signal to a process the
	// test did not start, even when it runs with an environment of its own.
	cmd := exec.Command("go", "build", "-ldflags", "-X github.com/ricardov03/code-forge/native/procguard.builtForTest=1", "-o", out, ".")
	cmd.Dir = pkg
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if text, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, text)
	}
	return out
}

func b511RunID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return "r-b511-" + hex.EncodeToString(b)
}

// b511Command is the live command line of pid ("" when it is gone).
func b511Command(pid int) string {
	out, err := exec.Command("/bin/ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// b511Stop sends SIGTERM to each launched worker's process group and waits
// for every pid to be gone; it fails the test when one is left.
func b511Stop(t *testing.T, pids ...int) {
	t.Helper()
	for _, pid := range pids {
		_ = syscall.Kill(-pid, syscall.SIGTERM)
	}
	deadline := time.Now().Add(10 * time.Second)
	for _, pid := range pids {
		for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if syscall.Kill(pid, 0) == nil {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			t.Errorf("worker pid %d did not stop on SIGTERM", pid)
		}
	}
}

func b511Pgrep(pattern string) string {
	out, _ := exec.Command("pgrep", "-f", pattern).Output()
	return strings.TrimSpace(string(out))
}

func TestB511RunStartLaunchesTheGoWorker(t *testing.T) {
	binary := b511BuildNative(t)
	f := newStartFixture(t, "project:\n  slug: unit\ntmp:\n  root: TMPROOT\n")
	runWorkerBinary = func() (string, error) { return binary, nil }
	t.Setenv("CODE_FORGE_KEY_BACKEND", "file")
	t.Setenv("CODE_FORGE_KEY_JEV", "")
	t.Setenv("CODE_FORGE_NATIVE", "1")
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	t.Setenv("CODE_FORGE_TMP_ROOT", f.tmpRoot)
	runLaunchTimeout = 60 * time.Second
	runID := b511RunID(t)
	launched := []int{}
	t.Cleanup(func() {
		b511Stop(t, launched...)
		if left := b511Pgrep("worker --run " + runID); left != "" {
			t.Errorf("worker processes left: %s", left)
		}
		if left := b511Pgrep("--run " + runID); left != "" {
			t.Errorf("processes of the run left: %s", left)
		}
	})

	code, out, errs := runVerbCall(t, "start", "--cwd", f.repo, "--run", runID)
	m := regexp.MustCompile(`^run ` + runID + ` started · engine auto · worker pid (\d+)\n$`).FindStringSubmatch(out)
	if code != 0 || errs != "" || m == nil {
		t.Fatalf("start = %d %q %q", code, out, errs)
	}
	pid, _ := strconv.Atoi(m[1])
	launched = append(launched, pid)
	if got, want := b511Command(pid), binary+" worker --run "+runID; got != want {
		t.Fatalf("the pinned worker runs %q, want %q", got, want)
	}
	repo, _ := filepath.EvalSymlinks(f.repo)
	announced, _ := filepath.Glob(filepath.Join(repo, ".code-forge", "workers", fmt.Sprintf("%d-*.json", pid)))
	if len(announced) != 1 {
		t.Fatalf("registry: %v", announced)
	}
	if data, _ := os.ReadFile(announced[0]); !bytes.Contains(data, []byte(`"run":"`+runID+`"`)) && !bytes.Contains(data, []byte(`"run": "`+runID+`"`)) {
		t.Fatalf("announcement: %s", data)
	}
	record := f.read(t, runID)
	workers := record["workers"].([]any)
	if len(workers) != 1 || workers[0].(map[string]any)["pid"] != float64(pid) || record["worker"].(map[string]any)["pid"] != float64(pid) {
		t.Fatalf("record: %#v", record)
	}
	rows := f.startRows(t, runID)
	if len(rows) != 2 || rows[0]["event"] != "run.start" || rows[0]["worker_pinned"] != false || rows[1]["event"] != "worker.reattach" || rows[1]["pid"] != float64(pid) {
		t.Fatalf("rows: %#v", rows)
	}

	// B512: --add-worker launches a second Go worker under review.workers 2
	f.rewriteSnapshot(t, runID, "project:\n  slug: unit\ntmp:\n  root: "+f.tmpRoot+"\nreview:\n  workers: 2\n")
	code, out, errs = runVerbCall(t, "start", "--add-worker", "--run", runID)
	m = regexp.MustCompile(`^run ` + runID + `: added worker pid (\d+) \(review\.workers limit 2\)\n$`).FindStringSubmatch(out)
	if code != 0 || errs != "" || m == nil {
		t.Fatalf("add = %d %q %q", code, out, errs)
	}
	added, _ := strconv.Atoi(m[1])
	launched = append(launched, added)
	if got, want := b511Command(added), binary+" worker --run "+runID; got != want || added == pid {
		t.Fatalf("the added worker runs %q (pid %d), want %q", got, added, want)
	}
	record = f.read(t, runID)
	if workers := record["workers"].([]any); len(workers) != 2 || workers[1].(map[string]any)["pid"] != float64(added) {
		t.Fatalf("record after add: %#v", record)
	}
	if rows := f.startRows(t, runID); len(rows) != 3 || rows[2]["event"] != "worker.add" || rows[2]["pid"] != float64(added) {
		t.Fatalf("rows after add: %#v", rows)
	}
	code, out, errs = runVerbCall(t, "start", "--add-worker", "--run", runID)
	if code != 1 || out != "" || errs != "run start: review.workers limit 2 reached\n" {
		t.Fatalf("add over the limit = %d %q %q", code, out, errs)
	}

	// Neither run start nor the workers handed anything to Node.
	if data, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "native", "handovers.jsonl")); err == nil {
		t.Fatalf("hand-overs: %s", data)
	}

	// SIGTERM stops each Go worker cleanly: its announcement is withdrawn.
	b511Stop(t, launched...)
	launched = nil
	if left, _ := filepath.Glob(filepath.Join(repo, ".code-forge", "workers", "*.json")); len(left) != 0 {
		t.Fatalf("announcements left after the stop: %v", left)
	}
}

// TestB511SweepStopsGoWorkersOfEndedRuns: run start's process sweep stops a Go
// worker (`code-forge-native worker --run <id>`, what run start now launches)
// of an ended or unknown run, as it stops Node's worker; a Go worker of an
// active run, of another HOME, and look-alike commands get no signal.
func TestB511SweepStopsGoWorkersOfEndedRuns(t *testing.T) {
	f := newStartFixture(t, "project:\n  slug: unit\ntmp:\n  root: TMPROOT\n")
	f.workspace = f.repo
	f.record(t, "r-old", "ended", `,"blocks":{}`)
	f.record(t, "r-live", "active", `,"blocks":{}`)
	goWorker := func(run string) string { return "/x/native/bin/darwin-arm64/code-forge-native worker --run " + run }
	w := &sweepWorld{}
	home := map[string]string{"HOME": f.home}
	ended := w.spawn(t, false, 4242, goWorker("r-old"), home)
	missing := w.spawn(t, false, 4242, "code-forge-native worker --run=r-none", home)
	active := w.spawn(t, false, 4242, goWorker("r-live"), home)
	otherHome := w.spawn(t, false, 4242, goWorker("r-old"), map[string]string{"HOME": t.TempDir()})
	lookAlike := w.spawn(t, false, 4242, "/x/my-code-forge-native-tool worker --run r-old", home)
	inShell := w.spawn(t, false, 4242, "sh -c code-forge-native worker --run r-old", home)
	w.install(t)
	sleeper, _ := startSleeper(t)
	code, out, errs := runVerbCall(t, "start", "--cwd", f.repo, "--run", "r-new", "--worker-pid", strconv.Itoa(sleeper.Process.Pid))
	if code != 0 || errs != "" || out == "" {
		t.Fatalf("start = %d %q %q", code, out, errs)
	}
	waitGone(t, ended)
	waitGone(t, missing)
	want := []string{fmt.Sprintf("%d:terminated", ended.pid), fmt.Sprintf("%d:terminated", missing.pid)}
	if got := w.sent(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("signals = %v, want %v", got, want)
	}
	for _, c := range []*sweepChild{active, otherHome, lookAlike, inShell} {
		if !w.alive(c) {
			t.Fatalf("pid %d (%s) was stopped", c.pid, c.command)
		}
	}
}

// TestB511RunStartNeedsNoNode: with no node on the machine, plain run start
// and --add-worker launch and pin the worker in Go (before B511 both handed
// over to Node, which exited 127 here) and log no hand-over.
func TestB511RunStartNeedsNoNode(t *testing.T) {
	f := newStartFixture(t, "project:\n  slug: unit\ntmp:\n  root: TMPROOT\n")
	t.Setenv("CODE_FORGE_NODE", filepath.Join(f.home, "missing-node"))
	t.Setenv("CODE_FORGE_NATIVE", "1")
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	code, out, errs := runVerbCall(t, "start", "--cwd", f.repo, "--run", "r-red")
	if code != 0 || !regexp.MustCompile(`^run r-red started · engine auto · worker pid \d+\n$`).MatchString(out) || errs != "" {
		t.Fatalf("start = %d %q %q", code, out, errs)
	}
	f.rewriteSnapshot(t, "r-red", "project:\n  slug: unit\nreview:\n  workers: 2\n")
	code, out, errs = runVerbCall(t, "start", "--add-worker", "--run", "r-red")
	if launched := f.launched(t); code != 0 || errs != "" || len(launched) != 2 || out != fmt.Sprintf("run r-red: added worker pid %d (review.workers limit 2)\n", launched[1]) {
		t.Fatalf("add = %d %q %q %v", code, out, errs, launched)
	}
	if data, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "native", "handovers.jsonl")); err == nil {
		t.Fatalf("hand-overs: %s", data)
	}
}
