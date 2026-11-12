package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/humanask"
	"github.com/ricardov03/code-forge/native/sign"
)

// autopilotCall runs `code-forge autopilot <args>` in process at a fixed
// clock, with no terminal and a fixed grant id.
func autopilotCall(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	stdout, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	beforeOut, beforeErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdout, stderr
	code, runErr := run(append([]string{"autopilot"}, args...))
	os.Stdout, os.Stderr = beforeOut, beforeErr
	_ = stdout.Close()
	_ = stderr.Close()
	out, _ := os.ReadFile(stdout.Name())
	errText, _ := os.ReadFile(stderr.Name())
	if runErr != nil {
		errText = append(errText, []byte("handover: "+runErr.Error())...)
	}
	return code, string(out), string(errText)
}

// apSignedRows returns the autopilot.* ledger rows, each verified with the
// run key; the hand-written spend rows in a fixture are not signed.
func apSignedRows(t *testing.T, f runFixture) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "ledger", "unit.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	out := []map[string]any{}
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		var row map[string]any
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		if event, _ := row["event"].(string); !strings.HasPrefix(event, "autopilot.") {
			continue
		}
		if verified, err := sign.VerifyRow(row, f.key); err != nil || !verified.OK {
			t.Fatalf("row signature: %s %#v %v", line, verified, err)
		}
		out = append(out, row)
	}
	return out
}

var autopilotTestNow = time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)

func newAutopilotFixture(t *testing.T) runFixture {
	t.Helper()
	f := newRunFixture(t)
	nowBefore, ttyBefore, idBefore := autopilotNow, autopilotIsTTY, autopilotGrantID
	promptableBefore, confirmBefore := autopilotPromptable, autopilotConfirm
	autopilotNow = func() time.Time { return autopilotTestNow }
	autopilotIsTTY = func() bool { return false }
	autopilotGrantID = func() (string, error) { return "ap-1234abcd", nil }
	autopilotPromptable = func() bool { return false }
	autopilotConfirm = func(string) (humanask.Result, error) { t.Fatal("unexpected prompt"); return humanask.NoTTY, nil }
	t.Cleanup(func() {
		autopilotNow, autopilotIsTTY, autopilotGrantID = nowBefore, ttyBefore, idBefore
		autopilotPromptable, autopilotConfirm = promptableBefore, confirmBefore
	})
	return f
}

const autopilotLinkGrant = `,"autopilot":{"grant_id":"ap-0000abcd","status":"%s","until":"%s","delegate":"L2","scopes":["round:extra","waive:nit"],"deny":["model:choose"],"caps":{"review":5,"coding":10.125},"stop_at":0.9,"started_at":"2026-11-11T09:00:00.000Z","stopped_at":%s,"expired_at":null,"link":%s}`

func grantJSON(status, until, stopped, link string) string {
	parts := strings.SplitN(autopilotLinkGrant, "%s", 5)
	return parts[0] + status + parts[1] + until + parts[2] + stopped + parts[3] + link + parts[4]
}

