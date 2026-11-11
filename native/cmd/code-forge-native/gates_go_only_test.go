package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/gitx"
)

// Compat tests present in every fixture repo; the go_only scope picks from them by name.
var goOnlyFixtureCompat = []string{
	"test/compat/daemon.test.mjs",
	"test/compat/go.test.mjs",
	"test/compat/parity-gates-run.test.mjs",
	"test/compat/parity-status-direct.test.mjs",
	"test/compat/parity-status.test.mjs",
}

type goOnlyFixture struct {
	root, repo, home, argvLog string
}

func newGoOnlyFixture(t *testing.T, owned []string, withNodeTest bool) goOnlyFixture {
	t.Helper()
	root := t.TempDir()
	f := goOnlyFixture{root: root, repo: filepath.Join(root, "repo"), home: filepath.Join(root, "home"), argvLog: filepath.Join(root, "argv.log")}
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = f.repo
		cmd.Env = gitx.ChildEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	// The fake test command logs each argument on its own line.
	logger := filepath.Join(root, "log-argv.sh")
	write(logger, "printf '%s\\n' \"$@\" >> '"+f.argvLog+"'\n")
	config, _ := json.Marshal(map[string]any{"gates": map[string]any{"test": []string{"/bin/sh", logger, "{test_files}"}, "lint": nil, "types": nil, "format": nil, "rerun_failed": 0}})
	write(filepath.Join(f.repo, ".code-forge.yml"), string(config))
	for _, file := range owned {
		if !strings.HasSuffix(file, ".test.mjs") {
			write(filepath.Join(f.repo, file), "// base\n")
		}
	}
	if withNodeTest {
		write(filepath.Join(f.repo, "test/foo.test.mjs"), "import test from 'node:test';\ntest('foo', () => {});\n")
		for _, file := range goOnlyFixtureCompat {
			write(filepath.Join(f.repo, file), "import test from 'node:test';\ntest('compat', () => {});\n")
		}
	}
	git("init", "-q", "-b", "main")
	git("add", ".")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "base")
	git("checkout", "-qb", "block")
	for _, file := range owned {
		if !strings.HasSuffix(file, ".test.mjs") {
			write(filepath.Join(f.repo, file), "// changed\n")
		}
	}
	block := map[string]any{"status": "open", "kind": "code", "base_branch": "main", "owned_files": owned, "test_files": []string{}}
	data, _ := json.Marshal(map[string]any{"run_id": "fixture", "workspace": f.repo, "status": "active", "project": "fixture", "blocks": map[string]any{"B1": block}})
	write(filepath.Join(f.home, ".code-forge", "runs", "fixture.json"), string(data))
	return f
}

