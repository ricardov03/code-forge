package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/sign"
)

const b500LedgerOld = `{"event":"decision","decision_id":"d1","question":"q1","confidence":0.55}
{"event":"decision","decision_id":"d2","question":"q1","confidence":0.8499999999999999}
{"event":"outcome","decision_id":"d1","outcome":"correct"}
`

const b500LedgerNow = `{"event":"session","run":"r1","usd":0.25}
{"event":"session","run":"r1","tokens_in":1000,"tokens_out":1000,"provider":"anthropic","level":"L2"}
{"event":"session","run":"r9","usd":5}
`

var b500LedgerKey = []byte("0123456789abcdef0123456789abcdef")

// b500Ledger writes a home with ledger demo and run r1 (blocks B1, B-2).
func b500Ledger(t *testing.T) (home, ledgerFile string) {
	t.Helper()
	home, _ = b500Env(t)
	dir := filepath.Join(home, ".code-forge")
	ledgerFile = filepath.Join(dir, "ledger", "demo.jsonl")
	b500Write(t, filepath.Join(dir, "ledger", "demo.1.jsonl"), b500LedgerOld)
	b500Write(t, ledgerFile, b500LedgerNow)
	b500Write(t, filepath.Join(dir, "runs", "r1.json"), `{"run_id":"r1","project":"demo","workspace":"/nonexistent-ws","blocks":{"B1":{},"B-2":{}}}`)
	b500Write(t, filepath.Join(dir, "runs", "r7.json"), "not json")
	b500Write(t, filepath.Join(dir, "runs", "r7.key"), string(b500LedgerKey))
	key := filepath.Join(dir, "runs", "r1.key")
	b500Write(t, key, string(b500LedgerKey))
	if err := os.Chmod(key, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "runs", "r7.key"), 0o600); err != nil {
		t.Fatal(err)
	}
	return home, ledgerFile
}

// b500NewRow returns the one row appended after the fixture rows, decoded.
func b500NewRow(t *testing.T, file string) (string, map[string]any) {
	t.Helper()
	text := b500Read(t, file)
	if !strings.HasPrefix(text, b500LedgerNow) {
		t.Fatalf("old rows changed:\n%s", text)
	}
	line := strings.TrimSuffix(strings.TrimPrefix(text, b500LedgerNow), "\n")
	if strings.Contains(line, "\n") || line == "" {
		t.Fatalf("want one new row, got %q", line)
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(line), &row); err != nil {
		t.Fatal(err)
	}
	return line, row
}

var b500TS = regexp.MustCompile(`"ts":"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z"`)

// Expected texts and rows are Node's (CODE_FORGE_NATIVE=0) for the same home.
func TestB500LedgerCalibrationInGo(t *testing.T) {
	home, _ := b500Ledger(t)
	code, out, errText := b500Run(t, "ledger", "calibration", "--slug", "demo", "--question", "q1")
	bucket := func(c, u int) string {
		return "{\n      \"correct\": " + string(rune('0'+c)) + ",\n      \"wrong\": 0,\n      \"unknown\": " + string(rune('0'+u)) + "\n    }"
	}
	want := "{\n  \"buckets\": {\n" +
		"    \"0.50-0.60\": " + bucket(1, 0) + ",\n" +
		"    \"0.60-0.70\": " + bucket(0, 0) + ",\n" +
		"    \"0.70-0.80\": " + bucket(0, 0) + ",\n" +
		"    \"0.80-0.85\": " + bucket(0, 0) + ",\n" +
		"    \"0.85-0.90\": " + bucket(0, 1) + ",\n" +
		"    \"0.90-0.95\": " + bucket(0, 0) + ",\n" +
		"    \"0.95-0.99\": " + bucket(0, 0) + ",\n" +
		"    \"0.99-1.00\": " + bucket(0, 0) + "\n" +
		"  },\n  \"unbacked\": 0,\n  \"unmapped\": 0\n}\n"
	if code != 0 || out != want || errText != "" {
		t.Errorf("calibration: got (%d, %q, %q)", code, out, errText)
	}
	if code, out, errText := b500Run(t, "ledger", "calibration", "--slug", "demo", "--question"); code != 2 || out != "" || errText != "ledger calibration: --question requires a value\n" {
		t.Errorf("--question without value: got (%d, %q, %q)", code, out, errText)
	}
	if reason := b500HandedOver(t, home); reason != "" {
		t.Errorf("handed over: %s", reason)
	}
	// A null row: Node throws reading row.event; Go prints Node's text.
	b500Write(t, filepath.Join(home, ".code-forge", "ledger", "demo.jsonl"), "null\n")
	if code, out, errText := b500Run(t, "ledger", "calibration", "--slug", "demo"); code != 1 || out != "" || errText != "ledger calibration: Cannot read properties of null (reading 'event')\n" || b500HandedOver(t, home) != "" {
		t.Errorf("null row: got (%d, %q, %q), reason %q", code, out, errText, b500HandedOver(t, home))
	}
}

