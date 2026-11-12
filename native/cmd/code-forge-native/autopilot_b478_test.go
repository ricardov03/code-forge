package main

import (
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/humanask"
)

// B478: start at a terminal without --yes asks the owner in Go (clack's
// confirm through humanask), with Node's message, cancel text and re-check.
func TestAutopilotStartPromptInGo(t *testing.T) {
	f := newAutopilotFixture(t)
	f.record(t, "r-a", "active", "")
	autopilotIsTTY = func() bool { return true }
	autopilotPromptable = func() bool { return true }
	asked := []string{}
	answer := humanask.Cancelled
	autopilotConfirm = func(message string) (humanask.Result, error) {
		asked = append(asked, message)
		return answer, nil
	}
	args := []string{"start", "--run", "r-a", "--until", "2026-11-11T15:30:00+00:00", "--delegate", "L2", "--allow", "round:extra"}

	code, out, errText := autopilotCall(t, args...)
	if code != 1 || out != "cancelled — no grant\n" || errText != "" {
		t.Fatalf("cancel = %d %q %q", code, out, errText)
	}
	if len(asked) != 1 || asked[0] != "Start autopilot for run r-a until 2026-11-11T15:30:00.000Z?" {
		t.Fatalf("prompt = %q", asked)
	}
	if rows := apSignedRows(t, f); len(rows) != 0 {
		t.Fatalf("a cancel wrote rows: %#v", rows)
	}

	// the prompt waited 30 minutes: the grant starts at the fresh clock
	answer = humanask.Confirmed
	later := autopilotTestNow.Add(30 * time.Minute)
	autopilotConfirm = func(message string) (humanask.Result, error) {
		autopilotNow = func() time.Time { return later }
		return humanask.Confirmed, nil
	}
	code, out, errText = autopilotCall(t, args...)
	want := "autopilot run r-a: grant ap-1234abcd active until 2026-11-11T15:30:00.000Z (5h 0m)\n  delegate L2 · allow round:extra · budget no caps · stop at 90%\n"
	if code != 0 || out != want || errText != "" {
		t.Fatalf("confirm = %d %q %q", code, out, errText)
	}
	rows := apSignedRows(t, f)
	if len(rows) != 1 || rows[0]["event"] != "autopilot.grant" || rows[0]["ts"] != "2026-11-11T10:30:00.000Z" {
		t.Fatalf("grant rows: %#v", rows)
	}
	if grant := f.read(t, "r-a")["autopilot"].(map[string]any); grant["started_at"] != "2026-11-11T10:30:00.000Z" {
		t.Fatalf("grant: %#v", grant)
	}

	// a prompt that outlived --until: the re-check refuses as usage, no grant
	f.record(t, "r-b", "active", "")
	autopilotNow = func() time.Time { return autopilotTestNow }
	autopilotConfirm = func(string) (humanask.Result, error) {
		autopilotNow = func() time.Time { return autopilotTestNow.Add(6 * time.Hour) }
		return humanask.Confirmed, nil
	}
	code, out, errText = autopilotCall(t, "start", "--run", "r-b", "--until", "2026-11-11T15:30:00+00:00", "--delegate", "L2", "--allow", "round:extra")
	if code != 2 || out != "" || errText != "autopilot start: --until is in the past; give a time after now, at most 24 h ahead\n" {
		t.Fatalf("late confirm = %d %q %q", code, out, errText)
	}
	if _, has := f.read(t, "r-b")["autopilot"]; has {
		t.Fatal("late confirm stored a grant")
	}
}