func (f goOnlyFixture) run(t *testing.T) (int, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal([]string{"gates", "run", "--run", "fixture", "--block", "B1", "--changed"})
	cmd := exec.Command(exe, "-test.run=^TestGatesProcess$")
	cmd.Dir = f.repo
	cmd.Env = append(envWithoutGit(), "HOME="+f.home, "CODE_FORGE_NO_ERROR_LOG=1", "CODE_FORGE_TEST_QUEUE=off", "CODE_FORGE_NODE=/nonexistent-code-forge-node", "CF_GATES_PROCESS=1", "CF_GATES_NODE_VERSION=v24.22.1", "CF_GATES_ARGS="+string(encoded))
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

func TestGatesGoOnlyRunsBlockTestsAndNativeCompat(t *testing.T) {
	for _, c := range []struct {
		name        string
		owned, want []string
	}{
		{"no-mapped-compat", []string{"native/x.go", "test/foo.test.mjs"}, []string{"test/foo.test.mjs", "test/compat/go.test.mjs"}},
		{"longest-name", []string{"native/cmd/code-forge-native/status_direct.go", "test/foo.test.mjs"}, []string{"test/foo.test.mjs", "test/compat/go.test.mjs", "test/compat/parity-status-direct.test.mjs"}},
		{"verb-prefix", []string{"native/cmd/code-forge-native/status.go", "docs/x.md", "CHANGELOG.md"}, []string{"test/compat/go.test.mjs", "test/compat/parity-status-direct.test.mjs", "test/compat/parity-status.test.mjs"}},
		{"package", []string{"native/daemon/x.go", "scripts/build-native.mjs"}, []string{"test/compat/go.test.mjs", "test/compat/daemon.test.mjs"}},
	} {
		t.Run(c.name, func(t *testing.T) { goOnlyCase(t, c.owned, c.want) })
	}
}

func goOnlyCase(t *testing.T, owned, want []string) {
	f := newGoOnlyFixture(t, owned, true)
	code, out := f.run(t)
	if code != 0 {
		t.Fatalf("go_only gates run: exit %d, want 0 (no hand-over to the Node full suite): %s", code, out)
	}
	logged, err := os.ReadFile(f.argvLog)
	if err != nil {
		t.Fatalf("the test command did not run in Go: %v; output %s", err, out)
	}
	if got := strings.Split(strings.TrimSuffix(string(logged), "\n"), "\n"); !reflect.DeepEqual(got, want) {
		t.Fatalf("test command argv: want exactly %d files %v (one run), got %d: %v", len(want), want, len(got), got)
	}
	var result struct {
		AllOK   bool `json:"allOk"`
		Results []struct {
			Gate, Scope, Reason string
			Skipped             bool
			TestCount           int `json:"test_count"`
		}
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("gate JSON: %v: %s", err, out)
	}
	if !result.AllOK || len(result.Results) != 4 {
		t.Fatalf("want allOk and four gates, got %s", out)
	}
	if g := result.Results[0]; g.Gate != "test" || g.Skipped || g.Scope != "related" || g.Reason != "go_only" || g.TestCount != len(want) {
		t.Fatalf("test gate: want scope related, reason go_only, test_count %d; got %+v", len(want), g)
	}
	ledgerData, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "ledger", "fixture.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	done := 0
	for _, line := range strings.Split(strings.TrimSpace(string(ledgerData)), "\n") {
		var row map[string]any
		if json.Unmarshal([]byte(line), &row) != nil || row["event"] != "gate.done" {
			continue
		}
		done++
		files, _ := json.Marshal(row["test_files"])
		wantFiles, _ := json.Marshal(want)
		if row["scope"] != "related" || row["reason"] != "go_only" || row["test_count"] != float64(len(want)) || row["all_ok"] != true || string(files) != string(wantFiles) {
			t.Fatalf("gate.done: want scope related, reason go_only, test_count %d, test_files %s, all_ok; got %v", len(want), wantFiles, row)
		}
	}
	if done != 1 {
		t.Fatalf("want exactly one gate.done row, got %d: %s", done, ledgerData)
	}
}

func TestGatesGoOnlyNodeProductKeepsFallback(t *testing.T) {
	for _, owned := range [][]string{{"src/a.mjs", "native/x.go"}, {"package.json", "native/x.go"}} {
		t.Run(owned[0], func(t *testing.T) {
			f := newGoOnlyFixture(t, owned, false)
			code, out := f.run(t)
			if code != 127 {
				t.Fatalf("exit %d, want 127 (hand-over to the missing Node): %s", code, out)
			}
			if _, err := os.Stat(f.argvLog); !os.IsNotExist(err) {
				t.Fatalf("the test command ran in Go; want the Node fallback only (stat err %v)", err)
			}
			rows, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "native", "handovers.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(rows)), "\n")
			var row handoverRow
			if len(lines) != 1 || json.Unmarshal([]byte(lines[0]), &row) != nil || row.Verb != "gates" || row.Reason != "gates-changed-native-dependencies" {
				t.Fatalf("want one gates hand-over row with reason gates-changed-native-dependencies, got %q", rows)
			}
		})
	}
}
