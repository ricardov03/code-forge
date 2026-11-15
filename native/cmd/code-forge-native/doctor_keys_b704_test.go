package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// TestB704KeyIndexLoneSurrogateInGo: an index.json whose key and value hold
// lone surrogate escapes is read as readIndex's JSON.parse reads it and
// rewritten as JSON.stringify(index, null, 2) writes it (the want text is
// Node's output for the same input and change), instead of handing over.
func TestB704KeyIndexLoneSurrogateInGo(t *testing.T) {
	home := t.TempDir()
	file := filepath.Join(home, ".code-forge", "store", "index.json")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	in := "{\"a\\ud83d\":{\"source\":\"env\",\"note\":\"x\\udc00\"},\"2\":{\"source\":\"op\"}}"
	if err := os.WriteFile(file, []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	if !keyIndexReadable(home, "jev", true) || !keyIndexReadable(home, "a\xed\xa0\xbd", true) {
		t.Fatal("index with a lone surrogate not readable in Go")
	}
	err := updateKeyIndex(home, func(index *ledger.Object) {
		index.Set("jev", ledger.Object{{Key: "source", Value: "op"}, {Key: "backend", Value: "file"}, {Key: "exp", Value: nil}})
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"2\": {\n    \"source\": \"op\"\n  },\n  \"a\\ud83d\": {\n    \"source\": \"env\",\n    \"note\": \"x\\udc00\"\n  },\n  \"jev\": {\n    \"source\": \"op\",\n    \"backend\": \"file\",\n    \"exp\": null\n  }\n}"
	if string(got) != want {
		t.Fatalf("index.json\n got %q\nwant %q", got, want)
	}
}
