package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B798 tests: a fake `go` (its base name is go) plays a go test gate. The
// gate call (no -run) prints go test failures for the names in B798_FAIL in
// package example.invalid/m/a and exits 1; with B798_ONCE it does so only
// the first time. A rerun call (-run) is logged and passes, unless the file
// <log>.rerun-fail exists. go list is the real go's (the import graph).
const b798FakeGo = `#!/bin/sh
if [ "$1" = list ]; then exec "$B667_REAL_GO" "$@"; fi
if [ "$1" != test ]; then exit 0; fi
for a in "$@"; do
  if [ "$a" = -run ]; then
    printf '%s\n' "$*" >> "$B798_LOG"
    [ -f "$B798_LOG.rerun-fail" ] && { echo "--- FAIL: TestA (0.00s)"; echo FAIL; printf 'FAIL\texample.invalid/m/a\t0.01s\n'; exit 1; }
    printf 'ok  \texample.invalid/m/a\t0.01s\n'
    exit 0
  fi
done
[ -n "$B798_FAIL" ] || exit 0
if [ -n "$B798_ONCE" ]; then
  [ -f "$B798_LOG.failed" ] && exit 0
  : > "$B798_LOG.failed"
fi
for n in $B798_FAIL; do echo "--- FAIL: $n (0.30s)"; echo "    x_test.go:9: did not answer in time"; done
[ -n "$B798_PANIC" ] && echo "panic: boom"
echo FAIL
printf 'FAIL\texample.invalid/m/a\t0.31s\n'
exit 1
`

type b798Fix struct {
	b667GateFixture
	rerunLog, fake string
}

func newB798Fix(t *testing.T, gatesExtra map[string]any) b798Fix {
	t.Helper()
	fake := filepath.Join(t.TempDir(), "go")
	b667WriteFile(t, fake, b798FakeGo)
	if err := os.Chmod(fake, 0o755); err != nil {
		t.Fatal(err)
	}
	extra := map[string]any{"test": []string{fake, "test", "./...", "-count=1"}, "lint": nil, "rerun_failed": 1}
	for k, v := range gatesExtra {
		extra[k] = v
	}
	f := newB667GateFixture(t, extra, map[string]*string{"a/a.go": b667Body("package a\n\nvar X = 1\n")})
	b798 := b798Fix{f, filepath.Join(f.home, "b798-reruns.log"), fake}
	t.Setenv("B798_LOG", b798.rerunLog)
	return b798
}

// gates runs `gates run --run fixture --block B1 --changed --json` in a
// fresh process: exit code, the JSON on stdout, stderr.
func (f b798Fix) gates(t *testing.T) (int, map[string]any, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal([]string{"gates", "run", "--run", "fixture", "--block", "B1", "--changed", "--json"})
	cmd := exec.Command(exe, "-test.run=^TestGatesProcess$")
	cmd.Dir = f.repo
	cmd.Env = append(os.Environ(), "HOME="+f.home, "B667_GO_LOG="+f.log, "CODE_FORGE_NO_ERROR_LOG=1", "CODE_FORGE_TEST_QUEUE=off", "CODE_FORGE_NODE=/nonexistent-code-forge-node", "CF_GATES_PROCESS=1", "CF_GATES_ARGS="+string(encoded))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		e, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		code = e.ExitCode()
	}
	out := map[string]any{}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("gate JSON: %v: %s / %s", err, stdout.String(), stderr.String())
	}
	return code, out, stderr.String()
}

func (f b798Fix) reruns(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(f.rerunLog)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func b798Rows(t *testing.T, home, event string) []map[string]any {
	t.Helper()
	rows, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge", "ledger"), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	found := []map[string]any{}
	for _, v := range rows {
		if row, ok := v.(map[string]any); ok && row["event"] == event {
			found = append(found, row)
		}
	}
	return found
}

func b798Findings(t *testing.T, home, run string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(gatesFindingsFile(home, run))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	out := []map[string]any{}
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		row := map[string]any{}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("findings line %q: %v", line, err)
		}
		out = append(out, row)
	}
	return out
}

func b798TestGate(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	results, _ := out["results"].([]any)
	for _, v := range results {
		if r, _ := v.(map[string]any); r["gate"] == "test" {
			return r
		}
	}
	t.Fatalf("no test gate in %v", out)
	return nil
}

