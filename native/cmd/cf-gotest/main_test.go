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
	gateSelectionFixture(t)
	return log
}

// gateSelectionFixture supplies real selected test files for fake-go tests.
func gateSelectionFixture(t *testing.T) {
	t.Helper()
	t.Setenv("CODE_FORGE_RUN_ID", "")
	t.Setenv("CODE_FORGE_TEST_RUN", "")
	old := nativeDir
	nativeDir = t.TempDir()
	t.Cleanup(func() { nativeDir = old })
	for _, name := range []string{"gates/scope_test.go", "cmd/code-forge-native/b1_test.go", "cmd/code-forge-native/b2_test.go"} {
		file := filepath.Join(nativeDir, name)
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("package unit\nimport \"testing\"\nfunc TestSelected(t *testing.T) {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
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
	want := "test -count=1 -timeout 75m -p 2 -run ^(TestSelected)$ -trimpath ./cmd/code-forge-native\ntest -count=1 -timeout 75m -p 2 -run ^(TestSelected)$ -trimpath ./gates\n"
	if got := readLog(t, log); got != want {
		t.Fatalf("go ran %q, want %q", got, want)
	}
	if !strings.Contains(out.String(), "cf-gotest: go test -count=1 -timeout 75m -p 2 -run ^(TestSelected)$ -trimpath ./gates") {
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
	gateSelectionFixture(t)
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
		{[]string{"native/gates/scope_test.go"}, "test -count=1 -timeout 75m -p 2 -run ^(TestSelected)$ -trimpath ./gates\n"},
		{[]string{"--p", "6", "native/gates/scope_test.go"}, "test -count=1 -timeout 75m -p 6 -run ^(TestSelected)$ -trimpath ./gates\n"},
		{[]string{"--p=1"}, "test -count=1 -timeout 75m -p 1 ./...\n"},
		{[]string{"--", "native/gates/scope_test.go"}, "test -count=1 -timeout 75m -p 2 -run ^(TestSelected)$ -trimpath ./gates\n"},
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

// shardFixture is a fake go whose `go test -c -o <bin> ... <pkg>` writes a fake test binary. The
// binary lists names (one per line) for -test.list, logs each -test.run regex and its directory to
// runs.log, and fails when its regex selects TestRed. nativeDir and shardScript point at a temp
// native/ folder and the real scripts/ci-shards.sh.
func shardFixture(t *testing.T, names string) (goLog, runsLog string) {
	t.Helper()
	t.Setenv("CODE_FORGE_TEST_RUN", "")
	t.Setenv("CODE_FORGE_RUN_ID", "")
	dir := t.TempDir()
	goLog, runsLog = filepath.Join(dir, "go.log"), filepath.Join(dir, "runs.log")
	bin := "#!/bin/sh\n" +
		"if [ \"$1\" = -test.list ]; then printf '" + names + "'; exit 0; fi\n" +
		"echo \"$2 $(pwd -P)\" >> '" + runsLog + "'\n" +
		"case \"$2\" in *TestRed*) echo '--- FAIL: TestRed'; echo FAIL; exit 1;; esac\n" +
		"echo PASS\n"
	if err := os.WriteFile(filepath.Join(dir, "bin.sh"), []byte(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	gosh := "#!/bin/sh\necho \"$*\" >> '" + goLog + "'\n" +
		"while [ $# -gt 0 ]; do if [ \"$1\" = -o ]; then cp '" + filepath.Join(dir, "bin.sh") + "' \"$2\"; fi; shift; done\n"
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(gosh), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	quietMachine(t)
	native := filepath.Join(dir, "native")
	if err := os.MkdirAll(filepath.Join(native, "cmd", "code-forge-native"), 0o755); err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "..", "scripts", "ci-shards.sh"))
	if err != nil {
		t.Fatal(err)
	}
	ns, ss := nativeDir, shardScript
	nativeDir, shardScript = native, script
	t.Cleanup(func() { nativeDir, shardScript = ns, ss })
	return goLog, runsLog
}

// shardRuns reads runs.log: the names each shard's -test.run regex selects, and the directories.
func shardRuns(t *testing.T, runsLog string) (sets [][]string, dirs []string) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(readLog(t, runsLog)), "\n") {
		re, dir, _ := strings.Cut(line, " ")
		if !strings.HasPrefix(re, "^(") || !strings.HasSuffix(re, ")$") {
			t.Fatalf("regex %q is not anchored", re)
		}
		sets = append(sets, strings.Split(strings.TrimSuffix(strings.TrimPrefix(re, "^("), ")$"), "|"))
		dirs = append(dirs, dir)
	}
	return sets, dirs
}

// TestB807ShardsRunDisjointSetsCoveringAllTests: --shards 4 builds the package once, then runs 4
// processes of that binary whose -test.run sets are disjoint and cover every listed test, each in
// the package directory, with the CI round-robin split.
func TestB807ShardsRunDisjointSetsCoveringAllTests(t *testing.T) {
	names := []string{"ExampleZ", "FuzzY", "TestA", "TestB", "TestC", "TestD", "TestE", "TestF", "TestG"}
	goLog, runsLog := shardFixture(t, strings.Join(names, `\n`)+`\nok  \tnoise\n`)
	var out, errb bytes.Buffer
	if code := run([]string{"--shards", "4", "--p", "4"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out.String(), errb.String())
	}
	if got := strings.Split(strings.TrimSpace(readLog(t, goLog)), "\n"); len(got) != 1 || !strings.HasPrefix(got[0], "test -c -o ") || !strings.HasSuffix(got[0], " -p 4 ./cmd/code-forge-native") {
		t.Fatalf("go ran %q, want one test -c build", got)
	}
	sets, dirs := shardRuns(t, runsLog)
	if len(sets) != 4 {
		t.Fatalf("%d shard processes, want 4", len(sets))
	}
	seen := map[string]int{}
	for _, set := range sets {
		for _, n := range set {
			seen[n]++
		}
	}
	for _, n := range names {
		if seen[n] != 1 {
			t.Fatalf("%s is in %d shards (%v)", n, seen[n], sets)
		}
	}
	if len(seen) != len(names) {
		t.Fatalf("shards ran %v, want only %v", seen, names)
	}
	// the CI split: k-th sorted name goes to shard k mod 4
	want := map[string]bool{"ExampleZ|TestC|TestG": true, "FuzzY|TestD": true, "TestA|TestE": true, "TestB|TestF": true}
	for _, set := range sets {
		if !want[strings.Join(set, "|")] {
			t.Fatalf("shard %v is not a CI round-robin shard", set)
		}
	}
	pkgDir, _ := filepath.EvalSymlinks(filepath.Join(nativeDir, "cmd", "code-forge-native"))
	for _, d := range dirs {
		if d != pkgDir {
			t.Fatalf("shard ran in %s, want %s", d, pkgDir)
		}
	}
	if !strings.Contains(out.String(), "cf-gotest: all 4 shards ok") {
		t.Fatalf("stdout %s", out.String())
	}
}

// TestB807OneRedShardMakesTheRunRed: a failing test in one shard fails the whole run, and that
// shard's output is printed under its header.
func TestB807OneRedShardMakesTheRunRed(t *testing.T) {
	_, runsLog := shardFixture(t, `TestA\nTestB\nTestRed\nTestZ\n`)
	var out, errb bytes.Buffer
	if code := run([]string{"--shards", "4"}, &out, &errb); code != 1 {
		t.Fatalf("exit %d, want 1\n%s", code, out.String())
	}
	if sets, _ := shardRuns(t, runsLog); len(sets) != 4 {
		t.Fatalf("%d shards ran, want all 4 (a red shard does not stop the others)", len(sets))
	}
	o := out.String()
	if !strings.Contains(o, "--- FAIL: TestRed") || !strings.Contains(o, "FAIL\t./cmd/code-forge-native [shard 3/4]") || !strings.Contains(o, "cf-gotest: 1 of 4 shards FAIL") {
		t.Fatalf("stdout %s", o)
	}
}

// TestB807ShardsNeedOnePackageWithTests: a selection of two packages is a usage error, and a
// package with no tests is red, never an empty green run.
func TestB807ShardsNeedOnePackageWithTests(t *testing.T) {
	goLog, _ := shardFixture(t, `TestA\n`)
	var out, errb bytes.Buffer
	if code := run([]string{"--shards", "2", "native/gates/a_test.go", "native/state/b_test.go"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "--shards runs one package") {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
	if got := readLog(t, goLog); got != "" {
		t.Fatalf("go ran %q", got)
	}
	shardFixture(t, `noise\n`)
	out.Reset()
	errb.Reset()
	if code := run([]string{"--shards", "2"}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "has no tests to shard") {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
}
