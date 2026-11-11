package gates

import (
	"regexp"
	"strings"
)

// B427: a narrow import graph. The B404 closure followed every edge to every
// importer, so one changed hub reached almost every test (348 of 380 for the
// B362b+B402 batch). Three rules cut it, each one sound for runtime behaviour:
//
//  1. Comments make no edge. A path named in a comment (`see
//     native/contextpack/pack.go`) or a JSDoc type import (`import('./x.mjs')`
//     inside /** */) does not run; only code text is read for edges and path
//     mentions.
//  2. Named imports are tracked through re-exports. `export { a } from
//     './leaf.mjs'` and `export * from './leaf.mjs'` hand on leaf bindings
//     without running code that uses them, so an importer of the barrel
//     reaches the leaf only when it imports a name the leaf provides (or
//     imports the whole module: namespace, side-effect, dynamic import,
//     require, new URL). A file that imports a binding for its own code is
//     affected as a whole.
//  3. Stop files (gates.import_graph.stop): the closure reaches a stop file and
//     the tests and test helpers that import or name it, but does not go on to
//     the product files that import it.

// link is how one file uses one target: as a whole (all), by the named
// bindings it imports for its own code, or by the bindings it re-exports.
type link struct {
	all      bool
	names    map[string][]string // imported binding name ("default" for a default import) -> local names
	reexport map[string]string   // target export name -> this file's export name
	star     bool                // export * from target
}

func (l *link) addName(n, local string) {
	if l.names == nil {
		l.names = map[string][]string{}
	}
	for _, have := range l.names[n] {
		if have == local {
			return
		}
	}
	l.names[n] = append(l.names[n], local)
}

func (l *link) addReexport(from, to string) {
	if l.reexport == nil {
		l.reexport = map[string]string{}
	}
	l.reexport[from] = to
}

var (
	graphImportFrom = regexp.MustCompile(`\bimport\s+([^'"();]*?)\s*\bfrom\s*['"]([^'"\n]+)['"]`)
	graphExportFrom = regexp.MustCompile(`\bexport\s+([^'"();]*?)\s*\bfrom\s*['"]([^'"\n]+)['"]`)
	// const { a, b: c } = await import('./x.mjs') binds named exports only.
	graphAwaitImport = regexp.MustCompile(`\b(?:const|let|var)\s*\{([^{}'"]*)\}\s*=\s*await\s+import\s*\(\s*['"]([^'"\n]+)['"]\s*\)`)
	graphIdent       = regexp.MustCompile(`^[A-Za-z_$][\w$]*$`)
	declFunc         = regexp.MustCompile(`\bexport\s+(?:async\s+)?function\s*\*?\s*([A-Za-z_$][\w$]*)`)
	declClass        = regexp.MustCompile(`\bexport\s+class\s+([A-Za-z_$][\w$]*)`)
	declVar          = regexp.MustCompile(`\bexport\s+(?:const|let|var)\s+([A-Za-z_$][\w$]*)`)
	declDefault      = regexp.MustCompile(`\bexport\s+default\b`)
	declList         = regexp.MustCompile(`\bexport\s*\{([^}]*)\}`)
	declListFrom     = regexp.MustCompile(`^\s*from\b`)
)

// splitSpecifiers parses "a, b as c, default as d" into (source, local) pairs.
func splitSpecifiers(list string) ([][2]string, bool) {
	out := [][2]string{}
	for _, part := range strings.Split(list, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		fields := strings.Fields(part)
		if len(fields) > 0 && fields[0] == "type" && len(fields) > 1 {
			fields = fields[1:]
		}
		switch {
		case len(fields) == 1 && graphIdent.MatchString(strings.Trim(fields[0], `'"`)):
			out = append(out, [2]string{strings.Trim(fields[0], `'"`), strings.Trim(fields[0], `'"`)})
		case len(fields) == 3 && fields[1] == "as":
			out = append(out, [2]string{strings.Trim(fields[0], `'"`), strings.Trim(fields[2], `'"`)})
		default:
			return nil, false
		}
	}
	return out, true
}

