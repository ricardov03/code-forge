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
	// graphToken is a run of path characters; a repository path named in a
	// file's text is one of its '/'-boundary suffixes.
	graphToken = regexp.MustCompile(`[A-Za-z0-9_@+.\-/]+`)
)

// NativeMarkers name the native build, the native binary or the native binary
// path helper (test/helpers/go-binary.mjs) in a test or test helper: a file
// that holds one builds or runs the native binary.
var NativeMarkers = []string{"cmd/code-forge-native", "build-native.mjs", "code-forge-native", "go-binary.mjs"}

// Graph is the reverse import graph over the listed JavaScript files.
type Graph struct {
	listed    map[string]bool
	content   map[string]string
	code      map[string]string           // content without comments (B427)
	importers map[string][]string         // target file -> files that import it
	links     map[string]map[string]*link // target file -> importer -> how it is used (B427)
	reexports map[string]map[string]*link // file -> target it re-exports from -> link (B427)
	dirs      map[string][]string         // target directory ("x/") -> files that name it
	missing   map[string][]string         // unresolved relative specifier path -> files that import it
	named     map[string][]string         // path-like text ("a/b.mjs") -> files that name it
	stop      []string                    // gates.import_graph.stop entries (B427)
	decls     map[string]map[string]bool
	modules   map[string]*moduleInfo
	declSure  map[string]bool
}

// NewGraph reads every listed .mjs/.js/.cjs file under cwd. A file that cannot
// be read has no edges. Comments make no edge (B427).
func NewGraph(cwd string, listed []string) *Graph {
	g := &Graph{listed: map[string]bool{}, content: map[string]string{}, code: map[string]string{}, importers: map[string][]string{}, links: map[string]map[string]*link{}, reexports: map[string]map[string]*link{}, dirs: map[string][]string{}, missing: map[string][]string{}, named: map[string][]string{}, decls: map[string]map[string]bool{}, declSure: map[string]bool{}}
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
		code := stripComments(text)
		g.code[f] = code
		// How f uses each specifier: `from` clauses are read for their
		// bindings; every other edge form uses the whole module.
		uses := map[string]*link{}
		use := func(spec string) *link {
			if uses[spec] == nil {
				uses[spec] = &link{}
			}
			return uses[spec]
		}
		classified := map[int]bool{}
		for _, m := range graphImportFrom.FindAllStringSubmatchIndex(code, -1) {
			classified[m[5]] = true
			importClause(use(code[m[4]:m[5]]), code[m[2]:m[3]])
		}
		for _, m := range graphExportFrom.FindAllStringSubmatchIndex(code, -1) {
			classified[m[5]] = true
			exportClause(use(code[m[4]:m[5]]), code[m[2]:m[3]])
		}
		for _, m := range graphAwaitImport.FindAllStringSubmatchIndex(code, -1) {
			classified[m[5]] = true
			destructureClause(use(code[m[4]:m[5]]), code[m[2]:m[3]])
		}
		order := []string{}
		// An import specifier is an edge, not a path mention: blank it in the
		// text the mention scan reads.
		mentionText := []byte(code)
		for _, re := range []*regexp.Regexp{graphFrom, graphBare, graphDynamic, graphURL} {
			for _, m := range re.FindAllStringSubmatchIndex(code, -1) {
				spec := code[m[2]:m[3]]
				if graphJoin(f, spec) != "" {
					for k := m[2]; k < m[3]; k++ {
						mentionText[k] = ' '
					}
				}
				if !classified[m[3]] {
					use(spec).all = true
				}
				order = append(order, spec)
			}
		}
		seen := map[string]bool{}
		for _, spec := range order {
			target, dir := g.resolve(f, spec)
			if target == "" {
				// A specifier that names no listed file may name a file the
				// change deleted or renamed: keep the edge by its path.
				if joined := graphJoin(f, spec); joined != "" && !seen["\x00"+joined] {
					seen["\x00"+joined] = true
					g.missing[joined] = append(g.missing[joined], f)
				}
				continue
			}
			if dir {
				if !seen[target] {
					seen[target] = true
					g.dirs[target] = append(g.dirs[target], f)
				}
				continue
			}
			if !seen[target] {
				seen[target] = true
				g.importers[target] = append(g.importers[target], f)
			}
			if g.links[target] == nil {
				g.links[target] = map[string]*link{}
			}
			l := g.links[target][f]
			if l == nil {
				l = &link{}
				g.links[target][f] = l
			}
			mergeLink(l, uses[spec])
			if l.star || l.reexport != nil {
				if g.reexports[f] == nil {
					g.reexports[f] = map[string]*link{}
				}
				g.reexports[f][target] = l
			}
		}
		if strings.HasPrefix(f, "src/") {
			// Product source names paths of the repository it works on
			// (messages, map keys, comparisons), not files it reads: no
			// mention edges (B427).
			continue
		}
		named := map[string]bool{}
		for _, token := range graphToken.FindAllString(string(mentionText), -1) {
			for i := 0; i < len(token); i++ {
				if i > 0 && token[i-1] != '/' {
					continue
				}
				for _, candidate := range []string{token[i:], strings.TrimRight(token[i:], ".")} {
					if strings.Contains(candidate, "/") && candidate != f && !named[candidate] {
						named[candidate] = true
						g.named[candidate] = append(g.named[candidate], f)
					}
				}
			}
		}
	}
	g.cliEdges(listed)
	return g
}

