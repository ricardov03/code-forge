package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// decideFixture is one open block B1 with a real worktree change to
// sample.mjs, its signed review rows, a coder transcript, and (with grant)
// an active autopilot grant on the run. It returns HOME, the run id and the
// transcript path. HOME is set for the test.
type decideFixture struct {
	home, run, transcript string
}

func newDecideFixture(t *testing.T, transcript string, grant bool, review func(f decideFixture, hash string)) decideFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NATIVE", "1")
	t.Setenv("CODE_FORGE_NODE", "/nonexistent")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "0")
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, gitx.ChildEnv()
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(repo, "init", "-qb", "main")
	git(repo, "config", "user.name", "Fixture")
	git(repo, "config", "user.email", "f@example.invalid")
	git(repo, "config", "commit.gpgsign", "false")
	must(os.WriteFile(filepath.Join(repo, "sample.mjs"), []byte("old\n"), 0600))
	must(os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".code-forge/\n"), 0600))
	cfg, err := json.Marshal(landTestConfig(nil))
	must(err)
	must(os.WriteFile(filepath.Join(repo, ".code-forge.yml"), cfg, 0600))
	git(repo, "add", ".")
	git(repo, "commit", "-qm", "base")
	base := git(repo, "rev-parse", "HEAD")
	tree := filepath.Join(repo, ".code-forge/worktrees/B1")
	git(repo, "worktree", "add", "-qb", "block-B1", tree, base)
	must(os.WriteFile(filepath.Join(tree, "sample.mjs"), []byte("new\n"), 0600))
	git(tree, "add", ".")
	git(tree, "commit", "-qm", "change")
	run := "r-decide-unit"
	entry := ledger.Object{{Key: "status", Value: "open"}, {Key: "owned_files", Value: []any{"sample.mjs"}}, {Key: "base_sha", Value: base}, {Key: "base_branch", Value: "main"}, {Key: "tree_at_open", Value: ledger.Object{}}, {Key: "kind", Value: "code"}, {Key: "test_files", Value: []any{}}, {Key: "worktree", Value: ledger.Object{{Key: "path", Value: tree}, {Key: "branch", Value: "block-B1"}, {Key: "base_sha", Value: base}}}}
	record := ledger.Object{{Key: "run_id", Value: run}, {Key: "project", Value: "fixture"}, {Key: "workspace", Value: repo}, {Key: "status", Value: "active"}, {Key: "worker", Value: ledger.Object{{Key: "pid", Value: float64(os.Getpid())}, {Key: "started_at", Value: statusStart(float64(os.Getpid()))}}}, {Key: "blocks", Value: ledger.Object{{Key: "B1", Value: entry}}}, {Key: "orphans", Value: []any{}}}
	stamp := func(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
	now := time.Now()
	if grant {
		record.Set("autopilot", ledger.Object{{Key: "grant_id", Value: "ag-unit"}, {Key: "status", Value: "active"}, {Key: "scopes", Value: []any{"waive:warning", "waive:nit"}}, {Key: "deny", Value: []any{}}, {Key: "delegate", Value: "L2"}, {Key: "until", Value: stamp(now.Add(time.Hour))}, {Key: "caps", Value: ledger.Object{}}, {Key: "stop_at", Value: 0.9}, {Key: "started_at", Value: stamp(now.Add(-time.Minute))}})
	}
	must(state.SaveRun(home, record))
	must(os.WriteFile(filepath.Join(home, ".code-forge/runs", run+".key"), make([]byte, 32), 0600))
	f := decideFixture{home: home, run: run, transcript: filepath.Join(home, "transcript")}
	if grant {
		_, err = state.WriteSigned(home, run, ledger.Object{{Key: "event", Value: "autopilot.grant"}, {Key: "grant_id", Value: "ag-unit"}, {Key: "scopes", Value: []any{"waive:warning", "waive:nit"}}, {Key: "deny", Value: []any{}}, {Key: "delegate", Value: "L2"}, {Key: "until", Value: stamp(now.Add(time.Hour))}, {Key: "ts", Value: stamp(now.Add(-time.Minute))}})
		must(err)
	}
	_, err = state.WriteSigned(home, run, ledger.Object{{Key: "event", Value: "dispatch"}, {Key: "block", Value: "B1"}, {Key: "level", Value: "L2"}})
	must(err)
	hash, err := gitx.ContentHash(tree, "sample.mjs")
	must(err)
	review(f, hash)
	must(os.WriteFile(f.transcript, []byte(transcript), 0600))
	return f
}

func approvedReview(f decideFixture, hash string) {
	if _, err := state.WriteSigned(f.home, f.run, ledger.Object{{Key: "event", Value: "review.approved"}, {Key: "block", Value: "B1"}, {Key: "file", Value: "sample.mjs"}, {Key: "content_hash", Value: hash}}); err != nil {
		panic(err)
	}
}

