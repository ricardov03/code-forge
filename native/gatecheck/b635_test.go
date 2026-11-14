package gatecheck

import (
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/sign"
)

// B635: an autopilot waiver the owner reverted (a later human
// review.unwaived row, written by autopilot revert) is no waiver at the gate.
func TestB635UnwaivedAutopilotWaiverIsGone(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	signed := func(raw ...Row) []Row {
		out := []Row{}
		for _, r := range raw {
			r["run"], r["block"] = "r1", "B1"
			s, err := sign.SignRow(r, key)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		return out
	}
	rows := signed(
		Row{"event": "review.result", "file": "src/a.go", "status": "open", "open": []any{map[string]any{"id": "F1", "severity": "warning"}}},
		Row{"event": "review.waived", "file": "src/a.go", "finding": "F1", "severity": "warning", "reason": "style", "by": "autopilot", "decision_id": "ad-1", "grant_id": "ap-1", "ts": "2026-11-14T08:00:00.000Z"},
	)
	g := newGate(Input{Run: "r1", Block: "B1", Rows: rows, Key: key})
	if by, why := g.waiverOf("src/a.go", "F1", "warning", nil); by == "" && why == "" {
		t.Fatalf("before the revert the autopilot waiver is not named: %q %q", by, why)
	}
	rows = append(rows, signed(Row{"event": "review.unwaived", "file": "src/a.go", "finding": "F1", "reason": "autopilot revert dg-1", "by": "human"})...)
	g = newGate(Input{Run: "r1", Block: "B1", Rows: rows, Key: key})
	if by, why := g.waiverOf("src/a.go", "F1", "warning", nil); by != "" || why != "" {
		t.Fatalf("after the revert = %q %q, want no waiver", by, why)
	}
}
