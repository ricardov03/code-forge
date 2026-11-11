package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B451: block open, claim, stop, close and land serve help, flags, errors and
// light limits in Go and never refuse a case Node completes; the cases B466
// ports still hand over to Node.

type b451Result struct {
	code           int
	stdout, stderr string
	home           string
}

// b451Run runs one block verb in this process with a fresh HOME, Node set to
// a path that cannot start, and the error log on.
func b451Run(t *testing.T, home string, args ...string) b451Result {
	t.Helper()
	return b451RunWith(t, home, false, args...)
}

// b451RunWith is b451Run; with handover true the case is one B451 keeps on
// Node (B466 ports it), so a hand-over row is expected instead of refused.
func b451RunWith(t *testing.T, home string, handover bool, args ...string) b451Result {
	t.Helper()
	if home == "" {
		home = t.TempDir()
	}
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NODE", "/nonexistent")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "0")
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	t.Setenv("CODE_FORGE_NATIVE", "1")
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	outC, errC := make(chan string), make(chan string)
	go func() { data, _ := io.ReadAll(outR); outC <- string(data) }()
	go func() { data, _ := io.ReadAll(errR); errC <- string(data) }()
	code := runBlock(args)
	os.Stdout, os.Stderr = stdout, stderr
	_ = outW.Close()
	_ = errW.Close()
	res := b451Result{code: code, stdout: <-outC, stderr: <-errC, home: home}
	if handover {
		return res
	}
	if _, err := os.Stat(filepath.Join(home, ".code-forge", "native", "handovers.jsonl")); err == nil {
		t.Errorf("%v: wrote a hand-over row; Node must never be asked", args)
	}
	if strings.Contains(res.stderr, "/nonexistent") || strings.Contains(strings.ToLower(res.stderr), "node") {
		t.Errorf("%v: stderr names Node: %q", args, res.stderr)
	}
	return res
}

// b451ErrorRows reads the error log rows the run wrote.
func b451ErrorRows(t *testing.T, home string) []map[string]any {
	t.Helper()
	file, err := os.Open(filepath.Join(home, ".code-forge", "logs", "errors.jsonl"))
	if err != nil {
		return nil
	}
	defer file.Close()
	var rows []map[string]any
	scan := bufio.NewScanner(file)
	for scan.Scan() {
		row := map[string]any{}
		if json.Unmarshal(scan.Bytes(), &row) != nil {
			t.Fatalf("error log row is not JSON: %q", scan.Text())
		}
		rows = append(rows, row)
	}
	return rows
}

func b451WriteRun(t *testing.T, home, run string, record map[string]any) {
	t.Helper()
	dir := filepath.Join(home, ".code-forge", "runs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(record)
	if err := os.WriteFile(filepath.Join(dir, run+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, run+".key"), []byte(strings.Repeat("k", 32)), 0600); err != nil {
		t.Fatal(err)
	}
}

// close-help with a value: Node's parser refuses --help=x as an unknown flag.
func TestB451CloseHelpValueIsUsage(t *testing.T) {
	res := b451Run(t, "", "close", "B1", "--run", "r-x", "--help=x")
	if res.code != 2 || !strings.HasPrefix(res.stderr, "block close: unknown flag \"--help=x\"") || res.stdout != "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q; want exit 2 and the unknown flag usage line", res.code, res.stdout, res.stderr)
	}
}

