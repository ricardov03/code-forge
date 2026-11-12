package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// handoverReasons reads the hand-over log rows the test wrote.
func apHandoverReasons(t *testing.T, f runFixture) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "native", "handovers.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		var row map[string]string
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		out = append(out, row["reason"])
	}
	return out
}

// B484: checkApprovals restores only an approval whose window is over. A
// record whose active approvals are not due (until later, or no until:
// Date.parse gives NaN) runs every subcommand in Go and is left as it is.
func TestAutopilotVerbQuietApprovals(t *testing.T) {
	f := newAutopilotFixture(t)
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	t.Setenv("CODE_FORGE_NATIVE", "1")
	later := `,"autopilot_approvals":[{"approval_id":"apr-1","key":"review.max_rounds_per_file","status":"active","until":"2026-11-11T12:00:00.000Z"},{"approval_id":"apr-0","status":"restored","until":"2026-11-10T12:00:00.000Z"},"x",7]`
	f.record(t, "r-later", "active", later)
	f.record(t, "r-nountil", "active", `,"autopilot_approvals":[{"approval_id":"x","status":"active"},{"approval_id":"y","status":"active","until":null}]`)
	f.record(t, "r-notarray", "active", `,"autopilot_approvals":{"status":"active","until":"2026-11-11T09:00:00.000Z"}`)
	f.record(t, "r-link", "active", grantJSON("active", "2026-11-11T12:30:00.000Z", "null", `"https://example.com/doc"`)+later)
	before := map[string][]byte{}
	for _, id := range []string{"r-later", "r-nountil", "r-notarray"} {
		data, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "runs", id+".json"))
		if err != nil {
			t.Fatal(err)
		}
		before[id] = data
	}
	for _, id := range []string{"r-later", "r-nountil", "r-notarray"} {
		code, out, errText := autopilotCall(t, "status", "--run", id)
		if code != 0 || out != "autopilot run "+id+": no grant\n" || errText != "" {
			t.Fatalf("%s status = %d %q %q", id, code, out, errText)
		}
		code, _, errText = autopilotCall(t, "stop", "--run", id)
		if code != 1 || errText != "autopilot stop: run "+id+" has no autopilot grant\n" {
			t.Fatalf("%s stop = %d %q", id, code, errText)
		}
		data, _ := os.ReadFile(filepath.Join(f.home, ".code-forge", "runs", id+".json"))
		if !bytes.Equal(data, before[id]) {
			t.Fatalf("%s record changed:\n%s", id, data)
		}
	}
	code, out, errText := autopilotCall(t, "stop", "--run", "r-link")
	if code != 0 || out != "autopilot run r-link: grant ap-0000abcd stopped\n  link      https://example.com/doc\n" || errText != "" {
		t.Fatalf("stop with approvals = %d %q %q", code, out, errText)
	}
	approvals := f.read(t, "r-link")["autopilot_approvals"].([]any)
	if a := approvals[0].(map[string]any); a["status"] != "active" || len(approvals) != 4 {
		t.Fatalf("stop touched the approvals: %#v", approvals)
	}
	if rows := apSignedRows(t, f); len(rows) != 1 || rows[0]["event"] != "autopilot.stop" {
		t.Fatalf("rows: %#v", rows)
	}
	if reasons := apHandoverReasons(t, f); len(reasons) != 0 {
		t.Fatalf("handed over: %v", reasons)
	}
}

// B484: an approval due now (the restore edits .code-forge.yml and reloads
// the run) and an entry Node reads differently keep their hand-over.
func TestAutopilotVerbApprovalRestoreHandsOver(t *testing.T) {
	f := newAutopilotFixture(t)
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	t.Setenv("CODE_FORGE_NATIVE", "1")
	f.record(t, "r-due", "active", `,"autopilot_approvals":[{"approval_id":"x","status":"active","until":"2026-11-11T10:00:00.000Z"}]`)
	f.record(t, "r-null", "active", `,"autopilot_approvals":[null]`)
	f.record(t, "r-odd", "active", `,"autopilot_approvals":[{"approval_id":"x","status":"active","until":"2026-11-11 12:00"}]`)
	cases := [][]string{{"status", "--run", "r-due"}, {"stop", "--run", "r-due"}, {"status", "--run", "r-null"}, {"status", "--run", "r-odd"}}
	for _, args := range cases {
		if code, out, _ := autopilotCall(t, args...); code != 127 || out != "" {
			t.Fatalf("%v = %d %q, want hand-over", args, code, out)
		}
	}
	want := []string{"autopilot-restore", "autopilot-restore", "autopilot-shape", "autopilot-shape"}
	got := apHandoverReasons(t, f)
	if len(got) != len(want) {
		t.Fatalf("reasons = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reasons = %v, want %v", got, want)
		}
	}
	if rows := apSignedRows(t, f); len(rows) != 0 {
		t.Fatalf("a hand-over wrote rows: %#v", rows)
	}
}
