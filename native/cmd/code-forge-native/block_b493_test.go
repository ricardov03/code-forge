package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/humanask"
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
	if !match {
		return b493AckFiles(t, nil, "é.txt", "e.txt", "ä.txt")
	}
	return b493AckFiles(t, []int{2, 1, 0}, "é.txt", "e.txt", "ä.txt")
}

// b493AckFiles signs agent waivers of F1 on files (in that ledger order, at a
// fixed ts so the MACs are the same each run) and an ack whose digest is over
// the waivers in digest order (indices into files), or "0000" when nil.
func b493AckFiles(t *testing.T, digestOrder []int, files ...string) *closeReviewFixture {
	f := newCloseReviewFixture(t, 1)
	b493Approve(t, f)
	b493SetConfig(t, f, ledger.Object{{Key: "review", Value: ledger.Object{{Key: "close_wait_s", Value: 1}, {Key: "agent_waivers", Value: ledger.Object{{Key: "require_ack", Value: true}}}}}, {Key: "context", Value: ledger.Object{{Key: "require_coder_tests", Value: true}}}})
	const ts = "2026-11-11T10:00:00.000Z"
	items := []any{}
	for _, file := range files {
		rows := b493Sign(t, f, ledger.Object{{Key: "event", Value: "review.triage"}, {Key: "block", Value: "B1"}, {Key: "file", Value: file}, {Key: "finding", Value: "F1"}, {Key: "severity", Value: "warning"}, {Key: "ts", Value: ts}},
			ledger.Object{{Key: "event", Value: "review.waived"}, {Key: "block", Value: "B1"}, {Key: "file", Value: file}, {Key: "finding", Value: "F1"}, {Key: "by", Value: "agent"}, {Key: "verdict", Value: "agree"}, {Key: "severity", Value: "warning"}, {Key: "reason", Value: "minor"}, {Key: "ts", Value: ts}})
		items = append(items, []any{file, "F1", rows[1].Get("mac")})
	}
	digest := "0000"
	if digestOrder != nil {
		set := []any{}
		for _, i := range digestOrder {
			set = append(set, items[i])
		}
		data, err := ledger.Stringify(set, false)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		digest = hex.EncodeToString(sum[:])
	}
	b493Sign(t, f, ledger.Object{{Key: "event", Value: "agent_waivers.acked"}, {Key: "block", Value: "B1"}, {Key: "by", Value: "human"}, {Key: "digest", Value: digest}, {Key: "ts", Value: ts}})
	return f
}

