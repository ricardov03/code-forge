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
	quietMachine(t)
	return log
}

// quietMachine pins the load reading to an idle 8-core machine for the test.
func quietMachine(t *testing.T) {
	t.Helper()
	machine(t, 0.5, true, 8)
}

func machine(t *testing.T, load float64, ok bool, cores int) {
	t.Helper()
	l, n := loadAvg, numCPU
	loadAvg = func() (float64, bool) { return load, ok }
	numCPU = func() int { return cores }
	t.Cleanup(func() { loadAvg, numCPU = l, n })
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
	want := "test -count=1 -timeout 75m -p 2 ./cmd/code-forge-native ./gates\n"
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
	if got := readLog(t, log); got != "test -count=1 -timeout 75m -p 2 ./...\n" {
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
	script := "#!/bin/sh\necho \"$*\" >> '" + argLog + "'\necho \"GOFLAGS=$GOFLAGS\" >> '" + envLog + "'\necho \"GOMAXPROCS=$GOMAXPROCS\" >> '" + envLog + "'\nexit " + code + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	quietMachine(t)
	return argLog, envLog
}

// TestB952GoRunsWithTrimpath: the go child sees -trimpath in GOFLAGS, so worktrees share the
// build cache; the caller's other GOFLAGS stay, and -trimpath is never added twice.
func TestB952GoRunsWithTrimpath(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", "GOFLAGS=-trimpath\nGOMAXPROCS=\n"},
		{"-mod=vendor", "GOFLAGS=-mod=vendor -trimpath\nGOMAXPROCS=\n"},
		{"-trimpath -mod=vendor", "GOFLAGS=-trimpath -mod=vendor\nGOMAXPROCS=\n"},
		{"-trimpath=false", "GOFLAGS=-trimpath=false\nGOMAXPROCS=\n"},
	} {
		_, envLog := fakeGoEnv(t, "0")
		t.Setenv("GOFLAGS", c.in)
		t.Setenv("GOMAXPROCS", "")
		os.Unsetenv("GOMAXPROCS")
		var out, errb bytes.Buffer
		if code := run([]string{"native/gates/scope_test.go"}, &out, &errb); code != 0 {
			t.Fatalf("GOFLAGS %q: exit %d %s", c.in, code, errb.String())
		}
		if got := readLog(t, envLog); got != c.want {
			t.Fatalf("GOFLAGS %q: go saw %q, want %q", c.in, got, c.want)
		}
	}
}

// TestB953DefaultTwoPackagesAtOnce: go test gets -p 2 unless --p sets it; flags come before the
// files and a bad flag is a usage error (exit 2) that starts no go process.
func TestB953DefaultTwoPackagesAtOnce(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"native/gates/scope_test.go"}, "test -count=1 -timeout 75m -p 2 ./gates\n"},
		{[]string{"--p", "6", "native/gates/scope_test.go"}, "test -count=1 -timeout 75m -p 6 ./gates\n"},
		{[]string{"--p=1"}, "test -count=1 -timeout 75m -p 1 ./...\n"},
		{[]string{"--", "native/gates/scope_test.go"}, "test -count=1 -timeout 75m -p 2 ./gates\n"},
	} {
		log := fakeGo(t, "0")
		var out, errb bytes.Buffer
		if code := run(c.args, &out, &errb); code != 0 {
			t.Fatalf("%v: exit %d %s", c.args, code, errb.String())
		}
		if got := readLog(t, log); got != c.want {
			t.Fatalf("%v: go ran %q, want %q", c.args, got, c.want)
		}
	}
	for _, bad := range [][]string{{"--p", "0"}, {"--p"}, {"--p", "x"}, {"--fast", "1"}} {
		log := fakeGo(t, "0")
		var out, errb bytes.Buffer
		if code := run(bad, &out, &errb); code != 2 || !strings.Contains(errb.String(), "usage: cf-gotest") {
			t.Fatalf("%v: exit %d stderr %q", bad, code, errb.String())
		}
		if got := readLog(t, log); got != "" {
			t.Fatalf("%v: go ran %q", bad, got)
		}
	}
}

// TestB953LoadOverCoresHalvesGOMAXPROCS: a 1-minute load over the core count gives the go child
// GOMAXPROCS=cores/2 and one line saying so; at or under the core count, an unreadable load or a
// GOMAXPROCS the caller set leaves it alone.
func TestB953LoadOverCoresHalvesGOMAXPROCS(t *testing.T) {
	for _, c := range []struct {
		name      string
		load      float64
		ok        bool
		cores     int
		callerSet string
		want      string
		says      bool
	}{
		{"busy", 27.5, true, 10, "", "GOMAXPROCS=5", true},
		{"busy one core", 3, true, 1, "", "GOMAXPROCS=1", true},
		{"at cores", 10, true, 10, "", "GOMAXPROCS=", false},
		{"quiet", 2, true, 10, "", "GOMAXPROCS=", false},
		{"no reading", 0, false, 10, "", "GOMAXPROCS=", false},
		{"caller set", 27.5, true, 10, "3", "GOMAXPROCS=3", false},
	} {
		_, envLog := fakeGoEnv(t, "0")
		machine(t, c.load, c.ok, c.cores)
		if c.callerSet != "" {
			t.Setenv("GOMAXPROCS", c.callerSet)
		} else {
			t.Setenv("GOMAXPROCS", "")
			os.Unsetenv("GOMAXPROCS")
		}
		var out, errb bytes.Buffer
		if code := run([]string{"native/gates/scope_test.go"}, &out, &errb); code != 0 {
			t.Fatalf("%s: exit %d", c.name, code)
		}
		if got := readLog(t, envLog); !strings.Contains(got, c.want+"\n") {
			t.Fatalf("%s: go saw %q, want %q", c.name, got, c.want)
		}
		if said := strings.Contains(out.String(), "cf-gotest: load "); said != c.says {
			t.Fatalf("%s: stdout %q", c.name, out.String())
		}
	}
}

// TestB953ReadsLoadLines: the load reading takes the first number of a Linux /proc/loadavg line
// and of macOS `sysctl -n vm.loadavg`.
func TestB953ReadsLoadLines(t *testing.T) {
	for in, want := range map[string]float64{"0.52 0.58 0.59 1/467 123\n": 0.52, "{ 27.54 33.37 28.63 }\n": 27.54} {
		if got, ok := parseLoad(in); !ok || got != want {
			t.Fatalf("%q: %v %v", in, got, ok)
		}
	}
	if _, ok := parseLoad(""); ok {
		t.Fatal("empty text read as a load")
	}
	if _, ok := machineLoad(); !ok {
		t.Fatal("this machine has no readable load average")
	}
}
