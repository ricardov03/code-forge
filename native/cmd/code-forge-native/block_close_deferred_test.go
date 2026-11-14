package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/blockclose"
	"github.com/ricardov03/code-forge/native/humanask"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/runstatus"
	"github.com/ricardov03/code-forge/native/sign"
	"github.com/ricardov03/code-forge/native/state"
)

// B632: scope human-pass:deferred. The fixture is the routine close under an
// active grant (b590Routine: approved review, signed green gate row) with a
// signed autopilot.grant row, the grant's scopes and deny set as given, and
// block B1 carrying a test clause plus the given human_check clauses.
const b632Run = "r-close-fixture"

type b632Clause struct{ check, waits string }

func b632Fixture(t *testing.T, scopes, deny []any, human ...b632Clause) *closeReviewFixture {
	t.Helper()
	f := b590Routine(t)
	record, err := state.ReadRun(f.home, b632Run)
	if err != nil {
		t.Fatal(err)
	}
	grant := blockObject(record.Get("autopilot"))
	grant.Set("scopes", scopes)
	grant.Set("deny", deny)
	record.Set("autopilot", grant)
	blocks := blockObject(record.Get("blocks"))
	entry := blockObject(blocks.Get("B1"))
	acceptance := []any{ledger.Object{{Key: "clause", Value: "c1"}, {Key: "tests", Value: []any{"t1"}}}}
	checks := []any{}
	for _, h := range human {
		clause := ledger.Object{{Key: "clause", Value: h.check}, {Key: "kind", Value: "human_check"}}
		if h.waits != "" {
			clause.Set("waits_for", h.waits)
		}
		acceptance = append(acceptance, clause)
		checks = append(checks, h.check)
	}
	entry.Set("acceptance", acceptance)
	entry.Set("human_checks", checks)
	blocks.Set("B1", entry)
	record.Set("blocks", blocks)
	if err := state.SaveRun(f.home, record); err != nil {
		t.Fatal(err)
	}
	b493Sign(t, f, ledger.Object{{Key: "event", Value: "autopilot.grant"}, {Key: "grant_id", Value: "g-1"}})
	return f
}

var b632Screen = b632Clause{"screen looks right", "device test on iPhone"}

func b632Status(t *testing.T, f *closeReviewFixture) any {
	t.Helper()
	record, err := state.ReadRun(f.home, b632Run)
	if err != nil {
		t.Fatal(err)
	}
	return blockObject(blockObject(record.Get("blocks")).Get("B1")).Get("status")
}

// b632Rows returns the ledger rows with the given event, each verified.
func b632Rows(t *testing.T, f *closeReviewFixture, event string) []map[string]any {
	t.Helper()
	key, err := blockclose.ReadKey(f.home, b632Run)
	if err != nil {
		t.Fatal(err)
	}
	out := []map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(b493LedgerLines(t, f)), "\n") {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		if row["event"] != event {
			continue
		}
		if got, err := sign.VerifyRow(row, key); err != nil || !got.OK {
			t.Fatalf("%s row does not verify: %s", event, line)
		}
		out = append(out, row)
	}
	return out
}

func b632Checks(t *testing.T, f *closeReviewFixture, args ...string) (int, string, string) {
	t.Helper()
	var out, errs strings.Builder
	code := runHumanChecks(f.home, args, &out, &errs)
	return code, out.String(), errs.String()
}

const b632Clear = "code-forge run human-checks clear --run r-close-fixture --block B1 --clause C2 --note \"<what you checked>\""