func findingReview(severity string) func(f decideFixture, hash string) {
	return func(f decideFixture, hash string) {
		// An approval, then a later review of the same content that leaves F1 open.
		approvedReview(f, hash)
		open := []any{ledger.Object{{Key: "id", Value: "F1"}, {Key: "severity", Value: severity}, {Key: "claim", Value: "the name is unclear"}}}
		if _, err := state.WriteSigned(f.home, f.run, ledger.Object{{Key: "event", Value: "review.result"}, {Key: "block", Value: "B1"}, {Key: "file", Value: "sample.mjs"}, {Key: "content_hash", Value: hash}, {Key: "status", Value: "done"}, {Key: "approved", Value: false}, {Key: "open", Value: open}}); err != nil {
			panic(err)
		}
	}
}

// close runs `block close B1 --run R --decide --reason <reason>` the way the
// verb does (non-TTY under go test) and fails on any handover to Node.
func (f decideFixture) close(t *testing.T, extra ...string) (int, string, string) {
	t.Helper()
	var stdout bytes.Buffer
	stderr := &closeErrorBuffer{}
	reason := hCloseAutoClaim
	args := append([]string{"B1", "--run", f.run, "--transcript", f.transcript}, extra...)
	code := closeFlagsStep(args, &landStep{stdout: &stdout, stderr: stderr}, &reason)
	if code == landHandover {
		t.Fatalf("close handed over to Node (%v); want a native answer; stderr %s", reason, stderr.String())
	}
	return code, stdout.String(), stderr.String()
}

