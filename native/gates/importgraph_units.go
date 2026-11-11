package gates

import (
	"regexp"
	"strings"
)

// B427: binding-level reach inside one ES module. A module is split into its
// top-level statements; each one defines names and refers to names (by
// identifier text, so a shadowed or property name only widens the result).
// Given the imported bindings that changed, a statement is affected when it
// refers to an affected name, and then every name it defines is affected. An
// affected statement that runs when the module loads (an initializer with a
// call, a class, an expression statement) affects the whole module, since
// every importer runs it. Affected code that refers to a module-level
// variable may change its value, so that variable counts as affected too.
// What is left is the set of exports an importer can reach the change
// through; an importer that uses none of them does not depend on it.

type unit struct {
	defines []string
	refs    []string
	load    bool // runs code when the module loads
}

type moduleInfo struct {
	whole   bool // binding-level reach is not read: any use is affected
	units   []unit
	exports map[string][]string // export name -> local names
	mutable map[string]bool     // module-level variables
}

const anonymousDefault = "\x00default"

var (
	unitImport   = regexp.MustCompile(`^import\s*[{*\w$'"]`)
	unitStart    = regexp.MustCompile(`^(?:export\b|import\s*[{*\w$'"]|function\b|async\s+function\b|const\b|let\b|var\b|class\b)`)
	unitFunc     = regexp.MustCompile(`^(?:async\s+)?function\b\s*\*?\s*([A-Za-z_$][\w$]*)?`)
	unitClass    = regexp.MustCompile(`^class\b\s*([A-Za-z_$][\w$]*)?`)
	unitVar      = regexp.MustCompile(`^(?:const|let|var)\b`)
	unitIdent    = regexp.MustCompile(`[A-Za-z_$][\w$]*`)
	unitArrow    = regexp.MustCompile(`^(?:async\s*)?(?:[A-Za-z_$][\w$]*|\([^()]*(?:\([^()]*\)[^()]*)*\))\s*=>`)
	unitFuncExpr = regexp.MustCompile(`^(?:async\s+)?function\b`)
	unitPure     = regexp.MustCompile(`^[\s\w$'"{}\[\],:]*$`)
	// const X = Object.freeze(…) holds a value no code can change.
	frozen  = regexp.MustCompile(`^const\s+[A-Za-z_$][\w$]*\s*=\s*Object\.freeze\(([\s\S]*)\)$`)
	unitCJS = regexp.MustCompile(`\bmodule\.exports\b|\bexports\.[A-Za-z_$]|\beval\s*\(`)
)

// module returns the binding-level reading of f, cached.
func (g *Graph) module(f string) *moduleInfo {
	if g.modules == nil {
		g.modules = map[string]*moduleInfo{}
	}
	if m, ok := g.modules[f]; ok {
		return m
	}
	m := analyzeModule(f, g.content[f])
	g.modules[f] = m
	return m
}

func analyzeModule(f, src string) *moduleInfo {
	whole := &moduleInfo{whole: true}
	if !strings.HasSuffix(f, ".mjs") && !strings.HasSuffix(f, ".js") || src == "" {
		return whole
	}
	skel := skeleton(src)
	if unitCJS.MatchString(skel) {
		return whole
	}
	m := &moduleInfo{exports: map[string][]string{}, mutable: map[string]bool{}}
	spans, ok := splitUnits(skel)
	if !ok {
		return whole
	}
	for _, span := range spans {
		text := strings.TrimSpace(skel[span[0]:span[1]])
		text = strings.TrimSuffix(text, ";")
		if text == "" || unitImport.MatchString(text) {
			continue
		}
		u := unit{}
		exported := false
		switch {
		case strings.HasPrefix(text, "export") && regexp.MustCompile(`^export\s+default\b`).MatchString(text):
			rest := strings.TrimSpace(regexp.MustCompile(`^export\s+default\b`).ReplaceAllString(text, ""))
			u.defines = []string{anonymousDefault}
			m.exports["default"] = []string{anonymousDefault}
			switch {
			case unitFunc.MatchString(rest):
				if name := unitFunc.FindStringSubmatch(rest)[1]; name != "" {
					u.defines = append(u.defines, name)
				}
			case unitClass.MatchString(rest):
				if name := unitClass.FindStringSubmatch(rest)[1]; name != "" {
					u.defines = append(u.defines, name)
				}
				u.load = true
			default:
				u.load = !unitPure.MatchString(strings.ReplaceAll(rest, "...", ""))
			}
		case strings.HasPrefix(text, "export") && regexp.MustCompile(`^export\s*[{*]`).MatchString(text):
			rest := strings.TrimSpace(strings.TrimPrefix(text, "export"))
			if strings.HasPrefix(rest, "*") || regexp.MustCompile(`\}\s*from\b`).MatchString(rest) {
				continue // a re-export: read by the import links
			}
			end := strings.Index(rest, "}")
			if end < 0 {
				return whole
			}
			specs, ok := splitSpecifiers(rest[1:end])
			if !ok {
				return whole
			}
			for _, s := range specs {
				m.exports[s[1]] = append(m.exports[s[1]], s[0])
			}
			continue
		default:
			if strings.HasPrefix(text, "export") && regexp.MustCompile(`^export\s`).MatchString(text) {
				exported = true
				text = strings.TrimSpace(text[len("export"):])
			}
			switch {
			case unitFunc.MatchString(text):
				name := unitFunc.FindStringSubmatch(text)[1]
				if name == "" {
					return whole
				}
				u.defines = []string{name}
			case unitClass.MatchString(text):
				name := unitClass.FindStringSubmatch(text)[1]
				if name == "" {
					return whole
				}
				u.defines, u.load = []string{name}, true
			case unitVar.MatchString(text):
				names, load, ok := declarators(unitVar.ReplaceAllString(text, ""))
				if !ok {
					return whole
				}
				u.defines, u.load = names, load
				if load && !frozenLiteral(text) {
					for _, n := range names {
						m.mutable[n] = true
					}
				}
			default:
				if exported {
					return whole
				}
				u.load = true
			}
			if exported {
				for _, n := range u.defines {
					m.exports[n] = append(m.exports[n], n)
				}
			}
		}
		own := map[string]bool{}
		for _, n := range u.defines {
			own[n] = true
		}
		seen := map[string]bool{}
		body := skel[span[0]:span[1]]
		for _, loc := range unitIdent.FindAllStringIndex(body, -1) {
			if loc[0] > 0 && body[loc[0]-1] == '.' && !(loc[0] > 1 && body[loc[0]-2] == '.') {
				continue // a property name, not a binding
			}
			if loc[0] > 0 && (body[loc[0]-1] >= '0' && body[loc[0]-1] <= '9' || body[loc[0]-1] == '_' || body[loc[0]-1] == '$') {
				continue
			}
			name := body[loc[0]:loc[1]]
			if !own[name] && !seen[name] {
				seen[name] = true
				u.refs = append(u.refs, name)
			}
		}
		m.units = append(m.units, u)
	}
	return m
}