// B493: agent waivers on non-ASCII file names with a signed ack digest that
// matches no order: Node refuses agent-waivers-not-acked, and so does Go.
// A digest that matches one order of names Go cannot collate keeps its own
// hand-over reason.
func TestB493CloseAckDigestNonASCII(t *testing.T) {
	code, out, errs, reason := b493Close(b493AckFixture(t, false))
	if reason != 0 || code != 1 || out != "agent-waivers-not-acked - agent-waivers-not-acked\n  fix: code-forge block ack-waivers B1 --run r-close-fixture\n" || errs != "block B1 open: agent-waivers-not-acked\n" {
		t.Fatalf("exit=%d reason=%v\nout=%q\nerr=%q", code, reason, out, errs)
	}
	// B498: Latin file names sort in Go as Node sorts them; another script
	// (Cyrillic) whose digest matches one order still hands over.
	code, out, errs, reason = b493Close(b493AckFiles(t, []int{1, 0, 2}, "б.txt", "а.txt", "в.txt"))
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

// b493HookFixture is a reviewed block whose run config sets hooks.on_block_end
// to argv; the coder transcript is text.
func b493HookFixture(t *testing.T, argv []any, text string) *closeReviewFixture {
	t.Helper()
	f := newCloseReviewFixture(t, 1)
	b493Approve(t, f)
	b493SetConfig(t, f, ledger.Object{{Key: "review", Value: ledger.Object{{Key: "close_wait_s", Value: 1}}},
		{Key: "context", Value: ledger.Object{{Key: "require_coder_tests", Value: true}}},
		{Key: "hooks", Value: ledger.Object{{Key: "on_block_end", Value: ledger.Object{{Key: "argv", Value: argv}}}}}})
	if err := os.WriteFile(f.transcript, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return f
}

// b493Ready runs `block ready` through the public router in a fresh process
// (TestGatesProcess) with Node unavailable: a hand-over cannot pass.
func b493Ready(t *testing.T, f *closeReviewFixture, extra ...string) (int, string, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(append([]string{"block", "ready", "B1", "--run", "r-close-fixture", "--report", f.report, "--transcript", f.transcript}, extra...))
	cmd := exec.Command(exe, "-test.run=^TestGatesProcess$")
	cmd.Dir = f.top
	cmd.Env = append(os.Environ(), "HOME="+f.home, "CODE_FORGE_NO_ERROR_LOG=1", "CODE_FORGE_HANDOVER_LOG=0", "CODE_FORGE_NODE=/nonexistent-code-forge-node", "CF_GATES_PROCESS=1", "CF_GATES_ARGS="+string(encoded))
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	code := 0
	if err := cmd.Run(); err != nil {
		e, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		code = e.ExitCode()
	}
	return code, out.String(), errOut.String()
}

// B493: a coder transcript that runs the on_block_end hook's command is Node's
// B64b rule_break (first close check) in Go's `block ready` and `block close`;
// the bytes are Node's (CODE_FORGE_NATIVE=0 on the same fixtures). Before B493
// ready handed over and close skipped the check and closed the block.
func TestB493HookCommandInTranscript(t *testing.T) {
	fix := func(home string) string {
		return "code-forge block close B1 --run r-close-fixture --decide --reason '<reason>' --report " + home + "/report.txt --transcript " + home + "/transcript.txt"
	}
	cases := []struct {
		name string
		argv []any
		text string
		line string // "" when the block is ready and closes
	}{
		{"hit", []any{"/usr/local/bin/notify-hook"}, "$ ls\n$ notify-hook --x\nok\n", "2"},
		{"mentioned", []any{"/usr/local/bin/notify-hook"}, "$ echo notify-hook is mentioned\n", "1"},
		{"quoted", []any{"/usr/local/bin/notify-hook"}, "$ 'notify-hook' now\n", "1"},
		{"npx exe", []any{"/usr/local/bin/notify-hook"}, "$ npx -y notify-hook.EXE\n", "1"},
		{"first argument", []any{"notify-hook", "--done"}, "$ notify-hook --done\n$ notify-hook other\n", "1"},
		{"git global options", []any{"git", " push "}, "$ git status\n$ git -C /x --no-pager push origin\n", "2"},
		{"other argument", []any{"notify-hook", "--done"}, "$ notify-hook other --done\n", ""},
		{"escape", []any{"/usr/local/bin/notify-hook"}, "$ notify\\x2dhook\n", ""},
		{"clean", []any{"/usr/local/bin/notify-hook"}, "$ ls\n$ go test ./...\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := b493HookFixture(t, c.argv, c.text)
			detail := "transcript line " + c.line + ": code-forge-on-block-end-hook"
			code, out, errs := b493Ready(t, f)
			want, wantErr, wantCode := "rule_break - "+detail+"\n  fix: "+fix(f.home)+"\nblock B1 not ready: 1 blockers\n", "", 1
			if c.line == "" {
				want, wantErr, wantCode = "block B1 ready\n", b493OpenedWarn, 0
			}
			if code != wantCode || out != want || errs != wantErr {
				t.Fatalf("ready: exit=%d\nout=%q\nerr=%q", code, out, errs)
			}
			code, out, _ = b493Ready(t, f, "--json")
			wantJSON := `{"block":"B1","ready":true,"blockers":[]}` + "\n"
			if c.line != "" {
				wantJSON = `{"block":"B1","ready":false,"blockers":[{"code":"rule_break","file":null,"detail":"` + detail + `","fix":"` + fix(f.home) + `","clears_by":"owner"}]}` + "\n"
			}
			if code != wantCode || out != wantJSON {
				t.Fatalf("ready --json: exit=%d\nout=%q", code, out)
			}
			before := b493LedgerLines(t, f)
			code, out, errs, reason := b493Close(f, "--json")
			if c.line == "" {
				if reason != 0 || code != 0 || !strings.Contains(out, `"status":"closed"`) {
					t.Fatalf("close: exit=%d reason=%v\nout=%q\nerr=%q", code, reason, out, errs)
				}
				return
			}
			want = `{"block":"B1","ok":false,"reason":"rule_break ` + detail + `","blockers":[{"code":"rule_break","file":null,"detail":"` + detail + `","fix":"` + fix(f.home) + `"}]}` + "\n"
			if reason != 0 || code != 1 || out != want || errs != "block B1 open: rule_break "+detail+"\n" {
				t.Fatalf("close: exit=%d reason=%v\nout=%q\nerr=%q", code, reason, out, errs)
			}
			if after := b493LedgerLines(t, f); after != before {
				t.Fatalf("close refusal wrote rows:\n%s", after[len(before):])
			}
			code, out, _, _ = b493Close(f)
			if code != 1 || out != "rule_break - "+detail+"\n  fix: "+fix(f.home)+"\n" {
				t.Fatalf("close text: exit=%d out=%q", code, out)
			}
		})
	}
}

func b493LedgerLines(t *testing.T, f *closeReviewFixture) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(f.home, ".code-forge/ledger/*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	all := ""
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		all += string(data)
	}
	return all
}

