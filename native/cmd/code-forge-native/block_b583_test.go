package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// b583Node runs the same close through Node (CODE_FORGE_NATIVE=0 ./bin/code-forge)
// on a fresh fixture when B583_NODE_GOLDEN=1, to print the bytes the goldens
// below were taken from.
func b583Node(t *testing.T, build func() *closeReviewFixture, extra ...string) {
	t.Helper()
	if os.Getenv("B583_NODE_GOLDEN") != "1" {
		return
	}
	f := build()
	bin, err := filepath.Abs("../../../bin/code-forge")
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{"block", "close", "B1", "--run", "r-close-fixture", "--report", f.report, "--transcript", f.transcript}, extra...)
	cmd := exec.Command(bin, args...)
	cmd.Dir = f.top
	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CODE_FORGE_NODE=") && !strings.HasPrefix(kv, "CODE_FORGE_NATIVE=") {
			env = append(env, kv)
		}
	}
	cmd.Env = append(env, "HOME="+f.home, "CODE_FORGE_NATIVE=0", "CODE_FORGE_HANDOVER_LOG=0")
	var out, errs strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errs
	code := 0
	if err := cmd.Run(); err != nil {
		e, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		code = e.ExitCode()
	}
	t.Logf("NODE %v exit=%d\nout=%q\nerr=%q", extra, code, strings.ReplaceAll(out.String(), f.home, "<HOME>"), strings.ReplaceAll(errs.String(), f.home, "<HOME>"))
}

func b583Transcript(t *testing.T, text []byte) *closeReviewFixture {
	t.Helper()
	f := newCloseReviewFixture(t, 1)
	b493Approve(t, f)
	closeFixtureWrite(t, f.transcript, text)
	return f
}

// B583: a transcript that is not valid UTF-8 (plain text or a Claude Code
// JSONL log) and a Claude log whose tool input has an escaped surrogate pair
// close in Go: Node reads the file with readFile(file, 'utf8') (each maximal
// invalid subpart is one U+FFFD) and JSON.parse joins the pair into one code
// point. Before B583 both handed over (close-transcript-format). The bytes
// are Node's (CODE_FORGE_NATIVE=0 on the same fixtures).
func TestB583CloseTranscriptDecoding(t *testing.T) {
	claude := func(command string) string {
		return `{"type":"assistant","sessionId":"s1","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"` + command + `"}}]}}` + "\n"
	}
	const hitOut = "rule_break - transcript line 2: git-push-force\n  fix: code-forge block close B1 --run r-close-fixture --decide --reason '<reason>' --report <HOME>/report.txt --transcript <HOME>/transcript.txt\n"
	const hitErr = b493OpenedWarn + "block B1 open: rule_break transcript line 2: git-push-force\n"
	const closedOut = "reviewed file0.txt ticket b82733d3089a96c3991c870e\nblock B1 closed\n"
	cases := []struct {
		name      string
		text      []byte
		code      int
		out, errs string
	}{
		{"plain invalid bytes then a hit", []byte("$ ls \xff\xe2\x82 caf\xc3\n$ git push --force origin main\n\xed\xa0\x80 done\n"), 1, hitOut, hitErr},
		{"plain invalid bytes, no hit", []byte("$ ls \xf0\x9f\x98\n$ git status \xc0\xaf\n"), 0, closedOut, b493OpenedWarn},
		{"claude log with invalid bytes", []byte(claude("echo \xff\xfe") + claude("git push --force")), 1, hitOut, hitErr},
		{"claude log with an escaped pair", []byte(claude(`echo 😀`) + claude(`git push --force 😀`)), 1, hitOut, hitErr},
		{"claude log with an escaped pair, no hit", []byte(claude(`echo 😀 \\ud800`) + claude(`git status`)), 0, closedOut, b493OpenedWarn},
	}
	for _, c := range cases {
		b583Node(t, func() *closeReviewFixture { return b583Transcript(t, c.text) })
		f := b583Transcript(t, c.text)
		code, out, errs, reason := b493Close(f)
		out = strings.ReplaceAll(out, f.home, "<HOME>")
		if reason != 0 || code != c.code || out != c.out || errs != c.errs {
			t.Errorf("%s: exit=%d reason=%v\nout=%q\nerr=%q\nNode: exit=%d\nout=%q\nerr=%q", c.name, code, reason, out, errs, c.code, c.out, c.errs)
		}
	}
}

