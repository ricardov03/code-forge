package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ricardov03/code-forge/native/config"
)

// B427: gates.import_graph.stop ends the closure at a hub. The hub's own test
// still runs; the product file that imports the hub, and its test, do not.
// A change to the hub itself goes on past it.
func TestGatesSelectImportGraphStop(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		".code-forge.yml":    "version: 1\nprovider: anthropic\nlevels:\n  L0: {model: m0}\n  L1: {model: m1}\n  L2: {model: m2}\n  L3: {model: m3}\ngates:\n  import_graph:\n    stop:\n      - src/hub.mjs\n",
		"src/leaf.mjs":       "export function leafFn() { return 1; }\n",
		"src/hub.mjs":        "import { leafFn } from './leaf.mjs';\nexport function hubFn() { return leafFn(); }\n",
		"src/app.mjs":        "import { hubFn } from './hub.mjs';\nexport function run() { return hubFn(); }\n",
		"test/leaf.test.mjs": "import { leafFn } from '../src/leaf.mjs';\n",
		"test/hub.test.mjs":  "import { hubFn } from '../src/hub.mjs';\n",
		"test/app.test.mjs":  "import { run } from '../src/app.mjs';\n",
	}
	listed := []string{}
	for name, body := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		listed = append(listed, name)
	}
	loaded, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.ValidateSchema(loaded, "0.0.0"); err != nil {
		t.Fatalf("gates.import_graph.stop is refused by the schema: %v", err)
	}
	got := gatesSelectFiles(root, listed, gatesSelection{changed: []string{"src/leaf.mjs"}})
	want := []string{"test/leaf.test.mjs", "test/hub.test.mjs"} // stem pair first
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("leaf change with stop src/hub.mjs: got %d files %v, want %d files %v", len(got), got, len(want), want)
	}
	got = gatesSelectFiles(root, listed, gatesSelection{changed: []string{"src/hub.mjs"}})
	want = []string{"test/hub.test.mjs", "test/app.test.mjs"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("change to the stop file itself: got %d files %v, want %d files %v", len(got), got, len(want), want)
	}
}