func TestB500LedgerOutcomeInGo(t *testing.T) {
	home, file := b500Ledger(t)
	if code, out, errText := b500Run(t, "ledger", "outcome", "--slug", "demo", "--pr", "0012", "--ci", "red"); code != 0 || out != "recorded outcome for PR #12: red\n" || errText != "" {
		t.Fatalf("outcome: got (%d, %q, %q)", code, out, errText)
	}
	line, _ := b500NewRow(t, file)
	if got := b500TS.ReplaceAllString(line, `"ts":"T"`); got != `{"event":"outcome","outcome":"wrong","outcome_source":"ci","pr":12,"ts":"T","tokens_source":null,"cost_source":null}` {
		t.Errorf("outcome row = %s", line)
	}
	if reason := b500HandedOver(t, home); reason != "" {
		t.Errorf("handed over: %s", reason)
	}
	// --scan-git is served in Go (B457); a null row fails with Node's text.
	b500Write(t, file, "null\n")
	if code, out, errText := b500Run(t, "ledger", "outcome", "--slug", "demo", "--scan-git"); code != 1 || out != "" || errText != "ledger outcome: Cannot read properties of null (reading 'event')\n" || b500HandedOver(t, home) != "" {
		t.Errorf("--scan-git: got (%d, %q, %q), reason %q", code, out, errText, b500HandedOver(t, home))
	}
}

func TestB500LedgerAddInGo(t *testing.T) {
	home, file := b500Ledger(t)
	code, out, errText := b500Run(t, "ledger", "add", "coder", "--run", "r1", "--block", "B1", "--usd", "0.12345", "--note", "hi")
	if code != 0 || out != "recorded coder spend $0.1235 for run r1 block B1 · run total $0.4035\n" || errText != "" {
		t.Fatalf("add: got (%d, %q, %q)", code, out, errText)
	}
	line, row := b500NewRow(t, file)
	shape := regexp.MustCompile(`"mac":"[0-9a-f]{64}"`).ReplaceAllString(b500TS.ReplaceAllString(line, `"ts":"T"`), `"mac":"M"`)
	if shape != `{"run":"r1","event":"session","role":"coder","manual":true,"block":"B1","provider":null,"level":null,"tokens_in":null,"tokens_out":null,"usd":0.1235,"cost_source":"manual","note":"hi","ts":"T","mac":"M","tokens_source":null}` {
		t.Errorf("add row = %s", line)
	}
	if verified, err := sign.VerifyRow(row, b500LedgerKey); err != nil || !verified.OK {
		t.Errorf("add row signature: %#v, %v", verified, err)
	}
	if reason := b500HandedOver(t, home); reason != "" {
		t.Errorf("handed over: %s", reason)
	}
	// Node's own refusals, in Go.
	if code, out, errText := b500Run(t, "ledger", "add", "coder", "--run", "r1", "--block", "B1"); code != 2 || out != "" || !strings.Contains(errText, "usage: code-forge ledger add coder --run <r> --block <b> --usd <n> [--note \"...\"]\n") {
		t.Errorf("missing --usd: got (%d, %q, %q)", code, out, errText)
	}
	// A run record that is not JSON: Node's parse error text stays with Node.
	if code, _, _ := b500Run(t, "ledger", "add", "coder", "--run", "r7", "--block", "B1", "--usd", "1"); code != 127 || b500HandedOver(t, home) != "ledger-run-record" {
		t.Errorf("bad record: exit %d, reason %q", code, b500HandedOver(t, home))
	}
	if _, row2 := b500NewRow(t, file); row2["usd"] != row["usd"] {
		t.Errorf("a handover wrote a row")
	}
}

