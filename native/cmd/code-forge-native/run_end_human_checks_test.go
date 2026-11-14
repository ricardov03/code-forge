package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B633 (issue #37 B, part 3): run end refuses while the run has an open
// human check of a block that is past open (deferred, awaiting or closed),
// and the binnacle never reads ready while one is open.

// b633Fixture is b631Fixture with B1 closed_deferred_human (its C2 waits
// for a device test) and B2 awaiting_human_check; B3 is stopped and B4
// passed its human check. No worker is pinned.
func b633Fixture(t *testing.T) *hvFixture {
	t.Helper()
	f := b631Fixture(t, map[string]any{"engine": "claude"})
	record := f.record()
	record["blocks"].(map[string]any)["B1"].(map[string]any)["status"] = "closed_deferred_human"
	f.writeRecord(record)
	t.Setenv("HOME", f.home)
	t.Setenv("CODE_FORGE_NODE", filepath.Join(f.home, "missing-node"))
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	return f
}

func (f *hvFixture) writeRecord(record map[string]any) {
	f.t.Helper()
	data, _ := json.Marshal(record)
	if err := os.WriteFile(filepath.Join(f.dir, "r-test.json"), data, 0600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *hvFixture) setStatus(block, status string) {
	f.t.Helper()
	record := f.record()
	record["blocks"].(map[string]any)[block].(map[string]any)["status"] = status
	f.writeRecord(record)
}

func (f *hvFixture) clearRow(block, clause, check string) {
	f.rows(map[string]any{"event": "human_check.cleared", "block": block, "clause": clause, "check": check, "note": "seen", "by": "human"})
}

const b633Clear1 = `code-forge run human-checks clear --run r-test --block B1 --clause C2 --note "<what you checked>"`
const b633Clear2 = `code-forge run human-checks clear --run r-test --block B2 --clause C1 --note "<what you checked>"`

const b633RefusedBoth = "run end: run r-test has 2 open human check(s); it cannot end until each is cleared:\n" +
	"  B1 C2 \"screen looks right\" (deferred; waits for: device test on iPhone)\n" +
	"    " + b633Clear1 + "\n" +
	"  B2 C1 \"sound plays\" (awaiting)\n" +
	"    " + b633Clear2 + "\n"

// b633Refused runs run end and checks it refused with nothing changed.
func b633Refused(t *testing.T, f *hvFixture, args []string, wantOut, wantErr string) {
	t.Helper()
	recordPath := filepath.Join(f.dir, "r-test.json")
	beforeRecord, _ := os.ReadFile(recordPath)
	beforeRows := len(f.ledger())
	code, out, errs := runVerbCall(t, args...)
	afterRecord, _ := os.ReadFile(recordPath)
	if code != 1 || out != wantOut || errs != wantErr {
		t.Fatalf("run end = %d\nstdout %q\nwant   %q\nstderr %q\nwant   %q", code, out, wantOut, errs, wantErr)
	}
	if !bytes.Equal(beforeRecord, afterRecord) || len(f.ledger()) != beforeRows {
		t.Fatalf("a refused run end changed the record or wrote rows")
	}
	if _, err := os.Stat(filepath.Join(f.dir, "r-test.lock")); !os.IsNotExist(err) {
		t.Fatalf("run lock left behind: %v", err)
	}
}

func TestB633RunEndRefusesDeferredAndAwaiting(t *testing.T) {
	f := b633Fixture(t)
	b633Refused(t, f, []string{"end", "--run", "r-test"}, "", b633RefusedBoth)
}

func TestB633RunEndRefusesDeferredOnly(t *testing.T) {
	f := b633Fixture(t)
	f.setStatus("B2", "closed")
	f.clearRow("B2", "C1", "sound plays")
	b633Refused(t, f, []string{"end", "--run", "r-test"}, "",
		"run end: run r-test has 1 open human check(s); it cannot end until each is cleared:\n"+
			"  B1 C2 \"screen looks right\" (deferred; waits for: device test on iPhone)\n"+
			"    "+b633Clear1+"\n")
}

func TestB633RunEndRefusesAwaitingOnly(t *testing.T) {
	f := b633Fixture(t)
	f.clearRow("B1", "C2", "screen looks right")
	f.setStatus("B1", "closed")
	b633Refused(t, f, []string{"end", "--run", "r-test"}, "",
		"run end: run r-test has 1 open human check(s); it cannot end until each is cleared:\n"+
			"  B2 C1 \"sound plays\" (awaiting)\n"+
			"    "+b633Clear2+"\n")
}

// A closed block whose human check has no verified clear row holds the end too.
func TestB633RunEndRefusesClosedWithOpenCheck(t *testing.T) {
	f := b633Fixture(t)
	f.setStatus("B1", "closed")
	f.setStatus("B2", "closed")
	f.clearRow("B2", "C1", "sound plays")
	b633Refused(t, f, []string{"end", "--run", "r-test"}, "",
		"run end: run r-test has 1 open human check(s); it cannot end until each is cleared:\n"+
			"  B1 C2 \"screen looks right\" (closed; waits for: device test on iPhone)\n"+
			"    "+b633Clear1+"\n")
}

func TestB633RunEndRefusalJSON(t *testing.T) {
	f := b633Fixture(t)
	want := `{"run":"r-test","ended":false,"refused":"human-checks-open","open":[` +
		`{"block":"B1","clause":"C2","check":"screen looks right","waits_for":"device test on iPhone","opened_at":"2026-11-13T08:00:00.000Z","state":"deferred","clear":` + b633JSON(b633Clear1) + `},` +
		`{"block":"B2","clause":"C1","check":"sound plays","waits_for":null,"opened_at":"2026-11-13T09:00:00.000Z","state":"awaiting","clear":` + b633JSON(b633Clear2) + `}]}` + "\n"
	b633Refused(t, f, []string{"end", "--run", "r-test", "--json"}, want,
		"run end: run r-test has 2 open human check(s); it cannot end until each is cleared\n")
	// --json is a run end flag only.
	code, out, errs := runVerbCall(t, "status", "--run", "r-test", "--json")
	if code != 2 || out != "" || errs != "run status: unknown flag \"--json\"\n" {
		t.Fatalf("status --json = %d %q %q", code, out, errs)
	}
	code, _, errs = runVerbCall(t, "end", "--run", "r-test", "--json=1")
	if code != 2 || errs != "run end: --json takes no value\n" {
		t.Fatalf("end --json=1 = %d %q", code, errs)
	}
}

// b633JSON is JSON.stringify of a string (no HTML escaping).
func b633JSON(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

// With every check cleared, run end behaves as before B633.
func TestB633RunEndAllClearedAsToday(t *testing.T) {
	f := b633Fixture(t)
	f.clearRow("B1", "C2", "screen looks right")
	f.clearRow("B2", "C1", "sound plays")
	// B2 is still awaiting_human_check: the pre-B633 refusal.
	b633Refused(t, f, []string{"end", "--run", "r-test"}, "", "run end: run r-test has blocks awaiting human check: B2; human-pass or stop them first\n")
	f.setStatus("B2", "closed")
	f.setStatus("B1", "closed")
	before := len(f.ledger())
	code, out, errs := runVerbCall(t, "end", "--run", "r-test")
	if code != 0 || out != "run r-test ended\n" || errs != "" {
		t.Fatalf("end = %d %q %q", code, out, errs)
	}
	rows := f.ledger()
	if len(rows) != before+2 || rows[before]["event"] != "worker.down" || rows[before+1]["event"] != "run.end" {
		t.Fatalf("rows %#v", rows[before:])
	}
	if f.record()["status"] != "ended" {
		t.Fatal("run not ended")
	}
}

func TestB633RunEndAllClearedJSON(t *testing.T) {
	f := b633Fixture(t)
	f.clearRow("B1", "C2", "screen looks right")
	f.setStatus("B2", "closed")
	f.clearRow("B2", "C1", "sound plays")
	code, out, errs := runVerbCall(t, "end", "--run", "r-test", "--json")
	ended, _ := f.record()["ended_at"].(string)
	if code != 0 || errs != "" || out != `{"run":"r-test","ended":true,"ended_at":"`+ended+`"}`+"\n" {
		t.Fatalf("end --json = %d %q %q", code, out, errs)
	}
}

// A run with no human checks is not read for them: an open block still
// gets the pre-B633 refusal first.
func TestB633RunEndNoHumanChecksUnchanged(t *testing.T) {
	f := b633Fixture(t)
	f.setStatus("B1", "open")
	f.clearRow("B2", "C1", "sound plays")
	f.setStatus("B2", "closed")
	b633Refused(t, f, []string{"end", "--run", "r-test"}, "", "run end: run r-test still has open blocks: B1\n")
}

// The binnacle names the open checks and does not call the closed block ready.
func TestB633BinnacleNamesOpenHumanChecks(t *testing.T) {
	f := b633Fixture(t)
	f.setStatus("B1", "closed")
	code, bin, berr := autopilotCall(t, "binnacle", "--run", "r-test")
	if code != 0 {
		t.Fatalf("binnacle = %d %q", code, berr)
	}
	item := "| Human checks | 2 open, so the run cannot end: B1 C2, B2 C1 (code-forge run human-checks list --run r-test) |"
	if !strings.Contains(bin, item) {
		t.Fatalf("no human checks item:\n%s", bin)
	}
	if strings.Contains(bin, "Mark block B1 ready") || !strings.Contains(bin, "Closed, but 1 human check is open; not ready") {
		t.Fatalf("closed block with an open check reads ready:\n%s", bin)
	}
	if !strings.Contains(bin, "- [ ] Check block B1 by hand: C2 \"screen looks right\" (waits for: device test on iPhone); then clear each with "+b633Clear1) {
		t.Fatalf("no clear action for B1:\n%s", bin)
	}
	// Cleared: no item and B1 is ready as before.
	f.clearRow("B1", "C2", "screen looks right")
	f.clearRow("B2", "C1", "sound plays")
	_, bin, _ = autopilotCall(t, "binnacle", "--run", "r-test")
	if strings.Contains(bin, "Human checks") || !strings.Contains(bin, "Mark block B1 ready and merge it") || !strings.Contains(bin, "Closed, ready for you to mark ready and merge") {
		t.Fatalf("cleared binnacle:\n%s", bin)
	}
}
