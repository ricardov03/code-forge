package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
	"github.com/ricardov03/code-forge/native/ticket"
)

// B630 (issue #37 C3/C4): block close counts a substitute review (a signed
// review.approved row with source: substitute) only under an active autopilot
// grant with scope review:substitute, with its signed autopilot.decision row
// and every gate green. Without the scope close refuses and names the owner
// command in the blocker's fix line.

type b630Case struct {
	scopes   []any // nil = no grant
	green    bool
	decision bool
	critical bool // the substitute found a critical: no approval
}

// b630Fixture: one owned file whose only approval (if any) is a substitute
// review, with the grant, gate row and decision row the case names.
func b630Fixture(t *testing.T, c b630Case) *closeReviewFixture {
	t.Helper()
	f := newCloseReviewFixture(t, 1)
	p := f.parts[0]
	if err := os.MkdirAll(filepath.Join(f.top, ".code-forge/queue"), 0700); err != nil {
		t.Fatal(err)
	}
	id := ticket.ReviewID(p)
	row := ledger.Object{{Key: "event", Value: "review.result"}, {Key: "block", Value: "B1"}, {Key: "file", Value: p.File}, {Key: "ticket", Value: id},
		{Key: "content_hash", Value: p.ContentHash}, {Key: "status", Value: "reviewed"}, {Key: "approved", Value: !c.critical}, {Key: "open", Value: []any{}},
		{Key: "source", Value: "substitute"}, {Key: "substitute", Value: ledger.Object{{Key: "provider", Value: "xai"}, {Key: "model", Value: "m-x"}}}}
	if c.critical {
		row.Set("open", []any{ledger.Object{{Key: "id", Value: "F1"}, {Key: "severity", Value: "critical"}, {Key: "message", Value: "a critical bug"}}})
	}
	b493Sign(t, f, row)
	if !c.critical {
		approved := row.Clone()
		approved.Set("event", "review.approved")
		b493Sign(t, f, approved)
	}
	data, err := json.Marshal(map[string]any{"ticket": id, "status": "reviewed", "approved": !c.critical})
	if err != nil {
		t.Fatal(err)
	}
	closeFixtureWrite(t, filepath.Join(f.top, ".code-forge/queue", id+".done"), data)
	if c.scopes != nil {
		record, err := state.ReadRun(f.home, "r-close-fixture")
		if err != nil {
			t.Fatal(err)
		}
		record.Set("autopilot", ledger.Object{{Key: "grant_id", Value: "g-1"}, {Key: "status", Value: "active"}, {Key: "scopes", Value: c.scopes}, {Key: "deny", Value: []any{}},
			{Key: "delegate", Value: "L2"}, {Key: "until", Value: "2999-01-01T00:00:00.000Z"}, {Key: "caps", Value: ledger.Object{}}, {Key: "stop_at", Value: 0.8},
			{Key: "started_at", Value: "2019-01-01T00:00:00.000Z"}})
		if err := state.SaveRun(f.home, record); err != nil {
			t.Fatal(err)
		}
	}
	if c.green {
		b493Sign(t, f, ledger.Object{{Key: "event", Value: "gate.done"}, {Key: "block", Value: "B1"}, {Key: "all_ok", Value: true}})
	}
	if c.decision {
		b493Sign(t, f, ledger.Object{{Key: "event", Value: "autopilot.decision"}, {Key: "decision_id", Value: "ad-1"}, {Key: "grant_id", Value: "g-1"},
			{Key: "scope", Value: "review:substitute"}, {Key: "acted", Value: true}, {Key: "decision", Value: "substitute"}, {Key: "block", Value: "B1"},
			{Key: "file", Value: p.File}, {Key: "content_hash", Value: p.ContentHash}, {Key: "provider", Value: "xai"}, {Key: "model", Value: "m-x"}})
	}
	return f
}

const b630Fix = "  fix: code-forge autopilot start --run r-close-fixture --until '<time>' --delegate L2 --allow review:substitute\n"

// TestB630CloseCountsSubstituteUnderScope: scope, decision row and a signed
// green gate row: the block closes on the substitute approval.
func TestB630CloseCountsSubstituteUnderScope(t *testing.T) {
	f := b630Fixture(t, b630Case{scopes: []any{"review:substitute"}, green: true, decision: true})
	code, out, errs, reason := b493Close(f)
	if reason != 0 || code != 0 || !strings.HasSuffix(out, "block B1 closed\n") || errs != b493OpenedWarn {
		t.Fatalf("exit=%d reason=%v\nout=%q\nerr=%q", code, reason, out, errs)
	}
}

