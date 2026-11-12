package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/filelock"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/sign"
	"github.com/ricardov03/code-forge/native/state"
)

// B524: src/doctor/worker-probe.mjs in Go, as a part the doctor full run calls
// (B525 wires it in). The probe makes a throwaway run in a throwaway git repo
// (the project's .code-forge.yml copied in): startRun makes the run key, a
// signed row verifies and a byte-flipped copy is refused, the worker
// (runWorkerBinary, as `run start` launches it) starts detached and announces
// itself, then it is stopped (SIGTERM; SIGKILL of its group after 5 s). The
// run's record, key, lock and the worker's temp root are removed afterwards.
// The probe signals only the pid it launched (and that pid's group).

// dwpCtx is probeWorkerAndSigner's ctx: cfg is the loaded config, env the
// doctor's environment (the worker and git get it), home the HOME whose
// ~/.code-forge/runs holds the run.
type dwpCtx struct {
	cfg        config.Snapshot
	configFile string
	workDir    string
	runRootDir string
	env        []string
	runID      string
	home       string
}

// dwpSignerRow is SIGNER_ROW, printed on every full run.
var dwpSignerRow = doctorRow{"signer", "INFO", "signer", "same-user boundary only"}

// Seams for tests: the SIGTERM grace, the wait after SIGKILL, the verifier.
var (
	dwpStopWait = 5 * time.Second
	dwpKillWait = 2 * time.Second
	dwpVerify   = sign.VerifyRow
)

// dwpErr is a failure with Node's err.code.
type dwpErr struct{ code string }

func (e *dwpErr) Error() string { return e.code }

// dwpCode is `err?.code ?? err?.name ?? 'error'` for the errors the probe meets.
func dwpCode(err error) string {
	var own *dwpErr
	var stateErr *state.Error
	var fail *runFail
	var errno syscall.Errno
	switch {
	case errors.As(err, &own):
		return own.code
	case errors.As(err, &stateErr):
		return stateErr.Code
	case errors.As(err, &fail):
		switch {
		case strings.HasSuffix(fail.msg, " already exists"):
			return "run-exists"
		case strings.HasSuffix(fail.msg, " already has a key"):
			return "key-exists"
		}
	case errors.As(err, &errno):
		if name := blockErrno(errno); name != "UNKNOWN" {
			return name
		}
	}
	return "error"
}

func dwpEnvGet(env []string, name string) (string, bool) {
	for i := len(env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(env[i], "="); ok && k == name {
			return v, true
		}
	}
	return "", false
}

func dwpEnvSet(env []string, name, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); k != name {
			out = append(out, kv)
		}
	}
	return append(out, name+"="+value)
}

// dwpGitChildEnv is ticket.mjs gitChildEnv(env).
func dwpGitChildEnv(env []string) []string {
	out := []string{}
	for _, kv := range env {
		if !strings.HasPrefix(kv, "GIT_") {
			out = append(out, kv)
		}
	}
	return append(out, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
}

// dwpLookPath finds command in the child's PATH, as spawn does.
func dwpLookPath(command, pathEnv string) string {
	for _, dir := range strings.Split(pathEnv, ":") {
		if dir == "" {
			continue
		}
		file := filepath.Join(dir, command)
		if st, err := os.Stat(file); err == nil && !st.IsDir() && syscall.Access(file, 1) == nil {
			return file
		}
	}
	return ""
}

// dwpCopyFile is copyFileSync: the bytes and the source's mode.
func dwpCopyFile(src, dst string) error {
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, st.Mode().Perm()); err != nil {
		return err
	}
	return os.Chmod(dst, st.Mode().Perm())
}

// dwpRegisterPid is reaper.mjs registerPid, with its error (Node throws).
func dwpRegisterPid(dir string, pid int, argv0 string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	start := "unknown"
	if t := filelock.ProcessStartTime(pid); t != nil {
		start = *t
	}
	data, err := ledger.Stringify(ledger.Object{{Key: "pid", Value: float64(pid)}, {Key: "start_time", Value: start}, {Key: "argv0", Value: argv0}}, false)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)+".json"), append(data, '\n'), 0o600)
}