func (f decideFixture) rows(t *testing.T, event string) []map[string]any {
	t.Helper()
	all, err := ledger.ReadAllRows(filepath.Join(f.home, ".code-forge/ledger"), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	out := []map[string]any{}
	for _, v := range all {
		if r := v.(map[string]any); event == "" || r["event"] == event {
			out = append(out, r)
		}
	}
	return out
}

func (f decideFixture) status(t *testing.T) any {
	t.Helper()
	record, err := state.ReadRun(f.home, f.run)
	if err != nil {
		t.Fatal(err)
	}
	return blockObject(blockObject(record.Get("blocks")).Get("B1")).Get("status")
}

const stashTranscript = "Completed implementation.\n$ git stash\nall green\n"

// B390: an active grant lets a non-TTY --decide clear a false transcript hit;
// the decision is one signed close.decision row with actor autopilot.
func TestB390AutopilotDecideClearsRuleBreak(t *testing.T) {
	f := newDecideFixture(t, stashTranscript, true, approvedReview)
	code, stdout, stderr := f.close(t, "--decide", "--reason", "the stash was a read-only list in a scratch tree")
	if code != 0 {
		t.Fatalf("exit=%d, want 0; stdout %q stderr %q", code, stdout, stderr)
	}
	if got := f.status(t); got != "closed" {
		t.Fatalf("block status=%v, want closed", got)
	}
	decided := f.rows(t, "close.decision")
	if len(decided) != 1 {
		t.Fatalf("close.decision rows=%d, want 1: %v", len(decided), decided)
	}
	d := decided[0]
	if d["block"] != "B1" || d["actor"] != "autopilot" || d["grant_id"] != "ag-unit" || d["reason"] != "the stash was a read-only list in a scratch tree" {
		t.Fatalf("close.decision row=%v, want block B1, actor autopilot, grant ag-unit and the reason", d)
	}
	if _, ok := d["ts"].(string); !ok {
		t.Fatalf("close.decision row has no ts: %v", d)
	}
	hits, _ := d["rule_breaks"].([]any)
	if len(hits) != 1 || hits[0].(map[string]any)["rule"] != "git-stash" || hits[0].(map[string]any)["line"] != float64(2) {
		t.Fatalf("rule_breaks=%v, want exactly git-stash at line 2", d["rule_breaks"])
	}
	if findings, _ := d["findings"].([]any); len(findings) != 0 {
		t.Fatalf("findings=%v, want none", findings)
	}
	accepted := f.rows(t, "rule_break.accepted")
	if len(accepted) != 1 || accepted[0]["by"] != "autopilot" || accepted[0]["rule"] != "git-stash" {
		t.Fatalf("rule_break.accepted rows=%v, want one git-stash row by autopilot", accepted)
	}
	if !strings.Contains(stdout, "accepted rule_break git-stash transcript line 2: the stash was a read-only list in a scratch tree (by: autopilot)") {
		t.Fatalf("stdout %q does not name the autopilot acceptance", stdout)
	}
}

// B390: a warning (major) finding is waived by autopilot; a critical that is
// not a rule_break stays with the owner and nothing is recorded.
func TestB390AutopilotDecideFindings(t *testing.T) {
	t.Run("warning", func(t *testing.T) {
		f := newDecideFixture(t, "Completed implementation.\n", true, findingReview("warning"))
		code, stdout, stderr := f.close(t, "--decide", "--reason", "naming only; follow-up block B999")
		if code != 0 {
			t.Fatalf("exit=%d, want 0; stdout %q stderr %q", code, stdout, stderr)
		}
		if got := f.status(t); got != "closed" {
			t.Fatalf("block status=%v, want closed", got)
		}
		decided := f.rows(t, "close.decision")
		if len(decided) != 1 {
			t.Fatalf("close.decision rows=%d, want 1", len(decided))
		}
		findings, _ := decided[0]["findings"].([]any)
		if len(findings) != 1 {
			t.Fatalf("findings=%v, want exactly F1", decided[0]["findings"])
		}
		one := findings[0].(map[string]any)
		if one["file"] != "sample.mjs" || one["finding"] != "F1" || one["severity"] != "warning" || decided[0]["actor"] != "autopilot" {
			t.Fatalf("finding=%v row=%v, want sample.mjs F1 warning by autopilot", one, decided[0])
		}
		waived := f.rows(t, "review.waived")
		if len(waived) != 1 || waived[0]["by"] != "autopilot" || waived[0]["finding"] != "F1" || waived[0]["decision_id"] != one["decision_id"] {
			t.Fatalf("review.waived rows=%v, want one F1 waiver by autopilot naming decision %v", waived, one["decision_id"])
		}
	})
	t.Run("critical", func(t *testing.T) {
		f := newDecideFixture(t, "Completed implementation.\n", true, findingReview("critical"))
		before := len(f.rows(t, ""))
		code, stdout, stderr := f.close(t, "--decide", "--reason", "looks fine")
		if code != 1 {
			t.Fatalf("exit=%d, want 1; stdout %q stderr %q", code, stdout, stderr)
		}
		if !strings.Contains(stderr, "F1 in sample.mjs is critical; only the owner decides it") {
			t.Fatalf("stderr %q does not say the critical stays with the owner", stderr)
		}
		if after := len(f.rows(t, "")); after != before {
			t.Fatalf("ledger rows %d -> %d, want nothing recorded", before, after)
		}
		if got := f.status(t); got != "open" {
			t.Fatalf("block status=%v, want open", got)
		}
	})
}

// B390: without a grant a non-TTY --decide is refused as before.
func TestB390DecideWithoutGrantNeedsTerminal(t *testing.T) {
	f := newDecideFixture(t, stashTranscript, false, approvedReview)
	before := len(f.rows(t, ""))
	code, _, stderr := f.close(t, "--decide", "--reason", "false hit")
	if code != 2 || !strings.Contains(stderr, "block close needs the human at a terminal to confirm; nothing was recorded") {
		t.Fatalf("exit=%d stderr %q, want 2 and the terminal refusal", code, stderr)
	}
	if after := len(f.rows(t, "")); after != before {
		t.Fatalf("ledger rows %d -> %d, want nothing recorded", before, after)
	}
	if got := f.status(t); got != "open" {
		t.Fatalf("block status=%v, want open", got)
	}
}

// B390: decisions list prints the autopilot decision rows of a run.
func TestB390DecisionsList(t *testing.T) {
	f := newDecideFixture(t, stashTranscript, true, approvedReview)
	if code, _, stderr := f.close(t, "--decide", "--reason", "false hit"); code != 0 {
		t.Fatalf("close exit=%d: %s", code, stderr)
	}
	var out, errs bytes.Buffer
	if code := decisionsMain([]string{"list", "--run", f.run}, &out, &errs); code != 0 {
		t.Fatalf("decisions list exit=%d: %s", code, errs.String())
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("decisions list printed %d lines, want 1: %q", len(lines), out.String())
	}
	for _, part := range []string{f.run, "B1", "autopilot", "rule_break git-stash line 2", "reason: false hit"} {
		if !strings.Contains(lines[0], part) {
			t.Fatalf("line %q lacks %q", lines[0], part)
		}
	}
	out.Reset()
	if code := decisionsMain([]string{"list", "--run", f.run, "--json"}, &out, &errs); code != 0 {
		t.Fatalf("decisions list --json exit=%d: %s", code, errs.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("decisions list --json is not a JSON array: %v %q", err, out.String())
	}
	if len(rows) != 1 || rows[0]["block"] != "B1" || rows[0]["actor"] != "autopilot" || rows[0]["reason"] != "false hit" || rows[0]["verified"] != true || rows[0]["run"] != f.run {
		t.Fatalf("decisions list --json=%v, want one verified B1 row by autopilot", rows)
	}
	// Without --run it lists every run's rows; another run has none.
	out.Reset()
	if code := decisionsMain([]string{"list", "--json"}, &out, &errs); code != 0 {
		t.Fatalf("decisions list (all runs) exit=%d: %s", code, errs.String())
	}
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("decisions list (all runs)=%q, want one row", out.String())
	}
}