// importClause fills l from the clause between `import` and `from`.
func importClause(l *link, clause string) {
	clause = strings.TrimSpace(clause)
	if strings.HasPrefix(clause, "type ") {
		clause = strings.TrimSpace(clause[5:])
	}
	if clause == "" || strings.Contains(clause, "*") {
		l.all = true
		return
	}
	named := ""
	if i := strings.Index(clause, "{"); i >= 0 {
		j := strings.LastIndex(clause, "}")
		if j < i {
			l.all = true
			return
		}
		named = clause[i+1 : j]
		clause = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(clause[:i]), ","))
	}
	if clause != "" {
		if !graphIdent.MatchString(clause) {
			l.all = true
			return
		}
		l.addName("default", clause)
	}
	specs, ok := splitSpecifiers(named)
	if !ok {
		l.all = true
		return
	}
	for _, s := range specs {
		l.addName(s[0], s[1])
	}
	if l.names == nil {
		// `import {} from` runs the module and binds nothing: whole module.
		l.all = true
	}
}

// destructureClause fills l from the pattern of `const { a, b: c } = await
// import(…)`; a default, nested or rest pattern uses the whole module.
func destructureClause(l *link, pattern string) {
	for _, part := range strings.Split(pattern, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, local := part, part
		if i := strings.Index(part, ":"); i >= 0 {
			name, local = strings.TrimSpace(part[:i]), strings.TrimSpace(part[i+1:])
		}
		if !graphIdent.MatchString(name) || !graphIdent.MatchString(local) {
			l.all = true
			return
		}
		l.addName(name, local)
	}
	if l.names == nil {
		l.all = true
	}
}

// exportClause fills l from the clause between `export` and `from`.
func exportClause(l *link, clause string) {
	clause = strings.TrimSpace(clause)
	if strings.HasPrefix(clause, "type ") {
		clause = strings.TrimSpace(clause[5:])
	}
	if clause == "*" {
		l.star = true
		return
	}
	if strings.HasPrefix(clause, "*") {
		// export * as ns from: ns is the whole target.
		rest := strings.Fields(strings.TrimSpace(clause[1:]))
		if len(rest) == 2 && rest[0] == "as" {
			l.addReexport("*", strings.Trim(rest[1], `'"`))
			return
		}
		l.all = true
		return
	}
	if !strings.HasPrefix(clause, "{") || !strings.HasSuffix(clause, "}") {
		l.all = true
		return
	}
	specs, ok := splitSpecifiers(clause[1 : len(clause)-1])
	if !ok {
		l.all = true
		return
	}
	for _, s := range specs {
		l.addReexport(s[0], s[1])
	}
	if l.reexport == nil {
		l.all = true
	}
}

// ownExports returns the names a module declares as exports itself (not the
// ones it re-exports from another file), and false when a declaration form is
// not one it can read (export const { a } = …, export const a = 1, b = 2).
func ownExports(code string) (map[string]bool, bool) {
	names := map[string]bool{}
	sure := true
	for _, re := range []*regexp.Regexp{declFunc, declClass, declVar} {
		for _, m := range re.FindAllStringSubmatch(code, -1) {
			names[m[1]] = true
		}
	}
	if regexp.MustCompile(`\bexport\s+(?:const|let|var)\s*[{\[]`).MatchString(code) {
		sure = false
	}
	if declDefault.MatchString(code) {
		names["default"] = true
	}
	for _, m := range declList.FindAllStringSubmatchIndex(code, -1) {
		if declListFrom.MatchString(code[m[1]:]) {
			continue
		}
		specs, ok := splitSpecifiers(code[m[2]:m[3]])
		if !ok {
			sure = false
			continue
		}
		for _, s := range specs {
			names[s[1]] = true
		}
	}
	return names, sure
}

