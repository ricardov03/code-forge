package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ledgerCall runs `code-forge ledger <args>` in process. A missing Node makes
// any hand-over fail, so a pass proves Go served the case.
func ledgerCall(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	stdout, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	beforeOut, beforeErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdout, stderr
	code, runErr := run(append([]string{"ledger"}, args...))
	os.Stdout, os.Stderr = beforeOut, beforeErr
	_ = stdout.Close()
	_ = stderr.Close()
	out, _ := os.ReadFile(stdout.Name())
	errText, _ := os.ReadFile(stderr.Name())
	if runErr != nil {
		errText = append(errText, []byte("handover: "+runErr.Error())...)
	}
	return code, string(out), string(errText)
}

func ledgerHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-b457-node")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	if err := os.MkdirAll(filepath.Join(home, ".code-forge", "ledger"), 0700); err != nil {
		t.Fatal(err)
	}
	return home
}

func ledgerWrite(t *testing.T, home, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, ".code-forge", "ledger", name), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func ledgerLines(t *testing.T, home, slug string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, ".code-forge", "ledger", slug+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	rows := []map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		row := map[string]any{}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("row %q: %v", line, err)
		}
		rows = append(rows, row)
	}
	return rows
}

type ledgerCase struct {
	args     []string
	code     int
	out, err string
}

func ledgerCheck(t *testing.T, cases []ledgerCase) {
	t.Helper()
	for _, tc := range cases {
		code, out, errText := ledgerCall(t, tc.args...)
		if code != tc.code || out != tc.out || errText != tc.err {
			t.Errorf("ledger %q:\n got (%d, %q, %q)\nwant (%d, %q, %q)", tc.args, code, out, errText, tc.code, tc.out, tc.err)
		}
	}
}

func TestLedgerGoSubcommandHelp(t *testing.T) {
	ledgerHome(t)
	ledgerCheck(t, []ledgerCase{
		{[]string{"calibration", "--help"}, 0, "usage: code-forge ledger calibration --slug <slug> [--question <id>]\n", ""},
		{[]string{"outcome", "-h"}, 0, "usage: code-forge ledger outcome --scan-git --slug <slug> [--cwd <dir>] [--days <n>]\nusage: code-forge ledger outcome --pr <n> --ci red|green|reverted --slug <slug>\n", ""},
		{[]string{"add", "--help"}, 0, "usage: code-forge ledger add coder --run <r> --block <b> --usd <n> [--note \"...\"]\n", ""},
		{[]string{"outside-review", "-h"}, 0, ledgerOutsideReviewUsage + "\n", ""},
	})
}

