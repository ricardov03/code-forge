package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// logsCall runs `code-forge logs <args>` in process with stdin from a plain
// file (no terminal). A missing Node makes any hand-over fail, so a pass
// proves Go served the case.
func logsCall(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	stdin, err := os.Create(filepath.Join(dir, "stdin"))
	if err != nil {
		t.Fatal(err)
	}
	beforeIn := os.Stdin
	os.Stdin = stdin
	defer func() { os.Stdin = beforeIn; _ = stdin.Close() }()
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
	code, runErr := run(append([]string{"logs"}, args...))
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

func logsHome(t *testing.T, body string) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-b457-node")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "")
	dir := filepath.Join(home, ".code-forge", "logs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "errors.jsonl")
	if body != "" {
		if err := os.WriteFile(file, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return home, file
}

const logsFixture = `{"ts":"2026-11-01T00:00:00.000Z","version":"0.22.0","verb":"block","sub":"close","exit":1,"kind":"error","message":"boom","fp":"aaaaaaaaaaaa"}
{"ts":"2026-11-02T00:00:00.000Z","version":"0.22.0","verb":"gates","sub":null,"exit":0,"kind":"warning","warning":"slow","message":"slow","fp":"bbbbbbbbbbbb"}
`

func TestLogsGoHelp(t *testing.T) {
	logsHome(t, "")
	cases := []struct {
		args []string
		out  string
	}{
		{[]string{"--help"}, logsUsage},
		{[]string{"-h", "bogus"}, logsUsage},
		{[]string{"clear", "--help"}, "usage: code-forge logs clear [--yes]\n"},
		{[]string{"summary", "-h"}, "usage: code-forge logs summary [--days N] [--json]\n"},
		{[]string{"path", "--help"}, "usage: code-forge logs path\n"},
		{[]string{"report", "-h"}, logsSubUsage["report"]},
	}
	for _, c := range cases {
		code, out, errText := logsCall(t, c.args...)
		c.out = helpExpect(t, append([]string{"logs"}, c.args...), 0, c.out) // B613 help page
		if code != 0 || out != c.out || errText != "" {
			t.Errorf("logs %v = %d %q %q; want 0 %q", c.args, code, out, errText, c.out)
		}
	}
}

func TestLogsGoUsageErrors(t *testing.T) {
	_, file := logsHome(t, "")
	cases := []struct {
		args []string
		err  string
	}{
		{[]string{"bogus"}, "logs: unknown subcommand \"bogus\"\n" + logsUsage},
		{[]string{"list"}, "logs: unknown subcommand \"list\"\n" + logsUsage},
		{[]string{"clear", "--nope"}, "logs: unknown flag \"--nope\"\n" + logsUsage},
		{[]string{"--last", "0"}, "logs: --last must be a positive integer\n" + logsUsage},
	}
	for _, c := range cases {
		code, out, errText := logsCall(t, c.args...)
		if code != 2 || out != "" || errText != c.err {
			t.Errorf("logs %v = %d %q %q; want 2 \"\" %q", c.args, code, out, errText, c.err)
		}
	}
	// Each usage error is logged like Node: kind usage, exit 2.
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != len(cases) {
		t.Fatalf("error log has %d lines; want %d: %s", len(lines), len(cases), data)
	}
	row := map[string]any{}
	if err := json.Unmarshal([]byte(lines[0]), &row); err != nil {
		t.Fatal(err)
	}
	if row["verb"] != "logs" || row["kind"] != "usage" || row["exit"] != float64(2) {
		t.Errorf("first log row = %v", row)
	}
}

func TestLogsGoHomeUnset(t *testing.T) {
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-b457-node")
	t.Setenv("HOME", "")
	code, out, errText := logsCall(t, "summary")
	if code != 1 || out != "" || errText != "logs: HOME is not set; there is no error log\n" {
		t.Errorf("logs summary without HOME = %d %q %q", code, out, errText)
	}
}

func TestLogsGoClear(t *testing.T) {
	_, file := logsHome(t, "")
	if code, out, errText := logsCall(t, "clear"); code != 0 || out != "no errors logged\n" || errText != "" {
		t.Errorf("clear with no log = %d %q %q", code, out, errText)
	}

	_, file = logsHome(t, logsFixture)
	code, out, errText := logsCall(t, "clear")
	want := "logs: no terminal to confirm; nothing deleted — pass --yes to delete the error log\n"
	if code != 2 || out != "" || errText != want {
		t.Errorf("clear without a terminal = %d %q %q; want 2 %q", code, out, errText, want)
	}
	data, err := os.ReadFile(file)
	if err != nil || !strings.HasPrefix(string(data), logsFixture) || !strings.Contains(string(data), `"sub":"clear"`) {
		t.Errorf("log after refused clear = %q, %v; want the old rows plus the refusal", data, err)
	}

	code, out, errText = logsCall(t, "clear", "--yes")
	if code != 0 || out != "deleted "+file+"\n" || errText != "" {
		t.Errorf("clear --yes = %d %q %q", code, out, errText)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("error log still there after clear --yes: %v", err)
	}
}

func TestLogsGoReportNothingToFile(t *testing.T) {
	logsHome(t, "")
	if code, out, errText := logsCall(t, "report"); code != 0 || out != "no errors logged\n" || errText != "" {
		t.Errorf("report with no log = %d %q %q", code, out, errText)
	}
	logsHome(t, logsFixture)
	for _, args := range [][]string{
		{"report", "--verb", "nope"},
		{"report", "--kind", "usage"},
		{"report", "--verb", "gates"}, // only a warning, left out without --include-warnings
	} {
		code, out, errText := logsCall(t, args...)
		if code != 0 || out != "no matching errors to report\n" || errText != "" {
			t.Errorf("logs %v = %d %q %q", args, code, out, errText)
		}
	}
	// A report that would file still runs in Node; with no Node it fails.
	if code, _, errText := logsCall(t, "report", "--verb", "block"); code != 127 || !strings.Contains(errText, "nonexistent-b457-node") {
		t.Errorf("report with a pick = %d %q; want the Node hand-over", code, errText)
	}
}
