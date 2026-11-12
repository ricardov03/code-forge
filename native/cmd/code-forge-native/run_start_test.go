package main

import (
	"bytes"
	"encoding/json"
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

	"github.com/ricardov03/code-forge/native/config"
)

// fakeNodeScript stands in for the worker run start launches
// (`<binary> worker --run <id>`, B511): it announces itself in the queue's
// worker registry the way the worker does, then keeps running.
// FAKE_WORKER_MODE=exit makes it die at once; FAKE_WORKER_RUN makes it
// announce another run. Node is never needed: CODE_FORGE_NODE names a missing
// file, so a hand-over fails the call with exit 127.
const fakeNodeScript = `#!/bin/sh
if [ "$1" != "worker" ] || [ "$2" != "--run" ]; then echo "fake worker: bad argv: $*" >&2; exit 127; fi
echo $$ >> "$FAKE_PIDS"
if [ "$FAKE_WORKER_MODE" = "exit" ]; then exit 3; fi
st=$(LC_ALL=C /bin/ps -o lstart= -p $$ | tr -s ' ' | sed 's/^ //;s/ $//')
now=$(date -u +%Y-%m-%dT%H:%M:%S.000Z)
run=$3
if [ -n "$FAKE_WORKER_RUN" ]; then run=$FAKE_WORKER_RUN; fi
mkdir -p .code-forge/workers
printf '{"pid":%s,"start_time":"%s","run":"%s","started_at":"%s","heartbeat_at":"%s"}' $$ "$st" "$run" "$now" "$now" > ".code-forge/workers/$$-$st.json"
exec sleep 30
`

type startFixture struct {
	runFixture
	repo, tmpRoot, pids string
}

