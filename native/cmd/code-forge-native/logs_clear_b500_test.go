package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/humanask"
)

const b500LogsUsage = "usage: code-forge logs [--last N] [--json] | logs summary [--days N] [--json] | logs clear [--yes] | logs path\n" +
	"       code-forge logs report [--last N] [--kind K] [--verb V] [--note \"text\"] [--include-warnings] [--with-doctor] [--no-ai] [--allow-old] [--force] [--dry-run] [--yes]\n"

const b500ErrorLog = `{"ts":"2026-11-10T09:00:00.000Z","verb":"run","exit":1,"kind":"other","message":"x"}
torn{
{"ts":"2026-11-10T10:00:00.000Z","verb":"block","exit":2,"kind":"usage","message":"y"}
`

// b500Terminal fakes the terminal: stdin, stdout and the clack answer.
func b500Terminal(t *testing.T, stdin, both bool, answer humanask.Result, asked *string) {
	t.Helper()
	oldStdin, oldAsk, oldConfirm := logsStdinTTY, logsAskTTY, logsConfirm
	logsStdinTTY = func() bool { return stdin }
	logsAskTTY = func() bool { return both }
	logsConfirm = func(message string) (humanask.Result, error) {
		*asked = message
		return answer, nil
	}
	t.Cleanup(func() { logsStdinTTY, logsAskTTY, logsConfirm = oldStdin, oldAsk, oldConfirm })
}

// Expected texts are Node's (CODE_FORGE_NATIVE=0) for the same home.
func TestB500LogsClearInGo(t *testing.T) {
	home, _ := b500Env(t)
	logs := filepath.Join(home, ".code-forge", "logs")
	file := filepath.Join(logs, "errors.jsonl")
	asked := ""
	b500Terminal(t, false, false, humanask.Cancelled, &asked)

	// No log: nothing to delete, with or without --yes.
	for _, args := range [][]string{{"logs", "clear"}, {"logs", "clear", "--yes"}} {
		if code, out, errText := b500Run(t, args...); code != 0 || out != "no errors logged\n" || errText != "" {
			t.Errorf("%q without a log: got (%d, %q, %q)", args, code, out, errText)
		}
	}
	b500Write(t, file, b500ErrorLog)
	b500Write(t, filepath.Join(logs, "breadcrumbs.json"), `["run","block"]`+"\n")

	// No terminal: refuse, keep the log, log the refusal.
	noTTY := "logs: no terminal to confirm; nothing deleted — pass --yes to delete the error log\n"
	if code, out, errText := b500Run(t, "logs", "clear"); code != 2 || out != "" || errText != noTTY {
		t.Fatalf("no terminal: got (%d, %q, %q)", code, out, errText)
	}
	log := b500Read(t, file)
	if !strings.HasPrefix(log, b500ErrorLog) || !strings.Contains(log, `"verb":"logs","sub":"clear","flags":[],"exit":2,"kind":"usage","message":"logs: no terminal to confirm; nothing deleted — pass --yes to delete the error log","stack":null,"cleaned":[],"fp":"`) || !strings.HasSuffix(log, `"before":["run","block"]}`+"\n") {
		t.Errorf("error log after the refusal:\n%s", log)
	}
	if asked != "" {
		t.Errorf("asked without a terminal: %q", asked)
	}

	// A terminal, answered no: cancelled, exit 1, the log stays (now 3 entries).
	b500Terminal(t, true, true, humanask.Cancelled, &asked)
	if code, out, errText := b500Run(t, "logs", "clear"); code != 1 || out != "cancelled — nothing deleted\n" || errText != "" {
		t.Errorf("cancelled: got (%d, %q, %q)", code, out, errText)
	}
	if asked != "Delete the error log (3 entries)?" {
		t.Errorf("asked %q", asked)
	}
	if log := b500Read(t, file); !strings.Contains(log, `"sub":"clear","flags":[],"exit":1,"kind":"error","message":null,`) {
		t.Errorf("cancel entry missing:\n%s", log)
	}

	// Answered yes: deleted.
	b500Terminal(t, true, true, humanask.Confirmed, &asked)
	if code, out, errText := b500Run(t, "logs", "clear"); code != 0 || out != "deleted "+file+"\n" || errText != "" {
		t.Errorf("confirmed: got (%d, %q, %q)", code, out, errText)
	}
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("log still there: %v", err)
	}

	// --yes never asks.
	asked = ""
	b500Write(t, file, b500ErrorLog)
	if code, out, _ := b500Run(t, "logs", "clear", "--yes"); code != 0 || out != "deleted "+file+"\n" || asked != "" {
		t.Errorf("--yes: got (%d, %q), asked %q", code, out, asked)
	}
	crumbs := b500Read(t, filepath.Join(logs, "breadcrumbs.json"))
	if crumbs != `["block","logs clear","logs clear","logs clear","logs clear"]`+"\n" {
		t.Errorf("breadcrumbs = %q", crumbs)
	}
	if reason := b500HandedOver(t, home); reason != "" {
		t.Errorf("handed over: %s", reason)
	}

	// stdin a terminal but stdout not: clack would still draw; Node keeps it.
	b500Write(t, file, b500ErrorLog)
	b500Terminal(t, true, false, humanask.Confirmed, &asked)
	if code, _, _ := b500Run(t, "logs", "clear"); code != 127 || b500HandedOver(t, home) != "logs-clear-prompt" {
		t.Errorf("half terminal: exit %d, reason %q", code, b500HandedOver(t, home))
	}
	if b500Read(t, file) != b500ErrorLog {
		t.Errorf("half terminal touched the log")
	}
}