func TestLedgerGoCalibration(t *testing.T) {
	home := ledgerHome(t)
	ledgerWrite(t, home, "proj.jsonl", strings.Join([]string{
		`{"event":"decision","confidence":0.55,"decision_id":"a","question":"q1"}`,
		`{"event":"decision","confidence":0.92,"decision_id":"b","question":"q2"}`,
		`{"event":"decision","confidence":0.995,"decision_id":"c"}`,
		`{"event":"decision","confidence":1,"decision_id":"d"}`,
		`{"event":"decision","confidence":0.3,"decision_id":"e"}`,
		`{"event":"decision","confidence":0.7,"decision_id":7}`,
		`{"event":"outcome","decision_id":"a","outcome":"correct"}`,
		`{"event":"outcome","decision_id":"b","outcome":"reverted"}`,
		`{"event":"outcome","decision_id":"c","outcome":"weird"}`,
		`{"event":"outcome","decision_id":7,"outcome":"wrong"}`,
	}, "\n")+"\n")
	ledgerWrite(t, home, "zeta.2.jsonl", "{}\n")
	code, out, errText := ledgerCall(t, "calibration", "--slug", "proj")
	var got struct {
		Buckets  map[string]map[string]int `json:"buckets"`
		Unbacked int                       `json:"unbacked"`
		Unmapped int                       `json:"unmapped"`
	}
	if code != 0 || errText != "" || json.Unmarshal([]byte(out), &got) != nil {
		t.Fatalf("calibration: (%d, %q, %q)", code, out, errText)
	}
	if !strings.HasPrefix(out, "{\n  \"buckets\": {\n    \"0.50-0.60\": {\n      \"correct\": 1,") {
		t.Errorf("calibration is not Node's pretty JSON: %q", out)
	}
	want := map[string][3]int{"0.50-0.60": {1, 0, 0}, "0.70-0.80": {0, 1, 0}, "0.90-0.95": {0, 1, 0}, "0.99-1.00": {0, 0, 1}}
	for id, b := range got.Buckets {
		if w := want[id]; b["correct"] != w[0] || b["wrong"] != w[1] || b["unknown"] != w[2] {
			t.Errorf("bucket %s = %v, want %v", id, b, w)
		}
	}
	if got.Unbacked != 1 || got.Unmapped != 1 || len(got.Buckets) != 8 {
		t.Errorf("unbacked/unmapped/buckets = %d/%d/%d, want 1/1/8", got.Unbacked, got.Unmapped, len(got.Buckets))
	}
	code, out, _ = ledgerCall(t, "calibration", "--slug", "proj", "--question", "q2")
	if code != 0 || !strings.Contains(out, "\"unbacked\": 0,\n  \"unmapped\": 0\n}\n") || !strings.Contains(out, "\"0.90-0.95\": {\n      \"correct\": 0,\n      \"wrong\": 1,") {
		t.Errorf("calibration --question q2: (%d, %q)", code, out)
	}
	ledgerCheck(t, []ledgerCase{
		{[]string{"calibration", "--slug", "nope"}, 1, "", "ledger calibration: no ledger for slug \"nope\"; known slugs: proj, zeta\n"},
		{[]string{"calibration", "--slug", "Bad"}, 1, "", "ledger calibration: ledgerPath: slug must match /^[a-z0-9][a-z0-9-]*$/ (got \"Bad\")\n"},
		{[]string{"calibration", "--slug", "proj", "--question"}, 2, "", "ledger calibration: --question requires a value\n"},
	})
	ledgerWrite(t, home, "nul.jsonl", "{\"event\":\"x\"}\nnull\n")
	ledgerCheck(t, []ledgerCase{
		{[]string{"calibration", "--slug", "nul"}, 1, "", "ledger calibration: Cannot read properties of null (reading 'event')\n"},
	})
}

func TestLedgerGoOutcomeCI(t *testing.T) {
	home := ledgerHome(t)
	ledgerCheck(t, []ledgerCase{
		{[]string{"outcome", "--slug", "proj", "--pr", "0", "--ci", "red"}, 2, "", "ledger outcome: --pr must be a positive integer, got \"0\"\n"},
		{[]string{"outcome", "--slug", "proj", "--pr", "4", "--ci", "blue"}, 2, "", "ledger outcome: --ci must be red|green|reverted, got \"blue\"\n"},
		{[]string{"outcome", "--slug", "B", "--pr", "4", "--ci", "red"}, 1, "", "ledger outcome: ledgerPath: slug must match /^[a-z0-9][a-z0-9-]*$/ (got \"B\")\n"},
		{[]string{"outcome", "--slug", "proj", "--pr", "0042", "--ci", "green"}, 0, "recorded outcome for PR #42: green\n", ""},
		{[]string{"outcome", "--slug", "proj", "--pr", "7", "--ci", "red"}, 0, "recorded outcome for PR #7: red\n", ""},
	})
	rows := ledgerLines(t, home, "proj")
	if len(rows) != 2 || rows[0]["outcome"] != "correct" || rows[0]["outcome_source"] != "ci" || rows[0]["pr"] != 42.0 || rows[1]["outcome"] != "wrong" {
		t.Errorf("outcome rows: %v", rows)
	}
	data, _ := os.ReadFile(filepath.Join(home, ".code-forge", "ledger", "proj.jsonl"))
	if !strings.HasPrefix(string(data), `{"event":"outcome","outcome":"correct","outcome_source":"ci","pr":42,"ts":"`) {
		t.Errorf("outcome row bytes: %s", data)
	}
}

