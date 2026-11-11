package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/gitx"
)

// lockWaitFixture is a repo with one block whose test gate appends a line to
// $HOME/gate-calls, and a HOME holding its run record.
type lockWaitFixture struct {
	repo, home, run string
}

func newLockWaitFixture(t *testing.T, worktree bool) lockWaitFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := lockWaitFixture{repo: filepath.Join(root, "repo"), home: filepath.Join(root, "home"), run: "fixture"}
	write := func(file, body string, mode os.FileMode) {
		t.Helper()
		p := filepath.Join(f.repo, file)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(".code-forge.yml", `{"gates":{"test":["/bin/sh","-c","echo call >> \"$HOME/gate-calls\""],"lint":null,"types":null,"format":null,"rerun_failed":0}}`, 0600)
	write("engine.go", "package engine\n", 0600)
	if worktree {
		// block rebase only runs the Node child itself in a code-forge checkout.
		write("native/go.mod", "module fixture\n", 0600)
		write("scripts/build-native.mjs", "// fixture\n", 0600)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = f.repo
		cmd.Env = gitx.ChildEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("add", ".")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "base")
	block := map[string]any{"status": "open", "kind": "code", "owned_files": []string{"engine.go"}, "test_files": []string{}, "base_branch": "main"}
	if worktree {
		block["worktree"] = map[string]any{"path": f.repo, "branch": "main"}
	}
	dir := filepath.Join(f.home, ".code-forge", "runs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"run_id": f.run, "workspace": f.repo, "status": "active", "project": "fixture", "blocks": map[string]any{"B1": block}})
	if err := os.WriteFile(filepath.Join(dir, f.run+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return f
}

// command runs the router in a fresh process (TestGatesProcess) in the repo.
func (f lockWaitFixture) command(t *testing.T, extra []string, args ...string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(args)
	cmd := exec.Command(exe, "-test.run=^TestGatesProcess$")
	cmd.Dir = f.repo
	cmd.Env = append(append(envWithoutGit(), "HOME="+f.home, "CODE_FORGE_NO_ERROR_LOG=1", "CODE_FORGE_NODE=/nonexistent-code-forge-node", "CF_GATES_PROCESS=1", "CF_GATES_NODE_VERSION=v24.22.1", "CF_GATES_ARGS="+string(encoded)), extra...)
	return cmd
}

// hold starts another code-forge process that takes the run lock and keeps it
// until release is called. It returns that process's pid once the lock is held.
func (f lockWaitFixture) hold(t *testing.T) (pid int, release func()) {
	t.Helper()
	cmd := f.command(t, nil, "internal", "state-golden", "--home", f.home, "--run", f.run, "--op", "lock")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	released := false
	release = func() {
		if released {
			return
		}
		released = true
		_ = stdin.Close()
		_ = cmd.Wait()
	}
	t.Cleanup(release)
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatalf("lock holder did not take the lock: %q %v", line, err)
	}
	go func() { _, _ = io.Copy(io.Discard, stdout) }()
	return cmd.Process.Pid, release
}

func (f lockWaitFixture) count(t *testing.T, file string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.home, file))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "\n")
}

func waitingLines(stderr string) []string {
	lines := []string{}
	for _, line := range strings.Split(stderr, "\n") {
		if strings.Contains(line, "waiting for run lock") {
			lines = append(lines, line)
		}
	}
	return lines
}

func runSplit(t *testing.T, cmd *exec.Cmd) (code int, stdout, stderr string, elapsed time.Duration) {
	t.Helper()
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	started := time.Now()
	err := cmd.Run()
	elapsed = time.Since(started)
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return code, out.String(), errOut.String(), elapsed
}

