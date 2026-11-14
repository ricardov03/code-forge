package worker

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// TestB634WorkerExpiryEndsUntilDoneGrant: the worker's grant expiry check
// ends an until-done grant once every planned block is closed, in Go (no
// hand-over), with one signed autopilot.expire row (reason done).
func TestB634WorkerExpiryEndsUntilDoneGrant(t *testing.T) {
	home := t.TempDir()
	run := "r-b634"
	grant, err := ledger.DecodeObject([]byte(`{"grant_id":"ap-done","status":"active","until":null,"until_done":true,"planned_blocks":["B1","B2"],"scopes":["close:routine"],"deny":[],"delegate":"L2","caps":{},"stop_at":0.9,"started_at":"2026-01-01T00:00:00.000Z","stopped_at":null,"expired_at":null,"link":null}`))
	if err != nil {
		t.Fatal(err)
	}
	blocks := func(b2 string) ledger.Object {
		o, err := ledger.DecodeObject([]byte(`{"B1":{"status":"closed"},"B2":{"status":"` + b2 + `"}}`))
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	r := ledger.Object{{Key: "run_id", Value: run}, {Key: "workspace", Value: t.TempDir()}, {Key: "project", Value: "b634"}, {Key: "status", Value: "active"},
		{Key: "blocks", Value: blocks("open")}, {Key: "autopilot", Value: grant}}
	if err := state.SaveRun(home, r); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".code-forge/runs", run+".key"), make([]byte, 32), 0600); err != nil {
		t.Fatal(err)
	}
	now, _ := time.Parse(time.RFC3339, "2026-01-01T12:00:00Z")
	l := &Loop{Runner: &TicketRunner{Home: home, RunID: run}, Now: func() time.Time { return now }}
	if err := l.ExpiryCheck(); err != nil || l.Handoff() != "" {
		t.Fatalf("open block: %v %q", err, l.Handoff())
	}
	saved, _ := state.ReadRun(home, run)
	if saved.Get("autopilot").(ledger.Object).Get("status") != "active" {
		t.Fatalf("grant ended with a block open: %+v", saved.Get("autopilot"))
	}
	if err := state.UpdateRun(home, run, func(record *ledger.Object) error {
		record.Set("blocks", blocks("closed"))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := l.ExpiryCheck(); err != nil || l.Handoff() != "" {
		t.Fatalf("done: %v %q", err, l.Handoff())
	}
	saved, _ = state.ReadRun(home, run)
	stored := saved.Get("autopilot").(ledger.Object)
	if stored.Get("status") != "expired" || stored.Get("done_at") != "2026-01-01T12:00:00.000Z" {
		t.Fatalf("grant after the last block closed: %+v", stored)
	}
	rows, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge/ledger"), "b634")
	if err != nil || len(rows) != 1 || rows[0].(map[string]any)["reason"] != "done" {
		t.Fatalf("rows: %+v, %v", rows, err)
	}
}
