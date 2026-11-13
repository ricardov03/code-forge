package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/worker"
)

// B616: a worker drain runs expiryChecks (the grant's lazy expiry, then
// checkApprovals and its restore) in Go. testdata/b616/<case> holds the
// input run record and .code-forge.yml, and what Node's worker --once
// wrote for them (recorded with CODE_FORGE_NATIVE=0 bin/code-forge on the
// same fixture): exit code, stdout, stderr, the ledger, the run record and
// the config. @WORKSPACE@ and @BASE@ stand for the temp paths.

var b616Dir, _ = filepath.Abs(filepath.Join("testdata", "b616"))

func b616Read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

var (
	b616Stamp = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z`)
	b616Mac   = regexp.MustCompile(`"mac":"[0-9a-f]{64}"`)
)

// b616Mask hides what the check time decides (a stamp not in the input)
// and the row MACs, which sign that stamp.
func b616Mask(text, input string) string {
	text = b616Stamp.ReplaceAllStringFunc(text, func(s string) string {
		if strings.Contains(input, s) {
			return s
		}
		return "<now>"
	})
	return b616Mac.ReplaceAllString(text, `"mac":"<mac>"`)
}

// b616Setup writes the case's fixture under a temp base and stubs the
// daemon, the orphan sweep and the signals; it returns the base.
func b616Setup(t *testing.T, c string) string {
	t.Helper()
	base, _ := filepath.EvalSymlinks(t.TempDir())
	home, ws := filepath.Join(base, "home"), filepath.Join(base, "ws")
	runs := filepath.Join(home, ".code-forge", "runs")
	for _, dir := range []string{runs, ws, filepath.Join(base, "tmp")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	git := exec.Command("git", "init", "-q", ws)
	git.Env = gitx.ChildEnv()
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	record := strings.ReplaceAll(b616Read(t, filepath.Join(b616Dir, c, "in.json")), "@WORKSPACE@", ws)
	b460Write(t, filepath.Join(runs, "r-fix.json"), record)
	if err := os.WriteFile(filepath.Join(runs, "r-fix.key"), []byte(strings.Repeat("\x07", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".code-forge.yml"), []byte(b616Read(t, filepath.Join(b616Dir, c, "in.yml"))), 0o644); err != nil {
		t.Fatal(err)
	}
	b460Chdir(t, ws)
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "file")
	t.Setenv("CODE_FORGE_KEY_JEV", "")
	t.Setenv("CODE_FORGE_TMP_ROOT", filepath.Join(base, "tmp"))
	t.Setenv("CODE_FORGE_NODE", filepath.Join(base, "missing-node"))
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	t.Setenv("CODE_FORGE_NATIVE", "1")
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	t.Setenv("PATH", "/usr/bin:/bin")
	oldDaemon, oldOrphans := workerStartDaemon, workerOrphans
	workerStartDaemon = func(string, map[string]any) (func(), error) { return func() {}, nil }
	workerOrphans = func(string) func() { return func() {} }
	t.Cleanup(func() { workerStartDaemon, workerOrphans = oldDaemon, oldOrphans })
	return base
}

// Every case runs in Go with no hand-over and writes what Node wrote: the
// grant's until in each form Node reads (Z with and without milliseconds,
// an offset, minutes only, a date, a UTC string, null, a future time), a
// hand-edited grant, a stopped grant, three due approvals restored with a
// reload each, an approval not due yet, one changed by hand, and a null
// entry (Node's TypeError warning), and a due approval restored with its
// reload. The "approvals" case (three due reloads in one pass) stays with
// Node: see TestB616WorkerManyReloadsHandOver.
func TestB616WorkerAutopilotMatchesNode(t *testing.T) {
	entries, err := os.ReadDir(b616Dir)
	if err != nil || len(entries) < 14 {
		t.Fatalf("cases %d %v", len(entries), err)
	}
	for _, e := range entries {
		c := e.Name()
		if c == "approvals" {
			continue
		}
		t.Run(c, func(t *testing.T) {
			base := b616Setup(t, c)
			dir := filepath.Join(b616Dir, c)
			input := b616Read(t, filepath.Join(dir, "in.json"))
			code, out, errText := b460Call(t, "worker", "--run", "r-fix", "--once")
			if rows := b460Handovers(t, filepath.Join(base, "home")); len(rows) != 0 {
				t.Fatalf("hand-overs %v (stderr %q)", rows, errText)
			}
			unbase := func(s string) string { return strings.ReplaceAll(s, base, "@BASE@") }
			if got, want := strconv.Itoa(code), strings.TrimSpace(b616Read(t, filepath.Join(dir, "node.exit"))); got != want {
				t.Errorf("exit %s, Node %s", got, want)
			}
			if want := b616Read(t, filepath.Join(dir, "node.stdout")); unbase(out) != want {
				t.Errorf("stdout %q, Node %q", out, want)
			}
			if want := b616Read(t, filepath.Join(dir, "node.stderr")); unbase(errText) != want {
				t.Errorf("stderr %q, Node %q", errText, want)
			}
			ledgerText := ""
			if files, _ := filepath.Glob(filepath.Join(base, "home", ".code-forge", "ledger", "*.jsonl")); len(files) > 0 {
				for _, f := range files {
					ledgerText += b616Read(t, f)
				}
			}
			if got, want := b616Mask(unbase(ledgerText), input), b616Mask(b616Read(t, filepath.Join(dir, "node.ledger.jsonl")), input); got != want {
				t.Errorf("ledger\n%s\nNode\n%s", got, want)
			}
			record := b616Read(t, filepath.Join(base, "home", ".code-forge", "runs", "r-fix.json"))
			if got, want := b616Mask(unbase(record), input), b616Mask(b616Read(t, filepath.Join(dir, "node.record.json")), input); got != want {
				t.Errorf("record\n%s\nNode\n%s", got, want)
			}
			if got, want := b616Read(t, filepath.Join(base, "ws", ".code-forge.yml")), b616Read(t, filepath.Join(dir, "node.yml")); got != want {
				t.Errorf("config\n%s\nNode\n%s", got, want)
			}
		})
	}
}

// Three approvals due at once each reload the run: the restore Go runs
// (B536) serves one reload per pass, so the worker hands the drain to Node
// before it writes anything (the grant expiry included).
func TestB616WorkerManyReloadsHandOver(t *testing.T) {
	base := b616Setup(t, "approvals")
	runFile := filepath.Join(base, "home", ".code-forge", "runs", "r-fix.json")
	recordBefore := b616Read(t, runFile)
	ymlBefore := b616Read(t, filepath.Join(base, "ws", ".code-forge.yml"))
	code, out, _ := b460Call(t, "worker", "--run", "r-fix", "--once")
	if code != 127 || out != "" {
		t.Fatalf("worker = %d %q, want a hand-over", code, out)
	}
	if rows := b460Handovers(t, filepath.Join(base, "home")); len(rows) != 1 || rows[0] != "worker worker-ticket-unported" {
		t.Fatalf("hand-overs %v", rows)
	}
	if b616Read(t, runFile) != recordBefore || b616Read(t, filepath.Join(base, "ws", ".code-forge.yml")) != ymlBefore {
		t.Fatal("wrote before handing over")
	}
	if files, _ := filepath.Glob(filepath.Join(base, "home", ".code-forge", "ledger", "*.jsonl")); len(files) != 0 {
		t.Fatalf("ledger written: %v", files)
	}
}

// With a fake reviewer: tickets are reviewed in Go while the run has an
// expired grant and a due approval (changed by hand); the drain writes the
// expire and restore_skipped rows first and nothing goes to Node.
func TestB616WorkerReviewsUnderAutopilot(t *testing.T) {
	f := newB489Fix(t)
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	base := b460Git(t, f.root, "rev-parse", "HEAD")
	b460Write(t, filepath.Join(f.root, ".code-forge.yml"), "version: 1\nreview:\n  max_rounds_per_file: 3\n")
	cfg := `{"levels":{"L2":{"model":"claude-x","provider":"anthropic"}},"native":{"daemon":false},"version":1}`
	h := b489Sha(cfg)
	f.record(t, "r1", "active", `,"blocks":{"B1":{"base_sha":"`+base+`","owned_files":["a.txt"]}}`+
		`,"config":{"hash":"`+h+`","snapshots":{"`+h+`":`+cfg+`}}`+
		`,"autopilot":{"grant_id":"ap-1","status":"active","until":"2026-11-11T12:30:00+02:00","scopes":[],"deny":[]}`+
		`,"autopilot_approvals":[{"approval_id":"apr-1","key":"review.max_rounds_per_file","new":9,"old":3,"old_absent":false,"prune":0,"until":"2026-11-11T11:00:00.000Z","status":"active","ended_at":null}]`)
	id := f.ticket(t, "B1", "a.txt", "hello\nworld\n")
	code, _, errText := b460Call(t, "worker", "--run", "r1", "--once")
	if code != 0 || !strings.HasSuffix(errText, "worker: drained 1 ticket(s)\n") {
		t.Fatalf("worker = %d %q", code, errText)
	}
	if rows := b460Handovers(t, f.home); len(rows) != 0 {
		t.Fatalf("hand-overs %v", rows)
	}
	if !b489Exists(filepath.Join(worker.QueueDir(f.root), id+".done")) || f.results(t, id) != 1 {
		t.Fatal("ticket not reviewed once in Go")
	}
	ledgerText, _ := os.ReadFile(filepath.Join(f.home, ".code-forge", "ledger", "unit.jsonl"))
	text := string(ledgerText)
	expire := strings.Index(text, `"event":"autopilot.expire","grant_id":"ap-1","until":"2026-11-11T12:30:00+02:00"`)
	skipped := strings.Index(text, `"event":"autopilot.restore_skipped","reason":"changed-by-hand"`)
	result := strings.Index(text, `"event":"review.result"`)
	if expire < 0 || skipped < expire || result < skipped {
		t.Fatalf("ledger order expire %d skipped %d result %d\n%s", expire, skipped, result, text)
	}
	rec := f.read(t, "r1")
	if g := rec["autopilot"].(map[string]any); g["status"] != "expired" {
		t.Fatalf("grant %v", g)
	}
	if a := rec["autopilot_approvals"].([]any)[0].(map[string]any); a["status"] != "restore_skipped" {
		t.Fatalf("approval %v", a)
	}
}
