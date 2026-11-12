package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
	"github.com/ricardov03/code-forge/native/ticket"
)

func b493SetConfig(t *testing.T, f *closeReviewFixture, cfg ledger.Object) {
	t.Helper()
	record, err := state.ReadRun(f.home, "r-close-fixture")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := canon.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	hash := hex.EncodeToString(digest[:])
	record.Set("config", ledger.Object{{Key: "hash", Value: hash}, {Key: "snapshots", Value: ledger.Object{{Key: hash, Value: cfg}}}})
	if err := state.SaveRun(f.home, record); err != nil {
		t.Fatal(err)
	}
}

func b493Sign(t *testing.T, f *closeReviewFixture, rows ...ledger.Object) []ledger.Object {
	t.Helper()
	out := []ledger.Object{}
	for _, row := range rows {
		signed, err := state.WriteSigned(f.home, "r-close-fixture", row)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, signed)
	}
	return out
}

func b493Close(f *closeReviewFixture, extra ...string) (int, string, string, handoverReason) {
	var out, errs bytes.Buffer
	var reason handoverReason
	args := append([]string{"B1", "--run", "r-close-fixture", "--report", f.report, "--transcript", f.transcript}, extra...)
	code := blockCloseStepWithReason(args, &landStep{stdout: &out, stderr: &errs}, &reason)
	return code, out.String(), errs.String(), reason
}

func b493Approve(t *testing.T, f *closeReviewFixture) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(f.top, ".code-forge/queue"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := f.result(f.parts[0], "approved"); err != nil {
		t.Fatal(err)
	}
}

const b493OpenedWarn = "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\n"

// B493: a late finding with no file closes in Go with Node's bytes (captured
// from CODE_FORGE_NATIVE=0 on the same fixture): the refusal names
// "undefined" and the JSON blocker has "file":null.
func TestB493CloseLateFindingWithNoFile(t *testing.T) {
	for _, json := range []bool{false, true} {
		f := newCloseReviewFixture(t, 1)
		b493Approve(t, f)
		b493Sign(t, f, ledger.Object{{Key: "event", Value: "review.late_finding"}, {Key: "block", Value: "B1"}, {Key: "finding", Value: "L1"}, {Key: "severity", Value: "warning"}})
		extra := []string{}
		want := "late_unruled - late finding L1 in undefined has no ruling\n  fix: no automatic fix: late_unruled\n"
		if json {
			extra = []string{"--json"}
			want = `{"block":"B1","ok":false,"reason":"late_unruled L1","blockers":[{"code":"late_unruled","file":null,"detail":"late finding L1 in undefined has no ruling","finding":"L1","fix":"no automatic fix: late_unruled"}]}` + "\n"
		}
		code, out, errs, reason := b493Close(f, extra...)
		if reason != 0 || code != 1 || out != want || errs != b493OpenedWarn+"block B1 open: late_unruled L1\n" {
			t.Fatalf("json=%v: exit=%d reason=%v\nout=%q\nerr=%q", json, code, reason, out, errs)
		}
	}
	// A nit ruling with no file clears it, as in Node: the block closes.
	f := newCloseReviewFixture(t, 1)
	b493Approve(t, f)
	b493Sign(t, f, ledger.Object{{Key: "event", Value: "review.late_finding"}, {Key: "block", Value: "B1"}, {Key: "finding", Value: "L1"}, {Key: "severity", Value: "warning"}},
		ledger.Object{{Key: "event", Value: "review.late_ruling"}, {Key: "block", Value: "B1"}, {Key: "finding", Value: "L1"}, {Key: "ruling", Value: "nit"}})
	code, out, errs, reason := b493Close(f)
	if reason != 0 || code != 0 || out != "reviewed file0.txt ticket b82733d3089a96c3991c870e\nblock B1 closed\n" || errs != b493OpenedWarn {
		t.Fatalf("ruled: exit=%d reason=%v\nout=%q\nerr=%q", code, reason, out, errs)
	}
}

