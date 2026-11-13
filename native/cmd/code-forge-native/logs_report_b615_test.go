package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/humanask"
	"github.com/ricardov03/code-forge/native/ledger"
)

// B615: `logs report` with the AI cleaning pass runs in Go. Expected texts in
// testdata/b615 are Node's (CODE_FORGE_NATIVE=0) for the lrTestEnv fixture
// with package version 0.23.0 and this fake claude on PATH; claude-argv.txt
// is the argument line the fake logged and packet.md.txt the stdin it read.

const lrTestFakeClaude = `#!/bin/sh
printf 'claude' >> "$FAKELOG"; for a; do printf ' [%s]' "$a" >> "$FAKELOG"; done; printf '\n' >> "$FAKELOG"
cat > "$PACKETLOG"
case "$FAKE_MODE" in
ok) printf '%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"items":[{"text":"older | pipe","kind":"Company","reason":"a name"},{"text":"slug","kind":"project","reason":"inside a placeholder"},{"text":"boom","kind":"weird","reason":"odd"},{"text":"x.mjs:1","kind":"path","reason":"a file"},{"text":"ab","kind":"host","reason":"too short"},{"text":"darwin arm64","kind":"host","reason":"a host"}]},"usage":{"input_tokens":100,"output_tokens":20}}' ;;
none) printf '%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"items":[]},"usage":{"input_tokens":100,"output_tokens":20}}' ;;
invalid) printf '%s' '{"is_error":false,"result":"{\"x\":true}"}' ;;
nologin) echo "Error: not logged in" >&2; exit 1 ;;
fail) echo "boom" >&2; exit 5 ;;
slow) exec sleep 30 ;;
esac
`

var lrAITestData, _ = filepath.Abs(filepath.Join("testdata", "b615"))

type lrAITest struct {
	lrTestCase
	packet string
}