func mergeLink(into, from *link) {
	if from == nil {
		into.all = true
		return
	}
	into.all = into.all || from.all
	into.star = into.star || from.star
	for n, locals := range from.names {
		for _, local := range locals {
			into.addName(n, local)
		}
	}
	for a, b := range from.reexport {
		into.addReexport(a, b)
	}
}

// SetStop sets the gates.import_graph.stop entries: exact repository paths,
// directory prefixes ending in '/' or path.Match globs (B427).
func (g *Graph) SetStop(entries []string) { g.stop = append([]string{}, entries...) }

func (g *Graph) stopped(f string) bool {
	for _, e := range g.stop {
		if e == f || strings.HasSuffix(e, "/") && strings.HasPrefix(f, e) {
			return true
		}
		if ok, _ := path.Match(e, f); ok {
			return true
		}
	}
	return false
}

// CLIEntry is the Node CLI entry and CLILauncher the shell script that runs
// it. The entry's router loads every src/cli/*.mjs by readdir, so no import
// names those verb modules (B373b).
const (
	CLIEntry    = "bin/code-forge.mjs"
	CLILauncher = "bin/code-forge"
)

// cliEdges adds the edges no import statement shows: the CLI entry depends on
// every listed src/cli/*.mjs, and the launcher depends on the entry. A test
// that names either path by text then reaches every verb module.
func (g *Graph) cliEdges(listed []string) {
	if !g.listed[CLIEntry] {
		return
	}
	add := func(target, importer string) {
		for _, f := range g.importers[target] {
			if f == importer {
				return
			}
		}
		g.importers[target] = append(g.importers[target], importer)
		// Dependents walks links (B427): the router loads the whole module.
		if g.links[target] == nil {
			g.links[target] = map[string]*link{}
		}
		if g.links[target][importer] == nil {
			g.links[target][importer] = &link{all: true}
		}
	}
	for _, f := range listed {
		if path.Dir(f) == "src/cli" && path.Ext(f) == ".mjs" {
			add(f, CLIEntry)
		}
	}
	if g.listed[CLILauncher] {
		add(CLIEntry, CLILauncher)
	}
}

// graphJoin is the repository path a relative specifier in file names, without
// a query or hash; "" for a bare, absolute or escaping specifier.
func graphJoin(file, spec string) string {
	if !strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../") {
		return ""
	}
	if i := strings.IndexAny(spec, "?#"); i >= 0 {
		spec = spec[:i]
	}
	joined := path.Clean(path.Join(path.Dir(file), spec))
	if joined == "." || joined == ".." || strings.HasPrefix(joined, "../") {
		return ""
	}
	return joined
}