// open-help and land-help: Node's verb usage, byte for byte, no state.
func TestB451OpenAndLandHelpAreNative(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		parts []string
	}{
		{[]string{"open", "--help"}, []string{
			"usage: code-forge block open <id> --run <r> [--level L<n>] --owned <paths…> --acceptance <file> [--brief <file>]",
			"  --acceptance <file>  JSON/YAML list [{clause, tests}]; tests is a nonempty list of test ids.\n                       Human checks use {clause, kind: human_check} without tests.\n",
			"  --allow-api-mismatch  Open anyway; print each api-fit: line as a note (native only).\n",
		}},
		{[]string{"open", "B1", "-h"}, []string{"usage: code-forge block open "}},
		{[]string{"land", "B1", "--help"}, []string{
			"usage: code-forge block land <id> --run <r> --report <file> --transcript <file> [--max <dur>] [--merge] [--no-merge-gate] [--push] [--watch-ci] [--copy-links] [--json] [--help|-h]\n",
			"  Join block reviews, run changed gates in its worktree, then close.\n",
			"  block land <id> <id>... --run <r> lands closed blocks in order with one merge gate (--merge implied).\n",
		}},
	} {
		res := b451Run(t, "", tc.args...)
		if res.code != 0 || res.stderr != "" {
			t.Errorf("%v: exit=%d stderr=%q; want usage exit 0", tc.args, res.code, res.stderr)
		}
		for _, part := range tc.parts {
			if !strings.Contains(res.stdout, part) {
				t.Errorf("%v: stdout %q lacks %q", tc.args, res.stdout, part)
			}
		}
		if _, err := os.Stat(filepath.Join(res.home, ".code-forge")); err == nil {
			t.Errorf("%v: help wrote under ~/.code-forge", tc.args)
		}
	}
}

// Unknown flags are Node's shared parser usage error (exit 2), not a hand-over.
func TestB451UnknownFlagsAreUsage(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"open", "B1", "--run", "r-x", "--bogus"}, "block open: unknown flag \"--bogus\"\n"},
		{[]string{"claim", "B1", "f", "--run", "r-x", "--auto"}, "block claim: unknown flag \"--auto\"\n"},
		{[]string{"stop", "B1", "--run", "r-x", "--auto-claim"}, "block stop: unknown flag \"--auto-claim\"\n"},
	} {
		res := b451Run(t, "", tc.args...)
		if res.code != 2 || res.stderr != tc.want {
			t.Errorf("%v: exit=%d stderr=%q; want exit 2 and %q", tc.args, res.code, res.stderr, tc.want)
		}
	}
}

// open-error-log, claim-stop-error-log: errors print in Go and land in the
// error log as Node's router writes them (verb block, its sub, the exit code).
func TestB451ErrorsAreLoggedNatively(t *testing.T) {
	for _, tc := range []struct {
		args       []string
		sub        string
		code       int
		wantPrefix string
	}{
		{[]string{"claim", "B1", "f", "--run", "r-missing"}, "claim", 1, "block claim: "},
		{[]string{"stop", "B1", "--run", "r-missing", "--reason", "done"}, "stop", 1, "block stop: "},
		{[]string{"open", "B1", "--run", "r-missing", "--level", "L0", "--owned", "a.go", "--acceptance", "missing-acceptance.json"}, "open", 1, "block open: "},
	} {
		res := b451Run(t, "", tc.args...)
		if res.code != tc.code || !strings.HasPrefix(res.stderr, tc.wantPrefix) || strings.Count(res.stderr, "\n") != 1 {
			t.Errorf("%v: exit=%d stderr=%q; want exit %d and one %q line", tc.args, res.code, res.stderr, tc.code, tc.wantPrefix)
			continue
		}
		rows := b451ErrorRows(t, res.home)
		if len(rows) != 1 {
			t.Errorf("%v: error log rows = %d, want 1", tc.args, len(rows))
			continue
		}
		row := rows[0]
		if row["verb"] != "block" || row["sub"] != tc.sub || row["exit"] != float64(tc.code) {
			t.Errorf("%v: error log row %v; want verb block, sub %s, exit %d", tc.args, row, tc.sub, tc.code)
		}
	}
}

// open-acceptance-read: a missing --acceptance file is Node's errno message.
func TestB451OpenAcceptanceReadError(t *testing.T) {
	home := t.TempDir()
	b451WriteRun(t, home, "r-open", map[string]any{"run_id": "r-open", "project": "unit", "workspace": t.TempDir(), "status": "active", "blocks": map[string]any{}})
	res := b451Run(t, home, "open", "B1", "--run", "r-open", "--level", "L0", "--owned", "a.go", "--acceptance", "missing-acceptance.json")
	if res.code != 1 || !strings.Contains(res.stderr, "no such file or directory") || !strings.Contains(res.stderr, "missing-acceptance.json") {
		t.Fatalf("exit=%d stderr=%q; want exit 1 naming the missing file", res.code, res.stderr)
	}
}