// newStartFixture is a temp HOME, a temp git repository with a config whose
// tmp.root is a temp dir, a fake node on CODE_FORGE_NODE and an empty process
// table for the sweep. Every launched fake worker is killed at the end.
func newStartFixture(t *testing.T, config string) startFixture {
	t.Helper()
	f := startFixture{runFixture: newRunFixture(t), repo: t.TempDir(), tmpRoot: t.TempDir()}
	f.pids = filepath.Join(f.home, "fake-pids")
	if out, err := exec.Command("git", "-C", f.repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if config != "" {
		body := strings.ReplaceAll(config, "TMPROOT", f.tmpRoot)
		if err := os.WriteFile(filepath.Join(f.repo, ".code-forge.yml"), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	fake := filepath.Join(f.home, "fake-worker")
	if err := os.WriteFile(fake, []byte(fakeNodeScript), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODE_FORGE_NODE", filepath.Join(f.home, "missing-node"))
	t.Setenv("FAKE_PIDS", f.pids)
	t.Setenv("FAKE_WORKER_MODE", "")
	t.Setenv("FAKE_WORKER_RUN", "")
	t.Setenv("CODE_FORGE_TMP_ROOT", "")
	here, _ := os.Getwd()
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	beforeProcs, beforeTimeout, beforeBinary := runReadProcesses, runLaunchTimeout, runWorkerBinary
	runReadProcesses = func() []runProc { return []runProc{} }
	runLaunchTimeout = 5 * time.Second
	runWorkerBinary = func() (string, error) { return fake, nil }
	t.Cleanup(func() {
		runReadProcesses, runLaunchTimeout, runWorkerBinary = beforeProcs, beforeTimeout, beforeBinary
		_ = os.Chdir(here)
		data, _ := os.ReadFile(f.pids)
		for _, line := range strings.Fields(string(data)) {
			if pid, err := strconv.Atoi(line); err == nil && pid > 1 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	return f
}

func (f startFixture) launched(t *testing.T) []int {
	t.Helper()
	data, _ := os.ReadFile(f.pids)
	out := []int{}
	for _, line := range strings.Fields(string(data)) {
		pid, _ := strconv.Atoi(line)
		out = append(out, pid)
	}
	return out
}

// startRows reads the ledger of project slug `unit`, verified with the key
// run start generated.
func (f startFixture) startRows(t *testing.T, runID string) []map[string]any {
	t.Helper()
	key, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "runs", runID+".key"))
	if err != nil {
		t.Fatal(err)
	}
	f.key = key
	return f.rows(t)
}

func TestRunStartWithWorkerPid(t *testing.T) {
	f := newStartFixture(t, "project:\n  slug: unit\nengine: claude\ntmp:\n  root: TMPROOT\nreview:\n  external:\n    required: true\n    name: \"  Ana \"\n")
	sleeper, started := startSleeper(t)
	pid := sleeper.Process.Pid
	code, out, errs := runVerbCall(t, "start", "--cwd", f.repo, "--run", "r-new", "--mode", "brief", "--worker-pid", strconv.Itoa(pid))
	if code != 0 || errs != "" || out != fmt.Sprintf("run r-new started · engine claude · mode brief (no plan) · worker pid %d\n", pid) {
		t.Fatalf("start = %d %q %q", code, out, errs)
	}
	data, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "runs", "r-new.json"))
	if err != nil {
		t.Fatal(err)
	}
	keys := regexp.MustCompile(`(?m)^  "([a-z_]+)":`).FindAllStringSubmatch(string(data), -1)
	order := []string{}
	for _, k := range keys {
		order = append(order, k[1])
	}
	want := "version run_id project workspace engine provider levels fallback_mode started_at orchestrator_pid solo_pid worker workers status blocks orphans mode config"
	if strings.Join(order, " ") != want {
		t.Fatalf("record keys = %v", order)
	}
	record := f.read(t, "r-new")
	ws, _ := filepath.EvalSymlinks(f.repo)
	if record["workspace"] != ws || record["project"] != "unit" || record["engine"] != "claude" || record["fallback_mode"] != "rules" || record["provider"] != nil || record["orchestrator_pid"] != float64(os.Getpid()) {
		t.Fatalf("record: %#v", record)
	}
	if worker := record["worker"].(map[string]any); worker["pid"] != float64(pid) || worker["started_at"] != started {
		t.Fatalf("worker: %#v", worker)
	}
	cfg := record["config"].(map[string]any)
	snaps := cfg["snapshots"].(map[string]any)
	if _, ok := snaps[cfg["hash"].(string)]; !ok || len(snaps) != 1 {
		t.Fatalf("config: %#v", cfg)
	}
	if info, err := os.Stat(filepath.Join(f.home, ".code-forge", "runs", "r-new.key")); err != nil || info.Mode().Perm() != 0600 || info.Size() != 32 {
		t.Fatalf("key: %v %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(f.repo, ".code-forge", "runs", "r-new.json")); err != nil {
		t.Fatalf("mirror: %v", err)
	}
	rows := f.startRows(t, "r-new")
	if len(rows) != 3 || rows[0]["event"] != "run.start" || rows[0]["worker_pinned"] != true || rows[1]["event"] != "run.mode" || rows[1]["plan"] != "none" || rows[2]["event"] != "review.external" || rows[2]["reviewer"] != "Ana" {
		t.Fatalf("rows: %#v", rows)
	}
	if len(f.launched(t)) != 0 {
		t.Fatal("a pinned start launched a worker")
	}

	code, _, errs = runVerbCall(t, "start", "--cwd", f.repo, "--run", "r-new", "--worker-pid", strconv.Itoa(pid))
	if code != 1 || errs != "run start: run r-new already exists\n" {
		t.Fatalf("again = %d %q", code, errs)
	}
}

func TestRunStartLaunchesAndPinsTheWorker(t *testing.T) {
	f := newStartFixture(t, "project:\n  slug: unit\ntmp:\n  root: TMPROOT\n")
	code, out, errs := runVerbCall(t, "start", "--cwd", f.repo, "--engine", "codex")
	launched := f.launched(t)
	if len(launched) != 1 {
		t.Fatalf("launched = %v (%d %q %q)", launched, code, out, errs)
	}
	m := regexp.MustCompile(`^run (r-\d{8}-\d{6}-[0-9a-f]{6}) started · engine codex · worker pid (\d+)\n$`).FindStringSubmatch(out)
	if code != 0 || errs != "" || m == nil || m[2] != strconv.Itoa(launched[0]) {
		t.Fatalf("start = %d %q %q", code, out, errs)
	}
	runID := m[1]
	record := f.read(t, runID)
	workers := record["workers"].([]any)
	if len(workers) != 1 || workers[0].(map[string]any)["pid"] != float64(launched[0]) || record["worker_history"] == nil {
		t.Fatalf("record: %#v", record)
	}
	rows := f.startRows(t, runID)
	if len(rows) != 2 || rows[0]["event"] != "run.start" || rows[0]["worker_pinned"] != false || rows[1]["event"] != "worker.reattach" || rows[1]["pid"] != float64(launched[0]) {
		t.Fatalf("rows: %#v", rows)
	}
	if syscall.Kill(launched[0], 0) != nil {
		t.Fatal("the pinned worker is not running")
	}

	// --add-worker: the limit counts the live registered worker
	code, out, errs = runVerbCall(t, "start", "--add-worker", "--run", runID)
	if code != 1 || out != "" || errs != "run start: review.workers limit 1 reached\n" {
		t.Fatalf("add over limit = %d %q %q", code, out, errs)
	}
	f.rewriteSnapshot(t, runID, "project:\n  slug: unit\nreview:\n  workers: 2\n")
	code, out, errs = runVerbCall(t, "start", "--add-worker", "--run", runID)
	launched = f.launched(t)
	if code != 0 || errs != "" || len(launched) != 2 || out != fmt.Sprintf("run %s: added worker pid %d (review.workers limit 2)\n", runID, launched[1]) {
		t.Fatalf("add = %d %q %q %v", code, out, errs, launched)
	}
	record = f.read(t, runID)
	if workers := record["workers"].([]any); len(workers) != 2 || workers[1].(map[string]any)["pid"] != float64(launched[1]) {
		t.Fatalf("record after add: %#v", record)
	}
	if rows := f.startRows(t, runID); len(rows) != 3 || rows[2]["event"] != "worker.add" {
		t.Fatalf("rows after add: %#v", rows)
	}
}

// rewriteSnapshot replaces the run's config snapshot in force by `config`
// (as `run reload` would have stored it).
func (f startFixture) rewriteSnapshot(t *testing.T, runID, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".code-forge.yml")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	snap, err := config.LoadSnapshotFile(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := config.Hash(runDeepMap(snap.Ordered))
	file := filepath.Join(f.home, ".code-forge", "runs", runID+".json")
	data, _ := os.ReadFile(file)
	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	record["config"] = map[string]any{"hash": hash, "snapshots": map[string]any{hash: runDeepMap(snap.Ordered)}, "pins": map[string]any{}}
	out, _ := json.MarshalIndent(record, "", "  ")
	if err := os.WriteFile(file, out, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRunStartLaunchFailures(t *testing.T) {
	t.Run("the worker exits", func(t *testing.T) {
		f := newStartFixture(t, "project:\n  slug: unit\ntmp:\n  root: TMPROOT\n")
		t.Setenv("FAKE_WORKER_MODE", "exit")
		code, out, errs := runVerbCall(t, "start", "--cwd", f.repo, "--run", "r-new")
		if code != 1 || out != "" || errs != "run start: the worker did not start (run `code-forge worker --run <id>` by hand to see why)\n" {
			t.Fatalf("exit = %d %q %q", code, out, errs)
		}
		if f.read(t, "r-new")["status"] != "active" {
			t.Fatal("no record")
		}
	})
	t.Run("the worker serves another run", func(t *testing.T) {
		f := newStartFixture(t, "project:\n  slug: unit\ntmp:\n  root: TMPROOT\n")
		t.Setenv("FAKE_WORKER_RUN", "r-other")
		code, out, errs := runVerbCall(t, "start", "--cwd", f.repo, "--run", "r-new")
		launched := f.launched(t)
		want := fmt.Sprintf("run start: launched worker pid %d was not pinned; stopped it\nrun start: the live worker serves run r-other\n", launched[0])
		if code != 1 || out != "" || errs != want {
			t.Fatalf("other run = %d %q %q", code, out, errs)
		}
		deadline := time.Now().Add(3 * time.Second)
		for syscall.Kill(launched[0], 0) == nil && time.Now().Before(deadline) {
			var status syscall.WaitStatus
			_, _ = syscall.Wait4(launched[0], &status, syscall.WNOHANG, nil)
			time.Sleep(20 * time.Millisecond)
		}
		if syscall.Kill(launched[0], 0) == nil {
			t.Fatal("the launched worker was not stopped")
		}
	})
	t.Run("not a git repository", func(t *testing.T) {
		f := newStartFixture(t, "")
		plain := t.TempDir()
		if err := os.WriteFile(filepath.Join(plain, ".code-forge.yml"), []byte("project:\n  slug: unit\ntmp:\n  root: "+f.tmpRoot+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		code, out, errs := runVerbCall(t, "start", "--cwd", plain, "--run", "r-new")
		if code != 1 || out != "" || errs != "run start: not inside a git repository\n" {
			t.Fatalf("no repo = %d %q %q", code, out, errs)
		}
	})
}

func TestRunStartRefusals(t *testing.T) {
	f := newStartFixture(t, "")
	write := func(body string) string {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".code-forge.yml"), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	empty := t.TempDir()
	for _, tc := range []struct {
		name string
		args []string
		code int
		errs string
	}{
		{"no config", []string{"--cwd", empty}, 2, "run start: no config file at " + filepath.Join(empty, ".code-forge.yml") + "\n"},
		{"relative tmp.root", []string{"--cwd", write("project:\n  slug: unit\ntmp:\n  root: rel/dir\n")}, 1, "run start: tmp: tmp.root must be an absolute path\n"},
		{"bad run id", []string{"--cwd", write("project:\n  slug: unit\ntmp:\n  root: " + f.tmpRoot + "\n"), "--run", "Bad"}, 1, "run start: run id must match /^[a-z0-9][a-z0-9-]{0,63}$/\n"},
		{"bad slug", []string{"--cwd", write("project:\n  slug: Bad_Slug\ntmp:\n  root: " + f.tmpRoot + "\n")}, 1, "run start: project slug must match /^[a-z0-9][a-z0-9-]*$/ (config key project.slug)\n"},
		{"external without name", []string{"--cwd", write("project:\n  slug: unit\ntmp:\n  root: " + f.tmpRoot + "\nreview:\n  external:\n    required: true\n    name: \"  \"\n")}, 1, "run start: review.external.required is true but review.external.name is missing or blank\n"},
		{"bad worker pid", []string{"--cwd", write("project:\n  slug: unit\ntmp:\n  root: " + f.tmpRoot + "\n"), "--worker-pid", "1"}, 1, "run start: worker pid must be an integer > 1\n"},
	} {
		code, out, errs := runVerbCall(t, append([]string{"start"}, tc.args...)...)
		if code != tc.code || out != "" || errs != tc.errs {
			t.Fatalf("%s = %d %q %q", tc.name, code, out, errs)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(f.home, ".code-forge", "runs"))
	if len(entries) != 0 || len(f.launched(t)) != 0 {
		t.Fatalf("a refusal wrote a run or launched a worker: %v", entries)
	}
}

func TestRunStartSweep(t *testing.T) {
	t.Run("removes a dead root", func(t *testing.T) {
		f := newStartFixture(t, "project:\n  slug: unit\ntmp:\n  root: TMPROOT\n")
		gone := exec.Command("true")
		if err := gone.Run(); err != nil {
			t.Fatal(err)
		}
		dead := filepath.Join(f.tmpRoot, "b1-dead")
		live := filepath.Join(f.tmpRoot, "b2-live")
		for dir, pid := range map[string]int{dead: gone.Process.Pid, live: os.Getpid()} {
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "owner.json"), []byte(fmt.Sprintf(`{"pid":%d,"start_time":"unknown"}`, pid)), 0600); err != nil {
				t.Fatal(err)
			}
		}
		sleeper, _ := startSleeper(t)
		code, _, errs := runVerbCall(t, "start", "--cwd", f.repo, "--run", "r-new", "--worker-pid", strconv.Itoa(sleeper.Process.Pid))
		if code != 0 {
			t.Fatalf("start = %d %q", code, errs)
		}
		if _, err := os.Stat(dead); !os.IsNotExist(err) {
			t.Fatalf("dead root kept: %v", err)
		}
		if _, err := os.Stat(live); err != nil {
			t.Fatalf("live root removed: %v", err)
		}
	})
	t.Run("an unreadable temp base hands over", func(t *testing.T) {
		f := newStartFixture(t, "project:\n  slug: unit\ntmp:\n  root: TMPROOT\n")
		t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
		t.Setenv("CODE_FORGE_NATIVE", "1")
		t.Setenv("CODE_FORGE_NODE", filepath.Join(f.home, "missing-node"))
		// Node's readdirSync throws on it: its error, its message
		if err := os.Chmod(f.tmpRoot, 0o300); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(f.tmpRoot, 0o700) })
		sleeper, _ := startSleeper(t)
		code, out, _ := runVerbCall(t, "start", "--cwd", f.repo, "--run", "r-new", "--worker-pid", strconv.Itoa(sleeper.Process.Pid))
		if code != 127 || out != "" {
			t.Fatalf("sweep = %d %q, want the hand-over", code, out)
		}
		data, _ := os.ReadFile(filepath.Join(f.home, ".code-forge", "native", "handovers.jsonl"))
		if !bytes.Contains(data, []byte(`"reason":"run-sweep"`)) {
			t.Fatalf("handover log: %s", data)
		}
		if _, err := os.Stat(filepath.Join(f.home, ".code-forge", "runs", "r-new.json")); !os.IsNotExist(err) {
			t.Fatal("the hand-over wrote a run")
		}
	})
}

func TestRunAddWorkerRefusals(t *testing.T) {
	f := newStartFixture(t, "project:\n  slug: unit\n")
	f.workspace = f.repo
	f.record(t, "r-done", "ended", `,"blocks":{}`)
	f.record(t, "r-plain", "active", `,"blocks":{}`)
	f.record(t, "r-bad", "active", `,"blocks":{},"config":{"hash":"abc","snapshots":{"abc":{"x":1}},"pins":{}}`)
	for _, tc := range []struct {
		run, errs string
	}{
		{"r-none", "run start: no run record for r-none\n"},
		{"r-done", "run start: run r-done has ended\n"},
		{"r-bad", "run start: run r-bad: the config snapshot in force does not match its hash\n"},
	} {
		code, out, errs := runVerbCall(t, "start", "--add-worker", "--run", tc.run)
		if code != 1 || out != "" || errs != tc.errs {
			t.Fatalf("%s = %d %q %q", tc.run, code, out, errs)
		}
	}
	// a record without a snapshot reads the workspace config (review.workers 1)
	code, out, errs := runVerbCall(t, "start", "--add-worker", "--run", "r-plain")
	launched := f.launched(t)
	if code != 0 || errs != "" || len(launched) != 1 || out != fmt.Sprintf("run r-plain: added worker pid %d (review.workers limit 1)\n", launched[0]) {
		t.Fatalf("plain add = %d %q %q", code, out, errs)
	}
	ws := f.workspace
	f.workspace = t.TempDir()
	f.record(t, "r-norepo", "active", `,"blocks":{}`)
	f.workspace = ws
	code, out, errs = runVerbCall(t, "start", "--add-worker", "--run", "r-norepo")
	if code != 1 || out != "" || errs != "run start: not inside a git repository\n" {
		t.Fatalf("no repo = %d %q %q", code, out, errs)
	}
	if rows := f.rows(t); len(rows) != 1 || rows[0]["event"] != "worker.add" {
		t.Fatalf("rows: %#v", rows)
	}
}

func TestRunReloadRefusals(t *testing.T) {
	f := newRunFixture(t)
	f.record(t, "r-done", "ended", `,"blocks":{}`)
	f.record(t, "r-test", "active", `,"blocks":{}`)
	file := filepath.Join(f.workspace, ".code-forge.yml")
	for _, tc := range []struct {
		name, run, config, errs string
	}{
		{"no record", "r-none", "", "run reload: no run record for r-none\n"},
		{"ended", "r-done", "", "run reload: run r-done has ended\n"},
		{"no config", "r-test", "", "run reload: no .code-forge.yml in the workspace — nothing changed\n"},
		{"not a mapping", "r-test", "- a\n- b\n", "run reload: the config could not be parsed (.code-forge.yml) — nothing changed\n"},
		{"bad version", "r-test", "version: 99\n", "run reload: .code-forge.yml could not be migrated to the current format — nothing changed\n"},
	} {
		_ = os.Remove(file)
		if tc.config != "" {
			if err := os.WriteFile(file, []byte(tc.config), 0644); err != nil {
				t.Fatal(err)
			}
		}
		code, out, errs := runVerbCall(t, "reload", "--run", tc.run)
		if code != 1 || out != "" || errs != tc.errs {
			t.Fatalf("%s = %d %q %q", tc.name, code, out, errs)
		}
	}
	_ = os.Remove(file)
	if err := os.Mkdir(file, 0755); err != nil {
		t.Fatal(err)
	}
	code, _, errs := runVerbCall(t, "reload", "--run", "r-test")
	if code != 1 || errs != "run reload: .code-forge.yml could not be read (EISDIR) — nothing changed\n" {
		t.Fatalf("directory = %d %q", code, errs)
	}
	if len(f.rows(t)) != 0 {
		t.Fatal("a refusal wrote a row")
	}
}
