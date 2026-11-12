package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// b456ReportLedger is a fixture run; the expected text below is what the Node report
// (CODE_FORGE_NATIVE=0) printed for it.
const b456ReportLedger = `{"ts":"2026-11-01T10:00:00.000Z","event":"run.start","run":"r1"}
{"ts":"2026-11-01T10:00:00.000Z","event":"dispatch","run":"r1","block":"B1","level":"L2","lane":"codex","lines":100}
{"ts":"2026-11-01T10:10:00.000Z","event":"session","run":"r1","block":"B1","role":"coder","provider":"openai","level":"L2","tokens_in":10000,"tokens_out":2500,"duration_ms":120000}
{"ts":"2026-11-01T10:20:00.000Z","event":"session","run":"r1","block":"B1","role":"reviewer","usd":0.25}
{"ts":"2026-11-01T10:21:00.000Z","event":"session","run":"r1","block":"B1","role":"judge","usd_unknown":true}
{"ts":"2026-11-01T10:30:00.000Z","event":"gate.done","run":"r1","block":"B1","duration_ms":65000,"repeats":{"n":2},"flaky":["b_test.go","a_test.go"]}
{"ts":"2026-11-01T11:15:30.000Z","event":"block.close","run":"r1","block":"B1","lines_actual":130}
{"ts":"2026-11-01T11:20:00.000Z","event":"outside_review","run":"r1","pr":9,"rounds":1,"findings":2,"blocks":["B1"]}
{"ts":"2026-11-01T11:30:00.000Z","event":"session","run":"r1","role":"coder","usd":1,"source":"shadow"}
{"ts":"2026-11-01T11:40:00.000Z","event":"outcome.recorded","run":"r1","skill":"seo-writer","status":"done","cost_usd":0.5,"minutes":3}
{"ts":"2026-11-01T12:00:00.000Z","event":"run.end","run":"r1"}
`

