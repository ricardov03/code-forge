package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// b593FakeWorker records its pid, then leaves only a registry entry Go
// cannot judge (a lone surrogate escape) and stays up: the strict launch
// always meets the undecided registry after the pid is on file.
const b593FakeWorker = `#!/bin/sh
echo $$ >> "$B524_PIDS"
mkdir -p .code-forge/workers
printf '%s' '{"x":"\ud800"}' > .code-forge/workers/bad.json
exec sleep 30
`

// B593: a strict launch that meets a worker registry Go cannot judge kills
// its child and returns only once the child is reaped, so the doctor's
// hand-over never leaves a live (or zombie) worker pid behind, whatever
// the machine load.
func TestB593StrictUndecidedLaunchReapsChild(t *testing.T) {
	_, repo := smallProject(t, "provider: anthropic\n")
	base := t.TempDir()
	fake := filepath.Join(base, "fake-worker")
	if err := os.WriteFile(fake, []byte(b593FakeWorker), 0o755); err != nil {
		t.Fatal(err)
	}
	e := &b525Env{pids: filepath.Join(base, "fake-pids")}
	t.Setenv("B524_PIDS", e.pids)
	beforeTimeout := runLaunchTimeout
	runLaunchTimeout = 10 * time.Second
	t.Cleanup(func() {
		runLaunchTimeout = beforeTimeout
		for _, pid := range e.launched() {
			if pid > 1 {
				_ = syscall.Kill(pid, syscall.SIGKILL) // only the fake worker this test started
			}
		}
	})
	for i := 0; i < 5; i++ {
		_ = os.Remove(e.pids)
		_ = os.RemoveAll(filepath.Join(repo, ".code-forge", "workers"))
		pid, _, err := runLaunchWorkerJudged("b593-probe", repo, fake, nil, true)
		if !errors.Is(err, errRunLaunchUndecided) || pid != 0 {
			t.Fatalf("round %d: want errRunLaunchUndecided and pid 0, got pid %d, %v", i, pid, err)
		}
		launched := e.launched()
		if len(launched) != 1 {
			t.Fatalf("round %d: want one fake worker started, got %v", i, launched)
		}
		// No wait here: the launch itself must have reaped the child.
		if err := syscall.Kill(launched[0], 0); !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("round %d: worker pid %d still answers signal 0 after the launch returned (%v)", i, launched[0], err)
		}
	}
}
