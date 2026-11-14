package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B667 tests: a fake `go` (its base name is go) logs each argv line; a Go
// project's block gate tests only the changed packages, and a batch runs one
// full go test ./... at the end.

const b667FakeGo = `#!/bin/sh
case "$PWD" in *.merge-gate-*) where=merge ;; *) where=block ;; esac
printf '%s|%s\n' "$where" "$*" >> "$B667_GO_LOG"
if [ "$1" = test ] && [ -f "$B667_GO_LOG.red-full" ]; then
  for a in "$@"; do [ "$a" = ./... ] && { echo "--- FAIL: TestWhole"; exit 1; }; done
fi
exit 0
`

func b667WriteFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func b667FakeGoBin(t *testing.T, root string) (string, string) {
	t.Helper()
	bin := filepath.Join(root, "fakebin", "go")
	b667WriteFile(t, bin, b667FakeGo)
	if err := os.Chmod(bin, 0755); err != nil {
		t.Fatal(err)
	}
	return bin, filepath.Join(root, "go-calls.log")
}

func b667Calls(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// b667GoBase is a Go module with three packages; c has a testdata file.
var b667GoBase = map[string]string{
	"go.mod":          "module example.invalid/m\n\ngo 1.22\n",
	"a/a.go":          "package a\n",
	"a/a_test.go":     "package a\n",
	"b/b.go":          "package b\n",
	"c/c.go":          "package c\n",
	"c/c_test.go":     "package c\n",
	"c/testdata/x.in": "one\n",
	"README.md":       "# m\n",
}

type b667GateFixture struct {
	repo, home, log string
}

// newB667GateFixture is a Go repository on branch block (from main) with one
// open block that owns a, b, c and README.md. changed writes files; a nil
// body deletes the file.
func newB667GateFixture(t *testing.T, gatesExtra map[string]any, changed map[string]*string) b667GateFixture {
	t.Helper()
	root := t.TempDir()
	goBin, log := b667FakeGoBin(t, root)
	f := b667GateFixture{repo: filepath.Join(root, "repo"), home: filepath.Join(root, "home"), log: log}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = f.repo
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	g := map[string]any{"test": []string{goBin, "test", "./...", "-count=1"}, "lint": []string{goBin, "vet", "./..."}, "types": nil, "format": nil, "rerun_failed": 0}
	for k, v := range gatesExtra {
		g[k] = v
	}
	config, _ := json.Marshal(map[string]any{"gates": g})
	b667WriteFile(t, filepath.Join(f.repo, ".code-forge.yml"), string(config))
	for name, body := range b667GoBase {
		b667WriteFile(t, filepath.Join(f.repo, name), body)
	}
	git("init", "-q", "-b", "main")
	git("add", ".")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "base")
	git("checkout", "-qb", "block")
	for name, body := range changed {
		if body == nil {
			if err := os.Remove(filepath.Join(f.repo, name)); err != nil {
				t.Fatal(err)
			}
			continue
		}
		b667WriteFile(t, filepath.Join(f.repo, name), *body)
	}
	block := map[string]any{"status": "open", "kind": "code", "base_branch": "main", "owned_files": []string{"a", "b", "c", "scripts", "README.md", "go.mod"}, "test_files": []string{}}
	data, _ := json.Marshal(map[string]any{"run_id": "fixture", "workspace": f.repo, "status": "active", "project": "fixture", "blocks": map[string]any{"B1": block}})
	b667WriteFile(t, filepath.Join(f.home, ".code-forge", "runs", "fixture.json"), string(data))
	return f
}

