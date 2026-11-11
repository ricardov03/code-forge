package gates

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func graphFixture(t *testing.T, files map[string]string) (string, []string) {
	t.Helper()
	root := t.TempDir()
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
	return root, listed
}

func TestGraphDependentsFollowsImportsTransitively(t *testing.T) {
	root, listed := graphFixture(t, map[string]string{
		"src/a.mjs":              "export const a = 1;\n",
		"src/b.mjs":              "import { a } from './a.mjs';\nexport const b = a;\n",
		"src/c.mjs":              "export * from \"./b.mjs\";\n",
		"src/other.mjs":          "export const o = 1;\n",
		"test/x.test.mjs":        "import { b } from '../src/b.mjs';\n",
		"test/via-c.test.mjs":    "import '../src/c.mjs';\n",
		"test/dynamic.test.mjs":  "const m = await import('../src/a.mjs');\n",
		"test/url.test.mjs":      "const cli = new URL('../src/a.mjs', import.meta.url);\n",
		"test/dir.test.mjs":      "const dir = new URL('../src/', import.meta.url);\n",
		"test/require.test.cjs":  "const a = require('../src/a');\n",
		"test/named.test.mjs":    "spawnSync('node', ['src/a.mjs']);\n",
		"test/named-b.test.mjs":  "spawnSync('node', ['src/b.mjs']);\n",
		"test/computed.test.mjs": "const name = '../src/' + 'a.mjs'; await import(name);\n",
		"test/y.test.mjs":        "import { o } from '../src/other.mjs';\n",
		"test/helpers/h.mjs":     "export { b } from '../../src/b.mjs';\n",
		"test/h.test.mjs":        "import { b } from './helpers/h.mjs';\n",
	})
	g := NewGraph(root, listed)
	got := g.Dependents([]string{"src/a.mjs"})
	// named-b names src/b.mjs, which is only in the closure: a path mention
	// counts for the changed file itself, not for every file it reaches.
	want := []string{"test/dir.test.mjs", "test/dynamic.test.mjs", "test/h.test.mjs", "test/named.test.mjs", "test/require.test.cjs", "test/url.test.mjs", "test/via-c.test.mjs", "test/x.test.mjs"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Dependents(src/a.mjs):\n got %v\nwant %v", got, want)
	}
	if got := g.Dependents([]string{"test/y.test.mjs"}); !reflect.DeepEqual(got, []string{"test/y.test.mjs"}) {
		t.Fatalf("a changed test is its own dependent; got %v", got)
	}
	if got := g.Dependents([]string{"README.md"}); len(got) != 0 {
		t.Fatalf("an unreferenced file has no dependents; got %v", got)
	}
}

func TestGraphNativeTestsFindsMarkersThroughHelpers(t *testing.T) {
	root, listed := graphFixture(t, map[string]string{
		"test/helpers/bin.mjs":   "export const bin = 'native/bin/code-forge-native';\n",
		"test/helpers/wrap.mjs":  "export * from './bin.mjs';\n",
		"test/p.test.mjs":        "import { bin } from './helpers/wrap.mjs';\n",
		"test/q.test.mjs":        "spawnSync('node', ['scripts/build-native.mjs', '--host']);\n",
		"test/g.test.mjs":        "spawnSync('go', ['build', './cmd/code-forge-native']);\n",
		"test/r.test.mjs":        "import test from 'node:test';\n",
		"src/uses-native.mjs":    "const bin = 'code-forge-native';\n",
		"test/s.test.mjs":        "import '../src/uses-native.mjs';\n",
		"test/compat/c.test.mjs": "import { bin } from '../helpers/bin.mjs';\n",
	})
	got := NewGraph(root, listed).NativeTests()
	// s.test.mjs reaches the marker only through src/, which is product code,
	// not a test helper.
	want := []string{"test/compat/c.test.mjs", "test/g.test.mjs", "test/p.test.mjs", "test/q.test.mjs"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NativeTests:\n got %v\nwant %v", got, want)
	}
}

// B330 fact: test/gates/changed-scope-rebase.test.mjs runs `go build
// ./cmd/code-forge-native` and was never selected by a native change.
func TestGraphNativeTestsSelectsRepositoryNativeTests(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	listed, err := List(root)
	if err != nil {
		t.Skipf("not a git checkout: %v", err)
	}
	got := map[string]bool{}
	tests := NewGraph(root, listed).NativeTests()
	for _, f := range tests {
		got[f] = true
	}
	t.Logf("%d native tests: %v", len(tests), tests)
	for _, f := range []string{"test/gates/changed-scope-rebase.test.mjs", "test/gates/stale-base.test.mjs", "test/docs-verbs.test.mjs", "test/bench-verbs.test.mjs", "test/cli/help-go-verbs.test.mjs", "test/helpers/go-binary.test.mjs", "test/compat/go.test.mjs"} {
		if !got[f] {
			t.Errorf("NativeTests misses %s", f)
		}
	}
	for _, f := range []string{"test/helpers/go-binary.mjs", "test/config/cli-validate.test.mjs"} {
		if got[f] {
			t.Errorf("NativeTests must not hold %s", f)
		}
	}
}

func TestGraphInputs(t *testing.T) {
	for p, want := range map[string]bool{
		"native/gates/scope.go": true, "native/go.mod": true, "scripts/build-native.mjs": true, "native/": true, "native/**": true,
		"native/gates/scope_test.go": false, "native/README.md": false, "src/a.mjs": false, "scripts/compat-golden.mjs": false,
	} {
		if NativeInput(p) != want {
			t.Errorf("NativeInput(%q) = %v, want %v", p, !want, want)
		}
	}
	for p, want := range map[string]bool{
		"test/fixtures/a/.code-forge.yml": true, "schema/config.json": true, "scripts/compat-golden.mjs": true, "src/config/load.mjs": true,
		"src/cli/gates.mjs": false, "test/config/load.test.mjs": false,
	} {
		if GoldenInput(p) != want {
			t.Errorf("GoldenInput(%q) = %v, want %v", p, !want, want)
		}
	}
	for p, want := range map[string]bool{"tests/Feature/LoginTest.php": true, "a/b_test.go": true, "test/x.test.mjs": true, "test/helpers/h.mjs": false} {
		if IsTestName(p) != want {
			t.Errorf("IsTestName(%q) = %v, want %v", p, !want, want)
		}
	}
}
