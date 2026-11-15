package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/humanask"
	"github.com/ricardov03/code-forge/native/sign"
)

// awFakeGh is a gh stand-in: it logs its argv, the cwd of repo view and each
// body file; $HOME/gh-mode picks nologin, norepo, dup or createfail. A create
// with --label fails like a repo without the label.
const awFakeGh = `#!/bin/sh
mode=$(cat "$HOME/gh-mode" 2>/dev/null)
log="$HOME/gh.log"
echo "ARGV $*" >> "$log"
case "$1 $2" in
"auth status")
  [ "$mode" = nologin ] && exit 1
  exit 0;;
"repo view")
  echo "CWD $(pwd -P)" >> "$log"
  [ "$mode" = norepo ] && exit 1
  echo '{"nameWithOwner":"acme/widgets"}'; exit 0;;
"issue list")
  if [ "$mode" = dup ]; then
    echo '[{"number":5,"url":"https://github.com/acme/widgets/issues/5","state":"OPEN","body":"other"},{"number":7,"url":"https://github.com/acme/widgets/issues/7","state":"OPEN","body":"x <!-- code-forge-waiver: wa/B2/src/a.mjs/W1 -->"}]'
  else
    echo '[]'
  fi
  exit 0;;
"issue comment"|"issue create")
  prev=""
  for a in "$@"; do
    if [ "$prev" = "--body-file" ]; then echo "BODY<<" >> "$log"; cat "$a" >> "$log"; echo ">>BODY" >> "$log"; fi
    prev="$a"
  done
  if [ "$2" = create ]; then
    [ "$mode" = createfail ] && exit 1
    case " $* " in *" --label "*) echo "could not add label: 'autopilot-waiver' not found" >&2; exit 1;; esac
    printf 'Creating issue in acme/widgets\n\n  https://github.com/acme/widgets/issues/8  \n'
  fi
  exit 0;;
esac
exit 3
`

// waiveFixture: run wa allows waive:warning and waive:nit, wn only
// round:extra and waive:nit, wp has the review budget paused, wg has no
// grant; the ledger holds the signed review rows and the delegate's
// decisions, the same rows the Node run that gave awNodeCases read.
func waiveFixture(t *testing.T) runFixture {
	t.Helper()
	f := newAutopilotFixture(t)
	blocks := `,"blocks":{"B2":{"status":"open"},"B3":{"status":"open"}}`
	grant := grantJSON("active", "2026-11-11T12:30:00.000Z", "null", "null")
	waives := strings.Replace(grant, `"round:extra",`, `"waive:warning",`, 1)
	f.record(t, "wa", "active", blocks+waives)
	f.record(t, "wn", "active", blocks+grant)
	f.record(t, "wp", "active", blocks+strings.Replace(waives, `"link":null`, `"link":null,"paused":{"review":{"at":"2026-11-11T09:00:00.000Z"}}`, 1))
	f.record(t, "wg", "active", blocks)
	signed := func(row map[string]any) string {
		row, err := sign.SignRow(row, f.key)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(row)
		return string(data)
	}
	tri := func(run, block, file, finding, severity string) string {
		return signed(map[string]any{"run": run, "event": "review.triage", "block": block, "file": file, "finding": finding, "severity": severity, "verdict": "fix_now"})
	}
	dec := func(id, file, finding string, change map[string]any) string {
		row := map[string]any{"run": "wa", "event": "autopilot.decision", "decision_id": id, "grant_id": "ap-0000abcd", "scope": "waive:nit", "acted": true, "decision": "waive", "subject": map[string]any{"block": "B2", "file": file, "finding": finding}, "ts": "2026-11-11T09:55:00.000Z"}
		for k, v := range change {
			row[k] = v
		}
		return signed(row)
	}
	secretFile := "src/Xk9mQ2vR7tL4pW8nZ3cF6hJ1bY5dG0sA.mjs"
	writeLedger(t, f,
		tri("wa", "B2", "src/a.mjs", "N1", "nit"),
		tri("wa", "B2", "src/a.mjs", "W1", "warning"),
		signed(map[string]any{"run": "wa", "event": "review.result", "block": "B2", "file": "src/a.mjs", "status": "reviewed", "open": []any{map[string]any{"id": "C1", "severity": "critical"}, map[string]any{"id": "N1", "severity": "nit"}}}),
		`{"run":"wa","event":"review.triage","block":"B2","file":"src/a.mjs","finding":"U1","severity":"nit"}`,
		tri("wa", "B2", "src/a.mjs", "K1", "major"),
		tri("wa", "B3", "src/a.mjs", "M1", "nit"),
		tri("wn", "B2", "src/a.mjs", "W1", "warning"),
		tri("wp", "B2", "src/a.mjs", "N1", "nit"),
		dec("d-n1", "src/a.mjs", "N1", nil),
		dec("d-w1", "src/a.mjs", "W1", map[string]any{"scope": "waive:warning"}),
		dec("d-n2", "src/a.mjs", "N2", nil),
		dec("d-old", "src/a.mjs", "N1", map[string]any{"ts": "2026-11-11T08:00:00.000Z"}),
		dec("d-fix", "src/a.mjs", "N1", map[string]any{"decision": "fix"}),
		dec("d-k1", "src/a.mjs", "K1", nil),
		tri("wa", "B2", secretFile, "N1", "nit"),
		dec("d-s", secretFile, "N1", nil),
	)
	bin := filepath.Join(f.home, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(awFakeGh), 0700); err != nil {
		t.Fatal(err)
	}
	return f
}

