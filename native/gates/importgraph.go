package gates

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// B404: the static import graph of a JavaScript tree, read in pure Go (no Node
// process). Edges come from static imports and re-exports (`from '…'`),
// side-effect imports (`import '…'`), literal dynamic imports (`import('…')`),
// `require('…')` and `new URL('…', import.meta.url)`. Only relative specifiers
// ('./', '../') make edges; a specifier that ends in '/' names a directory and
// covers every file under it. A computed specifier makes no edge.

var (
	graphFrom    = regexp.MustCompile(`\bfrom\s*['"]([^'"\n]+)['"]`)
	graphBare    = regexp.MustCompile(`\bimport\s*['"]([^'"\n]+)['"]`)
	graphDynamic = regexp.MustCompile(`\b(?:import|require)\s*\(\s*['"]([^'"\n]+)['"]\s*[,)]`)
	graphURL     = regexp.MustCompile(`\bnew\s+URL\s*\(\s*['"]([^'"\n]+)['"]`)
	graphModule  = regexp.MustCompile(`\.(mjs|js|cjs)$`)
	graphTest    = regexp.MustCompile(`\.(test|spec)\.(mjs|js|cjs)$`)
)

// NativeMarkers name the native build, the native binary or the native binary
// path helper (test/helpers/go-binary.mjs) in a test or test helper: a file
// that holds one builds or runs the native binary.
var NativeMarkers = []string{"cmd/code-forge-native", "build-native.mjs", "code-forge-native", "go-binary.mjs"}

// Graph is the reverse import graph over the listed JavaScript files.
type Graph struct {
	listed    map[string]bool
	content   map[string]string
	importers map[string][]string // target file -> files that import it
	dirs      map[string][]string // target directory ("x/") -> files that name it
}

// NewGraph reads every listed .mjs/.js/.cjs file under cwd. A file that cannot
// be read has no edges.
func NewGraph(cwd string, listed []string) *Graph {
	g := &Graph{listed: map[string]bool{}, content: map[string]string{}, importers: map[string][]string{}, dirs: map[string][]string{}}
	for _, f := range listed {
		g.listed[f] = true
	}
	for _, f := range listed {
		if !graphModule.MatchString(f) || strings.HasPrefix(f, "node_modules/") || strings.Contains(f, "/node_modules/") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(cwd, filepath.FromSlash(f)))
		if err != nil {
			continue
		}
		text := string(data)
		g.content[f] = text
		seen := map[string]bool{}
		for _, re := range []*regexp.Regexp{graphFrom, graphBare, graphDynamic, graphURL} {
			for _, m := range re.FindAllStringSubmatch(text, -1) {
				target, dir := g.resolve(f, m[1])
				if target == "" || seen[target] {
					continue
				}
				seen[target] = true
				if dir {
					g.dirs[target] = append(g.dirs[target], f)
				} else {
					g.importers[target] = append(g.importers[target], f)
				}
			}
		}
	}
	return g
}

// resolve turns a relative specifier in file into a listed repository path,
// or a directory prefix ending in '/'.
func (g *Graph) resolve(file, spec string) (string, bool) {
	if !strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../") {
		return "", false
	}
	if i := strings.IndexAny(spec, "?#"); i >= 0 {
		spec = spec[:i]
	}
	joined := path.Clean(path.Join(path.Dir(file), spec))
	if joined == "." || strings.HasPrefix(joined, "../") || joined == ".." {
		return "", false
	}
	if strings.HasSuffix(spec, "/") {
		return joined + "/", true
	}
	for _, candidate := range []string{joined, joined + ".mjs", joined + ".js", joined + ".cjs", joined + "/index.mjs", joined + "/index.js"} {
		if g.listed[candidate] {
			return candidate, false
		}
	}
	// A listed file under the path makes it a directory reference.
	prefix := joined + "/"
	for f := range g.listed {
		if strings.HasPrefix(f, prefix) {
			return prefix, true
		}
	}
	return "", false
}

// Importers returns the files that import f directly, including through a
// directory reference that covers f.
func (g *Graph) Importers(f string) []string {
	return append(append([]string{}, g.importers[f]...), g.dirImporters(f)...)
}

