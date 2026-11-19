package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// runGuardedCaptured runs args the way main does (runGuarded) and returns the
// exit code and what reached stdout and stderr.
func runGuardedCaptured(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	files := [2]*os.File{}
	for i := range files {
		file, err := os.CreateTemp(dir, "out-")
		if err != nil {
			t.Fatal(err)
		}
		files[i] = file
	}
	out, errOut := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = files[0], files[1]
	code, err := runGuarded(args)
	os.Stdout, os.Stderr = out, errOut
	if err != nil {
		t.Fatalf("%v: error %v", args, err)
	}
	texts := [2]string{}
	for i, file := range files {
		_ = file.Close()
		data, _ := os.ReadFile(file.Name())
		texts[i] = string(data)
	}
	return code, texts[0], texts[1]
}

type b872Case struct {
	args []string
	code int
	next any // string or nil
}

// b872Check runs one refusal with --json: same exit code, stderr as before,
// and exactly one compact {"ok":false,"error","next"} line on stdout whose
// error is the stderr text.
func b872Check(t *testing.T, tc b872Case) {
	t.Helper()
	code, out, errs := runGuardedCaptured(t, tc.args...)
	if code != tc.code {
		t.Fatalf("%v: exit %d, want %d (stdout %q stderr %q)", tc.args, code, tc.code, out, errs)
	}
	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("%v: stdout is not one line: %q (stderr %q)", tc.args, out, errs)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v: stdout is not JSON: %q", tc.args, out)
	}
	if len(got) != 3 || got["ok"] != false || got["error"] != strings.TrimSpace(errs) || got["next"] != tc.next {
		t.Fatalf("%v: line %s, stderr %q, want next %v", tc.args, out, errs, tc.next)
	}
	if !strings.HasPrefix(out, `{"ok":false,"error":`) {
		t.Fatalf("%v: key order: %s", tc.args, out)
	}
}

// TestB872JSONRefusalsPrintOneLine uses the B801 audit's P04 samples (state
// none: a repository with a config and no run).
func TestB872JSONRefusalsPrintOneLine(t *testing.T) {
	home, repo := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	t.Setenv("CODE_FORGE_NODE", filepath.Join(home, "missing-node"))
	if err := os.WriteFile(filepath.Join(repo, ".code-forge.yml"), []byte("project:\n  slug: repon\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	here, _ := os.Getwd()
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(here) }()
	for _, tc := range []b872Case{
		{[]string{"docs", "path", "--json"}, 1, "code-forge docs sync"},
		{[]string{"run", "end", "--run", "r-missing", "--json"}, 1, "code-forge status"},                                                                   // B875: the refusal names its next command
		{[]string{"block", "open", "B1", "--run", "r-missing", "--owned", "src/a.txt", "--acceptance", "plans/acc.yml", "--json"}, 1, "code-forge status"}, // B875
		{[]string{"report", "--slug", "repon", "--json"}, 1, "code-forge run start"},                                                                       // B875
		{[]string{"queue", "run", "--json", "--", "true"}, 2, "code-forge queue --help"},
	} {
		b872Check(t, tc)
	}

	// A command that answers --json on stdout itself keeps its one line.
	code, out, _ := runGuardedCaptured(t, "outcome", "check", "plans/missing.json", "--json")
	if code != 1 || out != `{"ok":false,"problems":["Cannot read outcome \"plans/missing.json\" (ENOENT)"]}`+"\n" {
		t.Fatalf("outcome check --json = %d %q", code, out)
	}
	// Without --json nothing changes: stdout stays empty.
	code, out, errs := runGuardedCaptured(t, "docs", "path")
	if code != 1 || out != "" || errs != "docs: none; run code-forge docs sync\n" {
		t.Fatalf("docs path = %d %q %q", code, out, errs)
	}
	// --json after `--` belongs to the child command.
	if wanted := jsonRefusalWanted([]string{"queue", "run", "--", "x", "--json"}); wanted {
		t.Fatal("--json after -- was read as the verb's flag")
	}
}

// TestB872JSONRefusalsWithALiveRun uses the audit's run-state samples.
func TestB872JSONRefusalsWithALiveRun(t *testing.T) {
	f := newStartFixture(t, "project:\n  slug: unit\ntmp:\n  root: TMPROOT\n")
	sleeper, _ := startSleeper(t)
	code, out, errs := runVerbCall(t, "start", "--cwd", f.repo, "--run", "r-json", "--worker-pid", strconv.Itoa(sleeper.Process.Pid))
	if code != 0 {
		t.Fatalf("start = %d %q %q", code, out, errs)
	}
	if err := os.Chdir(f.repo); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []b872Case{
		{[]string{"autopilot", "ratify", "row-missing", "--run", "r-json", "--json"}, 1, nil},
		{[]string{"autopilot", "revert", "row-missing", "--run", "r-json", "--json"}, 1, nil},
		{[]string{"block", "ack-waivers", "B1", "--run", "r-json", "--json"}, 1, nil},
		{[]string{"block", "waive", "B1", "F1", "--run", "r-json", "--file", "src/a.txt", "--reason", "test", "--json"}, 2, nil},
		{[]string{"run", "human-checks", "clear", "--run", "r-json", "--block", "B1", "--clause", "C1", "--note", "ok", "--json"}, 2, nil},
	} {
		b872Check(t, tc)
	}
}