type awNodeCase struct {
	args     []string
	mode     string
	noGh     bool
	code     int
	out, err string
}

// B681: `autopilot waive` runs in Go with Node's output, rows and gh calls.
// Each case ran first through Node's runAutopilot on the same fixture (same
// key, same clock, same fake gh, in this order); its exit code, stdout,
// stderr, the ledger lines it appended (MACs included) and the gh log are
// pinned below. Covered: each missing flag, a blank reason, critical
// severity, a bad finding id, a reason secret Node scrubs and one it
// refuses, proof (also on a missing run), a bad decision id, outside paths,
// no run, an unknown block, not-recorded/unverified/unknown/critical/
// mismatched severity, a decision about another finding or block, too old,
// "fix", the wrong scope or missing, a grant that does not allow the scope,
// a paused review budget, no grant; a created issue (the --label retry),
// a deduped comment, no gh, gh not logged in, no repo, a failed create and
// a title gh must not see (secret-in-body).
func TestAutopilotWaiveInGo(t *testing.T) {
	f := waiveFixture(t)
	empty := t.TempDir()
	withGh := filepath.Join(f.home, "bin") + string(os.PathListSeparator) + os.Getenv("PATH")
	ledgerPath := filepath.Join(f.home, ".code-forge", "ledger", "unit.jsonl")
	before, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range awNodeCases {
		if err := os.WriteFile(filepath.Join(f.home, "gh-mode"), []byte(tc.mode), 0600); err != nil {
			t.Fatal(err)
		}
		if tc.noGh {
			t.Setenv("PATH", empty)
		} else {
			t.Setenv("PATH", withGh)
		}
		code, out, errText := autopilotCall(t, append([]string{"waive"}, tc.args...)...)
		if code != tc.code || out != tc.out || errText != tc.err {
			t.Fatalf("%q = %d %q %q, Node %d %q %q", tc.args, code, out, errText, tc.code, tc.out, tc.err)
		}
	}
	after, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	added := strings.Split(strings.TrimSuffix(strings.TrimPrefix(string(after), string(before)), "\n"), "\n")
	if strings.Join(added, "\n") != strings.Join(awNodeLedger, "\n") {
		t.Fatalf("ledger rows:\n%s\nNode:\n%s", strings.Join(added, "\n"), strings.Join(awNodeLedger, "\n"))
	}
	ws, err := filepath.EvalSymlinks(f.workspace)
	if err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(filepath.Join(f.home, "gh.log"))
	if err != nil {
		t.Fatal(err)
	}
	got := regexp.MustCompile(`--body-file \S+`).ReplaceAllString(strings.ReplaceAll(string(logged), ws, "{WS}"), "--body-file F")
	if got != awNodeGhLog {
		t.Fatalf("gh log:\n%s\nNode:\n%s", got, awNodeGhLog)
	}
}

// B681: what Go would read differently keeps the waive on Node, before any
// write: a block name on Object.prototype, a path posix.normalize rewrites.
func TestAutopilotWaiveHandsOver(t *testing.T) {
	f := waiveFixture(t)
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	t.Setenv("CODE_FORGE_NATIVE", "1")
	rest := []string{"--finding", "N1", "--severity", "nit", "--reason", "r", "--decision", "d-n1"}
	cases := [][]string{
		{"--run", "wa", "--block", "constructor", "--file", "src/a.mjs"},
		{"--run", "wa", "--block", "B2", "--file", "src/./a.mjs"},
		{"--run", "wa", "--block", "B2", "--file", "src//a.mjs"},
		{"--run", "wa", "--block", "B2", "--file", "src/a.mjs/"},
		{"--run", "wzz", "--block", "B2", "--file", "src/./a.mjs"},
	}
	ledgerPath := filepath.Join(f.home, ".code-forge", "ledger", "unit.jsonl")
	before, _ := os.ReadFile(ledgerPath)
	for _, args := range cases {
		if code, out, _ := autopilotCall(t, append(append([]string{"waive"}, args...), rest...)...); code != 1 || out != "" {
			t.Fatalf("%v: refusal exit = %d stdout %q, want 1", args, code, out)
		}
	}
	data, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "native", "handovers.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != len(cases) {
		t.Fatalf("handover rows = %d, want %d", len(lines), len(cases))
	}
	for i, line := range lines {
		var row map[string]string
		if err := json.Unmarshal([]byte(line), &row); err != nil || row["reason"] != "autopilot-action" || row["sub"] != "waive" {
			t.Fatalf("row %d = %s", i, line)
		}
	}
	if after, _ := os.ReadFile(ledgerPath); string(after) != string(before) {
		t.Fatalf("a hand-over wrote rows: %s", after[len(before):])
	}
	if _, err := os.Stat(filepath.Join(f.home, "gh.log")); !os.IsNotExist(err) {
		t.Fatalf("a hand-over ran gh: %v", err)
	}
}