// resolve turns a relative specifier in file into a listed repository path,
// or a directory prefix ending in '/'.
func (g *Graph) resolve(file, spec string) (string, bool) {
	joined := graphJoin(file, spec)
	if joined == "" {
		return "", false
	}
	if i := strings.IndexAny(spec, "?#"); i >= 0 {
		spec = spec[:i]
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
// directory reference that covers f and a specifier that resolves to no listed
// file but names f (a file the change deleted or renamed).
func (g *Graph) Importers(f string) []string {
	return append(append(append([]string{}, g.importers[f]...), g.dirImporters(f)...), g.missingImporters(f)...)
}

// mentions returns the files that name f by its repository path in their text
// (a test that spawns a script or reads a fixture by path). Dependents follows
// these edges for every file in the closure, like imports.
func (g *Graph) mentions(f string) []string {
	if !strings.Contains(f, "/") {
		return nil
	}
	return g.named[f]
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
// reaches a changed file through the reverse closure of imports and path
// mentions (a file that names another by its repository path in code). A
// changed path that no longer exists (deleted or renamed) still has its
// importers. A changed runnable test is its own dependent. Named imports are
// tracked through re-exports, and the closure does not leave a stop file for
// product importers (B427, see importgraph_names.go).
func (g *Graph) Dependents(changed []string) []string {
	r := &reach{g: g, state: map[string]*reachState{}, hit: map[string]map[string]bool{}}
	start := map[string]bool{}
	for _, f := range changed {
		start[f] = true
		r.mark(f, true, nil, "")
	}
	for len(r.queue) > 0 {
		f := r.queue[0]
		r.queue = r.queue[1:]
		r.queued[f] = false
		st := r.state[f]
		follow := func(importer string) bool {
			return start[f] || !g.stopped(f) || strings.HasPrefix(importer, "test/")
		}
		for importer, l := range g.links[f] {
			if !follow(importer) {
				continue
			}
			r.propagate(f, st, importer, l)
		}
		whole := append(append(g.dirImporters(f), g.missingImporters(f)...), g.mentions(f)...)
		for _, importer := range whole {
			if follow(importer) {
				r.mark(importer, true, nil, "")
			}
		}
	}
	out := []string{}
	for f := range r.state {
		if g.listed[f] && IsRunnableTest(f) {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

// reachState is what a file has affected: all of it, some exported names, or
// the names it re-exports with export * from affected modules (open).
type reachState struct {
	all      bool
	names    map[string]bool
	openFrom map[string]bool
}

type reach struct {
	g      *Graph
	hit    map[string]map[string]bool // importer -> its affected imported local names
	state  map[string]*reachState
	queue  []string
	queued map[string]bool
}

// mark grows f's state and queues f when it grew.
func (r *reach) mark(f string, all bool, names []string, openFrom string) {
	if r.queued == nil {
		r.queued = map[string]bool{}
	}
	st := r.state[f]
	grew := false
	if st == nil && !all && len(names) == 0 && openFrom == "" {
		return
	}
	if st == nil {
		st = &reachState{names: map[string]bool{}, openFrom: map[string]bool{}}
		r.state[f] = st
		grew = true
	}
	if all && !st.all {
		st.all, grew = true, true
	}
	if !st.all {
		for _, n := range names {
			if !st.names[n] {
				st.names[n], grew = true, true
			}
		}
		if openFrom != "" && !st.openFrom[openFrom] {
			st.openFrom[openFrom], grew = true, true
		}
	}
	if grew && !r.queued[f] {
		r.queued[f] = true
		r.queue = append(r.queue, f)
	}
}

// propagate applies one link from importer to the affected target f.
func (r *reach) propagate(f string, st *reachState, importer string, l *link) {
	if l.all {
		r.mark(importer, true, nil, "")
		return
	}
	locals := []string{}
	for n, ls := range l.names {
		if r.hits(f, n, map[string]bool{}) {
			locals = append(locals, ls...)
		}
	}
	if len(locals) > 0 {
		m := r.g.module(importer)
		if IsRunnableTest(importer) || m.whole {
			r.mark(importer, true, nil, "")
			return
		}
		if r.hit[importer] == nil {
			r.hit[importer] = map[string]bool{}
		}
		for _, n := range locals {
			r.hit[importer][n] = true
		}
		all, names := m.affectedExports(r.hit[importer])
		r.mark(importer, all, names, "")
	}
	for from, to := range l.reexport {
		if from == "*" || r.hits(f, from, map[string]bool{}) {
			r.mark(importer, false, []string{to}, "")
		}
	}
	if l.star {
		names := []string{}
		for n := range st.names {
			if n != "default" {
				names = append(names, n)
			}
		}
		open := ""
		if st.all || len(st.openFrom) > 0 {
			open = f
		}
		if len(names) > 0 || open != "" {
			r.mark(importer, false, names, open)
		}
	}
}

// hits reports whether the export x of f is affected. A name that f's own
// code or an unaffected re-export cannot be shown to provide counts as
// affected when f re-exports * from an affected module.
func (r *reach) hits(f, x string, visiting map[string]bool) bool {
	st := r.state[f]
	if st == nil {
		return false
	}
	if st.all || st.names[x] {
		return true
	}
	if len(st.openFrom) == 0 {
		return false
	}
	if visiting[f] {
		return true
	}
	visiting[f] = true
	defer delete(visiting, f)
	return !r.providedUnaffected(f, x, visiting)
}

// providedUnaffected reports a name x that f exports from its own code, from a
// named re-export, or through export * from a module that provides x and does
// not have it affected.
func (r *reach) providedUnaffected(f, x string, visiting map[string]bool) bool {
	if st := r.state[f]; st != nil && st.all {
		return false
	}
	decls, _ := r.g.ownExports(f)
	if decls[x] {
		return true
	}
	for target, l := range r.g.reexports[f] {
		for from, to := range l.reexport {
			if to == x {
				if from == "*" {
					return r.state[target] == nil
				}
				return !r.hits(target, from, visiting)
			}
		}
	}
	if x == "default" {
		return false
	}
	for target, l := range r.g.reexports[f] {
		if !l.star || visiting[target] || r.hits(target, x, visiting) {
			continue
		}
		visiting[target] = true
		ok := r.providedUnaffected(target, x, visiting)
		delete(visiting, target)
		if ok {
			return true
		}
	}
	return false
}

func (g *Graph) ownExports(f string) (map[string]bool, bool) {
	if d, ok := g.decls[f]; ok {
		return d, g.declSure[f]
	}
	d, sure := ownExports(g.code[f])
	g.decls[f], g.declSure[f] = d, sure
	return d, sure
}

// missingImporters are the files whose relative specifier resolves to no
// listed file but names f (a file the change deleted or renamed).
func (g *Graph) missingImporters(f string) []string {
	out := []string{}
	keys := []string{f}
	for _, ext := range []string{".mjs", ".js", ".cjs"} {
		if strings.HasSuffix(f, ext) {
			keys = append(keys, strings.TrimSuffix(f, ext))
		}
	}
	if base := path.Base(f); base == "index.mjs" || base == "index.js" {
		keys = append(keys, path.Dir(f))
	}
	for _, key := range keys {
		out = append(out, g.missing[key]...)
	}
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
