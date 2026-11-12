package main

import (
	"errors"
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
	"github.com/ricardov03/code-forge/native/sign"
)

// b524FakeWorker stands in for `<binary> worker --run <id>`: it records its
// PATH and pid, then announces itself in the queue's worker registry the way
// the worker does and keeps running. B524_MODE=exit dies at once (never
// announces); B524_MODE=ignoreterm ignores SIGTERM.
const b524FakeWorker = `#!/bin/sh
if [ "$1" != "worker" ] || [ "$2" != "--run" ]; then exit 127; fi
echo "$PATH" > "$B524_PATH_FILE"
echo $$ >> "$B524_PIDS"
PATH=/usr/bin:/bin
if [ "$B524_MODE" = "exit" ]; then exit 3; fi
if [ "$B524_MODE" = "ignoreterm" ]; then trap '' TERM; fi
st=$(LC_ALL=C /bin/ps -o lstart= -p $$ | tr -s ' ' | sed 's/^ //;s/ $//')
now=$(date -u +%Y-%m-%dT%H:%M:%S.000Z)
mkdir -p .code-forge/workers
printf '{"pid":%s,"start_time":"%s","run":"%s","started_at":"%s","heartbeat_at":"%s"}' $$ "$st" "$3" "$now" "$now" > ".code-forge/workers/$$-$st.json"
exec sleep 30
`

type b524Fixture struct {
	dir, home, tmpRoot, runRoot, workDir, pids, pathFile string
	ctx                                                  dwpCtx
}