func b456ReportHome(t *testing.T, files map[string]string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	dir := filepath.Join(home, ".code-forge", "ledger")
	if len(files) > 0 {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func b456Report(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	plan := reportDecide(args)
	if plan == nil {
		t.Fatalf("report %v handed over to Node; want Go", args)
	}
	var stdout, stderr bytes.Buffer
	code := plan(&stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestB456ReportRouteRegistered(t *testing.T) {
	if routes["report"] == nil {
		t.Fatal("report has no Go route")
	}
}

func TestB456ReportUsageAndSlugErrors(t *testing.T) {
	b456ReportHome(t, nil)
	for _, args := range [][]string{{}, {"--slug"}, {"--slug", "--json"}, {"--json"}} {
		code, out, errText := b456Report(t, args...)
		if code != 2 || out != "" || errText != reportUsage {
			t.Fatalf("%v: code %d out %q err %q", args, code, out, errText)
		}
	}
	code, _, errText := b456Report(t, "--slug", "Bad Slug")
	if code != 1 || errText != "report: ledgerPath: slug must match /^[a-z0-9][a-z0-9-]*$/ (got \"Bad Slug\")\n" {
		t.Fatalf("bad slug: code %d err %q", code, errText)
	}
}

func TestB456ReportMissingLedgerListsKnownSlugs(t *testing.T) {
	b456ReportHome(t, nil)
	code, _, errText := b456Report(t, "--slug", "nope")
	if code != 1 || errText != "report: no ledger for slug \"nope\"; known slugs: none\n" {
		t.Fatalf("no ledgers: code %d err %q", code, errText)
	}
	b456ReportHome(t, map[string]string{"bb.jsonl": "", "aa.3.jsonl": "", "aa.jsonl": "", "notes.txt": ""})
	code, _, errText = b456Report(t, "--slug", "nope")
	if code != 1 || errText != "report: no ledger for slug \"nope\"; known slugs: aa, bb\n" {
		t.Fatalf("known slugs: code %d err %q", code, errText)
	}
}

func TestB456ReportTextMatchesNode(t *testing.T) {
	b456ReportHome(t, map[string]string{"t.jsonl": b456ReportLedger})
	code, out, errText := b456Report(t, "--slug", "t")
	if code != 0 || errText != "" {
		t.Fatalf("code %d err %q", code, errText)
	}
	for _, want := range []string{
		"== cost_per_block ==\nrun | block | USD | wall | human wait | status\nr1 | B1 | $0.4000 | 1h 15m 30s | - | completed\n" +
			"  level L2 · lane codex · coder $0.1500 · review $0.2500 · S1+S2 $0.0000 · facts $0.0000 · other $0.0000 · gates 1m 5s · review time - · proof 0s · 1 unknown\n== lane_distribution ==\n",
		"== forecast_vs_actual_lines ==\n[\n  {\n    \"block\": \"B1\",\n    \"forecast\": 100,\n    \"actual\": 130,\n    \"repeats\": 2\n  }\n]\n",
		"== outside_review ==\nPR #9 · 1 outside round · 2 findings · code-forge $0.40 (+1 unknown) · B1\n",
		"r1 | B1 | 75.5 | 2 | 0 | 0 | 0 | 1.0833333333333333 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0\n",
		"== run_time ==\nrun | started | ended | wall | USD | closed | stopped\nr1 | 2026-11-01T10:00:00.000Z | 2026-11-01T12:00:00.000Z | 2h 0m 0s | $0.4000 | 1 | 0\ntotal (1 run): 2h 0m 0s | $0.4000 | 1 closed\n",
		"spend per run (estimated USD, open blocks included):\n  run r1: $0.4000 · 3 session(s) · 1 with unknown price (not counted)\n  total: $0.4000 · 3 session(s) · 1 with unknown price (not counted)\nrun mode:\n  run r1: plan\nrun r1 flaky: a_test.go, b_test.go\n",
		"== specialist_outcomes ==\nskill | step | status | cost_usd | minutes\nseo-writer | - | done | $0.5000 | 3\ntotal (1 outcomes): $0.5000 | 3 minutes\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("text report lacks %q\n---\n%s", want, out)
		}
	}
	if !strings.HasSuffix(out, "total (1 outcomes): $0.5000 | 3 minutes\n") || strings.Count(out, "== specialist_outcomes ==") != 1 {
		t.Fatalf("specialist_outcomes must print once, last:\n%s", out)
	}
}

func TestB456ReportJSONHasAllSectionsAndFlaky(t *testing.T) {
	b456ReportHome(t, map[string]string{"t.jsonl": b456ReportLedger})
	code, out, _ := b456Report(t, "--slug", "t", "--json")
	if code != 0 {
		t.Fatalf("code %d", code)
	}
	decoder := json.NewDecoder(strings.NewReader(out))
	if _, err := decoder.Token(); err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for decoder.More() {
		key, _ := decoder.Token()
		keys = append(keys, key.(string))
		var skip json.RawMessage
		if err := decoder.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	want := append(append([]string{}, reportSectionNames...), "flaky")
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("sections %v, want %v", keys, want)
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	_ = json.Compact(&compact, parsed["cost_per_block"])
	wantCost := `[{"run":"r1","block":"B1","level":"L2","lane":"codex","completed":true,"reviewUsd":0.25,"coderUsd":0.15,"s1s2Usd":0,"factsUsd":0,"otherUsd":0,"proofTimeMs":0,"wallMs":4530000,"humanWaitMs":null,"totalUsd":0.4,"unknownUsdSessions":1,"gateTimeMs":65000}]`
	if compact.String() != wantCost {
		t.Fatalf("cost_per_block %s\nwant %s", compact.String(), wantCost)
	}
	compact.Reset()
	_ = json.Compact(&compact, parsed["flaky"])
	if compact.String() != `[{"run":"r1","files":["a_test.go","b_test.go"]}]` {
		t.Fatalf("flaky %s", compact.String())
	}
}

func TestB456ReportExportWritesSectionFiles(t *testing.T) {
	home := b456ReportHome(t, map[string]string{"t.jsonl": b456ReportLedger})
	dir := filepath.Join(home, "out", "deep")
	code, out, _ := b456Report(t, "--slug", "t", "--export", dir, "--json")
	if code != 0 || out != "exported 19 section file(s) to "+dir+"\n" {
		t.Fatalf("code %d out %q", code, out)
	}
	for _, name := range append(append([]string{}, reportSectionNames...), "flaky") {
		data, err := os.ReadFile(filepath.Join(dir, name+".json"))
		if err != nil || !strings.HasSuffix(string(data), "\n") || !json.Valid(data) {
			t.Fatalf("%s.json: %v %q", name, err, data)
		}
	}
	data, _ := os.ReadFile(filepath.Join(dir, "outside_review.json"))
	if !strings.Contains(string(data), "\"code_forge_usd\": 0.4,") {
		t.Fatalf("outside_review.json %s", data)
	}
	// an export path Go cannot create goes to Node, which owns the error text
	blocker := filepath.Join(home, "blocker")
	_ = os.WriteFile(blocker, []byte("x"), 0600)
	plan := reportDecide([]string{"--slug", "t", "--export", filepath.Join(blocker, "sub")})
	if plan == nil || plan(&bytes.Buffer{}, &bytes.Buffer{}) != -1 {
		t.Fatal("an unwritable export dir must ask for the Node hand-over")
	}
}

func TestB456ReportRotatedLedgersReadInOrder(t *testing.T) {
	b456ReportHome(t, map[string]string{
		"t.10.jsonl": `{"event":"run.start","run":"c","ts":"2026-11-01T10:00:00Z"}` + "\n",
		"t.2.jsonl":  `{"event":"run.start","run":"b","ts":"2026-11-01T10:00:00Z"}` + "\n",
		"t.jsonl":    `{"event":"run.start","run":"d","ts":"2026-11-01T10:00:00Z"}` + "\n" + `{"trunc`,
	})
	_, out, _ := b456Report(t, "--slug", "t")
	if !strings.Contains(out, "run mode:\n  run b: plan\n  run c: plan\n  run d: plan\n") {
		t.Fatalf("rotated order or truncated last line:\n%s", out)
	}
}

func TestB456ReportNodeOnlyRowsHandOver(t *testing.T) {
	for name, ledgerText := range map[string]string{
		"bad middle line":   "{\"a\":1}\n{bad\n{\"b\":2}\n",
		"null row":          "null\n",
		"hour 24":           `{"ts":"2026-11-01T24:00:00Z","event":"dispatch","run":"q","block":"Q"}` + "\n",
		"numeric block":     `{"event":"dispatch","run":"q","block":5}` + "\n",
		"prototype role":    `{"event":"session","run":"q","role":"constructor","usd":1}` + "\n",
		"lone surrogate":    `{"event":"note","x":"\ud800"}` + "\n",
		"proto severity":    `{"event":"review.done","findings_by_severity":{"__proto__":1}}` + "\n",
		"awaiting no block": `{"event":"block.awaiting_human_check","run":"q"}` + "\n",
	} {
		b456ReportHome(t, map[string]string{"t.jsonl": ledgerText})
		if reportDecide([]string{"--slug", "t"}) != nil {
			t.Fatalf("%s: Go served a row shape only Node reads exactly", name)
		}
	}
}

func TestB456ReportJSNumberForms(t *testing.T) {
	for _, c := range []struct {
		n      float64
		digits int
		want   string
	}{{0.00005, 4, "0.0001"}, {1.005, 2, "1.00"}, {0.125, 2, "0.13"}, {-0.0, 4, "0.0000"}, {2.5, 0, "3"}, {1e21, 2, "1e+21"}} {
		if got := rpFixed(c.n, c.digits); got != c.want {
			t.Fatalf("toFixed(%v,%d) = %q want %q", c.n, c.digits, got, c.want)
		}
	}
	if rpRound(-2.5) != -2 || rpRound(2.5) != 3 {
		t.Fatal("Math.round halves go to +Infinity")
	}
	for _, s := range []string{"2026-02-30T10:00:00Z", "2026-13-01T10:00:00Z", "2026-11-01T10:60:00Z", "2026-11-01 10:00:00Z", "2026-11-01T10:00:00+24:00"} {
		if _, ok, _ := rpTimestamp(s); ok {
			t.Fatalf("%s must not parse", s)
		}
	}
	ms, ok, _ := rpTimestamp("2026-11-02T09:00:00.1239+02:00")
	if !ok || ms != 1793602800123 {
		t.Fatalf("offset timestamp %v %v", ms, ok)
	}
	if got := rpDuration(4530000.0); got != "1h 15m 30s" {
		t.Fatalf("duration %q", got)
	}
}
