package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// queueProc runs the public router (TestGatesProcess) in a child process with
// a private HOME; the queue switches inherited from a parent gate are cleared.
func queueProc(t *testing.T, home, dir string, args ...string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(args)
	cmd := exec.Command(exe, "-test.run=^TestGatesProcess$")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HOME="+home, "CODE_FORGE_NO_ERROR_LOG=1", "CODE_FORGE_HANDOVER_LOG=0", "CODE_FORGE_NODE=/nonexistent-code-forge-node",
		"CF_GATES_PROCESS=1", "CF_GATES_ARGS="+string(encoded), "CODE_FORGE_TEST_QUEUE=", "CODE_FORGE_TEST_QUEUE_HELD=")
	return cmd
}

func exitCode(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	if e, ok := err.(*exec.ExitError); ok {
		return e.ExitCode()
	}
	t.Fatal(err)
	return -1
}

type queueRow struct {
	Position int    `json:"position"`
	Kind     string `json:"kind"`
	PID      int    `json:"pid"`
	Label    string `json:"label"`
	WaitMS   *int64 `json:"wait_ms"`
	HeldMS   *int64 `json:"held_ms"`
}

type queueView struct {
	Holders []queueRow `json:"holders"`
	Waiters []queueRow `json:"waiters"`
}

func readQueue(t *testing.T, home string) queueView {
	t.Helper()
	out, err := queueProc(t, home, home, "queue", "status", "--json").Output()
	if err != nil {
		t.Fatalf("queue status --json: %v: %s", err, out)
	}
	var v queueView
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("queue status JSON: %v: %s", err, out)
	}
	return v
}

func waitQueue(t *testing.T, home string, holders, waiters int) queueView {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		v := readQueue(t, home)
		if len(v.Holders) == holders && len(v.Waiters) == waiters {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatalf("want %d holders and %d waiters, have %+v", holders, waiters, v)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// holdSlot starts `queue run --kind full -- sleep` and waits until it holds.
func holdSlot(t *testing.T, home, label string) *exec.Cmd {
	t.Helper()
	holder := queueProc(t, home, home, "queue", "run", "--kind", "full", "--label", label, "--", "/bin/sleep", "30")
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Process.Kill(); _ = holder.Wait() })
	waitQueue(t, home, 1, 0)
	return holder
}

func TestQueueRunPassesExitCode(t *testing.T) {
	home := t.TempDir()
	out, err := queueProc(t, home, home, "queue", "run", "--", "/bin/sh", "-c", "exit 3").CombinedOutput()
	if code := exitCode(t, err); code != 3 {
		t.Fatalf("queue run -- sh -c 'exit 3': exit %d, want 3: %s", code, out)
	}
	if v := readQueue(t, home); len(v.Holders) != 0 || len(v.Waiters) != 0 {
		t.Fatalf("after queue run: want an empty queue, have %+v", v)
	}
	out, err = queueProc(t, home, home, "queue", "run", "--kind", "full").CombinedOutput()
	if code := exitCode(t, err); code != 2 || !strings.HasPrefix(string(out), "usage: code-forge queue") {
		t.Fatalf("queue run without a command: exit %d, want 2 with usage: %s", code, out)
	}
}

func TestQueueStatusListsHolderAndWaiterWithWaitTime(t *testing.T) {
	home := t.TempDir()
	holder := holdSlot(t, home, "hold")
	waiter := queueProc(t, home, home, "queue", "run", "--kind", "changed", "--label", "next", "--", "/bin/sh", "-c", "exit 0")
	var stderr strings.Builder
	waiter.Stderr = &stderr
	if err := waiter.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = waiter.Process.Kill() })
	waitQueue(t, home, 1, 1)
	time.Sleep(300 * time.Millisecond)
	v := readQueue(t, home)
	if len(v.Holders) != 1 || len(v.Waiters) != 1 {
		t.Fatalf("want exactly 1 holder and 1 waiter, have %+v", v)
	}
	h, w := v.Holders[0], v.Waiters[0]
	if h.Kind != "full" || h.Label != "hold" || h.PID != holder.Process.Pid || h.WaitMS == nil || *h.WaitMS < 0 || h.HeldMS == nil || *h.HeldMS < 300 {
		t.Fatalf("holder row: want full/hold/pid %d with wait_ms and held_ms >= 300, got %+v", holder.Process.Pid, h)
	}
	if w.Position != 1 || w.Kind != "changed" || w.Label != "next" || w.PID != waiter.Process.Pid || w.WaitMS == nil || *w.WaitMS < 300 || w.HeldMS != nil {
		t.Fatalf("waiter row: want #1 changed/next/pid %d waiting >= 300 ms, got %+v", waiter.Process.Pid, w)
	}
	text, err := queueProc(t, home, home, "queue").Output()
	if err != nil || !strings.Contains(string(text), "#1 ") || !strings.Contains(string(text), "next") || !strings.Contains(string(text), "hold") {
		t.Fatalf("queue (text): want the holder and waiter #1 listed: %v: %s", err, text)
	}
	if err := holder.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = holder.Wait()
	if code := exitCode(t, waiter.Wait()); code != 0 {
		t.Fatalf("waiter exit %d after the holder died, want 0: %s", code, stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, "queued #1 (holders: full pid ") {
		t.Fatalf("waiter stderr: want a queued #1 line naming the full holder, got %q", got)
	}
}