// newB524Fixture is a temp HOME, a temp CODE_FORGE_TMP_ROOT, a project with a
// config and a fake worker. Every pid the fake recorded is killed at the end.
func newB524Fixture(t *testing.T, mode string) *b524Fixture {
	t.Helper()
	dir := t.TempDir()
	f := &b524Fixture{dir: dir, home: filepath.Join(dir, "home"), tmpRoot: filepath.Join(dir, "tmp"), runRoot: filepath.Join(dir, "runroot")}
	f.workDir = filepath.Join(f.runRoot, "doctor")
	f.pids, f.pathFile = filepath.Join(dir, "fake-pids"), filepath.Join(dir, "fake-path")
	proj := filepath.Join(dir, "proj")
	for _, d := range []string{f.home, f.tmpRoot, f.workDir, proj} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(proj, ".code-forge.yml"), []byte("project:\n  slug: unit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(dir, "fake-worker")
	if err := os.WriteFile(fake, []byte(b524FakeWorker), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", f.home)
	t.Setenv("CODE_FORGE_TMP_ROOT", f.tmpRoot)
	snap, err := config.LoadSnapshot(proj)
	if err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "B524_PIDS="+f.pids, "B524_PATH_FILE="+f.pathFile, "B524_MODE="+mode)
	f.ctx = dwpCtx{cfg: snap, configFile: snap.Path, workDir: f.workDir, runRootDir: f.runRoot, env: env, runID: "r-20261112-000000-b524aa", home: f.home}
	beforeBinary, beforeTimeout, beforeStop, beforeVerify := runWorkerBinary, runLaunchTimeout, dwpStopWait, dwpVerify
	runWorkerBinary = func() (string, error) { return fake, nil }
	runLaunchTimeout = 5 * time.Second
	t.Cleanup(func() {
		runWorkerBinary, runLaunchTimeout, dwpStopWait, dwpVerify = beforeBinary, beforeTimeout, beforeStop, beforeVerify
		for _, pid := range f.launched() {
			if pid > 1 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	return f
}

func (f *b524Fixture) launched() []int {
	data, _ := os.ReadFile(f.pids)
	out := []int{}
	for _, line := range strings.Fields(string(data)) {
		if pid, err := strconv.Atoi(line); err == nil {
			out = append(out, pid)
		}
	}
	return out
}

var b524Pid = regexp.MustCompile(`^pid \d+ `)

// render is the rows as Node's JSON lines print them, the worker pid masked.
func b524Render(rows []doctorRow) string {
	lines := []string{}
	for _, r := range rows {
		lines = append(lines, r.id+"|"+r.status+"|"+r.label+"|"+b524Pid.ReplaceAllString(r.detail, "pid N "))
	}
	return strings.Join(lines, "\n")
}

func b524List(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return "null"
	}
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return "[" + strings.Join(names, ",") + "]"
}

// left is golden.mjs's LEFT line: the runs dir, the work dir, the worker's
// temp root and the pid registry after the probe.
func (f *b524Fixture) left(t *testing.T) string {
	_, err := os.Stat(filepath.Join(f.tmpRoot, f.ctx.runID))
	return "runs=" + b524List(t, filepath.Join(f.home, ".code-forge", "runs")) + " workDir=" + b524List(t, f.workDir) + " tmproot=" + strconv.FormatBool(err == nil) + " pids=" + b524List(t, filepath.Join(f.runRoot, "pids"))
}

func (f *b524Fixture) assertGone(t *testing.T) {
	t.Helper()
	for _, pid := range f.launched() {
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("worker pid %d still runs after the probe (%v)", pid, err)
		}
	}
	if _, err := os.Stat(filepath.Join(f.home, ".code-forge", "ledger")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the probe wrote a ledger: %v", err)
	}
}

// Node goldens (scratch probe run of src/doctor/worker-probe.mjs, B524).
const (
	b524OK         = "signer-key|OK|signer key|generated; a signed row verifies, a changed one is refused\nworker|OK|worker|pid N started, announced itself and stopped on SIGTERM\nsigner|INFO|signer|same-user boundary only"
	b524NodeOKLeft = "runs=[] workDir=[] tmproot=false pids=[]"
)

func TestDoctorWorkerProbeOK(t *testing.T) {
	f := newB524Fixture(t, "")
	rows, ok := doctorWorkerProbe(f.ctx)
	if !ok || b524Render(rows) != b524OK {
		t.Fatalf("rows (ok=%v):\n%s", ok, b524Render(rows))
	}
	if got := f.left(t); got != b524NodeOKLeft {
		t.Fatalf("left behind: %s", got)
	}
	if len(f.launched()) != 1 || !strings.HasPrefix(rows[1].detail, "pid "+strconv.Itoa(f.launched()[0])+" ") {
		t.Fatalf("launched %v, row %q", f.launched(), rows[1].detail)
	}
	f.assertGone(t)
}

func TestDoctorWorkerProbeNeverAnnounces(t *testing.T) {
	f := newB524Fixture(t, "exit")
	runLaunchTimeout = time.Second
	rows, ok := doctorWorkerProbe(f.ctx)
	want := "signer-key|OK|signer key|generated; a signed row verifies, a changed one is refused\nworker|FAIL|worker|could not start (worker_down)\nsigner|INFO|signer|same-user boundary only"
	if !ok || b524Render(rows) != want {
		t.Fatalf("rows:\n%s", b524Render(rows))
	}
	if got := f.left(t); got != "runs=[] workDir=[] tmproot=false pids=null" {
		t.Fatalf("left behind: %s", got)
	}
	f.assertGone(t)
}

func TestDoctorWorkerProbeSignerVerifyFails(t *testing.T) {
	for name, verify := range map[string]func(map[string]any, []byte) (sign.Result, error){
		"verify fails":       func(map[string]any, []byte) (sign.Result, error) { return sign.Result{Reason: "mismatch"}, nil },
		"byte flip accepted": func(map[string]any, []byte) (sign.Result, error) { return sign.Result{OK: true}, nil },
	} {
		t.Run(name, func(t *testing.T) {
			f := newB524Fixture(t, "")
			dwpVerify = verify
			rows, ok := doctorWorkerProbe(f.ctx)
			want := "signer-key|FAIL|signer key|sign/verify round trip failed\nworker|OK|worker|pid N started, announced itself and stopped on SIGTERM\nsigner|INFO|signer|same-user boundary only"
			if !ok || b524Render(rows) != want {
				t.Fatalf("rows:\n%s", b524Render(rows))
			}
			if got := f.left(t); got != b524NodeOKLeft {
				t.Fatalf("left behind: %s", got)
			}
			f.assertGone(t)
		})
	}
}

func TestDoctorWorkerProbeIgnoresSIGTERM(t *testing.T) {
	f := newB524Fixture(t, "ignoreterm")
	dwpStopWait = 500 * time.Millisecond
	rows, ok := doctorWorkerProbe(f.ctx)
	want := "signer-key|OK|signer key|generated; a signed row verifies, a changed one is refused\nworker|FAIL|worker|pid N start/stop incomplete (announced=true, stopped on SIGTERM=false, retracted=true)\nsigner|INFO|signer|same-user boundary only"
	if !ok || b524Render(rows) != want {
		t.Fatalf("rows:\n%s", b524Render(rows))
	}
	if got := f.left(t); got != b524NodeOKLeft {
		t.Fatalf("left behind: %s", got)
	}
	f.assertGone(t)
}

func TestDoctorWorkerProbeNoGit(t *testing.T) {
	f := newB524Fixture(t, "")
	f.ctx.env = dwpEnvSet(f.ctx.env, "PATH", "/nonexistent")
	rows, ok := doctorWorkerProbe(f.ctx)
	want := "signer-key|FAIL|signer key|run key could not be generated (git-init)\nworker|FAIL|worker|could not start (git-init)\nsigner|INFO|signer|same-user boundary only"
	if !ok || b524Render(rows) != want {
		t.Fatalf("rows:\n%s", b524Render(rows))
	}
	if got := f.left(t); got != "runs=null workDir=[] tmproot=false pids=null" {
		t.Fatalf("left behind: %s", got)
	}
	if len(f.launched()) != 0 {
		t.Fatalf("a worker was launched: %v", f.launched())
	}
}

func TestDoctorWorkerProbeConfigFileMissing(t *testing.T) {
	f := newB524Fixture(t, "")
	f.ctx.configFile = filepath.Join(f.dir, "nonexistent", "x.yml")
	rows, ok := doctorWorkerProbe(f.ctx)
	want := "signer-key|FAIL|signer key|run key could not be generated (ENOENT)\nworker|FAIL|worker|could not start (ENOENT)\nsigner|INFO|signer|same-user boundary only"
	if !ok || b524Render(rows) != want {
		t.Fatalf("rows:\n%s", b524Render(rows))
	}
	if got := f.left(t); got != "runs=null workDir=[] tmproot=false pids=null" {
		t.Fatalf("left behind: %s", got)
	}
}

// A PATH without ps (only git): the worker gets PATH plus a private dir that
// holds a ps link, as Node's probe does.
func TestDoctorWorkerProbeRestrictedPath(t *testing.T) {
	f := newB524Fixture(t, "")
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	gitOnly := filepath.Join(f.dir, "gitonly")
	if err := os.MkdirAll(gitOnly, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(git, filepath.Join(gitOnly, "git")); err != nil {
		t.Fatal(err)
	}
	f.ctx.env = dwpEnvSet(f.ctx.env, "PATH", gitOnly)
	rows, ok := doctorWorkerProbe(f.ctx)
	if !ok || b524Render(rows) != b524OK {
		t.Fatalf("rows:\n%s", b524Render(rows))
	}
	seen, _ := os.ReadFile(f.pathFile)
	if want := gitOnly + ":" + filepath.Join(f.workDir, "worker-repo", "worker-tools") + "\n"; string(seen) != want {
		t.Fatalf("worker PATH = %q, want %q", seen, want)
	}
	if got := f.left(t); got != b524NodeOKLeft {
		t.Fatalf("left behind: %s", got)
	}
	f.assertGone(t)
}

// Go hands over (ok false) before any side effect when it cannot name the
// worker binary or the inputs are not a probe Node would make.
func TestDoctorWorkerProbeHandsOverBeforeSideEffects(t *testing.T) {
	f := newB524Fixture(t, "")
	runWorkerBinary = func() (string, error) { return "", errors.New("no executable") }
	if rows, ok := doctorWorkerProbe(f.ctx); ok || rows != nil {
		t.Fatalf("rows %v ok %v", rows, ok)
	}
	if got := f.left(t); got != "runs=null workDir=[] tmproot=false pids=null" {
		t.Fatalf("left behind: %s", got)
	}
}

// CODE_FORGE_B524_WORKER names a built code-forge-native: the probe launches
// its real `worker --run <id>` (manual parity check; skipped otherwise).
func TestDoctorWorkerProbeRealWorker(t *testing.T) {
	binary := os.Getenv("CODE_FORGE_B524_WORKER")
	if binary == "" {
		t.Skip("CODE_FORGE_B524_WORKER not set")
	}
	f := newB524Fixture(t, "")
	runWorkerBinary = func() (string, error) { return binary, nil }
	runLaunchTimeout = 15 * time.Second
	rows, ok := doctorWorkerProbe(f.ctx)
	if !ok || b524Render(rows) != b524OK {
		t.Fatalf("rows:\n%s", b524Render(rows))
	}
	pid, _ := strconv.Atoi(strings.Fields(rows[1].detail)[1])
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("worker pid %d still runs (%v)", pid, err)
	}
	if got := f.left(t); got != b524NodeOKLeft {
		t.Fatalf("left behind: %s", got)
	}
}
