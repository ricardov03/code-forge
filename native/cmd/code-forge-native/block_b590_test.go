package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// b590Routine is the approved one-file close under an active close:routine
// grant (close.require_human set), with a signed green gate row.
func b590Routine(t *testing.T) *closeReviewFixture { return b590RoutineWith(t, true, nil) }

func b590RoutineWith(t *testing.T, green bool, gates ledger.Object) *closeReviewFixture {
	t.Helper()
	f := b587Fixture(t, nil)
	cfg := b587Config()
	if gates != nil {
		cfg.Set("gates", gates)
	}
	cfg.Set("close", ledger.Object{{Key: "require_human", Value: true}})
	b493SetConfig(t, f, cfg)
	record, err := state.ReadRun(f.home, "r-close-fixture")
	if err != nil {
		t.Fatal(err)
	}
	record.Set("autopilot", ledger.Object{{Key: "grant_id", Value: "g-1"}, {Key: "status", Value: "active"}, {Key: "scopes", Value: []any{"close:routine"}}, {Key: "deny", Value: []any{}}, {Key: "delegate", Value: "L2"}, {Key: "until", Value: "2999-01-01T00:00:00.000Z"}, {Key: "caps", Value: ledger.Object{}}, {Key: "stop_at", Value: 0.8}, {Key: "started_at", Value: "2019-01-01T00:00:00.000Z"}})
	if err := state.SaveRun(f.home, record); err != nil {
		t.Fatal(err)
	}
	if green {
		b493Sign(t, f, ledger.Object{{Key: "event", Value: "gate.done"}, {Key: "block", Value: "B1"}, {Key: "all_ok", Value: true}})
	}
	return f
}

func b590Tamper(t *testing.T, f *closeReviewFixture) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(f.home, ".code-forge/ledger/*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("ledger files %v %v", files, err)
	}
	file, err := os.OpenFile(files[0], os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(`{"run":"r-close-fixture","event":"proof","block":"B1","mac":"00"}` + "\n"); err != nil {
		t.Fatal(err)
	}
}

func b590Orphans(t *testing.T, f *closeReviewFixture, files ...any) {
	t.Helper()
	record, err := state.ReadRun(f.home, "r-close-fixture")
	if err != nil {
		t.Fatal(err)
	}
	record.Set("orphans", files)
	if err := state.SaveRun(f.home, record); err != nil {
		t.Fatal(err)
	}
}

func b590LateRows(rows ...ledger.Object) func(*testing.T) *closeReviewFixture {
	return func(t *testing.T) *closeReviewFixture {
		f := newCloseReviewFixture(t, 1)
		b493Approve(t, f)
		b493Sign(t, f, rows...)
		return f
	}
}

func b590Late(file, finding any) ledger.Object {
	return ledger.Object{{Key: "event", Value: "review.late_finding"}, {Key: "block", Value: "B1"}, {Key: "file", Value: file}, {Key: "finding", Value: finding}, {Key: "severity", Value: "warning"}}
}

func b590Cap(file any, open ...any) ledger.Object {
	return ledger.Object{{Key: "event", Value: "review.cap"}, {Key: "block", Value: "B1"}, {Key: "file", Value: file}, {Key: "open", Value: open}}
}

type b590Golden struct {
	name   string
	json   bool
	code   int
	stdout string
	stderr string
}

func b590Check(t *testing.T, build func(*testing.T) *closeReviewFixture, g b590Golden) {
	t.Helper()
	extra := []string{}
	if g.json {
		extra = append(extra, "--json")
	}
	b583Node(t, func() *closeReviewFixture { return build(t) }, extra...)
	f := build(t)
	before := b493LedgerLines(t, f)
	code, out, errs, reason := b493Close(f, extra...)
	out, errs = strings.ReplaceAll(out, f.home, "<HOME>"), strings.ReplaceAll(errs, f.home, "<HOME>")
	if reason != 0 || code != g.code || out != g.stdout || errs != g.stderr {
		t.Fatalf("%s json=%v: got reason=%v exit=%d\nout=%q\nerr=%q\nwant exit=%d\nout=%q\nerr=%q", g.name, g.json, reason, code, out, errs, g.code, g.stdout, g.stderr)
	}
	if code != 0 && b493LedgerLines(t, f) != before {
		t.Fatalf("%s: a refused close wrote ledger rows", g.name)
	}
}

