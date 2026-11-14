package autopilot

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/sign"
	"github.com/ricardov03/code-forge/native/state"
)

// B634: an until-done grant ends when every planned block of the run is
// closed, or at its deadline (until) when that comes first.

func b634Grant(t *testing.T, until string, planned string) ledger.Object {
	t.Helper()
	body := `{"grant_id":"ap-done","status":"active","until":` + until + `,"until_done":true` + planned + `,"scopes":["close:routine"],"deny":[],"delegate":"L2","caps":{},"stop_at":0.9,"started_at":"2026-01-01T00:00:00.000Z","stopped_at":null,"expired_at":null,"link":null}`
	g, err := ledger.DecodeObject([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func b634Blocks(t *testing.T, body string) ledger.Object {
	t.Helper()
	b, err := ledger.DecodeObject([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestB634UntilDoneReadGrant(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2026-01-01T12:00:00Z")
	for _, tc := range []struct {
		name, until, planned, blocks string
		want                         string
		done                         bool
	}{
		{"no blocks yet", `null`, ``, `{}`, "active", false},
		{"one open", `null`, ``, `{"B1":{"status":"closed"},"B2":{"status":"open"}}`, "active", false},
		{"stopped block is not done", `null`, ``, `{"B1":{"status":"closed"},"B2":{"status":"stopped"}}`, "active", false},
		{"all closed", `null`, ``, `{"B1":{"status":"closed"},"B2":{"status":"awaiting_human_check"}}`, "expired", true},
		{"planned block not opened", `null`, `,"planned_blocks":["B1","B3"]`, `{"B1":{"status":"closed"}}`, "active", false},
		{"planned all closed, extra open", `null`, `,"planned_blocks":["B1"]`, `{"B1":{"status":"closed"},"B9":{"status":"open"}}`, "expired", true},
		{"deadline first", `"2026-01-01T11:00:00.000Z"`, ``, `{"B1":{"status":"open"}}`, "expired", false},
		{"deadline later, open", `"2026-01-01T18:00:00.000Z"`, ``, `{"B1":{"status":"open"}}`, "active", false},
	} {
		r := ledger.Object{{Key: "status", Value: "active"}, {Key: "blocks", Value: b634Blocks(t, tc.blocks)}, {Key: "autopilot", Value: b634Grant(t, tc.until, tc.planned)}}
		result, err := ReadGrant(r, now)
		if err != nil || result.State != tc.want || result.Done != tc.done {
			t.Fatalf("%s: %+v, %v; want %s done=%v", tc.name, result, err, tc.want, tc.done)
		}
	}
	// the run ended: the grant no longer acts, and is not "done"
	r := ledger.Object{{Key: "status", Value: "ended"}, {Key: "blocks", Value: b634Blocks(t, `{"B1":{"status":"closed"}}`)}, {Key: "autopilot", Value: b634Grant(t, `null`, ``)}}
	if result, err := ReadGrant(r, now); err != nil || result.State != "run-ended" || result.Done {
		t.Fatalf("run ended: %+v, %v", result, err)
	}
	// a clock grant never ends on closed blocks
	clock := grantFixture(t)
	r = ledger.Object{{Key: "status", Value: "active"}, {Key: "blocks", Value: b634Blocks(t, `{"B1":{"status":"closed"}}`)}, {Key: "autopilot", Value: clock}}
	if result, err := ReadGrant(r, now); err != nil || result.State != "active" || result.Done {
		t.Fatalf("clock grant: %+v, %v", result, err)
	}
}

func TestB634UntilDoneShapes(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2026-01-01T12:00:00Z")
	for _, mutate := range []func(*ledger.Object){
		func(g *ledger.Object) { g.Set("until_done", false) },
		func(g *ledger.Object) { g.Set("planned_blocks", []any{}) },
		func(g *ledger.Object) { g.Set("planned_blocks", []any{""}) },
		func(g *ledger.Object) { g.Set("done_at", "soon") },
		func(g *ledger.Object) { g.Delete("until_done") }, // null until on a clock grant
	} {
		g := b634Grant(t, `null`, ``)
		mutate(&g)
		if _, err := GrantState(g, now); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("shape %+v: got %v, want ErrUnsupported", g, err)
		}
	}
	clock := grantFixture(t)
	clock.Set("planned_blocks", []any{"B1"})
	if _, err := GrantState(clock, now); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("planned_blocks on a clock grant: got %v", err)
	}
}

func TestB634CheckExpiryDone(t *testing.T) {
	home := t.TempDir()
	run := "r-done"
	r := ledger.Object{{Key: "run_id", Value: run}, {Key: "workspace", Value: t.TempDir()}, {Key: "project", Value: "expiry"}, {Key: "status", Value: "active"},
		{Key: "blocks", Value: b634Blocks(t, `{"B1":{"status":"closed"},"B2":{"status":"open"}}`)}, {Key: "autopilot", Value: b634Grant(t, `null`, ``)}}
	if err := state.SaveRun(home, r); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if err := os.WriteFile(filepath.Join(home, ".code-forge/runs", run+".key"), key, 0600); err != nil {
		t.Fatal(err)
	}
	now, _ := time.Parse(time.RFC3339, "2026-01-01T12:00:00Z")
	if result, err := CheckExpiry(home, run, now); err != nil || result.State != "active" || result.ExpiredNow {
		t.Fatalf("one open block: %+v, %v", result, err)
	}
	// the last planned block closes
	if err := state.UpdateRun(home, run, func(record *ledger.Object) error {
		record.Set("blocks", b634Blocks(t, `{"B1":{"status":"closed"},"B2":{"status":"closed"}}`))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	result, err := CheckExpiry(home, run, now)
	if err != nil || result.State != "expired" || !result.Done || !result.ExpiredNow {
		t.Fatalf("done: %+v, %v", result, err)
	}
	if result, err := CheckExpiry(home, run, now.Add(time.Minute)); err != nil || result.State != "expired" || result.ExpiredNow {
		t.Fatalf("second check: %+v, %v", result, err)
	}
	rows, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge/ledger"), "expiry")
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows: %d, %v", len(rows), err)
	}
	row := rows[0].(map[string]any)
	if verified, err := sign.VerifyRow(row, key); err != nil || !verified.OK {
		t.Fatalf("signature: %+v, %v", verified, err)
	}
	blocks, _ := row["blocks"].([]any)
	if row["event"] != "autopilot.expire" || row["reason"] != "done" || row["until"] != nil || len(blocks) != 2 || blocks[0] != "B1" || blocks[1] != "B2" || row["ts"] != "2026-01-01T12:00:00.000Z" {
		t.Fatalf("row: %+v", row)
	}
	saved, _ := state.ReadRun(home, run)
	stored := saved.Get("autopilot").(ledger.Object)
	if stored.Get("status") != "expired" || stored.Get("done_at") != "2026-01-01T12:00:00.000Z" || stored.Get("expired_at") != "2026-01-01T12:00:00.000Z" {
		t.Fatalf("saved: %+v", stored)
	}
	if s, err := GrantState(stored, now); err != nil || s != "expired" {
		t.Fatalf("stored state: %s, %v", s, err)
	}
}