func b493AckFixture(t *testing.T, match bool) *closeReviewFixture {
	f := newCloseReviewFixture(t, 1)
	b493Approve(t, f)
	b493SetConfig(t, f, ledger.Object{{Key: "review", Value: ledger.Object{{Key: "close_wait_s", Value: 1}, {Key: "agent_waivers", Value: ledger.Object{{Key: "require_ack", Value: true}}}}}, {Key: "context", Value: ledger.Object{{Key: "require_coder_tests", Value: true}}}})
	set := []any{}
	for _, file := range []string{"é.txt", "e.txt", "ä.txt"} {
		rows := b493Sign(t, f, ledger.Object{{Key: "event", Value: "review.triage"}, {Key: "block", Value: "B1"}, {Key: "file", Value: file}, {Key: "finding", Value: "F1"}, {Key: "severity", Value: "warning"}},
			ledger.Object{{Key: "event", Value: "review.waived"}, {Key: "block", Value: "B1"}, {Key: "file", Value: file}, {Key: "finding", Value: "F1"}, {Key: "by", Value: "agent"}, {Key: "verdict", Value: "agree"}, {Key: "severity", Value: "warning"}, {Key: "reason", Value: "minor"}})
		set = append([]any{[]any{file, "F1", rows[1].Get("mac")}}, set...)
	}
	digest := "0000"
	if match {
		data, err := ledger.Stringify(set, false)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		digest = hex.EncodeToString(sum[:])
	}
	b493Sign(t, f, ledger.Object{{Key: "event", Value: "agent_waivers.acked"}, {Key: "block", Value: "B1"}, {Key: "by", Value: "human"}, {Key: "digest", Value: digest}})
	return f
}

// B493: agent waivers on non-ASCII file names with a signed ack digest that
// matches no order: Node refuses agent-waivers-not-acked, and so does Go.
// A digest that matches one order keeps its own hand-over reason.
func TestB493CloseAckDigestNonASCII(t *testing.T) {
	code, out, errs, reason := b493Close(b493AckFixture(t, false))
	if reason != 0 || code != 1 || out != "agent-waivers-not-acked - agent-waivers-not-acked\n  fix: code-forge block ack-waivers B1 --run r-close-fixture\n" || errs != "block B1 open: agent-waivers-not-acked\n" {
		t.Fatalf("exit=%d reason=%v\nout=%q\nerr=%q", code, reason, out, errs)
	}
	code, out, errs, reason = b493Close(b493AckFixture(t, true))
	if reason != hCloseAckDigestOrder || out != "" || errs != "" {
		t.Fatalf("matching order: exit=%d reason=%v out=%q err=%q; want the close-ack-digest-order hand-over before any output", code, reason, out, errs)
	}
}

// b493AutoClaimFixture is an approved close fixture with an autopilot grant
// (until is the grant's end), a recorded tree at open, one unowned changed
// file (extra.txt), a live worker file and a fake reviewer that approves
// extra.txt once it is queued.
func b493AutoClaimFixture(t *testing.T, until string) *closeReviewFixture {
	t.Helper()
	f := newCloseReviewFixture(t, 1)
	b493Approve(t, f)
	record, err := state.ReadRun(f.home, "r-close-fixture")
	if err != nil {
		t.Fatal(err)
	}
	record.Set("autopilot", ledger.Object{{Key: "grant_id", Value: "g-1"}, {Key: "status", Value: "active"}, {Key: "scopes", Value: []any{"close:routine"}}, {Key: "deny", Value: []any{}}, {Key: "delegate", Value: "L2"}, {Key: "until", Value: until}, {Key: "caps", Value: ledger.Object{}}, {Key: "stop_at", Value: 0.8}, {Key: "started_at", Value: "2019-01-01T00:00:00.000Z"}})
	blocks := record.Get("blocks").(ledger.Object)
	entry := blocks.Get("B1").(ledger.Object)
	entry.Set("tree_at_open", ledger.Object{})
	blocks.Set("B1", entry)
	record.Set("blocks", blocks)
	if err := state.SaveRun(f.home, record); err != nil {
		t.Fatal(err)
	}
	closeFixtureWrite(t, filepath.Join(f.top, "extra.txt"), []byte("extra\n"))
	stamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	pid := strconv.Itoa(os.Getpid())
	closeFixtureWrite(t, filepath.Join(f.top, ".code-forge", "workers", pid+"-unknown.json"), []byte(`{"pid":`+pid+`,"start_time":"unknown","run":"r-close-fixture","started_at":"`+stamp+`","heartbeat_at":"`+stamp+`"}`))
	hash, err := gitx.ContentHash(f.top, "extra.txt")
	if err != nil {
		t.Fatal(err)
	}
	extra := ticket.Parts{Run: "r-close-fixture", Block: "B1", File: "extra.txt", ContentHash: hash}
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
			}
			entries, _ := os.ReadDir(filepath.Join(f.top, ".code-forge/queue"))
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ticket.ReviewID(extra)) && !strings.HasSuffix(e.Name(), ".done") {
					_ = f.result(extra, "approved")
					return
				}
			}
		}
	}()
	return f
}

