package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B498: close reads an object decision with the ledger line's property order
// (JSON.parse's), over rotations, and skips a malformed last line as Node does.
func TestB498CloseOrderedDecisionRows(t *testing.T) {
	dir := t.TempDir()
	rotated := `{"event":"autopilot.decision","decision_id":"d-0","decision":{"z":1,"a":{"y":2,"b":3}}}` + "\n"
	current := `{"event":"block.claim","run":"r1"}` + "\n" +
		`{"event":"autopilot.decision","decision_id":"d-1","decision":["x",{"q":1,"10":2,"c":null}]}` + "\n" +
		`{"event":"autopilot.decision","decision_id":"d-2","decision":"fix"}` + "\n" +
		`{"event":"autopilot.decision","decision_id":"d-3","decision":["a","b"]}` + "\n" +
		`{"event":"broken"`
	if err := os.WriteFile(filepath.Join(dir, "proj.1.jsonl"), []byte(rotated), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "proj.jsonl"), []byte(current), 0o644); err != nil {
		t.Fatal(err)
	}
	all, err := ledger.ReadAllRows(dir, "proj")
	if err != nil {
		t.Fatal(err)
	}
	ordered := closeOrderedRows(dir, "proj", all)
	want := map[string]string{"d-0": `{"z":1,"a":{"y":2,"b":3}}`, "d-1": `["x",{"10":2,"q":1,"c":null}]`}
	if len(ordered) != len(want) {
		t.Fatalf("ordered rows %d, want %d (only decisions holding an object)", len(ordered), len(want))
	}
	for _, v := range all {
		r := v.(map[string]any)
		id, _ := r["decision_id"].(string)
		o, has := ordered[gatecheck.OrderedKey(r)]
		if want[id] == "" {
			if has {
				t.Errorf("%s: read again though its decision holds no object", id)
			}
			continue
		}
		data, _ := ledger.Stringify(o.Get("decision"), false)
		if !has || string(data) != want[id] {
			t.Errorf("%s: decision %s, want JSON.stringify(JSON.parse(line).decision) = %s", id, data, want[id])
		}
	}
	// No decision holding an object: the ledger is not read again.
	if got := closeOrderedRows(filepath.Join(dir, "missing"), "proj", all[1:2]); got != nil {
		t.Fatalf("no object decision must read nothing: %v", got)
	}
}

// B498: an ack digest over agent waivers on Latin file names with marks is
// decided in Go: Node sorts the [file, finding, mac] items with localeCompare
// (ICU: accents count only when the whole strings tie on base letters, so the
// MACs order é.txt and e.txt). The fixed-ts fixture has the same MACs each
// run; every expected byte was captured from CODE_FORGE_NATIVE=0 on it.
func TestB498CloseAckDigestLocaleOrder(t *testing.T) {
	const refusedText = "agent-waivers-not-acked - agent-waivers-not-acked\n  fix: code-forge block ack-waivers B1 --run r-close-fixture\n"
	const refusedJSON = `{"block":"B1","ok":false,"reason":"agent-waivers-not-acked","blockers":[{"code":"agent-waivers-not-acked","file":null,"detail":"agent-waivers-not-acked","fix":"code-forge block ack-waivers B1 --run r-close-fixture"}]}` + "\n"
	const closedText = "reviewed file0.txt ticket b82733d3089a96c3991c870e\nblock B1: agent waiver é.txt F1 reason: minor; verdict: agree\nblock B1: agent waiver e.txt F1 reason: minor; verdict: agree\nblock B1: agent waiver ä.txt F1 reason: minor; verdict: agree\nblock B1 closed\n"
	const closedJSON = `{"block":"B1","event":"closed","status":"closed","reason":null,"close_event":null,"reviewed":[{"file":"file0.txt","ticket":"b82733d3089a96c3991c870e","via":"approved"}],"agent_waivers":[{"file":"é.txt","finding":"F1","severity":"warning","reason":"minor","verdict":"agree"},{"file":"e.txt","finding":"F1","severity":"warning","reason":"minor","verdict":"agree"},{"file":"ä.txt","finding":"F1","severity":"warning","reason":"minor","verdict":"agree"}]}` + "\n"
	for _, c := range []struct {
		order []int
		acked bool
	}{{[]int{0, 1, 2}, false}, {[]int{1, 0, 2}, false}, {[]int{2, 1, 0}, false}, {[]int{2, 0, 1}, true}} {
		for _, asJSON := range []bool{false, true} {
			extra := []string{}
			wantCode, wantOut, wantErr := 1, refusedText, "block B1 open: agent-waivers-not-acked\n"
			if asJSON {
				extra, wantOut = []string{"--json"}, refusedJSON
			}
			if c.acked {
				wantCode, wantOut, wantErr = 0, closedText, b493OpenedWarn
				if asJSON {
					wantOut = closedJSON
				}
			}
			code, out, errs, reason := b493Close(b493AckFiles(t, c.order, "é.txt", "e.txt", "ä.txt"), extra...)
			if reason != 0 || code != wantCode || out != wantOut || errs != wantErr {
				t.Errorf("digest order %v json=%v: exit=%d reason=%v\nout=%q\nerr=%q\nNode: exit=%d out=%q err=%q", c.order, asJSON, code, reason, out, errs, wantCode, wantOut, wantErr)
			}
		}
	}
}

