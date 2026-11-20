package main

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B963 tests: a red combined tip becomes ONE found fix block whose brief lists
// every failing test with its suspect blocks.

func b963Found(t *testing.T, f *landBatchFixture) []ledger.Object {
	t.Helper()
	record, err := state.ReadRun(f.home, "r-batch-unit")
	if err != nil {
		t.Fatal(err)
	}
	found, _ := record.Get("found").([]any)
	out := []ledger.Object{}
	for _, item := range found {
		if e, ok := item.(ledger.Object); ok {
			out = append(out, e)
		}
	}
	return out
}

func TestB963RedTipQueuesOneFixBlock(t *testing.T) {
	f := newLandBatchFixture(t, 2)
	code, output := f.land("B1", "B2", "B3")
	if code != 1 {
		t.Fatalf("exit=%d, want 1 (red tip): %s", code, output)
	}
	found := b963Found(t, f)
	if len(found) != 1 {
		t.Fatalf("found=%v, want one fix block for the red tip", found)
	}
	entry := found[0]
	if entry.Get("source") != "land" || entry.Get("state") != "queued" || entry.Get("block") != "B4" || entry.Get("from_block") != "B2" {
		t.Errorf("found entry=%v, want source land, queued block B4 from B2", entry)
	}
	if !strings.Contains(output, "land: fix block B4 queued for the red combined tip (finding F1, 1 failing test(s))") {
		t.Errorf("output must name the fix block: %s", output)
	}
	brief, err := os.ReadFile(autopilotText(entry.Get("brief")))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"- B2 integration contract -> B2 (test name)", "- B1: src/B1.mjs", "- B2: src/B2.mjs", "- B3: src/B3.mjs", "Bisect culprit: B2", "full log: ", "Do not hand-commit on the combined branch"} {
		if !strings.Contains(string(brief), want) {
			t.Errorf("brief must contain %q:\n%s", want, brief)
		}
	}
	// The same red again: the finding is seen again, no second block.
	code, output = f.land("B2", "B3")
	if code != 1 {
		t.Fatalf("second land exit=%d, want 1: %s", code, output)
	}
	found = b963Found(t, f)
	if len(found) != 1 || found[0].Get("seen") != float64(2) {
		t.Errorf("found=%v, want the one finding seen twice", found)
	}
	if !strings.Contains(output, "is finding F1 again (fix block B4); no new block") {
		t.Errorf("output must say the red is known: %s", output)
	}
}

func TestB963GreenTipNoFixBlock(t *testing.T) {
	f := newLandBatchFixture(t, 0)
	if code, output := f.land("B1", "B2"); code != 0 {
		t.Fatalf("exit=%d, want 0: %s", code, output)
	}
	if found := b963Found(t, f); len(found) != 0 {
		t.Errorf("found=%v, want none on a green tip", found)
	}
}

func TestB963SuspectsAndTests(t *testing.T) {
	output := strings.Join([]string{
		"--- FAIL: TestB12Thing (0.01s)",
		"    --- FAIL: TestB12Thing/sub (0.00s)",
		"--- FAIL: TestPlain (0.00s)",
		"    plain_test.go:9: boom",
		"FAIL",
		"FAIL\tgithub.com/x/native/other\t0.012s",
		"--- FAIL: TestInPkg (0.00s)",
		"FAIL\tgithub.com/x/native/diskguard\t0.3s",
		"FAIL\tgithub.com/x/native/broken [build failed]",
		"not ok 3 - spec in test/a.test.mjs",
		"not ok 4 - skipped one # SKIP later",
		"--- FAIL: TestB12Thing (0.01s)",
		"FAIL\tgithub.com/x/native/other\t0.012s",
	}, "\n")
	tests := landRedTests(output)
	want := "TestB12Thing (github.com/x/native/other)|TestB12Thing/sub (github.com/x/native/other)|TestPlain (github.com/x/native/other)|TestInPkg (github.com/x/native/diskguard)|github.com/x/native/broken [build failed]|spec in test/a.test.mjs|skipped one"
	if strings.Join(tests, "|") != want {
		t.Errorf("landRedTests=%q, want %q", strings.Join(tests, "|"), want)
	}
	// The spec summary is read whole, past the 10 names a land refusal shows.
	spec := []string{"✖ failing tests:"}
	for i := 1; i <= 12; i++ {
		spec = append(spec, "", "test at test/s.test.mjs:"+strconv.Itoa(i)+":1", "✖ case "+strconv.Itoa(i)+" (1.5ms)")
	}
	if got := landRedTests(strings.Join(spec, "\n")); len(got) != 12 || got[11] != "case 12 (test/s.test.mjs:12)" {
		t.Errorf("spec summary=%q, want all 12 cases with their files", got)
	}
	red := landRedTip{tests: tests, culprit: "B3", blocks: []landRedBlock{
		{id: "B1", files: []string{"test/a.test.mjs"}},
		{id: "B12", files: []string{"native/broken/x.go"}},
		{id: "B3", files: []string{"README.md"}},
		{id: "B4", files: []string{"native/diskguard/diskguard.go"}},
	}}
	cases := map[string]string{
		"TestB12Thing (github.com/x/native/other)":     "B12 test name",
		"TestB12Thing/sub (github.com/x/native/other)": "B12 test name",
		"TestPlain (github.com/x/native/other)":        "B3 bisect culprit",
		"TestInPkg (github.com/x/native/diskguard)":    "B4 changed file",
		"github.com/x/native/broken [build failed]":    "B12 changed file",
		"spec in test/a.test.mjs":                      "B1 changed file",
	}
	for test, wantText := range cases {
		suspects, why := landRedSuspects(test, red)
		if got := strings.Join(suspects, " ") + " " + why; got != wantText {
			t.Errorf("landRedSuspects(%q)=%q, want %q", test, got, wantText)
		}
	}
	// B1 is not named by TestB12; an empty id names nothing; without a
	// culprit an unmatched test suspects every block.
	if landRedNamesBlock("TestB12Thing", "B1") || landRedNamesBlock("1abc", "") {
		t.Errorf("landRedNamesBlock: B1 must not match TestB12, an empty id matches nothing")
	}
	red.culprit = ""
	if suspects, why := landRedSuspects("TestPlain (github.com/x/native/other)", red); strings.Join(suspects, " ") != "B1 B12 B3 B4" || why != "no match: every batch block" {
		t.Errorf("unmatched without culprit=%v %q, want every block", suspects, why)
	}
	in := landRedFinding(red)
	if in.source != "land" || !strings.HasPrefix(in.title, "red combined tip of B1 B12 B3 B4: 7 failing test(s)") {
		t.Errorf("finding=%+v", in)
	}
	if reason := apfOwnerReason(in.labels, in.title, in.evidence, false); reason != "" {
		t.Errorf("the fix finding waits for the owner (%s); it must be queued", reason)
	}
}
