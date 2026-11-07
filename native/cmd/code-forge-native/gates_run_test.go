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

// Exercise the public router in a fresh process; a handover must never replace
// the test process itself. This helper also works against the pre-port router.
func TestGatesProcess(t *testing.T) {
	if os.Getenv("CF_GATES_PROCESS") != "1" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("CF_GATES_ARGS")), &args); err != nil {
		os.Exit(99)
	}
	code, _ := run(args)
	os.Exit(code)
}

type changedTextFixture struct {
	t          *testing.T
	repo, home string
	block      map[string]any
}

func newChangedTextFixture(t *testing.T, files, tests []string) *changedTextFixture {
	t.Helper()
	f := &changedTextFixture{t: t, repo: filepath.Join(t.TempDir(), "repo"), home: filepath.Join(t.TempDir(), "home")}
	f.write(".code-forge.yml", `{"gates":{"test":["/bin/echo","suite","{test_files}"],"lint":["/bin/echo","lint","{changed_files}"],"types":null,"format":["/bin/echo","format","{changed_files}"],"rerun_failed":0}}`)
	for _, file := range files {
		f.write(file, "// before\n")
	}
	f.git("init", "-q", "-b", "main")
	f.git("add", ".")
	f.git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "base")
	f.git("checkout", "-qb", "block")
	f.block = map[string]any{"status": "open", "kind": "code", "owned_files": files, "test_files": tests, "base_branch": "main"}
	f.record()
	return f
}

func (f *changedTextFixture) write(file, body string) {
	f.t.Helper()
	p := filepath.Join(f.repo, file)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *changedTextFixture) git(args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = f.repo
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *changedTextFixture) record() {
	f.t.Helper()
	dir := filepath.Join(f.home, ".code-forge", "runs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		f.t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{"run_id": "fixture", "workspace": f.repo, "status": "active", "project": "fixture", "blocks": map[string]any{"B1": f.block}})
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fixture.json"), data, 0600); err != nil {
		f.t.Fatal(err)
	}
}

type changedTextResult struct {
	AllOK   bool `json:"allOk"`
	Results []struct {
		Gate, Reason, Scope, Stdout string
		Skipped, OK, Reused         bool
		Argv                        []string
	}
}

func (f *changedTextFixture) run(changed bool) changedTextResult {
	f.t.Helper()
	exe, err := os.Executable()
	if err != nil {
		f.t.Fatal(err)
	}
	args := []string{"gates", "run", "--run", "fixture", "--block", "B1"}
	if changed {
		args = append(args, "--changed")
	}
	encoded, _ := json.Marshal(args)
	cmd := exec.Command(exe, "-test.run=^TestGatesProcess$")
	cmd.Dir = f.repo
	cmd.Env = append(os.Environ(), "HOME="+f.home, "CODE_FORGE_NO_ERROR_LOG=1", "CODE_FORGE_NODE=/nonexistent-code-forge-node", "CF_GATES_PROCESS=1", "CF_GATES_ARGS="+string(encoded))
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("gates run: %v: %s", err, out)
	}
	var result changedTextResult
	if err := json.Unmarshal(out, &result); err != nil {
		f.t.Fatalf("gate JSON: %v: %s", err, out)
	}
	if !result.AllOK || len(result.Results) != 4 {
		f.t.Fatalf("want allOk and exactly four gates, got %s", out)
	}
	return result
}

func (f *changedTextFixture) check(textOnly bool, changed ...string) {
	f.t.Helper()
	r := f.run(true)
	test := r.Results[0]
	if test.Gate != "test" || !test.OK || test.Skipped != textOnly || test.Reused {
		f.t.Fatalf("test gate: want ok=true skipped=%v reused=false, got %+v", textOnly, test)
	}
	if textOnly {
		if test.Reason != "text_only" || test.Stdout != "" {
			f.t.Fatalf("test gate: want reason=text_only and no suite output, got %+v", test)
		}
		for _, index := range []int{1, 3} {
			gate := r.Results[index]
			want := append([]string{"/bin/echo", gate.Gate}, changed...)
			if gate.Skipped || !gate.OK || !reflect.DeepEqual(gate.Argv, want) {
				f.t.Fatalf("%s gate: want executed argv=%v, got %+v", gate.Gate, want, gate)
			}
		}
	} else if test.Scope != "full" || test.Stdout != "suite\n" || test.Reason == "text_only" || !reflect.DeepEqual(test.Argv, []string{"/bin/echo", "suite"}) {
		f.t.Fatalf("test gate: want full suite exactly once without file arguments, got %+v", test)
	}
}

func TestGatesChangedMarkdownWithoutFullBaseline(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "stale"}[stale], func(t *testing.T) {
			f := newChangedTextFixture(t, []string{"README.md", "guide.md"}, nil)
			if stale {
				f.run(false)
				f.write("engine.go", "// moved base\n")
				f.git("add", "engine.go")
				f.git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "move base")
				f.git("branch", "-f", "main", "HEAD")
			}
			f.write("README.md", "// changed\n")
			f.write("guide.md", "// changed\n")
			f.check(true, "README.md", "guide.md")
		})
	}
}

