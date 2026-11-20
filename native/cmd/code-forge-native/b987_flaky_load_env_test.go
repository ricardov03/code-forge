package main

import (
	"math"
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
	t.Setenv("CODE_FORGE_RUN_ID", "")
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
		{"unreadable", "", -1},
	} {
		t.Run("load "+tc.load, func(t *testing.T) {
			f := newB893Fix(t, b987FakeGo, func(fake string) []string { return []string{fake, "test", "./...", "-count=1"} })
			t.Setenv("CODE_FORGE_FLAKY_LOAD", tc.load)
			// Exercise the original failed gate before its retry. --changed would
			// select no tests from this synthetic module (B1009).
			code, out, errText := b988Gates(t, f)
			if code != 0 || out["allOk"] != true {
				t.Fatalf("gates = %d %v; stderr %s", code, out, errText)
			}
			rows := b798Rows(t, f.home, "test.flaky")
			if len(rows) != 1 {
				t.Fatalf("test.flaky rows %v, want one", rows)
			}
			load, ok := rows[0]["load"].(float64)
			if !ok || load < 0 || math.IsNaN(load) || math.IsInf(load, 0) {
				t.Fatalf("recorded load %v, want a finite nonnegative number", rows[0]["load"])
			}
			procs := tc.procs
			if tc.recorded < 0 {
				// The unreadable override falls back to the live probe. Judge
				// the cap against that same recorded sample, without a load race.
				procs = "-"
				if load > float64(runtime.NumCPU()) {
					procs = want
				}
			} else if load != tc.recorded {
				t.Errorf("recorded load %v, want %v", load, tc.recorded)
			}
			if got := f.reruns(t); !reflect.DeepEqual(got, []string{"GOMAXPROCS=" + procs}) {
				t.Fatalf("rerun env %q, want GOMAXPROCS=%s", got, procs)
			}
			loadText := strconv.FormatFloat(load, 'f', -1, 64)
			if !strings.Contains(errText, "(load "+loadText+")") {
				t.Errorf("WARN line must name the recorded load %s: %q", loadText, errText)
			}
		})
	}
}