// B681: start with stdin at a terminal and stdout piped asks in Go (clack
// still prompts there), through the piped confirm, with Node's message and
// cancel text; a yes writes the grant.
func TestAutopilotStartPipedPromptInGo(t *testing.T) {
	f := newAutopilotFixture(t)
	f.record(t, "r-a", "active", "")
	autopilotIsTTY = func() bool { return true }
	autopilotPromptable = func() bool { return false }
	asked := []string{}
	answer := humanask.Cancelled
	autopilotConfirmPiped = func(message string) (humanask.Result, error) {
		asked = append(asked, message)
		return answer, nil
	}
	args := []string{"start", "--run", "r-a", "--until", "2026-11-11T15:30:00+00:00", "--delegate", "L2", "--allow", "round:extra"}
	code, out, errText := autopilotCall(t, args...)
	if code != 1 || out != "cancelled — no grant\n" || errText != "" {
		t.Fatalf("cancel = %d %q %q", code, out, errText)
	}
	if len(asked) != 1 || asked[0] != "Start autopilot for run r-a until 2026-11-11T15:30:00.000Z?" {
		t.Fatalf("prompt = %q", asked)
	}
	if rows := apSignedRows(t, f); len(rows) != 0 {
		t.Fatalf("a cancel wrote rows: %#v", rows)
	}
	answer = humanask.Confirmed
	code, out, errText = autopilotCall(t, args...)
	want := "autopilot run r-a: grant ap-1234abcd active until 2026-11-11T15:30:00.000Z (5h 30m)\n  delegate L2 · allow round:extra · budget no caps · stop at 90%\n"
	if code != 0 || out != want || errText != "" {
		t.Fatalf("confirm = %d %q %q", code, out, errText)
	}
	if rows := apSignedRows(t, f); len(rows) != 1 || rows[0]["event"] != "autopilot.grant" {
		t.Fatalf("grant rows: %#v", rows)
	}
	// stdin is not a terminal either: no prompt, the owner must pass --yes
	autopilotIsTTY = func() bool { return false }
	f.record(t, "r-b", "active", "")
	code, out, errText = autopilotCall(t, "start", "--run", "r-b", "--until", "2026-11-11T15:30:00+00:00", "--delegate", "L2", "--allow", "round:extra")
	if code != 2 || out != "" || errText != "autopilot start: no terminal to confirm; the owner starts autopilot — pass --yes to start it without a prompt\n" || len(asked) != 2 {
		t.Fatalf("no terminal = %d %q %q %d", code, out, errText, len(asked))
	}
}