func TestGatesRunWaitsForRunLock(t *testing.T) {
	f := newLockWaitFixture(t, false)
	pid, release := f.hold(t)
	timer := time.AfterFunc(2*time.Second, release)
	defer timer.Stop()
	code, stdout, stderr, elapsed := runSplit(t, f.command(t, nil, "gates", "run", "--run", f.run, "--block", "B1"))
	if code != 0 {
		t.Fatalf("gates run exit=%d, want 0 after the lock is released\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	want := fmt.Sprintf("waiting for run lock held by pid %d (internal state-golden)", pid)
	if lines := waitingLines(stderr); len(lines) != 1 || !strings.HasSuffix(lines[0], want) {
		t.Errorf("waiting lines=%q, want exactly 1 ending %q", lines, want)
	}
	if elapsed < 2*time.Second {
		t.Errorf("gates run ended after %s, before the lock was released", elapsed)
	}
	if calls := f.count(t, "gate-calls"); calls != 1 {
		t.Errorf("gate test command ran %d times, want 1", calls)
	}
}

func TestGatesRunLockWaitEnds(t *testing.T) {
	f := newLockWaitFixture(t, false)
	f.hold(t)
	code, stdout, stderr, elapsed := runSplit(t, f.command(t, nil, "gates", "run", "--run", f.run, "--block", "B1", "--lock-wait", "1s"))
	locked := regexp.MustCompile(`(?m)^gates run: run fixture is locked \(.+\.lock\); remove it if no forge verb is running$`)
	if code != 1 || !locked.MatchString(stderr) {
		t.Errorf("exit=%d stderr=%q, want exit 1 and the locked message\nstdout: %s", code, stderr, stdout)
	}
	if elapsed < time.Second {
		t.Errorf("gates run gave up after %s, before --lock-wait 1s", elapsed)
	}
	if calls := f.count(t, "gate-calls"); calls != 0 {
		t.Errorf("gate test command ran %d times, want 0", calls)
	}
}

func TestBlockRebaseWaitsForRunLock(t *testing.T) {
	for _, shape := range []string{"timeout", "released"} {
		t.Run(shape, func(t *testing.T) {
			f := newLockWaitFixture(t, true)
			node := filepath.Join(f.home, "fake-node")
			// The fake Node logs its argv, one call per line.
			if err := os.WriteFile(node, []byte("#!/bin/sh\nprintf '%s ' \"$@\" >> \"$HOME/node-calls\"\necho >> \"$HOME/node-calls\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			pid, release := f.hold(t)
			args := []string{"block", "rebase", "B1", "--run", f.run, "--lock-wait", "1s"}
			if shape == "released" {
				args[len(args)-1] = "30s"
				timer := time.AfterFunc(2*time.Second, release)
				defer timer.Stop()
			}
			code, stdout, stderr, elapsed := runSplit(t, f.command(t, []string{"CODE_FORGE_NODE=" + node}, args...))
			calls := f.count(t, "node-calls")
			if shape == "timeout" {
				locked := regexp.MustCompile(`(?m)^block rebase: run fixture is locked \(.+\.lock\); remove it if no forge verb is running$`)
				if code != 1 || !locked.MatchString(stderr) {
					t.Errorf("exit=%d stderr=%q, want exit 1 and the locked message\nstdout: %s", code, stderr, stdout)
				}
				if elapsed < time.Second {
					t.Errorf("block rebase gave up after %s, before --lock-wait 1s", elapsed)
				}
				if calls != 0 {
					t.Errorf("Node rebase child started %d times, want 0", calls)
				}
				return
			}
			if code != 0 {
				t.Fatalf("block rebase exit=%d, want 0\nstdout: %s\nstderr: %s", code, stdout, stderr)
			}
			want := fmt.Sprintf("waiting for run lock held by pid %d (internal state-golden)", pid)
			if lines := waitingLines(stderr); len(lines) != 1 || !strings.HasSuffix(lines[0], want) {
				t.Errorf("waiting lines=%q, want exactly 1 ending %q", lines, want)
			}
			data, _ := os.ReadFile(filepath.Join(f.home, "node-calls"))
			if calls != 1 || strings.Contains(string(data), "--lock-wait") || !strings.Contains(string(data), "block rebase B1 --run fixture ") {
				t.Errorf("Node rebase child calls=%d argv=%q, want exactly 1 without --lock-wait", calls, data)
			}
		})
	}
}