func b493CloseAutoClaim(f *closeReviewFixture, extra ...string) (int, string, string, handoverReason) {
	var out, errs bytes.Buffer
	var reason handoverReason
	args := append([]string{"B1", "--run", "r-close-fixture", "--report", f.report, "--transcript", f.transcript, "--auto-claim"}, extra...)
	code := closeFlagsStep(args, &landStep{stdout: &out, stderr: &errs}, &reason)
	return code, out.String(), errs.String(), reason
}

// B493: --auto-claim on a run with an autopilot grant runs in Go. Node's bytes
// and rows (CODE_FORGE_NATIVE=0 on the same fixture): an expired grant signs
// autopilot.expire once, before the block.claim row; an active grant signs none.
func TestB493CloseAutoClaimWithAutopilotGrant(t *testing.T) {
	const stdout = "block B1 auto-claimed extra.txt\nreviewed extra.txt ticket 0a276b0b4e62b3cc2d085cb0\nreviewed file0.txt ticket b82733d3089a96c3991c870e\nblock B1 closed\n"
	for _, tc := range []struct {
		name, until string
		expire      int
	}{{"expired", "2020-01-01T00:00:00.000Z", 1}, {"active", "2099-01-01T00:00:00.000Z", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			f := b493AutoClaimFixture(t, tc.until)
			code, out, errs, reason := b493CloseAutoClaim(f)
			if code != 0 || out != stdout || errs != "" {
				t.Fatalf("exit=%d reason=%v\nstdout %q\nstderr %q", code, reason, out, errs)
			}
			all, err := ledger.ReadAllRows(filepath.Join(f.home, ".code-forge/ledger"), "fixture")
			if err != nil {
				t.Fatal(err)
			}
			events := []string{}
			for _, v := range all {
				if e := v.(map[string]any)["event"].(string); e == "autopilot.expire" || e == "block.claim" || e == "block.close" {
					events = append(events, e)
				}
			}
			want := []string{"block.claim", "block.close"}
			if tc.expire == 1 {
				want = append([]string{"autopilot.expire"}, want...)
			}
			if strings.Join(events, ",") != strings.Join(want, ",") {
				t.Fatalf("rows %v, want %v", events, want)
			}
			saved, err := state.ReadRun(f.home, "r-close-fixture")
			if err != nil {
				t.Fatal(err)
			}
			if tc.expire == 1 && saved.Get("autopilot").(ledger.Object).Get("status") != "expired" {
				t.Fatalf("grant not expired: %v", saved.Get("autopilot"))
			}
		})
	}
	t.Run("json", func(t *testing.T) {
		f := b493AutoClaimFixture(t, "2020-01-01T00:00:00.000Z")
		code, out, errs, reason := b493CloseAutoClaim(f, "--json")
		want := `{"block":"B1","event":"closed","status":"closed","reason":null,"close_event":null,"reviewed":[{"file":"extra.txt","ticket":"0a276b0b4e62b3cc2d085cb0","via":"approved"},{"file":"file0.txt","ticket":"b82733d3089a96c3991c870e","via":"approved"}]}` + "\n"
		if code != 0 || out != want || errs != "block B1 auto-claimed extra.txt\n" {
			t.Fatalf("exit=%d reason=%v\nstdout %q\nstderr %q", code, reason, out, errs)
		}
	})
}
