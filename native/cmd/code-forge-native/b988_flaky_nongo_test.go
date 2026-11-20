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
)

// b988FakeMake plays a non-go gate program (make test) that prints the go
// test it runs, one failed test TestFlaky and exits 1.
const b988FakeMake = `#!/bin/sh
echo "go test -count=1 -timeout 9m ./a"
echo "--- FAIL: TestFlaky (0.30s)"
echo FAIL
printf 'FAIL\texample.invalid/m/a\t0.31s\n'
exit 1
`

// B988: a gate whose program is not go (make test) but prints the go test it
// runs gets the flaky rerun by name: go (from PATH) test with the printed
// go test's build flags, -p 2, -count=3 and -run of the failed name. The
// rerun passes, so the gate passes with the WARN line and one test.flaky row.
func TestB988NonGoGateRerunsByName(t *testing.T) {
	f := b988Fix(t, b988FakeMake)
	code, out, errText := b988Gates(t, f)
	if code != 0 || out["allOk"] != true {
		t.Fatalf("gates = %d %v; stderr %s", code, out, errText)
	}
	if got, want := f.reruns(t), []string{"test -timeout 9m -p 2 -count=3 -run ^(TestFlaky)$ example.invalid/m/a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reruns %q, want %q", got, want)
	}
	if !strings.Contains(errText, "gates: WARN flaky test TestFlaky (example.invalid/m/a): failed, then passed 3/3 reruns") {
		t.Fatalf("no WARN line: %q", errText)
	}
	if rows := b798Rows(t, f.home, "test.flaky"); len(rows) != 1 || rows[0]["test"] != "TestFlaky" {
		t.Fatalf("test.flaky rows %v", rows)
	}
}

// B988: a non-go gate that prints no go test line is never rerun by name:
// no rerun runs, the gate stays red, and no test.flaky row is written.
func TestB988NonGoGateNoGoTestLineNotRerun(t *testing.T) {
	f := b988Fix(t, strings.Replace(b988FakeMake, "echo \"go test -count=1 -timeout 9m ./a\"\n", "", 1))
	code, out, errText := b988Gates(t, f)
	if code == 0 || out["allOk"] != false {
		t.Fatalf("gates = %d %v, want the gate red; stderr %s", code, out, errText)
	}
	if got := f.reruns(t); len(got) != 0 {
		t.Fatalf("reruns %q, want none", got)
	}
	if strings.Contains(errText, "WARN flaky test") {
		t.Fatalf("WARN line without a rerun: %q", errText)
	}
	if rows := b798Rows(t, f.home, "test.flaky"); len(rows) != 0 {
		t.Fatalf("test.flaky rows %v, want none", rows)
	}
}

// b988Fix is a B893 fixture whose test gate is a fake make running script,
// with the fake go first on PATH.
func b988Fix(t *testing.T, script string) b798Fix {
	t.Helper()
	f := newB893Fix(t, b893FakeGo, func(string) []string {
		mk := filepath.Join(t.TempDir(), "make")
		b667WriteFile(t, mk, script)
		if err := os.Chmod(mk, 0o755); err != nil {
			t.Fatal(err)
		}
		return []string{mk, "test"}
	})
	t.Setenv("PATH", filepath.Dir(f.fake)+string(os.PathListSeparator)+os.Getenv("PATH"))
	return f
}

// b988Gates is b798Fix.gates without --changed: a non-go test program takes
// no changed-file scope (gatesRunnerFiles), so --changed would skip it.
func b988Gates(t *testing.T, f b798Fix) (int, map[string]any, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal([]string{"gates", "run", "--run", "fixture", "--block", "B1", "--json"})
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
