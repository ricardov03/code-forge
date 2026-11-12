package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/sign"
)

const levelTestPlan = "# Plan\n\n## Wave 1\n\nDispatch order: B2 ∥ B4\n\n| id | level | owned_files | acceptance |\n|---|---|---|---|\n| B2 | L2 | `src/a` | ok |\n| B3 | none | `src/b` | ok |\n| **B4** | **L1** (cheap) | `x` | ok \\| fine |\n| B5 | L3 | `y` | ok |\n| B7 | L1 | `z` | ok |\n"

// levelFixture: run ra holds a grant allowing model:choose, a plan file, the
// recorded lane answers and the delegate's signed level decisions.
func levelFixture(t *testing.T) (runFixture, string) {
	t.Helper()
	f := newAutopilotFixture(t)
	allow := strings.Replace(grantJSON("active", "2026-11-11T12:30:00.000Z", "null", "null"), `"deny":["model:choose"]`, `"deny":[]`, 1)
	allow = strings.Replace(allow, `"waive:nit"`, `"model:choose"`, 1)
	f.record(t, "ra", "active", allow)
	f.record(t, "rn", "active", grantJSON("active", "2026-11-11T12:30:00.000Z", "null", "null"))
	f.record(t, "rg", "active", "")
	plan := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(plan, []byte(levelTestPlan), 0600); err != nil {
		t.Fatal(err)
	}
	dec := func(id, block, decision string) string {
		row := map[string]any{"run": "ra", "event": "autopilot.decision", "decision_id": id, "grant_id": "ap-0000abcd", "scope": "model:choose", "acted": true, "decision": decision, "subject": map[string]any{"block": block}, "ts": "2026-11-11T09:55:00.000Z"}
		row, err := sign.SignRow(row, f.key)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(row)
		return string(data)
	}
	writeLedger(t, f,
		dec("e-1", "B2", "L1"), dec("e-2", "B2", "L2"), dec("e-4", "B4", "L2"), dec("e-5", "B5", "L2"),
		`{"run":"rq","event":"decision","question":"lane","block":"B2","source":"jev","answer":" l1 ","decision_id":"j-1"}`,
		`{"run":"rq","event":"decision","question":"lane","block":"B4","source":"rules","answer":"L2","plan":"other.md"}`,
		`{"run":"rq","event":"decision","question":"lane","block":"B4","source":"rules","answer":"L2","plan":"/x/plan.md"}`,
		`{"run":"rq","event":"decision","question":"lane","block":"B4","source":"human","answer":"L0"}`,
		`{"run":"rq","event":"decision","question":"lane","block":"B5","source":"jev","answer":"fast"}`,
	)
	return f, plan
}

