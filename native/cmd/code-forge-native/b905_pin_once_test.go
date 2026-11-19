package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/procguard"
)

// B905, the customer's case: after the run's worker died, `run start
// --add-worker` launched a worker that re-pinned the run to itself at boot,
// and the add pinned the same pid again, so the run's workers list held the
// new worker twice. The helpers are shared with the B826 tests.

// b826Fixture is a temp repo + temp HOME with the built binary as
// code-forge, a fake claude that approves, a started run and block B1 that
// owns a.txt..d.txt (changed, not committed).
func b826Fixture(t *testing.T, cfg string) (b884Fix, string) {
	t.Helper()
	binary := b553Binary(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := b884Fix{dir: dir, home: filepath.Join(dir, "home"), repo: filepath.Join(dir, "repo"), bin: filepath.Join(dir, "bin"), tmp: filepath.Join(dir, "tmp")}
	for _, d := range []string{f.home, f.repo, f.bin, f.tmp} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.bin, "claude"), []byte(b884Claude), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.bin, "code-forge"), []byte("#!/bin/sh\nexec '"+binary+"' \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.env = []string{"PATH=" + f.bin + ":/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + f.home, "TMPDIR=" + f.tmp, "CODE_FORGE_TMP_ROOT=" + f.tmp,
		"CODE_FORGE_KEY_BACKEND=file", "CODE_FORGE_KEY_JEV=", "CODE_FORGE_NODE=" + filepath.Join(dir, "missing-node"), "CODE_FORGE_HANDOVER_LOG=1",
		"GIT_CONFIG_GLOBAL=/dev/null", procguard.Env + "=" + os.Getenv(procguard.Env)}
	run := ""
	t.Cleanup(func() {
		_ = os.Chmod(filepath.Join(f.repo, ".code-forge", "queue"), 0o700)
		if run == "" {
			return
		}
		for _, pid := range b884Workers(run) {
			_ = syscall.Kill(pid, syscall.SIGCONT)
			_ = syscall.Kill(pid, syscall.SIGTERM)
		}
		deadline := time.Now().Add(30 * time.Second)
		for len(b884Workers(run)) > 0 && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		for _, pid := range b884Workers(run) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Errorf("worker %d left", pid)
		}
	})
	gitc := "git -c user.name=t -c user.email=t@t -c commit.gpgsign=false"
	f.must(t, "git init -q -b main && for x in a b c d; do echo one > $x.txt; done && "+gitc+" add -A && "+gitc+" commit -q -m base")
	if err := os.WriteFile(filepath.Join(f.repo, ".code-forge.yml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	f.must(t, gitc+" add -A && "+gitc+" commit -q -m cfg")
	out := f.must(t, "code-forge run start 2>&1")
	m := regexp.MustCompile(`run (\S+) started`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("run start: %s", out)
	}
	run = m[1]
	if err := os.WriteFile(filepath.Join(dir, "acc.json"), []byte(`[{"clause":"a","tests":["t1"]}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	f.must(t, "code-forge block open B1 --run "+run+" --level L1 --owned a.txt --owned b.txt --owned c.txt --owned d.txt --acceptance "+filepath.Join(dir, "acc.json"))
	f.must(t, "for x in a b c d; do echo two >> $x.txt; done")
	return f, run
}

// b826KillWorker is the customer's state: kill -9 the run's worker, then
// delete queue/worker.json and the workers/ registry entry.
func b826KillWorker(t *testing.T, f b884Fix, run string) {
	t.Helper()
	for _, pid := range b884Workers(run) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	for deadline := time.Now().Add(30 * time.Second); len(b884Workers(run)) > 0; {
		if time.Now().After(deadline) {
			t.Fatal("the worker did not die")
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = os.Remove(filepath.Join(f.repo, ".code-forge", "queue", "worker.json"))
	entries, _ := filepath.Glob(filepath.Join(f.repo, ".code-forge", "workers", "*.json"))
	for _, e := range entries {
		if err := os.Remove(e); err != nil {
			t.Fatal(err)
		}
	}
}

// b826Pins is the pids of the run record's workers list.
func b826Pins(t *testing.T, f b884Fix, run string) []float64 {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "runs", run+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Workers []struct {
			Pid float64 `json:"pid"`
		} `json:"workers"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}
	pids := []float64{}
	for _, w := range rec.Workers {
		pids = append(pids, w.Pid)
	}
	return pids
}

// b826OneLiveWorker checks one live worker of the run, announced in
// worker.json, pinned exactly once (B905), and returns its pid.
func b826OneLiveWorker(t *testing.T, f b884Fix, run, stage string) int {
	t.Helper()
	live := b884Workers(run)
	if len(live) != 1 {
		t.Fatalf("%s: live workers %v", stage, live)
	}
	if _, err := os.Stat(filepath.Join(f.repo, ".code-forge", "queue", "worker.json")); err != nil {
		t.Fatalf("%s: worker.json: %v", stage, err)
	}
	count := 0
	for _, p := range b826Pins(t, f, run) {
		if p == float64(live[0]) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("%s: pid %d pinned %d times: %v", stage, live[0], count, b826Pins(t, f, run))
	}
	return live[0]
}

// B905: --add-worker after the run's worker died pins the new worker once.
func TestB905AddWorkerAfterDeadWorkerPinsOnce(t *testing.T) {
	f, run := b826Fixture(t, b884Config)
	b826KillWorker(t, f, run)
	f.must(t, "bash -c 'code-forge run start --add-worker --run "+run+"'")
	pid := b826OneLiveWorker(t, f, run, "after --add-worker")
	if pins := b826Pins(t, f, run); len(pins) != 1 || pins[0] != float64(pid) {
		t.Fatalf("pins %v, live %d", pins, pid)
	}
}
