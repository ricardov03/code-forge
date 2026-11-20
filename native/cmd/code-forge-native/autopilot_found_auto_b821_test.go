package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B821: autopilot found runs by itself after a red gate, a failed land, a
// hand-over and the FOUND lines of a coder report.

func b821Found(t *testing.T, home, runID string) []ledger.Object {
	t.Helper()
	record, err := state.ReadRun(home, runID)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := record.Get("found").([]any)
	out := []ledger.Object{}
	for _, item := range list {
		out = append(out, item.(ledger.Object))
	}
	return out
}

// A red gate writes one found block (its failing test in the title); the
// same red again is seen twice, not a second block; a green gate adds none.
func TestB821RedGateRecordsFound(t *testing.T) {
	f := b950Run(t, map[string]any{})
	red := ledger.Object{{Key: "gate", Value: "test"}, {Key: "ok", Value: false}, {Key: "code", Value: 1}, {Key: "argv", Value: []string{"go", "test", "./x"}},
		{Key: "stdout", Value: "=== RUN   TestB1Thing\n--- FAIL: TestB1Thing (0.00s)\n    x_test.go:9: got 1\nFAIL\n"}, {Key: "stderr", Value: ""}}
	green := ledger.Object{{Key: "gate", Value: "lint"}, {Key: "ok", Value: true}, {Key: "code", Value: 0}, {Key: "stdout", Value: ""}, {Key: "stderr", Value: ""}}
	gatesAfterRun(f.home, "r-test", "B1", []any{red, green}, 0)
	found := b821Found(t, f.home, "r-test")
	if len(found) != 1 || found[0].Get("source") != "gate" || found[0].Get("state") != "queued" || found[0].Get("from_block") != "B1" ||
		found[0].Get("title") != "the test gate is red: TestB1Thing" || !strings.Contains(autopilotText(found[0].Get("evidence")), "--- FAIL: TestB1Thing") {
		t.Fatalf("found = %v", found)
	}
	gatesAfterRun(f.home, "r-test", "B1", []any{red, green}, 0)
	if found = b821Found(t, f.home, "r-test"); len(found) != 1 || found[0].Get("seen") != float64(2) {
		t.Fatalf("after the second red = %v", found)
	}
}

// A land that stops (exit 1) writes one found block with its stderr text,
// deduped; a refused (usage) land and a hand-over write none.
func TestB821FailedLandRecordsFound(t *testing.T) {
	f := b950Run(t, map[string]any{})
	start := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	landAfter(io.Discard, f.home, "r-test", "B1", start, false, 2, "usage: block land")
	landAfter(io.Discard, f.home, "r-test", "B1", start, false, landHandover, "")
	if found := b821Found(t, f.home, "r-test"); len(found) != 0 {
		t.Fatalf("usage/hand-over found = %v", found)
	}
	message := "block B1 land stopped at merge-gate: go vet failed\nnext: code-forge block land B1 --run r-test"
	var out strings.Builder
	landAfter(&out, f.home, "r-test", "B1", start, false, 1, message)
	landAfter(&out, f.home, "r-test", "B1", start, false, 1, message)
	found := b821Found(t, f.home, "r-test")
	if len(found) != 1 || found[0].Get("source") != "land" || found[0].Get("title") != "block B1 failed to land" ||
		!strings.Contains(autopilotText(found[0].Get("evidence")), "go vet failed") || found[0].Get("seen") != float64(2) {
		t.Fatalf("found = %v", found)
	}
	if !strings.Contains(out.String(), "autopilot ") {
		t.Fatalf("no outcome line: %q", out.String())
	}
}

// A refused command that names the run (a hand-over) writes one found block,
// deduped by command and reason; without an active grant it writes none.
func TestB821HandoverRecordsFound(t *testing.T) {
	f := b950Run(t, map[string]any{})
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "0")
	args := []string{"block", "close", "B1", "--run", "r-test", "--decide"}
	stderr := os.Stderr
	devnull, _ := os.Open(os.DevNull)
	os.Stderr = devnull
	code1 := refuseUnported(args, hUnrouted)
	code2 := refuseUnported(args, hUnrouted)
	os.Stderr = stderr
	devnull.Close()
	if code1 != 1 || code2 != 1 {
		t.Fatalf("refusal exit = %d %d", code1, code2)
	}
	found := b821Found(t, f.home, "r-test")
	if len(found) != 1 || found[0].Get("source") != "handover" || found[0].Get("from_block") != "B1" ||
		!strings.HasPrefix(autopilotText(found[0].Get("title")), "code-forge block close hands over: ") || found[0].Get("seen") != float64(2) {
		t.Fatalf("found = %v", found)
	}
	g := b800Fixture(t, map[string]any{}) // no grant
	os.Stderr, _ = os.Open(os.DevNull)
	refuseUnported([]string{"block", "close", "B1", "--run", "r-test"}, hUnrouted)
	os.Stderr = stderr
	if found := b821Found(t, g.home, "r-test"); len(found) != 0 {
		t.Fatalf("no grant found = %v", found)
	}
}

// block close reads the coder report's FOUND lines into found blocks (land
// closes through the same path); a second close of the same leftovers adds none.
func TestB821CloseReportRecordsFound(t *testing.T) {
	f := newCloseReviewFixture(t, 1)
	if err := state.UpdateRun(f.home, "r-close-fixture", func(record *ledger.Object) error {
		grant := b950Grant()
		grant["planned_blocks"] = []any{"B1"}
		data, _ := ledger.Stringify(grant, false)
		value, err := ledger.ParseObject(data)
		if err != nil {
			return err
		}
		record.Set("autopilot", value)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	closeFixtureWrite(t, f.report, []byte("TESTS go test ./... exit=0\nLeftovers:\n- FOUND [docs] the README misses the --json flag :: README.md:40 shows no --json\n===BLOCK B1 COMPLETE===\n"))
	if err := os.MkdirAll(filepath.Join(f.top, ".code-forge/queue"), 0700); err != nil {
		t.Fatal(err)
	}
	done := f.reviewer("approved")
	code, out, errs := f.close()
	if err := <-done; err != nil {
		t.Error(err)
	}
	if code != 0 || !strings.Contains(out, "block B1 closed") {
		t.Fatalf("close = %d %s %s", code, out, errs)
	}
	found := b821Found(t, f.home, "r-close-fixture")
	if len(found) != 1 || found[0].Get("source") != "coder" || found[0].Get("from_block") != "B1" || found[0].Get("title") != "the README misses the --json flag" || found[0].Get("block") != "B2" {
		t.Fatalf("found = %v (stderr %s)", found, errs)
	}
	apfFoundReport(io.Discard, f.home, "r-close-fixture", "B1", "- FOUND [docs] the README misses the --json flag :: README.md:40 shows no --json\n")
	if found = b821Found(t, f.home, "r-close-fixture"); len(found) != 1 || found[0].Get("seen") != float64(2) {
		t.Fatalf("dedup = %v", found)
	}
}
