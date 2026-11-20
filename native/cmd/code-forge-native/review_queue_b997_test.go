package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// B997: one repository has one live worker. A second `code-forge review`
// started while another review runs waits for that review's run to end and
// then starts its own; a worker of any other run still refuses at once.

// b997SlowClaude approves after a pause, so the first review still holds the
// worker when the second one starts.
const b997SlowClaude = `#!/bin/sh
in=$(cat)
sleep 3
heads=$(printf '%s\n' "$in" | grep -E '^@@ -[0-9]+(,[0-9]+)? \+[0-9]+(,[0-9]+)? @@' | sed -E 's/^(@@ -[0-9,]+ \+[0-9,]+ @@).*/\1/' | awk '!s[$0]++' | sed 's/.*/"&"/' | paste -sd, -)
printf '%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"passed":true,"summary":"fake review","reviewed_hunks":['"$heads"'],"findings":[]},"usage":{"input_tokens":1000,"output_tokens":300}}'
`

type b997Out struct {
	code     int
	out, err string
	took     time.Duration
}

func b997Exec(t *testing.T, f b553Fix, env []string, argv ...string) b997Out {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = f.repo
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	r := b997Out{}
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Error(err)
			return r
		}
		r.code = exit.ExitCode()
	}
	r.took, r.out, r.err = time.Since(start), stdout.String(), stderr.String()
	return r
}

func b997Setup(t *testing.T) (string, b553Fix, []string) {
	t.Helper()
	bin := b553Binary(t)
	f := b553Fixture(t, "approve")
	if err := os.WriteFile(filepath.Join(f.bin, "claude"), []byte(b997SlowClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	env := b553Env(f, "CODE_FORGE_HANDOVER_LOG=1", "CODE_FORGE_NODE="+filepath.Join(f.dir, "missing-node"))
	t.Cleanup(func() { b553Reap(t, f) })
	return bin, f, env
}

const b997Wait = "is this repository's one live worker"

func TestB997ParallelReviewsQueue(t *testing.T) {
	bin, f, env := b997Setup(t)
	var wg sync.WaitGroup
	outs := make([]b997Out, 2)
	for i, file := range []string{"src/a.txt", "src/b.txt"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i == 1 {
				time.Sleep(time.Second)
			}
			outs[i] = b997Exec(t, f, env, bin, "review", "--files", file, "--max", "120")
		}()
	}
	wg.Wait()
	waited := 0
	for i, r := range outs {
		if r.code != 0 || !strings.Contains(r.out, "approved") {
			t.Errorf("review %d: exit %d\nstdout %q\nstderr %q", i, r.code, r.out, r.err)
		}
		if strings.Contains(r.err, b997Wait) {
			waited++
			if strings.Count(r.err, b997Wait) != 1 || !b553RunRe.MatchString(r.err) {
				t.Errorf("review %d: wait note not one line naming the run: %q", i, r.err)
			}
		}
	}
	if waited != 1 {
		t.Errorf("%d reviews waited, want 1:\n%q\n%q", waited, outs[0].err, outs[1].err)
	}
	if h := b460Handovers(t, f.home); len(h) != 0 {
		t.Errorf("hand-overs: %v", h)
	}
}

// b997StartRun starts a run whose worker stays live, as a lane's run does.
func b997StartRun(t *testing.T, bin string, f b553Fix, env []string, run string) {
	t.Helper()
	r := b997Exec(t, f, env, bin, "run", "start", "--cwd", f.repo, "--run", run)
	if r.code != 0 {
		t.Fatalf("run start %s: exit %d %q", run, r.code, r.err)
	}
}

func TestB997LaneRunWorkerRefusesAtOnce(t *testing.T) {
	bin, f, env := b997Setup(t)
	b997StartRun(t, bin, f, env, "lane-b997")
	r := b997Exec(t, f, env, bin, "review", "--files", "src/a.txt", "--max", "60")
	if r.code != 1 || !strings.Contains(r.err, "run lane-b997 still has a live worker") || strings.Contains(r.err, b997Wait) {
		t.Fatalf("exit %d, stderr %q", r.code, r.err)
	}
	if r.took > 20*time.Second {
		t.Errorf("refusal took %v: it waited", r.took)
	}
}

func TestB997QueueWaitEndsAtDeadline(t *testing.T) {
	bin, f, env := b997Setup(t)
	other := "r-20260101-000000-b99700"
	b997StartRun(t, bin, f, env, other)
	r := b997Exec(t, f, env, bin, "review", "--files", "src/a.txt", "--max", "3")
	if r.code != 1 || !strings.Contains(r.err, b997Wait) || !strings.Contains(r.err, "run "+other+" still has a live worker") {
		t.Fatalf("exit %d, stderr %q", r.code, r.err)
	}
	if r.took < 3*time.Second || r.took > 30*time.Second {
		t.Errorf("took %v, want the 3 s deadline", r.took)
	}
}
