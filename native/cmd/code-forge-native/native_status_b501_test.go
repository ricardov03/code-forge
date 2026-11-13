package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// b501NativeStatusFixture: a HOME with the ledger of project "p" and a working
// folder whose .code-forge.yml sets a $5 budget. Node is not reachable, so a
// hand-over fails the test. The expected lines are what Node's native-status
// printed for the same files and input.
func b501NativeStatusFixture(t *testing.T, logging bool) (home string) {
	t.Helper()
	root := t.TempDir()
	home, ws := filepath.Join(root, "home"), filepath.Join(root, "ws")
	b452Write(t, filepath.Join(home, ".code-forge", "ledger", "p.jsonl"),
		`{"run":"r","event":"session","usd":0.5}`+"\n"+
			`{"run":"r","block":"B1","event":"review.result","file":"a.js","approved":false,"open":[{"id":"F1","severity":"warning"}]}`+"\n")
	b452Write(t, filepath.Join(ws, ".code-forge.yml"), "version: 1\nbudget:\n  usd: 5\n")
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-b501-node")
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "0")
	if logging {
		t.Setenv("CODE_FORGE_NO_ERROR_LOG", "0")
	} else {
		t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	}
	for _, name := range []string{"CLAUDECODE", "CLAUDE_CODE", "CURSOR_AGENT", "AI_AGENT"} {
		t.Setenv(name, "")
	}
	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(ws); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(before) })
	return home
}

// b501Stdin points os.Stdin at a file holding data for the test.
func b501Stdin(t *testing.T, data string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdin
	os.Stdin = file
	t.Cleanup(func() { os.Stdin = saved; _ = file.Close() })
}

const b501RouterLine = "code-forge: verb \"native-status\" has no default export function\n"