func dwpWaitGone(pid int, within time.Duration) bool {
	until := time.Now().Add(within)
	for time.Now().Before(until) {
		if !runAlive(float64(pid)) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return !runAlive(float64(pid))
}

// dwpStopWorker is stopWorker: SIGTERM the launched pid, then SIGKILL its
// group when it is still alive after dwpStopWait. True: it stopped on SIGTERM.
func dwpStopWorker(pid int) bool {
	if pid <= 1 {
		return true
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return true // already gone
	}
	if dwpWaitGone(pid, dwpStopWait) {
		return true
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	dwpWaitGone(pid, dwpKillWait)
	return false
}

// dwpRemoveRun is removeRun: the probe run's record, key, lock and temp files.
func dwpRemoveRun(home, runID string) {
	dir := filepath.Join(home, ".code-forge", "runs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if name == runID || name == runID+".json" || name == runID+".key" || name == runID+".lock" || strings.HasPrefix(name, runID+".json.") {
			_ = os.RemoveAll(filepath.Join(dir, name))
		}
	}
}

// dwpStartRun is startRun({workspace: repo, project: 'doctor-probe', config:
// cfg, runId, writeRow: no-op}): the record and key; the run.start row is
// signed, never written.
func dwpStartRun(c dwpCtx, repo string) error {
	cfg := c.cfg.Ordered
	review, _ := cfg.Get("review").(ledger.Object)
	external, _ := review.Get("external").(ledger.Object)
	if external.Get("required") == true {
		if name, isString := external.Get("name").(string); !isString || runJSTrim(name) == "" {
			return &dwpErr{"external-no-name"}
		}
	}
	ws, err := canonicalPath(repo)
	if err != nil {
		return err
	}
	h, err := canonicalPath(c.home)
	if err != nil {
		return err
	}
	if rel, err := filepath.Rel(ws, filepath.Join(h, ".code-forge", "runs")); err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
		return &dwpErr{"workspace-contains-runs-dir"}
	}
	engine := any("auto")
	if v := cfg.Get("engine"); v != nil {
		engine = v
	}
	record := runNewRecord(cfg, c.runID, "doctor-probe", ws, engine, nil, false, runNow().UTC())
	rows := []ledger.Object{{{Key: "event", Value: "run.start"}, {Key: "engine", Value: engine}, {Key: "worker_pinned", Value: false}}}
	err = runCreateLocked(c.home, c.runID, record, rows, false)
	if errors.Is(err, errRunHandover) {
		return &dwpErr{"error"}
	}
	return err
}

