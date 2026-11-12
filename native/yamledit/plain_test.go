package yamledit

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// TestB527StringifyPlainMatchesNode checks StringifyPlain against the bytes
// the yaml package's stringify(value, {lineWidth: 0}) wrote for each value
// (testdata/plain_golden.json, made by testdata/gen_plain.mjs). A value Go
// refuses must be one outside the served subset.
func TestB527StringifyPlainMatchesNode(t *testing.T) {
	data, err := os.ReadFile("testdata/plain_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name  string          `json:"name"`
		Value json.RawMessage `json:"value"`
		Out   string          `json:"out"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	served := 0
	for _, c := range cases {
		value, err := ledger.DecodeValue(c.Value)
		if err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		out, err := StringifyPlain(value)
		if err != nil {
			if !IsUnsupported(err) {
				t.Fatalf("%s: %v", c.Name, err)
			}
			if c.Name == "empty root" || c.Name == "config shape" || c.Name == "gate words" || c.Name == "long string" || c.Name == "nested empty" {
				t.Fatalf("%s: refused: %v", c.Name, err)
			}
			continue
		}
		served++
		if out != c.Out {
			t.Fatalf("%s:\n go: %q\nnode: %q", c.Name, out, c.Out)
		}
	}
	if served < len(cases)/2 {
		t.Fatalf("served %d of %d cases", served, len(cases))
	}
	t.Logf("served %d of %d cases", served, len(cases))
}
