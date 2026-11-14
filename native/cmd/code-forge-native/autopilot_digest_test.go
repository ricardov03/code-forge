package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/humanask"
	"github.com/ricardov03/code-forge/native/ticket"
)

// B635: autopilot digest, ratify and revert.

var apdgTestNow = time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)

func apdgClock(t *testing.T) {
	t.Helper()
	before, beforeAP := apdgNow, autopilotNow
	apdgNow = func() time.Time { return apdgTestNow }
	autopilotNow = func() time.Time { return apdgTestNow }
	t.Cleanup(func() { apdgNow, autopilotNow = before, beforeAP })
}

// apdgFixture: block B1 closed with its human check deferred, B2 open with
// a substitute approval, an autopilot waiver and an agent waiver, an active
// approval, an extend and an until-done end; plus rows that are no act.
func apdgFixture(t *testing.T, extra map[string]any) *hvFixture {
	t.Helper()
	apdgClock(t)
	approval := map[string]any{"approval_id": "apr-1", "key": "review.max_rounds_per_file", "new": 4, "old": 3, "old_absent": false, "prune": 0,
		"until": "2026-11-14T12:00:00.000Z", "approved_at": "2026-11-14T08:00:00.000Z", "status": "active", "ended_at": nil}
	ended := map[string]any{"approval_id": "apr-0", "key": "caps.extra", "new": 2, "old": 1, "old_absent": false, "prune": 0,
		"until": "2026-11-14T09:00:00.000Z", "approved_at": "2026-11-14T07:00:00.000Z", "status": "restored", "ended_at": "2026-11-14T09:00:00.000Z"}
	all := map[string]any{"autopilot_approvals": []any{ended, approval}}
	for k, v := range extra {
		all[k] = v
	}
	f := newHVFixture(t, map[string]any{"status": "closed_deferred_human", "opened_at": "2026-11-13T08:00:00.000Z", "human_checks": []any{"screen looks right"},
		"acceptance": []any{map[string]any{"clause": "c1", "tests": []any{"t1"}}, map[string]any{"clause": "screen looks right", "kind": "human_check", "waits_for": "device test on iPhone"}}}, all)
	record := f.record()
	blocks := record["blocks"].(map[string]any)
	blocks["B2"] = map[string]any{"status": "open", "owned_files": []any{"src/b.go"}, "owned_kinds": map[string]any{}, "attempt": 1, "acceptance": []any{map[string]any{"clause": "c1", "tests": []any{"t1"}}}}
	data, _ := json.Marshal(record)
	if err := os.WriteFile(filepath.Join(f.dir, "r-test.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	f.rows(
		map[string]any{"event": "autopilot.decision", "decision_id": "ad-1", "scope": "human-pass:deferred", "acted": true, "block": "B1", "clauses": []any{"C2"}, "ts": "2026-11-14T08:10:00.000Z"},
		map[string]any{"event": "autopilot.decision", "decision_id": "ad-2", "scope": "review:substitute", "acted": true, "block": "B2", "file": "src/b.go", "content_hash": "h1",
			"reason": "the reviewer is unavailable", "provider": "codex", "model": "m", "ts": "2026-11-14T08:20:00.000Z"},
		map[string]any{"event": "autopilot.decision", "decision_id": "ad-3", "scope": "review:substitute", "acted": false, "block": "B2", "file": "src/b.go", "ts": "2026-11-14T08:21:00.000Z"},
		map[string]any{"event": "review.result", "block": "B2", "file": "src/b.go", "status": "open", "open": []any{map[string]any{"id": "F2", "severity": "nit"}}},
		map[string]any{"event": "review.waived", "block": "B2", "file": "src/b.go", "finding": "F1", "severity": "warning", "reason": "style only", "by": "autopilot",
			"decision_id": "ad-4", "grant_id": "ap-1", "ts": "2026-11-14T08:30:00.000Z"},
		map[string]any{"event": "review.waived", "block": "B2", "file": "src/b.go", "finding": "F2", "severity": "nit", "verdict": "agree", "reason": "agreed nit", "by": "agent", "ts": "2026-11-14T08:35:00.000Z"},
		map[string]any{"event": "review.waived", "block": "B2", "file": "src/b.go", "finding": "F3", "severity": "nit", "reason": "by hand", "by": "human", "ts": "2026-11-14T08:36:00.000Z"},
		map[string]any{"event": "autopilot.approve", "approval_id": "apr-0", "key": "caps.extra", "old": 1, "old_absent": false, "new": 2, "until": "2026-11-14T09:00:00.000Z", "ts": "2026-11-14T07:00:00.000Z"},
		map[string]any{"event": "autopilot.approve", "approval_id": "apr-1", "key": "review.max_rounds_per_file", "old": 3, "old_absent": false, "new": 4, "until": "2026-11-14T12:00:00.000Z", "ts": "2026-11-14T08:00:00.000Z"},
		map[string]any{"event": "autopilot.extend", "grant_id": "ap-1", "from": "2026-11-14T09:00:00.000Z", "until": "2026-11-14T09:30:00.000Z", "ts": "2026-11-14T08:50:00.000Z"},
		map[string]any{"event": "autopilot.expire", "grant_id": "ap-0", "until": "2026-11-14T07:00:00.000Z", "reason": "deadline", "ts": "2026-11-14T07:00:00.000Z"},
		map[string]any{"event": "autopilot.expire", "grant_id": "ap-1", "until": nil, "reason": "done", "blocks": []any{"B1", "B2"}, "ts": "2026-11-14T09:40:00.000Z"},
	)
	// a row signed with another key is no act
	other := &hvFixture{t: t, home: f.home, dir: f.dir, key: []byte(strings.Repeat("x", 32))}
	other.rows(map[string]any{"event": "autopilot.extend", "grant_id": "ap-9", "from": nil, "until": "2026-11-14T11:00:00.000Z", "ts": "2026-11-14T08:55:00.000Z"})
	return f
}

// apdgIDs maps the event+detail of the fixture's acts to their digest ids.
func apdgIDs(t *testing.T, f *hvFixture) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, r := range f.ledger() {
		mac, _ := r["mac"].(string)
		name := r["event"].(string)
		for _, k := range []string{"decision_id", "finding", "approval_id", "grant_id"} {
			if v, ok := r[k].(string); ok {
				name += ":" + v
				break
			}
		}
		if _, seen := out[name]; !seen {
			out[name] = "dg-" + mac[:12]
		}
	}
	return out
}