// stop-daemon (fix1): Node stops a block with a daemon socket, so Go keeps
// the hand-over until B466 ports it; no refusal, and Go writes nothing first.
func TestB451StopDaemonHandoverKept(t *testing.T) {
	home := t.TempDir()
	record := map[string]any{"run_id": "r-stop", "project": "unit", "workspace": t.TempDir(), "status": "active", "orphans": []string{}, "blocks": map[string]any{"B1": map[string]any{"status": "open", "owned_files": []string{}}}}
	b451WriteRun(t, home, "r-stop", record)
	sock := filepath.Join(home, ".code-forge", "runs", "r-stop")
	if err := os.MkdirAll(sock, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sock, "native.sock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	runFile := filepath.Join(home, ".code-forge", "runs", "r-stop.json")
	before, _ := os.ReadFile(runFile)
	res := b451RunWith(t, home, true, "stop", "B1", "--run", "r-stop", "--reason", "done")
	if res.code != 127 || strings.Contains(res.stderr, "refused") {
		t.Fatalf("exit=%d stdout=%q stderr=%q; want the Node hand-over (exit 127 with no Node), not a refusal", res.code, res.stdout, res.stderr)
	}
	data, err := os.ReadFile(filepath.Join(home, ".code-forge", "native", "handovers.jsonl"))
	if err != nil || !strings.Contains(string(data), `"stop-daemon"`) {
		t.Fatalf("hand-over log %q %v; want a stop-daemon row", data, err)
	}
	if after, _ := os.ReadFile(runFile); string(after) != string(before) {
		t.Fatal("Go changed the run file before handing over")
	}
}

// land-read-run (fix1): Go no longer refuses a run file it cannot read; the
// case keeps its Node hand-over (exact Node error text) until B466.
func TestB451LandReadRunHandoverKept(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".code-forge", "runs")
	if err := os.MkdirAll(filepath.Join(dir, "r-land.json"), 0700); err != nil {
		t.Fatal(err)
	}
	res := b451RunWith(t, home, true, "land", "B1", "--run", "r-land", "--report", "r.md", "--transcript", "t.log")
	if res.code != 127 || strings.Contains(res.stderr, "refused") {
		t.Fatalf("exit=%d stderr=%q; want the Node hand-over (exit 127 with no Node), not a refusal", res.code, res.stderr)
	}
	data, err := os.ReadFile(filepath.Join(home, ".code-forge", "native", "handovers.jsonl"))
	if err != nil || !strings.Contains(string(data), `"land-read-run"`) {
		t.Fatalf("hand-over log %q %v; want a land-read-run row", data, err)
	}
}

