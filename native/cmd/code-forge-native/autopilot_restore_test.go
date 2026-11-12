package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/humanask"
)

// apApproval is an approval entry as approveChange writes it, due at 09:00
// (the fixture clock is 10:00), with extra fields appended.
func apApproval(id, key, extra string) string {
	return `{"approval_id":"` + id + `","key":"` + key + `","new":4,"old":3,"old_absent":false,"prune":0,"until":"2026-11-11T09:00:00.000Z","approved_at":"2026-11-11T08:00:00.000Z","status":"active","ended_at":null` + extra + `}`
}

func apWriteConfig(t *testing.T, f runFixture, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.workspace, ".code-forge.yml"), []byte(text), 0644); err != nil {
		t.Fatal(err)
	}
}

func apRecordBytes(t *testing.T, f runFixture, id string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "runs", id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func apApprovalAt(t *testing.T, f runFixture, id string, i int) map[string]any {
	t.Helper()
	return f.read(t, id)["autopilot_approvals"].([]any)[i].(map[string]any)
}

func apNoHandover(t *testing.T, f runFixture) {
	t.Helper()
	if reasons := apHandoverReasons(t, f); len(reasons) != 0 {
		t.Fatalf("handed over: %v", reasons)
	}
}

// B490: a due approval whose key was changed by hand ends in Go: the signed
// autopilot.restore_skipped row, the record's status and ended_at, the
// line before the subcommand's own output; the config is not touched.
func TestAutopilotRestoreSkippedInGo(t *testing.T) {
	f := newAutopilotFixture(t)
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	t.Setenv("CODE_FORGE_NATIVE", "1")
	config := "review:\n  max_rounds_per_file: 5 # by hand\n"
	apWriteConfig(t, f, config)
	f.record(t, "r-s", "active", `,"autopilot_approvals":[`+apApproval("apr-1", "review.max_rounds_per_file", "")+`]`)
	code, out, errText := autopilotCall(t, "status", "--run", "r-s")
	if code != 0 || out != "autopilot run r-s: no grant\n" || errText != "autopilot: approval apr-1 expired but its key was changed by hand; left for the owner\n" {
		t.Fatalf("status = %d %q %q", code, out, errText)
	}
	a := apApprovalAt(t, f, "r-s", 0)
	if a["status"] != "restore_skipped" || a["ended_at"] != "2026-11-11T10:00:00.000Z" || a["finish_pending"] != nil {
		t.Fatalf("approval = %#v", a)
	}
	rows := apSignedRows(t, f)
	if len(rows) != 1 {
		t.Fatalf("rows = %#v", rows)
	}
	want := map[string]any{"approval_id": "apr-1", "key": "review.max_rounds_per_file", "until": "2026-11-11T09:00:00.000Z", "event": "autopilot.restore_skipped", "reason": "changed-by-hand", "ts": "2026-11-11T10:00:00.000Z"}
	for k, v := range want {
		if rows[0][k] != v {
			t.Fatalf("row %s = %#v, want %#v (%#v)", k, rows[0][k], v, rows[0])
		}
	}
	if data, _ := os.ReadFile(filepath.Join(f.workspace, ".code-forge.yml")); string(data) != config {
		t.Fatalf("config changed: %q", data)
	}
	// the next command finds it ended
	if code, _, errText := autopilotCall(t, "stop", "--run", "r-s"); code != 1 || errText != "autopilot stop: run r-s has no autopilot grant\n" {
		t.Fatalf("stop = %d %q", code, errText)
	}
	if len(apSignedRows(t, f)) != 1 {
		t.Fatal("a second row")
	}
	apNoHandover(t, f)
}

// B490: the steps Node reports as failed (a key that is not text or has a
// prototype segment, no config, a config that is not a mapping) print
// their WARN lines; nothing is written and the subcommand still runs.
func TestAutopilotRestoreWarnsInGo(t *testing.T) {
	f := newAutopilotFixture(t)
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	t.Setenv("CODE_FORGE_NATIVE", "1")
	approvals := `,"autopilot_approvals":[` + apApproval("apr-u", "a.__proto__", "") + `,{"approval_id":"apr-n","status":"active","until":"2026-11-11T09:00:00.000Z"},` +
		apApproval("apr-c", "review.max_rounds_per_file", "") + `,{"approval_id":"apr-later","key":"x","status":"active","until":"2026-11-11T12:00:00.000Z"}]`
	f.record(t, "r-w", "active", approvals)
	before := apRecordBytes(t, f, "r-w")
	warn := "autopilot: WARN approval apr-u could not be restored yet (unsafe key)\nautopilot: WARN approval apr-n could not be restored yet (unsafe key)\n"
	cases := []struct{ config, reason string }{
		{"", "no .code-forge.yml in the workspace"},
		{"- a\n", ".code-forge.yml could not be parsed"},
		{"# only a comment\n", ".code-forge.yml could not be parsed"},
	}
	for _, tc := range cases {
		if tc.config != "" {
			apWriteConfig(t, f, tc.config)
		}
		code, out, errText := autopilotCall(t, "status", "--run", "r-w")
		if code != 0 || out != "autopilot run r-w: no grant\n" || errText != warn+"autopilot: WARN approval apr-c could not be restored yet ("+tc.reason+")\n" {
			t.Fatalf("%q: status = %d %q %q", tc.config, code, out, errText)
		}
	}
	// approve prints them first, then its own refusal (an active approval
	// with no key stays with Node there: overlaps() throws on it)
	f.record(t, "r-w2", "active", strings.Replace(approvals, `{"approval_id":"apr-n","status":"active","until":"2026-11-11T09:00:00.000Z"},`, "", 1))
	warn = "autopilot: WARN approval apr-u could not be restored yet (unsafe key)\n"
	code, out, errText := autopilotCall(t, "approve", "--run", "r-w2", "--key", "x", "--value", "1", "--until", "2026-11-11T12:00:00Z")
	if code != 1 || out != "" || errText != warn+"autopilot: WARN approval apr-c could not be restored yet (.code-forge.yml could not be parsed)\nautopilot approve: an approval for x is active until 2026-11-11T12:00:00.000Z; it must end first\n" {
		t.Fatalf("approve = %d %q %q", code, out, errText)
	}
	if !bytes.Equal(before, apRecordBytes(t, f, "r-w")) {
		t.Fatal("record changed")
	}
	if rows := apSignedRows(t, f); len(rows) != 0 {
		t.Fatalf("rows = %#v", rows)
	}
	apNoHandover(t, f)
}

// B490: an approval whose signed row is missing (finish_pending) gets that
// stored row, with its own ts, and no config read; an ended run whose file
// already holds the old value is restored with no write and no reload.
func TestAutopilotRestoreRowsInGo(t *testing.T) {
	f := newAutopilotFixture(t)
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	t.Setenv("CODE_FORGE_NATIVE", "1")
	pending := `,"restoring":true,"finish_pending":{"status":"restored","row":{"approval_id":"apr-p","key":"review.max_rounds_per_file","until":"2026-11-11T09:00:00.000Z","event":"autopilot.restore","restored":3,"old_absent":false,"reloaded":true,"new_hash":"abc","ts":"2026-11-11T09:30:00.000Z"}}`
	f.record(t, "r-p", "active", `,"autopilot_approvals":[`+apApproval("apr-p", "review.max_rounds_per_file", pending)+`]`)
	code, out, errText := autopilotCall(t, "status", "--run", "r-p")
	if code != 0 || out != "autopilot run r-p: no grant\n" || errText != "autopilot: approval apr-p expired; the old value is back\n" {
		t.Fatalf("pending = %d %q %q", code, out, errText)
	}
	a := apApprovalAt(t, f, "r-p", 0)
	if a["status"] != "restored" || a["ended_at"] != "2026-11-11T09:30:00.000Z" || a["finish_pending"] != nil {
		t.Fatalf("approval = %#v", a)
	}
	rows := apSignedRows(t, f)
	if len(rows) != 1 || rows[0]["event"] != "autopilot.restore" || rows[0]["new_hash"] != "abc" || rows[0]["reloaded"] != true || rows[0]["ts"] != "2026-11-11T09:30:00.000Z" {
		t.Fatalf("rows = %#v", rows)
	}

	config := "review:\n  max_rounds_per_file: 3\n"
	apWriteConfig(t, f, config)
	absent := strings.Replace(apApproval("apr-a", "budget.extra", `,"restoring":true`), `"old":3,"old_absent":false`, `"old":null,"old_absent":true`, 1)
	f.record(t, "r-e", "ended", `,"autopilot_approvals":[`+apApproval("apr-o", "review.max_rounds_per_file", `,"restoring":true`)+`,`+absent+`]`)
	code, out, errText = autopilotCall(t, "status", "--run", "r-e")
	if code != 0 || out != "autopilot run r-e: no grant\n" || errText != "autopilot: approval apr-o expired; the old value is back\nautopilot: approval apr-a expired; the old value is back\n" {
		t.Fatalf("ended = %d %q %q", code, out, errText)
	}
	rows = apSignedRows(t, f)[1:]
	if len(rows) != 2 || rows[0]["restored"] != float64(3) || rows[0]["old_absent"] != false || rows[1]["restored"] != nil || rows[1]["old_absent"] != true {
		t.Fatalf("rows = %#v", rows)
	}
	for _, row := range rows {
		if row["event"] != "autopilot.restore" || row["reloaded"] != false || row["new_hash"] != nil || row["ts"] != "2026-11-11T10:00:00.000Z" {
			t.Fatalf("row = %#v", row)
		}
		if _, has := row["new_hash"]; !has {
			t.Fatalf("row without new_hash: %#v", row)
		}
	}
	for i := range 2 {
		if a := apApprovalAt(t, f, "r-e", i); a["status"] != "restored" || a["ended_at"] != "2026-11-11T10:00:00.000Z" {
			t.Fatalf("approval %d = %#v", i, a)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(f.workspace, ".code-forge.yml")); string(data) != config {
		t.Fatalf("config changed: %q", data)
	}
	apNoHandover(t, f)
}

// B490: a signed row that cannot be written leaves finish_pending (the
// restore-incomplete WARN); the next check writes that same row.
func TestAutopilotRestoreRowRetryInGo(t *testing.T) {
	f := newAutopilotFixture(t)
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	t.Setenv("CODE_FORGE_NATIVE", "1")
	apWriteConfig(t, f, "review:\n  max_rounds_per_file: 5\n")
	f.record(t, "r-r", "active", `,"autopilot_approvals":[`+apApproval("apr-1", "review.max_rounds_per_file", "")+`]`)
	ledgerDir := filepath.Join(f.home, ".code-forge", "ledger")
	if err := os.WriteFile(ledgerDir, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	code, out, errText := autopilotCall(t, "status", "--run", "r-r")
	if code != 0 || out != "autopilot run r-r: no grant\n" || errText != "autopilot: WARN approval apr-1: its signed row could not be written yet (restore-incomplete); the next check writes it\n" {
		t.Fatalf("blocked = %d %q %q", code, out, errText)
	}
	a := apApprovalAt(t, f, "r-r", 0)
	pending, _ := a["finish_pending"].(map[string]any)
	if a["status"] != "active" || pending["status"] != "restore_skipped" || pending["row"].(map[string]any)["ts"] != "2026-11-11T10:00:00.000Z" {
		t.Fatalf("approval = %#v", a)
	}
	if err := os.Remove(ledgerDir); err != nil {
		t.Fatal(err)
	}
	autopilotNow = func() time.Time { return autopilotTestNow.Add(time.Minute) }
	code, _, errText = autopilotCall(t, "status", "--run", "r-r")
	if code != 0 || errText != "autopilot: approval apr-1 expired but its key was changed by hand; left for the owner\n" {
		t.Fatalf("retry = %d %q", code, errText)
	}
	rows := apSignedRows(t, f)
	if len(rows) != 1 || rows[0]["event"] != "autopilot.restore_skipped" || rows[0]["ts"] != "2026-11-11T10:00:00.000Z" {
		t.Fatalf("rows = %#v", rows)
	}
	if a := apApprovalAt(t, f, "r-r", 0); a["status"] != "restore_skipped" || a["ended_at"] != "2026-11-11T10:00:00.000Z" || a["finish_pending"] != nil {
		t.Fatalf("approval = %#v", a)
	}
	apNoHandover(t, f)
}

// B490: the approvals lock held by another verb is Node's StateError
// (exit 1, no lines); nothing is written.
func TestAutopilotRestoreLockedInGo(t *testing.T) {
	f := newAutopilotFixture(t)
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	t.Setenv("CODE_FORGE_NATIVE", "1")
	before := autopilotApprovalsLockTimeout
	autopilotApprovalsLockTimeout = 60 * time.Millisecond
	t.Cleanup(func() { autopilotApprovalsLockTimeout = before })
	apWriteConfig(t, f, "review:\n  max_rounds_per_file: 5\n")
	f.record(t, "r-l", "active", `,"autopilot_approvals":[`+apApproval("apr-1", "review.max_rounds_per_file", "")+`]`)
	lock := filepath.Join(f.home, ".code-forge", "runs", "r-l.approvals.lock")
	if err := os.Mkdir(lock, 0700); err != nil {
		t.Fatal(err)
	}
	code, out, errText := autopilotCall(t, "status", "--run", "r-l")
	if code != 1 || out != "" || errText != "autopilot status: run r-l's approvals are locked ("+lock+"); remove it if no forge verb is running\n" {
		t.Fatalf("locked = %d %q %q", code, out, errText)
	}
	if a := apApprovalAt(t, f, "r-l", 0); a["status"] != "active" {
		t.Fatalf("approval = %#v", a)
	}
	if rows := apSignedRows(t, f); len(rows) != 0 {
		t.Fatalf("rows = %#v", rows)
	}
	apNoHandover(t, f)
}

// B490: a restore that needs the YAML write or the run reload stays with
// Node, with its own reason, before Go writes anything (even for another
// approval of the same check that Go could end).
func TestAutopilotRestoreHandsOverInGo(t *testing.T) {
	f := newAutopilotFixture(t)
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	t.Setenv("CODE_FORGE_NATIVE", "1")
	apWriteConfig(t, f, "review:\n  max_rounds_per_file: 4\nbudget:\n  coding: 9\n")
	skip := apApproval("apr-s", "budget.coding", "")
	f.record(t, "r-reload", "active", `,"autopilot_approvals":[`+skip+`,`+apApproval("apr-1", "review.max_rounds_per_file", "")+`]`)
	f.record(t, "r-write", "ended", `,"autopilot_approvals":[`+apApproval("apr-1", "review.max_rounds_per_file", "")+`]`)
	before := map[string][]byte{"r-reload": apRecordBytes(t, f, "r-reload"), "r-write": apRecordBytes(t, f, "r-write")}
	for _, args := range [][]string{{"status", "--run", "r-reload"}, {"stop", "--run", "r-write"}} {
		if code, out, errText := autopilotCall(t, args...); code != 127 || out != "" || strings.Contains(errText, "autopilot:") {
			t.Fatalf("%v = %d %q %q, want hand-over", args, code, out, errText)
		}
	}
	bom := filepath.Join(t.TempDir(), "bom")
	if err := os.MkdirAll(bom, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bom, ".code-forge.yml"), []byte("\xef\xbb\xbfreview: {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	f2 := f
	f2.workspace = bom
	f2.record(t, "r-bom", "active", `,"autopilot_approvals":[`+apApproval("apr-1", "review.max_rounds_per_file", "")+`]`)
	if code, _, _ := autopilotCall(t, "status", "--run", "r-bom"); code != 127 {
		t.Fatalf("bom = %d", code)
	}
	if err := os.WriteFile(filepath.Join(bom, ".code-forge.yml"), []byte("review:\n  max_rounds_per_file: .inf\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := autopilotCall(t, "status", "--run", "r-bom"); code != 127 {
		t.Fatalf("inf = %d", code)
	}
	want := []string{"autopilot-restore-reload", "autopilot-restore-write", "autopilot-restore-config", "autopilot-restore-config"}
	if got := apHandoverReasons(t, f); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("reasons = %v, want %v", got, want)
	}
	for id, data := range before {
		if !bytes.Equal(data, apRecordBytes(t, f, id)) {
			t.Fatalf("%s changed", id)
		}
	}
	if rows := apSignedRows(t, f); len(rows) != 0 {
		t.Fatalf("rows = %#v", rows)
	}
}

// B490: approve at a terminal runs the owner's prompt in Go when Node would
// refuse after it without a write: the prompt text, the cancel answer, the
// refusals after a yes and the --until check at the fresh clock. A change
// of .code-forge.yml stays with Node (asked there), found before or after
// the prompt.
func TestAutopilotApproveAtTerminalInGo(t *testing.T) {
	f := newAutopilotFixture(t)
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	t.Setenv("CODE_FORGE_NATIVE", "1")
	autopilotIsTTY = func() bool { return true }
	autopilotPromptable = func() bool { return true }
	f.record(t, "r-a", "active", `,"autopilot_approvals":[{"approval_id":"apr-0","key":"budget","status":"restored","until":"2026-11-11T09:00:00.000Z"}]`)
	before := apRecordBytes(t, f, "r-a")
	until := "2026-11-11T13:00:00+01:00"
	var asked []string
	answer := humanask.Cancelled
	var answerErr error
	during := func() {}
	autopilotConfirm = func(message string) (humanask.Result, error) {
		asked = append(asked, message)
		during()
		return answer, answerErr
	}
	approve := func(value string) (int, string, string) {
		return autopilotCall(t, "approve", "--run", "r-a", "--key", "review.max_rounds_per_file", "--value", value, "--until", until)
	}
	// no config: the owner is asked, and a no ends it
	code, out, errText := approve(`{"b":1,"a":[true,"x"]}`)
	if code != 1 || out != "cancelled — nothing changed\n" || errText != "" {
		t.Fatalf("cancel = %d %q %q", code, out, errText)
	}
	if len(asked) != 1 || asked[0] != `Approve review.max_rounds_per_file = {"b":1,"a":[true,"x"]} for run r-a until 2026-11-11T12:00:00.000Z? The old value comes back then.` {
		t.Fatalf("asked = %q", asked)
	}
	answer = humanask.Confirmed
	code, out, errText = approve("4")
	if code != 1 || out != "" || errText != "autopilot approve: no .code-forge.yml in the workspace\n" {
		t.Fatalf("no config = %d %q %q", code, out, errText)
	}
	apWriteConfig(t, f, "review:\n  max_rounds_per_file: 4.0\n")
	code, _, errText = approve("4")
	if code != 1 || errText != "autopilot approve: review.max_rounds_per_file already has that value in .code-forge.yml; nothing to approve\n" {
		t.Fatalf("no change = %d %q", code, errText)
	}
	// the prompt waited past --until
	during = func() { autopilotNow = func() time.Time { return autopilotTestNow.Add(3 * time.Hour) } }
	code, _, errText = approve("4")
	if code != 2 || errText != "autopilot approve: --until is in the past; give a time after now, at most 24 h ahead\n" {
		t.Fatalf("late until = %d %q", code, errText)
	}
	autopilotNow = func() time.Time { return autopilotTestNow }
	// a failed prompt
	during, answerErr = func() {}, errors.New("read /dev/tty: input/output error")
	code, out, errText = approve("4")
	if code != 1 || out != "" || errText != "autopilot approve: read /dev/tty: input/output error\n" {
		t.Fatalf("prompt error = %d %q %q", code, out, errText)
	}
	answerErr = nil
	// the file changed while the owner was asked: Node asks again and writes it
	during = func() { apWriteConfig(t, f, "review:\n  max_rounds_per_file: 2\n") }
	if code, out, _ = approve("4"); code != 127 || out != "" {
		t.Fatalf("changed during prompt = %d %q", code, out)
	}
	during = func() {}
	asked = nil
	// a change of the file, and a value Go cannot hold, go to Node before any prompt
	if code, _, _ = approve("5"); code != 127 {
		t.Fatalf("write = %d", code)
	}
	if code, _, _ = approve(`"\ud800"`); code != 127 {
		t.Fatalf("surrogate = %d", code)
	}
	// clack measures wide text by display width: non-ASCII prompts stay on Node
	if code, _, _ = approve(`"caf\u00e9"`); code != 127 {
		t.Fatalf("non-ASCII = %d", code)
	}
	if len(asked) != 0 {
		t.Fatalf("asked = %q", asked)
	}
	want := []string{"autopilot-approve-changed", "autopilot-approve-write", "autopilot-approve-value", "autopilot-approve-value"}
	if got := apHandoverReasons(t, f); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("reasons = %v, want %v", got, want)
	}
	if !bytes.Equal(before, apRecordBytes(t, f, "r-a")) {
		t.Fatal("record changed")
	}
	if rows := apSignedRows(t, f); len(rows) != 0 {
		t.Fatalf("rows = %#v", rows)
	}
	if data, _ := os.ReadFile(filepath.Join(f.workspace, ".code-forge.yml")); string(data) != "review:\n  max_rounds_per_file: 2\n" {
		t.Fatalf("config = %q", data)
	}
}