func writeLedger(t *testing.T, f runFixture, rows ...string) {
	t.Helper()
	dir := filepath.Join(f.home, ".code-forge", "ledger")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "unit.jsonl"), []byte(strings.Join(rows, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestAutopilotVerbHelpAndUsage(t *testing.T) {
	newAutopilotFixture(t)
	code, out, errText := autopilotCall(t, "--help")
	if code != 0 || out != autopilotUsage || errText != "" {
		t.Fatalf("--help = %d %q %q", code, out, errText)
	}
	code, out, _ = autopilotCall(t, "status", "-h")
	if code != 0 || out != "usage: code-forge autopilot status --run <id> [--json]\n" {
		t.Fatalf("status -h = %d %q", code, out)
	}
	code, out, _ = autopilotCall(t, "ask", "--help")
	if code != 0 || !strings.HasPrefix(out, "usage: code-forge autopilot ask --run <id>") || !strings.HasSuffix(out, "(options for an action: waive,fix · allow,deny · L1,L2)\n") {
		t.Fatalf("ask --help = %d %q", code, out)
	}
	cases := []struct {
		args []string
		err  string
	}{
		{nil, autopilotUsage},
		{[]string{"bogus"}, autopilotUsage},
		{[]string{"status"}, "autopilot status: autopilot status needs --run <id>\n"},
		{[]string{"status", "--run"}, "autopilot status: --run needs a value\n"},
		{[]string{"status", "--run="}, "autopilot status: --run needs a value\n"},
		{[]string{"status", "--run", "r-a", "extra"}, "autopilot status: unexpected argument \"extra\"\n"},
		{[]string{"status", "--run", "r-a", "--bogus"}, "autopilot status: unknown flag \"--bogus\"\n"},
		{[]string{"status", "--run", "r-a", "--json=1"}, "autopilot status: --json takes no value\n"},
		{[]string{"stop", "--run", "r-a", "--run", "r-b"}, "autopilot stop: --run given more than once\n"},
		{[]string{"waive", "--run", "r-a", "--nope"}, "autopilot waive: unknown flag \"--nope\"\n"},
		{[]string{"approve", "--run", "r-a", "--yes"}, autopilotApproveNoYes + "\n"},
		{[]string{"approve", "--yes=no"}, autopilotApproveNoYes + "\n"},
	}
	for _, tc := range cases {
		code, out, errText := autopilotCall(t, tc.args...)
		if code != 2 || out != "" || errText != tc.err {
			t.Fatalf("%v = %d %q %q, want 2 and %q", tc.args, code, out, errText, tc.err)
		}
	}
}

func TestAutopilotVerbStatus(t *testing.T) {
	f := newAutopilotFixture(t)
	f.record(t, "r-none", "active", "")
	linked := grantJSON("active", "2026-11-11T12:30:00.000Z", "null", `"https://example.com/doc"`)
	f.record(t, "r-link", "active", strings.TrimSuffix(linked, "}")+`,"paused":{"review":{}}}`)
	f.record(t, "r-old", "active", grantJSON("active", "2026-11-11T09:30:00.000Z", "null", `"https://example.com/doc"`))
	f.record(t, "r-end", "ended", grantJSON("active", "2026-11-11T12:30:00.000Z", "null", `"https://example.com/doc"`))
	writeLedger(t, f,
		`{"run":"r-link","event":"session","role":"coder","usd":1.23456,"ts":"2026-11-11T09:30:00.000Z"}`,
		`{"run":"r-link","event":"session","role":"reviewer","provider":"anthropic","level":"L2","tokens_in":1000,"tokens_out":500,"ts":"2026-11-11T09:30:00.000Z"}`,
		`{"run":"r-link","event":"session","role":"coder","usd":7,"ts":"2026-11-11T08:00:00.000Z"}`,
		`{"run":"r-link","event":"session","role":"judge","usd":0.5,"source":"shadow","ts":"2026-11-11T09:30:00.000Z"}`,
		`{"run":"r-link","event":"session.background","role":"facts","cost_usd":0.25,"ts":"2026-11-11T09:30:00.000Z"}`,
		`{"run":"r-other","event":"session","role":"coder","usd":3,"ts":"2026-11-11T09:30:00.000Z"}`,
	)

	code, out, errText := autopilotCall(t, "status", "--run", "r-none")
	if code != 0 || out != "autopilot run r-none: no grant\n" || errText != "" {
		t.Fatalf("no grant = %d %q %q", code, out, errText)
	}
	code, out, _ = autopilotCall(t, "status", "--run", "r-none", "--json")
	var none map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &none) != nil || none["state"] != "none" || none["grant_id"] != nil || len(none["fixed_deny"].([]any)) != 9 {
		t.Fatalf("no grant json = %d %q", code, out)
	}

	code, out, errText = autopilotCall(t, "status", "--run", "r-link")
	want := "autopilot run r-link: active · 2h 30m left (until 2026-11-11T12:30:00.000Z)\n" +
		"  grant     ap-0000abcd\n  delegate  L2\n  allow     round:extra, waive:nit\n" +
		"  deny      model:choose, waive:critical, waive:proof, reviews:skip, limits:change, plan:approve, design:approve, pr:merge, destructive, budget:raise\n" +
		"  budget    coding 10.13 USD, review 5.00 USD · stop at 90%\n" +
		"  spend     coding 1.23 of 10.13 USD, review 0.27 of 5.00 USD · paused review\n" +
		"  link      https://example.com/doc\n"
	if code != 0 || out != want || errText != "" {
		t.Fatalf("status with link = %d\n%s\nwant\n%s\n%q", code, out, want, errText)
	}
	code, out, _ = autopilotCall(t, "status", "--run", "r-link", "--json")
	if code != 0 || !strings.Contains(out, "\"time_left_s\": 9000,") || !strings.Contains(out, "\"spend\": {\n    \"coding\": 1.2346,\n    \"review\": 0.2725\n  },") || !strings.HasSuffix(out, "\"expired_at\": null\n}\n") {
		t.Fatalf("status json = %d %s", code, out)
	}

	code, out, _ = autopilotCall(t, "status", "--run", "r-end")
	if code != 0 || !strings.HasPrefix(out, "autopilot run r-end: the run has ended; the grant no longer acts (was until 2026-11-11T12:30:00.000Z)\n") {
		t.Fatalf("ended = %d %q", code, out)
	}
	// a window that is over gets its one signed expire row, then reads expired
	for range 2 {
		code, out, _ = autopilotCall(t, "status", "--run", "r-old")
		if code != 0 || !strings.HasPrefix(out, "autopilot run r-old: expired at 2026-11-11T09:30:00.000Z\n") {
			t.Fatalf("expired = %d %q", code, out)
		}
	}
	rows := apSignedRows(t, f)
	if len(rows) != 1 || rows[0]["event"] != "autopilot.expire" || rows[0]["grant_id"] != "ap-0000abcd" || rows[0]["ts"] != "2026-11-11T10:00:00.000Z" {
		t.Fatalf("expire rows: %#v", rows)
	}
	grant := f.read(t, "r-old")["autopilot"].(map[string]any)
	if grant["status"] != "expired" || grant["expired_at"] != "2026-11-11T10:00:00.000Z" {
		t.Fatalf("expired grant: %#v", grant)
	}

	code, _, errText = autopilotCall(t, "status", "--run", "r-zz")
	if code != 1 || errText != "autopilot status: no run record for r-zz\n" {
		t.Fatalf("no run = %d %q", code, errText)
	}
}

func TestAutopilotVerbStop(t *testing.T) {
	f := newAutopilotFixture(t)
	link := `"https://example.com/doc"`
	f.record(t, "r-link", "active", grantJSON("active", "2026-11-11T12:30:00.000Z", "null", link))
	f.record(t, "r-old", "active", grantJSON("active", "2026-11-11T09:30:00.000Z", "null", link))
	f.record(t, "r-end", "ended", grantJSON("active", "2026-11-11T12:30:00.000Z", "null", link))
	f.record(t, "r-none", "active", "")
	f.record(t, "r-nolink", "active", grantJSON("stopped", "2026-11-11T12:30:00.000Z", `"2026-11-11T09:10:00.000Z"`, "null"))

	code, out, errText := autopilotCall(t, "stop", "--run", "r-link")
	if code != 0 || out != "autopilot run r-link: grant ap-0000abcd stopped\n  link      https://example.com/doc\n" || errText != "" {
		t.Fatalf("stop = %d %q %q", code, out, errText)
	}
	grant := f.read(t, "r-link")["autopilot"].(map[string]any)
	if grant["status"] != "stopped" || grant["stopped_at"] != "2026-11-11T10:00:00.000Z" {
		t.Fatalf("stopped grant: %#v", grant)
	}
	refusals := []struct{ run, err string }{
		{"r-link", "autopilot stop: run r-link's autopilot grant was already stopped at 2026-11-11T10:00:00.000Z\n"},
		{"r-old", "autopilot stop: run r-old's autopilot grant already expired at 2026-11-11T09:30:00.000Z\n"},
		{"r-old", "autopilot stop: run r-old's autopilot grant already expired at 2026-11-11T09:30:00.000Z\n"},
		{"r-end", "autopilot stop: run r-end has ended\n"},
		{"r-none", "autopilot stop: run r-none has no autopilot grant\n"},
		{"r-nolink", "autopilot stop: run r-nolink's autopilot grant was already stopped at 2026-11-11T09:10:00.000Z\n"},
		{"r-zz", "autopilot stop: no run record for r-zz\n"},
	}
	for _, tc := range refusals {
		code, out, errText := autopilotCall(t, "stop", "--run", tc.run)
		if code != 1 || out != "" || errText != tc.err {
			t.Fatalf("stop %s = %d %q %q, want %q", tc.run, code, out, errText, tc.err)
		}
	}
	rows := apSignedRows(t, f)
	if len(rows) != 2 || rows[0]["event"] != "autopilot.stop" || rows[0]["run"] != "r-link" || rows[1]["event"] != "autopilot.expire" || rows[1]["run"] != "r-old" {
		t.Fatalf("rows: %#v", rows)
	}
}

func TestAutopilotVerbStart(t *testing.T) {
	f := newAutopilotFixture(t)
	f.record(t, "r-a", "active", "")
	f.record(t, "r-x", "ended", "")
	until := "2026-11-11T15:30:00+00:00"
	base := []string{"start", "--run", "r-a", "--until", until, "--delegate", "L2", "--allow", "round:extra"}
	usage := []struct {
		args []string
		err  string
	}{
		{[]string{"start", "--run", "r-a"}, "--until is required: an ISO-8601 time with an offset, e.g. 2026-10-07T07:00:00+02:00"},
		{[]string{"start", "--run", "r-a", "--until", "2026-02-30T00:00:00Z"}, "--until must be an ISO-8601 time with an offset (Z or ±hh:mm), e.g. 2026-10-07T07:00:00+02:00"},
		{[]string{"start", "--run", "r-a", "--until", "2026-11-11T12:00:00"}, "--until must be an ISO-8601 time with an offset (Z or ±hh:mm), e.g. 2026-10-07T07:00:00+02:00"},
		{[]string{"start", "--run", "r-a", "--until", "2026-11-11T09:00:00Z"}, "--until is in the past; give a time after now, at most 24 h ahead"},
		{[]string{"start", "--run", "r-a", "--until", "2026-11-12T10:00:01Z"}, "--until is more than 24 h ahead; a grant lasts at most 24 h"},
		{[]string{"start", "--run", "r-a", "--until", until, "--delegate", "L4"}, "--delegate must be L2 or L3"},
		{[]string{"start", "--run", "r-a", "--until", until, "--delegate", "L2"}, "--allow is required: one or more of waive:warning, waive:nit, round:extra, model:choose, close:routine"},
		{[]string{"start", "--run", "r-a", "--until", until, "--delegate", "L2", "--allow", "waive:critical"}, "scope waive:critical is on the fixed deny list and can never be allowed (waive a critical finding)"},
		{[]string{"start", "--run", "r-a", "--until", until, "--delegate", "L2", "--allow", "foo"}, "unknown scope \"foo\" in --allow; allowable scopes: waive:warning, waive:nit, round:extra, model:choose, close:routine"},
		{append(base, "--deny", " , "), "--deny needs one or more scopes"},
		{append(base, "--deny", "zzz"), "unknown scope \"zzz\" in --deny; allowable scopes: waive:warning, waive:nit, round:extra, model:choose, close:routine"},
		{append(base, "--deny", "round:extra"), "scope round:extra is in both --allow and --deny; name it in one of them"},
		{append(base, "--budget", ","), "--budget needs <category>=<usd>, e.g. review=5,coding=20"},
		{append(base, "--budget", "x=1"), "unknown budget category \"x\"; categories: coding, review"},
		{append(base, "--budget", "coding=1,coding=2"), "budget category coding given more than once"},
		{append(base, "--budget", "coding=0"), "budget for coding must be a number of USD above 0, e.g. coding=5"},
		{append(base, "--stop-at", "1.5"), "--stop-at must be a number above 0 and at most 1, e.g. 0.9"},
		{[]string{"start", "--run", "r-zz", "--until", "x"}, "--until must be an ISO-8601 time with an offset (Z or ±hh:mm), e.g. 2026-10-07T07:00:00+02:00"},
	}
	for _, tc := range usage {
		code, out, errText := autopilotCall(t, tc.args...)
		if code != 2 || out != "" || errText != "autopilot start: "+tc.err+"\n" {
			t.Fatalf("%v = %d %q %q, want %q", tc.args, code, out, errText, tc.err)
		}
	}
	code, _, errText := autopilotCall(t, "start", "--run", "r-x", "--until", until, "--delegate", "L2", "--allow", "round:extra", "--yes")
	if code != 1 || errText != "autopilot start: run r-x has ended\n" {
		t.Fatalf("ended = %d %q", code, errText)
	}
	code, _, errText = autopilotCall(t, "start", "--run", "r-zz", "--until", until, "--delegate", "L2", "--allow", "round:extra")
	if code != 1 || errText != "autopilot start: no run record for r-zz\n" {
		t.Fatalf("no run = %d %q", code, errText)
	}
	code, out, errText := autopilotCall(t, base...)
	if code != 2 || out != "" || errText != "autopilot start: no terminal to confirm; the owner starts autopilot — pass --yes to start it without a prompt\n" {
		t.Fatalf("no terminal = %d %q %q", code, out, errText)
	}
	if rows := apSignedRows(t, f); len(rows) != 0 {
		t.Fatalf("a refusal wrote rows: %#v", rows)
	}

	code, out, errText = autopilotCall(t, "start", "--run", "r-a", "--until", until, "--delegate", "L2", "--allow", "round:extra, waive:nit,round:extra", "--deny", "waive:proof,model:choose", "--budget", "review=5.125, coding=20", "--stop-at", "0.755", "--yes")
	want := "autopilot run r-a: grant ap-1234abcd active until 2026-11-11T15:30:00.000Z (5h 30m)\n  delegate L2 · allow round:extra, waive:nit · budget coding 20.00 USD, review 5.13 USD · stop at 76%\n"
	if code != 0 || out != want || errText != "" {
		t.Fatalf("start = %d %q %q", code, out, errText)
	}
	data, _ := os.ReadFile(filepath.Join(f.home, ".code-forge", "runs", "r-a.json"))
	wantGrant := `"autopilot": {
    "grant_id": "ap-1234abcd",
    "status": "active",
    "until": "2026-11-11T15:30:00.000Z",
    "delegate": "L2",
    "scopes": [
      "round:extra",
      "waive:nit"
    ],
    "deny": [
      "model:choose"
    ],
    "caps": {
      "review": 5.125,
      "coding": 20
    },
    "stop_at": 0.755,
    "started_at": "2026-11-11T10:00:00.000Z",
    "stopped_at": null,
    "expired_at": null,
    "link": null
  }
}
`
	if !bytes.HasSuffix(data, []byte(wantGrant)) {
		t.Fatalf("record:\n%s", data)
	}
	rows := apSignedRows(t, f)
	if len(rows) != 1 || rows[0]["event"] != "autopilot.grant" || rows[0]["grant_id"] != "ap-1234abcd" || rows[0]["stop_at"] != 0.755 || rows[0]["ts"] != "2026-11-11T10:00:00.000Z" {
		t.Fatalf("grant row: %#v", rows)
	}
	code, _, errText = autopilotCall(t, "start", "--run", "r-a", "--until", until, "--delegate", "L3", "--allow", "close:routine", "--yes")
	if code != 1 || errText != "autopilot start: run r-a already has an active autopilot grant until 2026-11-11T15:30:00.000Z; stop it first with code-forge autopilot stop --run r-a\n" {
		t.Fatalf("second grant = %d %q", code, errText)
	}
}

func TestAutopilotVerbHandsOver(t *testing.T) {
	f := newAutopilotFixture(t)
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	t.Setenv("CODE_FORGE_NATIVE", "1")
	f.record(t, "r-a", "active", "")
	f.record(t, "r-nolink", "active", grantJSON("active", "2026-11-11T12:30:00.000Z", "null", "null"))
	f.record(t, "r-appr", "active", `,"autopilot_approvals":[{"approval_id":"x","status":"active","until":"2026-11-11T09:00:00.000Z"}]`)
	f.record(t, "r-shape", "active", `,"autopilot":{"grant_id":"g","status":"weird"}`)
	if err := os.WriteFile(filepath.Join(f.home, ".code-forge", "runs", "r-bad.json"), []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	f.record(t, "r-ledger", "active", grantJSON("active", "2026-11-11T12:30:00.000Z", "null", `"https://example.com/doc"`))
	writeLedger(t, f, `{"run":"r-ledger","event":"session","role":"coder","usd":1,"ts":"2026-11-11 09:30"}`)
	cases := []struct {
		args   []string
		reason string
	}{
		{[]string{"ask", "--run", "r-nolink", "--scope", "round:extra", "--question", "q"}, "autopilot-ask"},
		{[]string{"waive", "--run", "r-a"}, "autopilot-action"},
		{[]string{"status", "--run", "r-appr"}, "autopilot-restore"},
		{[]string{"start", "--run", "r-shape", "--yes"}, "autopilot-shape"},
		{[]string{"stop", "--run", "r-bad"}, "autopilot-record"},
		{[]string{"status", "--run", "r-ledger"}, "autopilot-ledger"},
	}
	for _, tc := range cases {
		code, out, _ := autopilotCall(t, tc.args...)
		if code != 127 || out != "" {
			t.Fatalf("%v: hand-over exit = %d stdout %q, want 127", tc.args, code, out)
		}
	}
	autopilotIsTTY = func() bool { return true }
	cases = append(cases, struct {
		args   []string
		reason string
	}{[]string{"start", "--run", "r-a"}, "autopilot-prompt"})
	if code, _, _ := autopilotCall(t, "start", "--run", "r-a"); code != 127 {
		t.Fatalf("prompt hand-over exit = %d", code)
	}
	data, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "native", "handovers.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	if len(lines) != len(cases) {
		t.Fatalf("handover rows = %d, want %d", len(lines), len(cases))
	}
	for i, line := range lines {
		var row map[string]string
		if err := json.Unmarshal(line, &row); err != nil || row["verb"] != "autopilot" || row["reason"] != cases[i].reason {
			t.Fatalf("row %d = %s, want reason %s", i, line, cases[i].reason)
		}
	}
	if rows := apSignedRows(t, f); len(rows) != 0 {
		t.Fatalf("a hand-over wrote rows: %#v", rows)
	}
	if grant := f.read(t, "r-nolink")["autopilot"].(map[string]any); grant["status"] != "active" {
		t.Fatalf("hand-over changed the record: %#v", grant)
	}
}