var b501NativeStatusCases = []struct{ in, want string }{
	{`{"record":{"run_id":"r1","status":"active","blocks":{"B1":{"status":"open","level":"L2"},"B2":{"status":"closed"},"B3":{"status":"open","attempt":2}}},"rows":[{"run":"r1","block":"B1","event":"gate.red","gate":"tests","reason":"timeout"},{"run":"r1","event":"gate.done","block":"B1","flaky":["a"]},{"run":"r1","block":"B3","event":"session","role":"coder","provider":"openai","model":"gpt","tokens_in":1000,"tokens_out":1000,"level":"L1"}]}`,
		`{"runs":[{"run_id":"r1","status":"active","mode":"plan","spent_usd":0.005,"unknown_usd_sessions":0,"budget_usd":5,"blocks":[{"id":"B1","state":"open","level":"L2","model":null,"gate":{"result":"fail","name":"tests"},"findings":0},{"id":"B3","state":"attempt-2","level":null,"model":"openai/gpt","gate":{"result":"none","name":null},"findings":0}],"more":0,"closed":1,"stopped":0}]}`},
	{`nope`,
		`{"ok":false,"error":"status-unreadable"}`},
	{``,
		`{"ok":false,"error":"status-unreadable"}`},
	{`null`,
		`{"ok":false,"error":"status-unreadable"}`},
	{`5`,
		`{"ok":false,"error":"status-unreadable"}`},
	{`"s"`,
		`{"ok":false,"error":"status-unreadable"}`},
	{`[]`,
		`{"ok":false,"error":"status-unreadable"}`},
	{`{"record":null,"rows":[]}`,
		`{"ok":false,"error":"status-unreadable"}`},
	{`{"record":5,"rows":[]}`,
		`{"runs":[{"run_id":"undefined","status":null,"mode":"plan","spent_usd":0,"unknown_usd_sessions":0,"budget_usd":5,"blocks":[],"more":0,"closed":0,"stopped":0}]}`},
	{`{"record":{},"rows":"ab"}`,
		`{"ok":false,"error":"status-unreadable"}`},
	{`{"record":{},"rows":{}}`,
		`{"ok":false,"error":"status-unreadable"}`},
	{`{"record":[1],"rows":null}`,
		`{"runs":[{"run_id":"undefined","status":null,"mode":"plan","spent_usd":null,"unknown_usd_sessions":null,"budget_usd":5,"blocks":[],"more":0,"closed":0,"stopped":0}]}`},
	{`{"record":{},"rows":""}`,
		`{"ok":false,"error":"status-unreadable"}`},
	{`{"record":{},"rows":true}`,
		`{"ok":false,"error":"status-unreadable"}`},
	{`{"record":{"project":"p"}}`,
		`{"runs":[{"run_id":"undefined","status":null,"mode":"plan","spent_usd":0,"unknown_usd_sessions":0,"budget_usd":5,"blocks":[],"more":0,"closed":0,"stopped":0}]}`},
	{`{"record":"abc","rows":[]}`,
		`{"runs":[{"run_id":"undefined","status":null,"mode":"plan","spent_usd":0,"unknown_usd_sessions":0,"budget_usd":5,"blocks":[],"more":0,"closed":0,"stopped":0}]}`},
	{`{"record":true,"rows":[]}`,
		`{"runs":[{"run_id":"undefined","status":null,"mode":"plan","spent_usd":0,"unknown_usd_sessions":0,"budget_usd":5,"blocks":[],"more":0,"closed":0,"stopped":0}]}`},
	{`{"record":{},"rows":[null]}`,
		`{"ok":false,"error":"status-unreadable"}`},
	{`{"record":{},"rows":[5,"x",[1]]}`,
		`{"runs":[{"run_id":"undefined","status":null,"mode":"plan","spent_usd":0,"unknown_usd_sessions":0,"budget_usd":5,"blocks":[],"more":0,"closed":0,"stopped":0}]}`},
	{`{"record":{"run_id":"r"},"rows":[{"run":"r","event":"session","usd":1e308},{"run":"r","event":"session","usd":1e308}]}`,
		`{"runs":[{"run_id":"r","status":null,"mode":"plan","spent_usd":null,"unknown_usd_sessions":0,"budget_usd":5,"blocks":[],"more":0,"closed":0,"stopped":0}]}`},
	{`{"rows":[]}`,
		`{"ok":false,"error":"status-unreadable"}`},
	{`{"record":{"blocks":"ab"},"rows":[]}`,
		`{"runs":[{"run_id":"undefined","status":null,"mode":"plan","spent_usd":0,"unknown_usd_sessions":0,"budget_usd":5,"blocks":[],"more":0,"closed":0,"stopped":0}]}`},
	{`{"record":5}`,
		`{"runs":[{"run_id":"undefined","status":null,"mode":"plan","spent_usd":null,"unknown_usd_sessions":null,"budget_usd":5,"blocks":[],"more":0,"closed":0,"stopped":0}]}`},
	{`{"record":{}}`,
		`{"runs":[{"run_id":"undefined","status":null,"mode":"plan","spent_usd":null,"unknown_usd_sessions":null,"budget_usd":5,"blocks":[],"more":0,"closed":0,"stopped":0}]}`},
	{`{"record":{"project":"BAD SLUG"}}`,
		`{"runs":[{"run_id":"undefined","status":null,"mode":"plan","spent_usd":null,"unknown_usd_sessions":null,"budget_usd":5,"blocks":[],"more":0,"closed":0,"stopped":0}]}`},
	{`{"record":{"run_id":"r","project":"p","workspace":"/nonexistent"}}`,
		`{"runs":[{"run_id":"r","status":null,"mode":"plan","spent_usd":0.5,"unknown_usd_sessions":0,"budget_usd":null,"blocks":[],"more":0,"closed":0,"stopped":0}]}`},
	{`{"record":{"run_id":"r","project":"p","blocks":{"B1":{"status":"awaiting_human_check"},"B10":{},"B2":null}}}`,
		`{"ok":false,"error":"status-unreadable"}`},
	{`{"record":{"run_id":"r","project":"p","blocks":{"B1":{"status":"awaiting_human_check"},"B10":{},"a":{"status":"stopped"}}}}`,
		`{"runs":[{"run_id":"r","status":null,"mode":"plan","spent_usd":0.5,"unknown_usd_sessions":0,"budget_usd":5,"blocks":[{"id":"B1","state":"awaiting_human_check","level":null,"model":null,"gate":{"result":"none","name":null},"findings":1},{"id":"B10","state":"open","level":null,"model":null,"gate":{"result":"none","name":null},"findings":0}],"more":0,"closed":0,"stopped":1,"awaiting_human_check":["B1"]}]}`},
	{`  {"record":{}}  `,
		`{"runs":[{"run_id":"undefined","status":null,"mode":"plan","spent_usd":null,"unknown_usd_sessions":null,"budget_usd":5,"blocks":[],"more":0,"closed":0,"stopped":0}]}`},
	{`{"record":{"run_id":"r","mode":"brief","status":7},"rows":[],"record":{"run_id":"z"}}`,
		`{"runs":[{"run_id":"z","status":null,"mode":"plan","spent_usd":0,"unknown_usd_sessions":0,"budget_usd":5,"blocks":[],"more":0,"closed":0,"stopped":0}]}`},
}

