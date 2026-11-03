//go:build unix

package daemon

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// Exercise the decoded companion payload, before Go fills in gate details.
func statusPayload(t *testing.T, gate string) object {
	t.Helper()
	var result object
	if err := json.Unmarshal([]byte(`{"runs":[{"run_id":"r-test","blocks":[{"id":"B1","gate":{"result":"`+gate+`","name":"test"}},{"id":"B2","gate":{"result":"fail","name":"lint"}}]}]}`), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestStatusGateTimeout(t *testing.T) {
	red := object{"run": "r-test", "block": "B1", "event": "gate.red", "reason": "timeout"}
	flag := object{"run": "r-test", "block": "B1", "event": "gate.red", "timed_out": true}
	done := object{"run": "r-test", "block": "B1", "event": "gate.done", "all_ok": false}
	pass := object{"run": "r-test", "block": "B1", "event": "gate.done", "all_ok": true}
	fail := object{"run": "r-test", "block": "B1", "event": "gate.red", "reason": "exit"}
	for _, tc := range []struct {
		name, gate, want string
		rows             []ledger.Row
	}{
		{"pending legacy timeout", "fail", "timeout", []ledger.Row{red}},
		{"completed legacy timeout", "fail", "timeout", []ledger.Row{red, done}},
		{"timeout flag", "fail", "timeout", []ledger.Row{flag, done}},
		{"later red clears timeout", "fail", "fail", []ledger.Row{red, done, fail, done}},
		{"later red before done", "fail", "fail", []ledger.Row{red, fail, done}},
		{"successful done clears pending", "fail", "fail", []ledger.Row{red, pass, done}},
		{"done without pending red", "fail", "fail", []ledger.Row{red, done, done}},
		{"other run cannot clear timeout", "fail", "timeout", []ledger.Row{red, object{"run": "r-other", "block": "B1", "event": "gate.done", "all_ok": true}, done}},
		{"other run cannot set timeout", "fail", "fail", []ledger.Row{object{"run": "r-other", "block": "B1", "event": "gate.red", "reason": "timeout"}, done}},
		{"missing block ignored", "fail", "fail", []ledger.Row{object{"run": "r-test", "event": "gate.red", "reason": "timeout"}, done}},
		{"strict timeout flag", "fail", "fail", []ledger.Row{object{"run": "r-test", "block": "B1", "event": "gate.red", "timed_out": "true"}, done}},
		{"strict success flag", "fail", "timeout", []ledger.Row{red, object{"run": "r-test", "block": "B1", "event": "gate.done", "all_ok": "true"}}},
		{"pass is preserved", "pass", "pass", []ledger.Row{red}},
		{"none is preserved", "none", "none", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &server{run: "r-test", rows: tc.rows}
			result := statusPayload(t, tc.gate)
			s.statusGateDetails(result)
			run := mapping(result["runs"].([]any)[0])
			blocks := run["blocks"].([]any)
			gate := mapping(mapping(blocks[0])["gate"])
			if gate["result"] != tc.want || gate["name"] != "test" {
				t.Fatalf("gate = %v, want %s (test)", gate, tc.want)
			}
			if got := mapping(mapping(blocks[1])["gate"])["result"]; got != "fail" {
				t.Fatalf("unrelated block gate = %v, want fail", got)
			}
			if _, ok := run["flaky"]; ok {
				t.Fatal("empty flaky list must be omitted")
			}
		})
	}
}

func TestStatusFlakyFiles(t *testing.T) {
	s := &server{run: "r-test", rows: []ledger.Row{
		object{"run": "r-test", "block": "B2", "event": "gate.done", "all_ok": true, "flaky": []any{"z.test.mjs", "a.test.mjs", "a.test.mjs", 42, nil}},
		object{"run": "r-test", "event": "gate.done", "all_ok": false, "flaky": []any{"😀.test.mjs", "\ue000.test.mjs", ""}},
		object{"run": "r-test", "block": "B2", "event": "gate.done", "all_ok": true},
		object{"run": "r-test", "event": "gate.done", "flaky": "invalid.test.mjs"},
		object{"run": "r-other", "event": "gate.done", "flaky": []any{"other.test.mjs"}},
		object{"run": "r-test", "event": "gate.red", "flaky": []any{"red.test.mjs"}},
	}}
	result := statusPayload(t, "fail")
	s.statusGateDetails(result)
	run := mapping(result["runs"].([]any)[0])
	want := []string{"", "a.test.mjs", "z.test.mjs", "😀.test.mjs", "\ue000.test.mjs"}
	if !reflect.DeepEqual(run["flaky"], want) {
		t.Fatalf("flaky = %v, want %v", run["flaky"], want)
	}
	// Refreshes can replace/truncate the ledger; never retain a previous list.
	s.rows = nil
	s.statusGateDetails(result)
	if _, ok := run["flaky"]; ok {
		t.Fatal("flaky must be omitted when the ledger has no recovered files")
	}
}
