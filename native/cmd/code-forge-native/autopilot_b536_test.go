package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/humanask"
)

// B536 fixtures: testdata/b536 holds what Node's approveChange and
// checkApprovals wrote (the run record, .code-forge.yml and the ledger) for
// a run with a config snapshot and an active autopilot grant: three
// approvals (a scalar, a list under an existing mapping, a key whose parent
// is absent) at 10:00, then the restore of each at its until. @WORKSPACE@
// stands for the temp workspace.

var b536Dir, _ = filepath.Abs(filepath.Join("testdata", "b536"))

type b536Fixture struct{ home, ws string }

func b536Data(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(b536Dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// b536Setup is a temp HOME and a git workspace named ws holding the run
// record start.json and the config start.yml; the prompt answers yes.
func b536Setup(t *testing.T) b536Fixture {
	t.Helper()
	base, _ := filepath.EvalSymlinks(t.TempDir())
	f := b536Fixture{home: filepath.Join(base, "home"), ws: filepath.Join(base, "ws")}
	runs := filepath.Join(f.home, ".code-forge", "runs")
	for _, dir := range []string{runs, f.ws} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	git := exec.Command("git", "init", "-q", f.ws)
	git.Env = gitx.ChildEnv()
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	record := strings.ReplaceAll(b536Data(t, "start.json"), "@WORKSPACE@", f.ws)
	if err := os.WriteFile(filepath.Join(runs, "r-fix.json"), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runs, "r-fix.key"), []byte(strings.Repeat("\x07", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.ws, ".code-forge.yml"), []byte(b536Data(t, "start.yml")), 0o644); err != nil {
		t.Fatal(err)
	}
	here, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(here) })
	if err := os.Chdir(f.ws); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", f.home)
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-b536-node")
	t.Setenv("CODE_FORGE_TMP_ROOT", filepath.Join(base, "tmp"))
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	t.Setenv("CODE_FORGE_NATIVE", "1")
	nowBefore, ttyBefore, promptableBefore, confirmBefore, idBefore := autopilotNow, autopilotIsTTY, autopilotPromptable, autopilotConfirm, apdNewApprovalID
	autopilotNow = func() time.Time { return time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC) }
	autopilotIsTTY = func() bool { return true }
	autopilotPromptable = func() bool { return true }
	autopilotConfirm = func(string) (humanask.Result, error) { return humanask.Confirmed, nil }
	ids := []string{"apr-0000aaa1", "apr-0000aaa2", "apr-0000aaa3"}
	apdNewApprovalID = func() (string, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	}
	t.Cleanup(func() {
		autopilotNow, autopilotIsTTY, autopilotPromptable, autopilotConfirm, apdNewApprovalID = nowBefore, ttyBefore, promptableBefore, confirmBefore, idBefore
	})
	return f
}

func (f b536Fixture) at(clock string) {
	when, _ := time.Parse(time.RFC3339Nano, clock)
	autopilotNow = func() time.Time { return when }
}

func (f b536Fixture) config(t *testing.T) string {
	t.Helper()
	data, _ := os.ReadFile(filepath.Join(f.ws, ".code-forge.yml"))
	return string(data)
}

func (f b536Fixture) record(t *testing.T) string {
	t.Helper()
	data, _ := os.ReadFile(filepath.Join(f.home, ".code-forge", "runs", "r-fix.json"))
	return string(data)
}

// check compares the config and the record with Node's after step name.
func (f b536Fixture) check(t *testing.T, name string) {
	t.Helper()
	if got, want := f.config(t), b536Data(t, name+".yml"); got != want {
		t.Fatalf("%s .code-forge.yml:\n%s\nwant (Node):\n%s", name, got, want)
	}
	if got, want := f.record(t), strings.ReplaceAll(b536Data(t, name+".json"), "@WORKSPACE@", f.ws); got != want {
		t.Fatalf("%s run record:\n%s\nwant (Node):\n%s", name, got, want)
	}
}

var b536Stamp = regexp.MustCompile(`"(ts|mac)":"[^"]*",|,"ts":"[^"]*"`)

// ledger is the ledger file without each row's ts and mac (run.reload rows
// are signed at the wall clock).
func b536Ledger(text string) string { return b536Stamp.ReplaceAllString(text, "") }