// splitUnits splits a module skeleton into top-level statements: at a ';' on
// bracket depth 0, and at a line or a closing '}' on depth 0 that a statement
// keyword follows. Unbalanced brackets report false.
func splitUnits(skel string) ([][2]int, bool) {
	spans := [][2]int{}
	depth, start := 0, 0
	next := func(i int) string {
		j := i
		for j < len(skel) && (skel[j] == ' ' || skel[j] == '\t' || skel[j] == '\n' || skel[j] == '\r') {
			j++
		}
		end := j + 24
		if end > len(skel) {
			end = len(skel)
		}
		return skel[j:end]
	}
	cut := func(i int) {
		if strings.TrimSpace(skel[start:i]) != "" {
			spans = append(spans, [2]int{start, i})
		}
		start = i
	}
	for i := 0; i < len(skel); i++ {
		switch c := skel[i]; c {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth < 0 {
				return nil, false
			}
			if depth == 0 && c == '}' && unitStart.MatchString(next(i+1)) {
				cut(i + 1)
			}
		case ';':
			if depth == 0 {
				cut(i + 1)
			}
		case '\n':
			if depth == 0 && unitStart.MatchString(next(i+1)) {
				cut(i + 1)
			}
		}
	}
	if depth != 0 {
		return nil, false
	}
	cut(len(skel))
	return spans, true
}

// declarators reads `a = …, { b, c: d } = …` after const/let/var: the names
// it binds (every identifier of a destructuring pattern, which may over-count)
// and whether an initializer runs code at load (anything but a function, an
// arrow function or a literal made of names).
func declarators(text string) ([]string, bool, bool) {
	parts := []string{}
	depth, start := 0, 0
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, text[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, text[start:])
	names := []string{}
	load := false
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		eq := -1
		d := 0
		for i := 0; i < len(part); i++ {
			switch part[i] {
			case '(', '[', '{':
				d++
			case ')', ']', '}':
				d--
			case '=':
				if d == 0 && (i+1 >= len(part) || part[i+1] != '=' && part[i+1] != '>') && (i == 0 || !strings.ContainsRune("=!<>+-*/%&|^?", rune(part[i-1]))) {
					eq = i
				}
			}
			if eq >= 0 {
				break
			}
		}
		lhs, init := part, ""
		if eq >= 0 {
			lhs, init = strings.TrimSpace(part[:eq]), strings.TrimSpace(part[eq+1:])
		}
		if graphIdent.MatchString(lhs) {
			names = append(names, lhs)
		} else if strings.HasPrefix(lhs, "{") || strings.HasPrefix(lhs, "[") {
			names = append(names, unitIdent.FindAllString(lhs, -1)...)
			load = true
		} else {
			return nil, false, false
		}
		switch {
		case init == "":
		case unitFuncExpr.MatchString(init), unitArrow.MatchString(init):
		case unitPure.MatchString(strings.ReplaceAll(init, "...", "")):
		default:
			load = true
		}
	}
	return names, load, true
}

// affectedExports returns the exports of m that the affected local names
// reach, or all when an affected statement runs at load.
func (m *moduleInfo) affectedExports(hit map[string]bool) (bool, []string) {
	if m.whole {
		return true, nil
	}
	a := map[string]bool{}
	for n := range hit {
		a[n] = true
	}
	done := make([]bool, len(m.units))
	for changed := true; changed; {
		changed = false
		for i, u := range m.units {
			if done[i] {
				continue
			}
			touched := false
			for _, r := range u.refs {
				if a[r] {
					touched = true
					break
				}
			}
			if !touched {
				continue
			}
			if u.load {
				return true, nil
			}
			done[i], changed = true, true
			for _, n := range u.defines {
				a[n] = true
			}
			for _, r := range u.refs {
				if m.mutable[r] && !a[r] {
					a[r] = true
				}
			}
		}
	}
	names := []string{}
	for ext, locals := range m.exports {
		for _, l := range locals {
			if a[l] {
				names = append(names, ext)
				break
			}
		}
	}
	return false, names
}

// frozenLiteral reports `const X = Object.freeze(<literal of names>)`: one
// binding whose value, and every value in it, no code can change.
func frozenLiteral(text string) bool {
	m := frozen.FindStringSubmatch(strings.TrimSpace(text))
	return m != nil && unitPure.MatchString(strings.ReplaceAll(m[1], "...", ""))
}
