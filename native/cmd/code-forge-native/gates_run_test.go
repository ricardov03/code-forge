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

	"github.com/ricardov03/code-forge/native/gitx"
)

// Exercise the public router in a fresh process; a handover must never replace
// the test process itself. This helper also works against the pre-port router.
func TestGatesProcess(t *testing.T) {
	if os.Getenv("CF_GATES_PROCESS") != "1" {
		return
	}
	if version := os.Getenv("CF_GATES_NODE_VERSION"); version != "" {
		reviewWaitNodeVersion = version
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("CF_GATES_ARGS")), &args); err != nil {
		os.Exit(99)
	}
	code, _ := run(args)
	os.Exit(code)
}

func TestGatesChangedTextOnlyAffectedGates(t *testing.T) {
	for _, baseline := range []string{"missing", "stale", "shared", "unsafe-move", "oversized"} {
		t.Run(baseline, func(t *testing.T) {
			root := t.TempDir()
			repo, home := filepath.Join(root, "repo"), filepath.Join(root, "home")
			write := func(file, body string) {
				t.Helper()
				p := filepath.Join(repo, file)
				if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			git := func(args ...string) {
				t.Helper()
				cmd := exec.Command("git", args...)
				cmd.Dir = repo
				cmd.Env = gitx.ChildEnv()
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v: %s", args, err, out)
				}
			}
			write(".code-forge.yml", `{"gates":{"test":["/bin/echo","suite","{test_files}"],"lint":["/bin/echo","lint","{changed_files}"],"types":null,"format":["/bin/echo","format","{changed_files}"],"rerun_failed":0}}`)
			write("README.md", "before\n")
			write("engine.go", "// before\n")
			if baseline == "oversized" {
				// With README.md and engine.go the base holds 201 owned files.
				for i := 0; i < 199; i++ {
					write(fmt.Sprintf("docs/n%03d.md", i), "before\n")
				}
			}
			git("init", "-q", "-b", "main")
			git("add", ".")
			git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "base")
			git("checkout", "-qb", "block")
			block := map[string]any{"status": "open", "kind": "code", "owned_files": []string{"README.md", "engine.go"}, "test_files": []string{}}
			if baseline != "shared" {
				block["base_branch"] = "main"
			}
			if baseline == "oversized" {
				block["owned_files"] = []string{"README.md", "engine.go", "docs/"}
			}
			dir := filepath.Join(home, ".code-forge", "runs")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(map[string]any{"run_id": "fixture", "workspace": repo, "status": "active", "project": "fixture", "blocks": map[string]any{"B1": block}})
			if err := os.WriteFile(filepath.Join(dir, "fixture.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			type gateResult struct {
				Gate, Reason, Scope, Stdout string
				Skipped, OK, Reused         bool
				Argv                        []string
				FullStamp                   string `json:"full_stamp"`
			}
			run := func(changed bool) []gateResult {
				t.Helper()
				exe, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				args := []string{"gates", "run", "--run", "fixture", "--block", "B1"}
				if changed {
					args = append(args, "--changed")
				}
				encoded, _ := json.Marshal(args)
				cmd := exec.Command(exe, "-test.run=^TestGatesProcess$")
				cmd.Dir = repo
				cmd.Env = append(envWithoutGit(), "HOME="+home, "CODE_FORGE_NO_ERROR_LOG=1", "CODE_FORGE_NODE=/nonexistent-code-forge-node", "CF_GATES_PROCESS=1", "CF_GATES_NODE_VERSION=v24.22.1", "CF_GATES_ARGS="+string(encoded))
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("gates run: %v: %s", err, out)
				}
				var result struct {
					AllOK   bool `json:"allOk"`
					Results []gateResult
				}
				if err := json.Unmarshal(out, &result); err != nil {
					t.Fatalf("gate JSON: %v: %s", err, out)
				}
				if !result.AllOK || len(result.Results) != 4 {
					t.Fatalf("want allOk and exactly four gates, got %s", out)
				}
				return result.Results
			}
			if baseline == "oversized" {
				// The green ledger baseline holds 200 files; the deleted doc makes
				// the base baseline 201 entries, which validation rejects.
				git("rm", "-q", "docs/n000.md")
				git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "drop doc")
			}
			if baseline != "missing" {
				run(baseline == "unsafe-move")
			}
			if baseline == "unsafe-move" || baseline == "oversized" {
				if baseline == "unsafe-move" {
					// A code commit moves the recorded test base unsafely.
					write("unrelated.go", "// moved base\n")
					git("add", "unrelated.go")
					git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "move base")
					git("branch", "-f", "main", "HEAD")
				}
				write("README.md", "changed\n")
				test := run(true)[0]
				if test.Gate != "test" || !test.OK || test.Skipped || test.Reused || test.Reason == "text_only" || test.Scope != "full" || test.Stdout != "suite\n" {
					t.Fatalf("%s: want exactly one full suite execution instead of text_only, got %+v", baseline, test)
				}
				return
			}
			if baseline == "stale" {
				write("unrelated.go", "// moved base\n")
				git("add", "unrelated.go")
				git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "move base")
				git("branch", "-f", "main", "HEAD")
			}
			write("README.md", "changed\n")
			results := run(true)
			if test := results[0]; test.Gate != "test" || !test.OK || !test.Skipped || test.Reused || test.Reason != "text_only" || test.Stdout != "" || test.FullStamp != "" {
				t.Fatalf("test gate: want one text_only skip without execution, reuse or full stamp, got %+v", test)
			}
			for index, name := range map[int]string{1: "lint", 3: "format"} {
				gate := results[index]
				if gate.Gate != name || gate.Skipped || !gate.OK || !reflect.DeepEqual(gate.Argv, []string{"/bin/echo", name, "README.md"}) {
					t.Fatalf("%s: want exactly README.md passed to the executed gate, got %+v", name, gate)
				}
			}
			if types := results[2]; types.Gate != "types" || !types.Skipped {
				t.Fatalf("types: want one disabled gate, got %+v", types)
			}
			// A subsequent code edit must invalidate the text-only decision.
			write("engine.go", "// changed\n")
			if test := run(true)[0]; test.Skipped || test.Reused || test.Reason == "text_only" || test.Stdout != "suite\n" || !reflect.DeepEqual(test.Argv, []string{"/bin/echo", "suite"}) {
				t.Fatalf("mixed code change: want exactly one full suite execution, got %+v", test)
			}
		})
	}
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
		cmd.Env = append(envWithoutGit(), "HOME="+home, "CODE_FORGE_NO_ERROR_LOG=1", "CODE_FORGE_NODE=/nonexistent-code-forge-node", "CF_GATES_PROCESS=1", "CF_GATES_ARGS="+string(encoded))
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

