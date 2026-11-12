package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// B484: approve validates in Go (validateApproval's order) and refuses with
// no terminal; it never writes a row or changes the record. At a terminal
// the owner's prompt and the change stay with Node.
func TestAutopilotVerbApprove(t *testing.T) {
	f := newAutopilotFixture(t)
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	t.Setenv("CODE_FORGE_NATIVE", "1")
	f.record(t, "r-a", "active", `,"autopilot_approvals":[{"approval_id":"apr-1","key":"budget.coding","status":"active","until":"2026-11-11T12:00:00.000Z"},{"approval_id":"apr-0","key":"budget","status":"restored","until":"2026-11-11T09:00:00.000Z"}]`)
	f.record(t, "r-end", "ended", "")
	until := "2026-11-11T12:00:00Z"
	usage := func(msg string) [3]any { return [3]any{2, "", "autopilot approve: " + msg + "\n"} }
	cases := []struct {
		args []string
		want [3]any
	}{
		{[]string{"--run", "r-a"}, usage("--key is required: a dotted config key path, e.g. review.max_rounds_per_file")},
		{[]string{"--run", "r-a", "--key", "a..b"}, usage("--key must be a dotted path of plain segments (letters, digits, _ and -), e.g. review.max_rounds_per_file")},
		{[]string{"--run", "r-a", "--key", "a.$b"}, usage("--key must be a dotted path of plain segments (letters, digits, _ and -), e.g. review.max_rounds_per_file")},
		{[]string{"--run", "r-a", "--key", "a.__proto__"}, usage("--key must not contain __proto__, constructor or prototype")},
		{[]string{"--run", "r-a", "--key", "proof"}, [3]any{1, "", "autopilot approve: proof cannot be approved: proof.isolation cannot change mid-run (every block of the run is isolated the same way)\n"}},
		{[]string{"--run", "r-a", "--key", "keys.x", "--value", "nope"}, [3]any{1, "", "autopilot approve: keys.x cannot be approved: keys cannot change mid-run (the worker resolves keys once, at start)\n"}},
		{[]string{"--run", "r-a", "--key", "review.max_rounds_per_file"}, usage(`--value is required: a JSON value, e.g. 4, true, "text" or {"a": 1}`)},
		{[]string{"--run", "r-a", "--key", "review.max_rounds_per_file", "--value", "{secret"}, usage(`--value must be JSON, e.g. 4, true, "text" or {"a": 1}`)},
		{[]string{"--run", "r-a", "--key", "review.max_rounds_per_file", "--value", " 4 "}, usage("--until is required: an ISO-8601 time with an offset, e.g. 2026-10-07T07:00:00+02:00")},
		{[]string{"--run", "r-a", "--key", "review.max_rounds_per_file", "--value", "4", "--until", "tomorrow"}, usage("--until must be an ISO-8601 time with an offset (Z or ±hh:mm), e.g. 2026-10-07T07:00:00+02:00")},
		{[]string{"--run", "r-a", "--key", "review.max_rounds_per_file", "--value", "4", "--until", "2026-11-11T09:00:00Z"}, usage("--until is in the past; give a time after now, at most 24 h ahead")},
		{[]string{"--run", "r-a", "--key", "review.max_rounds_per_file", "--value", "4", "--until", "2026-11-12T11:00:00+00:00"}, usage("--until is more than 24 h ahead; a grant lasts at most 24 h")},
		{[]string{"--run", "r-zz", "--key", "review.max_rounds_per_file", "--value", "4"}, usage("--until is required: an ISO-8601 time with an offset, e.g. 2026-10-07T07:00:00+02:00")},
		{[]string{"--run", "r-zz", "--key", "review.max_rounds_per_file", "--value", "4", "--until", until}, [3]any{1, "", "autopilot approve: no run record for r-zz\n"}},
		{[]string{"--run", "r-end", "--key", "review.max_rounds_per_file", "--value", "4", "--until", until}, [3]any{1, "", "autopilot approve: run r-end has ended\n"}},
		{[]string{"--run", "r-a", "--key", "budget", "--value", "{}", "--until", until}, [3]any{1, "", "autopilot approve: an approval for budget.coding is active until 2026-11-11T12:00:00.000Z; it must end first\n"}},
		{[]string{"--run", "r-a", "--key", "budget.coding.x", "--value", "1", "--until", until}, [3]any{1, "", "autopilot approve: an approval for budget.coding is active until 2026-11-11T12:00:00.000Z; it must end first\n"}},
		{[]string{"--run", "r-a", "--key", "review.max_rounds_per_file", "--value", `"text"`, "--until", until}, [3]any{2, "", "autopilot approve: no terminal to confirm; only the owner approves a change, in a terminal (approve has no --yes)\n"}},
	}
	before, _ := os.ReadFile(filepath.Join(f.home, ".code-forge", "runs", "r-a.json"))
	for _, tc := range cases {
		code, out, errText := autopilotCall(t, append([]string{"approve"}, tc.args...)...)
		if code != tc.want[0] || out != tc.want[1] || errText != tc.want[2] {
			t.Fatalf("approve %v = %d %q %q, want %v", tc.args, code, out, errText, tc.want)
		}
	}
	after, _ := os.ReadFile(filepath.Join(f.home, ".code-forge", "runs", "r-a.json"))
	if !bytes.Equal(before, after) {
		t.Fatalf("approve changed the record:\n%s", after)
	}
	if rows := apSignedRows(t, f); len(rows) != 0 {
		t.Fatalf("approve wrote rows: %#v", rows)
	}
	if reasons := apHandoverReasons(t, f); len(reasons) != 0 {
		t.Fatalf("handed over: %v", reasons)
	}
	// at a terminal (and for an active approval Node reads differently) Node runs it
	f.record(t, "r-oddkey", "active", `,"autopilot_approvals":[{"approval_id":"apr-1","status":"active","until":"2026-11-11T12:00:00.000Z"}]`)
	if code, out, _ := autopilotCall(t, "approve", "--run", "r-oddkey", "--key", "review.max_rounds_per_file", "--value", "4", "--until", until); code != 127 || out != "" {
		t.Fatalf("odd approval key = %d %q, want hand-over", code, out)
	}
	autopilotIsTTY = func() bool { return true }
	if code, out, _ := autopilotCall(t, "approve", "--run", "r-a", "--key", "review.max_rounds_per_file", "--value", "4", "--until", until); code != 127 || out != "" {
		t.Fatalf("terminal approve = %d %q, want hand-over", code, out)
	}
	// a terminal does not skip the checks before the prompt
	if code, _, errText := autopilotCall(t, "approve", "--run", "r-end", "--key", "x.y", "--value", "4", "--until", until); code != 1 || errText != "autopilot approve: run r-end has ended\n" {
		t.Fatalf("terminal ended run = %d %q", code, errText)
	}
	if reasons := apHandoverReasons(t, f); len(reasons) != 2 || reasons[0] != "autopilot-shape" || reasons[1] != "autopilot-approve" {
		t.Fatalf("reasons = %v", reasons)
	}
}