// b498LateCapFixture: an approved block whose ledger has late and cap rows
// naming a file or finding that is "", a number, false or null (waived: the
// number finding has a human waiver with the same number).
func b498LateCapFixture(t *testing.T, waived bool) *closeReviewFixture {
	t.Helper()
	f := newCloseReviewFixture(t, 1)
	b493Approve(t, f)
	late := func(file, finding any) ledger.Object {
		return ledger.Object{{Key: "event", Value: "review.late_finding"}, {Key: "block", Value: "B1"}, {Key: "file", Value: file}, {Key: "finding", Value: finding}, {Key: "severity", Value: "warning"}}
	}
	if waived {
		b493Sign(t, f, late("file0.txt", 5),
			ledger.Object{{Key: "event", Value: "review.waived"}, {Key: "block", Value: "B1"}, {Key: "file", Value: "file0.txt"}, {Key: "finding", Value: 5}, {Key: "by", Value: "human"}, {Key: "reason", Value: "ok"}})
		return f
	}
	b493Sign(t, f, late("", "L1"), late(7, "L1"), late("7", "L1"), late(7, "L1"), late("file0.txt", 5), late(false, nil),
		ledger.Object{{Key: "event", Value: "review.cap"}, {Key: "block", Value: "B1"}, {Key: "file", Value: 3}, {Key: "open", Value: []any{1, 1, "F2", ""}}})
	return f
}

