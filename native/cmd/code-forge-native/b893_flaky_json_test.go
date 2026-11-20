package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// b893FakeGo plays a `go test -json` gate: the gate call prints test2json
// events for one failed test TestFlaky in example.invalid/m/a and exits 1;
// a rerun call (-run) is logged and passes. go list is the real go's.
const b893FakeGo = `#!/bin/sh
if [ "$1" = list ]; then exec "$B667_REAL_GO" "$@"; fi
if [ "$1" != test ]; then exit 0; fi
for a in "$@"; do
  if [ "$a" = -run ]; then
    printf '%s\n' "$*" >> "$B798_LOG"
    printf 'ok  \texample.invalid/m/a\t0.01s\n'
    exit 0
  fi
done
echo '{"Action":"start","Package":"example.invalid/m/a"}'
echo '{"Action":"run","Package":"example.invalid/m/a","Test":"TestFlaky"}'
echo '{"Action":"output","Package":"example.invalid/m/a","Test":"TestFlaky","Output":"--- FAIL: TestFlaky (0.30s)\n"}'
echo '{"Action":"fail","Package":"example.invalid/m/a","Test":"TestFlaky","Elapsed":0.3}'
echo '{"Action":"pass","Package":"example.invalid/m/a","Test":"TestSteady","Elapsed":0}'
echo '{"Action":"output","Package":"example.invalid/m/a","Output":"FAIL\n"}'
echo '{"Action":"fail","Package":"example.invalid/m/a","Elapsed":0.31}'
exit 1
`

// B893: a go test -json gate with one failed test reruns that name (no
// -json in the rerun); the rerun passes, so the gate passes with the WARN
// line and one test.flaky row.
func TestB893FlakyGoTestJSONGateReruns(t *testing.T) {
	f := newB893Fix(t, b893FakeGo, func(fake string) []string { return []string{fake, "test", "-json", "./...", "-count=1"} })
	code, out, errText := f.gates(t)
	if code != 0 || out["allOk"] != true {
		t.Fatalf("gates = %d %v; stderr %s", code, out, errText)
	}
	if got, want := f.reruns(t), []string{"test -count=3 -run ^(TestFlaky)$ example.invalid/m/a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reruns %q, want %q", got, want)
	}
	if !strings.Contains(errText, "gates: WARN flaky test TestFlaky (example.invalid/m/a): failed, then passed 3/3 reruns (load ") {
		t.Fatalf("no WARN line: %q", errText)
	}
	rows := b798Rows(t, f.home, "test.flaky")
	if len(rows) != 1 || rows[0]["test"] != "TestFlaky" || !reflect.DeepEqual(rows[0]["packages"], []any{"example.invalid/m/a"}) {
		t.Fatalf("test.flaky rows %v", rows)
	}
}

// newB893Fix is newB798Fix with the fake go given and the test gate argv
// made from its path.
func newB893Fix(t *testing.T, script string, argv func(fake string) []string) b798Fix {
	t.Helper()
	fake := filepath.Join(t.TempDir(), "go")
	b667WriteFile(t, fake, script)
	if err := os.Chmod(fake, 0o755); err != nil {
		t.Fatal(err)
	}
	extra := map[string]any{"test": argv(fake), "lint": nil, "rerun_failed": 1}
	f := newB667GateFixture(t, extra, map[string]*string{"a/a.go": b667Body("package a\n\nvar X = 1\n")})
	fix := b798Fix{f, filepath.Join(f.home, "b798-reruns.log"), fake}
	t.Setenv("B798_LOG", fix.rerunLog)
	return fix
}
