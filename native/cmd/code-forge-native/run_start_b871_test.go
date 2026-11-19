package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestB871SecondRunStartWritesNoRun is the B801 audit's P01 sample: `run
// start` while a run's worker is live in the repository refuses (exit 1) and
// must leave no new run record, key, mirror or ledger row, so `status --json`
// reads the same before and after.
func TestB871SecondRunStartWritesNoRun(t *testing.T) {
	f := newStartFixture(t, "project:\n  slug: unit\ntmp:\n  root: TMPROOT\n")
	code, out, errs := runVerbCall(t, "start", "--cwd", f.repo, "--run", "r-first")
	if code != 0 || !strings.HasPrefix(out, "run r-first started") {
		t.Fatalf("first start = %d %q %q", code, out, errs)
	}
	here, _ := os.Getwd()
	if err := os.Chdir(f.repo); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(here) }()
	statusBefore := runStatusJSON(t)
	rowsBefore := len(f.startRows(t, "r-first"))

	code, out, errs = runVerbCall(t, "start", "--cwd", f.repo, "--run", "r-second")
	launched := f.launched(t)
	want := "run start: the worker did not start: run r-first still has a live worker (pid " + strconv.Itoa(launched[0]) + "); end it with: code-forge run end --run r-first; nothing was recorded\n"
	if code != 1 || out != "" || errs != want {
		t.Fatalf("second start = %d %q %q, want %q", code, out, errs, want)
	}
	if len(launched) != 1 {
		t.Fatalf("a second worker was launched: %v", launched)
	}
	for _, name := range []string{"r-second.json", "r-second.key"} {
		if _, err := os.Stat(filepath.Join(f.home, ".code-forge", "runs", name)); !os.IsNotExist(err) {
			t.Fatalf("%s was written: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(f.repo, ".code-forge", "runs", "r-second.json")); !os.IsNotExist(err) {
		t.Fatalf("workspace mirror was written: %v", err)
	}
	if rows := len(f.startRows(t, "r-first")); rows != rowsBefore {
		t.Fatalf("ledger rows %d -> %d", rowsBefore, rows)
	}
	if after := runStatusJSON(t); after != statusBefore {
		t.Fatalf("status --json changed:\nbefore %s\nafter  %s", statusBefore, after)
	}
}

func runStatusJSON(t *testing.T) string {
	t.Helper()
	code, out, errs := runCaptured(t, "status", "--json")
	if code != 0 {
		t.Fatalf("status --json = %d %q %q", code, out, errs)
	}
	return out
}
