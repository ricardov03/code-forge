package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ricardov03/code-forge/native/humanask"
)

// b631Fixture: run r-test with four blocks. B1 has a test clause and a
// human clause with waits_for (C2); B2 a human clause without waits_for
// (C1); B3 is stopped; B4 passed its human check (signed block.human_pass).
func b631Fixture(t *testing.T, extra map[string]any) *hvFixture {
	t.Helper()
	f := newHVFixture(t, map[string]any{"opened_at": "2026-11-13T08:00:00.000Z", "acceptance": []any{
		map[string]any{"clause": "c1", "tests": []any{"t1"}},
		map[string]any{"clause": "screen looks right", "kind": "human_check", "waits_for": "device test on iPhone"},
	}}, extra)
	record := f.record()
	blocks := record["blocks"].(map[string]any)
	human := func(status, clause string) map[string]any {
		return map[string]any{"status": status, "owned_files": []any{}, "owned_kinds": map[string]any{}, "attempt": 1, "opened_at": "2026-11-13T09:00:00.000Z",
			"acceptance": []any{map[string]any{"clause": clause, "kind": "human_check"}}, "human_checks": []any{clause}}
	}
	blocks["B2"] = human("awaiting_human_check", "sound plays")
	blocks["B3"] = human("stopped", "never mind")
	blocks["B4"] = human("closed", "already seen")
	data, _ := json.Marshal(record)
	if err := os.WriteFile(filepath.Join(f.dir, "r-test.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	f.rows(map[string]any{"event": "block.human_pass", "block": "B4", "note": "ok", "checks": []any{"already seen"}, "by": "human"})
	return f
}

func b631Run(t *testing.T, f *hvFixture, args ...string) hvResult {
	t.Helper()
	t.Setenv("HOME", f.home)
	var out, errs bytes.Buffer
	code := runHumanChecks(f.home, args, &out, &errs)
	return hvResult{code: code, stdout: out.String(), stderr: errs.String()}
}

const b631OpenText = "run r-test: 2 open human check(s)\n" +
	"B1 C2 \"screen looks right\"; waits for: device test on iPhone; opened 2026-11-13T08:00:00.000Z\n" +
	"B2 C1 \"sound plays\"; opened 2026-11-13T09:00:00.000Z\n"

const b631OpenJSON = `{"run":"r-test","open":[{"block":"B1","clause":"C2","check":"screen looks right","waits_for":"device test on iPhone","opened_at":"2026-11-13T08:00:00.000Z"},{"block":"B2","clause":"C1","check":"sound plays","waits_for":null,"opened_at":"2026-11-13T09:00:00.000Z"}]}` + "\n"

func TestB631HumanChecksList(t *testing.T) {
	f := b631Fixture(t, nil)
	b631Run(t, f, "list", "--run", "r-test").want(t, 0, b631OpenText, "")
	b631Run(t, f, "list", "--run=r-test", "--json").want(t, 0, b631OpenJSON, "")
	b631Run(t, f, "list").want(t, 2, "", "run human-checks list: --run is required\n")
	b631Run(t, f, "list", "--run", "r-test", "--block", "B1").want(t, 2, "", "run human-checks list: unknown flag --block\n")
	b631Run(t, f, "bogus").want(t, 2, "", rhcListUsage+rhcClearUsage)
	b631Run(t, f, "list", "--run", "r-none").want(t, 1, "", "run human-checks list: no run record for r-none\n")
	// A cleared row without a valid MAC clears nothing.
	f.rows(map[string]any{"event": "human_check.cleared", "block": "B2", "clause": "C1", "check": "sound plays", "note": "x", "by": "human"})
	rows := f.ledger()
	last := rows[len(rows)-1]
	last["mac"] = "00" + last["mac"].(string)[2:]
	var buf bytes.Buffer
	for _, r := range rows[:len(rows)-1] {
		data, _ := json.Marshal(r)
		buf.Write(append(data, '\n'))
	}
	data, _ := json.Marshal(last)
	buf.Write(append(data, '\n'))
	if err := os.WriteFile(filepath.Join(f.home, ".code-forge", "ledger", "unit.jsonl"), buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	b631Run(t, f, "list", "--run", "r-test").want(t, 0, b631OpenText, "")
	// A run with no human clauses.
	g := newHVFixture(t, nil, nil)
	b631Run(t, g, "list", "--run", "r-test").want(t, 0, "run r-test: no open human checks\n", "")
	b631Run(t, g, "list", "--run", "r-test", "--json").want(t, 0, `{"run":"r-test","open":[]}`+"\n", "")
}

func TestB631HumanChecksClear(t *testing.T) {
	args := []string{"clear", "--run", "r-test", "--block", "B1", "--clause", "C2", "--note", "  tried it on my phone  "}
	t.Run("confirmed writes one signed row and the clause leaves the list", func(t *testing.T) {
		f := b631Fixture(t, nil)
		before := len(f.ledger())
		asked := b493HumanSeam(t, true, humanask.Confirmed)
		b631Run(t, f, args...).want(t, 0, "run r-test: human check B1 C2 cleared (by: human)\n", "")
		if len(*asked) != 1 || (*asked)[0] != `Confirm human check C2 of block B1 (run r-test) passed: "screen looks right"? Only the owner may say yes.` {
			t.Fatalf("asked %q", *asked)
		}
		rows := f.ledger()
		if len(rows) != before+1 {
			t.Fatalf("rows %d, want %d", len(rows), before+1)
		}
		row := rows[len(rows)-1]
		if row["event"] != "human_check.cleared" || row["run"] != "r-test" || row["block"] != "B1" || row["clause"] != "C2" || row["check"] != "screen looks right" || row["waits_for"] != "device test on iPhone" || row["note"] != "tried it on my phone" || row["by"] != "human" {
			t.Fatalf("row %#v", row)
		}
		f.verified(row)
		b631Run(t, f, "list", "--run", "r-test").want(t, 0, "run r-test: 1 open human check(s)\nB2 C1 \"sound plays\"; opened 2026-11-13T09:00:00.000Z\n", "")
		// The block's state is unchanged.
		if s := f.record()["blocks"].(map[string]any)["B1"].(map[string]any)["status"]; s != "open" {
			t.Fatalf("status %v", s)
		}
		// Clearing it again is refused before the question.
		*asked = nil
		b631Run(t, f, args...).want(t, 1, "", "run human-checks clear: human-check-not-open: block B1 has no open human check C2 in run r-test; nothing was recorded\n")
		if len(*asked) != 0 || len(f.ledger()) != before+1 {
			t.Fatal("refusal asked or wrote")
		}
	})
	t.Run("json", func(t *testing.T) {
		f := b631Fixture(t, nil)
		b493HumanSeam(t, true, humanask.Confirmed)
		b631Run(t, f, "clear", "--run", "r-test", "--block", "B2", "--clause", "C1", "--note", "heard it", "--json").want(t, 0, `{"event":"human_check.cleared","block":"B2","clause":"C1","check":"sound plays","waits_for":null,"note":"heard it","by":"human"}`+"\n", "")
	})
	t.Run("agent without a terminal is refused", func(t *testing.T) {
		f := b631Fixture(t, nil)
		before := len(f.ledger())
		b493HumanSeam(t, false, humanask.NoTTY)
		b631Run(t, f, args...).want(t, 2, "", "run human-checks clear needs the human at a terminal to confirm; nothing was recorded (there is no --yes)\n")
		if len(f.ledger()) != before {
			t.Fatal("no-terminal wrote rows")
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		f := b631Fixture(t, nil)
		before := len(f.ledger())
		b493HumanSeam(t, true, humanask.Cancelled)
		b631Run(t, f, args...).want(t, 1, "", "run human-checks clear B1 C2: cancelled — nothing was recorded\n")
		if len(f.ledger()) != before {
			t.Fatal("cancel wrote rows")
		}
	})
	t.Run("active autopilot grant is refused", func(t *testing.T) {
		grant := map[string]any{"autopilot": map[string]any{"grant_id": "g1", "status": "active", "until": "2099-01-01T00:00:00.000Z", "started_at": "2026-01-01T00:00:00.000Z", "scopes": []any{}, "deny": []any{}, "delegate": "L2", "caps": map[string]any{}, "stop_at": 0.8}}
		f := b631Fixture(t, grant)
		before := len(f.ledger())
		asked := b493HumanSeam(t, true, humanask.Confirmed)
		b631Run(t, f, args...).want(t, 1, "", "run human-checks clear: run human-checks clear is refused while an autopilot grant is active on run r-test\n")
		if len(*asked) != 0 || len(f.ledger()) != before {
			t.Fatalf("grant refusal asked %q or wrote rows", *asked)
		}
	})
	t.Run("usage and unknown clause", func(t *testing.T) {
		f := b631Fixture(t, nil)
		asked := b493HumanSeam(t, true, humanask.Confirmed)
		b631Run(t, f, "clear", "--run", "r-test", "--block", "B1", "--clause", "C2").want(t, 2, "", "run human-checks clear: --note must be nonblank text without control characters\n")
		b631Run(t, f, "clear", "--run", "r-test", "--block", "B1", "--note", "x").want(t, 2, "", "run human-checks clear: --run, --block and --clause are required\n")
		b631Run(t, f, "clear", "--run", "r-test", "--block", "B1", "--clause", "C1", "--note", "x").want(t, 1, "", "run human-checks clear: human-check-not-open: block B1 has no open human check C1 in run r-test; nothing was recorded\n")
		b631Run(t, f, "clear", "--run", "r-test", "--block", "B3", "--clause", "C1", "--note", "x").want(t, 1, "", "run human-checks clear: human-check-not-open: block B3 has no open human check C1 in run r-test; nothing was recorded\n")
		if len(*asked) != 0 {
			t.Fatalf("asked %q", *asked)
		}
	})
}

// The verb is routed through `code-forge run` (child process, real router).
func TestB631HumanChecksRouted(t *testing.T) {
	if os.Getenv("BLOCK_HV_CHILD") == "1" {
		blockHVChild()
	}
	f := b631Fixture(t, nil)
	f.run("TestB631HumanChecksRouted", "notty", "run|human-checks|list|--run|r-test").want(t, 0, b631OpenText, "")
	f.run("TestB631HumanChecksRouted", "notty", "run|human-checks|--help").want(t, 0, helpWrapped(t, "run human-checks", rhcListUsage+rhcClearUsage), "")
	f.run("TestB631HumanChecksRouted", "notty", "run|human-checks|clear|--run|r-test|--block|B1|--clause|C2|--note|x").want(t, 2, "", "run human-checks clear needs the human at a terminal to confirm; nothing was recorded (there is no --yes)\n")
}

// waits_for through block amend-acceptance: a valid one is stored on the
// clause, an invalid one is refused before the question.
func TestB631AmendAcceptanceWaitsFor(t *testing.T) {
	if os.Getenv("BLOCK_HV_CHILD") == "1" {
		blockHVChild()
	}
	file := func(t *testing.T, text string) string {
		p := filepath.Join(t.TempDir(), "acc.yml")
		if err := os.WriteFile(p, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	f := newHVFixture(t, nil, nil)
	bad := file(t, "- clause: c1\n  tests: [t1]\n  waits_for: phone\n")
	r := f.run("TestB631AmendAcceptanceWaitsFor", "yes", "block|amend-acceptance|B1|--run|r-test|--acceptance|"+bad+"|--reason|why")
	r.want(t, 1, "", "block amend-acceptance: acceptance: waits_for is allowed only on a human_check clause\n")
	if r.asked != "" || f.ledger() != nil {
		t.Fatal("refusal asked or wrote")
	}
	good := file(t, "- clause: c1\n  tests: [t1]\n- clause: look at it\n  kind: human_check\n  waits_for: device test on iPhone\n")
	r = f.run("TestB631AmendAcceptanceWaitsFor", "yes", "block|amend-acceptance|B1|--run|r-test|--acceptance|"+good+"|--reason|why")
	if r.code != 0 {
		t.Fatalf("exit %d stderr %q", r.code, r.stderr)
	}
	clauses := f.record()["blocks"].(map[string]any)["B1"].(map[string]any)["acceptance"].([]any)
	if len(clauses) != 2 || clauses[1].(map[string]any)["waits_for"] != "device test on iPhone" {
		t.Fatalf("acceptance %#v", clauses)
	}
	f.run("TestB631AmendAcceptanceWaitsFor", "notty", "run|human-checks|list|--run|r-test|--json").want(t, 0, `{"run":"r-test","open":[{"block":"B1","clause":"C2","check":"look at it","waits_for":"device test on iPhone","opened_at":null}]}`+"\n", "")
}