// A test that fails once, then passes three reruns: the gate passes with a
// WARN, one test.flaky row and one finding in the run's findings list. A
// second flaky pass adds a row but no second finding.
func TestB798FlakyGoTestPassesWithRow(t *testing.T) {
	f := newB798Fix(t, nil)
	t.Setenv("B798_FAIL", "TestFlaky")
	code, out, errText := f.gates(t)
	if code != 0 || out["allOk"] != true {
		t.Fatalf("gates = %d %v; stderr %s", code, out, errText)
	}
	if got, want := f.reruns(t), []string{"test -p 2 -count=3 -run ^(TestFlaky)$ example.invalid/m/a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reruns %q, want %q", got, want)
	}
	r := b798TestGate(t, out)
	if r["ok"] != true || !reflect.DeepEqual(r["flaky"], []any{"TestFlaky"}) {
		t.Fatalf("test gate %v", r)
	}
	if !strings.Contains(errText, "gates: WARN flaky test TestFlaky (example.invalid/m/a): failed, then passed 3/3 reruns (load ") {
		t.Fatalf("no WARN line: %q", errText)
	}
	rows := b798Rows(t, f.home, "test.flaky")
	if len(rows) != 1 {
		t.Fatalf("test.flaky rows %v", rows)
	}
	row := rows[0]
	if row["run"] != "fixture" || row["block"] != "B1" || row["test"] != "TestFlaky" || row["gate"] != "test" || row["reruns"] != float64(3) || !reflect.DeepEqual(row["packages"], []any{"example.invalid/m/a"}) {
		t.Fatalf("test.flaky row %v", row)
	}
	if load, ok := row["load"].(float64); !ok || load < 0 {
		t.Fatalf("load %v", row["load"])
	}
	done := b798Rows(t, f.home, "gate.done")
	if len(done) != 1 || done[0]["all_ok"] != true || !reflect.DeepEqual(done[0]["flaky"], []any{"TestFlaky"}) {
		t.Fatalf("gate.done %v", done)
	}
	findings := b798Findings(t, f.home, "fixture")
	if len(findings) != 1 {
		t.Fatalf("findings %v", findings)
	}
	fd := findings[0]
	if fd["source"] != "flaky-test" || fd["fingerprint"] != "flaky-test:example.invalid/m/a.TestFlaky" || fd["block"] != "B1" || fd["run"] != "fixture" || !strings.HasPrefix(fd["title"].(string), "Fix flaky test TestFlaky (failed at load ") {
		t.Fatalf("finding %v", fd)
	}
	ev, _ := fd["evidence"].(map[string]any)
	if ev["test"] != "TestFlaky" || !reflect.DeepEqual(ev["rerun"], []any{f.fake, "test", "-p", "2", "-count=3", "-run", "^(TestFlaky)$", "example.invalid/m/a"}) {
		t.Fatalf("evidence %v", ev)
	}

	if code, _, errText := f.gates(t); code != 0 {
		t.Fatalf("second gates = %d: %s", code, errText)
	}
	if rows := b798Rows(t, f.home, "test.flaky"); len(rows) != 2 {
		t.Fatalf("second test.flaky rows %d", len(rows))
	}
	if findings := b798Findings(t, f.home, "fixture"); len(findings) != 1 {
		t.Fatalf("a duplicate finding was added: %v", findings)
	}
}

// A test that fails every rerun is a real failure: red gate, no row, no
// finding.
func TestB798AlwaysFailingGoTestFailsGate(t *testing.T) {
	f := newB798Fix(t, nil)
	t.Setenv("B798_FAIL", "TestBroken")
	b667WriteFile(t, f.rerunLog+".rerun-fail", "")
	code, out, errText := f.gates(t)
	if code != 1 || out["allOk"] != false || b798TestGate(t, out)["ok"] != false || b798TestGate(t, out)["flaky"] != nil {
		t.Fatalf("gates = %d %v", code, out)
	}
	if got := f.reruns(t); len(got) != 1 {
		t.Fatalf("reruns %q, want one", got)
	}
	if strings.Contains(errText, "WARN flaky") || len(b798Rows(t, f.home, "test.flaky")) != 0 || b798Findings(t, f.home, "fixture") != nil {
		t.Fatalf("a real failure was reported flaky: %q", errText)
	}
	if red := b798Rows(t, f.home, "gate.red"); len(red) != 1 {
		t.Fatalf("gate.red rows %v", red)
	}
}