// B590: a close under an active close:routine grant prints Node's
// "opened before" warning once per gate load (the review load, the routine
// check's read-only load and the real pass's two loads) and joins every
// routine failure into one reason, as Node's dry run does. Goldens are
// Node's bytes on the same fixtures (B583_NODE_GOLDEN=1, CODE_FORGE_NATIVE=0).
func TestB590RoutineCloseWarnsPerGateLoad(t *testing.T) {
	builds := map[string]func(*testing.T) *closeReviewFixture{
		"routine pass":      b590Routine,
		"no green gate row": func(t *testing.T) *closeReviewFixture { return b590RoutineWith(t, false, nil) },
		"no green gates run": func(t *testing.T) *closeReviewFixture {
			return b590RoutineWith(t, false, ledger.Object{{Key: "test", Value: "true"}})
		},
		"ledger tamper": func(t *testing.T) *closeReviewFixture { f := b590Routine(t); b590Tamper(t, f); return f },
		"orphans": func(t *testing.T) *closeReviewFixture {
			f := b590Routine(t)
			b590Orphans(t, f, "x.txt", "y.txt")
			return f
		},
	}
	for _, g := range []b590Golden{
		{"routine pass", false, 0, "reviewed file0.txt ticket b82733d3089a96c3991c870e\nblock B1 closed\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nWARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nWARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nWARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\n"},
		{"routine pass", true, 0, "{\"block\":\"B1\",\"event\":\"closed\",\"status\":\"closed\",\"reason\":null,\"close_event\":null,\"reviewed\":[{\"file\":\"file0.txt\",\"ticket\":\"b82733d3089a96c3991c870e\",\"via\":\"approved\"}]}\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nWARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nWARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nWARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\n"},
		{"no green gate row", false, 1, "close-not-routine - no signed green gate row\n  fix: code-forge block close B1 --run r-close-fixture --decide --reason '<reason>' --report <HOME>/report.txt --transcript <HOME>/transcript.txt\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nWARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nclose-not-routine: no signed green gate row\n"},
		{"no green gate row", true, 1, "{\"block\":\"B1\",\"ok\":false,\"reason\":\"no signed green gate row\",\"blockers\":[{\"code\":\"close-not-routine\",\"file\":null,\"detail\":\"no signed green gate row\",\"fix\":\"code-forge block close B1 --run r-close-fixture --decide --reason '<reason>' --report <HOME>/report.txt --transcript <HOME>/transcript.txt\"}]}\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nWARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nclose-not-routine: no signed green gate row\n"},
		{"no green gates run", false, 1, "no_green_gates - no green gates run; run: code-forge gates run --run r-close-fixture --block B1\n  fix: code-forge gates run --run r-close-fixture --block B1\nclose-not-routine - no signed green gate row\n  fix: code-forge block close B1 --run r-close-fixture --decide --reason '<reason>' --report <HOME>/report.txt --transcript <HOME>/transcript.txt\n", "close-not-routine: no green gates run; run: code-forge gates run --run r-close-fixture --block B1; no signed green gate row\n"},
		{"no green gates run", true, 1, "{\"block\":\"B1\",\"ok\":false,\"reason\":\"no green gates run; run: code-forge gates run --run r-close-fixture --block B1; no signed green gate row\",\"blockers\":[{\"code\":\"no_green_gates\",\"file\":null,\"detail\":\"no green gates run; run: code-forge gates run --run r-close-fixture --block B1\",\"fix\":\"code-forge gates run --run r-close-fixture --block B1\"},{\"code\":\"close-not-routine\",\"file\":null,\"detail\":\"no signed green gate row\",\"fix\":\"code-forge block close B1 --run r-close-fixture --decide --reason '<reason>' --report <HOME>/report.txt --transcript <HOME>/transcript.txt\"}]}\n", "close-not-routine: no green gates run; run: code-forge gates run --run r-close-fixture --block B1; no signed green gate row\n"},
		{"ledger tamper", false, 1, "ledger.tamper - proof: mismatch\n  fix: code-forge doctor\n", "close-not-routine: proof: mismatch\n"},
		{"ledger tamper", true, 1, "{\"block\":\"B1\",\"ok\":false,\"reason\":\"proof: mismatch\",\"blockers\":[{\"code\":\"ledger.tamper\",\"file\":null,\"detail\":\"proof: mismatch\",\"fix\":\"code-forge doctor\"}]}\n", "close-not-routine: proof: mismatch\n"},
		{"orphans", false, 1, "unowned_change x.txt orphan in run registry: x.txt\n  fix: code-forge block claim B1 x.txt --run r-close-fixture\nunowned_change y.txt orphan in run registry: y.txt\n  fix: code-forge block claim B1 y.txt --run r-close-fixture\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nWARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nclose-not-routine: orphans: x.txt, y.txt\n"},
		{"orphans", true, 1, "{\"block\":\"B1\",\"ok\":false,\"reason\":\"orphans: x.txt, y.txt\",\"blockers\":[{\"code\":\"unowned_change\",\"file\":\"x.txt\",\"detail\":\"orphan in run registry: x.txt\",\"fix\":\"code-forge block claim B1 x.txt --run r-close-fixture\"},{\"code\":\"unowned_change\",\"file\":\"y.txt\",\"detail\":\"orphan in run registry: y.txt\",\"fix\":\"code-forge block claim B1 y.txt --run r-close-fixture\"}]}\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nWARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nclose-not-routine: orphans: x.txt, y.txt\n"},
	} {
		b590Check(t, builds[g.name], g)
	}
}

