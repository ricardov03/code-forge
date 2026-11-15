package review

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/ricardov03/code-forge/native/jsonx"
)

// TestB720JSONParseMessageMatchesNode checks JSONParseMessage gives V8's
// exact JSON.parse message (Node v24, testdata/b720_json.js) and fails
// exactly when jsonx.Parse, the value reader, fails.
func TestB720JSONParseMessageMatchesNode(t *testing.T) {
	raw, err := os.ReadFile("testdata/b720_json.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Src     string  `json:"src"`
		Message *string `json:"message"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	bad := 0
	for _, c := range cases {
		got, failed := JSONParseMessage(c.Src)
		_, perr := jsonx.Parse(c.Src)
		want := ""
		if c.Message != nil {
			want = *c.Message
		}
		if failed != (c.Message != nil) || got != want || (perr != nil) != failed {
			bad++
			if bad <= 10 {
				t.Errorf("%q: got %q (%v), want %q, jsonx error %v", c.Src, got, failed, want, perr)
			}
		}
	}
	if len(cases) < 5000 || bad > 0 {
		t.Fatalf("%d of %d differ", bad, len(cases))
	}
}
