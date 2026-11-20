package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGo puts a `go` on PATH that writes its arguments to a log and exits with code.
func fakeGo(t *testing.T, code string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "go.log")
	script := "#!/bin/sh\necho \"$*\" >> '" + log + "'\nexit " + code + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	return log
}

func readLog(t *testing.T, log string) string {
	t.Helper()
	data, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestB760GateTestsThePackagesOfTheSelection: the selected files (repository paths) become the
// packages that hold them, one go test run from native/.
func TestB760GateTestsThePackagesOfTheSelection(t *testing.T) {
	log := fakeGo(t, "0")
	var out, errb bytes.Buffer
	code := run([]string{
		"native/cmd/code-forge-native/b1_test.go",
		"native/cmd/code-forge-native/b2_test.go",
		"native/gates/scope_test.go",
		"native/cmd/code-forge-native/testdata/b9/x.go",
		"native/vendor/gopkg.in/yaml.v3/yaml.go",
		"docs/native-engine.md",
		"test/fixtures/a.mjs",
	}, &out, &errb)
	if code != 0 || errb.Len() != 0 {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
	want := "test -count=1 -timeout 75m ./cmd/code-forge-native ./gates\n"
	if got := readLog(t, log); got != want {
		t.Fatalf("go ran %q, want %q", got, want)
	}
	if !strings.Contains(out.String(), "cf-gotest: go "+strings.TrimSuffix(want, "\n")) {
		t.Fatalf("stdout %q", out.String())
	}
}

// TestB760GateNoFileRunsEveryPackage: no file is the full suite.
func TestB760GateNoFileRunsEveryPackage(t *testing.T) {
	log := fakeGo(t, "0")
	var out, errb bytes.Buffer
	if code := run(nil, &out, &errb); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := readLog(t, log); got != "test -count=1 -timeout 75m ./...\n" {
		t.Fatalf("go ran %q", got)
	}
}

// TestB760GateNoGoPackageRunsNothing: files but no Go package start no go process and pass.
func TestB760GateNoGoPackageRunsNothing(t *testing.T) {
	log := fakeGo(t, "0")
	var out, errb bytes.Buffer
	if code := run([]string{"docs/a.md", "native/README.md"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := readLog(t, log); got != "" {
		t.Fatalf("go ran %q", got)
	}
	if out.String() != "cf-gotest: no Go package in the selection (2 files)\n" {
		t.Fatalf("stdout %q", out.String())
	}
}

// TestB760GateFailingTestsFailTheGate: go test's exit code is the gate's.
func TestB760GateFailingTestsFailTheGate(t *testing.T) {
	fakeGo(t, "1")
	var out, errb bytes.Buffer
	if code := run([]string{"native/gates/scope_test.go"}, &out, &errb); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
}

// fakeGoEnv is fakeGo that also logs the GOFLAGS the go child sees, one line per run.
func fakeGoEnv(t *testing.T, code string) (argLog, envLog string) {
	t.Helper()
	dir := t.TempDir()
	argLog, envLog = filepath.Join(dir, "go.log"), filepath.Join(dir, "env.log")
	script := "#!/bin/sh\necho \"$*\" >> '" + argLog + "'\necho \"GOFLAGS=$GOFLAGS\" >> '" + envLog + "'\nexit " + code + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	return argLog, envLog
}

// TestB952GoRunsWithTrimpath: the go child sees -trimpath in GOFLAGS, so worktrees share the
// build cache; the caller's other GOFLAGS stay, and -trimpath is never added twice.
func TestB952GoRunsWithTrimpath(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", "GOFLAGS=-trimpath\n"},
		{"-mod=vendor", "GOFLAGS=-mod=vendor -trimpath\n"},
		{"-trimpath -mod=vendor", "GOFLAGS=-trimpath -mod=vendor\n"},
		{"-trimpath=false", "GOFLAGS=-trimpath=false\n"},
	} {
		_, envLog := fakeGoEnv(t, "0")
		t.Setenv("GOFLAGS", c.in)
		var out, errb bytes.Buffer
		if code := run([]string{"native/gates/scope_test.go"}, &out, &errb); code != 0 {
			t.Fatalf("GOFLAGS %q: exit %d %s", c.in, code, errb.String())
		}
		if got := readLog(t, envLog); got != c.want {
			t.Fatalf("GOFLAGS %q: go saw %q, want %q", c.in, got, c.want)
		}
	}
}
