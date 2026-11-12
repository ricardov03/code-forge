package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// b458Side is what one engine left behind for one call.
type b458Side struct {
	code           int
	stdout, stderr string
	crumbs         string
	errors         []map[string]any
}

func b458Logs(t *testing.T, home string) (string, []map[string]any) {
	t.Helper()
	crumbs, _ := os.ReadFile(filepath.Join(home, ".code-forge", "logs", "breadcrumbs.json"))
	data, _ := os.ReadFile(filepath.Join(home, ".code-forge", "logs", "errors.jsonl"))
	rows := []map[string]any{}
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		row := map[string]any{}
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatalf("errors.jsonl line %q: %v", line, err)
		}
		// Engine identity differs by design; everything else must match.
		delete(row, "ts")
		delete(row, "version")
		delete(row, "node")
		rows = append(rows, row)
	}
	return string(crumbs), rows
}

func b458Repo(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// b458Go runs the public route in-process from an empty cwd with an empty HOME.
// CODE_FORGE_NODE names no program, so a hand-over fails the test.
func b458Go(t *testing.T, args []string) b458Side {
	t.Helper()
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-b458-node")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "")
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "0")
	t.Setenv("CODE_FORGE_TMP_ROOT", "")
	repo := b458Repo(t)
	saved := metaPackageRoot
	metaPackageRoot = func() (string, error) { return repo, nil }
	defer func() { metaPackageRoot = saved }()
	here, _ := os.Getwd()
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(here)
	code, stdout, stderr := runCaptured(t, args...)
	side := b458Side{code: code, stdout: stdout, stderr: stderr}
	side.crumbs, side.errors = b458Logs(t, home)
	return side
}

// b458Node runs the same call through the Node CLI with its own empty HOME and cwd.
func b458Node(t *testing.T, args []string) b458Side {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH; the Go side is checked by the golden cases")
	}
	home, cwd := t.TempDir(), t.TempDir()
	cmd := exec.Command(node, append([]string{filepath.Join(b458Repo(t), "bin", "code-forge.mjs")}, args...)...)
	cmd.Dir = cwd
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + os.TempDir(), "CODE_FORGE_NATIVE=0", "CODE_FORGE_HANDOVER_LOG=0", "NO_COLOR=1"}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		code = exit.ExitCode()
	}
	side := b458Side{code: code, stdout: stdout.String(), stderr: stderr.String()}
	side.crumbs, side.errors = b458Logs(t, home)
	return side
}

// Every form below handed over to Node before B458; Go must now answer it
// with Node's stdout, stderr, exit code, breadcrumb and error-log entry.
var b458GoForms = [][]string{
	{}, {"--help"}, {"-h"}, {"-h", "extra"}, {"help"}, {"help", "--help"}, {"help", "-h"}, {"help", "x", "--help"},
	{"--version"}, {"-v"}, {"version"}, {"version", "--help"}, {"--version", "-h"},
	{"init", "--help"}, {"init", "-h"}, {"native-status", "--help"},
	{"facts"}, {"facts", "--help"}, {"facts", "--brief"}, {"facts", "--bogus", "x"}, {"facts", "pos", "--brief", "b"},
	{"facts", "--sources"}, {"facts", "--brief", "a", "--brief", "b"}, {"facts", "--out", "o"},
	{"author", "--help"}, {"author", "--brief", "b"}, {"author", "--job=", "x"}, {"author", "--job", "plan", "--brief", "b", "extra"},
	{"s2"}, {"s2", "-h"}, {"s2", "--packet=p", "--json"}, {"s2", "--packet", "--run", "r"},
	{"jev"}, {"jev", "--help"}, {"jev", "ask", "--help"}, {"jev", "ask"}, {"jev", "nope"}, {"jev", "ask", "--state", "s"},
	{"jev", "ask", "nope", "--state", "s"}, {"jev", "ask", "lane"}, {"jev", "ask", "lane", "--state"},
	{"jev", "ask", "lane", "--state", "s", "--block", "bad id"}, {"jev", "ask", "next", "--state", "s", "--rules"},
	{"jev", "ask", "lane", "--state", "s", "--rules", "--rules"}, {"jev", "ask", "lane", "--state", "s", "--bogus", "x"},
	{"proof"}, {"proof", "--help"}, {"proof", "tier", "--help"}, {"proof", "red-green", "-h"}, {"proof", "bogus"},
	{"proof", "bogus", "--help"}, {"proof", "baseline"}, {"proof", "tier"}, {"proof", "tier", "--file", "a", "--risk", "4"},
	{"proof", "tier", "--file", "a", "--risk", "1"}, {"proof", "tier", "--file", "src/a.go", "--risk", "2"},
	{"proof", "tier", "--file", "a", "--risk", "0", "--security"}, {"proof", "tier", "--file", "", "--risk", "0"},
	{"proof", "tier", "--file", "a", "--risk", "1", "--security=yes"}, {"proof", "tier", "--file", "a", "--risk", "1", "x"},
	{"proof", "tier", "--file", "a", "--risk", "3", "--cwd", "/nonexistent-b458"},
	{"proof", "export"}, {"proof", "export", "B1"}, {"proof", "export", "B1", "--run", "r", "--remove=x"},
	{"proof", "lock", "B1", "B2", "--run", "r"}, {"proof", "unlock", "--run", "r"}, {"proof", "restore", "--run"},
	{"proof", "lock", "B1", "--run", "r", "--remove"},
	{"proof", "red-green", "B1", "--run", "r"}, {"proof", "red-green", "B1", "--run", "r", "--test", "/abs.test.mjs"},
	{"proof", "red-green", "B1", "--run", "r", "--test", "a.test.mjs::"}, {"proof", "red-green", "B1", "--run", "r", "--test", "../x.test.mjs"},
	{"proof", "red-green", "B1", "--run", "r", "--test", "a/../.."}, {"proof", "red-green", "B1", "--run", "r", "--test", ".code-forge/x.mjs"},
	{"proof", "red-green", "B1", "--run", "r", "--test", "a\\b.mjs"}, {"proof", "red-green", "B1", "--run", "r", "--test", "::case"},
	{"proof", "red-green", "B1", "--run", "r", "--test", "t.mjs", "--mechanism", "mutate"},
}

