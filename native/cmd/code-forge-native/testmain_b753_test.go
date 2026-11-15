package main

import (
	"fmt"
	"os"
	"testing"
)

// TestMain gives the whole test binary its own TMPDIR (B753). Tests run verbs
// in-process (autopilot waive, jev ask, ...) that make the CLI's lazy run root
// <TMPDIR>/code-forge/proc-<pid>-<ms>; in the real binary main() releases it
// at exit (childregRelease), but a test binary never runs main(), so each such
// root stayed under the machine's real $TMPDIR/code-forge with a dead owner
// and every later run start had to sweep it. Here every root lands in a
// private directory that is released and removed when the tests end.
//
// A test that re-runs this binary as a child (os.Args[0] -test.run=...)
// passes it the TMPDIR it wants; the child inherits b753TmpEnv from the
// parent and keeps that TMPDIR as given.
func TestMain(m *testing.M) {
	os.Exit(b753Main(m))
}

// b753TmpEnv marks a child of a test binary that already has its own TMPDIR.
const b753TmpEnv = "CODE_FORGE_B753_TEST_TMPDIR"

func b753Main(m *testing.M) int {
	if os.Getenv(b753TmpEnv) != "" {
		return m.Run()
	}
	real, hadReal := os.LookupEnv("TMPDIR")
	dir, err := os.MkdirTemp("", "cf-b753-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "b753: cannot make the test TMPDIR:", err)
		return 1
	}
	_ = os.Setenv("TMPDIR", dir)
	_ = os.Setenv(b753TmpEnv, dir)
	code := m.Run()
	childregRelease()
	_ = os.RemoveAll(dir)
	if hadReal {
		_ = os.Setenv("TMPDIR", real)
	} else {
		_ = os.Unsetenv("TMPDIR")
	}
	_ = os.Unsetenv(b753TmpEnv)
	return code
}