// B478: `autopilot level` runs in Go: Node's usage checks, the recorded
// lane, the level rule min(lane, plan+1, L2), its refusal texts and its
// signed autopilot.level row (texts captured from Node on the same fixture).
func TestAutopilotLevelInGo(t *testing.T) {
	f, plan := levelFixture(t)
	code, out, errText := autopilotCall(t, "level", "--run", "ra", "--block", "B2", "--plan", plan, "--decision", "e-1")
	if want := "autopilot level run ra: block B2 codes at L1 (lane L1, plan L2, grant ap-0000abcd); block open without --level uses it\n"; code != 0 || out != want || errText != "" {
		t.Fatalf("level B2 = %d %q %q", code, out, errText)
	}
	rows := bnSignedRows(t, f)
	last := rows[len(rows)-1]
	data, _ := json.Marshal(last)
	if last["event"] != "autopilot.level" || last["run"] != "ra" || last["block"] != "B2" || last["level"] != "L1" || last["lane"] != "L1" || last["lane_source"] != "jev" || last["lane_decision_id"] != "j-1" || last["plan_level"] != "L2" || last["grant_id"] != "ap-0000abcd" || last["decision_id"] != "e-1" || last["ts"] != "2026-11-11T10:00:00.000Z" {
		t.Fatalf("level row: %s", data)
	}
	code, out, _ = autopilotCall(t, "level", "--run", "ra", "--block", "B4", "--plan", plan, "--decision", "e-4")
	if want := "autopilot level run ra: block B4 codes at L2 (lane L2, plan L1, grant ap-0000abcd); block open without --level uses it\n"; code != 0 || out != want {
		t.Fatalf("level B4 = %d %q", code, out)
	}
	rows = bnSignedRows(t, f)
	if last := rows[len(rows)-1]; last["lane_source"] != "rules" || last["lane_decision_id"] != nil || last["level"] != "L2" {
		t.Fatalf("level B4 row: %#v", last)
	}
	refused := "; the question goes to the owner (autopilot ask, then --decision <id>)\n"
	cases := []struct {
		args []string
		code int
		err  string
	}{
		{[]string{"--run", "ra", "--block", "B2", "--plan", plan, "--decision", "e-2"}, 1, "autopilot level: refused — decision e-2 decided \"L2\", not \"L1\"" + refused},
		{[]string{"--run", "ra", "--block", "B5", "--plan", plan, "--decision", "e-5"}, 1, "autopilot level: refused — block B5's recorded lane is \"fast\", not a level; the level goes to the owner\n"},
		{[]string{"--run", "ra", "--block", "B7", "--plan", plan, "--decision", "e-1"}, 1, "autopilot level: refused — block B7 has no recorded lane — run forge jev ask lane --block B7; the level goes to the owner\n"},
		{[]string{"--run", "ra", "--block", "B3", "--plan", plan, "--decision", "e-1"}, 2, "autopilot level: block B3 has no level (L0–L3) in the plan\n"},
		{[]string{"--run", "ra", "--block", "B9", "--plan", plan, "--decision", "e-1"}, 2, "autopilot level: block B9 is not in the plan's block table\n"},
		{[]string{"--run", "ra", "--block", "B2", "--decision", "e-1"}, 2, "autopilot level: autopilot level needs --plan\n"},
		{[]string{"--run", "ra", "--block", "B2", "--plan", plan + ".gone", "--decision", "e-1"}, 2, "autopilot level: --plan " + plan + ".gone cannot be read\n"},
		{[]string{"--run", "ra", "--block", "a b", "--plan", plan, "--decision", "e-1"}, 2, "autopilot level: autopilot level needs --block <id>\n"},
		{[]string{"--run", "ra", "--block", "B2", "--plan", plan, "--decision", "x y"}, 2, "autopilot level: autopilot level needs --decision <id> (the delegate's acted decision from autopilot ask)\n"},
		{[]string{"--run", "rzz", "--block", "B2", "--plan", plan, "--decision", "e-1"}, 1, "autopilot level: no run record for rzz\n"},
		{[]string{"--run", "rzz", "--block", "B2", "--decision", "e-1"}, 2, "autopilot level: autopilot level needs --plan\n"},
		{[]string{"--run", "rn", "--block", "B2", "--plan", plan, "--decision", "e-1"}, 1, "autopilot level: refused — the grant refuses (denied); the question goes to the owner\n"},
		{[]string{"--run", "rg", "--block", "B2", "--plan", plan, "--decision", "e-1"}, 1, "autopilot level: refused — the grant refuses (no-grant); the question goes to the owner\n"},
		{[]string{"--run", "ra", "--block", "B2", "--plan", plan, "--decision", "e-zz"}, 1, "autopilot level: refused — no autopilot.decision row e-zz in this run" + refused},
	}
	before := len(bnSignedRows(t, f))
	for _, tc := range cases {
		code, out, errText := autopilotCall(t, append([]string{"level"}, tc.args...)...)
		if code != tc.code || out != "" || errText != tc.err {
			t.Fatalf("%v = %d %q %q", tc.args, code, out, errText)
		}
	}
	if n := len(bnSignedRows(t, f)); n != before {
		t.Fatalf("a refusal wrote rows: %d, want %d", n, before)
	}
}

// B478: a plan or lane answer Go could read differently keeps the level on
// Node, before any write: a non-UTF-8 plan, a non-ASCII table header, a
// JavaScript-only space in the plan, a non-ASCII lane answer.
func TestAutopilotLevelHandsOver(t *testing.T) {
	f, plan := levelFixture(t)
	dir := filepath.Dir(plan)
	write := func(name, text string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	bad := write("bad.md", levelTestPlan+"\xff\n")
	header := write("header.md", strings.Replace(levelTestPlan, "| acceptance |\n", "| acceptance | é |\n", 1))
	space := write("space.md", levelTestPlan+"note\v\n")
	data, _ := os.ReadFile(filepath.Join(f.home, ".code-forge", "ledger", "unit.jsonl"))
	if err := os.WriteFile(filepath.Join(f.home, ".code-forge", "ledger", "unit.jsonl"), append(data, []byte(`{"run":"rq","event":"decision","question":"lane","block":"B7","source":"jev","answer":"L1é"}`+"\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	before := len(bnSignedRows(t, f))
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	t.Setenv("CODE_FORGE_NATIVE", "1")
	cases := [][]string{
		{"--run", "ra", "--block", "B2", "--plan", bad, "--decision", "e-1"},
		{"--run", "ra", "--block", "B2", "--plan", header, "--decision", "e-1"},
		{"--run", "ra", "--block", "B2", "--plan", space, "--decision", "e-1"},
		{"--run", "ra", "--block", "B7", "--plan", plan, "--decision", "e-1"},
	}
	for _, args := range cases {
		if code, out, _ := autopilotCall(t, append([]string{"level"}, args...)...); code != 127 || out != "" {
			t.Fatalf("%v: hand-over exit = %d stdout %q, want 127", args, code, out)
		}
	}
	logged, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "native", "handovers.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(logged)), "\n")
	if len(lines) != len(cases) {
		t.Fatalf("handover rows = %d, want %d", len(lines), len(cases))
	}
	for i, line := range lines {
		var row map[string]string
		if err := json.Unmarshal([]byte(line), &row); err != nil || row["reason"] != "autopilot-action" || row["sub"] != "level" {
			t.Fatalf("row %d = %s", i, line)
		}
	}
	if rows := bnSignedRows(t, f); len(rows) != before {
		t.Fatalf("a hand-over wrote rows: %#v", rows[before:])
	}
}
