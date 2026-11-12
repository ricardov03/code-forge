package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// B502: `init` refusals in Go, compared with Node's wizard on the same temp
// HOME and project: exit code, stdout (the agent JSON line), stderr, the
// init log file, the breadcrumb and the error log.

type b502Side struct {
	code                int
	stdout, stderr, log string
	crumbs, errors      string
}

var b502Stamp = regexp.MustCompile(`init-\d{4}-\d\d-\d\dT\d\d-\d\d-\d\d-\d{3}Z\.log`)

func b502Normalize(text, home, cwd string) string {
	if home != "" {
		text = strings.ReplaceAll(text, home, "<HOME>")
	}
	text = strings.ReplaceAll(text, cwd, "<CWD>")
	return b502Stamp.ReplaceAllString(text, "init-<TS>.log")
}

func b502Collect(t *testing.T, side *b502Side, home, cwd string) {
	t.Helper()
	if home != "" {
		crumbs, rows := b458Logs(t, home)
		data, _ := json.Marshal(rows)
		side.crumbs, side.errors = crumbs, b502Normalize(string(data), home, cwd)
		logs, _ := filepath.Glob(filepath.Join(home, ".code-forge", "logs", "init-*.log"))
		for _, file := range logs {
			data, _ := os.ReadFile(file)
			side.log += filepath.Base(b502Normalize(file, home, cwd)) + ":" + string(data)
		}
	}
	side.stdout = b502Normalize(side.stdout, home, cwd)
	side.stderr = b502Normalize(side.stderr, home, cwd)
}

// b502Project makes a temp project dir (physical path) and runs setup in it.
func b502Project(t *testing.T, setup func(cwd string)) string {
	t.Helper()
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if setup != nil {
		setup(cwd)
	}
	return cwd
}

func b502Go(t *testing.T, args []string, noHome bool, setup func(cwd string)) b502Side {
	t.Helper()
	home, cwd := t.TempDir(), b502Project(t, setup)
	if noHome {
		home = ""
	}
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-b502-node")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "")
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "0")
	t.Setenv("CODE_FORGE_TMP_ROOT", "")
	t.Setenv("CODE_FORGE_NATIVE", "1")
	for _, name := range []string{"CLAUDECODE", "CLAUDE_CODE", "CURSOR_AGENT", "AI_AGENT", "CODEX_SANDBOX", "CODEX_SANDBOX_NETWORK_DISABLED"} {
		t.Setenv(name, "")
	}
	savedTTY, savedNow := initStdinTTY, initNow
	initStdinTTY = func() bool { return false }
	initNow = func() time.Time { return time.Date(2026, 11, 12, 9, 8, 7, 6e6, time.UTC) }
	defer func() { initStdinTTY, initNow = savedTTY, savedNow }()
	repo := b458Repo(t)
	savedRoot := metaPackageRoot
	metaPackageRoot = func() (string, error) { return repo, nil }
	defer func() { metaPackageRoot = savedRoot }()
	here, _ := os.Getwd()
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(here)
	code, stdout, stderr := runCaptured(t, append([]string{"init"}, args...)...)
	side := b502Side{code: code, stdout: stdout, stderr: stderr}
	b502Collect(t, &side, home, cwd)
	return side
}

func b502Node(t *testing.T, args []string, noHome bool, setup func(cwd string)) b502Side {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH")
	}
	home, cwd := t.TempDir(), b502Project(t, setup)
	env := []string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + os.TempDir(), "CODE_FORGE_NATIVE=0", "CODE_FORGE_HANDOVER_LOG=0", "NO_COLOR=1"}
	if noHome {
		home = ""
	} else {
		env = append(env, "HOME="+home)
	}
	cmd := exec.Command(node, append([]string{filepath.Join(b458Repo(t), "bin", "code-forge.mjs"), "init"}, args...)...)
	cmd.Dir, cmd.Env = cwd, env
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
	side := b502Side{code: code, stdout: stdout.String(), stderr: stderr.String()}
	b502Collect(t, &side, home, cwd)
	return side
}

func b502WriteConfig(text string) func(string) {
	return func(cwd string) {
		_ = os.WriteFile(filepath.Join(cwd, ".code-forge.yml"), []byte(text), 0o644)
	}
}