func TestLedgerGoScanGit(t *testing.T) {
	home := ledgerHome(t)
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	before := ledgerNow
	ledgerNow = func() time.Time { return now }
	t.Cleanup(func() { ledgerNow = before })
	repo := filepath.Join(home, "repo")
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0700); err != nil {
		t.Fatal(err)
	}
	git := func(date string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_COMMITTER_DATE="+date, "GIT_AUTHOR_DATE="+date)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git("2026-10-20T00:00:00Z", "init", "-q", "-b", "main", ".")
	write("src/a.txt", "a\n")
	write("b.txt", "b\n")
	git("2026-10-20T00:00:00Z", "add", "-A")
	git("2026-10-20T00:00:00Z", "commit", "-qm", "base")
	write("src/a.txt", "a2\n")
	write("b.txt", "b2\n")
	git("2026-11-09T00:00:00Z", "commit", "-qam", "feature")
	git("2026-11-10T00:00:00Z", "revert", "--no-edit", "HEAD")
	revert := git("2026-11-10T00:00:00Z", "rev-parse", "HEAD")
	git("2026-11-10T00:00:00Z", "revert", "--no-edit", "HEAD") // reapply: even depth, skipped
	write("b.txt", "x\n")
	git("2026-11-10T01:00:00Z", "commit", "-qam", `Revert "something"`)
	ledgerWrite(t, home, "proj.jsonl", fmt.Sprintf(
		"{\"event\":\"review.done\",\"file\":\"src/a.txt\",\"ts\":\"2026-11-08T00:00:00.000Z\",\"block\":\"B7\"}\n"+
			"{\"event\":\"review.done\",\"file\":%q,\"ts\":\"2026-11-08T00:00:00.000Z\"}\n", filepath.Join(repo, "b.txt")))
	ledgerCheck(t, []ledgerCase{
		{[]string{"outcome", "--scan-git", "--slug", "proj", "--cwd", repo, "--days", "0"}, 2, "", "ledger outcome --scan-git: --days must be a finite number > 0, got \"0\"\n"},
		{[]string{"outcome", "--scan-git", "--slug", "proj", "--cwd", repo, "--days"}, 2, "", "ledger outcome --scan-git: --days requires a value\n"},
		{[]string{"outcome", "--scan-git", "--slug", "proj", "--cwd", repo, "--days", "1"}, 0, "scanned git: 0 row(s) marked reverted\n", ""},
		{[]string{"outcome", "--scan-git", "--slug", "proj", "--cwd", repo}, 0, "scanned git: 3 row(s) marked reverted\n", ""},
		{[]string{"outcome", "--scan-git", "--slug", "proj", "--cwd", filepath.Join(repo, "src")}, 0, "scanned git: 0 row(s) marked reverted\n", ""},
	})
	rows := ledgerLines(t, home, "proj")
	if len(rows) != 5 {
		t.Fatalf("rows: %v", rows)
	}
	seen := map[string]any{}
	for _, row := range rows[2:] {
		if row["outcome"] != "reverted" || row["outcome_source"] != "git" {
			t.Errorf("row: %v", row)
		}
		seen[fmt.Sprint(row["commit"] == revert, "/", row["file"])] = row["block"]
	}
	if block, ok := seen["true/src/a.txt"]; !ok || block != "B7" {
		t.Errorf("revert of src/a.txt missing or without block B7: %v", seen)
	}
	if _, ok := seen["true/b.txt"]; !ok {
		t.Errorf("revert of b.txt missing: %v", seen)
	}
	if _, ok := seen["false/b.txt"]; !ok {
		t.Errorf("Revert \"something\" of b.txt missing: %v", seen)
	}
	code, _, errText := ledgerCall(t, "outcome", "--scan-git", "--slug", "proj", "--cwd", "/")
	if code != 1 || !strings.HasPrefix(errText, "ledger outcome: git rev-parse --show-toplevel failed: fatal: not a git repository") {
		t.Errorf("scan outside git: (%d, %q)", code, errText)
	}
}