func TestQueueGatesRunWaitsForTheSlot(t *testing.T) {
	root := t.TempDir()
	repo, home := filepath.Join(root, "repo"), filepath.Join(root, "home")
	for _, dir := range []string{repo, filepath.Join(home, ".code-forge", "runs")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// The test gate prints the held marker: it proves the gate ran inside a slot.
	config := `{"gates":{"test":["/bin/sh","-c","printf held=$CODE_FORGE_TEST_QUEUE_HELD"],"lint":null,"types":null,"format":null,"rerun_failed":0}}`
	if err := os.WriteFile(filepath.Join(repo, ".code-forge.yml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	record := map[string]any{"run_id": "fixture", "workspace": repo, "status": "active", "project": "fixture", "started_at": "2026-11-10T00:00:00.000Z", "blocks": map[string]any{"B1": map[string]any{"status": "open", "kind": "code", "owned_files": []string{}}}}
	data, _ := json.Marshal(record)
	if err := os.WriteFile(filepath.Join(home, ".code-forge", "runs", "fixture.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	holder := holdSlot(t, home, "land")
	gate := queueProc(t, home, repo, "gates", "run", "--run", "fixture", "--block", "B1")
	var stdout strings.Builder
	gate.Stdout = &stdout
	pipe, err := gate.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gate.Process.Kill() })
	lines := bufio.NewReader(pipe)
	first, err := lines.ReadString('\n')
	if err != nil || !strings.HasPrefix(first, "queued #1 (holders: full pid ") || !strings.Contains(first, "land") {
		t.Fatalf("gates run first stderr line = %q (%v), want queued #1 naming the land holder", first, err)
	}
	v := waitQueue(t, home, 1, 1)
	if w := v.Waiters[0]; w.Kind != "full" || w.Label != "gates fixture/B1" || w.PID != gate.Process.Pid {
		t.Fatalf("gate waiter row: want full 'gates fixture/B1' pid %d, got %+v", gate.Process.Pid, w)
	}
	status, err := queueProc(t, home, repo, "status", "--run", "fixture").CombinedOutput()
	if err != nil || !strings.Contains(string(status), "queued #1 (gates B1)") {
		t.Fatalf("status while the gate waits: want 'queued #1 (gates B1)': %v: %s", err, status)
	}
	if err := holder.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = holder.Wait()
	rest, _ := io.ReadAll(lines)
	if code := exitCode(t, gate.Wait()); code != 0 {
		t.Fatalf("gates run exit %d after the holder died, want 0: %s%s", code, first, rest)
	}
	if !strings.Contains(string(rest), "queue: admitted after ") {
		t.Fatalf("gates run stderr: want an admitted line, got %q", rest)
	}
	var result struct {
		AllOK   bool `json:"allOk"`
		Results []struct{ Gate, Stdout string }
	}
	if err := json.Unmarshal([]byte(stdout.String()), &result); err != nil {
		t.Fatalf("gate JSON: %v: %s", err, stdout.String())
	}
	if !result.AllOK || len(result.Results) != 4 || result.Results[0].Gate != "test" || result.Results[0].Stdout != "held=1" {
		t.Fatalf("want allOk with the test gate run inside the slot (held=1), got %s", stdout.String())
	}
	if v := readQueue(t, home); len(v.Holders) != 0 || len(v.Waiters) != 0 {
		t.Fatalf("after the gate: want an empty queue, have %+v", v)
	}
}