// stripComments blanks the comments of JavaScript source, keeping its length
// and newlines. Strings, template literals (with ${} nesting) and regular
// expression literals are kept as they are.
func stripComments(src string) string { return scanJS(src, false) }

// skeleton is stripComments that also blanks the text of string, template
// and regular expression literals (a template's ${ … } code stays), so
// brackets and identifiers in it are code only.
func skeleton(src string) string { return scanJS(src, true) }

func scanJS(src string, literals bool) string {
	b := []byte(src)
	n := len(b)
	templates := []int{} // brace depth of each open ${ … }
	var last byte        // last significant code byte
	lastWord := ""
	blank := func(from, to int) {
		for k := from; k < to && k < n; k++ {
			if b[k] != '\n' {
				b[k] = ' '
			}
		}
	}
	regexAllowed := func() bool {
		if last == 0 || strings.IndexByte("(,=:[!&|?{};+-*%<>~^", last) >= 0 {
			return true
		}
		switch lastWord {
		case "return", "typeof", "case", "do", "else", "in", "of", "new", "delete", "void", "throw", "yield", "await":
			return true
		}
		return false
	}
	// template scans a template literal body from i; it returns the index after
	// the closing backtick, or the index after "${" (pushing a brace level).
	template := func(i int) (int, bool) {
		start := i
		for i < n {
			switch b[i] {
			case '\\':
				i += 2
				continue
			case '`':
				if literals {
					blank(start, i)
				}
				return i + 1, false
			case '$':
				if i+1 < n && b[i+1] == '{' {
					if literals {
						blank(start, i)
					}
					templates = append(templates, 0)
					return i + 2, true
				}
			}
			i++
		}
		if literals {
			blank(start, n)
		}
		return n, false
	}
	i := 0
	for i < n {
		c := b[i]
		switch {
		case c == '/' && i+1 < n && b[i+1] == '/':
			j := i
			for j < n && b[j] != '\n' {
				j++
			}
			blank(i, j)
			i = j
			continue
		case c == '/' && i+1 < n && b[i+1] == '*':
			j := i + 2
			for j+1 < n && !(b[j] == '*' && b[j+1] == '/') {
				j++
			}
			j += 2
			if j > n {
				j = n
			}
			blank(i, j)
			i = j
			continue
		case c == '\'' || c == '"':
			j := i + 1
			for j < n && b[j] != c && b[j] != '\n' {
				if b[j] == '\\' {
					j++
				}
				j++
			}
			if literals {
				blank(i+1, j)
			}
			i = j + 1
			last, lastWord = c, ""
			continue
		case c == '`':
			j, _ := template(i + 1)
			i = j
			last, lastWord = '`', ""
			continue
		case c == '/' && regexAllowed():
			j := i + 1
			class := false
			for j < n && b[j] != '\n' {
				if b[j] == '\\' {
					j += 2
					continue
				}
				if b[j] == '[' {
					class = true
				} else if b[j] == ']' {
					class = false
				} else if b[j] == '/' && !class {
					break
				}
				j++
			}
			if literals {
				blank(i+1, j)
			}
			i = j + 1
			last, lastWord = '/', ""
			continue
		case c == '{' && len(templates) > 0:
			templates[len(templates)-1]++
		case c == '}' && len(templates) > 0:
			if templates[len(templates)-1] == 0 {
				templates = templates[:len(templates)-1]
				j, _ := template(i + 1)
				i = j
				last, lastWord = '`', ""
				continue
			}
			templates[len(templates)-1]--
		}
		if c == '_' || c == '$' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			j := i
			for j < n && (b[j] == '_' || b[j] == '$' || b[j] >= 'A' && b[j] <= 'Z' || b[j] >= 'a' && b[j] <= 'z' || b[j] >= '0' && b[j] <= '9') {
				j++
			}
			lastWord, last = string(b[i:j]), 'a'
			i = j
			continue
		}
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			last, lastWord = c, ""
		}
		i++
	}
	return string(b)
}
