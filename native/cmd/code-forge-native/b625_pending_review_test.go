package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B625: a block whose only blockers are files the reviewer could not review
// (reviewer infrastructure: timeout, provider down, worker error) reports
// COMPLETE_PENDING_REVIEW, its fix is the review command, and a later review
// lets it close with no coder session.

type b625Ready struct {
	Ready         bool     `json:"ready"`
	State         string   `json:"state"`
	PendingReview []string `json:"pending_review"`
	Blockers      []struct {
		Code, Detail, Fix string
		File              *string
	} `json:"blockers"`
}

func b625Parse(t *testing.T, code int, stdout, stderr string) b625Ready {
	t.Helper()
	var got b625Ready
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("want one JSON line: exit %d: %v\nstdout=%q\nstderr=%q", code, err, stdout, stderr)
	}
	return got
}

// b625Result signs a review.result row of B1 for sample.txt at its current content.
func (f *preflightFixture) b625Result(status, reason string, approved bool, open []any) {
	f.t.Helper()
	hash, err := gitx.ContentHash(f.tree, "sample.txt")
	if err != nil {
		f.t.Fatal(err)
	}
	row := ledger.Object{{Key: "event", Value: "review.result"}, {Key: "ticket", Value: "0123456789abcdef01234567"}, {Key: "block", Value: "B1"}, {Key: "file", Value: "sample.txt"},
		{Key: "content_hash", Value: hash}, {Key: "status", Value: status}, {Key: "approved", Value: approved}, {Key: "open", Value: open}}
	if reason != "" {
		row = append(row, ledger.Field{Key: "reason", Value: reason})
	}
	f.sign(row)
}