func b493HumanSeam(t *testing.T, tty bool, answer humanask.Result) *[]string {
	t.Helper()
	asked := []string{}
	oldTTY, oldAsk := humanTTY, humanAsk
	humanTTY = func() bool { return tty }
	humanAsk = func(message string) (humanask.Result, error) {
		asked = append(asked, message)
		if !tty {
			return humanask.NoTTY, nil
		}
		return answer, nil
	}
	t.Cleanup(func() { humanTTY, humanAsk = oldTTY, oldAsk })
	return &asked
}

func b493RequireHumanFixture(t *testing.T, until string) *closeReviewFixture {
	t.Helper()
	f := b493AutoClaimFixture(t, until)
	b493SetConfig(t, f, ledger.Object{{Key: "review", Value: ledger.Object{{Key: "close_wait_s", Value: 1}}}, {Key: "context", Value: ledger.Object{{Key: "require_coder_tests", Value: true}}}, {Key: "close", Value: ledger.Object{{Key: "require_human", Value: true}}}})
	if until == "" {
		record, err := state.ReadRun(f.home, "r-close-fixture")
		if err != nil {
			t.Fatal(err)
		}
		record.Delete("autopilot")
		if err := state.SaveRun(f.home, record); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func b493Events(t *testing.T, f *closeReviewFixture) string {
	t.Helper()
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
	return strings.Join(events, ",")
}

// TestB493CloseAutoClaimRequireHuman: --auto-claim under close.require_human
// asks the owner at the terminal before the claims, as Node does. With an
// expired grant Node writes no autopilot.expire row on this path.
func TestB493CloseAutoClaimRequireHuman(t *testing.T) {
	const stdout = "block B1 auto-claimed extra.txt\nreviewed extra.txt ticket 0a276b0b4e62b3cc2d085cb0\nreviewed file0.txt ticket b82733d3089a96c3991c870e\nblock B1 closed\n"
	const question = "Close block B1 of run r-close-fixture? Only the owner may say yes."
	for _, tc := range []struct {
		name, until, events string
	}{{"no grant", "", "block.claim,block.close"}, {"expired grant", "2020-01-01T00:00:00.000Z", "block.claim,block.close"}} {
		t.Run("yes "+tc.name, func(t *testing.T) {
			f := b493RequireHumanFixture(t, tc.until)
			asked := b493HumanSeam(t, true, humanask.Confirmed)
			code, out, errs, reason := b493CloseAutoClaim(f)
			if code != 0 || out != stdout || errs != "" {
				t.Fatalf("exit=%d reason=%v\nstdout %q\nstderr %q", code, reason, out, errs)
			}
			if strings.Join(*asked, "|") != question {
				t.Fatalf("asked %q", *asked)
			}
			if got := b493Events(t, f); got != tc.events {
				t.Fatalf("rows %s, want %s", got, tc.events)
			}
		})
	}
	t.Run("no", func(t *testing.T) {
		f := b493RequireHumanFixture(t, "")
		asked := b493HumanSeam(t, true, humanask.Cancelled)
		code, out, errs, reason := b493CloseAutoClaim(f)
		if code != 1 || out != "" || errs != "block B1 close: cancelled — nothing was recorded\n" {
			t.Fatalf("exit=%d reason=%v\nstdout %q\nstderr %q", code, reason, out, errs)
		}
		if len(*asked) != 1 {
			t.Fatalf("asked %q", *asked)
		}
		if got := b493Events(t, f); got != "" {
			t.Fatalf("rows %s after a cancel", got)
		}
	})
	t.Run("yes json", func(t *testing.T) {
		f := b493RequireHumanFixture(t, "")
		b493HumanSeam(t, true, humanask.Confirmed)
		code, out, errs, reason := b493CloseAutoClaim(f, "--json")
		want := `{"block":"B1","event":"closed","status":"closed","reason":null,"close_event":null,"reviewed":[{"file":"extra.txt","ticket":"0a276b0b4e62b3cc2d085cb0","via":"approved"},{"file":"file0.txt","ticket":"b82733d3089a96c3991c870e","via":"approved"}]}` + "\n"
		if code != 0 || out != want || errs != "block B1 auto-claimed extra.txt\n" {
			t.Fatalf("exit=%d reason=%v\nstdout %q\nstderr %q", code, reason, out, errs)
		}
	})
}