func TestB458FormsMatchNode(t *testing.T) {
	for _, args := range b458GoForms {
		goSide := b458Go(t, args)
		nodeSide := b458Node(t, args)
		if goSide.code != nodeSide.code || goSide.stdout != nodeSide.stdout || goSide.stderr != nodeSide.stderr {
			t.Errorf("%q:\n go   code=%d stdout=%q stderr=%q\n node code=%d stdout=%q stderr=%q", args, goSide.code, goSide.stdout, goSide.stderr, nodeSide.code, nodeSide.stdout, nodeSide.stderr)
			continue
		}
		if goSide.crumbs != nodeSide.crumbs {
			t.Errorf("%q: breadcrumbs go=%q node=%q", args, goSide.crumbs, nodeSide.crumbs)
		}
		if !reflect.DeepEqual(goSide.errors, nodeSide.errors) {
			t.Errorf("%q: error log\n go   %v\n node %v", args, goSide.errors, nodeSide.errors)
		}
	}
}

// Golden checks that hold without Node on PATH.
func TestB458GoAnswers(t *testing.T) {
	help := b458Go(t, []string{"help"})
	if help.code != 0 || !strings.HasPrefix(help.stdout, "Usage: code-forge <verb> [args]\n\nVerbs:\n") || !strings.Contains(help.stdout, "\n  proof\n") || !strings.Contains(help.stdout, "\n  version\n") || help.crumbs != "[\"help\"]\n" {
		t.Fatalf("help: %+v", help)
	}
	data, err := os.ReadFile(filepath.Join(b458Repo(t), "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct{ Name, Version string }
	if err := json.Unmarshal(data, &pkg); err != nil {
		t.Fatal(err)
	}
	if got := b458Go(t, []string{"-v"}); got.code != 0 || got.stdout != pkg.Name+" "+pkg.Version+"\n" || got.stderr != "" || got.crumbs != "[\"version\"]\n" {
		t.Fatalf("-v: %+v", got)
	}
	tier := b458Go(t, []string{"proof", "tier", "--file", "a", "--risk", "2"})
	if tier.code != 0 || tier.stdout != "{\"file\":\"a\",\"tier\":\"high\",\"reason\":\"risk\"}\n" || tier.crumbs != "[\"proof tier\"]\n" {
		t.Fatalf("proof tier: %+v", tier)
	}
	bad := b458Go(t, []string{"proof", "red-green", "B1", "--run", "r", "--test", "../x.mjs"})
	if bad.code != 2 || bad.stderr != "proof red-green: --test must stay inside the repository (no .. escape)\n" || len(bad.errors) != 1 || bad.errors[0]["kind"] != "usage" || bad.errors[0]["sub"] != "red-green" {
		t.Fatalf("proof red-green usage: %+v", bad)
	}
}

// The forms Go does not serve yet keep the hand-over, each with its own reason.
func TestB458HandOvers(t *testing.T) {
	cases := []struct {
		args   []string
		reason string
	}{
		{[]string{"init"}, "init-setup"},
		{[]string{"init", "--no-interaction"}, "init-setup"},
		{[]string{"facts", "--brief", "b.md"}, "facts-session"},
		{[]string{"author", "--job", "plan", "--brief", "b.md"}, "author-session"},
		{[]string{"s2", "--packet", "p.json"}, "system-two-session"},
		{[]string{"jev", "ask", "lane", "--state", "s.json"}, "jev-ask"},
	}
	for _, c := range cases {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("CODE_FORGE_NODE", "/nonexistent-b458-node")
		t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
		t.Setenv("CODE_FORGE_NATIVE", "1")
		// A route that hands over prints the exec error and returns 127.
		if code, _ := run(c.args); code != 127 {
			t.Fatalf("%q: code=%d; want the Node hand-over", c.args, code)
		}
		data, _ := os.ReadFile(filepath.Join(home, ".code-forge", "native", "handovers.jsonl"))
		var row handoverRow
		if json.Unmarshal(bytes.TrimSpace(data), &row) != nil || row.Reason != c.reason || row.Verb != c.args[0] {
			t.Fatalf("%q: handover row %q; want reason %q", c.args, data, c.reason)
		}
		if _, err := os.Stat(filepath.Join(home, ".code-forge", "logs", "breadcrumbs.json")); err == nil {
			t.Fatalf("%q: Go wrote a breadcrumb before the hand-over; Node writes its own", c.args)
		}
	}
	saved := metaPackageRoot
	metaPackageRoot = func() (string, error) { return "", errors.New("no install") }
	defer func() { metaPackageRoot = saved }()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if code, _ := run([]string{"help"}); code != 127 {
		t.Fatalf("help without an install: code=%d", code)
	}
	data, _ := os.ReadFile(filepath.Join(home, ".code-forge", "native", "handovers.jsonl"))
	if !bytes.Contains(data, []byte(`"reason":"help-verbs"`)) {
		t.Fatalf("help hand-over row %q", data)
	}
}
