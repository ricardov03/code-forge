package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/sign"
)

// roundFixture: run ra holds an active grant allowing round:extra and the
// delegate's decision rows (signed with the run key unless unsigned).
func roundFixture(t *testing.T) runFixture {
	t.Helper()
	f := newAutopilotFixture(t)
	blocks := `,"blocks":{"B2":{"status":"open"},"B3":{"status":"open"}}`
	f.record(t, "ra", "active", blocks+grantJSON("active", "2026-11-11T12:30:00.000Z", "null", "null"))
	f.record(t, "rn", "active", blocks+strings.Replace(grantJSON("active", "2026-11-11T12:30:00.000Z", "null", "null"), `"round:extra",`, "", 1))
	f.record(t, "rp", "active", blocks+strings.Replace(grantJSON("active", "2026-11-11T12:30:00.000Z", "null", "null"), `"link":null`, `"link":null,"paused":{"coding":{"at":"2026-11-11T09:00:00.000Z"}}`, 1))
	f.record(t, "rg", "active", blocks)
	dec := func(id string, change map[string]any, signed bool) string {
		row := map[string]any{"run": "ra", "event": "autopilot.decision", "decision_id": id, "grant_id": "ap-0000abcd", "scope": "round:extra", "acted": true, "decision": "allow", "subject": map[string]any{"block": "B2", "file": "src/a.mjs"}, "ts": "2026-11-11T09:55:00.000Z"}
		for k, v := range change {
			row[k] = v
		}
		if signed {
			var err error
			if row, err = sign.SignRow(row, f.key); err != nil {
				t.Fatal(err)
			}
		}
		data, _ := json.Marshal(row)
		return string(data)
	}
	writeLedger(t, f,
		dec("d-1", nil, true),
		dec("d-2", map[string]any{"acted": false}, true),
		dec("d-3", map[string]any{"decision": "deny"}, true),
		dec("d-4", map[string]any{"scope": "waive:nit"}, true),
		dec("d-5", map[string]any{"subject": map[string]any{"block": "B2", "file": "src/b.mjs"}}, true),
		dec("d-6", map[string]any{"ts": "2026-11-11T08:00:00.000Z"}, true),
		dec("d-7", nil, false),
		dec("d-8", map[string]any{"decision": 5}, true),
		dec("d-12", map[string]any{"scope": nil}, true),
		dec("d-13", map[string]any{"decision": map[string]any{"b": 1, "a": 2}}, true),
	)
	return f
}