// B590: object, array and unquotable late and cap values close in Go as
// Node reads them: an object prints as [object Object], an array joins, and
// a late or cap file that is not text and fails Node's word test is Node's
// own thrown error building the fix line (no rows written).
func TestB590CloseLateAndCapValuesAsNode(t *testing.T) {
	obj := ledger.Object{{Key: "a", Value: 1}}
	builds := map[string]func(*testing.T) *closeReviewFixture{
		"late file object":          b590LateRows(b590Late(obj, "L1")),
		"late file array":           b590LateRows(b590Late([]any{"a", 1}, "L1")),
		"late file empty array":     b590LateRows(b590Late([]any{}, "L1")),
		"late finding object":       b590LateRows(b590Late("file0.txt", obj)),
		"late finding array":        b590LateRows(b590Late("file0.txt", []any{"x"})),
		"cap file object":           b590LateRows(b590Cap(obj, "F1")),
		"cap open object and array": b590LateRows(b590Cap("file0.txt", obj, []any{"y"})),
		"late ruling file object":   b590LateRows(ledger.Object{{Key: "event", Value: "review.late_ruling"}, {Key: "block", Value: "B1"}, {Key: "file", Value: obj}, {Key: "finding", Value: "L1"}, {Key: "ruling", Value: "waive"}}),
	}
	for _, g := range []b590Golden{
		{"late file object", false, 1, "", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nblock close: arg.replaceAll is not a function\n"},
		{"late file object", true, 1, "", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nblock close: arg.replaceAll is not a function\n"},
		{"late file array", false, 1, "", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nblock close: arg.replaceAll is not a function\n"},
		{"late file array", true, 1, "", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nblock close: arg.replaceAll is not a function\n"},
		{"late file empty array", false, 1, "", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nblock close: arg.replaceAll is not a function\n"},
		{"late file empty array", true, 1, "", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nblock close: arg.replaceAll is not a function\n"},
		{"late finding object", false, 1, "late_unruled file0.txt late finding [object Object] in file0.txt has no ruling\n  fix: code-forge block reopen B1 --run r-close-fixture --file file0.txt --reason '<reason>'\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nblock B1 open: late_unruled file0.txt [object Object]\n"},
		{"late finding object", true, 1, "{\"block\":\"B1\",\"ok\":false,\"reason\":\"late_unruled file0.txt [object Object]\",\"blockers\":[{\"code\":\"late_unruled\",\"file\":\"file0.txt\",\"detail\":\"late finding [object Object] in file0.txt has no ruling\",\"fix\":\"code-forge block reopen B1 --run r-close-fixture --file file0.txt --reason '<reason>'\"}]}\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nblock B1 open: late_unruled file0.txt [object Object]\n"},
		{"late finding array", false, 1, "late_unruled file0.txt late finding x in file0.txt has no ruling\n  fix: code-forge block reopen B1 --run r-close-fixture --file file0.txt --reason '<reason>'\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nblock B1 open: late_unruled file0.txt x\n"},
		{"late finding array", true, 1, "{\"block\":\"B1\",\"ok\":false,\"reason\":\"late_unruled file0.txt x\",\"blockers\":[{\"code\":\"late_unruled\",\"file\":\"file0.txt\",\"detail\":\"late finding x in file0.txt has no ruling\",\"fix\":\"code-forge block reopen B1 --run r-close-fixture --file file0.txt --reason '<reason>'\"}]}\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nblock B1 open: late_unruled file0.txt x\n"},
		{"cap file object", false, 1, "", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nblock close: arg.replaceAll is not a function\n"},
		{"cap file object", true, 1, "", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nblock close: arg.replaceAll is not a function\n"},
		{"cap open object and array", false, 1, "not_approved file0.txt a later review of file0.txt's current content did not approve it (review.cap) and names no finding; review it again\n  fix: code-forge block reopen B1 --run r-close-fixture --file file0.txt --reason '<reason>'\nreview_cap file0.txt file0.txt stopped at review_cap; finding [object Object] is open and not waived; exits: block reopen, block waive, review-file --answers, re-decompose\n  fix: code-forge block reopen B1 --run r-close-fixture --file file0.txt --reason '<reason>'\nreview_cap file0.txt file0.txt stopped at review_cap; finding y is open and not waived; exits: block reopen, block waive, review-file --answers, re-decompose\n  fix: code-forge block reopen B1 --run r-close-fixture --file file0.txt --reason '<reason>'\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nblock B1 open: not_approved file0.txt; review_cap file0.txt [object Object]; review_cap file0.txt y\n"},
		{"cap open object and array", true, 1, "{\"block\":\"B1\",\"ok\":false,\"reason\":\"not_approved file0.txt; review_cap file0.txt [object Object]; review_cap file0.txt y\",\"blockers\":[{\"code\":\"not_approved\",\"file\":\"file0.txt\",\"detail\":\"a later review of file0.txt's current content did not approve it (review.cap) and names no finding; review it again\",\"fix\":\"code-forge block reopen B1 --run r-close-fixture --file file0.txt --reason '<reason>'\"},{\"code\":\"review_cap\",\"file\":\"file0.txt\",\"detail\":\"file0.txt stopped at review_cap; finding [object Object] is open and not waived; exits: block reopen, block waive, review-file --answers, re-decompose\",\"fix\":\"code-forge block reopen B1 --run r-close-fixture --file file0.txt --reason '<reason>'\"},{\"code\":\"review_cap\",\"file\":\"file0.txt\",\"detail\":\"file0.txt stopped at review_cap; finding y is open and not waived; exits: block reopen, block waive, review-file --answers, re-decompose\",\"fix\":\"code-forge block reopen B1 --run r-close-fixture --file file0.txt --reason '<reason>'\"}]}\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nblock B1 open: not_approved file0.txt; review_cap file0.txt [object Object]; review_cap file0.txt y\n"},
		{"late ruling file object", false, 0, "reviewed file0.txt ticket b82733d3089a96c3991c870e\nblock B1 closed\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\n"},
		{"late ruling file object", true, 0, "{\"block\":\"B1\",\"event\":\"closed\",\"status\":\"closed\",\"reason\":null,\"close_event\":null,\"reviewed\":[{\"file\":\"file0.txt\",\"ticket\":\"b82733d3089a96c3991c870e\",\"via\":\"approved\"}]}\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\n"},
	} {
		b590Check(t, builds[g.name], g)
	}
}

// B590: the command's flag path prints Node's thrown error once (not prefixed again)
// and exits 1.
func TestB590CloseFlagsPathThrowsOnce(t *testing.T) {
	f := b590LateRows(b590Late(ledger.Object{{Key: "a", Value: 1}}, "L1"))(t)
	var out, errs bytes.Buffer
	var reason handoverReason
	code := closeFlagsStep([]string{"B1", "--run", "r-close-fixture", "--report", f.report, "--transcript", f.transcript}, &landStep{stdout: &out, stderr: &errs}, &reason)
	if reason != 0 || code != 1 || out.String() != "" || errs.String() != b493OpenedWarn+"block close: arg.replaceAll is not a function\n" {
		t.Fatalf("reason=%v exit=%d out=%q err=%q", reason, code, out.String(), errs.String())
	}
}

// b590TranscriptCases are transcripts with JavaScript-only whitespace (NBSP,
// U+2028, U+3000, BOM, \v) and U+0085, which JavaScript does not treat as
// whitespace.
func b590TranscriptCases(codex func(string) string) []string {
	return []string{
		"git\u00a0push --force origin main\n",
		"rm\u2028-rf build\n",
		"\ufeffgit reset --hard HEAD\n",
		"git stash\u0085\n",
		"git\u0085stash\n",
		"bash -c 'git\u00a0push --force'\n",
		"cat ~/.code-forge/runs\u00a0x\n",
		"git\u3000clean -fd\n",
		"gh\u00a0pr merge 1\n",
		"echo ok\u00a0\n",
		"sh -c 'git status'\u00a0\n",
		codex("git\u00a0push --force"),
		codex("git\u2029reset --hard"),
		codex("echo\u00a0fine"),
		"python3 - <<'PY'\nimport os\u00a0; os.system('git push --force')\nPY\n",
	}
}

func b590Codex(cmd string) string {
	return `{"type":"thread.started","thread_id":"t1"}` + "\n" + `{"type":"item.completed","item":{"type":"command_execution","command":"` + cmd + `"}}` + "\n"
}

// B590: plain-text and Codex transcripts with JavaScript-only whitespace
// close in Go (they handed over before): tokens and path rules split on
// JavaScript's \s, and U+0085 is not whitespace. Goldens are Node's bytes
// on the same fixtures (B583_NODE_GOLDEN=1 logs Node's run).
func TestB590TranscriptJavaScriptWhitespace(t *testing.T) {
	want := []struct {
		code           int
		stdout, stderr string
	}{
		{1, "rule_break - transcript line 1: git-push-force\n  fix: code-forge block close B1 --run r-close-fixture --decide --reason '<reason>' --report <HOME>/report.txt --transcript <HOME>/transcript.txt\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nblock B1 open: rule_break transcript line 1: git-push-force\n"},
		{1, "rule_break - transcript line 1: rm-rf\n  fix: code-forge block close B1 --run r-close-fixture --decide --reason '<reason>' --report <HOME>/report.txt --transcript <HOME>/transcript.txt\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nblock B1 open: rule_break transcript line 1: rm-rf\n"},
		{1, "rule_break - transcript line 1: git-reset-hard\n  fix: code-forge block close B1 --run r-close-fixture --decide --reason '<reason>' --report <HOME>/report.txt --transcript <HOME>/transcript.txt\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nblock B1 open: rule_break transcript line 1: git-reset-hard\n"},
		{0, "reviewed file0.txt ticket b82733d3089a96c3991c870e\nblock B1 closed\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\n"},
		{0, "reviewed file0.txt ticket b82733d3089a96c3991c870e\nblock B1 closed\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\n"},
		{1, "rule_break - transcript line 1: git-push-force\n  fix: code-forge block close B1 --run r-close-fixture --decide --reason '<reason>' --report <HOME>/report.txt --transcript <HOME>/transcript.txt\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nblock B1 open: rule_break transcript line 1: git-push-force\n"},
		{1, "rule_break - transcript line 1: code-forge-runs-access\n  fix: code-forge block close B1 --run r-close-fixture --decide --reason '<reason>' --report <HOME>/report.txt --transcript <HOME>/transcript.txt\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nblock B1 open: rule_break transcript line 1: code-forge-runs-access\n"},
		{1, "rule_break - transcript line 1: git-clean\n  fix: code-forge block close B1 --run r-close-fixture --decide --reason '<reason>' --report <HOME>/report.txt --transcript <HOME>/transcript.txt\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nblock B1 open: rule_break transcript line 1: git-clean\n"},
		{1, "rule_break - transcript line 1: gh-pr-merge\n  fix: code-forge block close B1 --run r-close-fixture --decide --reason '<reason>' --report <HOME>/report.txt --transcript <HOME>/transcript.txt\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nblock B1 open: rule_break transcript line 1: gh-pr-merge\n"},
		{0, "reviewed file0.txt ticket b82733d3089a96c3991c870e\nblock B1 closed\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\n"},
		{0, "reviewed file0.txt ticket b82733d3089a96c3991c870e\nblock B1 closed\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\n"},
		{1, "rule_break - transcript line 2: git-push-force\n  fix: code-forge block close B1 --run r-close-fixture --decide --reason '<reason>' --report <HOME>/report.txt --transcript <HOME>/transcript.txt\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nblock B1 open: rule_break transcript line 2: git-push-force\n"},
		{1, "rule_break - transcript line 2: git-reset-hard\n  fix: code-forge block close B1 --run r-close-fixture --decide --reason '<reason>' --report <HOME>/report.txt --transcript <HOME>/transcript.txt\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nblock B1 open: rule_break transcript line 2: git-reset-hard\n"},
		{0, "reviewed file0.txt ticket b82733d3089a96c3991c870e\nblock B1 closed\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\n"},
		{1, "rule_break - transcript line 2: git-push-force\n  fix: code-forge block close B1 --run r-close-fixture --decide --reason '<reason>' --report <HOME>/report.txt --transcript <HOME>/transcript.txt\n", "WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nblock B1 open: rule_break transcript line 2: git-push-force\n"},
	}
	cases := b590TranscriptCases(b590Codex)
	if len(cases) != len(want) {
		t.Fatalf("%d cases, %d goldens", len(cases), len(want))
	}
	for i, text := range cases {
		b583Node(t, func() *closeReviewFixture { return b583Transcript(t, []byte(text)) })
		f := b583Transcript(t, []byte(text))
		code, out, errs, reason := b493Close(f)
		out, errs = strings.ReplaceAll(out, f.home, "<HOME>"), strings.ReplaceAll(errs, f.home, "<HOME>")
		if reason != 0 || code != want[i].code || out != want[i].stdout || errs != want[i].stderr {
			t.Fatalf("%q: reason=%v exit=%d\nout=%q\nerr=%q\nwant exit=%d\nout=%q\nerr=%q", text, reason, code, out, errs, want[i].code, want[i].stdout, want[i].stderr)
		}
	}
}