// B498: late and cap rows whose file is "", a number or false, or whose
// finding is missing or not text, close in Go with Node's bytes (captured from
// CODE_FORGE_NATIVE=0 on the same fixtures): String(value) in text, the value
// itself in JSON, Node's dedupe key and fix lines.
func TestB498CloseLateAndCapValues(t *testing.T) {
	for _, c := range []struct {
		name         string
		waived, json bool
		code         int
		out, errs    string
	}{
		{"refused text", false, false, 1, "late_unruled  late finding L1 in  has no ruling\n  fix: no automatic fix: late_unruled\nlate_unruled 7 late finding L1 in 7 has no ruling\n  fix: code-forge block waive B1 L1 --run r-close-fixture --file 7 --reason '<reason>'\nlate_unruled 7 late finding L1 in 7 has no ruling\n  fix: code-forge block waive B1 L1 --run r-close-fixture --file 7 --reason '<reason>'\nlate_unruled file0.txt late finding 5 in file0.txt has no ruling\n  fix: code-forge block reopen B1 --run r-close-fixture --file file0.txt --reason '<reason>'\nlate_unruled false late finding null in false has no ruling\n  fix: no automatic fix: late_unruled\nreview_cap 3 3 stopped at review_cap; finding 1 is open and not waived; exits: block reopen, block waive, review-file --answers, re-decompose\n  fix: code-forge block reopen B1 --run r-close-fixture --file 3 --reason '<reason>'\nreview_cap 3 3 stopped at review_cap; finding F2 is open and not waived; exits: block reopen, block waive, review-file --answers, re-decompose\n  fix: code-forge block waive B1 F2 --run r-close-fixture --file 3 --reason '<reason>'\nreview_cap 3 3 stopped at review_cap; finding  is open and not waived; exits: block reopen, block waive, review-file --answers, re-decompose\n  fix: code-forge block reopen B1 --run r-close-fixture --file 3 --reason '<reason>'\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nblock B1 open: late_unruled L1; late_unruled 7 L1; late_unruled 7 L1; late_unruled 7 L1; late_unruled file0.txt 5; late_unruled; review_cap 3 1; review_cap 3 1; review_cap 3 F2; review_cap 3\n"},
		{"refused json", false, true, 1, "{\"block\":\"B1\",\"ok\":false,\"reason\":\"late_unruled L1; late_unruled 7 L1; late_unruled 7 L1; late_unruled 7 L1; late_unruled file0.txt 5; late_unruled; review_cap 3 1; review_cap 3 1; review_cap 3 F2; review_cap 3\",\"blockers\":[{\"code\":\"late_unruled\",\"file\":\"\",\"detail\":\"late finding L1 in  has no ruling\",\"finding\":\"L1\",\"fix\":\"no automatic fix: late_unruled\"},{\"code\":\"late_unruled\",\"file\":7,\"detail\":\"late finding L1 in 7 has no ruling\",\"finding\":\"L1\",\"fix\":\"code-forge block waive B1 L1 --run r-close-fixture --file 7 --reason '<reason>'\"},{\"code\":\"late_unruled\",\"file\":\"7\",\"detail\":\"late finding L1 in 7 has no ruling\",\"finding\":\"L1\",\"fix\":\"code-forge block waive B1 L1 --run r-close-fixture --file 7 --reason '<reason>'\"},{\"code\":\"late_unruled\",\"file\":\"file0.txt\",\"detail\":\"late finding 5 in file0.txt has no ruling\",\"fix\":\"code-forge block reopen B1 --run r-close-fixture --file file0.txt --reason '<reason>'\"},{\"code\":\"late_unruled\",\"file\":false,\"detail\":\"late finding null in false has no ruling\",\"fix\":\"no automatic fix: late_unruled\"},{\"code\":\"review_cap\",\"file\":3,\"detail\":\"3 stopped at review_cap; finding 1 is open and not waived; exits: block reopen, block waive, review-file --answers, re-decompose\",\"fix\":\"code-forge block reopen B1 --run r-close-fixture --file 3 --reason '<reason>'\"},{\"code\":\"review_cap\",\"file\":3,\"detail\":\"3 stopped at review_cap; finding F2 is open and not waived; exits: block reopen, block waive, review-file --answers, re-decompose\",\"finding\":\"F2\",\"fix\":\"code-forge block waive B1 F2 --run r-close-fixture --file 3 --reason '<reason>'\"},{\"code\":\"review_cap\",\"file\":3,\"detail\":\"3 stopped at review_cap; finding  is open and not waived; exits: block reopen, block waive, review-file --answers, re-decompose\",\"fix\":\"code-forge block reopen B1 --run r-close-fixture --file 3 --reason '<reason>'\"}]}\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nblock B1 open: late_unruled L1; late_unruled 7 L1; late_unruled 7 L1; late_unruled 7 L1; late_unruled file0.txt 5; late_unruled; review_cap 3 1; review_cap 3 1; review_cap 3 F2; review_cap 3\n"},
		{"waived text", true, false, 0, "reviewed file0.txt ticket b82733d3089a96c3991c870e\nblock B1 closed\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\n"},
		{"waived json", true, true, 0, "{\"block\":\"B1\",\"event\":\"closed\",\"status\":\"closed\",\"reason\":null,\"close_event\":null,\"reviewed\":[{\"file\":\"file0.txt\",\"ticket\":\"b82733d3089a96c3991c870e\",\"via\":\"approved\"}]}\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\n"},
	} {
		extra := []string{}
		if c.json {
			extra = []string{"--json"}
		}
		code, out, errs, reason := b493Close(b498LateCapFixture(t, c.waived), extra...)
		if reason != 0 || code != c.code || out != c.out || errs != c.errs {
			t.Errorf("%s: exit=%d reason=%v\nout=%q\nerr=%q\nNode: exit=%d\nout=%q\nerr=%q", c.name, code, reason, out, errs, c.code, c.out, c.errs)
		}
	}
}