// B478: `autopilot round` runs in Go: Node's checks in Node's order, its
// refusal texts and its signed autopilot.extra_round row.
func TestAutopilotRoundInGo(t *testing.T) {
	f := roundFixture(t)
	code, out, errText := autopilotCall(t, "round", "--run", "ra", "--block", "B2", "--file", "src/a.mjs", "--decision", "d-1")
	if code != 0 || out != "autopilot round run ra: one extra fix round for src/a.mjs in block B2 (grant ap-0000abcd)\n" || errText != "" {
		t.Fatalf("round = %d %q %q", code, out, errText)
	}
	rows := bnSignedRows(t, f)
	last := rows[len(rows)-1]
	data, _ := json.Marshal(last)
	if last["event"] != "autopilot.extra_round" || last["run"] != "ra" || last["block"] != "B2" || last["file"] != "src/a.mjs" || last["grant_id"] != "ap-0000abcd" || last["decision_id"] != "d-1" || last["ts"] != "2026-11-11T10:00:00.000Z" {
		t.Fatalf("round row: %s", data)
	}
	refused := " the question goes to the owner (autopilot ask, then --decision <id>)\n"
	cases := []struct {
		args []string
		code int
		err  string
	}{
		{[]string{"--run", "ra", "--block", "B2", "--file", "src/a.mjs", "--decision", "d-1"}, 1, "autopilot round: refused — src/a.mjs already had its extra round under grant ap-0000abcd\n"},
		{[]string{"--run", "ra", "--file", "src/a.mjs", "--decision", "d-1"}, 2, "autopilot round: autopilot round needs --block\n"},
		{[]string{"--run", "ra", "--block", "\u00a0\ufeff", "--file", "src/a.mjs", "--decision", "d-1"}, 2, "autopilot round: autopilot round needs --block\n"},
		{[]string{"--run", "ra", "--block", "B2", "--file", "src/a.mjs", "--decision", "x y"}, 2, "autopilot round: autopilot round needs --decision <id> (the delegate's acted decision from autopilot ask)\n"},
		{[]string{"--run", "ra", "--block", "B2", "--file", "/abs", "--decision", "d-1"}, 2, "autopilot round: --file must be a repo-relative path\n"},
		{[]string{"--run", "ra", "--block", "B2", "--file", "../x", "--decision", "d-1"}, 2, "autopilot round: --file must be a repo-relative path\n"},
		{[]string{"--run", "rzz", "--file", "src/a.mjs", "--decision", "d-1"}, 2, "autopilot round: autopilot round needs --block\n"},
		{[]string{"--run", "rzz", "--block", "B2", "--file", "src/a.mjs", "--decision", "d-1"}, 1, "autopilot round: no run record for rzz\n"},
		{[]string{"--run", "ra", "--block", "B9", "--file", "src/a.mjs", "--decision", "d-1"}, 1, "autopilot round: block B9 is not in run ra\n"},
		{[]string{"--run", "ra", "--block", "B2", "--file", "src/a.mjs", "--decision", "d-2"}, 1, "autopilot round: refused — decision d-2 did not act (it went to the owner);" + refused},
		{[]string{"--run", "ra", "--block", "B2", "--file", "src/a.mjs", "--decision", "d-3"}, 1, "autopilot round: refused — decision d-3 decided \"deny\", not \"allow\";" + refused},
		{[]string{"--run", "ra", "--block", "B2", "--file", "src/a.mjs", "--decision", "d-4"}, 1, "autopilot round: refused — decision d-4 is for waive:nit, not round:extra;" + refused},
		{[]string{"--run", "ra", "--block", "B2", "--file", "src/a.mjs", "--decision", "d-5"}, 1, "autopilot round: refused — decision d-5 is about another file;" + refused},
		{[]string{"--run", "ra", "--block", "B3", "--file", "src/a.mjs", "--decision", "d-1"}, 1, "autopilot round: refused — decision d-1 is about another block;" + refused},
		{[]string{"--run", "ra", "--block", "B2", "--file", "src/a.mjs", "--decision", "d-6"}, 1, "autopilot round: refused — decision d-6 is older than 30 min (or not yet made);" + refused},
		{[]string{"--run", "ra", "--block", "B2", "--file", "src/a.mjs", "--decision", "d-7"}, 1, "autopilot round: refused — decision d-7 has no valid MAC;" + refused},
		{[]string{"--run", "ra", "--block", "B2", "--file", "src/a.mjs", "--decision", "d-8"}, 1, "autopilot round: refused — decision d-8 decided 5, not \"allow\";" + refused},
		{[]string{"--run", "ra", "--block", "B2", "--file", "src/a.mjs", "--decision", "d-12"}, 1, "autopilot round: refused — decision d-12 is for null, not round:extra;" + refused},
		{[]string{"--run", "ra", "--block", "B2", "--file", "src/a.mjs", "--decision", "d-zz"}, 1, "autopilot round: refused — no autopilot.decision row d-zz in this run;" + refused},
		{[]string{"--run", "rn", "--block", "B2", "--file", "src/a.mjs", "--decision", "d-1"}, 1, "autopilot round: refused — the grant refuses (not-allowed); the question goes to the owner\n"},
		{[]string{"--run", "rp", "--block", "B2", "--file", "src/a.mjs", "--decision", "d-1"}, 1, "autopilot round: refused — the grant refuses (paused); the question goes to the owner\n"},
		{[]string{"--run", "rg", "--block", "B2", "--file", "src/a.mjs", "--decision", "d-1"}, 1, "autopilot round: refused — the grant refuses (no-grant); the question goes to the owner\n"},
	}
	for _, tc := range cases {
		code, out, errText := autopilotCall(t, append([]string{"round"}, tc.args...)...)
		if code != tc.code || out != "" || errText != tc.err {
			t.Fatalf("%v = %d %q %q", tc.args, code, out, errText)
		}
	}
	if n := len(bnSignedRows(t, f)); n != len(rows) {
		t.Fatalf("a refusal wrote rows: %d, want %d", n, len(rows))
	}

	// the grant is over: the one expire row, then the refusal
	f.record(t, "rx", "active", `,"blocks":{"B2":{}}`+grantJSON("active", "2026-11-11T09:30:00.000Z", "null", "null"))
	code, _, errText = autopilotCall(t, "round", "--run", "rx", "--block", "B2", "--file", "src/a.mjs", "--decision", "d-1")
	if code != 1 || errText != "autopilot round: refused — the grant refuses (expired); the question goes to the owner\n" {
		t.Fatalf("expired = %d %q", code, errText)
	}
	if rows := bnSignedRows(t, f); rows[len(rows)-1]["event"] != "autopilot.expire" || rows[len(rows)-1]["run"] != "rx" {
		t.Fatalf("expire row: %#v", rows[len(rows)-1])
	}
}

// B478: what Go would read differently keeps the round on Node, before any
// write: a block name on Object.prototype, a path posix.normalize rewrites,
// an object decision (its key order).
func TestAutopilotRoundHandsOver(t *testing.T) {
	f := roundFixture(t)
	before := len(bnSignedRows(t, f))
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	t.Setenv("CODE_FORGE_NATIVE", "1")
	cases := [][]string{
		{"--run", "ra", "--block", "constructor", "--file", "src/a.mjs", "--decision", "d-1"},
		{"--run", "ra", "--block", "B2", "--file", "src/./a.mjs", "--decision", "d-1"},
		{"--run", "ra", "--block", "B2", "--file", "src/a.mjs/", "--decision", "d-1"},
		{"--run", "ra", "--block", "B2", "--file", "src//a.mjs", "--decision", "d-1"},
		{"--run", "ra", "--block", "B2", "--file", "src/a.mjs", "--decision", "d-13"},
	}
	for _, args := range cases {
		if code, out, _ := autopilotCall(t, append([]string{"round"}, args...)...); code != 127 || out != "" {
			t.Fatalf("%v: hand-over exit = %d stdout %q, want 127", args, code, out)
		}
	}
	data, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "native", "handovers.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != len(cases) {
		t.Fatalf("handover rows = %d, want %d", len(lines), len(cases))
	}
	for i, line := range lines {
		var row map[string]string
		if err := json.Unmarshal([]byte(line), &row); err != nil || row["reason"] != "autopilot-action" || row["sub"] != "round" {
			t.Fatalf("row %d = %s", i, line)
		}
	}
	if rows := bnSignedRows(t, f); len(rows) != before {
		t.Fatalf("a hand-over wrote rows: %#v", rows[before:])
	}
}