// B484: ask answers in Go every refusal Node gives before a delegate
// session: the usage checks, the option and subject checks, an unknown
// scope, and grantFor's refusals (text and --json). A grant that lets the
// question through needs the session, which stays with Node.
func TestAutopilotVerbAsk(t *testing.T) {
	f := newAutopilotFixture(t)
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	t.Setenv("CODE_FORGE_NATIVE", "1")
	active := grantJSON("active", "2026-11-11T12:30:00.000Z", "null", `"https://example.com/doc"`)
	f.record(t, "r-none", "active", "")
	f.record(t, "r-act", "active", active)
	f.record(t, "r-pause", "active", strings.TrimSuffix(active, "}")+`,"paused":{"coding":{}}}`)
	f.record(t, "r-stop", "active", grantJSON("stopped", "2026-11-11T12:30:00.000Z", `"2026-11-11T09:30:00.000Z"`, `"https://example.com/doc"`))
	f.record(t, "r-end", "ended", active)
	f.record(t, "r-old", "active", grantJSON("active", "2026-11-11T09:30:00.000Z", "null", `"https://example.com/doc"`))
	ctx := filepath.Join(t.TempDir(), "ctx.md")
	if err := os.WriteFile(ctx, []byte("context"), 0600); err != nil {
		t.Fatal(err)
	}
	q := []string{"--question", "fix or waive?"}
	ask := func(run string, extra ...string) []string {
		return append(append([]string{"ask", "--run", run}, extra...), q...)
	}
	usage := func(msg string) [3]any { return [3]any{2, "", "autopilot ask: " + msg + "\n"} }
	long := strings.Repeat("a", 201)
	many := strings.Repeat("o,", 20) + "p"
	manyDistinct := ""
	for i := range 21 {
		manyDistinct += string(rune('a'+i)) + ","
	}
	refused := func(run, reason string) [3]any {
		return [3]any{1, "autopilot ask run " + run + ": refused (" + reason + ") — no session; the question goes to the owner\n", ""}
	}
	cases := []struct {
		args []string
		want [3]any
	}{
		{[]string{"ask", "--run", "r-act"}, usage("autopilot ask needs --scope <scope>")},
		{[]string{"ask", "--run", "r-act", "--scope", "  "}, usage("autopilot ask needs --scope <scope>")},
		{[]string{"ask", "--run", "r-act", "--scope", "round:extra"}, usage("autopilot ask needs --question <text>")},
		{[]string{"ask", "--run", "r-act", "--scope", "round:extra", "--question", "\t"}, usage("autopilot ask needs --question <text>")},
		{ask("r-act", "--scope", "round:extra", "--options", " , "), usage("--options needs at least 2 distinct options, e.g. --options waive,fix")},
		{ask("r-act", "--scope", "round:extra", "--context-file", "no-such-b484.md"), usage("--context-file no-such-b484.md cannot be read")},
		{ask("r-act", "--scope", "round:extra", "--options", "a,"+long), usage("delegate: an option is longer than 200 bytes")},
		{ask("r-act", "--scope", "round:extra", "--options", "a, a ,a"), usage("delegate: give at least 2 distinct options")},
		{ask("r-act", "--scope", "round:extra", "--options", "solo"), usage("delegate: give at least 2 distinct options")},
		{ask("r-act", "--scope", "round:extra", "--options", manyDistinct), usage("delegate: at most 20 options")},
		{ask("r-act", "--scope", "round:extra", "--block", "B 1"), usage("delegate: --block must be a block id (letters, digits, . _ -; at most 32 characters)")},
		{ask("r-act", "--scope", "round:extra", "--file", "a/../../x"), usage("delegate: --file must be a repo-relative path")},
		{ask("r-act", "--scope", "round:extra", "--file", "/etc/x"), usage("delegate: --file must be a repo-relative path")},
		{ask("r-act", "--scope", "round:extra", "--file", "a/.."), usage("delegate: --file must be a repo-relative path")},
		{ask("r-act", "--scope", "round:extra", "--finding", "-F1"), usage("delegate: --finding must be a finding id (letters, digits, . _ : -)")},
		{ask("r-act", "--scope", "nope"), usage(`unknown autopilot scope "nope"`)},
		{ask("r-zz", "--scope", "nope"), usage(`unknown autopilot scope "nope"`)},
		{ask("r-zz", "--scope", "round:extra", "--options", many), [3]any{1, "", "autopilot ask: no run record for r-zz\n"}},
		{ask("r-none", "--scope", "waive:nit"), refused("r-none", "no-grant")},
		{ask("r-act", "--scope", "pr:merge"), refused("r-act", "denied")},
		{ask("r-act", "--scope", "model:choose"), refused("r-act", "denied")},
		{ask("r-act", "--scope", "waive:warning", "--context-file", ctx, "--block", "B1", "--file", "a/../", "--finding", "F1:x"), refused("r-act", "not-allowed")},
		{ask("r-pause", "--scope", "round:extra"), refused("r-pause", "paused")},
		{ask("r-pause", "--scope", " waive:nit "), [3]any{127, "", ""}},
		{ask("r-stop", "--scope", "waive:nit"), refused("r-stop", "stopped")},
		{ask("r-end", "--scope", "waive:nit"), refused("r-end", "run-ended")},
		{ask("r-old", "--scope", "waive:nit"), refused("r-old", "expired")},
		{ask("r-old", "--scope", "waive:nit", "--json"), [3]any{1, `{"run":"r-old","scope":"waive:nit","grant_id":"ap-0000abcd","decision_id":null,"answered":false,"acted":false,"to_owner":true,"reason":"expired","decision":null,"confidence":null,"within_scope":null,"escalate":null,"answer_reason":null}` + "\n", ""}},
		{ask("r-none", "--scope", " waive:nit", "--json"), [3]any{1, `{"run":"r-none","scope":" waive:nit","grant_id":null,"decision_id":null,"answered":false,"acted":false,"to_owner":true,"reason":"no-grant","decision":null,"confidence":null,"within_scope":null,"escalate":null,"answer_reason":null}` + "\n", ""}},
		{ask("r-act", "--scope", "round:extra", "--options", "waive,fix"), [3]any{127, "", ""}},
	}
	for _, tc := range cases {
		code, out, errText := autopilotCall(t, tc.args...)
		if tc.want[0] == 127 {
			if code != 127 || out != "" {
				t.Fatalf("%v = %d %q, want hand-over", tc.args, code, out)
			}
			continue
		}
		if code != tc.want[0] || out != tc.want[1] || errText != tc.want[2] {
			t.Fatalf("%v = %d %q %q, want %v", tc.args, code, out, errText, tc.want)
		}
	}
	// the expired grant got its one signed expire row; nothing else was written
	rows := apSignedRows(t, f)
	if len(rows) != 1 || rows[0]["event"] != "autopilot.expire" || rows[0]["grant_id"] != "ap-0000abcd" {
		t.Fatalf("rows: %#v", rows)
	}
	if grant := f.read(t, "r-old")["autopilot"].(map[string]any); grant["status"] != "expired" {
		t.Fatalf("expired grant: %#v", grant)
	}
	if reasons := apHandoverReasons(t, f); len(reasons) != 2 || reasons[0] != "autopilot-ask" || reasons[1] != "autopilot-ask" {
		t.Fatalf("reasons = %v", reasons)
	}
}