var awNodeCases = []awNodeCase{
	{[]string{"--run", "wa", "--file", "src/a.mjs", "--finding", "N1", "--severity", "nit", "--reason", "cosmetic only", "--decision", "d-n1"}, "create", false, 2, "", "autopilot waive: autopilot waive needs --block\n"},
	{[]string{"--run", "wa", "--block", "B2", "--finding", "N1", "--severity", "nit", "--reason", "cosmetic only", "--decision", "d-n1"}, "create", false, 2, "", "autopilot waive: autopilot waive needs --file\n"},
	{[]string{"--run", "wa", "--block", "B2", "--file", "src/a.mjs", "--severity", "nit", "--reason", "cosmetic only", "--decision", "d-n1"}, "create", false, 2, "", "autopilot waive: autopilot waive needs --finding\n"},
	{[]string{"--run", "wa", "--block", "B2", "--file", "src/a.mjs", "--finding", "N1", "--reason", "cosmetic only", "--decision", "d-n1"}, "create", false, 2, "", "autopilot waive: autopilot waive needs --severity\n"},
	{[]string{"--run", "wa", "--block", "B2", "--file", "src/a.mjs", "--finding", "N1", "--severity", "nit", "--decision", "d-n1"}, "create", false, 2, "", "autopilot waive: autopilot waive needs --reason\n"},
	{[]string{"--run", "wa", "--block", "B2", "--file", "src/a.mjs", "--finding", "N1", "--severity", "nit", "--reason", "cosmetic only"}, "create", false, 2, "", "autopilot waive: autopilot waive needs --decision\n"},
	{[]string{"--run", "wa", "--block", "B2", "--file", "src/a.mjs", "--finding", "N1", "--severity", "nit", "--reason", " \u00a0 ", "--decision", "d-n1"}, "create", false, 2, "", "autopilot waive: autopilot waive needs --reason\n"},
	{[]string{"--run", "wa", "--block", "B2", "--file", "src/a.mjs", "--finding", "N1", "--severity", "critical", "--reason", "cosmetic only", "--decision", "d-n1"}, "create", false, 2, "", "autopilot waive: --severity must be warning or nit (critical findings are never delegated)\n"},
	{[]string{"--run", "wa", "--block", "B2", "--file", "src/a.mjs", "--finding", "-bad", "--severity", "nit", "--reason", "cosmetic only", "--decision", "d-n1"}, "create", false, 2, "", "autopilot waive: a finding id is required (letters, digits, . _ : -)\n"},
	{[]string{"--run", "wa", "--block", "B2", "--file", "src/a.mjs", "--finding", "N1", "--severity", "nit", "--reason", "see ghp_A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8", "--decision", "d-n1"}, "create", false, 0, "autopilot waive run wa: waived N1 (nit) in src/a.mjs, block B2 (by: autopilot, grant ap-0000abcd) \u00b7 issue https://github.com/acme/widgets/issues/8 (created)\n", ""},
	{[]string{"--run", "wa", "--block", "B2", "--file", "src/a.mjs", "--finding", "proof", "--severity", "nit", "--reason", "cosmetic only", "--decision", "x y"}, "create", false, 1, "", "autopilot waive: refused \u2014 proof is never delegated; only the owner can waive it (block waive)\n"},
	{[]string{"--run", "wa", "--block", "B2", "--file", "src/a.mjs", "--finding", "N1", "--severity", "nit", "--reason", "cosmetic only", "--decision", "x y"}, "create", false, 2, "", "autopilot waive: autopilot waive needs --decision <id> (the delegate's acted decision from autopilot ask)\n"},
	{[]string{"--run", "wa", "--block", "B2", "--file", "../x", "--finding", "N1", "--severity", "nit", "--reason", "cosmetic only", "--decision", "d-n1"}, "create", false, 2, "", "autopilot waive: --file must be a repo-relative path\n"},
	{[]string{"--run", "wa", "--block", "B2", "--file", "/abs", "--finding", "N1", "--severity", "nit", "--reason", "cosmetic only", "--decision", "d-n1"}, "create", false, 2, "", "autopilot waive: --file must be a repo-relative path\n"},
	{[]string{"--run", "wzz", "--block", "B2", "--file", "src/a.mjs", "--finding", "N1", "--severity", "nit", "--reason", "cosmetic only", "--decision", "d-n1"}, "create", false, 1, "", "autopilot waive: no run record for wzz\n"},
	{[]string{"--run", "wzz", "--block", "B2", "--file", "src/a.mjs", "--finding", "proof", "--severity", "nit", "--reason", "cosmetic only", "--decision", "d-n1"}, "create", false, 1, "", "autopilot waive: refused \u2014 proof is never delegated; only the owner can waive it (block waive)\n"},
	{[]string{"--run", "wzz", "--block", "B2", "--file", "src/a.mjs", "--finding", "N1", "--severity", "nit", "--reason", "cosmetic only", "--decision", "x y"}, "create", false, 2, "", "autopilot waive: autopilot waive needs --decision <id> (the delegate's acted decision from autopilot ask)\n"},
	{[]string{"--run", "wa", "--block", "B9", "--file", "src/a.mjs", "--finding", "N1", "--severity", "nit", "--reason", "cosmetic only", "--decision", "d-n1"}, "create", false, 1, "", "autopilot waive: block B9 is not in run wa\n"},
	{[]string{"--run", "wa", "--block", "B2", "--file", "src/a.mjs", "--finding", "Z9", "--severity", "nit", "--reason", "cosmetic only", "--decision", "d-n1"}, "create", false, 1, "", "autopilot waive: refused \u2014 finding Z9 in src/a.mjs has no verifiable recorded severity (not-recorded); critical findings are never delegated\n"},
	{[]string{"--run", "wa", "--block", "B2", "--file", "src/a.mjs", "--finding", "U1", "--severity", "nit", "--reason", "cosmetic only", "--decision", "d-n1"}, "create", false, 1, "", "autopilot waive: refused \u2014 finding U1 in src/a.mjs has no verifiable recorded severity (unverified); critical findings are never delegated\n"},
	{[]string{"--run", "wa", "--block", "B2", "--file", "src/a.mjs", "--finding", "K1", "--severity", "nit", "--reason", "cosmetic only", "--decision", "d-k1"}, "create", false, 1, "", "autopilot waive: refused \u2014 finding K1 in src/a.mjs has no verifiable recorded severity (unknown); critical findings are never delegated\n"},
	{[]string{"--run", "wa", "--block", "B2", "--file", "src/a.mjs", "--finding", "C1", "--severity", "nit", "--reason", "cosmetic only", "--decision", "d-n1"}, "create", false, 1, "", "autopilot waive: refused \u2014 finding C1 in src/a.mjs is recorded as critical; critical findings are never delegated\n"},
	{[]string{"--run", "wa", "--block", "B2", "--file", "src/a.mjs", "--finding", "W1", "--severity", "nit", "--reason", "cosmetic only", "--decision", "d-n1"}, "create", false, 1, "", "autopilot waive: refused \u2014 finding W1 in src/a.mjs is recorded as warning, not nit\n"},
	{[]string{"--run", "wa", "--block", "B2", "--file", "src/a.mjs", "--finding", "N1", "--severity", "nit", "--reason", "cosmetic only", "--decision", "d-n2"}, "create", false, 1, "", "autopilot waive: refused \u2014 decision d-n2 is about another finding; the question goes to the owner (autopilot ask, then --decision <id>)\n"},
	{[]string{"--run", "wa", "--block", "B2", "--file", "src/a.mjs", "--finding", "N1", "--severity", "nit", "--reason", "cosmetic only", "--decision", "d-old"}, "create", false, 1, "", "autopilot waive: refused \u2014 decision d-old is older than 30 min (or not yet made); the question goes to the owner (autopilot ask, then --decision <id>)\n"},
	{[]string{"--run", "wa", "--block", "B2", "--file", "src/a.mjs", "--finding", "N1", "--severity", "nit", "--reason", "cosmetic only", "--decision", "d-fix"}, "create", false, 1, "", "autopilot waive: refused \u2014 decision d-fix decided \"fix\", not \"waive\"; the question goes to the owner (autopilot ask, then --decision <id>)\n"},
	{[]string{"--run", "wa", "--block", "B2", "--file", "src/a.mjs", "--finding", "N1", "--severity", "nit", "--reason", "cosmetic only", "--decision", "d-w1"}, "create", false, 1, "", "autopilot waive: refused \u2014 decision d-w1 is for waive:warning, not waive:nit; the question goes to the owner (autopilot ask, then --decision <id>)\n"},
	{[]string{"--run", "wa", "--block", "B2", "--file", "src/a.mjs", "--finding", "N1", "--severity", "nit", "--reason", "cosmetic only", "--decision", "d-none"}, "create", false, 1, "", "autopilot waive: refused \u2014 no autopilot.decision row d-none in this run; the question goes to the owner (autopilot ask, then --decision <id>)\n"},
	{[]string{"--run", "wn", "--block", "B2", "--file", "src/a.mjs", "--finding", "W1", "--severity", "warning", "--reason", "cosmetic only", "--decision", "d-w1"}, "create", false, 1, "", "autopilot waive: refused \u2014 the grant refuses (not-allowed); the question goes to the owner\n"},
	{[]string{"--run", "wp", "--block", "B2", "--file", "src/a.mjs", "--finding", "N1", "--severity", "nit", "--reason", "cosmetic only", "--decision", "d-n1"}, "create", false, 1, "", "autopilot waive: refused \u2014 the grant refuses (paused); the question goes to the owner\n"},
	{[]string{"--run", "wg", "--block", "B2", "--file", "src/a.mjs", "--finding", "N1", "--severity", "nit", "--reason", "cosmetic only", "--decision", "d-n1"}, "create", false, 1, "", "autopilot waive: refused \u2014 finding N1 in src/a.mjs has no verifiable recorded severity (not-recorded); critical findings are never delegated\n"},
	{[]string{"--run", "wa", "--block", "B3", "--file", "src/a.mjs", "--finding", "M1", "--severity", "nit", "--reason", "cosmetic only", "--decision", "d-n1"}, "create", false, 1, "", "autopilot waive: refused \u2014 decision d-n1 is about another block; the question goes to the owner (autopilot ask, then --decision <id>)\n"},
	{[]string{"--run", "wa", "--block", "B2", "--file", "src/a.mjs", "--finding", "N1", "--severity", "nit", "--reason", "  line one\r\nline two  ", "--decision", "d-n1"}, "create", false, 0, "autopilot waive run wa: waived N1 (nit) in src/a.mjs, block B2 (by: autopilot, grant ap-0000abcd) \u00b7 issue https://github.com/acme/widgets/issues/8 (created)\n", ""},
	{[]string{"--run", "wa", "--block", "B2", "--file", "src/a.mjs", "--finding", "W1", "--severity", "warning", "--reason", "dup", "--decision", "d-w1"}, "dup", false, 0, "autopilot waive run wa: waived W1 (warning) in src/a.mjs, block B2 (by: autopilot, grant ap-0000abcd) \u00b7 issue https://github.com/acme/widgets/issues/7 (commented)\n", ""},
	{[]string{"--run", "wa", "--block", "B2", "--file", "src/a.mjs", "--finding", "N1", "--severity", "nit", "--reason", "no gh", "--decision", "d-n1"}, "create", true, 0, "autopilot waive run wa: waived N1 (nit) in src/a.mjs, block B2 (by: autopilot, grant ap-0000abcd) \u00b7 issue none (gh-missing; the waiver still stands)\n", ""},
	{[]string{"--run", "wa", "--block", "B2", "--file", "src/a.mjs", "--finding", "N1", "--severity", "nit", "--reason", "no login", "--decision", "d-n1"}, "nologin", false, 0, "autopilot waive run wa: waived N1 (nit) in src/a.mjs, block B2 (by: autopilot, grant ap-0000abcd) \u00b7 issue none (gh-not-logged-in; the waiver still stands)\n", ""},
	{[]string{"--run", "wa", "--block", "B2", "--file", "src/a.mjs", "--finding", "N1", "--severity", "nit", "--reason", "no repo", "--decision", "d-n1"}, "norepo", false, 0, "autopilot waive run wa: waived N1 (nit) in src/a.mjs, block B2 (by: autopilot, grant ap-0000abcd) \u00b7 issue none (no-project-repo; the waiver still stands)\n", ""},
	{[]string{"--run", "wa", "--block", "B2", "--file", "src/a.mjs", "--finding", "N1", "--severity", "nit", "--reason", "create fails", "--decision", "d-n1"}, "createfail", false, 0, "autopilot waive run wa: waived N1 (nit) in src/a.mjs, block B2 (by: autopilot, grant ap-0000abcd) \u00b7 issue none (create-failed; the waiver still stands)\n", ""},
	{[]string{"--run", "wa", "--block", "B2", "--file", "src/Xk9mQ2vR7tL4pW8nZ3cF6hJ1bY5dG0sA.mjs", "--finding", "N1", "--severity", "nit", "--reason", "secret file", "--decision", "d-s"}, "create", false, 0, "autopilot waive run wa: waived N1 (nit) in src/Xk9mQ2vR7tL4pW8nZ3cF6hJ1bY5dG0sA.mjs, block B2 (by: autopilot, grant ap-0000abcd) \u00b7 issue none (secret-in-body; the waiver still stands)\n", ""},
}

