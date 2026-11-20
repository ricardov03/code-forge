package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/blockopen"
	"github.com/ricardov03/code-forge/native/ledger"
)

// B822: a found block's acceptance file carries the red test the evidence
// names; with no name it carries the needs-test marker.

// b822Accept is finding i's acceptance text, the tests of its one clause
// (it must parse as block open reads it) and its brief.
func b822Accept(t *testing.T, f *hvFixture, i int) (string, []any, string) {
	t.Helper()
	found := b800Found(t, f)
	if len(found) <= i {
		t.Fatalf("found = %v", found)
	}
	data, err := os.ReadFile(found[i]["acceptance"].(string))
	if err != nil {
		t.Fatal(err)
	}
	brief, _ := os.ReadFile(found[i]["brief"].(string))
	parsed, err := blockopen.ParseAcceptance(data)
	if err == nil {
		_, err = blockopen.Acceptance(parsed)
	}
	clauses, _ := parsed.([]any)
	if err != nil || len(clauses) != 1 {
		t.Fatalf("acceptance = %q (%v)", data, err)
	}
	obj, _ := clauses[0].(ledger.Object)
	tests, _ := obj.Get("tests").([]any)
	return string(data), tests, string(brief)
}

func TestB822AcceptanceNamesEvidenceTest(t *testing.T) {
	f := b800Fixture(t, map[string]any{"autopilot": b950Grant()})
	// a `--- FAIL:` line in the evidence
	if code, out, errText := autopilotCall(t, "found", "--run", "r-test", "--severity", "customer-breaking", "--source", "gate", "--title", "the test gate is red",
		"--evidence", "=== RUN   TestLandKeepsURL\n--- FAIL: TestLandKeepsURL (0.01s)\n    land_test.go:40: url dropped\nFAIL"); code != 0 || !strings.Contains(out, " queued") {
		t.Fatalf("found = %d %q %q", code, out, errText)
	}
	// a findings.jsonl row as the evidence file
	row := filepath.Join(f.home, "findings.jsonl")
	if err := os.WriteFile(row, []byte(`{"source":"flaky-test","evidence":{"test":"TestQueueDrains","packages":["./worker"]}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, errText := autopilotCall(t, "found", "--run", "r-test", "--severity", "customer-breaking", "--source", "flaky", "--title", "the queue test is flaky", "--evidence-file", row); code != 0 || !strings.Contains(out, " queued") {
		t.Fatalf("found = %d %q %q", code, out, errText)
	}
	// no test name: the needs-test marker
	if code, out, errText := autopilotCall(t, "found", "--run", "r-test", "--severity", "customer-breaking", "--source", "land", "--title", "land drops the PR url",
		"--evidence", "block B1 land stopped at merge: the PR url is empty"); code != 0 || !strings.Contains(out, " queued") {
		t.Fatalf("found = %d %q %q", code, out, errText)
	}
	for i, want := range [][]any{{"TestLandKeepsURL"}, {"TestQueueDrains"}, {"needs-test"}} {
		text, tests, brief := b822Accept(t, f, i)
		if !reflect.DeepEqual(tests, want) {
			t.Fatalf("finding %d tests = %v, want %v\n%s", i, tests, want, text)
		}
		if strings.Contains(text, "red-test-from-the-evidence") {
			t.Fatalf("finding %d keeps the placeholder:\n%s", i, text)
		}
		marker := strings.Contains(text, "# needs-test:") && strings.Contains(brief, "`needs-test`")
		if marker != (want[0] == "needs-test") {
			t.Fatalf("finding %d marker = %v\n%s\n%s", i, marker, text, brief)
		}
		if want[0] != "needs-test" && !strings.Contains(brief, "`"+want[0].(string)+"`") {
			t.Fatalf("finding %d brief misses the test:\n%s", i, brief)
		}
	}
}
