package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B826: review-file heals a dead worker. When no live worker serves an
// active run of this repository (the worker died, or left after
// worker.idle_exit_hours), review-file starts the run's worker exactly as
// `run start --add-worker` does (same lock, same review.workers limit, same
// pin) and goes on. When that start fails, the answer stays worker_down and
// carries the one command that fixes it.

// reviewHealResult is one heal attempt: tried is false when the run is not an
// active run of this repository (nothing is started, worker_down as before).
type reviewHealResult struct {
	tried   bool
	started bool   // this call launched the worker
	pid     int    // the worker that now serves the run (0 when none)
	failure string // why the start failed (tried and pid 0)
}

// reviewHeal is the heal seam (tests replace it).
var reviewHeal = reviewHealWorker

func reviewHealFix(run string) string { return "code-forge run start --add-worker --run " + run }

func reviewHealWorker(root, run string) reviewHealResult {
	home := os.Getenv("HOME")
	if home == "" || !reviewBlockRun.MatchString(run) {
		return reviewHealResult{}
	}
	record, err := state.ReadRun(home, run)
	if err != nil || record.Get("status") != "active" {
		return reviewHealResult{}
	}
	workspace, _ := record.Get("workspace").(string)
	top, err := gitx.RepoTop(workspace)
	if workspace == "" || err != nil || !reviewHealSameDir(top, root) {
		return reviewHealResult{}
	}
	runApplyTmpRoot()
	binary, err := runWorkerBinary()
	if err != nil {
		return reviewHealResult{tried: true, failure: "the code-forge binary cannot be found"}
	}
	o := &runOut{}
	pid, _, started, err := runStartWorker(o, home, run, binary, true, true)
	if err == nil {
		return reviewHealResult{tried: true, started: started, pid: pid}
	}
	failure := "the worker registry could not be read"
	var fail *runFail
	var stateErr *state.Error
	switch {
	case errors.Is(err, errRunHandover):
	case errors.As(err, &fail):
		failure = fail.msg
	case errors.As(err, &stateErr):
		failure = stateErr.Message
	default:
		failure = blockIOMessage(err)
	}
	// Another caller may have started the worker meanwhile.
	if w, ok := reviewEnqueueWorker(root); ok && w != nil && w.Get("run") == run {
		n, _ := w.Get("pid").(float64)
		return reviewHealResult{tried: true, pid: int(n)}
	}
	return reviewHealResult{tried: true, failure: failure}
}

func reviewHealSameDir(a, b string) bool {
	if ra, err := filepath.EvalSymlinks(a); err == nil {
		a = ra
	}
	if rb, err := filepath.EvalSymlinks(b); err == nil {
		b = rb
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// reviewHealSay prints the one stderr line of a heal: the started worker, or
// the failure and its fix. Nothing for a run that was not tried or a worker
// another caller started.
func reviewHealSay(run string, h reviewHealResult) {
	switch {
	case h.started:
		fmt.Fprintf(safeStderr, "review-file: no live worker for run %s; started worker pid %d\n", run, h.pid)
	case h.tried && h.pid == 0:
		fmt.Fprintf(safeStderr, "review-file: no live worker for run %s; could not start one (%s); fix: %s\n", run, h.failure, reviewHealFix(run))
	}
}

// reviewHealDown adds the failed start (start_error) and the fix to a worker_down
// answer.
func reviewHealDown(out ledger.Object, run string, h reviewHealResult) ledger.Object {
	if h.tried && h.pid == 0 {
		out.Set("start_error", h.failure)
		out.Set("fix", reviewHealFix(run))
	}
	return out
}

// reviewHealTicketRun is the run of a queued ticket (queue/<ticket>.json).
func reviewHealTicketRun(root, ticket string) string {
	data, err := os.ReadFile(filepath.Join(root, ".code-forge", "queue", ticket+".json"))
	if err != nil {
		return ""
	}
	var t struct {
		Run string `json:"run"`
	}
	if json.Unmarshal(data, &t) != nil {
		return ""
	}
	return t.Run
}