// lrAIEnv is lrTestEnv with package 0.23.0 and the fake claude (none for "missing").
func lrAIEnv(t *testing.T, mode string) lrAITest {
	t.Helper()
	c := lrAITest{lrTestCase: lrTestEnv(t, "", false), packet: filepath.Join(t.TempDir(), "packet.md")}
	c.data = lrAITestData
	b500Write(t, c.pkg, `{"name":"@codedology/code-forge","version":"0.23.0","repository":{"type":"git","url":"git+https://github.com/ricardov03/code-forge.git"},"bugs":{"url":"https://github.com/ricardov03/code-forge/issues"}}`)
	if mode != "missing" {
		bin := strings.Split(os.Getenv("PATH"), ":")[0]
		b500Write(t, filepath.Join(bin, "claude"), lrTestFakeClaude)
		if err := os.Chmod(filepath.Join(bin, "claude"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("FAKE_MODE", mode)
	t.Setenv("PACKETLOG", c.packet)
	return c
}

// lrAINoTrace checks no handover, an empty run root and no ledger folder.
func (c lrAITest) lrAINoTrace(t *testing.T) {
	t.Helper()
	if got := b500HandedOver(t, c.home); got != "" {
		t.Errorf("handed over: %s", got)
	}
	if left := c.rootLeft(t); len(left) != 0 {
		t.Errorf("run root left behind: %v", left)
	}
	if _, err := os.Stat(filepath.Join(c.home, ".code-forge", "ledger")); !os.IsNotExist(err) {
		t.Errorf("a ledger folder was written (Node writes none): %v", err)
	}
}

func TestB615LogsReportAIDryRunMatchesNode(t *testing.T) {
	for _, mode := range []string{"ok", "none", "fail", "nologin", "invalid", "missing"} {
		t.Run(mode, func(t *testing.T) {
			c := lrAIEnv(t, mode)
			before := b500Read(t, c.errorLog())
			code, out, errText := b500Run(t, "logs", "report", "--dry-run")
			lrTestCheck(t, mode, code, out, errText, 0, c.golden(t, mode+".stdout"), "")
			want := lrNpmView
			if mode != "missing" {
				want += c.golden(t, "claude-argv.txt")
				if got := b500Read(t, c.packet); got != c.golden(t, "packet.md.txt") {
					t.Errorf("packet differs from Node's:\n%s", got)
				}
			}
			if got := c.fakeLog(t); got != want {
				t.Errorf("children differ from Node's:\n%s\nwant:\n%s", got, want)
			}
			if b500Read(t, c.errorLog()) != before {
				t.Errorf("a dry run changed the error log")
			}
			c.lrAINoTrace(t)
		})
	}
}

func TestB615LogsReportAITimeoutFallsBack(t *testing.T) {
	c := lrAIEnv(t, "slow")
	old := lrAITimeoutMs
	lrAITimeoutMs = 300
	t.Cleanup(func() { lrAITimeoutMs = old })
	code, out, errText := b500Run(t, "logs", "report", "--dry-run")
	// Node's timeout line is the fail golden with plainReason("timeout").
	want := strings.Replace(c.golden(t, "fail.stdout"), "(the session failed)", "(it timed out)", 1)
	lrTestCheck(t, "timeout", code, out, errText, 0, want, "")
	c.lrAINoTrace(t)
}

func TestB615LogsReportAISendMatchesNode(t *testing.T) {
	c := lrAIEnv(t, "ok")
	code, out, errText := b500Run(t, "logs", "report", "--yes", "--allow-old")
	lrTestCheck(t, "send", code, out, errText, 0, c.golden(t, "okyes.stdout"), "")
	log := c.fakeLog(t)
	if !strings.Contains(log, "gh issue create") || !strings.Contains(log, "<company>\nline\n```") || strings.Contains(log, "older | pipe") {
		t.Errorf("gh did not get the AI-cleaned body:\n%s", log)
	}
	c.lrAINoTrace(t)
}

func TestB615LogsReportAIFailedSendRefusedLikeNode(t *testing.T) {
	c := lrAIEnv(t, "fail")
	code, out, errText := b500Run(t, "logs", "report", "--yes", "--allow-old")
	lrTestCheck(t, "failed AI send", code, out, errText, 1, c.golden(t, "failyes.stdout"), c.golden(t, "failyes.stderr"))
	if strings.Contains(c.fakeLog(t), "gh ") {
		t.Errorf("a refused report ran gh: %q", c.fakeLog(t))
	}
	if !strings.Contains(b500Read(t, c.errorLog()), `"verb":"logs","sub":"report"`) {
		t.Errorf("refusal not logged like Node's failure: %s", b500Read(t, c.errorLog()))
	}
}

// A terminal send whose AI pass does not run asks a question: Node keeps it.
func TestB615LogsReportAITerminalSendStaysInNode(t *testing.T) {
	c := lrAIEnv(t, "ok")
	asked := ""
	b500Terminal(t, true, true, humanask.Cancelled, &asked)
	code, out, _ := b500Run(t, "logs", "report", "--yes", "--allow-old")
	if code != 127 || out != "" || b500HandedOver(t, c.home) != "logs-report" {
		t.Errorf("got (%d, %q) reason %q, want handover logs-report", code, out, b500HandedOver(t, c.home))
	}
	if c.fakeLog(t) != "" {
		t.Errorf("side effects before the handover: %q", c.fakeLog(t))
	}
}

// Node's readItems and applyItems on the fake's items.
func TestB615LogsReportApplyItemsMatchesNode(t *testing.T) {
	answer, err := ledger.DecodeValue([]byte(`{"items":[{"text":"older | pipe","kind":"Company"},{"text":"slug","kind":"project"},{"text":"boom","kind":"weird"},{"text":"ab","kind":"host"},{"text":" ","kind":"host"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	items, ok := lrReadItems(answer)
	if !ok || len(items) != 3 || items[0] != (lrScrubItem{"older | pipe", "company"}) || items[1] != (lrScrubItem{"slug", "project"}) || items[2] != (lrScrubItem{"boom", "other"}) {
		t.Fatalf("read items: %+v", items)
	}
	texts, counts := lrApplyItems([]string{"older | pipe in <slug>", "boom boom"}, items)
	if texts[0] != "<company> in <slug>" || texts[1] != "<other> <other>" {
		t.Errorf("applied: %q", texts)
	}
	if got := lrDescribeAICounts(counts); got != "AI pass: 1 company, 2 other items." {
		t.Errorf("counts: %q", got)
	}
}