// close light blocks: Go measures light limits itself (Node's numbers) and
// returns the light_limits blocker past them instead of handing over.
func TestB451LightLimits(t *testing.T) {
	top := t.TempDir()
	for _, argv := range [][]string{{"init", "-q"}, {"config", "user.email", "u@example.com"}, {"config", "user.name", "u"}, {"commit", "-q", "--allow-empty", "-m", "base"}} {
		if out, err := exec.Command("git", append([]string{"-C", top}, argv...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", argv, err, out)
		}
	}
	base, err := exec.Command("git", "-C", top, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	var owned []any
	var changed []string
	for i := 0; i < 6; i++ {
		name := fmt.Sprintf("f%d.go", i)
		if err := os.WriteFile(filepath.Join(top, name), []byte("a\nb\n"), 0600); err != nil {
			t.Fatal(err)
		}
		owned = append(owned, name)
		changed = append(changed, name)
	}
	entry := ledger.Object{{Key: "owned_files", Value: owned}}
	sha := strings.TrimSpace(string(base))
	blockers, ok := lightLimits(top, sha, "", entry, changed, map[string]any{})
	if !ok || len(blockers) != 1 || blockers[0].Detail != "6 files, 12 lines exceed light block limits" {
		t.Fatalf("6 files: ok=%v blockers=%+v; want one light_limits blocker for 6 files, 12 lines", ok, blockers)
	}
	blockers, ok = lightLimits(top, sha, "", entry, changed[:2], map[string]any{})
	if !ok || len(blockers) != 0 {
		t.Fatalf("2 files: ok=%v blockers=%+v; want within limits", ok, blockers)
	}
	cfg := map[string]any{"light": map[string]any{"max_files": float64(1)}}
	if blockers, ok = lightLimits(top, sha, "", entry, changed[:2], cfg); !ok || len(blockers) != 1 {
		t.Fatalf("light.max_files 1: ok=%v blockers=%+v; want one blocker", ok, blockers)
	}
	if _, ok = lightLimits(top, sha, "", ledger.Object{{Key: "owned_files", Value: []any{".env"}}}, []string{".env"}, map[string]any{}); ok {
		t.Fatal("a secret-like path must be refused (ok=false)")
	}
}

// fix1 close-gates-format: Node's checks read only the closing block's rows,
// so a row of another block whose shape Go once handed over (a late finding
// that names no file or finding, a cap with no file) no longer sends the close
// to Node; the close completes in Go.
func TestB451CloseOtherBlockRowsStayNative(t *testing.T) {
	for _, row := range []ledger.Object{
		{{Key: "event", Value: "review.late_finding"}, {Key: "block", Value: "B2"}},
		{{Key: "event", Value: "review.cap"}, {Key: "block", Value: "B2"}, {Key: "open", Value: []any{1}}},
		{{Key: "event", Value: "rule_break"}, {Key: "block", Value: "B2"}, {Key: "id", Value: 3}},
	} {
		t.Run(fmt.Sprint(row.Get("event")), func(t *testing.T) {
			f := newCloseReviewFixture(t, 1)
			if _, err := state.WriteSigned(f.home, "r-close-fixture", row); err != nil {
				t.Fatal(err)
			}
			done := f.reviewer("approved")
			code, out, errs := f.close()
			if err := <-done; err != nil {
				t.Error(err)
			}
			if code != 0 || !strings.Contains(out, "block B1 closed") || strings.Contains(errs, "close-gates-format") {
				t.Fatalf("exit=%d out=%s err=%s; want the close to finish in Go", code, out, errs)
			}
		})
	}
}

// fix1 rule_break ids: a rule_break row whose id or rule is not text is a
// rule_break blocker with Node's String(r.id ?? r.rule ?? 'unnamed') detail,
// not a hand-over.
func TestB451CloseRuleBreakIdsAreNative(t *testing.T) {
	for _, tc := range []struct {
		row  ledger.Object
		want string
	}{
		{ledger.Object{{Key: "id", Value: 7}}, "rule_break row: 7"},
		{ledger.Object{{Key: "id", Value: []any{"a", nil, 2}}}, "rule_break row: a,,2"},
		{ledger.Object{{Key: "id", Value: ledger.Object{{Key: "x", Value: 1}}}}, "rule_break row: [object Object]"},
		{ledger.Object{{Key: "id", Value: nil}, {Key: "rule", Value: true}}, "rule_break row: true"},
		{ledger.Object{{Key: "rule", Value: 1.5}}, "rule_break row: 1.5"},
		{ledger.Object{}, "rule_break row: unnamed"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			f := newCloseReviewFixture(t, 1)
			row := append(ledger.Object{{Key: "event", Value: "rule_break"}, {Key: "block", Value: "B1"}}, tc.row...)
			if _, err := state.WriteSigned(f.home, "r-close-fixture", row); err != nil {
				t.Fatal(err)
			}
			done := f.reviewer("approved")
			code, out, errs := f.close()
			if err := <-done; err != nil {
				t.Error(err)
			}
			if code != 1 || !strings.Contains(out, tc.want+"\n") || strings.Contains(out, "block B1 closed") || strings.Contains(errs, "close-gates-format") {
				t.Fatalf("exit=%d out=%s err=%s; want the %q blocker from Go", code, out, errs, tc.want)
			}
			record, err := state.ReadRun(f.home, "r-close-fixture")
			if err != nil || blockObject(blockObject(record.Get("blocks")).Get("B1")).Get("status") != "open" {
				t.Fatalf("refused block must remain open: %v %v", record, err)
			}
		})
	}
}