func TestB502InitRefusalsMatchNode(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		noHome bool
		setup  func(string)
	}{
		{"unknown flag", []string{"--bogus"}, false, nil},
		{"positional", []string{"extra"}, false, nil},
		{"needs a value", []string{"--tools"}, false, nil},
		{"value starts with dash", []string{"--provider", "-g"}, false, nil},
		{"bad tools", []string{"--tools=all"}, false, nil},
		{"bad yes-tool", []string{"--yes-tool", "vim"}, false, nil},
		{"empty harness list", []string{"--harness", " , "}, false, nil},
		{"unknown harness", []string{"--harness", "claude,vim"}, false, nil},
		{"bad provider", []string{"--second-provider", "google"}, false, nil},
		{"level no key", []string{"--level", "=x"}, false, nil},
		{"level name", []string{"--level", "L4=x"}, false, nil},
		{"level provider", []string{"--level", "L2=m:high@google"}, false, nil},
		{"level effort", []string{"--level", "L2=m:@xai"}, false, nil},
		{"level model", []string{"--level", "L2=:high"}, false, nil},
		{"multimodel", []string{"--multimodel", "yes"}, false, nil},
		{"jev-ref", []string{"--jev-ref", "not-a-ref"}, false, nil},
		{"jev-env", []string{"--jev-env", "1BAD"}, false, nil},
		{"engine subprocess", []string{"--engine", "subprocess"}, false, nil},
		{"engine", []string{"--engine=cloud"}, false, nil},
		{"solo project", []string{"--solo-project", "01"}, false, nil},
		{"gate no key", []string{"--gate", "test"}, false, nil},
		{"gate name", []string{"--gate", "build=make"}, false, nil},
		{"gate quote", []string{"--gate", `test=npm "run`}, false, nil},
		{"gate empty", []string{"--gate", "test=  "}, false, nil},
		{"coder flag", []string{"--coder-may-push", "maybe"}, false, nil},
		{"proof key", []string{"--proof", "speed=1"}, false, nil},
		{"proof isolation", []string{"--proof", "isolation=copy"}, false, nil},
		{"boolean value", []string{"--skip-doctor=1"}, false, nil},
		{"repeated", []string{"--engine", "auto", "--engine", "solo"}, false, nil},
		{"scope exclusive", []string{"-g", "-p"}, false, nil},
		{"jev exclusive", []string{"--no-jev", "--jev-ref", "op://v/i/f", "--jev-env", "K"}, false, nil},
		{"no HOME", []string{"--no-interaction"}, true, nil},
		{"config is a directory", []string{"--no-interaction"}, false, func(cwd string) { _ = os.Mkdir(filepath.Join(cwd, ".code-forge.yml"), 0o755) }},
		{"config is a list", nil, false, b502WriteConfig("- a\n- b\n")},
		{"config is empty", nil, false, b502WriteConfig("")},
		{"config version newer", nil, false, b502WriteConfig("version: 2\n")},
		{"config version text", []string{"--no-interaction"}, false, b502WriteConfig("version: one\n")},
		{"draft PR without push", []string{"--no-interaction", "--coder-may-open-draft-pr", "yes", "--skip-doctor", "--no-jev"}, false, nil},
		{"draft PR, push no", []string{"--coder-may-open-draft-pr=yes", "--coder-may-push=no"}, false, nil},
		{"draft PR, existing push no", []string{"--coder-may-open-draft-pr", "yes"}, false, b502WriteConfig("version: 1\nautonomy:\n  coder_may_push: false\n")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			goSide := b502Go(t, c.args, c.noHome, c.setup)
			nodeSide := b502Node(t, c.args, c.noHome, c.setup)
			if goSide != nodeSide {
				t.Fatalf("%q:\n go:   %#v\n node: %#v", c.args, goSide, nodeSide)
			}
			if goSide.code == 0 {
				t.Fatalf("%q: a refusal exited 0", c.args)
			}
		})
	}
}

// The draft-PR refusal writes only the init log: no .code-forge.yml, no
// user config, no links.
func TestB502DraftRefusalWritesNothing(t *testing.T) {
	var project string
	side := b502Go(t, []string{"--coder-may-open-draft-pr", "yes"}, false, func(cwd string) { project = cwd })
	if side.code != 2 || !strings.Contains(side.stdout, `"ok":false`) || side.log == "" {
		t.Fatalf("refusal: %#v", side)
	}
	entries, _ := os.ReadDir(project)
	if len(entries) != 0 {
		t.Fatalf("the project gained %d entries", len(entries))
	}
}

func TestB502InitHandOvers(t *testing.T) {
	cases := []struct {
		args   []string
		tty    bool
		config string
		reason string
	}{
		{[]string{"init"}, false, "", "init-setup"},
		{[]string{"init", "--no-interaction", "--no-jev"}, false, "", "init-setup"},
		{[]string{"init", "--coder-may-open-draft-pr", "yes", "--coder-may-push", "yes"}, false, "", "init-setup"},
		{[]string{"init"}, true, "", "init-interactive"},
		{[]string{"init", "--jev-ref", "https://my.1password.com/vaults/x/allitems/y"}, false, "", "init-jev-link"},
		{[]string{"init", "--engine", "\xff"}, false, "", "init-arg-encoding"},
		{[]string{"init"}, false, "a: [1\n", "init-config-syntax"},
		{[]string{"init", "--coder-may-open-draft-pr", "yes"}, false, "provider: google\n", "init-answers"},
	}
	for _, c := range cases {
		home, cwd := t.TempDir(), b502Project(t, nil)
		if c.config != "" {
			b502WriteConfig(c.config)(cwd)
		}
		t.Setenv("HOME", home)
		t.Setenv("CODE_FORGE_NODE", "/nonexistent-b502-node")
		t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
		t.Setenv("CODE_FORGE_NATIVE", "1")
		for _, name := range []string{"CLAUDECODE", "CLAUDE_CODE", "CURSOR_AGENT", "AI_AGENT", "CODEX_SANDBOX", "CODEX_SANDBOX_NETWORK_DISABLED"} {
			t.Setenv(name, "")
		}
		tty := c.tty
		saved := initStdinTTY
		initStdinTTY = func() bool { return tty }
		here, _ := os.Getwd()
		_ = os.Chdir(cwd)
		code, _ := run(c.args)
		_ = os.Chdir(here)
		initStdinTTY = saved
		if code != 127 {
			t.Fatalf("%q: code=%d; want the Node hand-over", c.args, code)
		}
		data, _ := os.ReadFile(filepath.Join(home, ".code-forge", "native", "handovers.jsonl"))
		var row handoverRow
		if json.Unmarshal(bytes.TrimSpace(data), &row) != nil || row.Reason != c.reason || row.Verb != "init" {
			t.Fatalf("%q: handover row %q; want reason %q", c.args, data, c.reason)
		}
		if _, err := os.Stat(filepath.Join(home, ".code-forge", "logs")); err == nil {
			t.Fatalf("%q: Go wrote logs before the hand-over", c.args)
		}
		if entries, _ := os.ReadDir(cwd); len(entries) > 1 || len(entries) == 1 && c.config == "" {
			t.Fatalf("%q: Go wrote into the project before the hand-over", c.args)
		}
	}
}
