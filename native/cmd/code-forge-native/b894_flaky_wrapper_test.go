package main

import (
	"reflect"
	"testing"
)

// b894FakeGo plays a go run wrapper gate (like cf-gotest): `go run ...`
// names the go test it runs with its own flags, prints one failed test and
// exits 1; a rerun (go test ... -run) is logged and passes.
const b894FakeGo = `#!/bin/sh
if [ "$1" = list ]; then exec "$B667_REAL_GO" "$@"; fi
if [ "$1" = run ]; then
  echo "wrap: go test -count=1 -timeout 75m -p 3 ./a"
  echo "--- FAIL: TestFlaky (0.30s)"
  echo FAIL
  printf 'FAIL\texample.invalid/m/a\t0.31s\n'
  exit 1
fi
for a in "$@"; do
  if [ "$a" = -run ]; then
    printf '%s\n' "$*" >> "$B798_LOG"
    printf 'ok  \texample.invalid/m/a\t0.01s\n'
    exit 0
  fi
done
exit 0
`

// B894: the flaky rerun of a wrapper gate keeps the wrapper's go test flags
// (-timeout 75m, -p 3), not go test's defaults.
func TestB894WrapperGateRerunKeepsFlags(t *testing.T) {
	f := newB893Fix(t, b894FakeGo, func(fake string) []string { return []string{fake, "run", "./wrap"} })
	code, out, errText := f.gates(t)
	if code != 0 || out["allOk"] != true {
		t.Fatalf("gates = %d %v; stderr %s", code, out, errText)
	}
	if got, want := f.reruns(t), []string{"test -timeout 75m -p 3 -count=3 -run ^(TestFlaky)$ example.invalid/m/a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reruns %q, want %q", got, want)
	}
	if rows := b798Rows(t, f.home, "test.flaky"); len(rows) != 1 || rows[0]["test"] != "TestFlaky" {
		t.Fatalf("test.flaky rows %v", rows)
	}
}
