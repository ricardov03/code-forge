package canon_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/ricardov03/code-forge/native/canon"
)

func TestGolden(t *testing.T) {
	data, err := os.ReadFile("../../test/compat/golden/canon.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct{ Name, Input, Expected string }
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 20 {
		t.Fatalf("canonical cases: got %d, want 20", len(cases))
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			var value any
			if err := json.Unmarshal([]byte(c.Input), &value); err != nil {
				t.Fatal(err)
			}
			got, err := canon.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != c.Expected {
				t.Fatalf("canonical bytes: got %q, want %q", got, c.Expected)
			}
		})
	}
}