func TestGatesChangedMixedCodeRunsFullSuite(t *testing.T) {
	for _, code := range []string{"engine.go", "engine.mjs"} {
		t.Run(code, func(t *testing.T) {
			f := newChangedTextFixture(t, []string{"README.md", code}, nil)
			f.write("README.md", "// changed\n")
			f.check(true, "README.md")
			f.write(code, "// changed\n")
			f.check(false)
		})
	}
}

func TestGatesChangedTextTestInputsRunFullSuite(t *testing.T) {
	for _, input := range []struct {
		file  string
		tests []string
	}{
		{"test/fixtures/sample.txt", nil},
		{"checks/fixtures/sample.md", []string{"checks/"}},
		{"docs/checked.md", []string{"docs/checked.md"}},
		{"checks/sample.svg", []string{"checks/**"}},
	} {
		t.Run(input.file, func(t *testing.T) {
			f := newChangedTextFixture(t, []string{"README.md", input.file}, input.tests)
			// The input must never qualify for text-only skipping, even after a green run.
			f.write("README.md", "// changed\n")
			f.check(true, "README.md")
			f.write(input.file, "// changed\n")
			f.check(false)
		})
	}
}

func TestGatesChangedTextOnlyKinds(t *testing.T) {
	for _, ext := range []string{".md", ".mdx", ".txt", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".webp"} {
		t.Run(ext, func(t *testing.T) {
			file := "asset" + ext
			f := newChangedTextFixture(t, []string{file, "LICENSE"}, nil)
			f.write(file, "// changed\n")
			f.check(true, file)
			f.write("LICENSE", "// changed\n")
			f.check(false)
		})
	}
}

func TestGatesChangedTextOnlyUsesLastGreen(t *testing.T) {
	f := newChangedTextFixture(t, []string{"README.md", "engine.go"}, nil)
	f.run(false)
	f.write("README.md", "// changed\n")
	f.check(true, "README.md")
	// Moving code into the base must not hide changes since the last green run.
	f.write("engine.go", "// changed\n")
	f.git("add", "engine.go")
	f.git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "code moved into base")
	f.git("branch", "-f", "main", "HEAD")
	f.write("README.md", "// changed again\n")
	f.check(false)
}

func TestGatesNativeRoute(t *testing.T) {
	root := t.TempDir()
	repo, home := filepath.Join(root, "repo"), filepath.Join(root, "home")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(repo, 0755))
	must(os.MkdirAll(filepath.Join(home, ".code-forge", "runs"), 0700))
	config := `{"gates":{"test":["/bin/sh","-c","printf green"],"lint":null,"types":null,"format":null,"rerun_failed":0}}`
	must(os.WriteFile(filepath.Join(repo, ".code-forge.yml"), []byte(config), 0600))
	record := map[string]any{"run_id": "fixture", "workspace": repo, "status": "active", "project": "fixture", "blocks": map[string]any{"B1": map[string]any{"status": "open", "kind": "code", "owned_files": []string{}}}}
	data, _ := json.Marshal(record)
	must(os.WriteFile(filepath.Join(home, ".code-forge", "runs", "fixture.json"), data, 0600))
	exe, err := os.Executable()
	must(err)
	call := func(args ...string) (int, string) {
		t.Helper()
		encoded, _ := json.Marshal(args)
		cmd := exec.Command(exe, "-test.run=^TestGatesProcess$")
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "HOME="+home, "CODE_FORGE_NO_ERROR_LOG=1", "CODE_FORGE_NODE=/nonexistent-code-forge-node", "CF_GATES_PROCESS=1", "CF_GATES_ARGS="+string(encoded))
		out, err := cmd.CombinedOutput()
		code := 0
		if err != nil {
			if e, ok := err.(*exec.ExitError); ok {
				code = e.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		return code, string(out)
	}
	code, out := call("gates", "run", "--run", "fixture", "--block", "B1")
	if code != 0 {
		t.Fatalf("native green exit = %d, want 0: %s", code, out)
	}
	var result struct {
		AllOK   bool `json:"allOk"`
		Results []struct {
			Gate    string
			Skipped bool
			Stdout  string
		}
	}
	must(json.Unmarshal([]byte(out), &result))
	if !result.AllOK || len(result.Results) != 4 || result.Results[0].Stdout != "green" {
		t.Fatalf("green: want allOk, four results, and exact green output; got %s", out)
	}
	for _, flags := range [][]string{{"--timeout-ms"}, {"--timeout-ms", "NaN"}, {"--slug"}, {"--cwd"}} {
		args := append([]string{"gates", "run", "--run", "fixture", "--block", "B1"}, flags...)
		code, out := call(args...)
		if code != 2 || !strings.HasPrefix(out, "gates run: ") {
			t.Fatalf("%v: exit=%d output=%q, want native usage exit 2", flags, code, out)
		}
	}
	code, _ = call("gates", "detect")
	if code != 127 {
		t.Fatalf("detect handover exit=%d, want 127", code)
	}
}