// b498SharedClose runs `block close` in a fresh process (TestGatesProcess) with
// stdout and stderr on one pipe, as on a shared terminal, and Node unavailable:
// a hand-over cannot pass.
func b498SharedClose(t *testing.T, f *closeReviewFixture, extra ...string) (int, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(append([]string{"block", "close", "B1", "--run", "r-close-fixture", "--report", f.report, "--transcript", f.transcript}, extra...))
	cmd := exec.Command(exe, "-test.run=^TestGatesProcess$")
	cmd.Dir = f.top
	cmd.Env = append(os.Environ(), "HOME="+f.home, "CODE_FORGE_NO_ERROR_LOG=1", "CODE_FORGE_HANDOVER_LOG=0", "CODE_FORGE_NODE=/nonexistent-code-forge-node", "CF_GATES_PROCESS=1", "CF_GATES_ARGS="+string(encoded))
	var both bytes.Buffer
	cmd.Stdout, cmd.Stderr = &both, &both
	code := 0
	if err := cmd.Run(); err != nil {
		e, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		code = e.ExitCode()
	}
	return code, both.String()
}

// B498: on a shared terminal Node writes each line when it happens, so the
// stderr claim and WARN lines come before the stdout result (JSON or text) and
// a refused --json close prints its stderr summary before the JSON. Before B498
// Go held stdout and stderr apart and printed all stdout first. The expected
// bytes are a pty capture (script -q) of Node (CODE_FORGE_NATIVE=0) on the
// same fixtures.
func TestB498CloseSharedStreamOrder(t *testing.T) {
	const warn = "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\n"
	const claim = "block B1 auto-claimed extra.txt\n"
	const reviewedBoth = `[{"file":"extra.txt","ticket":"0a276b0b4e62b3cc2d085cb0","via":"approved"},{"file":"file0.txt","ticket":"b82733d3089a96c3991c870e","via":"approved"}]`
	autoClaim := func(t *testing.T) *closeReviewFixture { return b493AutoClaimFixture(t, "2020-01-01T00:00:00.000Z") }
	approved := func(t *testing.T) *closeReviewFixture {
		f := newCloseReviewFixture(t, 1)
		b493Approve(t, f)
		return f
	}
	reason := "late_unruled L1; late_unruled 7 L1; late_unruled 7 L1; late_unruled 7 L1; late_unruled file0.txt 5; late_unruled; review_cap 3 1; review_cap 3 1; review_cap 3 F2; review_cap 3"
	cases := []struct {
		name  string
		build func(*testing.T) *closeReviewFixture
		extra []string
		code  int
		want  string
	}{
		{"auto-claim json", autoClaim, []string{"--auto-claim", "--json"}, 0,
			claim + `{"block":"B1","event":"closed","status":"closed","reason":null,"close_event":null,"reviewed":` + reviewedBoth + "}\n"},
		{"auto-claim text", autoClaim, []string{"--auto-claim"}, 0,
			claim + "reviewed extra.txt ticket 0a276b0b4e62b3cc2d085cb0\nreviewed file0.txt ticket b82733d3089a96c3991c870e\nblock B1 closed\n"},
		{"warn json", approved, []string{"--json"}, 0,
			warn + `{"block":"B1","event":"closed","status":"closed","reason":null,"close_event":null,"reviewed":[{"file":"file0.txt","ticket":"b82733d3089a96c3991c870e","via":"approved"}]}` + "\n"},
		{"warn text", approved, nil, 0,
			warn + "reviewed file0.txt ticket b82733d3089a96c3991c870e\nblock B1 closed\n"},
		{"refused json", func(t *testing.T) *closeReviewFixture { return b498LateCapFixture(t, false) }, []string{"--json"}, 1,
			warn + "block B1 open: " + reason + "\n" + `{"block":"B1","ok":false,"reason":"` + reason + `","blockers":`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.build(t)
			code, got := b498SharedClose(t, f, tc.extra...)
			if code != tc.code {
				t.Fatalf("exit=%d want %d\n%s", code, tc.code, got)
			}
			if tc.code == 1 {
				if !strings.HasPrefix(got, tc.want) {
					t.Fatalf("shared stream\n got %q\nwant prefix %q", got, tc.want)
				}
				return
			}
			if got != tc.want {
				t.Fatalf("shared stream\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// B498: Node's --auto-claim signs its claims after the ledger, run-ended and
// key reads, so a close that then refuses on the transcript read or on
// --worker-pid has already claimed (fact: CODE_FORGE_NATIVE=0 on the same
// fixture). Before B498 Go's preflight refused without claiming. A run that
// has ended refuses before any claim in both.
func TestB498AutoClaimBeforeLaterRefusal(t *testing.T) {
	const claim = "block B1 auto-claimed extra.txt\n"
	cases := []struct {
		name        string
		args        func(f *closeReviewFixture) []string
		code        int
		events, out string
		errs        func(f *closeReviewFixture) string
	}{
		{"transcript missing", func(f *closeReviewFixture) []string {
			return []string{"--report", f.report, "--transcript", filepath.Join(f.home, "nope.txt")}
		}, 1, "block.claim", claim, func(f *closeReviewFixture) string {
			return "block close: ENOENT: no such file or directory, open '" + filepath.Join(f.home, "nope.txt") + "'\n"
		}},
		{"worker-pid not a number", func(f *closeReviewFixture) []string {
			return []string{"--report", f.report, "--transcript", f.transcript, "--worker-pid", "abc"}
		}, 2, "block.claim", claim, func(*closeReviewFixture) string {
			return "block close: --worker-pid must be a positive integer\n"
		}},
		{"run ended", func(f *closeReviewFixture) []string {
			r, err := state.ReadRun(f.home, "r-close-fixture")
			if err != nil {
				t.Fatal(err)
			}
			r.Set("status", "ended")
			if err := state.SaveRun(f.home, r); err != nil {
				t.Fatal(err)
			}
			return []string{"--report", f.report, "--transcript", f.transcript}
		}, 1, "", "", func(*closeReviewFixture) string {
			return "block close: run r-close-fixture has ended\n"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := b493AutoClaimFixture(t, "2099-01-01T00:00:00.000Z")
			var out, errs bytes.Buffer
			var reason handoverReason
			code := closeFlagsStep(append(append([]string{"B1", "--run", "r-close-fixture"}, tc.args(f)...), "--auto-claim"), &landStep{stdout: &out, stderr: &errs}, &reason)
			if reason != 0 || code != tc.code || out.String() != tc.out || errs.String() != tc.errs(f) {
				t.Fatalf("exit=%d reason=%v\nout %q\nerr %q", code, reason, out.String(), errs.String())
			}
			if got := b493Events(t, f); got != tc.events {
				t.Fatalf("rows %q, want %q", got, tc.events)
			}
		})
	}
}
