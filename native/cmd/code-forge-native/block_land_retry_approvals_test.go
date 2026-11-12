package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B589 (issue #36, report 4): a land that failed after close is retried with
// `block land`. Under an autopilot run the record holds active approvals (and
// a grant); the retry ends no block, so it stays in Go instead of going to
// Node, which refuses every closed block ("block is closed, not open").
func TestLandClosedRetryUnderAutopilotApprovals(t *testing.T) {
	f := androidLand(t)
	const run = "r-android-approvals"
	entry := ledger.Object{{Key: "status", Value: "closed"}, {Key: "owned_files", Value: []any{"sample.mjs"}}, {Key: "base_branch", Value: "main"}, {Key: "worktree", Value: ledger.Object{{Key: "path", Value: f.tree}, {Key: "branch", Value: "block-B1"}, {Key: "base_sha", Value: f.base}}}}
	approval := ledger.Object{{Key: "id", Value: "a1"}, {Key: "status", Value: "active"}, {Key: "until", Value: "2099-01-01T00:00:00.000Z"}}
	record := ledger.Object{{Key: "run_id", Value: run}, {Key: "project", Value: "fixture"}, {Key: "workspace", Value: f.repo}, {Key: "blocks", Value: ledger.Object{{Key: "B1", Value: entry}}}, {Key: "orphans", Value: []any{}}, {Key: "autopilot_approvals", Value: []any{approval}}}
	if err := state.SaveRun(f.home, record); err != nil {
		t.Fatal(err)
	}
	landWrite(t, filepath.Join(f.home, ".code-forge/runs", run+".key"), strings.Repeat("0", 32))
	landDone(t, f.home, run, landGit(t, f.tree, "rev-parse", "HEAD"))
	saved, err := state.ReadRun(f.home, run)
	if err != nil {
		t.Fatal(err)
	}
	capture, err := os.CreateTemp(f.home, "output-")
	if err != nil {
		t.Fatal(err)
	}
	out, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = capture, capture
	flags := map[string]any{"report": "unused", "transcript": "unused", "merge": true, "no-merge-gate": true}
	code := landExecuteWithReason(nil, flags, f.home, run, "B1", saved, blockObject(blockObject(saved.Get("blocks")).Get("B1")), func(reason handoverReason) int {
		t.Errorf("closed retry handed over to Node: %v", reason)
		return 127
	})
	os.Stdout, os.Stderr = out, stderr
	capture.Close()
	data, err := os.ReadFile(capture.Name())
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 || !strings.Contains(string(data), "block B1 landed") || strings.Contains(string(data), "closed, not open") {
		t.Fatalf("retry: exit=%d output=%q, want B1 landed", code, data)
	}
	if got := landGit(t, f.repo, "show", "HEAD:sample.mjs"); got != "new" {
		t.Fatalf("merged sample=%q, want new", got)
	}
	after, err := state.ReadRun(f.home, run)
	if err != nil {
		t.Fatal(err)
	}
	approvals, _ := after.Get("autopilot_approvals").([]any)
	if len(approvals) != 1 || blockObject(approvals[0]).Get("status") != "active" {
		t.Fatalf("approvals after the retry=%v, want the one active approval untouched", after.Get("autopilot_approvals"))
	}
}
