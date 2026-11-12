package yamledit

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

type goldenCase struct {
	Name  string          `json:"name"`
	Text  string          `json:"text"`
	Segs  []string        `json:"segs"`
	Op    string          `json:"op"`
	Value json.RawMessage `json:"value"`
	Prune int             `json:"prune"`
	Out   *string         `json:"out"`
	Error string          `json:"error"`
}

func loadGolden(t *testing.T) []goldenCase {
	t.Helper()
	file := "testdata/golden.json"
	if alt := os.Getenv("YAMLEDIT_GOLDEN"); alt != "" {
		file = alt // a wider run made by gen.mjs with another SEED and COUNT
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var cases []goldenCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	return cases
}

// jsonValue decodes a JSON value keeping object key order (as JS does).
func jsonValue(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	obj, err := ledger.DecodeObject([]byte(`{"v":` + string(raw) + `}`))
	if err != nil {
		t.Fatal(err)
	}
	return obj.Get("v")
}

func run(t *testing.T, c goldenCase) (string, error) {
	if c.Op == "set" {
		return Set(c.Text, c.Segs, jsonValue(t, c.Value))
	}
	return Remove(c.Text, c.Segs, c.Prune)
}

// TestGoldenNodeBytes runs every case Node's own code made (testdata/gen.mjs):
// Go writes Node's exact bytes or refuses with a named reason, never other
// bytes; where Node throws, Go reports ErrNotMapping or refuses.
func TestGoldenNodeBytes(t *testing.T) {
	served, refused := 0, map[string]int{}
	for _, c := range loadGolden(t) {
		out, err := run(t, c)
		var u *Unsupported
		switch {
		case err != nil && errors.As(err, &u):
			refused[u.Reason]++
			if c.Out == nil {
				continue
			}
		case c.Error == "throw":
			if !errors.Is(err, ErrNotMapping) {
				t.Errorf("%s: Node throws, Go gave out=%q err=%v", c.Name, out, err)
			}
			continue
		case c.Error != "":
			t.Errorf("%s: Node fails (%s), Go gave out=%q err=%v", c.Name, c.Error, out, err)
			continue
		case err != nil:
			t.Errorf("%s: Node writes %q, Go error %v", c.Name, *c.Out, err)
			continue
		case out != *c.Out:
			t.Errorf("%s:\n text %q\n node %q\n go   %q", c.Name, c.Text, *c.Out, out)
			continue
		default:
			served++
			continue
		}
	}
	t.Logf("served %d byte-equal; refusals by reason: %v", served, refused)
}

// TestGoldenServesCommonShapes keeps the shapes a real .code-forge.yml uses
// in Go: these cases must be written, not handed over.
func TestGoldenServesCommonShapes(t *testing.T) {
	fixtures := []string{"real", "comments", "quoted", "flow", "flow full", "flow mixed", "blank lines", "numbers", "nulls bools", "doc start comment",
		"doc end comment", "doc end before", "doc end after", "doc end blank", "seq comments", "seq zero indent", "crlf comments", "empty comment", "deep"}
	refusedOK := map[string]bool{
		// Node writes these; Go hands them over (named reasons, listed in the report).
	}
	total := 0
	for _, c := range loadGolden(t) {
		common := false
		for _, f := range fixtures {
			for _, v := range []string{" set ", " remove ", " crlf ", " nofinal "} {
				common = common || strings.HasPrefix(c.Name, f+v)
			}
		}
		if !common || c.Out == nil {
			continue
		}
		total++
		if _, err := run(t, c); err != nil && !refusedOK[c.Name] {
			t.Errorf("%s: Node writes it, Go refuses: %v", c.Name, err)
		}
	}
	if total == 0 {
		t.Fatal("no common cases")
	}
}

func TestRefusesShapesOutsideTheSubset(t *testing.T) {
	for _, text := range []string{
		"a:\t1\n", "a: [1, [2], ]\n", "- [a]\n", "a: &x 1\nb: *x\n", "a: |\n  text\n", "a: café\n", "- a\n", "a: 1\n---\nb: 2\n",
		"a: \"x\\ty\"\n", "a: 0x1f\n", "a: 1e3\n", "\ufeffa: 1\n", "a: 1\rb: 2\n", "? a\n: 1\n",
	} {
		if _, err := Set(text, []string{"z"}, 1.0); !IsUnsupported(err) {
			t.Errorf("%q: want a named refusal, got %v", text, err)
		}
	}
}

func TestNotMappingParent(t *testing.T) {
	if _, err := Set("a: 1\n", []string{"a", "b"}, 1.0); !errors.Is(err, ErrNotMapping) {
		t.Fatalf("set under a scalar: %v", err)
	}
	if _, err := Remove("a: 1\n", []string{"b", "c"}, 0); !errors.Is(err, ErrNotMapping) {
		t.Fatalf("remove under a missing key: %v", err)
	}
}