// mentions returns the files that name f by its repository path in their text
// (a test that spawns a script or reads a fixture by path). Only the changed
// files themselves are matched this way, not the whole closure.
func (g *Graph) mentions(f string) []string {
	out := []string{}
	if !strings.Contains(f, "/") {
		return out
	}
	for file, text := range g.content {
		if file != f && strings.Contains(text, f) {
			out = append(out, file)
		}
	}
	return out
}

// IsTestName reports a file whose name marks a test (foo.test.mjs,
// FooTest.php, foo_test.go, test_foo.py), wherever it lives.
func IsTestName(p string) bool {
	return testName.MatchString(path.Base(p))
}

// IsRunnableTest reports a Node test file under test/.
func IsRunnableTest(f string) bool {
	return strings.HasPrefix(f, "test/") && graphTest.MatchString(f)
}

// Dependents returns, sorted, every listed runnable test under test/ that
// reaches a changed file through the reverse import closure, or that names a
// changed file by its path. A changed runnable test is its own dependent.
func (g *Graph) Dependents(changed []string) []string {
	seen := map[string]bool{}
	queue := []string{}
	for _, f := range changed {
		if !seen[f] {
			seen[f] = true
			queue = append(queue, f)
		}
	}
	for _, f := range changed {
		for _, file := range g.mentions(f) {
			if !seen[file] {
				seen[file] = true
				queue = append(queue, file)
			}
		}
	}
	for len(queue) > 0 {
		f := queue[0]
		queue = queue[1:]
		for _, importer := range g.Importers(f) {
			if !seen[importer] {
				seen[importer] = true
				queue = append(queue, importer)
			}
		}
	}
	out := []string{}
	for f := range seen {
		if g.listed[f] && IsRunnableTest(f) {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

// NativeTests returns, sorted, every listed runnable test whose own text or a
// test/ helper it imports (directly or through other test/ files) names the
// native build or binary (NativeMarkers).
func (g *Graph) NativeTests() []string {
	marked := []string{}
	for f, text := range g.content {
		if !strings.HasPrefix(f, "test/") {
			continue
		}
		for _, marker := range NativeMarkers {
			if strings.Contains(text, marker) {
				marked = append(marked, f)
				break
			}
		}
	}
	seen := map[string]bool{}
	queue := append([]string{}, marked...)
	for _, f := range marked {
		seen[f] = true
	}
	for len(queue) > 0 {
		f := queue[0]
		queue = queue[1:]
		for _, importer := range g.Importers(f) {
			if strings.HasPrefix(importer, "test/") && !seen[importer] {
				seen[importer] = true
				queue = append(queue, importer)
			}
		}
	}
	out := []string{}
	for f := range seen {
		if g.listed[f] && IsRunnableTest(f) {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

func (g *Graph) dirImporters(f string) []string {
	out := []string{}
	for dir, files := range g.dirs {
		if strings.HasPrefix(f, dir) {
			out = append(out, files...)
		}
	}
	return out
}

// NativeInput reports a path that changes the native binary: non-test Go
// under native/, native/go.mod, native/go.sum or scripts/build-native.mjs. A
// directory or glob entry under native/ may cover such a file, so it counts.
func NativeInput(p string) bool {
	switch {
	case p == "scripts/build-native.mjs", p == "native/go.mod", p == "native/go.sum", p == "native":
		return true
	case !strings.HasPrefix(p, "native/") || strings.HasSuffix(p, "_test.go"):
		return false
	}
	return strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "/") || strings.ContainsAny(p, "*?[")
}

// GoldenTest is the config golden test; GoldenInput names what it reads (B409).
const GoldenTest = "test/compat/config-golden.test.mjs"

func GoldenInput(p string) bool {
	return p == "scripts/compat-golden.mjs" || strings.HasPrefix(p, "test/fixtures/") || strings.HasPrefix(p, "schema/") || strings.HasPrefix(p, "src/config/")
}

// Pair reports b as the stem-paired test of the source a, by Node's
// review/paired.mjs rules (FooTests.swift for Foo.swift, test_foo.py for
// foo.py, test/a.test.mjs for src/a.mjs).
func Pair(a, b string) bool { return pair(a, b) }
