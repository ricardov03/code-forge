package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ricardov03/code-forge/native/canon"
)

// B732: testdata/b732/parse.json holds a source with \udXXX texts in every
// scalar style and JSON.stringify of the yaml package's parse() of it (Node
// 24, yaml 2.9.1; keys in sorted order, so it is the canonical JSON): a lone
// escape stays a lone surrogate, a pair is one character, an escaped
// backslash and the plain, single-quoted and block styles keep the text.
func TestB732ParseYAMLSurrogateEscapesAsNode(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "b732", "parse.json"))
	if err != nil {
		t.Fatal(err)
	}
	var golden struct{ Src, JSON string }
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatal(err)
	}
	for _, ordered := range []bool{false, true} {
		var value any
		if ordered {
			mapping, _, err := ParseTextOrdered(golden.Src, "x.yml")
			if err != nil {
				t.Fatalf("ParseTextOrdered: %v", err)
			}
			delete(mapping, "version") // migrateConfig's version: 1
			value = mapping
		} else if value, err = ParseYAML(golden.Src); err != nil {
			t.Fatalf("ParseYAML: %v", err)
		}
		got, err := canon.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != golden.JSON {
			t.Fatalf("ordered %v:\n got %s\nwant %s (Node)", ordered, got, golden.JSON)
		}
	}
}

// B732: the swap is never seen in a source that holds the slot text or a
// slot character itself: the parse fails as before.
func TestB732ParseYAMLSlotTextKeepsRefusal(t *testing.T) {
	for _, src := range []string{"a: \"\\ud800\"\nb: \\U000F0000\n", "a: \"\\ud800\"\nb: \U000F0000\n"} {
		if _, err := ParseYAML(src); err == nil {
			t.Fatalf("%q parsed", src)
		}
	}
}