// Every line shape Node's companion answers (statusOf with gateDetails false:
// no timeout result, no flaky list), then the router's line, exit 1, the
// breadcrumb and the error-log entry, all in Go.
func TestB501NativeStatusServesEveryLineInGo(t *testing.T) {
	home := b501NativeStatusFixture(t, true)
	input, want := "", ""
	for _, c := range b501NativeStatusCases {
		input += c.in + "\n"
		want += c.want + "\n"
	}
	b501Stdin(t, input)
	code, out, errText := runCaptured(t, "native-status", "a", "--x")
	if code != 1 || out != want || errText != b501RouterLine {
		t.Fatalf("code=%d\nstdout=%s\nwant  =%s\nstderr=%q", code, out, want, errText)
	}
	crumbs, _ := os.ReadFile(filepath.Join(home, ".code-forge", "logs", "breadcrumbs.json"))
	if string(crumbs) != "[\"native-status\"]\n" {
		t.Fatalf("breadcrumbs = %q", crumbs)
	}
	log, _ := os.ReadFile(filepath.Join(home, ".code-forge", "logs", "errors.jsonl"))
	entry := string(log)
	if strings.Count(entry, "\n") != 1 || !strings.Contains(entry, `"verb":"native-status","sub":null,"flags":["--x"],"exit":1,"kind":"error","message":"code-forge: verb \"native-status\" has no default export function","stack":null,"cleaned":[],"fp":"72c04116089e","before":[]}`) {
		t.Fatalf("errors.jsonl = %s", entry)
	}
}

// node:readline with crlfDelay Infinity: \r, \r\n and \n end a line, an empty
// line is a line, and a last line with no ending is still answered.
func TestB501NativeStatusLineEndingsAsReadline(t *testing.T) {
	b501NativeStatusFixture(t, false)
	b501Stdin(t, "{\"record\":{},\"rows\":[]}\r{\"record\":{\"run_id\":\"a\"},\"rows\":[]}\r\n\r\n{\"record\":{\"run_id\":\"b\"},\"rows\":[]}\n{\"record\":{\"run_id\":\"c\"}}")
	want := `{"runs":[{"run_id":"undefined","status":null,"mode":"plan","spent_usd":0,"unknown_usd_sessions":0,"budget_usd":5,"blocks":[],"more":0,"closed":0,"stopped":0}]}` + "\n" +
		`{"runs":[{"run_id":"a","status":null,"mode":"plan","spent_usd":0,"unknown_usd_sessions":0,"budget_usd":5,"blocks":[],"more":0,"closed":0,"stopped":0}]}` + "\n" +
		`{"ok":false,"error":"status-unreadable"}` + "\n" +
		`{"runs":[{"run_id":"b","status":null,"mode":"plan","spent_usd":0,"unknown_usd_sessions":0,"budget_usd":5,"blocks":[],"more":0,"closed":0,"stopped":0}]}` + "\n" +
		`{"runs":[{"run_id":"c","status":null,"mode":"plan","spent_usd":null,"unknown_usd_sessions":null,"budget_usd":5,"blocks":[],"more":0,"closed":0,"stopped":0}]}` + "\n"
	code, out, errText := runCaptured(t, "native-status")
	if code != 1 || out != want || errText != b501RouterLine {
		t.Fatalf("code=%d\nstdout=%s\nwant  =%s\nstderr=%q", code, out, want, errText)
	}
	b501Stdin(t, "")
	code, out, errText = runCaptured(t, "native-status")
	if code != 1 || out != "" || errText != b501RouterLine {
		t.Fatalf("empty stdin: code=%d stdout=%q stderr=%q", code, out, errText)
	}
}

// The router prints USAGE for native-status --help and -h; with a word first
// there is no SUB_USAGE, so the companion serves.
func TestB501NativeStatusHelpInGo(t *testing.T) {
	b501NativeStatusFixture(t, false)
	for _, flag := range []string{"--help", "-h"} {
		b501Stdin(t, "{}\n")
		code, out, errText := runCaptured(t, "native-status", flag)
		if code != 0 || out != helpExpect(t, []string{"native-status", flag}, 0, "usage: code-forge native-status\n  Internal daemon companion: reads JSON records and rows from standard input.\n") || errText != "" {
			t.Fatalf("%s: code=%d stdout=%q stderr=%q", flag, code, out, errText)
		}
	}
	b501Stdin(t, "{}\n")
	code, out, errText := runCaptured(t, "native-status", "x", "--help")
	if code != 1 || out != "{\"ok\":false,\"error\":\"status-unreadable\"}\n" || errText != b501RouterLine {
		t.Fatalf("x --help: code=%d stdout=%q stderr=%q", code, out, errText)
	}
}