func (f b536Fixture) noHandover(t *testing.T) {
	t.Helper()
	if data, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "native", "handovers.jsonl")); err == nil {
		t.Fatalf("handed over: %s", data)
	}
}

func b536Approve(t *testing.T, key, value, until string) (int, string, string) {
	t.Helper()
	return autopilotCall(t, "approve", "--run", "r-fix", "--key", key, "--value", value, "--until", until)
}

// B536: approve writes .code-forge.yml (a scalar, a list, a key whose
// parent is absent), reloads the run past the active grant's limit guard
// and stores the approval with its signed row; the restore of each due
// approval on the active run puts the old value back and reloads. Every
// byte of the config, the record and the ledger rows is Node's.
func TestB536ApproveAndRestoreActiveRunMatchNode(t *testing.T) {
	f := b536Setup(t)
	steps := []struct{ name, key, value, until string }{
		{"a1", "review.max_rounds_per_file", "5", "2026-11-11T11:00:00.000Z"},
		{"a2", "review.exclude", `["dist/**","*.lock"]`, "2026-11-11T11:10:00.000Z"},
		{"a3", "autopilot.min_confidence", "0.8", "2026-11-11T11:20:00.000Z"},
	}
	for i, s := range steps {
		code, out, errText := b536Approve(t, s.key, s.value, s.until)
		want := "autopilot approve run r-fix: " + s.key + " changed until " + s.until + " (approval apr-0000aaa" + string(rune('1'+i)) + "); the old value comes back then\n"
		if code != 0 || out != want || errText != "" {
			t.Fatalf("%s = %d %q %q, want 0 %q", s.name, code, out, errText, want)
		}
		f.check(t, s.name)
	}
	for i, clock := range []string{"2026-11-11T11:00:00.000Z", "2026-11-11T11:10:00.000Z", "2026-11-11T11:20:00.000Z"} {
		f.at(clock)
		code, _, errText := autopilotCall(t, "status", "--run", "r-fix")
		want := "autopilot: approval apr-0000aaa" + string(rune('1'+i)) + " expired; the old value is back\n"
		if code != 0 || errText != want {
			t.Fatalf("restore %d = %d %q, want %q", i+1, code, errText, want)
		}
		f.check(t, "r"+string(rune('1'+i)))
	}
	data, _ := os.ReadFile(filepath.Join(f.home, ".code-forge", "ledger", "ws.jsonl"))
	if got, want := b536Ledger(string(data)), b536Ledger(b536Data(t, "ledger.jsonl")); got != want {
		t.Fatalf("ledger:\n%s\nwant (Node):\n%s", got, want)
	}
	f.noHandover(t)
}

var (
	b536Duration = regexp.MustCompile(`"duration_ms":[0-9]+,`)
	b536Decision = regexp.MustCompile(`ad-[0-9a-f]{12}`)
)

