package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/gates"
)

func b1009Gate(t *testing.T, f b667GateFixture, flags ...string) (int, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{"gates", "run", "--run", "fixture", "--block", "B1", "--changed", "--json"}, flags...)
	encoded, _ := json.Marshal(args)
	cmd := exec.Command(exe, "-test.run=^TestGatesProcess$")
	cmd.Dir = f.repo
	cmd.Env = append(slices.DeleteFunc(os.Environ(), func(v string) bool { return strings.HasPrefix(v, "CODE_FORGE_RUN_ID=") }), "HOME="+f.home, "B667_GO_LOG="+f.log, "CODE_FORGE_TEST_QUEUE=off", "CODE_FORGE_NO_ERROR_LOG=1", "CF_GATES_PROCESS=1", "CF_GATES_ARGS="+string(encoded))
	out, err := cmd.CombinedOutput()
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			return e.ExitCode(), string(out)
		}
		t.Fatal(err)
	}
	return 0, string(out)
}

var b1009Base = map[string]string{
	"go.mod":          "module example.invalid/m\n\ngo 1.22\n",
	"a/a.go":          "package a\n",
	"a/a_test.go":     "package a\nimport \"testing\"\nfunc TestOld(t *testing.T) {}\nfunc TestShared(t *testing.T) {}\n",
	"a/other_test.go": "package a\nimport \"testing\"\nfunc TestB1Existing(t *testing.T) {}\nfunc TestAcceptance(t *testing.T) {}\nfunc TestUnrelated(t *testing.T) {}\n",
	"b/b.go":          "package b\n",
	"b/b_test.go":     "package b\nimport \"testing\"\nfunc TestShared(t *testing.T) {}\nfunc TestB1Other(t *testing.T) {}\nfunc TestB10Wrong(t *testing.T) {}\n",
}