// b625Pin pins this test process as the run's live worker.
func (f *preflightFixture) b625Pin() {
	f.t.Helper()
	err := state.UpdateRun(f.home, f.run, func(record *ledger.Object) error {
		record.Set("worker", ledger.Object{{Key: "pid", Value: os.Getpid()}, {Key: "started_at", Value: statusStart(float64(os.Getpid()))}})
		return nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
}

func b625ReviewFix(run string) string {
	return "code-forge review-file sample.txt --block B1 --run " + run
}

func TestB625ReadyReviewerTimeoutIsPendingReview(t *testing.T) {
	f := newReadyFixture(t)
	f.setSnapshot(readyBaseConfig+`}`, nil)
	f.b625Pin()
	f.b625Result("unavailable", "timeout", false, []any{})
	report := f.reportFile()
	for _, extra := range [][]string{nil, {"--report", report}} {
		args := append([]string{"block", "ready", "B1", "--run", f.run, "--json"}, extra...)
		got := b625Parse(f.callCode(args...))
		if got.Ready || got.State != "COMPLETE_PENDING_REVIEW" || strings.Join(got.PendingReview, ",") != "sample.txt" || len(got.Blockers) == 0 {
			t.Fatalf("%v: want not ready, state COMPLETE_PENDING_REVIEW for sample.txt; got %+v", extra, got)
		}
		for _, b := range got.Blockers {
			if b.Fix != b625ReviewFix(f.run) || b.File == nil || *b.File != "sample.txt" {
				t.Fatalf("%v: every blocker's fix is the review command, not a coder redo; got %+v", extra, got.Blockers)
			}
		}
		code, stdout, stderr := f.call(append([]string{"block", "ready", "B1", "--run", f.run}, extra...)...)
		if code != 1 || !strings.Contains(stdout, "block B1 COMPLETE_PENDING_REVIEW: ") || !strings.Contains(stdout, "no coder session needed") || strings.Contains(stderr, "node") {
			t.Fatalf("%v text: want exit 1 and the state line; got %d stdout=%q stderr=%q", extra, code, stdout, stderr)
		}
	}
}

func TestB625ReadyReviewWithFindingsKeepsState(t *testing.T) {
	for _, name := range []string{"findings", "timeout-then-findings"} {
		t.Run(name, func(t *testing.T) {
			f := newReadyFixture(t)
			f.setSnapshot(readyBaseConfig+`}`, nil)
			f.b625Pin()
			f.b625Pin()
			if name == "timeout-then-findings" {
				f.b625Result("unavailable", "timeout", false, []any{})
			}
			f.b625Result("reviewed", "", false, []any{ledger.Object{{Key: "id", Value: "F1"}, {Key: "severity", Value: "warning"}}})
			code, stdout, stderr := f.call("block", "ready", "B1", "--run", f.run, "--json")
			got := b625Parse(t, code, stdout, stderr)
			if code != 1 || got.Ready || got.State != "" || got.PendingReview != nil || strings.Contains(stdout, "COMPLETE_PENDING_REVIEW") {
				t.Fatalf("a review with findings is not reviewer-down: got exit %d %s", code, stdout)
			}
			for _, b := range got.Blockers {
				if strings.HasPrefix(b.Fix, "code-forge review-file sample.txt") {
					t.Fatalf("a review with findings keeps its own fixes, got %+v", got.Blockers)
				}
			}
		})
	}
}

func TestB625ReadyCoderFailureStaysFailed(t *testing.T) {
	f := newReadyFixture(t)
	f.setSnapshot(readyBaseConfig+`,"context":{"require_coder_tests":true}}`, nil)
	f.b625Pin()
	f.b625Result("unavailable", "worker-error", false, []any{})
	report := filepath.Join(f.home, "failed.txt")
	f.write(f.home, "failed.txt", "TESTS go test ./... exit=1\n===BLOCK B1 FAILED: tests fail===\n")
	code, stdout, stderr := f.call("block", "ready", "B1", "--run", f.run, "--report", report, "--json")
	got := b625Parse(t, code, stdout, stderr)
	if code != 1 || got.Ready || got.State != "" || strings.Contains(stdout, "COMPLETE_PENDING_REVIEW") {
		t.Fatalf("a coder failure is not pending review: got exit %d %s", code, stdout)
	}
	found := false
	for _, b := range got.Blockers {
		found = found || b.Code == "coder_report_failed" && strings.Contains(b.Detail, "COMPLETE=== sentinel") && strings.Contains(b.Fix, "add ===BLOCK B1 COMPLETE===")
	}
	if !found {
		t.Fatalf("want the coder report failure with its coder fix, got %+v", got.Blockers)
	}
}

func TestB625PreflightListsReviewUnavailable(t *testing.T) {
	f := newPreflightFixture(t)
	f.b625Result("unavailable", "timeout", false, []any{})
	code, stdout, stderr := f.call("block", "ready", "B1", "--run", f.run, "--preflight", "--json")
	got := b625Parse(t, code, stdout, stderr)
	if code != 1 || got.Ready || got.State != "COMPLETE_PENDING_REVIEW" || len(got.Blockers) != 1 {
		t.Fatalf("preflight: want exit 1, state and one blocker; got exit %d %s", code, stdout)
	}
	b := got.Blockers[0]
	if b.Code != "review_unavailable" || b.File == nil || *b.File != "sample.txt" || b.Fix != b625ReviewFix(f.run) || !strings.Contains(b.Detail, "(timeout)") {
		t.Fatalf("preflight blocker: want review_unavailable on sample.txt with the review fix; got %+v", b)
	}
	code, stdout, _ = f.call("block", "ready", "B1", "--run", f.run, "--preflight")
	if code != 1 || !strings.Contains(stdout, "review_unavailable sample.txt ") || !strings.Contains(stdout, "block B1 COMPLETE_PENDING_REVIEW: ") {
		t.Fatalf("preflight text: got %d %q", code, stdout)
	}
	// gates run is not stopped by it: review runs next to the gate.
	gcode, _, gerr := f.call("gates", "run", "--run", f.run, "--block", "B1")
	if gcode != 0 || f.gateRuns() != 1 {
		t.Fatalf("gates run: want exit 0 and one test run, got exit %d, %d runs, stderr=%q", gcode, f.gateRuns(), gerr)
	}
}

func TestB625StatusShowsPendingReview(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"timeout", "COMPLETE_PENDING_REVIEW"}, {"findings", "open"}, {"gate-red", "open"}, {"approved-later", "open"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReadyFixture(t)
			f.setSnapshot(readyBaseConfig+`}`, nil)
			f.b625Pin()
			f.b625Pin()
			if tc.name != "gate-red" {
				f.sign(ledger.Object{{Key: "event", Value: "gate.done"}, {Key: "block", Value: "B1"}, {Key: "all_ok", Value: true}})
			}
			if tc.name == "findings" {
				f.b625Result("reviewed", "", false, []any{ledger.Object{{Key: "id", Value: "F1"}, {Key: "severity", Value: "warning"}}})
			} else {
				f.b625Result("unavailable", "timeout", false, []any{})
			}
			if tc.name == "approved-later" {
				f.b625Result("reviewed", "", true, []any{})
			}
			code, stdout, stderr := f.call("status", "--run", f.run, "--json")
			var got struct {
				Runs []struct {
					Blocks []struct{ ID, State string }
				}
			}
			if err := json.Unmarshal([]byte(stdout), &got); err != nil || code != 0 || len(got.Runs) != 1 || len(got.Runs[0].Blocks) != 1 {
				t.Fatalf("status --json: exit %d %v stdout=%q stderr=%q", code, err, stdout, stderr)
			}
			if s := got.Runs[0].Blocks[0].State; s != tc.want {
				t.Fatalf("status block state: want %s, got %s", tc.want, s)
			}
			_, text, _ := f.call("status", "--run", f.run)
			if !strings.Contains(text, "\nB1 "+tc.want+" ") {
				t.Fatalf("status text: want B1 %s, got %q", tc.want, text)
			}
		})
	}
}

