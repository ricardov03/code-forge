package gates

import (
	"fmt"
	"reflect"
	"testing"
)

// B427: a hub imported by 10 tests re-exports a leaf by name; one test imports
// the leaf's binding through the hub. A change to the leaf selects that one
// test, not the 10 that use only the hub's own export.
func TestGraphDependentsNamedExportThroughHub(t *testing.T) {
	files := map[string]string{
		"src/leaf.mjs": "export function leafFn() { return 1; }\n",
		"src/util.mjs": "export const fmt = (n) => String(n);\n",
		"src/hub.mjs": "import { fmt } from './util.mjs';\n" +
			"export { leafFn } from './leaf.mjs';\n" +
			"export function hubFn() { return fmt(2); }\n",
		"test/leaf-user.test.mjs": "import { leafFn } from '../src/hub.mjs';\nleafFn();\n",
	}
	for i := 1; i <= 10; i++ {
		files[fmt.Sprintf("test/hub-%02d.test.mjs", i)] = "import { hubFn } from '../src/hub.mjs';\nhubFn();\n"
	}
	root, listed := graphFixture(t, files)
	g := NewGraph(root, listed)
	got := g.Dependents([]string{"src/leaf.mjs"})
	if want := []string{"test/leaf-user.test.mjs"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Dependents(src/leaf.mjs): got %d files %v, want 1 file %v", len(got), got, want)
	}
	// The hub's own code uses util.mjs, so a util change reaches the 10 hub
	// tests and not the leaf user.
	got = g.Dependents([]string{"src/util.mjs"})
	if len(got) != 10 {
		t.Fatalf("Dependents(src/util.mjs): got %d files %v, want the 10 hub tests", len(got), got)
	}
	for _, f := range got {
		if f == "test/leaf-user.test.mjs" {
			t.Fatalf("Dependents(src/util.mjs) has test/leaf-user.test.mjs, which uses only leafFn: %v", got)
		}
	}
}

// B427: a pure re-export barrel (export * and export { } from) hands on its
// sources' bindings; an importer reaches a source only through a name that
// source provides, or by importing the barrel as a whole.
func TestGraphDependentsReexportBarrel(t *testing.T) {
	root, listed := graphFixture(t, map[string]string{
		"src/a.mjs":            "export function a1() {}\nexport function a2() {}\n",
		"src/b.mjs":            "export function b1() {}\n",
		"src/index.mjs":        "export * from './a.mjs';\nexport { b1 as bee } from './b.mjs';\n",
		"test/a1.test.mjs":     "import { a1 } from '../src/index.mjs';\n",
		"test/a2.test.mjs":     "import { a2 as two } from '../src/index.mjs';\n",
		"test/bee.test.mjs":    "import { bee } from '../src/index.mjs';\n",
		"test/ns.test.mjs":     "import * as all from '../src/index.mjs';\n",
		"test/side.test.mjs":   "import '../src/index.mjs';\n",
		"test/await.test.mjs":  "const { bee } = await import('../src/index.mjs');\n",
		"test/helpers/h.mjs":   "export { a1 } from '../../src/index.mjs';\n",
		"test/via-h.test.mjs":  "import { a1 } from './helpers/h.mjs';\n",
		"test/other.test.mjs":  "import { b1 } from '../src/b.mjs';\n",
		"test/direct.test.mjs": "import { a2 } from '../src/a.mjs';\n",
	})
	g := NewGraph(root, listed)
	got := g.Dependents([]string{"src/a.mjs"})
	want := []string{"test/a1.test.mjs", "test/a2.test.mjs", "test/direct.test.mjs", "test/ns.test.mjs", "test/side.test.mjs", "test/via-h.test.mjs"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Dependents(src/a.mjs): got %d files %v, want %d files %v", len(got), got, len(want), want)
	}
	got = g.Dependents([]string{"src/b.mjs"})
	want = []string{"test/await.test.mjs", "test/bee.test.mjs", "test/ns.test.mjs", "test/other.test.mjs", "test/side.test.mjs"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Dependents(src/b.mjs): got %d files %v, want %d files %v", len(got), got, len(want), want)
	}
}

// B427: inside one module, only the exports whose code reaches the changed
// binding carry the change on; code that runs at load carries it to every
// importer.
func TestGraphDependentsBindingLevel(t *testing.T) {
	root, listed := graphFixture(t, map[string]string{
		"src/leaf.mjs":        "export function leafFn() { return 1; }\n",
		"src/other.mjs":       "export function other() { return 2; }\n",
		"src/mid.mjs":         "import { leafFn } from './leaf.mjs';\nimport { other } from './other.mjs';\nexport function uses() { return leafFn(); }\nexport function viaUses() { return uses(); }\nexport function apart() { return other(); }\n",
		"src/load.mjs":        "import { leafFn } from './leaf.mjs';\nexport const value = leafFn();\nexport function plain() { return 3; }\n",
		"test/uses.test.mjs":  "import { uses } from '../src/mid.mjs';\n",
		"test/via.test.mjs":   "import { viaUses } from '../src/mid.mjs';\n",
		"test/apart.test.mjs": "import { apart } from '../src/mid.mjs';\n",
		"test/plain.test.mjs": "import { plain } from '../src/load.mjs';\n",
	})
	g := NewGraph(root, listed)
	got := g.Dependents([]string{"src/leaf.mjs"})
	want := []string{"test/plain.test.mjs", "test/uses.test.mjs", "test/via.test.mjs"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Dependents(src/leaf.mjs): got %d files %v, want %d files %v (apart uses only other(); load.mjs runs leafFn at load)", len(got), got, len(want), want)
	}
}

// B427: a path in a comment, or a JSDoc import() type, is no edge; the same
// path in code still is.
func TestGraphDependentsIgnoresComments(t *testing.T) {
	root, listed := graphFixture(t, map[string]string{
		"src/a.mjs":               "export const a = 1;\n",
		"src/doc.mjs":             "// see src/a.mjs\n/** @param {import('./a.mjs').A} x */\nexport function d(x) { return x; }\n",
		"test/doc.test.mjs":       "import { d } from '../src/doc.mjs';\n",
		"test/comment.test.mjs":   "// covers src/a.mjs too\nexport {};\n",
		"test/code-name.test.mjs": "spawnSync('node', ['src/a.mjs']);\n",
	})
	g := NewGraph(root, listed)
	got := g.Dependents([]string{"src/a.mjs"})
	if want := []string{"test/code-name.test.mjs"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Dependents(src/a.mjs): got %d files %v, want 1 file %v", len(got), got, want)
	}
}