func ledgerRunFixture(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, ".code-forge", "runs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	ws, _ := json.Marshal(filepath.Join(home, "ws"))
	record := fmt.Sprintf(`{"run_id":"r1","project":"proj","workspace":%s,"status":"running","engine":"claude","blocks":{"B1":{},"B2":{}}}`, ws)
	if err := os.WriteFile(filepath.Join(dir, "r1.json"), []byte(record), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "r1.key"), []byte(strings.Repeat("k", 32)), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLedgerGoAddCoder(t *testing.T) {
	home := ledgerHome(t)
	ledgerCheck(t, []ledgerCase{
		{[]string{"add", "coder", "--run", "r1"}, 2, "", "usage: code-forge ledger add coder --run <r> --block <b> --usd <n> [--note \"...\"]\n"},
		{[]string{"add", "coder", "--run", "r1", "--block", "B1", "--usd", "1", "--note"}, 2, "", "ledger add: --note requires a value\n"},
		{[]string{"add", "coder", "--run", ".r", "--block", "B1", "--usd", "1"}, 2, "", "ledger add: --run must be a run id (letters, digits, \".\", \"_\", \"-\")\n"},
		{[]string{"add", "coder", "--run", "r1", "--block", "B1", "--usd", "0.00001"}, 2, "", "ledger add: --usd must be a number > 0 (at 4 decimals), got \"0.00001\"\n"},
		{[]string{"add", "coder", "--run", "r1", "--block", "B1", "--usd", "1", "--note", strings.Repeat("x", 501)}, 2, "", "ledger add: --note is longer than 500 characters\n"},
		{[]string{"add", "coder", "--run", "r1", "--block", "B1", "--usd", "1"}, 1, "", "ledger add: no run record for r1\n"},
		{[]string{"add", "coder", "--run", "R.1", "--block", "B1", "--usd", "1"}, 1, "", "ledger add: run id must match /^[a-z0-9][a-z0-9-]{0,63}$/\n"},
	})
	ledgerRunFixture(t, home)
	ledgerWrite(t, home, "proj.jsonl", `{"run":"r1","event":"session","role":"coder","usd":1.5}`+"\n")
	ledgerCheck(t, []ledgerCase{
		{[]string{"add", "coder", "--run", "r1", "--block", "B1", "--usd", "1.23456", "--note", "hi"}, 0, "recorded coder spend $1.2346 for run r1 block B1 · run total $2.7346\n", ""},
	})
	rows := ledgerLines(t, home, "proj")
	row := rows[len(rows)-1]
	if row["run"] != "r1" || row["event"] != "session" || row["manual"] != true || row["usd"] != 1.2346 || row["note"] != "hi" || row["cost_source"] != "manual" || row["mac"] == nil {
		t.Errorf("signed session row: %v", row)
	}
	if err := os.Chmod(filepath.Join(home, ".code-forge", "runs", "r1.key"), 0644); err != nil {
		t.Fatal(err)
	}
	ledgerCheck(t, []ledgerCase{
		{[]string{"add", "coder", "--run", "r1", "--block", "B1", "--usd", "2"}, 1, "", "ledger add: run r1 signer key is accessible to other users (mode 644); expected 600\n"},
	})
}

func TestLedgerGoOutsideReview(t *testing.T) {
	home := ledgerHome(t)
	usage := "\n" + ledgerOutsideReviewUsage + "\n"
	ledgerCheck(t, []ledgerCase{
		{[]string{"outside-review", "--foo", "1"}, 2, "", "ledger outside-review: unknown argument at position 1" + usage},
		{[]string{"outside-review", "--run", "--pr"}, 2, "", "ledger outside-review: --run requires a value" + usage},
		{[]string{"outside-review", "--run", "a", "--run", "b"}, 2, "", "ledger outside-review: --run is given more than once" + usage},
		{[]string{"outside-review", "--run", "a", "--pr", "1", "--rounds", "1"}, 2, "", "ledger outside-review: --findings is required" + usage},
		{[]string{"outside-review", "--run", "a", "--pr", "01", "--rounds", "1", "--findings", "0"}, 2, "", "ledger outside-review: --pr must be a positive integer" + usage},
		{[]string{"outside-review", "--run", "a", "--pr", "1", "--rounds", "1", "--findings", "0", "--link", "https://a b"}, 2, "", "ledger outside-review: --link must be an https:// or http:// URL with no whitespace" + usage},
		{[]string{"outside-review", "--run", "r1", "--pr", "1", "--rounds", "1", "--findings", "0"}, 1, "", "ledger outside-review: no run record for r1\n"},
	})
	ledgerRunFixture(t, home)
	key := strings.Repeat("k", 32)
	ledgerCheck(t, []ledgerCase{
		{[]string{"outside-review", "--run", "r1", "--pr", "1", "--rounds", "1", "--findings", "0", "--block", "B9"}, 1, "", "ledger outside-review: block \"B9\" is not a block of run r1; nothing written\n"},
		{[]string{"outside-review", "--run", "r1", "--pr", "12", "--rounds", "2", "--findings", "3", "--link", "https://x/" + key, "--block", "B1", "--block", "B2", "--block", "B1"}, 0, "outside_review pr 12 · rounds 2 · findings 3 · blocks B1, B2\n", ""},
		{[]string{"outside-review", "--run", "r1", "--pr", "13", "--rounds", "0", "--findings", "0"}, 0, "outside_review pr 13 · rounds 0 · findings 0 · blocks -\n", ""},
	})
	rows := ledgerLines(t, home, "proj")
	if len(rows) != 2 || rows[0]["event"] != "outside_review" || rows[0]["link"] != "https://x/"+key || fmt.Sprint(rows[0]["blocks"]) != "[B1 B2]" || rows[1]["link"] != nil || rows[0]["mac"] == nil {
		t.Errorf("outside_review rows: %v", rows)
	}
}

func TestLedgerGoErrorLog(t *testing.T) {
	home := ledgerHome(t)
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "")
	if code, _, _ := ledgerCall(t, "calibration", "--slug", "nope"); code != 1 {
		t.Fatalf("calibration exit %d", code)
	}
	data, err := os.ReadFile(filepath.Join(home, ".code-forge", "logs", "errors.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	entry := map[string]any{}
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("%s: %v", data, err)
	}
	if entry["verb"] != "ledger" || entry["sub"] != "calibration" || entry["exit"] != 1.0 || entry["message"] != "ledger calibration: no ledger for slug \"nope\"; known slugs: none" {
		t.Errorf("error entry: %v", entry)
	}
}

func TestErrorLogNormalizeNodeCases(t *testing.T) {
	for _, tc := range [][2]string{
		{`ledger outside-review: block "B 2" is not a block of run r1; nothing written`, `ledger outside-review: block is not a block of run r1; nothing written`},
		{`no ledger for slug "x"; known slugs: a, b`, `no ledger for slug ; known slugs: a, b`},
		{"it's 'quoted' and `tick` <placeholder> at /tmp/x/y.txt", `it's and at`},
		{`failed 2026-11-11T10:20:30.123Z at 10:20 and 9:05:01.5 sha abcdef1 deadbeef cafe123x`, `failed at and sha deadbeef cafe123x`},
		{"count 12345 and 1234.5x and v1.12345 and 99999.25 abc_12345 tail   spaced\u00a0nbsp", `count and .5x and v1.12345 and abc_12345 tail spaced nbsp`},
		{`exit 1234.56.`, `exit .`},
	} {
		if got := errorLogNormalize(tc[0]); got != tc[1] {
			t.Errorf("errorLogNormalize(%q) = %q, want Node's %q", tc[0], got, tc[1])
		}
	}
}