// B583: a lone surrogate escape stays with Node (JSON.parse keeps the code
// unit; Go's decoder cannot), decided before any write.
func TestB583CloseTranscriptLoneSurrogate(t *testing.T) {
	for _, escape := range []string{`\ud800`, `\udc00`, `\ud83d x`, `\ud83dA`} {
		f := b583Transcript(t, []byte(`{"type":"assistant","sessionId":"s1","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"echo `+escape+`"}}]}}`+"\n"))
		before := b493LedgerLines(t, f)
		code, out, errs, reason := b493Close(f)
		if reason != hCloseTranscriptFormat || code != landHandover || out != "" || errs != "" {
			t.Errorf("%s: exit=%d reason=%v out=%q err=%q, want the close-transcript-format hand-over", escape, code, reason, out, errs)
		}
		if after := b493LedgerLines(t, f); after != before {
			t.Errorf("%s: the hand-over wrote ledger rows", escape)
		}
	}
}

// B583: agent waiver and reopen rows whose fields hold objects print in the
// --json close audit with JSON.stringify in the ledger line's key order; Go
// reads that order again (as B498 does for decisions). Before B583 these
// closes handed over (close-gates-format). The bytes are Node's
// (CODE_FORGE_NATIVE=0 on the same fixtures).
func TestB583CloseAuditObjectValues(t *testing.T) {
	fixture := func(t *testing.T) *closeReviewFixture {
		f := b583Transcript(t, nil)
		const ts = "2026-11-11T10:00:00.000Z"
		b493Sign(t, f,
			ledger.Object{{Key: "event", Value: "review.triage"}, {Key: "block", Value: "B1"}, {Key: "file", Value: "file0.txt"}, {Key: "finding", Value: "F1"}, {Key: "severity", Value: "warning"}, {Key: "ts", Value: ts}},
			ledger.Object{{Key: "event", Value: "review.waived"}, {Key: "block", Value: "B1"}, {Key: "file", Value: "file0.txt"}, {Key: "finding", Value: "F1"}, {Key: "by", Value: "agent"}, {Key: "verdict", Value: "agree"}, {Key: "severity", Value: "warning"},
				{Key: "reason", Value: ledger.Object{{Key: "z", Value: 1}, {Key: "a", Value: []any{ledger.Object{{Key: "y", Value: "2"}, {Key: "b", Value: nil}}}}}}, {Key: "ts", Value: ts}},
			ledger.Object{{Key: "event", Value: "review.reopened"}, {Key: "block", Value: "B1"}, {Key: "file", Value: "other.txt"}, {Key: "reason", Value: []any{"x", 2}}, {Key: "by", Value: "agent"},
				{Key: "verdict", Value: ledger.Object{{Key: "reason", Value: "why"}, {Key: "verdict", Value: "reopen"}, {Key: "extra", Value: ledger.Object{{Key: "k", Value: true}, {Key: "c", Value: 1.5}}}}}, {Key: "ts", Value: ts}},
		)
		return f
	}
	const text = "reviewed file0.txt ticket b82733d3089a96c3991c870e\nblock B1: agent waiver file0.txt F1 reason: [object Object]; verdict: agree\nagent reopen other.txt: x,2 (verdict: reopen)\nblock B1 closed\n"
	const asJSON = `{"block":"B1","event":"closed","status":"closed","reason":null,"close_event":null,"reviewed":[{"file":"file0.txt","ticket":"b82733d3089a96c3991c870e","via":"approved"}],"agent_waivers":[{"file":"file0.txt","finding":"F1","severity":"warning","reason":{"z":1,"a":[{"y":"2","b":null}]},"verdict":"agree"}],"agent_reopens":[{"file":"other.txt","reason":["x",2],"verdict":{"reason":"why","verdict":"reopen","extra":{"k":true,"c":1.5}}}]}` + "\n"
	for _, c := range []struct {
		json bool
		out  string
	}{{false, text}, {true, asJSON}} {
		extra := []string{}
		if c.json {
			extra = []string{"--json"}
		}
		b583Node(t, func() *closeReviewFixture { return fixture(t) }, extra...)
		f := fixture(t)
		code, out, errs, reason := b493Close(f, extra...)
		if reason != 0 || code != 0 || out != c.out || errs != b493OpenedWarn {
			t.Errorf("json=%v: exit=%d reason=%v\nout=%q\nerr=%q\nNode: exit=0\nout=%q\nerr=%q", c.json, code, reason, out, errs, c.out, b493OpenedWarn)
		}
	}
}