// Scope granted and only human checks open: the block closes as
// closed_deferred_human with one signed decision row, the check stays in
// `run human-checks list` (marked deferred), the binnacle names the owner's
// clear command, and the block's done file and dependents see it as done.
func TestB632CloseDefersHumanChecksUnderScope(t *testing.T) {
	f := b632Fixture(t, []any{"close:routine", "human-pass:deferred"}, []any{}, b632Screen)
	code, out, errs, reason := b493Close(f)
	want := "reviewed file0.txt ticket b82733d3089a96c3991c870e\n" +
		"block B1 closed_deferred_human (scope human-pass:deferred, decision "
	if reason != 0 || code != 0 || !strings.HasPrefix(out, want) {
		t.Fatalf("exit=%d reason=%v\nout=%q\nerr=%q", code, reason, out, errs)
	}
	if !strings.HasSuffix(out, "): its human checks stay open for the owner\n"+
		"block B1 deferred human check C2: \"screen looks right\"; waits for: device test on iPhone\n"+
		"owner: "+b632Clear+"\n") {
		t.Fatalf("out=%q", out)
	}
	if s := b632Status(t, f); s != "closed_deferred_human" {
		t.Fatalf("status %v", s)
	}
	decisions := b632Rows(t, f, "autopilot.decision")
	if len(decisions) != 1 {
		t.Fatalf("decision rows %d, want 1", len(decisions))
	}
	d := decisions[0]
	clauses, _ := json.Marshal(d["clauses"])
	waits, _ := json.Marshal(d["waits_for"])
	if d["scope"] != "human-pass:deferred" || d["block"] != "B1" || d["grant_id"] != "g-1" || d["run"] != b632Run || d["decision"] != "defer" || d["acted"] != true ||
		string(clauses) != `[{"check":"screen looks right","clause":"C2","waits_for":"device test on iPhone"}]` || string(waits) != `["device test on iPhone"]` {
		t.Fatalf("decision row %#v", d)
	}
	if !strings.Contains(out, d["decision_id"].(string)) {
		t.Fatalf("out does not name the decision %v", d["decision_id"])
	}
	if n := len(b632Rows(t, f, "block.awaiting_human_check")); n != 0 {
		t.Fatalf("awaiting rows %d, want 0", n)
	}
	// The check is still open, marked deferred.
	code, list, lerr := b632Checks(t, f, "list", "--run", b632Run)
	if code != 0 || list != "run r-close-fixture: 1 open human check(s)\nB1 C2 \"screen looks right\"; waits for: device test on iPhone; deferred (block closed_deferred_human)\n" {
		t.Fatalf("list exit=%d out=%q err=%q", code, list, lerr)
	}
	code, list, _ = b632Checks(t, f, "list", "--run", b632Run, "--json")
	if code != 0 || !strings.Contains(list, `"clause":"C2"`) || !strings.Contains(list, `"deferred":true`) {
		t.Fatalf("list json exit=%d out=%q", code, list)
	}
	// The done file says what the block is.
	data, err := os.ReadFile(filepath.Join(f.home, ".code-forge/runs", b632Run, "blocks", "B1.done.json"))
	if err != nil || !strings.Contains(string(data), `"status":"closed_deferred_human"`) {
		t.Fatalf("done file %v %s", err, data)
	}
	// The binnacle shows one owner line for the deferred block.
	ownerBefore := bnGitOwner
	bnGitOwner = func(any) string { return "Test Owner" }
	t.Cleanup(func() { bnGitOwner = ownerBefore })
	code, bin, berr := autopilotCall(t, "binnacle", "--run", b632Run)
	line := "Check block B1 by hand (its human checks were deferred under autopilot): C2 \"screen looks right\" (waits for: device test on iPhone); then clear each with " + b632Clear
	if code != 0 || strings.Count(bin, line) != 1 || !strings.Contains(bin, "Closed with human checks deferred to you (B632); it may land") {
		t.Fatalf("binnacle exit=%d err=%q\n%s", code, berr, bin)
	}
	// run status lists the block with its new state.
	raw, err := os.ReadFile(filepath.Join(f.home, ".code-forge/runs", b632Run+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var runRecord map[string]any
	if err := json.Unmarshal(raw, &runRecord); err != nil {
		t.Fatal(err)
	}
	ledgerRows, err := ledger.ReadAllRows(filepath.Join(f.home, ".code-forge", "ledger"), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	summary, err := runstatus.ComputeErr(runRecord, ledgerRows, f.home)
	if err != nil {
		t.Fatal(err)
	}
	listed, _ := json.Marshal(summary.Get("blocks"))
	if !strings.Contains(string(listed), `"id":"B1","state":"closed_deferred_human"`) || summary.Get("closed") != 0 {
		t.Fatalf("status blocks %s closed %v", listed, summary.Get("closed"))
	}
	// block ready names the status.
	code, rout, rerr := b493Ready(t, f)
	if code == 0 || !strings.Contains(rout+rerr, "block B1 is closed_deferred_human in run r-close-fixture") {
		t.Fatalf("ready exit=%d out=%q err=%q", code, rout, rerr)
	}
}

// JSON close output carries the decision and the clear commands.
func TestB632CloseDeferredJSON(t *testing.T) {
	f := b632Fixture(t, []any{"human-pass:deferred", "close:routine"}, []any{}, b632Screen, b632Clause{"sound plays", ""})
	code, out, errs, _ := b493Close(f, "--json")
	if code != 0 || !strings.Contains(out, `"status":"closed_deferred_human"`) || !strings.Contains(out, `"deferred_human_checks":[{"clause":"C2","check":"screen looks right","waits_for":"device test on iPhone","clear_command":"`+strings.ReplaceAll(b632Clear, `"`, `\"`)+`"},{"clause":"C3","check":"sound plays","waits_for":null,"clear_command":`) {
		t.Fatalf("exit=%d\nout=%s\nerr=%q", code, out, errs)
	}
	d := b632Rows(t, f, "autopilot.decision")
	if len(d) != 1 || !strings.Contains(out, `"decision_id":"`+d[0]["decision_id"].(string)+`"`) {
		t.Fatalf("decision rows %v out %s", d, out)
	}
	waits, _ := json.Marshal(d[0]["waits_for"])
	if string(waits) != `["device test on iPhone"]` {
		t.Fatalf("waits_for %s", waits)
	}
}

// Without the scope (or with it denied, or with no grant at all) the close
// is exactly today's awaiting_human_check, with no decision row.
func TestB632CloseWithoutScopeAwaitsHuman(t *testing.T) {
	cases := map[string]func(*testing.T) *closeReviewFixture{
		"no scope": func(t *testing.T) *closeReviewFixture {
			return b632Fixture(t, []any{"close:routine"}, []any{}, b632Screen)
		},
		"scope denied": func(t *testing.T) *closeReviewFixture {
			return b632Fixture(t, []any{"close:routine", "human-pass:deferred"}, []any{"human-pass:deferred"}, b632Screen)
		},
		"grant stopped": func(t *testing.T) *closeReviewFixture {
			f := b632Fixture(t, []any{"close:routine", "human-pass:deferred"}, []any{}, b632Screen)
			record, err := state.ReadRun(f.home, b632Run)
			if err != nil {
				t.Fatal(err)
			}
			record.Delete("autopilot")
			if err := state.SaveRun(f.home, record); err != nil {
				t.Fatal(err)
			}
			b493HumanSeam(t, true, humanask.Confirmed) // close.require_human asks the owner
			return f
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			f := build(t)
			code, out, errs, reason := b493Close(f)
			if reason != 0 || code != 0 || !strings.Contains(out, "block B1 awaiting human check: screen looks right\n") || strings.Contains(out, "deferred") {
				t.Fatalf("exit=%d reason=%v\nout=%q\nerr=%q", code, reason, out, errs)
			}
			if s := b632Status(t, f); s != "awaiting_human_check" {
				t.Fatalf("status %v", s)
			}
			if n := len(b632Rows(t, f, "autopilot.decision")); n != 0 {
				t.Fatalf("decision rows %d, want 0", n)
			}
			if n := len(b632Rows(t, f, "block.awaiting_human_check")); n != 1 {
				t.Fatalf("awaiting rows %d, want 1", n)
			}
		})
	}
}

// Another blocker (an unruled late finding) still blocks under the scope:
// the block stays open and no decision row is written.
func TestB632OtherBlockerStillRefuses(t *testing.T) {
	f := b632Fixture(t, []any{"close:routine", "human-pass:deferred"}, []any{}, b632Screen)
	b493Sign(t, f, b590Late("file0.txt", "L1"))
	code, out, errs, _ := b493Close(f)
	if code != 1 || !strings.Contains(out, "late_unruled") || strings.Contains(out, "closed_deferred_human") {
		t.Fatalf("exit=%d\nout=%q\nerr=%q", code, out, errs)
	}
	if s := b632Status(t, f); s != "open" {
		t.Fatalf("status %v, want open", s)
	}
	for _, event := range []string{"autopilot.decision", "block.close"} {
		if n := len(b632Rows(t, f, event)); n != 0 {
			t.Fatalf("%s rows %d, want 0", event, n)
		}
	}
}

// Clearing a deferred block's checks: the first of two keeps it deferred;
// the last makes it closed with one signed block.human_checks_cleared row.
func TestB632ClearLastDeferredCheckCloses(t *testing.T) {
	f := b632Fixture(t, []any{"close:routine", "human-pass:deferred"}, []any{}, b632Screen, b632Clause{"sound plays", ""})
	if code, out, errs, _ := b493Close(f); code != 0 {
		t.Fatalf("close exit=%d out=%q err=%q", code, out, errs)
	}
	// The owner clears with no active grant (the verb is refused under one).
	record, err := state.ReadRun(f.home, b632Run)
	if err != nil {
		t.Fatal(err)
	}
	record.Delete("autopilot")
	if err := state.SaveRun(f.home, record); err != nil {
		t.Fatal(err)
	}
	b493HumanSeam(t, true, humanask.Confirmed)
	code, out, errs := b632Checks(t, f, "clear", "--run", b632Run, "--block", "B1", "--clause", "C2", "--note", "seen on my phone")
	if code != 0 || out != "run r-close-fixture: human check B1 C2 cleared (by: human)\n" || errs != "" {
		t.Fatalf("first clear exit=%d out=%q err=%q", code, out, errs)
	}
	if s := b632Status(t, f); s != "closed_deferred_human" {
		t.Fatalf("status after first clear %v", s)
	}
	if n := len(b632Rows(t, f, "block.human_checks_cleared")); n != 0 {
		t.Fatalf("closed rows after first clear %d", n)
	}
	code, out, errs = b632Checks(t, f, "clear", "--run", b632Run, "--block", "B1", "--clause", "C3", "--note", "heard it", "--json")
	if code != 0 || out != `{"event":"human_check.cleared","block":"B1","clause":"C3","check":"sound plays","waits_for":null,"note":"heard it","by":"human","block_status":"closed"}`+"\n" || errs != "" {
		t.Fatalf("last clear exit=%d out=%q err=%q", code, out, errs)
	}
	if s := b632Status(t, f); s != "closed" {
		t.Fatalf("status after last clear %v", s)
	}
	rows := b632Rows(t, f, "block.human_checks_cleared")
	if len(rows) != 1 || rows[0]["block"] != "B1" || rows[0]["status"] != "closed" || rows[0]["by"] != "human" {
		t.Fatalf("closed rows %v", rows)
	}
	if code, list, _ := b632Checks(t, f, "list", "--run", b632Run); code != 0 || list != "run r-close-fixture: no open human checks\n" {
		t.Fatalf("list after clears %d %q", code, list)
	}
}

// Text clear of the last check says the block closed.
func TestB632ClearLastDeferredCheckText(t *testing.T) {
	f := b632Fixture(t, []any{"close:routine", "human-pass:deferred"}, []any{}, b632Screen)
	if code, out, errs, _ := b493Close(f); code != 0 {
		t.Fatalf("close exit=%d out=%q err=%q", code, out, errs)
	}
	record, err := state.ReadRun(f.home, b632Run)
	if err != nil {
		t.Fatal(err)
	}
	record.Delete("autopilot")
	if err := state.SaveRun(f.home, record); err != nil {
		t.Fatal(err)
	}
	b493HumanSeam(t, true, humanask.Confirmed)
	code, out, errs := b632Checks(t, f, "clear", "--run", b632Run, "--block", "B1", "--clause", "C2", "--note", "seen")
	if code != 0 || out != "run r-close-fixture: human check B1 C2 cleared (by: human)\nrun r-close-fixture: block B1 closed (its last deferred human check is cleared)\n" || errs != "" {
		t.Fatalf("clear exit=%d out=%q err=%q", code, out, errs)
	}
}

// A closed_deferred_human block lands like a closed one (the land retry).
func TestB632LandDeferredBlock(t *testing.T) {
	f := androidLand(t)
	const run = "r-b632-land"
	entry := ledger.Object{{Key: "status", Value: "closed_deferred_human"}, {Key: "owned_files", Value: []any{"sample.mjs"}}, {Key: "base_branch", Value: "main"}, {Key: "worktree", Value: ledger.Object{{Key: "path", Value: f.tree}, {Key: "branch", Value: "block-B1"}, {Key: "base_sha", Value: f.base}}}}
	record := ledger.Object{{Key: "run_id", Value: run}, {Key: "project", Value: "fixture"}, {Key: "workspace", Value: f.repo}, {Key: "blocks", Value: ledger.Object{{Key: "B1", Value: entry}}}, {Key: "orphans", Value: []any{}}}
	if err := state.SaveRun(f.home, record); err != nil {
		t.Fatal(err)
	}
	landWrite(t, filepath.Join(f.home, ".code-forge/runs", run+".key"), strings.Repeat("0", 32))
	done, err := json.Marshal(map[string]any{"block": "B1", "run_id": run, "status": "closed_deferred_human", "event": "block.close", "commit": landGit(t, f.tree, "rev-parse", "HEAD")})
	if err != nil {
		t.Fatal(err)
	}
	landWrite(t, filepath.Join(f.home, ".code-forge/runs", run, "blocks", "B1.done.json"), string(done))
	saved, err := state.ReadRun(f.home, run)
	if err != nil {
		t.Fatal(err)
	}
	capture, err := os.CreateTemp(f.home, "output-")
	if err != nil {
		t.Fatal(err)
	}
	out, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = capture, capture
	flags := map[string]any{"report": "unused", "transcript": "unused", "merge": true, "no-merge-gate": true}
	code := landExecuteWithReason(nil, flags, f.home, run, "B1", saved, blockObject(blockObject(saved.Get("blocks")).Get("B1")), func(reason handoverReason) int {
		t.Errorf("deferred land handed over to Node: %v", reason)
		return 127
	})
	os.Stdout, os.Stderr = out, stderr
	capture.Close()
	data, err := os.ReadFile(capture.Name())
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 || !strings.Contains(string(data), "block B1 landed") {
		t.Fatalf("land: exit=%d output=%q, want B1 landed", code, data)
	}
	if got := landGit(t, f.repo, "show", "HEAD:sample.mjs"); got != "new" {
		t.Fatalf("merged sample=%q, want new", got)
	}
}

// Under an active grant, block open takes human_check clauses only when the
// grant allows human-pass:deferred.
func TestB632OpenHumanCheckUnderScope(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		f, _ := b581Fixture(t)
		scopes := []any{"close:routine"}
		if allowed {
			scopes = append(scopes, "human-pass:deferred")
		}
		b470SetRun(t, f, func(record, _ *ledger.Object) {
			record.Set("autopilot", ledger.Object{{Key: "grant_id", Value: "g-1"}, {Key: "status", Value: "active"}, {Key: "scopes", Value: scopes}, {Key: "deny", Value: []any{}}, {Key: "delegate", Value: "L2"}, {Key: "until", Value: "2999-01-01T00:00:00.000Z"}, {Key: "caps", Value: ledger.Object{}}, {Key: "stop_at", Value: 0.8}, {Key: "started_at", Value: "2019-01-01T00:00:00.000Z"}})
		})
		b581Level(t, f)
		acceptance := filepath.Join(f.home, "human.yml")
		closeFixtureWrite(t, acceptance, []byte("- clause: c\n  tests: [t]\n- clause: screen looks right\n  kind: human_check\n  waits_for: device test on iPhone\n"))
		res := b451Run(t, f.home, append([]string{"open"}, b581Open(acceptance)...)...)
		if !allowed {
			if res.code != 1 || !strings.Contains(res.stderr, "autopilot-human-check: an active autopilot grant cannot wait on human_check clauses: \"screen looks right\"") {
				t.Fatalf("no scope: exit=%d stderr=%q", res.code, res.stderr)
			}
			continue
		}
		if res.code != 0 {
			t.Fatalf("scope: exit=%d stderr=%q", res.code, res.stderr)
		}
		record, err := state.ReadRun(f.home, b632Run)
		if err != nil {
			t.Fatal(err)
		}
		checks, _ := json.Marshal(blockObject(blockObject(record.Get("blocks")).Get("B2")).Get("human_checks"))
		if string(checks) != `["screen looks right"]` {
			t.Fatalf("human_checks %s", checks)
		}
	}
}