// b536Ask runs `autopilot ask` with the fake claude of testdata/b523 the
// only provider CLI on PATH (FAKE_MODE picks its answer) and returns what
// testdata/b536/ask-<case>.txt holds for Node: exit code, stdout, stderr,
// the ledger rows (no ts, mac, duration_ms; decision ids masked) and the
// fake's argv.
func b536Ask(t *testing.T, f b536Fixture, mode string, args ...string) string {
	t.Helper()
	bin := filepath.Join(filepath.Dir(f.home), "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	fake, err := os.ReadFile(filepath.Join(filepath.Dir(b536Dir), "b523", "fake.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if mode == "sleep" {
		fake = []byte("#!/bin/sh\nexec sleep 30\n") // answers nothing: the session times out
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), fake, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(filepath.Dir(f.home), "argv.log")
	if err := os.WriteFile(log, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("FAKE_MODE", mode)
	t.Setenv("FAKE_LOG", log)
	code, out, errText := autopilotCall(t, append([]string{"ask", "--run", "r-fix"}, args...)...)
	rows, _ := os.ReadFile(filepath.Join(f.home, ".code-forge", "ledger", "ws.jsonl"))
	argv, _ := os.ReadFile(log)
	ledgerText := b536Ledger(b536Duration.ReplaceAllString(string(rows), ""))
	text := "code " + strconv.Itoa(code) + "\n--- stdout\n" + out + "--- stderr\n" + errText + "--- ledger\n" + ledgerText + "--- argv\n" + string(argv)
	return b536Decision.ReplaceAllString(text, "ad-<ID>")
}

// B536: an ask the grant lets through runs the delegate session in Go:
// the answer acted on (text and --json), a session that fails, an answer
// that does not match the delegate schema, a decision that is not one of
// the options, and a session the budget refuses (tiny budget.usd: "session
// unavailable (budget)"). Output, exit code, the session/budget and
// autopilot.decision rows and the CLI argv are Node's (testdata/b536/ask-*).
func TestB536AskDelegateSessionMatchesNode(t *testing.T) {
	cases := []struct {
		name, mode string
		budget     bool
		args       []string
	}{
		{"ok", "ok", false, []string{"--scope", "round:extra", "--question", "one more round on a.go?", "--options", "proceed,stop"}},
		{"json", "ok", false, []string{"--scope", "round:extra", "--question", "one more round on a.go?", "--options", "proceed,stop", "--json"}},
		{"fail", "fail", false, []string{"--scope", "round:extra", "--question", "one more round?"}},
		{"bad", "mismatch", false, []string{"--scope", "round:extra", "--question", "one more round?"}},
		{"notopt", "ok", false, []string{"--scope", "round:extra", "--question", "go?", "--options", "stop,wait"}},
		{"budget", "ok", true, []string{"--scope", "round:extra", "--question", "one more round?"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := b536Setup(t)
			if tc.budget {
				// the snapshot holds budget.usd 0.000001 (and its hash)
				record := strings.ReplaceAll(b536Data(t, "ask-budget-start.json"), "@WORKSPACE@", f.ws)
				if err := os.WriteFile(filepath.Join(f.home, ".code-forge", "runs", "r-fix.json"), []byte(record), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got, want := b536Ask(t, f, tc.mode, tc.args...), b536Data(t, "ask-"+tc.name+".txt"); got != want {
				t.Fatalf("got:\n%s\nwant (Node):\n%s", got, want)
			}
			f.noHandover(t)
		})
	}
}

// B536: the delegate session's timeout ends it as Node's spawn does
// (status timeout, "killed after <ms> ms"): the question goes to the owner.
func TestB536AskDelegateTimeout(t *testing.T) {
	f := b536Setup(t)
	before := apdTimeoutMs
	apdTimeoutMs = 300
	t.Cleanup(func() { apdTimeoutMs = before })
	got := b536Ask(t, f, "sleep", "--scope", "round:extra", "--question", "one more round?")
	if !strings.HasPrefix(got, "code 3\n--- stdout\nautopilot ask run r-fix: to the owner (session timeout (killed after 300 ms))\n--- stderr\n") {
		t.Fatalf("got:\n%s", got)
	}
	if !strings.Contains(got, `"status":"timeout","reason":"killed after 300 ms"`) || !strings.Contains(got, `"owner_reason":"session-failed","failure":"session timeout (killed after 300 ms)"`) {
		t.Fatalf("rows:\n%s", got)
	}
	f.noHandover(t)
}

// state is the config, the record and the ledger file (empty when absent).
func (f b536Fixture) state(t *testing.T) string {
	t.Helper()
	rows, _ := os.ReadFile(filepath.Join(f.home, ".code-forge", "ledger", "ws.jsonl"))
	return f.config(t) + "\x00" + f.record(t) + "\x00" + string(rows)
}

func (f b536Fixture) handovers(t *testing.T) []string {
	t.Helper()
	data, _ := os.ReadFile(filepath.Join(f.home, ".code-forge", "native", "handovers.jsonl"))
	reasons := []string{}
	for _, m := range regexp.MustCompile(`"reason":"([^"]*)"`).FindAllStringSubmatch(string(data), -1) {
		reasons = append(reasons, m[1])
	}
	return reasons
}

// B536: approveChange's refusals after the owner's yes, worded as Node's
// approveChange throws them (scratch run of src/autopilot/limits.mjs on this
// fixture): a value the file already holds, a key under a scalar. Nothing
// is written and nothing hands over.
func TestB536ApproveRefusedInGo(t *testing.T) {
	cases := []struct {
		key, value string
		code       int
		msg        string
	}{
		{"review.max_rounds_per_file", "3", 1, "review.max_rounds_per_file already has that value in .code-forge.yml; nothing to approve"},
		{"review.max_rounds_per_file.x", "1", 2, "review.max_rounds_per_file.x cannot be set: a parent of it is not a mapping in .code-forge.yml"},
	}
	for _, tc := range cases {
		f := b536Setup(t)
		before := f.state(t)
		code, out, errText := b536Approve(t, tc.key, tc.value, "2026-11-11T11:00:00.000Z")
		if code != tc.code || out != "" || errText != "autopilot approve: "+tc.msg+"\n" {
			t.Fatalf("%s = %d %q %q", tc.key, code, out, errText)
		}
		if f.state(t) != before {
			t.Fatalf("%s wrote", tc.key)
		}
		f.noHandover(t)
	}
}

// B536: what Go does not reproduce hands over before the prompt with a
// narrow reason and writes nothing: an edited config Node calls invalid
// (".code-forge.yml would be invalid ([schema] /review/max_rounds_per_file)"
// names key paths Go's validator does not carry) and a YAML shape yamledit
// does not write like the yaml package (an anchor).
func TestB536ApproveNarrowHandOvers(t *testing.T) {
	cases := []struct{ name, yml, key, value, reason string }{
		{"invalid", "", "review.max_rounds_per_file", `"abc"`, "autopilot-approve-reload-shape"},
		{"anchor", "# project config\nprovider: anthropic\nreview:\n  max_rounds_per_file: &n 3\n", "review.max_rounds_per_file", "5", "autopilot-approve-yaml-shape"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := b536Setup(t)
			if tc.yml != "" {
				if err := os.WriteFile(filepath.Join(f.ws, ".code-forge.yml"), []byte(tc.yml), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			before := f.state(t)
			asked := false
			autopilotConfirm = func(string) (humanask.Result, error) { asked = true; return humanask.Confirmed, nil }
			code, out, _ := b536Approve(t, tc.key, tc.value, "2026-11-11T11:00:00.000Z")
			if code != 127 || out != "" || asked {
				t.Fatalf("= %d %q asked %v, want a hand-over before the prompt", code, out, asked)
			}
			if f.state(t) != before {
				t.Fatal("wrote before handing over")
			}
			if got := f.handovers(t); len(got) != 1 || got[0] != tc.reason {
				t.Fatalf("reasons = %v, want %s", got, tc.reason)
			}
		})
	}
}

// B536: a due approval on an active run whose revert Go cannot reload the
// way Node does (the owner made another key invalid since) hands over
// autopilot-restore-reload-shape and writes nothing; one whose file Go cannot
// edit (an anchor) hands over autopilot-restore-write.
func TestB536RestoreNarrowHandOvers(t *testing.T) {
	cases := []struct{ name, edit, reason string }{
		{"invalid", "review:\n  max_rounds_per_file: 5\n  exclude: 7\n", "autopilot-restore-reload-shape"},
		{"anchor", "review:\n  max_rounds_per_file: &n 5\n", "autopilot-restore-write"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := b536Setup(t)
			if code, _, errText := b536Approve(t, "review.max_rounds_per_file", "5", "2026-11-11T11:00:00.000Z"); code != 0 {
				t.Fatalf("approve = %d %q", code, errText)
			}
			text := strings.Replace(f.config(t), "review:\n  max_rounds_per_file: 5\n", tc.edit, 1)
			if err := os.WriteFile(filepath.Join(f.ws, ".code-forge.yml"), []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
			before := f.state(t)
			f.at("2026-11-11T11:00:00.000Z")
			code, out, _ := autopilotCall(t, "status", "--run", "r-fix")
			if code != 127 || out != "" {
				t.Fatalf("status = %d %q, want a hand-over", code, out)
			}
			if f.state(t) != before {
				t.Fatal("wrote before handing over")
			}
			if got := f.handovers(t); len(got) != 1 || got[0] != tc.reason {
				t.Fatalf("reasons = %v, want %s", got, tc.reason)
			}
		})
	}
}