// A reviewer timeout leaves the block pending review; close refuses it as an
// unreviewed file; a later review that approves lets close finish with the
// same coder report (no coder session).
func TestB625LaterReviewClosesWithoutCoder(t *testing.T) {
	f := newCloseReviewFixture(t, 1)
	if err := os.MkdirAll(filepath.Join(f.top, ".code-forge/queue"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := f.result(f.parts[0], "unavailable"); err != nil {
		t.Fatal(err)
	}
	p := &preflightFixture{t: t, home: f.home, repo: f.top}
	got := b625Parse(p.callCode("block", "ready", "B1", "--run", "r-close-fixture", "--report", f.report, "--json"))
	if got.Ready || got.State != "COMPLETE_PENDING_REVIEW" || strings.Join(got.PendingReview, ",") != "file0.txt" {
		t.Fatalf("after a reviewer timeout: want COMPLETE_PENDING_REVIEW for file0.txt, got %+v", got)
	}
	done := f.reviewer("unavailable")
	code, out, errs := f.close()
	if err := <-done; err != nil {
		t.Error(err)
	}
	if code != 1 || !strings.Contains(out, "review_pending file0.txt") || strings.Contains(out, "block B1 closed") {
		t.Fatalf("close while the reviewer is still down: want refusal, got exit %d out=%s err=%s", code, out, errs)
	}
	done = f.reviewer("approved")
	code, out, errs = f.close()
	if err := <-done; err != nil {
		t.Error(err)
	}
	if code != 0 || !strings.Contains(out, "block B1 closed") {
		t.Fatalf("close after a later review: want closed, got exit %d out=%s err=%s", code, out, errs)
	}
	record, err := state.ReadRun(f.home, "r-close-fixture")
	if err != nil || blockObject(blockObject(record.Get("blocks")).Get("B1")).Get("status") != "closed" {
		t.Fatalf("block must be closed: %v", err)
	}
	rows, err := ledger.ReadAllRows(filepath.Join(f.home, ".code-forge/ledger"), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range rows {
		if r, _ := v.(map[string]any); r["event"] == "session" && r["role"] == "coder" {
			t.Fatalf("no coder session may run, got %v", r)
		}
	}
}

// callCode is call with its results in the order b625Parse takes them.
func (f *preflightFixture) callCode(args ...string) (*testing.T, int, string, string) {
	code, stdout, stderr := f.call(args...)
	return f.t, code, stdout, stderr
}