func apdgSettleCall(t *testing.T, f *hvFixture, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv("HOME", f.home)
	return autopilotCall(t, args...)
}

func TestB635DigestListsActs(t *testing.T) {
	f := apdgFixture(t, nil)
	id := apdgIDs(t, f)
	code, out, errText := apdgSettleCall(t, f, "digest", "--run", "r-test")
	want := "run r-test: 7 autopilot act(s) wait for the owner's look\n" +
		id["autopilot.decision:ad-1"] + " deferred-human B1 2026-11-14T08:10:00.000Z: block B1 closed closed_deferred_human with its human checks deferred to the owner: C2 \"screen looks right\" (waits for: device test on iPhone)\n" +
		"  ratify: code-forge autopilot ratify " + id["autopilot.decision:ad-1"] + " --run r-test\n" +
		"  revert: code-forge autopilot revert " + id["autopilot.decision:ad-1"] + " --run r-test\n" +
		id["autopilot.decision:ad-2"] + " substitute B2 2026-11-14T08:20:00.000Z: src/b.go was approved by a substitute review: the reviewer is unavailable\n" +
		"  ratify: code-forge autopilot ratify " + id["autopilot.decision:ad-2"] + " --run r-test\n" +
		"  revert: code-forge autopilot revert " + id["autopilot.decision:ad-2"] + " --run r-test\n" +
		id["review.waived:ad-4"] + " waiver B2 2026-11-14T08:30:00.000Z: warning finding F1 of src/b.go waived by autopilot: style only\n" +
		"  ratify: code-forge autopilot ratify " + id["review.waived:ad-4"] + " --run r-test\n" +
		"  revert: code-forge autopilot revert " + id["review.waived:ad-4"] + " --run r-test\n" +
		id["review.waived:F2"] + " waiver B2 2026-11-14T08:35:00.000Z: nit finding F2 of src/b.go waived by agent: agreed nit\n" +
		"  ratify: code-forge autopilot ratify " + id["review.waived:F2"] + " --run r-test\n" +
		"  revert: code-forge autopilot revert " + id["review.waived:F2"] + " --run r-test\n" +
		id["autopilot.approve:apr-1"] + " temp-config - 2026-11-14T08:00:00.000Z: config review.max_rounds_per_file changed from 3 to 4 until 2026-11-14T12:00:00.000Z (approval apr-1)\n" +
		"  ratify: code-forge autopilot ratify " + id["autopilot.approve:apr-1"] + " --run r-test\n" +
		"  revert: code-forge autopilot revert " + id["autopilot.approve:apr-1"] + " --run r-test\n"
	extend := id["autopilot.extend:ap-1"] + " extend - 2026-11-14T08:50:00.000Z: grant ap-1 extended from 2026-11-14T09:00:00.000Z to 2026-11-14T09:30:00.000Z\n" +
		"  ratify: code-forge autopilot ratify " + id["autopilot.extend:ap-1"] + " --run r-test\n" +
		"  revert: none (an extend is undone by autopilot stop while its grant runs; once the grant has ended there is nothing to revert)\n"
	done := id["autopilot.expire:ap-1"] + " until-done-end - 2026-11-14T09:40:00.000Z: until-done grant ap-1 ended: every planned block closed (B1, B2)\n" +
		"  ratify: code-forge autopilot ratify " + id["autopilot.expire:ap-1"] + " --run r-test\n" +
		"  revert: none (the grant has ended; a new grant is autopilot start)\n"
	want += extend + done
	if code != 0 || errText != "" || out != want {
		t.Fatalf("digest = %d %q\n got: %s\nwant: %s", code, errText, out, want)
	}

	code, out, _ = apdgSettleCall(t, f, "digest", "--run=r-test", "--json")
	var got struct {
		Run  string           `json:"run"`
		Rows []map[string]any `json:"rows"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || got.Run != "r-test" || len(got.Rows) != 7 {
		t.Fatalf("json = %d %s", code, out)
	}
	first := got.Rows[0]
	if first["id"] != id["autopilot.decision:ad-1"] || first["kind"] != "deferred-human" || first["block"] != "B1" || first["file"] != nil || first["when"] != "2026-11-14T08:10:00.000Z" ||
		first["ratify"] != "code-forge autopilot ratify "+id["autopilot.decision:ad-1"]+" --run r-test" || first["revert"] != "code-forge autopilot revert "+id["autopilot.decision:ad-1"]+" --run r-test" {
		t.Fatalf("first row = %#v", first)
	}
	if last := got.Rows[6]; last["kind"] != "until-done-end" || last["revert"] != nil || last["revert_note"] == nil {
		t.Fatalf("last row = %#v", last)
	}
	if !strings.HasPrefix(out, `{"run":"r-test","rows":[{"id":`) {
		t.Fatalf("json key order: %s", out)
	}

	// usage
	for _, args := range [][]string{{"digest"}, {"digest", "--run", "r-test", "x"}, {"digest", "--run", "r-test", "--yes"}, {"ratify", "--run", "r-test"}, {"revert", "a", "b", "--run", "r-test"}} {
		if code, _, errText := apdgSettleCall(t, f, args...); code != 2 || !strings.HasPrefix(errText, "autopilot "+args[0]+": ") {
			t.Fatalf("%v = %d %q", args, code, errText)
		}
	}
	if code, out, _ := apdgSettleCall(t, f, "digest", "--help"); code != 0 || out != helpWrapped(t, "autopilot digest", "usage: code-forge autopilot digest --run <id> [--json]\n") {
		t.Fatalf("help = %d %q", code, out)
	}
}

func TestB635DigestEmpty(t *testing.T) {
	apdgClock(t)
	f := newHVFixture(t, nil, nil)
	code, out, errText := apdgSettleCall(t, f, "digest", "--run", "r-test")
	if code != 0 || errText != "" || out != "run r-test: no autopilot act waits for the owner's look\n" {
		t.Fatalf("digest = %d %q %q", code, out, errText)
	}
	if code, _, errText := apdgSettleCall(t, f, "digest", "--run", "r-none"); code != 1 || !strings.HasPrefix(errText, "autopilot digest: ") {
		t.Fatalf("missing run = %d %q", code, errText)
	}
}

func apdgSettleRows(f *hvFixture, event string) []map[string]any {
	out := []map[string]any{}
	for _, r := range f.ledger() {
		if r["event"] == event {
			out = append(out, r)
		}
	}
	return out
}

func TestB635RatifyOwnerOnly(t *testing.T) {
	f := apdgFixture(t, nil)
	id := apdgIDs(t, f)["autopilot.decision:ad-2"]
	before := len(f.ledger())

	b493HumanSeam(t, false, humanask.Confirmed)
	if code, _, errText := apdgSettleCall(t, f, "ratify", id, "--run", "r-test"); code != 2 || errText != "autopilot ratify needs the human at a terminal to confirm; nothing was recorded (there is no --yes)\n" {
		t.Fatalf("no tty = %d %q", code, errText)
	}
	if code, _, errText := apdgSettleCall(t, f, "ratify", id, "--run", "r-test", "--yes"); code != 2 || errText != "autopilot ratify: unknown flag \"--yes\"\n" {
		t.Fatalf("--yes = %d %q", code, errText)
	}
	b493HumanSeam(t, true, humanask.Cancelled)
	if code, _, errText := apdgSettleCall(t, f, "ratify", id, "--run", "r-test"); code != 1 || errText != "autopilot ratify "+id+": cancelled — nothing was recorded\n" {
		t.Fatalf("cancel = %d %q", code, errText)
	}
	if code, _, errText := apdgSettleCall(t, f, "ratify", "dg-000000000000", "--run", "r-test"); code != 1 || !strings.Contains(errText, "digest-row-not-open: run r-test has no open digest row dg-000000000000") {
		t.Fatalf("unknown = %d %q", code, errText)
	}
	if len(f.ledger()) != before {
		t.Fatal("a refusal wrote a row")
	}

	asked := b493HumanSeam(t, true, humanask.Confirmed)
	code, out, errText := apdgSettleCall(t, f, "ratify", id, "--run", "r-test")
	if code != 0 || errText != "" || out != "run r-test: substitute "+id+" ratified (by: human)\n" {
		t.Fatalf("ratify = %d %q %q", code, out, errText)
	}
	if len(*asked) != 1 || !strings.Contains((*asked)[0], "Only the owner may say yes.") {
		t.Fatalf("asked = %q", *asked)
	}
	rows := apdgSettleRows(f, "autopilot.ratify")
	if len(rows) != 1 {
		t.Fatalf("rows = %#v", rows)
	}
	f.verified(rows[0])
	want := map[string]any{"digest_id": id, "kind": "substitute", "source_event": "autopilot.decision", "block": "B2", "file": "src/b.go", "decision_id": "ad-2",
		"content_hash": "h1", "by": "human", "ts": "2026-11-14T10:00:00.000Z", "run": "r-test"}
	for k, v := range want {
		if rows[0][k] != v {
			t.Fatalf("row %s = %#v, want %#v", k, rows[0][k], v)
		}
	}
	_, out, _ = apdgSettleCall(t, f, "digest", "--run", "r-test")
	if strings.Contains(out, id) || !strings.HasPrefix(out, "run r-test: 6 autopilot act(s)") {
		t.Fatalf("digest after ratify: %s", out)
	}
	if code, _, errText := apdgSettleCall(t, f, "ratify", id, "--run", "r-test"); code != 1 || !strings.Contains(errText, "digest-row-not-open") {
		t.Fatalf("twice = %d %q", code, errText)
	}
	// an ended run is still ratified
	record := f.record()
	record["status"] = "ended"
	data, _ := json.Marshal(record)
	if err := os.WriteFile(filepath.Join(f.dir, "r-test.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	extend := apdgIDs(t, f)["autopilot.extend:ap-1"]
	if code, out, _ := apdgSettleCall(t, f, "ratify", extend, "--run", "r-test", "--json"); code != 0 || !strings.HasPrefix(out, `{"event":"autopilot.ratify","digest_id":"`+extend+`","kind":"extend"`) {
		t.Fatalf("ended ratify = %d %q", code, out)
	}
	if code, _, errText := apdgSettleCall(t, f, "revert", apdgIDs(t, f)["review.waived:ad-4"], "--run", "r-test"); code != 1 || !strings.Contains(errText, "run r-test has ended; only ratify is left") {
		t.Fatalf("ended revert = %d %q", code, errText)
	}
}

func TestB635RefusedUnderActiveGrant(t *testing.T) {
	var grant map[string]any
	if err := json.Unmarshal([]byte("{"+strings.TrimPrefix(grantJSON("active", "2026-11-14T12:00:00.000Z", "null", "null"), ",")+"}"), &grant); err != nil {
		t.Fatal(err)
	}
	f := apdgFixture(t, grant)
	id := apdgIDs(t, f)["autopilot.decision:ad-1"]
	before := len(f.ledger())
	asked := b493HumanSeam(t, true, humanask.Confirmed)
	for _, verb := range []string{"ratify", "revert"} {
		code, _, errText := apdgSettleCall(t, f, verb, id, "--run", "r-test")
		if code != 1 || errText != "autopilot "+verb+": autopilot "+verb+" is refused while an autopilot grant is active on run r-test\n" {
			t.Fatalf("%s = %d %q", verb, code, errText)
		}
	}
	if len(*asked) != 0 || len(f.ledger()) != before {
		t.Fatal("asked or wrote under an active grant")
	}
	// digest is read-only and works under the grant
	if code, out, _ := apdgSettleCall(t, f, "digest", "--run", "r-test"); code != 0 || !strings.Contains(out, id) {
		t.Fatalf("digest = %d %q", code, out)
	}
}

func TestB635RevertDeferredBackToPending(t *testing.T) {
	f := apdgFixture(t, nil)
	id := apdgIDs(t, f)["autopilot.decision:ad-1"]
	b493HumanSeam(t, true, humanask.Confirmed)
	code, out, errText := apdgSettleCall(t, f, "revert", id, "--run", "r-test")
	if code != 0 || errText != "" || out != "run r-test: deferred-human "+id+" reverted (by: human)\nrun r-test: block B1 is awaiting_human_check again (block human-pass, or run human-checks clear, ends it)\n" {
		t.Fatalf("revert = %d %q %q", code, out, errText)
	}
	if status := f.record()["blocks"].(map[string]any)["B1"].(map[string]any)["status"]; status != "awaiting_human_check" {
		t.Fatalf("status = %v", status)
	}
	waits := apdgSettleRows(f, "block.awaiting_human_check")
	if len(waits) != 1 || waits[0]["block"] != "B1" || len(waits[0]["checks"].([]any)) != 1 {
		t.Fatalf("awaiting rows = %#v", waits)
	}
	f.verified(waits[0])
	reverts := apdgSettleRows(f, "autopilot.revert")
	if len(reverts) != 1 || reverts[0]["digest_id"] != id || reverts[0]["kind"] != "deferred-human" || reverts[0]["decision_id"] != "ad-1" || reverts[0]["by"] != "human" {
		t.Fatalf("revert rows = %#v", reverts)
	}
	f.verified(reverts[0])
	if _, out, _ := apdgSettleCall(t, f, "digest", "--run", "r-test"); strings.Contains(out, id) {
		t.Fatalf("still listed: %s", out)
	}
	// an extend has nothing to revert
	extend := apdgIDs(t, f)["autopilot.extend:ap-1"]
	if code, _, errText := apdgSettleCall(t, f, "revert", extend, "--run", "r-test"); code != 1 || !strings.Contains(errText, "revert-not-applicable: "+extend+" is a extend row") {
		t.Fatalf("extend revert = %d %q", code, errText)
	}
}

func TestB635RevertWaiverReopensFinding(t *testing.T) {
	f := apdgFixture(t, nil)
	ids := apdgIDs(t, f)
	b493HumanSeam(t, true, humanask.Confirmed)
	for _, name := range []string{"review.waived:ad-4", "review.waived:F2"} {
		id := ids[name]
		code, out, errText := apdgSettleCall(t, f, "revert", id, "--run", "r-test")
		if code != 0 || errText != "" || !strings.HasPrefix(out, "run r-test: waiver "+id+" reverted (by: human)\nrun r-test: finding ") {
			t.Fatalf("%s revert = %d %q %q", name, code, out, errText)
		}
	}
	unwaived := apdgSettleRows(f, "review.unwaived")
	if len(unwaived) != 2 || unwaived[0]["finding"] != "F1" || unwaived[1]["finding"] != "F2" || unwaived[0]["by"] != "human" || unwaived[0]["file"] != "src/b.go" {
		t.Fatalf("unwaived = %#v", unwaived)
	}
	f.verified(unwaived[0])
	_, out, _ := apdgSettleCall(t, f, "digest", "--run", "r-test")
	if strings.Contains(out, " waiver ") {
		t.Fatalf("waivers still listed: %s", out)
	}
	// the agent waiver no longer counts at the gate
	rows := []gatecheck.Row{}
	for _, r := range f.ledger() {
		rows = append(rows, r)
	}
	if got := gatecheck.AgentWaivers(rows, "B2", f.key); len(got) != 0 {
		t.Fatalf("agent waivers = %#v", got)
	}
}

func TestB635RevertSubstitute(t *testing.T) {
	f := apdgFixture(t, nil)
	id := apdgIDs(t, f)["autopilot.decision:ad-2"]
	if apdgSubstRevertedRun(f.home, "r-test", "B2", "src/b.go", "h1") {
		t.Fatal("reverted before the revert")
	}
	b493HumanSeam(t, true, humanask.Confirmed)
	code, out, errText := apdgSettleCall(t, f, "revert", id, "--run", "r-test")
	if code != 0 || errText != "" || out != "run r-test: substitute "+id+" reverted (by: human)\nrun r-test: the substitute review of src/b.go no longer counts at close; a normal review: code-forge review-file src/b.go --block B2 --run r-test --fresh\n" {
		t.Fatalf("revert = %d %q %q", code, out, errText)
	}
	if !apdgSubstRevertedRun(f.home, "r-test", "B2", "src/b.go", "h1") || apdgSubstRevertedRun(f.home, "r-test", "B2", "src/b.go", "h2") {
		t.Fatal("revert not found for the reviewed content only")
	}
	// close: the substitute does not count once reverted
	rows := []gatecheck.Row{}
	for _, r := range f.ledger() {
		rows = append(rows, r)
	}
	approved := map[string]any{"event": "review.approved", "block": "B2", "file": "src/b.go", "content_hash": "h1", "source": "substitute"}
	f.rows(approved)
	rows = append(rows, f.ledger()[len(f.ledger())-1])
	check := substCloseCheck(nil, "r-test", "B2", rows, f.key, []gatecheck.File{{File: "src/b.go", Hash: "h1"}}, gatecheck.Result{}, apdgTestNow)
	if len(check.Blockers) != 1 || !strings.Contains(check.Blockers[0].Detail, "the owner reverted the substitute (autopilot revert)") {
		t.Fatalf("close = %#v", check.Blockers)
	}
}

// B635: review-file --fresh re-runs an approved substitute result once the
// owner reverted it; without the revert it is kept (fresh-needs-reopen).
func TestB635FreshRerunsRevertedSubstitute(t *testing.T) {
	for _, reverted := range []bool{false, true} {
		root := t.TempDir()
		dir := filepath.Join(root, ".code-forge", "queue")
		reviews := filepath.Join(root, ".code-forge", "reviews")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(reviews, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(reviews, "t1.json"), []byte(`{"status":"approved","source":"substitute"}`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "t1.done"), []byte(`{"status":"approved","result":".code-forge/reviews/t1.json"}`), 0600); err != nil {
			t.Fatal(err)
		}
		before := reviewFreshSubst
		reviewFreshSubst = reverted
		out, code := reviewEnqueueFresh(root, ticket.Parts{Run: "r-test", Block: "B2", File: "src/b.go", ContentHash: "h1"}, "t1", false)
		reviewFreshSubst = before
		if !reverted {
			if code != 1 || out.Get("reason") != "fresh-needs-reopen" {
				t.Fatalf("kept = %d %v", code, out)
			}
			continue
		}
		if code != 0 || out.Get("fresh") != true || out.Get("status") != "queued" {
			t.Fatalf("rerun = %d %v", code, out)
		}
		if _, err := os.Stat(filepath.Join(dir, "t1.json")); err != nil {
			t.Fatalf("no ticket queued: %v", err)
		}
	}
}

func TestB635RevertTempConfigRestoresOldValue(t *testing.T) {
	f := apdgFixture(t, map[string]any{"status": "ended"})
	file := filepath.Join(f.workspace, ".code-forge.yml")
	if err := os.WriteFile(file, []byte("review:\n  max_rounds_per_file: 4\n"), 0644); err != nil {
		t.Fatal(err)
	}
	id := apdgIDs(t, f)["autopilot.approve:apr-1"]
	b493HumanSeam(t, true, humanask.Confirmed)
	code, out, errText := apdgSettleCall(t, f, "revert", id, "--run", "r-test")
	if code != 0 || errText != "" || out != "run r-test: temp-config "+id+" reverted (by: human)\nautopilot: approval apr-1 expired; the old value is back\n" {
		t.Fatalf("revert = %d %q %q", code, out, errText)
	}
	if data, _ := os.ReadFile(file); string(data) != "review:\n  max_rounds_per_file: 3\n" {
		t.Fatalf("config = %q", data)
	}
	a := f.record()["autopilot_approvals"].([]any)[1].(map[string]any)
	if a["status"] != "restored" || a["until"] != "2026-11-14T10:00:00.000Z" {
		t.Fatalf("approval = %#v", a)
	}
	if rows := apdgSettleRows(f, "autopilot.restore"); len(rows) != 1 || rows[0]["approval_id"] != "apr-1" {
		t.Fatalf("restore rows = %#v", rows)
	}
	reverts := apdgSettleRows(f, "autopilot.revert")
	if len(reverts) != 1 || reverts[0]["approval_id"] != "apr-1" || reverts[0]["key"] != "review.max_rounds_per_file" || reverts[0]["kind"] != "temp-config" {
		t.Fatalf("revert rows = %#v", reverts)
	}
	f.verified(reverts[0])
	if _, out, _ := apdgSettleCall(t, f, "digest", "--run", "r-test"); strings.Contains(out, "temp-config") {
		t.Fatalf("still listed: %s", out)
	}
}