// doctorWorkerProbe is probeWorkerAndSigner: the `signer key` row, the
// `worker` row and the `signer` line. ok false means Node runs the probe;
// that is decided before anything is written or started.
func doctorWorkerProbe(c dwpCtx) ([]doctorRow, bool) {
	if c.home == "" || !filepath.IsAbs(c.home) || !runIDPattern.MatchString(c.runID) || c.cfg.Ordered == nil {
		return nil, false
	}
	binary, err := runWorkerBinary()
	if err != nil {
		return nil, false
	}
	repo := filepath.Join(c.workDir, "worker-repo")
	rows := []doctorRow{}
	pid := 0
	signerKey := false
	err = func() error {
		if err := os.MkdirAll(repo, 0o700); err != nil {
			return err
		}
		pathEnv, _ := dwpEnvGet(c.env, "PATH")
		git := dwpLookPath("git", pathEnv)
		if git == "" {
			return &dwpErr{"git-init"}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		cmd := exec.CommandContext(ctx, git, "init", "-q")
		cmd.Dir = repo
		cmd.Env = dwpGitChildEnv(c.env)
		runErr := cmd.Run()
		cancel()
		if runErr != nil {
			return &dwpErr{"git-init"}
		}
		if err := dwpCopyFile(c.configFile, filepath.Join(repo, ".code-forge.yml")); err != nil {
			return err
		}
		if err := dwpStartRun(c, repo); err != nil {
			return err
		}

		key, err := dfLoadKey(c.home, c.runID)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return &dwpErr{"no-key"}
			}
			if msg := err.Error(); msg == "bad-key" || msg == "bad-key-mode" {
				return &dwpErr{msg}
			}
			return err
		}
		signerOk := false
		if signed, err := sign.SignRow(map[string]any{"event": "doctor.probe", "run": c.runID}, key); err == nil {
			flipped := make(map[string]any, len(signed))
			for k, v := range signed {
				flipped[k] = v
			}
			flipped["event"] = "doctor.probf"
			same, err1 := dwpVerify(signed, key)
			changed, err2 := dwpVerify(flipped, key)
			signerOk = err1 == nil && err2 == nil && same.OK && changed.Reason == "mismatch"
		}
		if signerOk {
			rows = append(rows, doctorRow{"signer-key", "OK", "signer key", "generated; a signed row verifies, a changed one is refused"})
		} else {
			rows = append(rows, doctorRow{"signer-key", "FAIL", "signer key", "sign/verify round trip failed"})
		}
		signerKey = true

		// The boot lock needs ps even with a restricted provider PATH. Expose just that utility.
		workerEnv := c.env
		if !commandOnPath("ps", pathEnv) {
			tools := filepath.Join(repo, "worker-tools")
			for _, systemDir := range []string{"/bin", "/usr/bin"} {
				if !commandOnPath("ps", systemDir) {
					continue
				}
				if err := os.Mkdir(tools, 0o700); err != nil {
					return err
				}
				if err := os.Symlink(filepath.Join(systemDir, "ps"), filepath.Join(tools, "ps")); err != nil {
					return err
				}
				joined := tools
				if pathEnv != "" {
					joined = pathEnv + ":" + tools
				}
				workerEnv = dwpEnvSet(c.env, "PATH", joined)
				break
			}
		}
		if _, err := gitx.RepoTop(repo); err != nil {
			return &dwpErr{"no-repo"}
		}
		launched, repoRoot, err := runLaunchWorkerEnv(c.runID, repo, binary, workerEnv)
		if err != nil {
			return &dwpErr{"worker_down"}
		}
		pid = launched
		pids := filepath.Join(c.runRootDir, "pids")
		if err := dwpRegisterPid(pids, pid, "code-forge worker"); err != nil { // a killed doctor's worker is reaped by the next sweep
			return err
		}
		live, ok := runLiveWorker(repoRoot, 0, false)
		announced := ok && live != nil && live.Get("pid") == float64(pid)
		clean := dwpStopWorker(pid)
		_ = os.Remove(filepath.Join(pids, strconv.Itoa(pid)+".json"))
		workerPid := pid
		pid = 0
		after, ok := runLiveWorker(repoRoot, 0, false)
		gone := ok && after == nil
		if announced && clean && gone {
			rows = append(rows, doctorRow{"worker", "OK", "worker", fmt.Sprintf("pid %d started, announced itself and stopped on SIGTERM", workerPid)})
		} else {
			rows = append(rows, doctorRow{"worker", "FAIL", "worker", fmt.Sprintf("pid %d start/stop incomplete (announced=%t, stopped on SIGTERM=%t, retracted=%t)", workerPid, announced, clean, gone)})
		}
		return nil
	}()
	if err != nil {
		code := dwpCode(err)
		if !signerKey {
			rows = append(rows, doctorRow{"signer-key", "FAIL", "signer key", "run key could not be generated (" + code + ")"})
		}
		rows = append(rows, doctorRow{"worker", "FAIL", "worker", "could not start (" + code + ")"})
	}
	if pid != 0 {
		dwpStopWorker(pid)
	}
	dwpRemoveRun(c.home, c.runID)
	tmp, _ := c.cfg.Ordered.Get("tmp").(ledger.Object)
	root, set := tmp.Get("root").(string)
	if base, err := runTmpBase(root, set); err == nil {
		_ = os.RemoveAll(filepath.Join(base, c.runID)) // the worker's own run root
	}
	_ = os.RemoveAll(repo)
	return append(rows, dwpSignerRow), true
}