// More than three failed names, a panic, or rerun_failed: 0: never rerun,
// the gate fails as before.
func TestB798NoRerunCases(t *testing.T) {
	for _, c := range []struct {
		name, fail, panic string
		extra             map[string]any
	}{
		{"four failed tests", "TestA TestB TestC TestD", "", nil},
		{"a panic", "TestA", "1", nil},
		{"rerun_failed off", "TestA", "", map[string]any{"rerun_failed": 0}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newB798Fix(t, c.extra)
			t.Setenv("B798_FAIL", c.fail)
			t.Setenv("B798_PANIC", c.panic)
			code, out, errText := f.gates(t)
			if code != 1 || out["allOk"] != false {
				t.Fatalf("gates = %d %v", code, out)
			}
			if got := f.reruns(t); got != nil {
				t.Fatalf("reruns %q, want none", got)
			}
			if strings.Contains(errText, "WARN flaky") || len(b798Rows(t, f.home, "test.flaky")) != 0 || b798Findings(t, f.home, "fixture") != nil {
				t.Fatalf("reported flaky: %q", errText)
			}
		})
	}
}

// Exactly three failed names that pass their reruns: one rerun call for all
// three, three rows and three findings.
func TestB798ThreeFlakyNamesPass(t *testing.T) {
	f := newB798Fix(t, nil)
	t.Setenv("B798_FAIL", "TestA TestB TestC")
	code, out, errText := f.gates(t)
	if code != 0 || out["allOk"] != true {
		t.Fatalf("gates = %d %v: %s", code, out, errText)
	}
	if got, want := f.reruns(t), []string{"test -p 2 -count=3 -run ^(TestA|TestB|TestC)$ example.invalid/m/a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reruns %q, want %q", got, want)
	}
	if rows := b798Rows(t, f.home, "test.flaky"); len(rows) != 3 {
		t.Fatalf("rows %v", rows)
	}
	if findings := b798Findings(t, f.home, "fixture"); len(findings) != 3 {
		t.Fatalf("findings %v", findings)
	}
}

// Block land (batch): a block gate whose Go test fails once lands; the
// signed test.flaky row names the block and the finding is in the run's list.
func TestB798LandBatchFlakyBlockGateLands(t *testing.T) {
	f, goLog := newB667BatchFixture(t)
	// The batch fixture's fake go becomes B798's; main's gate config turns
	// rerun_failed back on (the fixture sets 0).
	b667WriteFile(t, filepath.Join(filepath.Dir(goLog), "fakebin", "go"), b798FakeGo)
	cfg := landTestConfig([]string{filepath.Join(filepath.Dir(goLog), "fakebin", "go"), "test", "./...", "-count=1"})
	data, _ := json.Marshal(cfg)
	f.write(f.repo, ".code-forge.yml", string(data))
	f.git(f.repo, "commit", "-qam", "gate config")
	f.base = f.git(f.repo, "rev-parse", "HEAD")
	log := filepath.Join(f.home, "b798-reruns.log")
	t.Setenv("B798_LOG", log)
	t.Setenv("B798_FAIL", "TestFlaky")
	t.Setenv("B798_ONCE", "1")
	code, output := f.landWith(nil, "B1", "B2")
	if code != 0 {
		t.Fatalf("exit=%d, want 0: %s", code, output)
	}
	if !strings.Contains(output, "gates: WARN flaky test TestFlaky (example.invalid/m/a)") {
		t.Fatalf("no WARN: %s", output)
	}
	rows := f.rows("test.flaky")
	if len(rows) != 1 || rows[0]["run"] != "r-batch-unit" || rows[0]["block"] != "B1" || rows[0]["test"] != "TestFlaky" || rows[0]["mac"] == nil {
		t.Fatalf("test.flaky rows %v", rows)
	}
	findings := b798Findings(t, f.home, "r-batch-unit")
	if len(findings) != 1 || findings[0]["block"] != "B1" {
		t.Fatalf("findings %v", findings)
	}
	f.checkMain([]string{"B1", "B2"})
}
