package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// selectFixture is a repository with one open block whose test command is a
// fake runner named node: it logs "CALL <argc>" and then each argument.
type selectFixture struct {
	repo, home, log string
}

func newSelectFixture(t *testing.T, base map[string]string, changed map[string]string, owned []string) selectFixture {
	t.Helper()
	return newSelectFixtureTest(t, true, base, changed, owned)
}

// newSelectFixtureTest takes tokens=false for a test command without
// {test_files}.
func newSelectFixtureTest(t *testing.T, tokens bool, base map[string]string, changed map[string]string, owned []string) selectFixture {
	t.Helper()
	root := t.TempDir()
	f := selectFixture{repo: filepath.Join(root, "repo"), home: filepath.Join(root, "home"), log: filepath.Join(root, "calls.log")}
	write := func(path, body string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = f.repo
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	runner := filepath.Join(root, "bin", "node")
	write(runner, "#!/bin/sh\n{ echo \"CALL $#\"; for a in \"$@\"; do echo \"$a\"; done; } >> '"+f.log+"'\n", 0755)
	argv := []string{runner, "{test_files}"}
	if !tokens {
		argv = []string{runner, "--test"}
	}
	config, _ := json.Marshal(map[string]any{"gates": map[string]any{"test": argv, "lint": nil, "types": nil, "format": nil, "rerun_failed": 0}})
	write(filepath.Join(f.repo, ".code-forge.yml"), string(config), 0600)
	for name, body := range base {
		write(filepath.Join(f.repo, name), body, 0600)
	}
	git("init", "-q", "-b", "main")
	git("add", ".")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "base")
	git("checkout", "-qb", "block")
	for name, body := range changed {
		write(filepath.Join(f.repo, name), body, 0600)
	}
	block := map[string]any{"status": "open", "kind": "code", "base_branch": "main", "owned_files": owned, "test_files": []string{}}
	data, _ := json.Marshal(map[string]any{"run_id": "fixture", "workspace": f.repo, "status": "active", "project": "fixture", "blocks": map[string]any{"B1": block}})
	write(filepath.Join(f.home, ".code-forge", "runs", "fixture.json"), string(data), 0600)
	return f
}

func (f selectFixture) gates(t *testing.T, flags ...string) (int, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(append([]string{"gates", "run", "--run", "fixture", "--block", "B1"}, flags...))
	cmd := exec.Command(exe, "-test.run=^TestGatesProcess$")
	cmd.Dir = f.repo
	cmd.Env = append(os.Environ(), "HOME="+f.home, "CODE_FORGE_NO_ERROR_LOG=1", "CODE_FORGE_TEST_QUEUE=off", "CODE_FORGE_NODE=/nonexistent-code-forge-node", "CF_GATES_PROCESS=1", "CF_GATES_NODE_VERSION=v24.22.1", "CF_GATES_ARGS="+string(encoded))
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		e, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		code = e.ExitCode()
	}
	return code, string(out)
}