var awNodeLedger = []string{
	`{"run":"wa","event":"review.waived","block":"B2","file":"src/a.mjs","finding":"N1","reason":"see [REDACTED]","by":"autopilot","grant_id":"ap-0000abcd","decision_id":"d-n1","severity":"nit","issue":"https://github.com/acme/widgets/issues/8","ts":"2026-11-11T10:00:00.000Z","mac":"ca54253a92d13dbae1334671ebf8a954d6a0d5f5c0018c8b1df55a5c4c15cf93","tokens_source":null,"cost_source":null}`,
	`{"run":"wa","event":"review.waived","block":"B2","file":"src/a.mjs","finding":"N1","reason":"line one\r\nline two","by":"autopilot","grant_id":"ap-0000abcd","decision_id":"d-n1","severity":"nit","issue":"https://github.com/acme/widgets/issues/8","ts":"2026-11-11T10:00:00.000Z","mac":"821356e4213984548c2a95237a6055c00ad08c2ce858d349d47eda4af703fba7","tokens_source":null,"cost_source":null}`,
	`{"run":"wa","event":"review.waived","block":"B2","file":"src/a.mjs","finding":"W1","reason":"dup","by":"autopilot","grant_id":"ap-0000abcd","decision_id":"d-w1","severity":"warning","issue":"https://github.com/acme/widgets/issues/7","ts":"2026-11-11T10:00:00.000Z","mac":"27d2514c27a24c51f1b418ff32be755ebf72795122425bf670187f0e918872cd","tokens_source":null,"cost_source":null}`,
	`{"run":"wa","event":"review.waived","block":"B2","file":"src/a.mjs","finding":"N1","reason":"no gh","by":"autopilot","grant_id":"ap-0000abcd","decision_id":"d-n1","severity":"nit","issue":null,"ts":"2026-11-11T10:00:00.000Z","mac":"5c5a5c14c7bde2a65d98c8e264799c9d3f6a2a82ec8b4765d80c54a014f721e2","tokens_source":null,"cost_source":null}`,
	`{"run":"wa","event":"autopilot.issue_failed","block":"B2","file":"src/a.mjs","finding":"N1","grant_id":"ap-0000abcd","reason":"gh-missing","ts":"2026-11-11T10:00:00.000Z","mac":"6842c40f6a733e51e1037c9eefe97428a1741320a6d15be9b8eae008feba09a7","tokens_source":null,"cost_source":null}`,
	`{"run":"wa","event":"review.waived","block":"B2","file":"src/a.mjs","finding":"N1","reason":"no login","by":"autopilot","grant_id":"ap-0000abcd","decision_id":"d-n1","severity":"nit","issue":null,"ts":"2026-11-11T10:00:00.000Z","mac":"8691eeef6786fdf7dff3efaaa0b21ae81593a2e0c4275817c6039b1f67652629","tokens_source":null,"cost_source":null}`,
	`{"run":"wa","event":"autopilot.issue_failed","block":"B2","file":"src/a.mjs","finding":"N1","grant_id":"ap-0000abcd","reason":"gh-not-logged-in","ts":"2026-11-11T10:00:00.000Z","mac":"e3dc633b18e294ddbe0dc3ad744ac0bac4a41fd2bfd61efe7469c7d9703d84fc","tokens_source":null,"cost_source":null}`,
	`{"run":"wa","event":"review.waived","block":"B2","file":"src/a.mjs","finding":"N1","reason":"no repo","by":"autopilot","grant_id":"ap-0000abcd","decision_id":"d-n1","severity":"nit","issue":null,"ts":"2026-11-11T10:00:00.000Z","mac":"b978e5319e2205f0a400223a6c42ef0b80054a5c17458fe8d8ce8ca259efa0df","tokens_source":null,"cost_source":null}`,
	`{"run":"wa","event":"autopilot.issue_failed","block":"B2","file":"src/a.mjs","finding":"N1","grant_id":"ap-0000abcd","reason":"no-project-repo","ts":"2026-11-11T10:00:00.000Z","mac":"c675b41ddb1042f3dc25f68128f7158cd06534cbc82fe7d2d7a7954cae3fe5f7","tokens_source":null,"cost_source":null}`,
	`{"run":"wa","event":"review.waived","block":"B2","file":"src/a.mjs","finding":"N1","reason":"create fails","by":"autopilot","grant_id":"ap-0000abcd","decision_id":"d-n1","severity":"nit","issue":null,"ts":"2026-11-11T10:00:00.000Z","mac":"954fb15218bb173275436e69fbb9931b9a840ed882b531c5a690acb868374539","tokens_source":null,"cost_source":null}`,
	`{"run":"wa","event":"autopilot.issue_failed","block":"B2","file":"src/a.mjs","finding":"N1","grant_id":"ap-0000abcd","reason":"create-failed","ts":"2026-11-11T10:00:00.000Z","mac":"b5fb9991356f876be4cd9bb82f84744139d5e5a28b55c885797697c4550d1e42","tokens_source":null,"cost_source":null}`,
	`{"run":"wa","event":"review.waived","block":"B2","file":"src/Xk9mQ2vR7tL4pW8nZ3cF6hJ1bY5dG0sA.mjs","finding":"N1","reason":"secret file","by":"autopilot","grant_id":"ap-0000abcd","decision_id":"d-s","severity":"nit","issue":null,"ts":"2026-11-11T10:00:00.000Z","mac":"117eb212a7e19d4ef8e353352c72b2900ef4e82797423e4d5a19479c3bf879b0","tokens_source":null,"cost_source":null}`,
	`{"run":"wa","event":"autopilot.issue_failed","block":"B2","file":"src/Xk9mQ2vR7tL4pW8nZ3cF6hJ1bY5dG0sA.mjs","finding":"N1","grant_id":"ap-0000abcd","reason":"secret-in-body","ts":"2026-11-11T10:00:00.000Z","mac":"787e729c8c9b6c71b2402204c07efb028eca8edf7a21a4967a6315ac31529d70","tokens_source":null,"cost_source":null}`,
}