// B335: gates run --run --block expands {test_classes} in gates.test to the
// block's own classes, and drops the item (full suite) for a block with none.
func TestGatesRunExpandsTestClasses(t *testing.T) {
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
	config := `{"gates":{"test":["/bin/sh","-c","printf '[%s]' \"$@\"","sh","run","-Pclass={test_classes}"],"lint":null,"types":null,"format":null,"rerun_failed":0,` +
		`"test_class_map":[{"glob":"app/src/androidTest/java/**/*Test.kt","strip_prefix":"app/src/androidTest/java/","strip_suffix":".kt"}]}}`
	must(os.WriteFile(filepath.Join(repo, ".code-forge.yml"), []byte(config), 0600))
	record := map[string]any{"run_id": "fixture", "workspace": repo, "status": "active", "project": "fixture", "blocks": map[string]any{
		"B1": map[string]any{"status": "open", "kind": "code", "owned_files": []string{"app/src/androidTest/java/a/b/FooTest.kt", "app/src/main/java/a/b/Foo.kt", "app/src/androidTest/java/a/b/BarTest.kt"}},
		"B2": map[string]any{"status": "open", "kind": "code", "owned_files": []string{"app/src/main/java/a/b/Foo.kt"}},
	}}
	data, _ := json.Marshal(record)
	must(os.WriteFile(filepath.Join(home, ".code-forge", "runs", "fixture.json"), data, 0600))
	exe, err := os.Executable()
	must(err)
	for _, tc := range []struct{ block, stdout string }{{"B1", "[run][-Pclass=a.b.FooTest,a.b.BarTest]"}, {"B2", "[run]"}} {
		encoded, _ := json.Marshal([]string{"gates", "run", "--run", "fixture", "--block", tc.block})
		cmd := exec.Command(exe, "-test.run=^TestGatesProcess$")
		cmd.Dir = repo
		cmd.Env = append(envWithoutGit(), "HOME="+home, "CODE_FORGE_NO_ERROR_LOG=1", "CODE_FORGE_NODE=/nonexistent-code-forge-node", "CF_GATES_PROCESS=1", "CF_GATES_ARGS="+string(encoded))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v: %s", tc.block, err, out)
		}
		var result struct {
			AllOK   bool `json:"allOk"`
			Results []struct {
				Gate   string
				Stdout string
			}
		}
		must(json.Unmarshal(out, &result))
		if !result.AllOK || len(result.Results) != 4 || result.Results[0].Gate != "test" || result.Results[0].Stdout != tc.stdout {
			t.Fatalf("%s: want test stdout %q; got %s", tc.block, tc.stdout, out)
		}
	}
}