func b1009Acceptance(t *testing.T, f b667GateFixture, names ...string) {
	t.Helper()
	path := filepath.Join(f.home, ".code-forge", "runs", "fixture.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var run map[string]any
	if err := json.Unmarshal(data, &run); err != nil {
		t.Fatal(err)
	}
	block := run["blocks"].(map[string]any)["B1"].(map[string]any)
	block["acceptance"] = []any{map[string]any{"clause": "named", "tests": names}}
	data, _ = json.Marshal(run)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestB1009ChangedGateRunsTestsByName(t *testing.T) {
	f := newB667GateFixtureOn(t, b1009Base, nil, map[string]*string{
		"a/a_test.go": b667Body(b1009Base["a/a_test.go"] + "func TestNew(t *testing.T) {}\n"),
		"b/b.go":      b667Body("package b\nvar X = 1\n"),
	})
	b1009Acceptance(t, f, "TestAcceptance")
	code, out := b1009Gate(t, f)
	if code != 0 {
		t.Fatalf("gate exit %d: %s", code, out)
	}
	calls := strings.Join(b667Calls(t, f.log), "\n")
	for _, want := range []string{"-run ^(TestAcceptance|TestB1Existing|TestNew|TestOld|TestShared)$", "-run ^(TestB1Other)$", "-trimpath", "-count=1", "vet ./a ./b"} {
		if !strings.Contains(calls, want) {
			t.Errorf("calls missing %q:\n%s", want, calls)
		}
	}
	if strings.Contains(calls, "./...") || strings.Contains(calls, "TestUnrelated") || strings.Contains(calls, "TestB10Wrong") {
		t.Errorf("unrelated work: %s", calls)
	}
	row := f.done(t)
	if row["tests_only"] != "changed tests only (6 tests)" {
		t.Errorf("gate.done missing scope label: %v", row)
	}
	for _, value := range row["results"].([]any) {
		r := value.(map[string]any)
		if r["gate"] == "test" && r["tests_only"] != row["tests_only"] {
			t.Errorf("test summary: %v", r)
		}
	}
}

func TestB1009NamesFromChangedFilesBlockIDAndAcceptance(t *testing.T) {
	f := newB667GateFixtureOn(t, b1009Base, nil, map[string]*string{"a/a.go": b667Body("package a\nvar X = 1\n")})
	b1009Acceptance(t, f, "a/a_test.go", "TestAcceptance")
	code, out := b1009Gate(t, f)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	names := f.done(t)["test_names"]
	want := []any{"TestAcceptance", "TestB1Existing", "TestB1Other", "TestOld", "TestShared"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("names %v want %v", names, want)
	}
}

func TestB1009NoNamesRunsDeclaredPackageGate(t *testing.T) {
	base := map[string]string{"go.mod": b1009Base["go.mod"], "a/a.go": "package a\n", "a/a_test.go": "package a\nimport \"testing\"\nfunc TestUnrelated(t *testing.T) {}\n"}
	f := newB667GateFixtureOn(t, base, nil, map[string]*string{"a/a.go": b667Body("package a\nvar X = 1\n")})
	code, out := b1009Gate(t, f)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	calls := strings.Join(b667Calls(t, f.log), "\n")
	if strings.Contains(calls, "-run") || !strings.Contains(calls, "block|test ./a -count=1") || strings.Contains(calls, "block|test ./...") {
		t.Fatalf("want declared package test command: %s", calls)
	}
	if f.done(t)["tests_only"] != nil {
		t.Fatal(f.done(t))
	}
}

func TestB1009MissingNamesFallsBackForMixedPackagesAndWrappers(t *testing.T) {
	for _, wrapper := range []bool{false, true} {
		t.Run(fmt.Sprint(wrapper), func(t *testing.T) {
			base := map[string]string{}
			for file, body := range b1009Base {
				base[file] = strings.ReplaceAll(body, "TestB1Other", "TestOther")
			}
			f := newB667GateFixtureOn(t, base, nil, map[string]*string{"a/a.go": b667Body("package a\nvar X = 1\n"), "b/b.go": b667Body("package b\nvar X = 1\n")})
			if wrapper {
				script := filepath.Join(t.TempDir(), "go")
				b667WriteFile(t, script, "#!/bin/sh\nprintf 'fallback|%s|%s\\n' \"$CODE_FORGE_TEST_RUN\" \"$*\" >> \"$B667_GO_LOG\"\nexit 7\n")
				if err := os.Chmod(script, 0755); err != nil {
					t.Fatal(err)
				}
				cfg := map[string]any{"gates": map[string]any{"test": []string{script, "run", "./wrapper", "--flag", "kept", "{test_files}"}, "lint": nil, "types": nil, "format": nil, "rerun_failed": 0}}
				data, _ := json.Marshal(cfg)
				b667WriteFile(t, filepath.Join(f.repo, ".code-forge.yml"), string(data))
			}
			t.Setenv("CODE_FORGE_TEST_RUN", "^(Inherited)$")
			code, out := b1009Gate(t, f)
			calls := strings.Join(b667Calls(t, f.log), "\n")
			if strings.Contains(calls, "-run") || strings.Contains(calls, "Inherited") || f.done(t)["tests_only"] != nil {
				t.Fatalf("incomplete names used: exit %d %s; calls %s", code, out, calls)
			}
			if wrapper {
				if code != 1 || !strings.Contains(calls, "fallback||run ./wrapper --flag kept") {
					t.Fatalf("declared wrapper failure lost: exit %d %s; calls %s", code, out, calls)
				}
			} else if code != 0 || !strings.Contains(calls, "block|test ./a ./b -count=1") {
				t.Fatalf("declared package command lost: exit %d %s; calls %s", code, out, calls)
			}
		})
	}
}

func TestB1009NoNamesRunsFailingDeclaredTest(t *testing.T) {
	base := map[string]string{"go.mod": b1009Base["go.mod"], "a/a.go": "package a\n", "a/a_test.go": "package a\nimport \"testing\"\nfunc TestUnrelated(t *testing.T) { t.Fatal(\"declared test ran\") }\n"}
	f := newB667GateFixtureOn(t, base, nil, map[string]*string{"a/a.go": b667Body("package a\nvar X = 1\n")})
	data, err := os.ReadFile(filepath.Join(f.repo, ".code-forge.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	bin := cfg["gates"].(map[string]any)["test"].([]any)[0].(string)
	body := strings.TrimSuffix(b667FakeGo, "exit 0\n") + "if [ \"$1\" = test ]; then exec \"$B667_REAL_GO\" \"$@\"; fi\nexit 0\n"
	if err := os.WriteFile(bin, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	code, out := b1009Gate(t, f)
	if code != 1 || !strings.Contains(out, "declared test ran") {
		t.Fatalf("empty names hid a failing declared test: exit %d %s", code, out)
	}
}

func TestB1009NoNamesWithoutWrapperFilesFailsLoudly(t *testing.T) {
	base := map[string]string{"go.mod": b1009Base["go.mod"], "a/a.go": "package a\n"}
	f := newB667GateFixtureOn(t, base, nil, map[string]*string{"a/a.go": b667Body("package a\nvar X = 1\n")})
	script := filepath.Join(t.TempDir(), "go")
	b667WriteFile(t, script, "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(script, 0755); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{"gates": map[string]any{"test": []string{script, "run", "./wrapper", "{test_files}"}, "lint": nil, "types": nil, "format": nil}}
	data, _ := json.Marshal(cfg)
	b667WriteFile(t, filepath.Join(f.repo, ".code-forge.yml"), string(data))
	code, out := b1009Gate(t, f)
	if code != 1 || !strings.Contains(out, "no Go test names or test files selected") {
		t.Fatalf("empty wrapper selection silently passed: exit %d %s", code, out)
	}
}

func TestB1009PackagesFlagRunsWholePackage(t *testing.T) {
	f := newB667GateFixtureOn(t, b1009Base, nil, map[string]*string{"a/a.go": b667Body("package a\nvar X = 1\n")})
	code, out := b1009Gate(t, f, "--packages")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	calls := strings.Join(b667Calls(t, f.log), "\n")
	if !strings.Contains(calls, "block|test ./a -count=1") || strings.Contains(calls, "-run") {
		t.Fatal(calls)
	}
	if f.done(t)["tests_only"] != nil {
		t.Fatal(f.done(t))
	}
}

func TestB1009LandDoesNotReuseNamesGate(t *testing.T) {
	row := map[string]any{"event": "gate.done", "all_ok": true, "tree": "abc", "scope": "changed", "tests_only": "changed tests only (1 tests)", "results": []any{map[string]any{"gate": "test", "ok": true}}}
	if _, ok := landGateRow(row); ok {
		t.Fatal("names-only gate reused for merge")
	}
	delete(row, "tests_only")
	if _, ok := landGateRow(row); !ok {
		t.Fatal("whole-package gate no longer reusable")
	}
}

func TestB1009WrapperReceivesEachPackageFilter(t *testing.T) {
	f := newB667GateFixtureOn(t, b1009Base, nil, map[string]*string{"a/a.go": b667Body("package a\nvar X = 1\n"), "b/b.go": b667Body("package b\nvar X = 1\n")})
	script := filepath.Join(t.TempDir(), "go")
	b667WriteFile(t, script, "#!/bin/sh\nshift 2\nprintf 'wrapper|%s|%s\\n' \"$CODE_FORGE_TEST_RUN\" \"$*\" >> \"$B667_GO_LOG\"\n")
	if err := os.Chmod(script, 0755); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{"gates": map[string]any{"test": []string{script, "run", "./wrapper", "{test_files}"}, "lint": nil, "types": nil, "format": nil, "rerun_failed": 0}}
	data, _ := json.Marshal(cfg)
	b667WriteFile(t, filepath.Join(f.repo, ".code-forge.yml"), string(data))
	code, out := b1009Gate(t, f)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	calls := strings.Join(b667Calls(t, f.log), "\n")
	if !strings.Contains(calls, "wrapper|^(TestB1Existing)$|a/") || !strings.Contains(calls, "wrapper|^(TestB1Other)$|b/") {
		t.Fatalf("wrapper filters: %s", calls)
	}
	t.Setenv("CODE_FORGE_TEST_RUN", "^(Inherited)$")
	code, out = b1009Gate(t, f, "--packages")
	if code != 0 {
		t.Fatalf("packages exit %d: %s", code, out)
	}
	calls = strings.Join(b667Calls(t, f.log), "\n")
	if strings.Contains(calls, "wrapper|^(Inherited)$|") || !strings.Contains(calls, "wrapper||") {
		t.Fatalf("--packages retained a by-name filter: %s", calls)
	}
}

func TestB1009RealGoSkipsUnrelatedAndFailsCompile(t *testing.T) {
	base := map[string]string{"go.mod": b1009Base["go.mod"], "a/a.go": "package a\n", "a/a_test.go": "package a\nimport \"testing\"\nfunc TestUnrelated(t *testing.T) { t.Fatal(\"unrelated test ran\") }\n"}
	base["a/a_test.go"] += "func TestNew(t *testing.T) {}\n"
	f := newB667GateFixtureOn(t, base, nil, map[string]*string{"a/a.go": b667Body("package a\nvar X = 1\n")})
	b667WriteFile(t, filepath.Join(f.repo, "a/a.go"), "package a\nvar X = 1\n")
	b1009Acceptance(t, f, "TestNew")
	data, err := os.ReadFile(filepath.Join(f.repo, ".code-forge.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err = json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	argv := cfg["gates"].(map[string]any)["test"].([]any)
	bin := argv[0].(string)
	// Replace the fake's test exit with the real toolchain; vet remains a fake.
	body := strings.TrimSuffix(b667FakeGo, "exit 0\n") + "if [ \"$1\" = test ]; then exec \"$B667_REAL_GO\" \"$@\"; fi\nexit 0\n"
	if err := os.WriteFile(bin, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	code, out := b1009Gate(t, f)
	if code != 0 {
		t.Fatalf("named test exit %d: %s", code, out)
	}
	b667WriteFile(t, filepath.Join(f.repo, "a/a.go"), "package a\nvar X = undefinedSymbol\n")
	code, out = b1009Gate(t, f)
	if code == 0 || !strings.Contains(out, "undefinedSymbol") {
		t.Fatalf("compile error must fail even with selected names: exit %d %s", code, out)
	}
}

func TestB1009PackageFailureKeepsEvidenceAndRunsRemaining(t *testing.T) {
	f := newB667GateFixtureOn(t, b1009Base, nil, map[string]*string{"a/a.go": b667Body("package a\nvar X = 1\n"), "b/b.go": b667Body("package b\nvar X = 1\n")})
	data, err := os.ReadFile(filepath.Join(f.repo, ".code-forge.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err = json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	bin := cfg["gates"].(map[string]any)["test"].([]any)[0].(string)
	body := strings.TrimSuffix(b667FakeGo, "exit 0\n") + "if [ \"$1\" = test ]; then for a in \"$@\"; do [ \"$a\" = ./a ] && exit 7; done; fi\nexit 0\n"
	if err := os.WriteFile(bin, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	code, out := b1009Gate(t, f)
	if code != 1 {
		t.Fatalf("exit %d: %s", code, out)
	}
	calls := strings.Join(b667Calls(t, f.log), "\n")
	if !strings.Contains(calls, "test ./b -run") {
		t.Fatalf("remaining named package never ran: %s", calls)
	}
	// JSON is stdout's final line after the scope notice on stderr.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var result map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &result); err != nil {
		t.Fatalf("JSON: %v %s", err, out)
	}
	test := result["results"].([]any)[0].(map[string]any)
	if test["code"] != float64(7) || !strings.Contains(strings.Join(gates.Strings(test["argv"]), " "), "./a") {
		t.Fatalf("failure evidence lost: %v", test)
	}
}

func TestB1009KeepsGoFlagValuesAndReplacesFilter(t *testing.T) {
	f := newB667GateFixtureOn(t, b1009Base, nil, map[string]*string{"a/a.go": b667Body("package a\nvar X = 1\n")})
	data, err := os.ReadFile(filepath.Join(f.repo, ".code-forge.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err = json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	g := cfg["gates"].(map[string]any)
	argv := g["test"].([]any)
	g["test"] = append(argv, "-exec", "runner", "-cpuprofile", "profile.out", "-run=TestUnrelated", "-count", "9")
	data, _ = json.Marshal(cfg)
	b667WriteFile(t, filepath.Join(f.repo, ".code-forge.yml"), string(data))
	code, out := b1009Gate(t, f)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	calls := strings.Join(b667Calls(t, f.log), "\n")
	if !strings.Contains(calls, "-exec runner -cpuprofile profile.out") || strings.Contains(calls, "TestUnrelated") || strings.Contains(calls, "-count 9") {
		t.Fatal(calls)
	}
}