func (f b667GateFixture) gates(t *testing.T) (int, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal([]string{"gates", "run", "--run", "fixture", "--block", "B1", "--changed", "--json"})
	cmd := exec.Command(exe, "-test.run=^TestGatesProcess$")
	cmd.Dir = f.repo
	cmd.Env = append(os.Environ(), "HOME="+f.home, "B667_GO_LOG="+f.log, "CODE_FORGE_NO_ERROR_LOG=1", "CODE_FORGE_TEST_QUEUE=off", "CODE_FORGE_NODE=/nonexistent-code-forge-node", "CF_GATES_PROCESS=1", "CF_GATES_NODE_VERSION=v24.22.1", "CF_GATES_ARGS="+string(encoded))
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

func (f b667GateFixture) done(t *testing.T) map[string]any {
	t.Helper()
	rows, err := ledger.ReadAllRows(filepath.Join(f.home, ".code-forge", "ledger"), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	var last map[string]any
	for _, v := range rows {
		if row, ok := v.(map[string]any); ok && row["event"] == "gate.done" {
			last = row
		}
	}
	if last == nil {
		t.Fatalf("no gate.done row")
	}
	return last
}

func b667Body(s string) *string { return &s }

func TestB667GoChangedPackages(t *testing.T) {
	cases := []struct {
		name     string
		changed  map[string]*string
		test     string // the logged go test line; "" means go test never ran
		packages []any
		reason   string
	}{
		{"one package", map[string]*string{"a/a.go": b667Body("package a\n\nvar X = 1\n")}, "block|test ./a -count=1", []any{"./a"}, "go_packages"},
		{"two packages", map[string]*string{"a/a.go": b667Body("package a\n\nvar X = 1\n"), "b/b.go": b667Body("package b\n\nvar Y = 1\n")}, "block|test ./a ./b -count=1", []any{"./a", "./b"}, "go_packages"},
		{"testdata change", map[string]*string{"c/testdata/x.in": b667Body("two\n")}, "block|test ./c -count=1", []any{"./c"}, "go_packages"},
		{"deleted package dropped", map[string]*string{"b/b.go": nil, "a/a_test.go": b667Body("package a\n\n// more\n")}, "block|test ./a -count=1", []any{"./a"}, "go_packages"},
		{"no Go change", map[string]*string{"scripts/run.sh": b667Body("echo hi\n")}, "", []any{}, "no_changes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newB667GateFixture(t, nil, tc.changed)
			code, out := f.gates(t)
			if code != 0 {
				t.Fatalf("exit=%d, want 0: %s", code, out)
			}
			calls := b667Calls(t, f.log)
			tests := []string{}
			vets := []string{}
			for _, c := range calls {
				if strings.HasPrefix(c, "block|test") {
					tests = append(tests, c)
				}
				if strings.HasPrefix(c, "block|vet") {
					vets = append(vets, c)
				}
			}
			want := []string{}
			if tc.test != "" {
				want = []string{tc.test}
			}
			if !reflect.DeepEqual(tests, want) {
				t.Errorf("go test calls=%q, want %q (all calls %q)", tests, want, calls)
			}
			if !reflect.DeepEqual(vets, []string{"block|vet ./..."}) {
				t.Errorf("go vet calls=%q, want one whole-module go vet ./...", vets)
			}
			row := f.done(t)
			if row["scope"] != "changed" || row["reason"] != tc.reason || !reflect.DeepEqual(row["packages"], tc.packages) {
				t.Errorf("gate.done scope=%v reason=%v packages=%v, want changed %s %v: %v", row["scope"], row["reason"], row["packages"], tc.reason, tc.packages, row)
			}
			if _, ok := row["test_files"]; ok {
				t.Errorf("gate.done has test_files %v, want packages only", row["test_files"])
			}
		})
	}
}

func TestB667GoModChangeRunsFullSuite(t *testing.T) {
	f := newB667GateFixture(t, nil, map[string]*string{"go.mod": b667Body("module example.invalid/m\n\ngo 1.23\n"), "a/a.go": b667Body("package a\n\nvar X = 1\n")})
	code, out := f.gates(t)
	if code != 0 {
		t.Fatalf("exit=%d, want 0: %s", code, out)
	}
	if calls := b667Calls(t, f.log); !reflect.DeepEqual(calls, []string{"block|vet ./...", "block|test ./... -count=1"}) {
		t.Errorf("calls=%q, want vet then the full go test ./...", calls)
	}
	if row := f.done(t); row["scope"] != "full" || row["reason"] != "go_mod" {
		t.Errorf("gate.done scope=%v reason=%v, want full go_mod", row["scope"], row["reason"])
	}
}

func TestB667ConfigOffKeepsFileSelection(t *testing.T) {
	f := newB667GateFixture(t, map[string]any{"go_changed_packages": false}, map[string]*string{"a/a.go": b667Body("package a\n\nvar X = 1\n")})
	code, out := f.gates(t)
	if code != 0 {
		t.Fatalf("exit=%d, want 0: %s", code, out)
	}
	for _, c := range b667Calls(t, f.log) {
		if strings.HasPrefix(c, "block|test ./a ") {
			t.Errorf("go_changed_packages: false still narrowed to packages: %q", c)
		}
	}
	if row := f.done(t); row["reason"] == "go_packages" || row["packages"] != nil {
		t.Errorf("gate.done=%v, want no go package scope", row)
	}
}

// newB667BatchFixture is a Go repository with closed blocks B1 (a/a.go) and
// B2 (b/b.go), each in its own worktree, ready for a batch land.
func newB667BatchFixture(t *testing.T) (*landBatchFixture, string) {
	t.Helper()
	f := &landBatchFixture{t: t, home: t.TempDir(), repo: t.TempDir()}
	t.Setenv("HOME", f.home)
	t.Setenv("CODE_FORGE_NODE", "/nonexistent")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	t.Setenv("CODE_FORGE_TEST_QUEUE", "off")
	goBin, log := b667FakeGoBin(t, f.home)
	t.Setenv("B667_GO_LOG", log)
	oldVersion := reviewWaitNodeVersion
	reviewWaitNodeVersion = "v24.22.1"
	t.Cleanup(func() { reviewWaitNodeVersion = oldVersion })
	f.git(f.repo, "init", "-qb", "main")
	f.git(f.repo, "config", "user.name", "Fixture")
	f.git(f.repo, "config", "user.email", "f@example.invalid")
	f.git(f.repo, "config", "commit.gpgsign", "false")
	f.write(f.repo, ".gitignore", ".code-forge/\n")
	f.write(f.repo, "CHANGELOG.md", landChangelog)
	for name, body := range b667GoBase {
		f.write(f.repo, name, body)
	}
	cfg := landTestConfig([]string{goBin, "test", "./...", "-count=1"})
	cfg["gates"].(map[string]any)["lint"] = []string{goBin, "vet", "./..."}
	cfg["gates"].(map[string]any)["rerun_failed"] = 0
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.write(f.repo, ".code-forge.yml", string(data))
	f.git(f.repo, "add", ".")
	f.git(f.repo, "commit", "-qm", "base")
	f.base = f.git(f.repo, "rev-parse", "HEAD")
	blocks := ledger.Object{}
	for i, file := range []string{"a/a.go", "b/b.go"} {
		id := fmt.Sprintf("B%d", i+1)
		tree := filepath.Join(f.repo, ".code-forge/worktrees", id)
		f.git(f.repo, "worktree", "add", "-qb", "block-"+id, tree, f.base)
		f.write(tree, file, fmt.Sprintf("package %s\n\nvar %s = 1\n", filepath.Base(filepath.Dir(file)), id))
		f.git(tree, "add", ".")
		f.git(tree, "commit", "-qm", id)
		blocks.Set(id, ledger.Object{{Key: "status", Value: "closed"}, {Key: "owned_files", Value: []any{file}}, {Key: "kind", Value: "code"}, {Key: "base_sha", Value: f.base}, {Key: "base_branch", Value: "main"}, {Key: "test_files", Value: []any{}}, {Key: "worktree", Value: ledger.Object{{Key: "path", Value: tree}, {Key: "branch", Value: "block-" + id}, {Key: "base_sha", Value: f.base}}}})
	}
	record := ledger.Object{{Key: "run_id", Value: "r-batch-unit"}, {Key: "project", Value: "fixture"}, {Key: "workspace", Value: f.repo}, {Key: "status", Value: "active"}, {Key: "blocks", Value: blocks}, {Key: "orphans", Value: []any{}}}
	if err := state.SaveRun(f.home, record); err != nil {
		t.Fatal(err)
	}
	f.write(f.home, ".code-forge/runs/r-batch-unit.key", string(make([]byte, 32)))
	return f, log
}

func b667TestCalls(calls []string) (block, merge []string) {
	for _, c := range calls {
		if strings.HasPrefix(c, "block|test") {
			block = append(block, strings.TrimPrefix(c, "block|"))
		}
		if strings.HasPrefix(c, "merge|test") {
			merge = append(merge, strings.TrimPrefix(c, "merge|"))
		}
	}
	return
}

func TestB667BatchRunsOneFullSuiteAtEnd(t *testing.T) {
	f, log := newB667BatchFixture(t)
	bare := f.bareRemote()
	code, output := f.landWith([]string{"--push"}, "B1", "B2")
	if code != 0 {
		t.Fatalf("exit=%d, want 0: %s", code, output)
	}
	block, merge := b667TestCalls(b667Calls(t, log))
	if want := []string{"test ./a -count=1", "test ./b -count=1"}; !reflect.DeepEqual(block, want) {
		t.Errorf("block go test calls=%q, want only the changed packages %q", block, want)
	}
	if want := []string{"test ./... -count=1"}; !reflect.DeepEqual(merge, want) {
		t.Errorf("merge-gate go test calls=%q, want one full suite %q", merge, want)
	}
	if strings.Count(output, "merge gate: full go test suite once for the batch (B667)\n") != 1 {
		t.Errorf("want one full-suite merge gate line: %s", output)
	}
	f.checkMain([]string{"B1", "B2"})
	if got, want := f.git(bare, "rev-parse", "refs/heads/main"), f.git(f.repo, "rev-parse", "main"); got != want {
		t.Errorf("remote main=%s, want pushed %s", got, want)
	}
}

func TestB667BatchRedFullSuiteLandsAndPushesNothing(t *testing.T) {
	f, log := newB667BatchFixture(t)
	bare := f.bareRemote()
	before := f.git(bare, "rev-parse", "refs/heads/main")
	f.write(f.home, filepath.Base(log)+".red-full", "")
	code, output := f.landWith([]string{"--push"}, "B1", "B2")
	if code == 0 {
		t.Fatalf("exit=0 with a red full suite, want a failed batch: %s", output)
	}
	block, merge := b667TestCalls(b667Calls(t, log))
	if want := []string{"test ./a -count=1", "test ./b -count=1"}; !reflect.DeepEqual(block, want) {
		t.Errorf("block go test calls=%q, want %q", block, want)
	}
	for _, m := range merge {
		if m != "test ./... -count=1" {
			t.Errorf("merge-gate call %q, want only full go test ./...", m)
		}
	}
	if len(merge) == 0 || strings.Contains(output, " landed\n") && !strings.Contains(output, "not landed") {
		t.Errorf("merge calls=%q output=%s, want a full suite and no landed block", merge, output)
	}
	if strings.Contains(output, "block B1 landed") || strings.Contains(output, "block B2 landed") || strings.Contains(output, "pushed ") {
		t.Errorf("a red full suite landed or pushed: %s", output)
	}
	if got := f.git(f.repo, "rev-parse", "main"); got != f.base {
		t.Errorf("main=%s, want unchanged %s", got, f.base)
	}
	if got := f.git(bare, "rev-parse", "refs/heads/main"); got != before {
		t.Errorf("remote main=%s, want unchanged %s", got, before)
	}
	for _, row := range f.rows("land.batch") {
		if landed, _ := row["landed"].([]any); len(landed) != 0 {
			t.Errorf("land.batch landed=%v, want none", landed)
		}
	}
	for _, event := range []string{"land.push", "land.pushed", "push"} {
		if rows := f.rows(event); len(rows) != 0 {
			t.Errorf("%s rows=%v, want none", event, rows)
		}
	}
}