// calls is the logged runner calls; nil when the runner never ran.
func (f selectFixture) calls(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(f.log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

type selectTestGate struct {
	Gate, Scope, Reason string
	Skipped, OK         bool
}

func selectTestResult(t *testing.T, out string) selectTestGate {
	t.Helper()
	var result struct {
		AllOK   bool `json:"allOk"`
		Results []selectTestGate
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil || len(result.Results) == 0 {
		t.Fatalf("gate JSON: %v: %s", err, out)
	}
	if !result.AllOK || result.Results[0].Gate != "test" {
		t.Fatalf("want allOk with the test gate first: %s", out)
	}
	return result.Results[0]
}

// The B404 brief case: a block owning src/a.mjs, with test/x.test.mjs ->
// src/b.mjs -> src/a.mjs and an unrelated test/y.test.mjs.
var selectGraphBase = map[string]string{
	"src/a.mjs":       "export const a = 1;\n",
	"src/b.mjs":       "import { a } from './a.mjs';\nexport const b = a;\n",
	"test/x.test.mjs": "import { b } from '../src/b.mjs';\n",
	"test/y.test.mjs": "import test from 'node:test';\n",
}

func TestGatesSelectImportGraph(t *testing.T) {
	f := newSelectFixture(t, selectGraphBase, map[string]string{"src/a.mjs": "export const a = 2;\n"}, []string{"src/a.mjs"})
	code, out := f.gates(t, "--changed")
	if code != 0 {
		t.Fatalf("exit %d, want 0 with no hand-over to the Node full suite: %s", code, out)
	}
	if got, want := f.calls(t), []string{"CALL 1", "test/x.test.mjs"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("runner calls=%q, want %q (one run, never the full list)", got, want)
	}
	if g := selectTestResult(t, out); g.Skipped || g.Scope != "related" || g.Reason != "import_graph" {
		t.Fatalf("test gate=%+v, want scope related and reason import_graph", g)
	}
}

func TestGatesSelectChangedConfigTest(t *testing.T) {
	t.Run("node", func(t *testing.T) {
		// src/lonely.mjs is a Node product file no test reaches, so the
		// reason is import_graph (a test-only block is go_only, B399b).
		f := newSelectFixture(t, map[string]string{"test/config/c.test.mjs": "// base\n", "src/lonely.mjs": "export {};\n", "test/y.test.mjs": "// other\n"},
			map[string]string{"test/config/c.test.mjs": "// changed\n", "src/lonely.mjs": "export const x = 1;\n"}, []string{"test/config/c.test.mjs", "src/lonely.mjs"})
		code, out := f.gates(t, "--changed")
		if code != 0 {
			t.Fatalf("exit %d, want 0 with no hand-over: %s", code, out)
		}
		if got, want := f.calls(t), []string{"CALL 1", "test/config/c.test.mjs"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("runner calls=%q, want %q", got, want)
		}
		if g := selectTestResult(t, out); g.Skipped || g.Scope != "related" || g.Reason != "import_graph" {
			t.Fatalf("test gate=%+v, want scope related and reason import_graph", g)
		}
	})
	t.Run("native", func(t *testing.T) {
		// A native build input also selects go.test.mjs and every test that
		// builds or runs the native binary, here through a helper (B330).
		f := newSelectFixture(t, map[string]string{
			"test/config/c.test.mjs":  "// base\n",
			"native/x.go":             "package x\n",
			"test/compat/go.test.mjs": "// go build, vet and test\n",
			"test/helpers/bin.mjs":    "export const build = ['go', 'build', './cmd/code-forge-native'];\n",
			"test/n.test.mjs":         "import { build } from './helpers/bin.mjs';\n",
			"test/y.test.mjs":         "// other\n",
		}, map[string]string{"test/config/c.test.mjs": "// changed\n", "native/x.go": "package x // changed\n"}, []string{"test/config/c.test.mjs", "native/x.go"})
		code, out := f.gates(t, "--changed")
		if code != 0 {
			t.Fatalf("exit %d, want 0: %s", code, out)
		}
		if got, want := f.calls(t), []string{"CALL 3", "test/config/c.test.mjs", "test/compat/go.test.mjs", "test/n.test.mjs"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("runner calls=%q, want %q", got, want)
		}
		// This block owns no Node product file, so it keeps reason go_only.
		if g := selectTestResult(t, out); g.Skipped || g.Scope != "related" || g.Reason != "go_only" {
			t.Fatalf("test gate=%+v, want scope related and reason go_only", g)
		}
	})
}

func TestGatesSelectConfigGolden(t *testing.T) {
	f := newSelectFixture(t, map[string]string{
		"test/fixtures/a/.code-forge.yml":    "version: 1\n",
		"test/compat/config-golden.test.mjs": "// golden\n",
		"test/y.test.mjs":                    "// other\n",
	}, map[string]string{"test/fixtures/a/.code-forge.yml": "version: 2\n"}, []string{"test/fixtures/a/.code-forge.yml"})
	code, out := f.gates(t, "--changed")
	if code != 0 {
		t.Fatalf("exit %d, want 0: %s", code, out)
	}
	if got, want := f.calls(t), []string{"CALL 1", "test/compat/config-golden.test.mjs"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("runner calls=%q, want %q (B409: a fixture change runs the config golden test)", got, want)
	}
}

func TestGatesSelectForeignTestsNeverFull(t *testing.T) {
	// B409: a changed test the node runner cannot take is dropped; it never
	// widens the gate to the full suite.
	base := map[string]string{"other/UserTest.php": "<?php\n"}
	for k, v := range selectGraphBase {
		base[k] = v
	}
	f := newSelectFixture(t, base, map[string]string{"other/UserTest.php": "<?php // changed\n", "src/a.mjs": "export const a = 3;\n"}, []string{"other/UserTest.php", "src/a.mjs"})
	code, out := f.gates(t, "--changed")
	if code != 0 {
		t.Fatalf("exit %d, want 0: %s", code, out)
	}
	if got, want := f.calls(t), []string{"CALL 1", "test/x.test.mjs"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("runner calls=%q, want %q", got, want)
	}
	if g := selectTestResult(t, out); g.Scope != "related" || g.Reason != "import_graph" {
		t.Fatalf("test gate=%+v, want related import_graph, never foreign-tests full", g)
	}
}

func TestGatesSelectEmptySkips(t *testing.T) {
	f := newSelectFixture(t, map[string]string{"src/lonely.mjs": "export {};\n", "test/y.test.mjs": "// other\n"},
		map[string]string{"src/lonely.mjs": "export const x = 1;\n"}, []string{"src/lonely.mjs"})
	code, out := f.gates(t, "--changed")
	if code != 0 {
		t.Fatalf("exit %d, want 0: %s", code, out)
	}
	if got := f.calls(t); got != nil {
		t.Fatalf("runner calls=%q, want none: an empty selection skips the test gate", got)
	}
	if g := selectTestResult(t, out); !g.Skipped || g.Reason != "import_graph" {
		t.Fatalf("test gate=%+v, want skipped with reason import_graph", g)
	}
}

func TestGatesFullRunsWholeCommand(t *testing.T) {
	f := newSelectFixture(t, selectGraphBase, map[string]string{"src/a.mjs": "export const a = 2;\n"}, []string{"src/a.mjs"})
	code, out := f.gates(t, "--full")
	if code != 0 {
		t.Fatalf("--full exit %d, want 0: %s", code, out)
	}
	if got, want := f.calls(t), []string{"CALL 0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("runner calls=%q, want %q (the whole command, no file list)", got, want)
	}
	if g := selectTestResult(t, out); g.Skipped || g.Scope != "full" || g.Reason != "explicit" {
		t.Fatalf("test gate=%+v, want scope full and reason explicit", g)
	}
	if code, out := f.gates(t, "--full", "--changed"); code != 2 || !strings.Contains(out, "--full and --changed cannot go together") {
		t.Fatalf("--full --changed exit %d, want usage 2: %s", code, out)
	}
}

// A changed source with a stem-paired test that does not import it still runs
// that test (Node's pairing, kept for Swift, Python and PHP projects).
func TestGatesSelectStemPair(t *testing.T) {
	f := newSelectFixture(t, map[string]string{"src/c.mjs": "export const c = 1;\n", "test/c.test.mjs": "// reads no import\n", "test/y.test.mjs": "// other\n"},
		map[string]string{"src/c.mjs": "export const c = 2;\n"}, []string{"src/c.mjs"})
	code, out := f.gates(t, "--changed")
	if code != 0 {
		t.Fatalf("exit %d, want 0: %s", code, out)
	}
	if got, want := f.calls(t), []string{"CALL 1", "test/c.test.mjs"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("runner calls=%q, want %q (the stem pair of src/c.mjs)", got, want)
	}
}

// B404: a test command without {test_files} never runs unscoped under
// --changed; the selection is appended as its last arguments.
func TestGatesSelectNoTokenAppends(t *testing.T) {
	f := newSelectFixtureTest(t, false, selectGraphBase, map[string]string{"src/a.mjs": "export const a = 2;\n"}, []string{"src/a.mjs"})
	code, out := f.gates(t, "--changed")
	if code != 0 {
		t.Fatalf("exit %d, want 0: %s", code, out)
	}
	if got, want := f.calls(t), []string{"CALL 2", "--test", "test/x.test.mjs"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("runner calls=%q, want %q (the selection appended, never the bare command)", got, want)
	}
	if g := selectTestResult(t, out); g.Skipped || g.Scope != "related" || g.Reason != "import_graph" {
		t.Fatalf("test gate=%+v, want scope related and reason import_graph", g)
	}
}
