package yamledit

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B732: testdata/b732.json holds what the yaml package (editDoc, as
// limits.mjs) writes for strings with a \uD800-\uDFFF escape (made by
// testdata/gen_b732.mjs.txt, yaml 2.9.1): a lone surrogate set as a value,
// and a file that already holds one in a double-quoted scalar.
type b732Case struct {
	Name  string   `json:"name"`
	Text  string   `json:"text"`
	Segs  []string `json:"segs"`
	Op    string   `json:"op"`
	Value string   `json:"value"` // the JSON text of the value
	Out   *string  `json:"out"`
}

// b732Refused are cases Go hands over (a named refusal) where Node writes:
// an escape JSON.stringify does not write (\uD800 upper case, \n), a pair
// escape (one character in JavaScript, written plain) and a scalar long
// enough to fold, as for any string.
var b732Refused = map[string]bool{"read pair escape": true, "read upper escape": true, "read other escape": true, "very long": true}

func TestB732SurrogateStringsAsNode(t *testing.T) {
	data, err := os.ReadFile("testdata/b732.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []b732Case
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 18 {
		t.Fatalf("%d cases", len(cases))
	}
	for _, c := range cases {
		var out string
		var err error
		if c.Op == "set" {
			obj, perr := ledger.ParseObject([]byte(`{"v":` + c.Value + `}`))
			if perr != nil {
				t.Fatal(perr)
			}
			out, err = Set(c.Text, c.Segs, obj.Get("v"))
		} else {
			out, err = Remove(c.Text, c.Segs, 0)
		}
		var u *Unsupported
		switch {
		case c.Out == nil:
			t.Fatalf("%s: Node fails", c.Name)
		case b732Refused[c.Name]:
			if !errors.As(err, &u) {
				t.Errorf("%s: want a named refusal, got %q %v", c.Name, out, err)
			}
		case err != nil:
			t.Errorf("%s: Node writes %q, Go error %v", c.Name, *c.Out, err)
		case out != *c.Out:
			t.Errorf("%s:\n node %q\n go   %q", c.Name, *c.Out, out)
		}
	}
}

// B732: a WTF-8 high surrogate followed by a low one is one character in
// JavaScript, which the yaml package writes plain; Go refuses it, as any
// string outside printable ASCII.
func TestB732SurrogatePairBytesRefused(t *testing.T) {
	_, err := Set("version: 1\n", []string{"name"}, "\xed\xa0\xbd\xed\xb8\x80")
	var u *Unsupported
	if !errors.As(err, &u) {
		t.Fatalf("err = %v, want a refusal", err)
	}
}
