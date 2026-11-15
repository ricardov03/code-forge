package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B714: close cases B709 left to Node run in Go. Every case runs on the B684
// fixture; testdata/b714/<case>.txt is Node's run of the same fixture
// (B714_RECORD_NODE=<repo root>, Node's runBlock in process with the terminal
// and the answer injected).
func b714LengthChecks(length any) func(f *b684Fixture) {
	return b697Checks(ledger.Object{{Key: "a", Value: "look"}, {Key: "length", Value: length}})
}

var b714CloseCases = []struct {
	name, ask, args string
	setup           b684Setup
	after           func(f *b684Fixture)
}{
	// human_checks is an object with its own length key: Node's closeBlock
	// compares .length > 0 (JavaScript's loose number reading), then its
	// output loop cannot iterate an object.
	{"hcl-zero", "notty", "", b684Setup{review: b684Approved}, b714LengthChecks(float64(0))},
	{"hcl-two", "notty", "", b684Setup{review: b684Approved}, b714LengthChecks(float64(2))},
	{"hcl-two-json", "notty", "|--json", b684Setup{review: b684Approved}, b714LengthChecks(float64(2))},
	{"hcl-text-number", "notty", "", b684Setup{review: b684Approved}, b714LengthChecks(" 3 ")},
	{"hcl-text", "notty", "", b684Setup{review: b684Approved}, b714LengthChecks("abc")},
	{"hcl-hex", "notty", "", b684Setup{review: b684Approved}, b714LengthChecks("0x10")},
	{"hcl-true", "notty", "", b684Setup{review: b684Approved}, b714LengthChecks(true)},
	{"hcl-false", "notty", "", b684Setup{review: b684Approved}, b714LengthChecks(false)},
	{"hcl-null", "notty", "", b684Setup{review: b684Approved}, b714LengthChecks(nil)},
	{"hcl-negative", "notty", "", b684Setup{review: b684Approved}, b714LengthChecks(float64(-1))},
	{"hcl-array-one", "notty", "", b684Setup{review: b684Approved}, b714LengthChecks([]any{float64(1)})},
	{"hcl-array-two", "notty", "", b684Setup{review: b684Approved}, b714LengthChecks([]any{float64(1), float64(2)})},
	{"hcl-array-empty", "notty", "", b684Setup{review: b684Approved}, b714LengthChecks([]any{})},
	{"hcl-object", "notty", "", b684Setup{review: b684Approved}, b714LengthChecks(ledger.Object{{Key: "n", Value: float64(1)}})},
	{"hcl-infinity", "notty", "", b684Setup{review: b684Approved}, b714LengthChecks("Infinity")},
}

func TestB714CloseMatchesNode(t *testing.T) {
	if os.Getenv("BLOCK_HV_CHILD") == "1" {
		b684Child()
	}
	record := os.Getenv("B714_RECORD_NODE")
	for _, c := range b714CloseCases {
		t.Run(c.name, func(t *testing.T) {
			f := newB684Fixture(t, c.setup)
			if c.after != nil {
				c.after(f)
			}
			args := f.args(c.args)
			before := len(f.ledgerLines())
			golden := filepath.Join("testdata", "b714", c.name+".txt")
			var r hvResult
			if record != "" {
				r = f.nodeRun(record, c.ask, args)
			} else {
				r = f.goRun("TestB714CloseMatchesNode", c.ask, args)
			}
			got := b697Mac.ReplaceAllString(f.report(r, before), `"mac":"<MAC>"`)
			if record != "" {
				f.must(os.MkdirAll(filepath.Dir(golden), 0o755))
				f.must(os.WriteFile(golden, []byte(got), 0o644))
				return
			}
			want, err := os.ReadFile(golden)
			f.must(err)
			if got != string(want) {
				t.Fatalf("report differs from Node's\n--- got\n%s\n--- want (Node)\n%s", got, want)
			}
		})
	}
}