const awNodeGhLog = "ARGV auth status\n" +
	"ARGV repo view --json nameWithOwner\n" +
	"CWD {WS}\n" +
	"ARGV issue list --repo acme/widgets --state all --search \"<!-- code-forge-waiver: wa/B2/src/a.mjs/N1 -->\" in:body --json number,url,state,body --limit 100\n" +
	"ARGV issue create --repo acme/widgets --title code-forge autopilot waived nit N1 in src/a.mjs (block B2) --body-file F --label autopilot-waiver\n" +
	"BODY<<\n" +
	"code-forge autopilot waived a nit review finding while the owner was away. It still needs a follow-up.\n" +
	"\n" +
	"| field | value |\n" +
	"|---|---|\n" +
	"| run | wa |\n" +
	"| block | B2 |\n" +
	"| file | `src/a.mjs` |\n" +
	"| finding | N1 |\n" +
	"| severity | nit |\n" +
	"| grant | ap-0000abcd |\n" +
	"| waived at | 2026-11-11T10:00:00.000Z |\n" +
	"\n" +
	"Reason: see [REDACTED]\n" +
	"\n" +
	"<!-- code-forge-waiver: wa/B2/src/a.mjs/N1 -->\n" +
	">>BODY\n" +
	"ARGV issue create --repo acme/widgets --title code-forge autopilot waived nit N1 in src/a.mjs (block B2) --body-file F\n" +
	"BODY<<\n" +
	"code-forge autopilot waived a nit review finding while the owner was away. It still needs a follow-up.\n" +
	"\n" +
	"| field | value |\n" +
	"|---|---|\n" +
	"| run | wa |\n" +
	"| block | B2 |\n" +
	"| file | `src/a.mjs` |\n" +
	"| finding | N1 |\n" +
	"| severity | nit |\n" +
	"| grant | ap-0000abcd |\n" +
	"| waived at | 2026-11-11T10:00:00.000Z |\n" +
	"\n" +
	"Reason: see [REDACTED]\n" +
	"\n" +
	"<!-- code-forge-waiver: wa/B2/src/a.mjs/N1 -->\n" +
	">>BODY\n" +
	"ARGV auth status\n" +
	"ARGV repo view --json nameWithOwner\n" +
	"CWD {WS}\n" +
	"ARGV issue list --repo acme/widgets --state all --search \"<!-- code-forge-waiver: wa/B2/src/a.mjs/N1 -->\" in:body --json number,url,state,body --limit 100\n" +
	"ARGV issue create --repo acme/widgets --title code-forge autopilot waived nit N1 in src/a.mjs (block B2) --body-file F --label autopilot-waiver\n" +
	"BODY<<\n" +
	"code-forge autopilot waived a nit review finding while the owner was away. It still needs a follow-up.\n" +
	"\n" +
	"| field | value |\n" +
	"|---|---|\n" +
	"| run | wa |\n" +
	"| block | B2 |\n" +
	"| file | `src/a.mjs` |\n" +
	"| finding | N1 |\n" +
	"| severity | nit |\n" +
	"| grant | ap-0000abcd |\n" +
	"| waived at | 2026-11-11T10:00:00.000Z |\n" +
	"\n" +
	"Reason: line one line two\n" +
	"\n" +
	"<!-- code-forge-waiver: wa/B2/src/a.mjs/N1 -->\n" +
	">>BODY\n" +
	"ARGV issue create --repo acme/widgets --title code-forge autopilot waived nit N1 in src/a.mjs (block B2) --body-file F\n" +
	"BODY<<\n" +
	"code-forge autopilot waived a nit review finding while the owner was away. It still needs a follow-up.\n" +
	"\n" +
	"| field | value |\n" +
	"|---|---|\n" +
	"| run | wa |\n" +
	"| block | B2 |\n" +
	"| file | `src/a.mjs` |\n" +
	"| finding | N1 |\n" +
	"| severity | nit |\n" +
	"| grant | ap-0000abcd |\n" +
	"| waived at | 2026-11-11T10:00:00.000Z |\n" +
	"\n" +
	"Reason: line one line two\n" +
	"\n" +
	"<!-- code-forge-waiver: wa/B2/src/a.mjs/N1 -->\n" +
	">>BODY\n" +
	"ARGV auth status\n" +
	"ARGV repo view --json nameWithOwner\n" +
	"CWD {WS}\n" +
	"ARGV issue list --repo acme/widgets --state all --search \"<!-- code-forge-waiver: wa/B2/src/a.mjs/W1 -->\" in:body --json number,url,state,body --limit 100\n" +
	"ARGV issue comment 7 --repo acme/widgets --body-file F\n" +
	"BODY<<\n" +
	"Waived again by code-forge autopilot at 2026-11-11T10:00:00.000Z (grant ap-0000abcd). Reason: dup\n" +
	"\n" +
	"<!-- code-forge-waiver: wa/B2/src/a.mjs/W1 -->\n" +
	">>BODY\n" +
	"ARGV auth status\n" +
	"ARGV auth status\n" +
	"ARGV repo view --json nameWithOwner\n" +
	"CWD {WS}\n" +
	"ARGV auth status\n" +
	"ARGV repo view --json nameWithOwner\n" +
	"CWD {WS}\n" +
	"ARGV issue list --repo acme/widgets --state all --search \"<!-- code-forge-waiver: wa/B2/src/a.mjs/N1 -->\" in:body --json number,url,state,body --limit 100\n" +
	"ARGV issue create --repo acme/widgets --title code-forge autopilot waived nit N1 in src/a.mjs (block B2) --body-file F --label autopilot-waiver\n" +
	"BODY<<\n" +
	"code-forge autopilot waived a nit review finding while the owner was away. It still needs a follow-up.\n" +
	"\n" +
	"| field | value |\n" +
	"|---|---|\n" +
	"| run | wa |\n" +
	"| block | B2 |\n" +
	"| file | `src/a.mjs` |\n" +
	"| finding | N1 |\n" +
	"| severity | nit |\n" +
	"| grant | ap-0000abcd |\n" +
	"| waived at | 2026-11-11T10:00:00.000Z |\n" +
	"\n" +
	"Reason: create fails\n" +
	"\n" +
	"<!-- code-forge-waiver: wa/B2/src/a.mjs/N1 -->\n" +
	">>BODY\n" +
	"ARGV auth status\n" +
	"ARGV repo view --json nameWithOwner\n" +
	"CWD {WS}\n"
