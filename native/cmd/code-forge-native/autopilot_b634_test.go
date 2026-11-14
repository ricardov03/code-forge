package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/humanask"
)

// B634 (issue #37 D): autopilot start --until-done [--deadline] [--blocks]
// and autopilot extend --until|--deadline. Temp HOME fixtures, fixed clock.

// b634SetBlocks rewrites the run record's blocks map (the block states).
func b634SetBlocks(t *testing.T, f runFixture, id, blocks string) {
	t.Helper()
	path := filepath.Join(f.home, ".code-forge", "runs", id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	var b map[string]any
	if err := json.Unmarshal([]byte(blocks), &b); err != nil {
		t.Fatal(err)
	}
	record["blocks"] = b
	out, _ := json.MarshalIndent(record, "", "  ")
	if err := os.WriteFile(path, append(out, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

func b634At(t *testing.T, at time.Time) {
	t.Helper()
	autopilotNow = func() time.Time { return at }
}

func TestB634StartUntilDoneUsage(t *testing.T) {
	f := newAutopilotFixture(t)
	f.record(t, "r-a", "active", "")
	tail := []string{"--delegate", "L2", "--allow", "round:extra", "--yes"}
	for _, tc := range []struct {
		args []string
		err  string
	}{
		{[]string{"--until", "2026-11-11T15:30:00Z", "--until-done"}, "give --until or --until-done, not both"},
		{[]string{"--deadline", "2026-11-11T15:30:00Z"}, "--deadline goes with --until-done; a grant with --until already ends at that time"},
		{[]string{"--until", "2026-11-11T15:30:00Z", "--blocks", "B1"}, "--blocks goes with --until-done"},
		{[]string{"--until-done", "--blocks", " , "}, "--blocks needs one or more block ids, e.g. B1,B2"},
		{[]string{"--until-done", "--deadline", "2026-11-11T09:00:00Z"}, "--deadline is in the past; give a time after now, at most 24 h ahead"},
		{[]string{"--until-done", "--deadline", "2026-11-12T10:00:01Z"}, "--deadline is more than 24 h ahead; a grant lasts at most 24 h"},
		{[]string{"--until-done", "--deadline", "tomorrow"}, "--deadline must be an ISO-8601 time with an offset (Z or ±hh:mm), e.g. 2026-10-07T07:00:00+02:00"},
		{nil, "--until is required: an ISO-8601 time with an offset, e.g. 2026-10-07T07:00:00+02:00"},
	} {
		args := append(append([]string{"start", "--run", "r-a"}, tc.args...), tail...)
		code, out, errText := autopilotCall(t, args...)
		if code != 2 || out != "" || errText != "autopilot start: "+tc.err+"\n" {
			t.Fatalf("%v = %d %q %q, want %q", tc.args, code, out, errText, tc.err)
		}
	}
	if rows := apSignedRows(t, f); len(rows) != 0 {
		t.Fatalf("a refusal wrote rows: %#v", rows)
	}
	if f.read(t, "r-a")["autopilot"] != nil {
		t.Fatal("a refusal stored a grant")
	}
}

func TestB634UntilDoneEndsAfterLastPlannedBlockCloses(t *testing.T) {
	f := newAutopilotFixture(t)
	f.record(t, "r-a", "active", `,"blocks":{"B1":{"status":"open"}}`)
	code, out, errText := autopilotCall(t, "start", "--run", "r-a", "--until-done", "--blocks", "B1, B2,B1", "--delegate", "L2", "--allow", "close:routine", "--yes")
	want := "autopilot run r-a: grant ap-1234abcd active until done (every planned block closed) (no deadline)\n  delegate L2 · allow close:routine · budget no caps · stop at 90%\n"
	if code != 0 || out != want || errText != "" {
		t.Fatalf("start = %d %q %q", code, out, errText)
	}
	grant := f.read(t, "r-a")["autopilot"].(map[string]any)
	planned, _ := grant["planned_blocks"].([]any)
	if grant["until"] != nil || grant["until_done"] != true || len(planned) != 2 || planned[0] != "B1" || planned[1] != "B2" || grant["status"] != "active" {
		t.Fatalf("grant: %#v", grant)
	}
	rows := apSignedRows(t, f)
	if len(rows) != 1 || rows[0]["event"] != "autopilot.grant" || rows[0]["until_done"] != true || rows[0]["until"] != nil {
		t.Fatalf("grant row: %#v", rows)
	}

	// a second start while it runs names the until-done end
	code, _, errText = autopilotCall(t, "start", "--run", "r-a", "--until", "2026-11-11T15:00:00Z", "--delegate", "L2", "--allow", "close:routine", "--yes")
	if code != 1 || errText != "autopilot start: run r-a already has an active autopilot grant until done (every planned block closed); stop it first with code-forge autopilot stop --run r-a\n" {
		t.Fatalf("second start = %d %q", code, errText)
	}

	// B1 closed, B2 not opened yet: still active, hours later
	b634SetBlocks(t, f, "r-a", `{"B1":{"status":"closed"}}`)
	b634At(t, autopilotTestNow.Add(30*time.Hour))
	code, out, _ = autopilotCall(t, "status", "--run", "r-a")
	if code != 0 || !strings.HasPrefix(out, "autopilot run r-a: active · until done (every planned block closed), no deadline\n  planned   1 of 2 done (B1 closed, B2 not opened)\n") {
		t.Fatalf("status one open = %d %q", code, out)
	}
	code, out, _ = autopilotCall(t, "status", "--run", "r-a", "--json")
	var data map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &data) != nil || data["state"] != "active" || data["time_left_s"] != nil || data["until_done"] != true || data["planned_done"] != 1.0 {
		t.Fatalf("status json = %d %s", code, out)
	}
	if rows := apSignedRows(t, f); len(rows) != 1 {
		t.Fatalf("the grant ended early: %#v", rows)
	}

	// the last planned block closes (deferred human checks count as closed)
	b634SetBlocks(t, f, "r-a", `{"B1":{"status":"closed"},"B2":{"status":"awaiting_human_check"},"B3":{"status":"open"}}`)
	code, out, _ = autopilotCall(t, "status", "--run", "r-a")
	if code != 0 || !strings.HasPrefix(out, "autopilot run r-a: done at 2026-11-12T16:00:00.000Z · every planned block closed\n  planned   2 of 2 done (B1 closed, B2 awaiting_human_check)\n") {
		t.Fatalf("status done = %d %q", code, out)
	}
	rows = apSignedRows(t, f)
	blocks, _ := rows[len(rows)-1]["blocks"].([]any)
	if len(rows) != 2 || rows[1]["event"] != "autopilot.expire" || rows[1]["reason"] != "done" || len(blocks) != 2 || rows[1]["ts"] != "2026-11-12T16:00:00.000Z" {
		t.Fatalf("expire row: %#v", rows)
	}
	grant = f.read(t, "r-a")["autopilot"].(map[string]any)
	if grant["status"] != "expired" || grant["done_at"] != "2026-11-12T16:00:00.000Z" {
		t.Fatalf("grant after done: %#v", grant)
	}
	// the ended grant refuses stop and extend, naming why it ended
	code, _, errText = autopilotCall(t, "stop", "--run", "r-a")
	if code != 1 || errText != "autopilot stop: run r-a's autopilot grant already ended at 2026-11-12T16:00:00.000Z: every planned block closed\n" {
		t.Fatalf("stop = %d %q", code, errText)
	}
	code, _, errText = autopilotCall(t, "extend", "--run", "r-a", "--deadline", "2026-11-12T18:00:00Z", "--yes")
	if code != 1 || errText != "autopilot extend: run r-a's autopilot grant already ended at 2026-11-12T16:00:00.000Z: every planned block closed\n" {
		t.Fatalf("extend = %d %q", code, errText)
	}
	code, out, _ = autopilotCall(t, "log", "--run", "r-a")
	if code != 0 || !strings.Contains(out, "Grant ended") || !strings.Contains(out, "every planned block closed (B1, B2)") || !strings.Contains(out, "until done") {
		t.Fatalf("log = %d %q", code, out)
	}
}

func TestB634UntilDoneWithoutBlocksUsesRunBlocks(t *testing.T) {
	f := newAutopilotFixture(t)
	f.record(t, "r-a", "active", "")
	if code, _, errText := autopilotCall(t, "start", "--run", "r-a", "--until-done", "--delegate", "L2", "--allow", "close:routine", "--yes"); code != 0 {
		t.Fatalf("start = %d %q", code, errText)
	}
	// no block opened yet: the grant does not end
	code, out, _ := autopilotCall(t, "status", "--run", "r-a")
	if code != 0 || !strings.Contains(out, "  planned   none yet (the run has no blocks; the grant does not end before one is closed)\n") || !strings.HasPrefix(out, "autopilot run r-a: active") {
		t.Fatalf("no blocks = %d %q", code, out)
	}
	b634SetBlocks(t, f, "r-a", `{"B1":{"status":"closed"},"B2":{"status":"open"}}`)
	if code, out, _ = autopilotCall(t, "status", "--run", "r-a"); !strings.HasPrefix(out, "autopilot run r-a: active") {
		t.Fatalf("one open = %d %q", code, out)
	}
	b634SetBlocks(t, f, "r-a", `{"B1":{"status":"closed"},"B2":{"status":"closed"}}`)
	if code, out, _ = autopilotCall(t, "status", "--run", "r-a"); !strings.HasPrefix(out, "autopilot run r-a: done at 2026-11-11T10:00:00.000Z") {
		t.Fatalf("all closed = %d %q", code, out)
	}
}

func TestB634DeadlineEndsFirst(t *testing.T) {
	f := newAutopilotFixture(t)
	f.record(t, "r-a", "active", `,"blocks":{"B1":{"status":"open"}}`)
	code, out, errText := autopilotCall(t, "start", "--run", "r-a", "--until-done", "--deadline", "2026-11-11T13:00:00+01:00", "--delegate", "L2", "--allow", "close:routine", "--yes")
	if code != 0 || !strings.HasPrefix(out, "autopilot run r-a: grant ap-1234abcd active until done (every planned block closed) or 2026-11-11T12:00:00.000Z (2h 0m)\n") || errText != "" {
		t.Fatalf("start = %d %q %q", code, out, errText)
	}
	code, out, _ = autopilotCall(t, "status", "--run", "r-a")
	if code != 0 || !strings.HasPrefix(out, "autopilot run r-a: active · until done (every planned block closed) or 2h 0m left (deadline 2026-11-11T12:00:00.000Z)\n  planned   0 of 1 done (B1 open)\n") {
		t.Fatalf("status = %d %q", code, out)
	}
	b634At(t, autopilotTestNow.Add(2*time.Hour))
	code, out, _ = autopilotCall(t, "status", "--run", "r-a")
	if code != 0 || !strings.HasPrefix(out, "autopilot run r-a: expired at 2026-11-11T12:00:00.000Z (the deadline, before every planned block closed)\n") {
		t.Fatalf("status at deadline = %d %q", code, out)
	}
	rows := apSignedRows(t, f)
	if len(rows) != 2 || rows[1]["event"] != "autopilot.expire" || rows[1]["until"] != "2026-11-11T12:00:00.000Z" || rows[1]["reason"] != nil {
		t.Fatalf("expire row: %#v", rows)
	}
	if grant := f.read(t, "r-a")["autopilot"].(map[string]any); grant["status"] != "expired" || grant["done_at"] != nil {
		t.Fatalf("grant: %#v", grant)
	}
}

func TestB634ExtendMovesEndWithSignedRow(t *testing.T) {
	f := newAutopilotFixture(t)
	f.record(t, "r-a", "active", grantJSON("active", "2026-11-11T12:00:00.000Z", "null", "null"))
	code, out, errText := autopilotCall(t, "extend", "--run", "r-a", "--until", "2026-11-11T18:30:00+02:00", "--yes")
	if code != 0 || out != "autopilot run r-a: grant ap-0000abcd now runs until 2026-11-11T16:30:00.000Z (6h 30m; was 2026-11-11T12:00:00.000Z)\n" || errText != "" {
		t.Fatalf("extend = %d %q %q", code, out, errText)
	}
	grant := f.read(t, "r-a")["autopilot"].(map[string]any)
	if grant["until"] != "2026-11-11T16:30:00.000Z" || grant["status"] != "active" {
		t.Fatalf("grant: %#v", grant)
	}
	rows := apSignedRows(t, f)
	if len(rows) != 1 || rows[0]["event"] != "autopilot.extend" || rows[0]["grant_id"] != "ap-0000abcd" || rows[0]["from"] != "2026-11-11T12:00:00.000Z" || rows[0]["until"] != "2026-11-11T16:30:00.000Z" || rows[0]["ts"] != "2026-11-11T10:00:00.000Z" {
		t.Fatalf("extend row: %#v", rows)
	}
	// past the old end, the grant is still active
	b634At(t, autopilotTestNow.Add(3*time.Hour))
	code, out, _ = autopilotCall(t, "status", "--run", "r-a")
	if code != 0 || !strings.HasPrefix(out, "autopilot run r-a: active · 3h 30m left (until 2026-11-11T16:30:00.000Z)\n") {
		t.Fatalf("status = %d %q", code, out)
	}
	code, out, _ = autopilotCall(t, "log", "--run", "r-a")
	if code != 0 || !strings.Contains(out, "Grant extended") || !strings.Contains(out, "until 2026-11-11T16:30:00.000Z (was 2026-11-11T12:00:00.000Z)") {
		t.Fatalf("log = %d %q", code, out)
	}
}

func TestB634ExtendUntilDoneDeadline(t *testing.T) {
	f := newAutopilotFixture(t)
	f.record(t, "r-a", "active", `,"blocks":{"B1":{"status":"open"}}`)
	if code, _, errText := autopilotCall(t, "start", "--run", "r-a", "--until-done", "--delegate", "L2", "--allow", "close:routine", "--yes"); code != 0 {
		t.Fatalf("start = %d %q", code, errText)
	}
	code, _, errText := autopilotCall(t, "extend", "--run", "r-a", "--until", "2026-11-11T15:00:00Z", "--yes")
	if code != 1 || errText != "autopilot extend: run r-a's autopilot grant runs until done with no deadline; give --deadline <time> to set one\n" {
		t.Fatalf("--until on no deadline = %d %q", code, errText)
	}
	code, out, errText := autopilotCall(t, "extend", "--run", "r-a", "--deadline", "2026-11-11T15:00:00Z", "--yes")
	if code != 0 || out != "autopilot run r-a: grant ap-1234abcd now runs until done (every planned block closed) or 2026-11-11T15:00:00.000Z (5h 0m; no deadline)\n" || errText != "" {
		t.Fatalf("extend = %d %q %q", code, out, errText)
	}
	// with a deadline set, --until also moves it later
	code, out, _ = autopilotCall(t, "extend", "--run", "r-a", "--until", "2026-11-11T17:00:00Z", "--yes")
	if code != 0 || out != "autopilot run r-a: grant ap-1234abcd now runs until done (every planned block closed) or 2026-11-11T17:00:00.000Z (7h 0m; was 2026-11-11T15:00:00.000Z)\n" {
		t.Fatalf("extend again = %d %q", code, out)
	}
	rows := apSignedRows(t, f)
	if len(rows) != 3 || rows[1]["event"] != "autopilot.extend" || rows[1]["from"] != nil || rows[2]["from"] != "2026-11-11T15:00:00.000Z" {
		t.Fatalf("rows: %#v", rows)
	}
	if grant := f.read(t, "r-a")["autopilot"].(map[string]any); grant["until"] != "2026-11-11T17:00:00.000Z" || grant["until_done"] != true {
		t.Fatalf("grant: %#v", grant)
	}
}

func TestB634ExtendRefusals(t *testing.T) {
	f := newAutopilotFixture(t)
	for sub, want := range map[string]string{
		"extend": "usage: code-forge autopilot extend --run <id> --until <ISO-8601 with offset>|--deadline <ISO-8601 with offset> [--yes]\n",
		"start":  "usage: code-forge autopilot start --run <id> --until <ISO-8601 with offset>|--until-done [--deadline <ISO-8601 with offset>] [--blocks <id,…>] --delegate <L2|L3> --allow <scope,…> [--deny <scope,…>] [--budget <category>=<usd>,…] [--stop-at <0..1>] [--yes]\n",
	} {
		if code, out, errText := autopilotCall(t, sub, "--help"); code != 0 || out != want || errText != "" {
			t.Fatalf("%s --help = %d %q %q", sub, code, out, errText)
		}
	}
	f.record(t, "r-none", "active", "")
	f.record(t, "r-a", "active", grantJSON("active", "2026-11-11T12:00:00.000Z", "null", "null"))
	f.record(t, "r-old", "active", grantJSON("active", "2026-11-11T09:00:00.000Z", "null", "null"))
	f.record(t, "r-stop", "active", grantJSON("stopped", "2026-11-11T12:00:00.000Z", `"2026-11-11T09:30:00.000Z"`, "null"))
	f.record(t, "r-end", "ended", grantJSON("active", "2026-11-11T12:00:00.000Z", "null", "null"))
	for _, tc := range []struct {
		args []string
		code int
		err  string
	}{
		{[]string{"--run", "r-a"}, 2, "--until is required: the grant's new end, an ISO-8601 time with an offset, e.g. 2026-10-07T07:00:00+02:00 (--deadline for an until-done grant)"},
		{[]string{"--run", "r-a", "--until", "2026-11-11T15:00:00Z", "--deadline", "2026-11-11T15:00:00Z"}, 2, "give --until or --deadline, not both"},
		{[]string{"--run", "r-a", "--until", "2026-11-11T09:00:00Z"}, 2, "--until is in the past; give a time after now, at most 24 h ahead"},
		{[]string{"--run", "r-a", "--until", "2026-11-12T10:00:01Z"}, 2, "--until is more than 24 h ahead; a grant lasts at most 24 h"},
		{[]string{"--run", "r-zz", "--until", "nope"}, 2, "--until must be an ISO-8601 time with an offset (Z or ±hh:mm), e.g. 2026-10-07T07:00:00+02:00"},
		{[]string{"--run", "r-a", "--until", "2026-11-11T11:00:00Z"}, 1, "--until 2026-11-11T11:00:00.000Z is not after the grant's current end 2026-11-11T12:00:00.000Z; extend only moves the end later"},
		{[]string{"--run", "r-a", "--until", "2026-11-11T12:00:00Z"}, 1, "--until 2026-11-11T12:00:00.000Z is not after the grant's current end 2026-11-11T12:00:00.000Z; extend only moves the end later"},
		{[]string{"--run", "r-a", "--deadline", "2026-11-11T15:00:00Z"}, 1, "run r-a's autopilot grant is not an until-done grant; move its end with --until <time>"},
		{[]string{"--run", "r-none", "--until", "2026-11-11T15:00:00Z"}, 1, "run r-none has no autopilot grant"},
		{[]string{"--run", "r-old", "--until", "2026-11-11T15:00:00Z"}, 1, "run r-old's autopilot grant already expired at 2026-11-11T09:00:00.000Z"},
		{[]string{"--run", "r-stop", "--until", "2026-11-11T15:00:00Z"}, 1, "run r-stop's autopilot grant was already stopped at 2026-11-11T09:30:00.000Z; start a new one with code-forge autopilot start"},
		{[]string{"--run", "r-end", "--until", "2026-11-11T15:00:00Z"}, 1, "run r-end has ended"},
		{[]string{"--run", "r-zz", "--until", "2026-11-11T15:00:00Z"}, 1, "no run record for r-zz"},
	} {
		code, out, errText := autopilotCall(t, append(append([]string{"extend"}, tc.args...), "--yes")...)
		if code != tc.code || out != "" || errText != "autopilot extend: "+tc.err+"\n" {
			t.Fatalf("%v = %d %q %q, want %d %q", tc.args, code, out, errText, tc.code, tc.err)
		}
	}
	// the owner's verb: no terminal and no --yes refuses
	code, out, errText := autopilotCall(t, "extend", "--run", "r-a", "--until", "2026-11-11T15:00:00Z")
	if code != 2 || out != "" || errText != "autopilot extend: no terminal to confirm; the owner extends autopilot — pass --yes to extend it without a prompt\n" {
		t.Fatalf("no terminal = %d %q %q", code, out, errText)
	}
	// a terminal that says no leaves the end as it was
	autopilotIsTTY = func() bool { return true }
	autopilotPromptable = func() bool { return true }
	asked := ""
	autopilotConfirm = func(q string) (humanask.Result, error) { asked = q; return humanask.Cancelled, nil }
	code, out, _ = autopilotCall(t, "extend", "--run", "r-a", "--until", "2026-11-11T15:00:00Z")
	if code != 1 || out != "cancelled — the grant's end is unchanged\n" || asked != "Extend autopilot for run r-a to 2026-11-11T15:00:00.000Z?" {
		t.Fatalf("declined = %d %q %q", code, out, asked)
	}
	rows := apSignedRows(t, f)
	for _, row := range rows {
		if row["event"] == "autopilot.extend" {
			t.Fatalf("a refusal wrote an extend row: %#v", rows)
		}
	}
	if grant := f.read(t, "r-a")["autopilot"].(map[string]any); grant["until"] != "2026-11-11T12:00:00.000Z" {
		t.Fatalf("a refusal moved the end: %#v", grant)
	}
	// a terminal that says yes extends
	autopilotConfirm = func(string) (humanask.Result, error) { return humanask.Confirmed, nil }
	if code, out, _ = autopilotCall(t, "extend", "--run", "r-a", "--until", "2026-11-11T15:00:00Z"); code != 0 || !strings.HasPrefix(out, "autopilot run r-a: grant ap-0000abcd now runs until 2026-11-11T15:00:00.000Z") {
		t.Fatalf("confirmed = %d %q", code, out)
	}
}
