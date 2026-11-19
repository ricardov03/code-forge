package main

import (
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

// TestB882RunEndStopsTheDetachedWorkerOfItsTest: a test-built binary runs
// `run start` as its own process, which exits; the Setsid worker it launched
// is adopted by init and no longer descends from the test. `run end` of the
// same test (same signal root) still stops that worker it owns (before:
// "run end: kill EPERM", exit 1, and the worker outlived the test).
func TestB882RunEndStopsTheDetachedWorkerOfItsTest(t *testing.T) {
	binary := b511BuildNative(t)
	home, repo, tmpRoot := t.TempDir(), t.TempDir(), t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	b828Exclude(t, repo) // B828: run start refuses a workspace where git would see .code-forge/
	if err := os.WriteFile(filepath.Join(repo, ".code-forge.yml"), []byte("project:\n  slug: unit\ntmp:\n  root: "+tmpRoot+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runID := b511RunID(t)
	env := append(os.Environ(), "HOME="+home, "CODE_FORGE_KEY_BACKEND=file", "CODE_FORGE_KEY_JEV=", "CODE_FORGE_NO_ERROR_LOG=1",
		"CODE_FORGE_TMP_ROOT="+tmpRoot, "CODE_FORGE_NODE="+filepath.Join(home, "missing-node"))
	call := func(args ...string) (int, string) {
		cmd := exec.Command(binary, args...)
		cmd.Dir = repo
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		code := 0
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return code, string(out)
	}
	worker := 0
	t.Cleanup(func() {
		if worker > 0 && syscall.Kill(worker, 0) == nil {
			_ = syscall.Kill(-worker, syscall.SIGKILL)
			t.Errorf("worker pid %d outlived the test", worker)
		}
	})

	code, out := call("run", "start", "--run", runID)
	m := regexp.MustCompile(`worker pid (\d+)`).FindStringSubmatch(out)
	if code != 0 || m == nil {
		t.Fatalf("run start = %d %q", code, out)
	}
	worker, _ = strconv.Atoi(m[1])
	deadline := time.Now().Add(5 * time.Second)
	for {
		ppid, _ := exec.Command("/bin/ps", "-o", "ppid=", "-p", strconv.Itoa(worker)).Output()
		if strings.TrimSpace(string(ppid)) == "1" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker %d still has parent %q", worker, ppid)
		}
		time.Sleep(20 * time.Millisecond)
	}

	code, out = call("run", "end", "--run", runID)
	if code != 0 || strings.Contains(out, "EPERM") {
		t.Fatalf("run end = %d %q", code, out)
	}
	deadline = time.Now().Add(10 * time.Second)
	for syscall.Kill(worker, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if syscall.Kill(worker, 0) == nil {
		t.Fatalf("run end left worker %d running; output %q", worker, out)
	}
}
