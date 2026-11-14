package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/gitx"
)

// B473 fixtures: testdata/b473 holds the run records and ledger rows that
// Node (CODE_FORGE_NATIVE=0 ./bin/code-forge run reload) wrote for the same
// configs; @WORKSPACE@ and @NOW@ stand for the temp workspace and the clock.

const b473Now = "2026-11-11T09:30:00.123Z"

const b473Usage = "usage: code-forge run start [--cwd <dir>] [--run <id>] [--engine <e>] [--worker-pid <pid>] [--mode plan|brief] | start --add-worker --run <id> | start --reattach --run <id> [--worker-pid <pid>] | status --run <id> | reload --run <id> | end --run <id> [--json] | human-checks list --run <id> [--json] | human-checks clear --run <id> --block <id> --clause <C<n>> --note <text> [--json]\n"

const b473Tail = "queued review tickets (%d) keep the config they were enqueued with; new tickets use the new config\n"

type b473Fixture struct{ home, ws string }

// b473Dir is absolute: the tests chdir into their workspace.
var b473Dir, _ = filepath.Abs(filepath.Join("testdata", "b473"))

func b473Data(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(b473Dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// b473Setup is a temp HOME and a git workspace named ws (the slug the Node
// fixture recorded) holding startCfg's run record, then reloadCfg as the
// config on disk. edit changes the record before it is written.
func b473Setup(t *testing.T, record, reloadCfg string, edit func(map[string]any)) b473Fixture {
	t.Helper()
	base, _ := filepath.EvalSymlinks(t.TempDir())
	f := b473Fixture{home: filepath.Join(base, "home"), ws: filepath.Join(base, "ws")}
	for _, dir := range []string{filepath.Join(f.home, ".code-forge", "runs"), f.ws} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	git := exec.Command("git", "init", "-q", f.ws)
	git.Env = gitx.ChildEnv()
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	text := strings.ReplaceAll(b473Data(t, record), "@WORKSPACE@", f.ws)
	if edit != nil {
		var obj map[string]any
		if err := json.Unmarshal([]byte(text), &obj); err != nil {
			t.Fatal(err)
		}
		edit(obj)
		data, _ := json.MarshalIndent(obj, "", "  ")
		text = string(data) + "\n"
	}
	runs := filepath.Join(f.home, ".code-forge", "runs")
	if err := os.WriteFile(filepath.Join(runs, "r-fix.json"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runs, "r-fix.key"), []byte(strings.Repeat("\x07", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	if reloadCfg != "" {
		if err := os.WriteFile(filepath.Join(f.ws, ".code-forge.yml"), []byte(b473Data(t, reloadCfg)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	here, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(here) })
	if err := os.Chdir(f.ws); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", f.home)
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-b473-node")
	t.Setenv("CODE_FORGE_TMP_ROOT", filepath.Join(base, "tmp"))
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "")
	fixed, _ := time.Parse(time.RFC3339Nano, b473Now)
	old := runReloadNow
	runReloadNow = func() time.Time { return fixed }
	t.Cleanup(func() { runReloadNow = old })
	return f
}

func (f b473Fixture) record(t *testing.T) string {
	t.Helper()
	data, _ := os.ReadFile(filepath.Join(f.home, ".code-forge", "runs", "r-fix.json"))
	return string(data)
}

func (f b473Fixture) want(t *testing.T, name string) string {
	t.Helper()
	text := strings.ReplaceAll(b473Data(t, name), "@WORKSPACE@", f.ws)
	return strings.ReplaceAll(text, "@NOW@", b473Now)
}

// ledgerRows is the ledger file without each row's ts and mac (Node's row is
// signed at its own time); it also checks every row carries a mac.
func (f b473Fixture) ledgerRows(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "ledger", "ws.jsonl"))
	if err != nil {
		return ""
	}
	var out strings.Builder
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		before, rest, ok := strings.Cut(line, `"ts":"`)
		_, after, ok2 := strings.Cut(rest, `","mac":"`)
		mac, tail, ok3 := strings.Cut(after, `",`)
		if !ok || !ok2 || !ok3 || len(mac) != 64 {
			t.Fatalf("ledger row without ts/mac: %s", line)
		}
		out.WriteString(before + tail + "\n")
	}
	return out.String()
}

func b473Expect(t *testing.T, code int, stdout, stderr string, wantCode int, wantOut, wantErr string) {
	t.Helper()
	if code != wantCode || stdout != wantOut || stderr != wantErr {
		t.Fatalf("code=%d stdout=%q stderr=%q\nwant code=%d stdout=%q stderr=%q", code, stdout, stderr, wantCode, wantOut, wantErr)
	}
}

// Group 1: help, usage and flag errors print Node's words with Node's exits.
func TestB473RunUsageAndFlagErrors(t *testing.T) {
	smallProject(t, "")
	reload := "usage: code-forge run reload --run <id>\n"
	checkSmallCases(t, []smallCase{
		{[]string{"run", "--help"}, 0, b473Usage, ""},
		{[]string{"run", "-h"}, 0, b473Usage, ""},
		{[]string{"run", "reload", "--help"}, 0, reload, ""},
		{[]string{"run", "reload", "-h"}, 0, reload, ""},
		{[]string{"run", "status", "--help"}, 0, "usage: code-forge run status --run <id>\n", ""},
		{[]string{"run"}, 2, "", b473Usage},
		{[]string{"run", "bogus"}, 2, "", b473Usage},
		{[]string{"run", "reload"}, 2, "", b473Usage},
		{[]string{"run", "reload", "x"}, 2, "", b473Usage},
		{[]string{"run", "reload", "--cwd=/x"}, 2, "", b473Usage},
		{[]string{"run", "reload", "--run", "x", "--help"}, 2, "", "run reload: unknown flag \"--help\"\n"},
		{[]string{"run", "reload", "--bogus"}, 2, "", "run reload: unknown flag \"--bogus\"\n"},
		{[]string{"run", "reload", "--"}, 2, "", "run reload: unknown flag \"--\"\n"},
		{[]string{"run", "reload", "--run"}, 2, "", "run reload: --run needs a value\n"},
		{[]string{"run", "reload", "--run="}, 2, "", "run reload: --run needs a value\n"},
		{[]string{"run", "reload", "--run", "--cwd"}, 2, "", "run reload: --run needs a value\n"},
		{[]string{"run", "reload", "--run", "a", "--run", "b"}, 2, "", "run reload: --run given more than once\n"},
		{[]string{"run", "reload", "--reattach=1"}, 2, "", "run reload: --reattach takes no value\n"},
		{[]string{"run", "reload", "--mode", "plan", "--run", "a"}, 2, "", "run reload: --mode is set once, by a new run start (the mode is fixed for the run)\n"},
		{[]string{"run", "reload", "--mode", "zz"}, 2, "", "run reload: --mode is set once, by a new run start (the mode is fixed for the run)\n"},
		{[]string{"run", "reload", "--add-worker", "--run", "a"}, 2, "", "run reload: run start --add-worker needs --run and cannot use --reattach or --worker-pid\n"},
		{[]string{"run", "reload", "--worker-pid", "0", "--run", "a"}, 2, "", "run reload: --worker-pid must be a positive integer\n"},
		{[]string{"run", "reload", "--run", "BAD"}, 1, "", "run reload: run id must match /^[a-z0-9][a-z0-9-]{0,63}$/\n"},
		{[]string{"run", "reload", "--run", "nope"}, 1, "", "run reload: no run record for nope\n"},
		{[]string{"run", "reload", "pos", "--run", "nope"}, 1, "", "run reload: no run record for nope\n"},
		{[]string{"run", "reload", "--run", "nope", "--reattach", "--worker-pid", "5", "--cwd", "/x", "--engine", "e"}, 1, "", "run reload: no run record for nope\n"},
	})
}

// Node's errors.jsonl entry for a reload names no sub (reload is not in
// SUBCOMMANDS) and the usage kind for exit 2.
func TestB473RunReloadErrorLogMatchesNode(t *testing.T) {
	home, _ := smallProject(t, "")
	runCaptured(t, "run", "reload", "--bogus")
	data, _ := os.ReadFile(filepath.Join(home, ".code-forge", "logs", "errors.jsonl"))
	for _, want := range []string{`"verb":"run","sub":null,"flags":["--bogus"],"exit":2,"kind":"usage","message":"run reload: unknown flag \"--bogus\""`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("errors.jsonl = %s; want %s", data, want)
		}
	}
	crumbs, _ := os.ReadFile(filepath.Join(home, ".code-forge", "logs", "breadcrumbs.json"))
	if string(crumbs) != "[\"run\"]\n" {
		t.Fatalf("breadcrumbs = %q; want [\"run\"]", crumbs)
	}
}

// Group 2: a reload that changes keys writes Node's record, mirror and row.
func TestB473RunReloadWritesNodeRecordAndRow(t *testing.T) {
	f := b473Setup(t, "start-snapshot.json", "c2.yml", nil)
	code, out, errText := runCaptured(t, "run", "reload", "--run", "r-fix")
	b473Expect(t, code, out, errText, 0, "run r-fix: config reloaded · 2 keys changed\n  review.max_rounds_per_file\n  review.session_timeout_s\n"+strings.Replace(b473Tail, "%d", "0", 1), "")
	if got, want := f.record(t), f.want(t, "want-basic.json"); got != want {
		t.Fatalf("record:\n%s\nwant (Node):\n%s", got, want)
	}
	mirror, _ := os.ReadFile(filepath.Join(f.ws, ".code-forge", "runs", "r-fix.json"))
	if want := f.want(t, "want-basic.mirror.json"); string(mirror) != want {
		t.Fatalf("mirror:\n%s\nwant (Node):\n%s", mirror, want)
	}
	if got, want := f.ledgerRows(t), b473Data(t, "want-basic.row"); got != want {
		t.Fatalf("ledger:\n%s\nwant (Node):\n%s", got, want)
	}
	// the same config again is no change, and nothing more is written
	code, out, errText = runCaptured(t, "run", "reload", "--run", "r-fix")
	b473Expect(t, code, out, errText, 0, "run r-fix: no config change\n", "")
	if got := f.ledgerRows(t); got != b473Data(t, "want-basic.row") {
		t.Fatalf("a no-change reload wrote a row:\n%s", got)
	}
}

func TestB473RunReloadLegacyRecordsSnapshot(t *testing.T) {
	f := b473Setup(t, "start-legacy.json", "c2.yml", nil)
	code, out, errText := runCaptured(t, "run", "reload", "--run", "r-fix")
	b473Expect(t, code, out, errText, 0, "run r-fix: config snapshot recorded (the run predates run reload; no earlier snapshot to compare)\n"+strings.Replace(b473Tail, "%d", "0", 1), "")
	if got, want := f.record(t), f.want(t, "want-legacy.json"); got != want {
		t.Fatalf("record:\n%s\nwant (Node):\n%s", got, want)
	}
	mirror, _ := os.ReadFile(filepath.Join(f.ws, ".code-forge", "runs", "r-fix.json"))
	if want := f.want(t, "want-legacy.mirror.json"); string(mirror) != want {
		t.Fatalf("mirror:\n%s\nwant (Node):\n%s", mirror, want)
	}
	if got, want := f.ledgerRows(t), b473Data(t, "want-legacy.row"); got != want {
		t.Fatalf("ledger:\n%s\nwant (Node):\n%s", got, want)
	}
}

// A frozen key changed together with a review-only recovery keeps the
// frozen value from the snapshot (reviewOnlyReload).
func TestB473RunReloadKeepsFrozenKeys(t *testing.T) {
	f := b473Setup(t, "start-snapshot.json", "c6-kept.yml", nil)
	code, out, errText := runCaptured(t, "run", "reload", "--run", "r-fix")
	b473Expect(t, code, out, errText, 0, "run r-fix: config reloaded · 1 key changed\n  review.session_timeout_s\nkept frozen keys from the run snapshot: engine\n"+strings.Replace(b473Tail, "%d", "0", 1), "")
	if got, want := f.record(t), f.want(t, "want-kept.json"); got != want {
		t.Fatalf("record:\n%s\nwant (Node):\n%s", got, want)
	}
	if got, want := f.ledgerRows(t), b473Data(t, "want-kept.row"); got != want {
		t.Fatalf("ledger:\n%s\nwant (Node):\n%s", got, want)
	}
}

// Pending tickets (no done or claim marker) are pinned to the old snapshot,
// oldest enqueued_at first, then by id.
func TestB473RunReloadPinsPendingTickets(t *testing.T) {
	f := b473Setup(t, "start-snapshot.json", "c2.yml", nil)
	queue := filepath.Join(f.ws, ".code-forge", "queue")
	if err := os.MkdirAll(queue, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"aaaaaaaaaaaaaaaaaaaaaaaa.json":                   `{"enqueued_at":"2026-11-11T02:00:00.000Z"}`,
		"bbbbbbbbbbbbbbbbbbbbbbbb.json":                   `{"enqueued_at":"2026-11-11T01:00:00.000Z"}`,
		"cccccccccccccccccccccccc.json":                   "x",
		"dddddddddddddddddddddddd.json":                   "{}",
		"dddddddddddddddddddddddd.done":                   "",
		"eeeeeeeeeeeeeeeeeeeeeeee.json":                   "{}",
		"eeeeeeeeeeeeeeeeeeeeeeee.done.12.0123abcd.fresh": "",
	} {
		if err := os.WriteFile(filepath.Join(queue, name), []byte(body+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	code, out, errText := runCaptured(t, "run", "reload", "--run", "r-fix")
	b473Expect(t, code, out, errText, 0, "run r-fix: config reloaded · 2 keys changed\n  review.max_rounds_per_file\n  review.session_timeout_s\n"+strings.Replace(b473Tail, "%d", "3", 1), "")
	if got, want := f.record(t), f.want(t, "want-tickets.json"); got != want {
		t.Fatalf("record:\n%s\nwant (Node):\n%s", got, want)
	}
	if got, want := f.ledgerRows(t), b473Data(t, "want-tickets.row"); got != want {
		t.Fatalf("ledger:\n%s\nwant (Node):\n%s", got, want)
	}
}

// A run without its key still saves the record and warns the way Node does.
func TestB473RunReloadRowErrorWarns(t *testing.T) {
	f := b473Setup(t, "start-snapshot.json", "c2.yml", nil)
	if err := os.Remove(filepath.Join(f.home, ".code-forge", "runs", "r-fix.key")); err != nil {
		t.Fatal(err)
	}
	code, out, errText := runCaptured(t, "run", "reload", "--run", "r-fix")
	b473Expect(t, code, out, errText, 0, "run r-fix: config reloaded · 2 keys changed\n  review.max_rounds_per_file\n  review.session_timeout_s\n"+strings.Replace(b473Tail, "%d", "0", 1),
		"run reload: WARN the run.reload ledger row could not be written (no-key); the run record holds the new config\n")
	if got, want := f.record(t), f.want(t, "want-basic.json"); got != want {
		t.Fatalf("record:\n%s\nwant (Node):\n%s", got, want)
	}
}

// Group 3: refusals, each with Node's words and exit 1, and no write.
func TestB473RunReloadRefusals(t *testing.T) {
	cfg := func(extra string) func(t *testing.T, ws string) {
		return func(t *testing.T, ws string) {
			text := b473Data(t, "c1.yml") + extra
			if err := os.WriteFile(filepath.Join(ws, ".code-forge.yml"), []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	grant := func(until string) func(map[string]any) {
		return func(r map[string]any) {
			r["autopilot"] = map[string]any{"grant_id": "g1", "status": "active", "scopes": []any{"block.close"}, "deny": []any{}, "delegate": "L2",
				"until": until, "caps": map[string]any{"usd": 5}, "stop_at": 0.8, "started_at": "2019-11-11T00:00:00.000Z"}
		}
	}
	for _, c := range []struct {
		name, record string
		edit         func(map[string]any)
		write        func(t *testing.T, ws string)
		stderr       string
	}{
		{"frozen engine", "start-snapshot.json", nil, cfg("engine: solo\n"),
			"run reload: engine cannot change mid-run (the run engine is fixed at run start) — put it back, or end the run and start a new one\n"},
		{"two frozen keys", "start-snapshot.json", nil, cfg("engine: solo\ntmp: {root: /tmp/zz}\n"),
			"run reload: engine cannot change mid-run (the run engine is fixed at run start); tmp.root cannot change mid-run (the worker's run temp root is created at run start) — put them back, or end the run and start a new one\n"},
		{"close.require_human", "start-snapshot.json", nil, cfg("close:\n  require_human: true\n"),
			"run reload: close.require_human cannot change mid-run (fixed by the owner before the run starts)\n"},
		{"legacy external reviewer", "start-legacy.json", nil, cfg("hooks:\n  on_block_end:\n    argv: [echo, hi]\n"),
			"run reload: hooks.on_block_end cannot change mid-run (it runs a command on the owner's machine at every block end; fixed by the owner before the run starts) — put it back, or end the run and start a new one\n"},
		{"legacy slug", "start-legacy.json", func(r map[string]any) { r["project"] = "other" }, func(t *testing.T, ws string) { writeB473(t, ws, "c2.yml") },
			"run reload: project.slug cannot change mid-run (the ledger file of the run is chosen at run start) — put it back, or end the run and start a new one\n"},
		{"autopilot limit", "start-snapshot.json", grant("2099-01-01T00:00:00.000Z"), func(t *testing.T, ws string) { cfg("")(t, ws); writeB473(t, ws, "c2.yml") },
			"run reload: autopilot is active: review.max_rounds_per_file is a limit; only the owner can change it with code-forge autopilot approve\n"},
		{"autopilot legacy", "start-legacy.json", grant("2099-01-01T00:00:00.000Z"), func(t *testing.T, ws string) { writeB473(t, ws, "c2.yml") },
			"run reload: autopilot is active: the whole config is a limit; only the owner can change it with code-forge autopilot approve\n"},
		{"ended run", "start-snapshot.json", func(r map[string]any) { r["status"] = "ended" }, func(t *testing.T, ws string) { writeB473(t, ws, "c2.yml") },
			"run reload: run r-fix has ended\n"},
		{"missing snapshot", "start-snapshot.json", func(r map[string]any) {
			r["config"] = map[string]any{"hash": "abc", "snapshots": map[string]any{}, "pins": map[string]any{}}
		},
			func(t *testing.T, ws string) { writeB473(t, ws, "c2.yml") },
			"run reload: run r-fix: the current config snapshot is missing from the run record\n"},
		{"domain error", "start-snapshot.json", nil, func(t *testing.T, ws string) {
			text := strings.Replace(b473Data(t, "c1.yml"), "L2: {model: claude-opus-5-5}", "L2: {model: claude-sonnet-5}", 1)
			_ = os.WriteFile(filepath.Join(ws, ".code-forge.yml"), []byte(text), 0o600)
		}, "run reload: .code-forge.yml is invalid (1 error) — nothing changed; run code-forge validate\n  [duplicate-level-tuple]\n"},
		{"not found", "start-snapshot.json", nil, func(*testing.T, string) {},
			"run reload: no .code-forge.yml in the workspace — nothing changed\n"},
		{"read error", "start-snapshot.json", nil, func(t *testing.T, ws string) { _ = os.Mkdir(filepath.Join(ws, ".code-forge.yml"), 0o700) },
			"run reload: .code-forge.yml could not be read (EISDIR) — nothing changed\n"},
		{"migrate error", "start-snapshot.json", nil, func(t *testing.T, ws string) {
			_ = os.WriteFile(filepath.Join(ws, ".code-forge.yml"), []byte("version: 99\n"), 0o600)
		}, "run reload: .code-forge.yml could not be migrated to the current format — nothing changed\n"},
		{"queue unreadable", "start-snapshot.json", nil, func(t *testing.T, ws string) {
			writeB473(t, ws, "c2.yml")
			_ = os.MkdirAll(filepath.Join(ws, ".code-forge"), 0o700)
			_ = os.WriteFile(filepath.Join(ws, ".code-forge", "queue"), nil, 0o600)
		}, "run reload: cannot read the review queue; nothing changed\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := b473Setup(t, c.record, "", c.edit)
			c.write(t, f.ws)
			before := f.record(t)
			code, out, errText := runCaptured(t, "run", "reload", "--run", "r-fix")
			b473Expect(t, code, out, errText, 1, "", c.stderr)
			if f.record(t) != before || f.ledgerRows(t) != "" {
				t.Fatalf("a refused reload wrote the record or a row")
			}
		})
	}
}

// An expired grant is not a guard (Node's grantState: now >= until).
func TestB473RunReloadExpiredGrantIsNoGuard(t *testing.T) {
	f := b473Setup(t, "start-snapshot.json", "c2.yml", func(r map[string]any) {
		r["autopilot"] = map[string]any{"grant_id": "g1", "status": "active", "scopes": []any{}, "deny": []any{}, "delegate": "L2",
			"until": b473Now, "caps": map[string]any{}, "stop_at": 0.8, "started_at": "2019-11-11T00:00:00.000Z"}
	})
	code, _, errText := runCaptured(t, "run", "reload", "--run", "r-fix")
	if code != 0 || errText != "" || f.ledgerRows(t) != b473Data(t, "want-basic.row") {
		t.Fatalf("code=%d stderr=%q; want the reload to go through", code, errText)
	}
}

func writeB473(t *testing.T, ws, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(ws, ".code-forge.yml"), []byte(b473Data(t, name)), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Group 4: the cases Go does not reproduce keep their named hand-over.
func TestB473RunReloadKeptHandovers(t *testing.T) {
	for _, c := range []struct {
		name, reason string
		edit         func(map[string]any)
		text         string
	}{
		{"yaml syntax error", "run-reload", nil, "a: [\n"},
		{"schema issue", "run-reload-schema-issues", nil, "engine: claude\n"},
		{"explicit yaml tag", "run-reload-yaml-tag", nil, "provider: !!str anthropic\n"},
		{"array grant", "run-reload-autopilot-grant", func(r map[string]any) { r["autopilot"] = []any{} }, ""},
		{"array snapshots", "run-reload-record-shape", func(r map[string]any) {
			r["config"].(map[string]any)["snapshots"] = []any{}
		}, ""},
		{"workspace not a string", "run-shape", func(r map[string]any) { r["workspace"] = 5 }, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := b473Setup(t, "start-snapshot.json", "c2.yml", c.edit)
			if c.text != "" {
				if err := os.WriteFile(filepath.Join(f.ws, ".code-forge.yml"), []byte(c.text), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			expectHandover(t, c.reason, "run", "reload", "--run", "r-fix")
		})
	}
}
