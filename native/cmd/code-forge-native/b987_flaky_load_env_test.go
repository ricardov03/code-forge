package main

import (
	"os"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// b987FakeGo plays a go test gate with one failed test TestFlaky; a rerun
// (-run) logs its GOMAXPROCS ("-" when unset) and passes. go list is the
// real go's.
const b987FakeGo = `#!/bin/sh
if [ "$1" = list ]; then exec "$B667_REAL_GO" "$@"; fi
if [ "$1" != test ]; then exit 0; fi
for a in "$@"; do
  if [ "$a" = -run ]; then
    printf 'GOMAXPROCS=%s\n' "${GOMAXPROCS:--}" >> "$B798_LOG"
    printf 'ok  \texample.invalid/m/a\t0.01s\n'
    exit 0
  fi
done
echo "--- FAIL: TestFlaky (0.30s)"
echo FAIL
printf 'FAIL\texample.invalid/m/a\t0.31s\n'
exit 1
`

// B987: CODE_FORGE_FLAKY_LOAD stands in for the machine's 1-minute load in a
// real `gates run` process, so the B973 load cap is proven end to end: a load
// over the core count gives the flaky rerun GOMAXPROCS=cores/2, a load of 0
// gives none, and the result records the load used.
func TestB987FlakyLoadEnvCapsRerun(t *testing.T) {
	// The cap is never set when the gate's process already sets GOMAXPROCS.
	if old, had := os.LookupEnv("GOMAXPROCS"); had {
		os.Unsetenv("GOMAXPROCS")
		t.Cleanup(func() { os.Setenv("GOMAXPROCS", old) })
	}
	want := strconv.Itoa(max(1, runtime.NumCPU()/2))
	for _, tc := range []struct {
		load, procs string
		recorded    float64
	}{
		{"100000", want, 100000},
		{"0", "-", 0},
	} {
		t.Run("load "+tc.load, func(t *testing.T) {
			f := newB893Fix(t, b987FakeGo, func(fake string) []string { return []string{fake, "test", "./...", "-count=1"} })
			t.Setenv("CODE_FORGE_FLAKY_LOAD", tc.load)
			code, out, errText := f.gates(t)
			if code != 0 || out["allOk"] != true {
				t.Fatalf("gates = %d %v; stderr %s", code, out, errText)
			}
			if got := f.reruns(t); !reflect.DeepEqual(got, []string{"GOMAXPROCS=" + tc.procs}) {
				t.Fatalf("rerun env %q, want GOMAXPROCS=%s", got, tc.procs)
			}
			if !strings.Contains(errText, "(load "+tc.load) {
				t.Errorf("WARN line must name the load %s: %q", tc.load, errText)
			}
			rows := b798Rows(t, f.home, "test.flaky")
			if len(rows) != 1 || rows[0]["load"] != tc.recorded {
				t.Errorf("test.flaky rows %v, want one with load %v", rows, tc.recorded)
			}
		})
	}
}
