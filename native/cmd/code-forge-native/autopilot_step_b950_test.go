package main

import (
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/config"
)

// B950: a speed budget per step (land, gate, coder, CI wait).

// b950Run is a run with an active until-done grant whose config snapshot
// sets autopilot.step_budgets.
func b950Run(t *testing.T, budgets map[string]any) *hvFixture {
	t.Helper()
	cfg := map[string]any{"version": float64(1), "autopilot": map[string]any{"step_budgets": budgets}}
	h := config.Hash(cfg)
	return b800Fixture(t, map[string]any{
		"config":    map[string]any{"hash": h, "snapshots": map[string]any{h: cfg}},
		"autopilot": b950Grant(),
	})
}

// b950Grant is an active until-done grant.
func b950Grant() map[string]any {
	return map[string]any{"grant_id": "ap-1", "status": "active", "started_at": "2026-11-19T08:00:00.000Z", "until": nil, "scopes": []any{"waive"}, "deny": []any{}, "delegate": "L2", "caps": map[string]any{}, "stop_at": 0.8, "until_done": true, "planned_blocks": []any{"B1"}}
}

func b950Rows(f *hvFixture, event string) []map[string]any {
	out := []map[string]any{}
	for _, row := range f.ledger() {
		if row["event"] == event {
			out = append(out, row)
		}
	}
	return out
}

// Over budget: WARN, a step.over_budget row and one found block (deduped);
// under budget: nothing.
func TestB950StepOverBudgetWarnsRowsFound(t *testing.T) {
	f := b950Run(t, map[string]any{"min_samples": float64(3), "margin": float64(0.5)})
	for i := 0; i < 3; i++ {
		code, out, errText := autopilotCall(t, "step", "--run", "r-test", "--step", "coder", "--ms", "60000")
		if code != 0 || errText != "" || !strings.Contains(out, "coder 1m00s recorded") {
			t.Fatalf("warm-up %d = %d %q %q", i, code, out, errText)
		}
	}
	code, out, errText := autopilotCall(t, "step", "--run", "r-test", "--step", "coder", "--ms", "89000", "--block", "B1")
	if code != 0 || errText != "" || !strings.Contains(out, "within budget 1m30s") {
		t.Fatalf("under budget = %d %q %q", code, out, errText)
	}
	if rows := b950Rows(f, "step.over_budget"); len(rows) != 0 || len(b800Found(t, f)) != 0 {
		t.Fatalf("under budget wrote rows %v / found %v", rows, b800Found(t, f))
	}
	code, out, errText = autopilotCall(t, "step", "--run", "r-test", "--step", "coder", "--ms", "200000", "--block", "B1")
	if code != 0 || !strings.Contains(out, "over budget") {
		t.Fatalf("over budget = %d %q %q", code, out, errText)
	}
	if !strings.Contains(errText, "WARN step budget: coder took 3m20s, over its budget 1m30s") || !strings.Contains(errText, "(block B1)") {
		t.Fatalf("no WARN line: %q", errText)
	}
	rows := b950Rows(f, "step.over_budget")
	if len(rows) != 1 || rows[0]["step"] != "coder" || rows[0]["ms"] != float64(200000) || rows[0]["budget_ms"] != float64(90000) || rows[0]["block"] != "B1" || rows[0]["source"] != "measured" {
		t.Fatalf("rows = %v", rows)
	}
	found := b800Found(t, f)
	if len(found) != 1 || found[0]["source"] != "speed" || found[0]["state"] != "queued" || found[0]["block"] != "B2" || found[0]["from_block"] != "B1" {
		t.Fatalf("found = %v", found)
	}
	// the same step over budget again: a new row, the same finding seen twice
	if code, _, errText = autopilotCall(t, "step", "--run", "r-test", "--step", "coder", "--ms", "400000"); code != 0 || !strings.Contains(errText, "WARN step budget") {
		t.Fatalf("second over = %d %q", code, errText)
	}
	if found = b800Found(t, f); len(found) != 1 || found[0]["seen"] != float64(2) {
		t.Fatalf("found after the second over = %v", found)
	}
	// the binnacle log names the row
	if code, out, errText = autopilotCall(t, "log", "--run", "r-test"); code != 0 || !strings.Contains(out, "Step over budget") {
		t.Fatalf("log = %d %q %q", code, out, errText)
	}
}

// A fixed budget from the config needs no samples; with no grant the found
// block is not recorded, the WARN and the row still are.
func TestB950FixedBudgetWithoutGrant(t *testing.T) {
	cfg := map[string]any{"version": float64(1), "autopilot": map[string]any{"step_budgets": map[string]any{"ci_wait": float64(5)}}}
	h := config.Hash(cfg)
	f := b800Fixture(t, map[string]any{"config": map[string]any{"hash": h, "snapshots": map[string]any{h: cfg}}})
	code, out, errText := autopilotCall(t, "step", "--run", "r-test", "--step", "ci-wait", "--ms", "360000", "--json")
	if code != 0 || !strings.Contains(out, `"budget_ms":300000`) || !strings.Contains(out, `"over":true`) || !strings.Contains(errText, "autopilot.step_budgets.ci_wait") {
		t.Fatalf("fixed = %d %q %q", code, out, errText)
	}
	if len(b950Rows(f, "step.over_budget")) != 1 || len(b800Found(t, f)) != 0 {
		t.Fatalf("rows %v found %v", b950Rows(f, "step.over_budget"), b800Found(t, f))
	}
	if code, _, errText = autopilotCall(t, "step", "--run", "r-test", "--step", "deploy", "--ms", "1"); code != 2 || !strings.Contains(errText, "--step must be one of land, gate, coder, ci-wait") {
		t.Fatalf("bad step = %d %q", code, errText)
	}
	if code, _, errText = autopilotCall(t, "step", "--run", "r-test", "--step", "land", "--ms", "-4"); code != 2 || !strings.Contains(errText, "--ms needs") {
		t.Fatalf("bad ms = %d %q", code, errText)
	}
}

// The land step reads its wall time from the clock: a fake clock over the
// budget gives the WARN, the row and the found block; under it, nothing.
func TestB950LandFakeClock(t *testing.T) {
	f := b950Run(t, map[string]any{"land": float64(10)}) // 10 minutes
	start := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	before := stepNow
	t.Cleanup(func() { stepNow = before })
	var buf strings.Builder
	stepNow = func() time.Time { return start.Add(9 * time.Minute) }
	landAfter(&buf, f.home, "r-test", "B1", start, true, 0, "")
	if buf.String() != "" || len(b950Rows(f, "step.over_budget")) != 0 || len(b800Found(t, f)) != 0 {
		t.Fatalf("under budget: %q", buf.String())
	}
	stepNow = func() time.Time { return start.Add(25 * time.Minute) }
	landAfter(&buf, f.home, "r-test", "B1", start, false, 2, "") // a refused land is not timed
	if buf.String() != "" {
		t.Fatalf("failed land timed: %q", buf.String())
	}
	landAfter(&buf, f.home, "r-test", "B1", start, true, 0, "")
	if !strings.Contains(buf.String(), "WARN step budget: land took 25m00s, over its budget 10m00s") {
		t.Fatalf("over budget: %q", buf.String())
	}
	if rows := b950Rows(f, "step.over_budget"); len(rows) != 1 || rows[0]["step"] != "land" {
		t.Fatalf("rows = %v", rows)
	}
	if found := b800Found(t, f); len(found) != 1 || found[0]["source"] != "speed" || found[0]["block"] != "B2" {
		t.Fatalf("found = %v", found)
	}
}