func TestB500LogsClearUsageAndHelp(t *testing.T) {
	home, _ := b500Env(t)
	file := filepath.Join(home, ".code-forge", "logs", "errors.jsonl")
	b500Write(t, file, b500ErrorLog)
	// Help writes no breadcrumb (the router returns before noting the verb).
	b500Run(t, "logs", "--help")
	b500Run(t, "logs", "clear", "-h")
	if _, err := os.Stat(filepath.Join(home, ".code-forge", "logs", "breadcrumbs.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("help wrote a breadcrumb: %v", err)
	}
	for _, tc := range []struct {
		args    []string
		code    int
		out, er string
	}{
		{[]string{"logs", "--help"}, 0, b500LogsUsage, ""},
		{[]string{"logs", "-h"}, 0, b500LogsUsage, ""},
		{[]string{"logs", "clear", "--help"}, 0, "usage: code-forge logs clear [--yes]\n", ""},
		{[]string{"logs", "summary", "-h"}, 0, "usage: code-forge logs summary [--days N] [--json]\n", ""},
		{[]string{"logs", "path", "--help"}, 0, "usage: code-forge logs path\n", ""},
		{[]string{"logs", "report", "-h"}, 0, "usage: code-forge logs report [--last N] [--kind K] [--verb V] [--note \"text\"] [--include-warnings] [--with-doctor] [--no-ai] [--allow-old] [--force] [--dry-run] [--yes]\n", ""},
		{[]string{"logs", "clear", "--yes", "--yes"}, 2, "", "logs: --yes given more than once\n" + b500LogsUsage},
		{[]string{"logs", "clear", "--yes=1"}, 2, "", "logs: --yes takes no value\n" + b500LogsUsage},
		{[]string{"logs", "clear", "--no"}, 2, "", "logs: unknown flag \"--no\"\n" + b500LogsUsage},
		{[]string{"logs", "clear", "--"}, 2, "", "logs: unknown flag \"--\"\n" + b500LogsUsage},
		{[]string{"logs", "clear", "--yes", "--help"}, 2, "", "logs: unknown flag \"--help\"\n" + b500LogsUsage},
		{[]string{"logs", "clear", "-y"}, 2, "", "logs: unexpected argument \"-y\"\n" + b500LogsUsage},
		{[]string{"logs", "clear", "extra", "--bad"}, 2, "", "logs: unknown flag \"--bad\"\n" + b500LogsUsage},
	} {
		code, out, errText := b500Run(t, tc.args...)
		if code != tc.code || out != tc.out || errText != tc.er {
			t.Errorf("%q: got (%d, %q, %q), want (%d, %q, %q)", tc.args, code, out, errText, tc.code, tc.out, tc.er)
		}
	}
	if !strings.Contains(b500Read(t, file), `"sub":"clear","flags":["--bad"],"exit":2,"kind":"usage","message":"logs: unknown flag \"--bad\"\nusage: code-forge logs [--last N]`) {
		t.Errorf("usage entry:\n%s", b500Read(t, file))
	}
	if !strings.HasPrefix(b500Read(t, file), b500ErrorLog) {
		t.Errorf("usage errors changed the old entries")
	}
	t.Setenv("HOME", "")
	if code, out, errText := b500Run(t, "logs", "clear"); code != 1 || out != "" || errText != "logs: HOME is not set; there is no error log\n" {
		t.Errorf("no HOME: got (%d, %q, %q)", code, out, errText)
	}
	t.Setenv("HOME", home)
	if reason := b500HandedOver(t, home); reason != "" {
		t.Errorf("handed over: %s", reason)
	}
	// logs report stays with Node, under its own reason.
	if code, _, _ := b500Run(t, "logs", "report", "--dry-run"); code != 127 || b500HandedOver(t, home) != "logs-report" {
		t.Errorf("report: exit %d, reason %q", code, b500HandedOver(t, home))
	}
}