// TestB630CloseRefusesSubstitute: each missing condition refuses with the
// substitute_review blocker; without the scope its fix line is the owner's
// autopilot start command.
func TestB630CloseRefusesSubstitute(t *testing.T) {
	cases := []struct {
		name string
		c    b630Case
		want string
	}{
		{"no scope", b630Case{scopes: []any{"close:routine"}, green: true, decision: true}, "no active autopilot grant with scope review:substitute (not-allowed)"},
		{"no grant", b630Case{green: true, decision: true}, "no active autopilot grant with scope review:substitute (no-grant)"},
		{"denied", b630Case{scopes: []any{"review:substitute"}, green: true, decision: true}, ""},
		{"no green gate", b630Case{scopes: []any{"review:substitute"}, decision: true}, "no signed green gate row"},
		{"no decision row", b630Case{scopes: []any{"review:substitute"}, green: true}, "no signed autopilot.decision row for the substitute"},
	}
	for _, tc := range cases {
		f := b630Fixture(t, tc.c)
		if tc.name == "denied" {
			record, err := state.ReadRun(f.home, "r-close-fixture")
			if err != nil {
				t.Fatal(err)
			}
			ap := record.Get("autopilot").(ledger.Object)
			ap.Set("deny", []any{"review:substitute"})
			record.Set("autopilot", ap)
			if err := state.SaveRun(f.home, record); err != nil {
				t.Fatal(err)
			}
			tc.want = "no active autopilot grant with scope review:substitute (denied)"
		}
		before := b493LedgerLines(t, f)
		code, out, errs, reason := b493Close(f)
		if reason != 0 || code != 1 || !strings.Contains(out, "substitute_review file0.txt file0.txt is approved only by a substitute review") ||
			!strings.Contains(out, tc.want) || !strings.HasSuffix(out, b630Fix) || !strings.Contains(errs, "block B1 open: substitute_review file0.txt") {
			t.Fatalf("%s: exit=%d reason=%v\nout=%q\nerr=%q", tc.name, code, reason, out, errs)
		}
		if b493LedgerLines(t, f) != before {
			t.Fatalf("%s: a refused close wrote ledger rows", tc.name)
		}
		code, out, _, _ = b493Close(f, "--json")
		if code != 1 || !strings.Contains(out, `"code":"substitute_review","file":"file0.txt"`) ||
			!strings.Contains(out, `"fix":"code-forge autopilot start --run r-close-fixture --until '<time>' --delegate L2 --allow review:substitute"`) {
			t.Fatalf("%s json: exit=%d out=%q", tc.name, code, out)
		}
	}
}

// TestB630CloseSubstituteCritical: a substitute that found a critical wrote
// no approval: close refuses on the open finding even under the scope.
func TestB630CloseSubstituteCritical(t *testing.T) {
	f := b630Fixture(t, b630Case{scopes: []any{"review:substitute"}, green: true, decision: true, critical: true})
	code, out, errs, reason := b493Close(f)
	if reason != 0 || code != 1 || strings.Contains(out, "block B1 closed") || !strings.Contains(errs, "block B1 open:") {
		t.Fatalf("exit=%d reason=%v\nout=%q\nerr=%q", code, reason, out, errs)
	}
}

// TestB630CloseNormalApprovalUnchanged: a file with a normal approval at
// its content closes as before, with no grant.
func TestB630CloseNormalApprovalUnchanged(t *testing.T) {
	f := newCloseReviewFixture(t, 1)
	b493Approve(t, f)
	code, out, _, reason := b493Close(f)
	if reason != 0 || code != 0 || !strings.HasSuffix(out, "block B1 closed\n") {
		t.Fatalf("exit=%d reason=%v out=%q", code, reason, out)
	}
}

// TestB630BinnacleNamesOwnerCommand: the worker's no-scope row (acted false)
// shows in the binnacle as a question to the owner that names the owner's
// autopilot start command.
func TestB630BinnacleNamesOwnerCommand(t *testing.T) {
	row := map[string]any{"event": "autopilot.decision", "decision_id": "ad-1", "scope": "review:substitute", "acted": false, "to_owner": true,
		"owner_reason": "scope review:substitute not-allowed; the owner grants it with code-forge autopilot start --run r1 --until <time> --delegate L2 --allow review:substitute, else the review retries when its provider answers again"}
	label, detail, _ := bnDescribe(row)
	if label != "Question to the owner" || !strings.Contains(detail, "code-forge autopilot start --run r1 --until <time> --delegate L2 --allow review:substitute") {
		t.Fatalf("binnacle %q %q", label, detail)
	}
}

// TestB630StartAllowsSubstituteScope: autopilot start (temp HOME) accepts
// --allow review:substitute and stores it in the grant's scopes.
func TestB630StartAllowsSubstituteScope(t *testing.T) {
	f := newAutopilotFixture(t)
	f.record(t, "r-a", "active", "")
	code, out, errText := autopilotCall(t, "start", "--run", "r-a", "--until", "2026-11-11T15:30:00+00:00", "--delegate", "L2", "--allow", "review:substitute", "--yes")
	if code != 0 || !strings.Contains(out, "allow review:substitute") || errText != "" {
		t.Fatalf("start = %d %q %q", code, out, errText)
	}
	grant := f.read(t, "r-a")["autopilot"].(map[string]any)
	if scopes, _ := grant["scopes"].([]any); len(scopes) != 1 || scopes[0] != "review:substitute" || grant["status"] != "active" {
		t.Fatalf("grant %#v", grant)
	}
}
