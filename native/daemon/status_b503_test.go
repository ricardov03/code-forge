//go:build unix

package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// fakeStatusNode puts a `node` on PATH that answers every companion line with
// a marker, so a test sees whether the daemon still starts Node's companion.
func fakeStatusNode(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\nwhile read line; do echo '{\"from\":\"node\"}'; done\n"
	if err := os.WriteFile(filepath.Join(bin, "node"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func statusServer(t *testing.T, record object, rows []ledger.Row) *server {
	t.Helper()
	home := t.TempDir()
	workspace := t.TempDir()
	record["workspace"] = workspace
	s := &server{run: "r-test", home: home, workspace: workspace, record: record, rows: rows}
	t.Cleanup(s.closeStatus)
	return s
}

// B503: Node's companion catches statusOf's throw on a null row or a null
// block entry and answers {"ok":false,"error":"status-unreadable"}; Go
// answers the same without starting Node.
func TestStatusB503UnreadableInGo(t *testing.T) {
	fakeStatusNode(t)
	for _, tc := range []struct {
		name   string
		record object
		rows   []ledger.Row
	}{
		{"null row", object{"run_id": "r-test", "project": "p"}, []ledger.Row{nil}},
		{"null block entry", object{"run_id": "r-test", "project": "p", "blocks": map[string]any{"B1": nil}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := statusServer(t, tc.record, tc.rows)
			got := s.status()
			want := object{"ok": false, "error": "status-unreadable"}
			if !jsonEqual(t, got, want) {
				t.Fatalf("status = %v, want %v", got, want)
			}
			if s.statusProcess != nil {
				t.Fatal("status started Node's companion")
			}
		})
	}
}

// B503: a spend that overflows to Infinity is written as null by Node's
// JSON.stringify; the daemon then adds the gate details (timeout, flaky).
func TestStatusB503InfiniteSpendInGo(t *testing.T) {
	fakeStatusNode(t)
	record := object{"run_id": "r-test", "project": "p", "status": "running", "blocks": map[string]any{"B1": map[string]any{"status": "open", "level": "L1"}}}
	rows := []ledger.Row{
		object{"run": "r-test", "event": "session", "usd": 1e308},
		object{"run": "r-test", "event": "session", "usd": 1e308},
		object{"run": "r-test", "block": "B1", "event": "gate.red", "gate": "test", "reason": "timeout"},
		object{"run": "r-test", "block": "B1", "event": "gate.done", "all_ok": false, "flaky": []any{"b.test.mjs", "a.test.mjs"}},
	}
	s := statusServer(t, record, rows)
	got := s.status()
	if s.statusProcess != nil {
		t.Fatal("status started Node's companion")
	}
	want := `{"runs":[{"run_id":"r-test","status":"running","mode":"plan","spent_usd":null,"unknown_usd_sessions":0,"budget_usd":null,` +
		`"blocks":[{"id":"B1","state":"open","level":"L1","model":null,"gate":{"result":"timeout","name":"test"},"findings":0}],` +
		`"more":0,"closed":0,"stopped":0,"flaky":["a.test.mjs","b.test.mjs"]}]}`
	var wantObj object
	if err := json.Unmarshal([]byte(want), &wantObj); err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(t, got, wantObj) {
		data, _ := json.Marshal(got)
		t.Fatalf("status = %s\nwant     %s", data, want)
	}
}

// A block id outside printable ASCII still needs Node's ICU order: the
// companion keeps answering it.
func TestStatusB503UnorderedBlockKeepsCompanion(t *testing.T) {
	fakeStatusNode(t)
	record := object{"run_id": "r-test", "project": "p", "blocks": map[string]any{"Bé": map[string]any{"status": "open"}}}
	rows := []ledger.Row{}
	s := statusServer(t, record, rows)
	got := s.status()
	if !jsonEqual(t, got, object{"from": "node"}) {
		t.Fatalf("status = %v, want the companion's answer", got)
	}
}

func jsonEqual(t *testing.T, a, b any) bool {
	t.Helper()
	x, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	y, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var xa, ya any
	_ = json.Unmarshal(x, &xa)
	_ = json.Unmarshal(y, &ya)
	xb, _ := json.Marshal(xa)
	yb, _ := json.Marshal(ya)
	return string(xb) == string(yb)
}