// toFixed(4) switches to exponent form at 1e21; Go writes Node's text and row.
func TestB500LedgerAddHugeUSD(t *testing.T) {
	home, file := b500Ledger(t)
	code, out, errText := b500Run(t, "ledger", "add", "coder", "--run", "r1", "--block", "B1", "--usd", "99999999999999999999999999")
	if code != 0 || out != "recorded coder spend $1e+26 for run r1 block B1 · run total $1e+26\n" || errText != "" {
		t.Fatalf("huge usd: got (%d, %q, %q)", code, out, errText)
	}
	line, _ := b500NewRow(t, file)
	if !strings.Contains(line, `"usd":1e+26,`) {
		t.Errorf("huge usd row = %s", line)
	}
	if reason := b500HandedOver(t, home); reason != "" {
		t.Errorf("handed over: %s", reason)
	}
}

func TestB500LedgerOutsideReviewInGo(t *testing.T) {
	home, file := b500Ledger(t)
	code, out, errText := b500Run(t, "ledger", "outside-review", "--run", "r1", "--block", "B1", "--pr", "7", "--rounds", "2", "--findings", "3", "--link", "https://x.test/a")
	if code != 0 || out != "outside_review pr 7 · rounds 2 · findings 3 · blocks B1\n" || errText != "" {
		t.Fatalf("outside-review: got (%d, %q, %q)", code, out, errText)
	}
	line, row := b500NewRow(t, file)
	shape := regexp.MustCompile(`"mac":"[0-9a-f]{64}"`).ReplaceAllString(b500TS.ReplaceAllString(line, `"ts":"T"`), `"mac":"M"`)
	if shape != `{"run":"r1","event":"outside_review","pr":7,"rounds":2,"findings":3,"link":"https://x.test/a","blocks":["B1"],"ts":"T","mac":"M","tokens_source":null,"cost_source":null}` {
		t.Errorf("outside-review row = %s", line)
	}
	if verified, err := sign.VerifyRow(row, b500LedgerKey); err != nil || !verified.OK {
		t.Errorf("row signature: %#v, %v", verified, err)
	}
	if code, out, errText := b500Run(t, "ledger", "outside-review", "--run", "r1", "--block", "B9", "--pr", "7", "--rounds", "2", "--findings", "3"); code != 1 || out != "" || errText != "ledger outside-review: block \"B9\" is not a block of run r1; nothing written\n" {
		t.Errorf("unknown block: got (%d, %q, %q)", code, out, errText)
	}
	usage := "usage: code-forge ledger outside-review --run <r> --pr <n> --rounds <n> --findings <n> [--link <url>] [--block <id>]...\n"
	if code, out, errText := b500Run(t, "ledger", "outside-review", "r1"); code != 2 || out != "" || errText != "ledger outside-review: unknown argument at position 1\n"+usage {
		t.Errorf("positional: got (%d, %q, %q)", code, out, errText)
	}
	if reason := b500HandedOver(t, home); reason != "" {
		t.Errorf("handed over: %s", reason)
	}
	if code, _, _ := b500Run(t, "ledger", "outside-review", "--run", "r7", "--pr", "7", "--rounds", "2", "--findings", "3"); code != 127 || b500HandedOver(t, home) != "ledger-run-record" {
		t.Errorf("bad record: exit %d, reason %q", code, b500HandedOver(t, home))
	}
	b500NewRow(t, file)
}

func TestB500LedgerHelp(t *testing.T) {
	b500Ledger(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"ledger", "--help"}, "usage: code-forge ledger calibration|outcome|tail --slug <slug> [...]  (tail: [--n|-n <count>]) | add coder --run <r> --block <b> --usd <n> [--note \"...\"] | outside-review --run <r> --pr <n> --rounds <n> --findings <n> [--link <url>] [--block <id>]...\n"},
		{[]string{"ledger", "add", "-h"}, "usage: code-forge ledger add coder --run <r> --block <b> --usd <n> [--note \"...\"]\n"},
		{[]string{"ledger", "outside-review", "--help"}, "usage: code-forge ledger outside-review --run <r> --pr <n> --rounds <n> --findings <n> [--link <url>] [--block <id>]...\n"},
	} {
		if code, out, errText := b500Run(t, tc.args...); code != 0 || out != helpExpect(t, tc.args, 0, tc.want) || errText != "" {
			t.Errorf("%q: got (%d, %q, %q)", tc.args, code, out, errText)
		}
	}
}
