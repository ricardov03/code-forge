//go:build unix

package daemon

import (
	"encoding/json"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B576: the daemon's status never starts Node. Each want is Node's
// statusOf(record, rows, {gateDetails: false}) line, written by a scratch
// script over the same record and rows (no masks: the output holds no path).
func TestStatusB576NonASCIIBlockIDsInGo(t *testing.T) {
	for _, tc := range []struct {
		name   string
		record object
		rows   []ledger.Row
		want   string
	}{
		{
			"one listed non-ascii",
			object{"run_id": "r-test", "project": "p", "status": "active", "blocks": map[string]any{"Bé": map[string]any{"status": "open", "level": "L1"}}},
			[]ledger.Row{
				object{"run": "r-test", "block": "Bé", "event": "gate.red", "gate": "test"},
				object{"run": "r-test", "block": "Bé", "event": "session", "role": "coder", "model": "m", "provider": "p", "usd": 0.5},
			},
			`{"runs":[{"run_id":"r-test","status":"active","mode":"plan","spent_usd":0.5,"unknown_usd_sessions":0,"budget_usd":null,"blocks":[{"id":"Bé","state":"open","level":"L1","model":"p/m","gate":{"result":"fail","name":"test"},"findings":0}],"more":0,"closed":0,"stopped":0}]}`,
		},
		{
			"non-ascii closed and stopped",
			object{"run_id": "r-test", "project": "p", "blocks": map[string]any{"B10": map[string]any{"status": "open"}, "Bé": map[string]any{"status": "closed"}, "B2": map[string]any{"status": "open", "attempt": 2.0}, "Bü😀": map[string]any{"status": "stopped"}, "B ": map[string]any{"status": "closed"}}},
			[]ledger.Row{},
			`{"runs":[{"run_id":"r-test","status":null,"mode":"plan","spent_usd":0,"unknown_usd_sessions":0,"budget_usd":null,"blocks":[{"id":"B2","state":"attempt-2","level":null,"model":null,"gate":{"result":"none","name":null},"findings":0},{"id":"B10","state":"open","level":null,"model":null,"gate":{"result":"none","name":null},"findings":0}],"more":0,"closed":2,"stopped":1}]}`,
		},
		{
			"one listed emoji control",
			object{"run_id": "r-test", "project": "p", "blocks": map[string]any{"B\t😀": map[string]any{"status": "awaiting_human_check"}, "Ä": map[string]any{"status": "closed"}}},
			[]ledger.Row{},
			`{"runs":[{"run_id":"r-test","status":null,"mode":"plan","spent_usd":0,"unknown_usd_sessions":0,"budget_usd":null,"blocks":[{"id":"B\t😀","state":"awaiting_human_check","level":null,"model":null,"gate":{"result":"none","name":null},"findings":0}],"more":0,"closed":1,"stopped":0,"awaiting_human_check":["B\t😀"]}]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeStatusNode(t)
			s := statusServer(t, tc.record, tc.rows)
			got := s.status()
			var want object
			if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
				t.Fatal(err)
			}
			if !jsonEqual(t, got, want) {
				data, _ := json.Marshal(got)
				t.Fatalf("status = %s\nwant     %s", data, tc.want)
			}
		})
	}
}

// B576: two listed blocks with an id outside printable ASCII need Node's ICU
// order (Node writes [Bé, Bf]); Go has no collator, so the daemon answers
// status-unreadable, as it did with no Node, and starts nothing. The status
// client then computes the run's status itself.
func TestStatusB576UnorderedBlockNoNode(t *testing.T) {
	fakeStatusNode(t)
	record := object{"run_id": "r-test", "project": "p", "blocks": map[string]any{"Bé": map[string]any{"status": "open"}, "Bf": map[string]any{"status": "open"}}}
	s := statusServer(t, record, []ledger.Row{})
	got := s.status()
	if !jsonEqual(t, got, object{"ok": false, "error": "status-unreadable"}) {
		t.Fatalf("status = %v, want status-unreadable", got)
	}
}
