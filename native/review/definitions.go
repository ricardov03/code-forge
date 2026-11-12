package review

// B537: src/review/definitions.mjs — the bodies of functions the changed lines
// call: JavaScript/TypeScript (local and relative imports) and Go (top-level
// functions of the file and its package siblings). Missing or malformed
// source never fails the packet.

import (
	"path"
	"regexp"
	"strconv"
	"strings"
)

// definition is definitions.mjs Definition.
type definition struct {
	Path  string
	Name  string
	Start int
	End   int
	Lines []string
}

var (
	defKeywords   = map[string]bool{"if": true, "for": true, "while": true, "switch": true, "catch": true, "function": true, "return": true, "typeof": true, "new": true}
	defExtensions = []string{".mjs", ".js", ".ts", ".cjs", ".tsx"}
	defIdentRE    = regexp.MustCompile(`^[A-Za-z_$][\w$]*$`)
	defGoIdentRE  = regexp.MustCompile(`^[\p{L}_][\p{L}\p{Nd}_]*$`)
	defJSTokenRE  = regexp.MustCompile("//[^\\r\\n]*|/\\*" + jsAny + "*?\\*/|`(?:\\\\" + jsAny + "|[^`\\\\])*`|'(?:\\\\" + jsAny + "|[^'\\\\])*'|\"(?:\\\\" + jsAny + "|[^\"\\\\])*\"|/(?:\\\\" + jsDot + "|\\[(?:\\\\" + jsDot + "|[^\\]\\\\])*\\]|[^/\\n\\\\])+/[dgimsuvy]*|[A-Za-z_$][\\w$]*|=>|" + jsNonWS)
	defGoTokenRE  = regexp.MustCompile("//[^\\r\\n]*|/\\*" + jsAny + "*?\\*/|`[^`]*`|'(?:\\\\" + jsAny + "|[^'\\\\])*'|\"(?:\\\\" + jsAny + "|[^\"\\\\])*\"|[\\p{L}_][\\p{L}\\p{Nd}_]*|" + jsNonWS)
	defCommentRE  = regexp.MustCompile(`^/[/*]`)
	defHunkRE     = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)
	defLineSplit  = regexp.MustCompile(`\r?\n`)
	defQuotedRel  = regexp.MustCompile(`^['"]\.\.?/`)
)

type defToken struct {
	value string
	line  int
}

// defTokensOf is tokensOf: comments dropped, each token with its 1-based line.
func defTokensOf(text string, goSyntax bool) []defToken {
	var raw []jsToken
	if goSyntax {
		for _, m := range defGoTokenRE.FindAllStringIndex(text, -1) {
			raw = append(raw, jsToken{text[m[0]:m[1]], m[0]})
		}
	} else {
		raw = jsTokens(defJSTokenRE, text)
	}
	out := []defToken{}
	line, offset := 1, 0
	for _, t := range raw {
		if t.index > offset {
			line += strings.Count(text[offset:t.index], "\n")
			offset = t.index
		}
		if !defCommentRE.MatchString(t.value) {
			out = append(out, defToken{t.value, line})
		}
	}
	return out
}

// defAt is tokens[i]?.value.
func defAt(tokens []defToken, i int) (string, bool) {
	if i < 0 || i >= len(tokens) {
		return "", false
	}
	return tokens[i].value, true
}

func defIs(tokens []defToken, i int, v string) bool {
	t, ok := defAt(tokens, i)
	return ok && t == v
}

func defIn(v string, ok bool, list ...string) bool {
	if !ok {
		return false
	}
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// defClosing is closing(): the index of the bracket closing tokens[start], -1
// when there is none.
func defClosing(tokens []defToken, start int) int {
	open, ok := defAt(tokens, start)
	if !ok {
		return -1
	}
	closer := map[string]string{"(": ")", "{": "}", "[": "]"}[open]
	if closer == "" {
		return -1
	}
	depth := 0
	for i := start; i < len(tokens); i++ {
		if tokens[i].value == open {
			depth++
		}
		if tokens[i].value == closer {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func defShown(file, name string, lines []string, firstLine, lastLine int) definition {
	lastShown := lastLine
	if firstLine+39 < lastShown {
		lastShown = firstLine + 39
	}
	shown := []string{}
	for n := firstLine; n <= lastShown && n-1 < len(lines); n++ {
		if n >= 1 {
			shown = append(shown, lines[n-1])
		}
	}
	if lastLine > lastShown {
		shown = append(shown, "…")
	}
	return definition{Path: file, Name: name, Start: firstLine, End: lastShown, Lines: shown}
}

// defDeclaration is declaration().
func defDeclaration(text, file, name string, exported bool) *definition {
	tokens := defTokensOf(text, false)
	lines := defLineSplit.Split(text, -1)
	for i := range tokens {
		if tokens[i].value != name {
			continue
		}
		prev, hasPrev := defAt(tokens, i-1)
		kind := ""
		if defIn(prev, hasPrev, "function", "const", "class") {
			kind = prev
		}
		start := i - 1
		for start > 0 && defIn(tokens[start-1].value, true, "async", "default", "export", "declare") {
			start--
		}
		if exported {
			found := false
			for k := start; k >= 0 && k < i; k++ {
				if tokens[k].value == "export" {
					found = true
				}
			}
			if !found {
				continue
			}
		}
		if kind == "" && (exported || !defIs(tokens, i+1, "(") || !defIn(prev, hasPrev, "{", "}", ";", "async", "static", "get", "set")) {
			continue
		}
		if kind == "" && !defIs(tokens, defClosing(tokens, i+1)+1, "{") {
			continue
		}
		if kind == "" {
			if defIn(prev, hasPrev, "async", "static", "get", "set") {
				start = i - 1
			} else {
				start = i
			}
		}
		if kind == "const" && !defIs(tokens, i+1, "=") {
			continue
		}
		end := -1
		for body := i + 1; body < len(tokens); body++ {
			value := tokens[body].value
			if value == "(" || value == "[" {
				last := defClosing(tokens, body)
				if last < 0 {
					break
				}
				body = last
			} else if value == "{" {
				end = defClosing(tokens, body)
				break
			} else if value == ";" || (kind == "const" && tokens[body].line > tokens[i].line && !defIs(tokens, body-1, "=")) {
				if value == ";" {
					end = body
				} else {
					end = body - 1
				}
				break
			}
		}
		if end < 0 {
			continue
		}
		d := defShown(file, name, lines, tokens[start].line, tokens[end].line)
		return &d
	}
	return nil
}

// defGoDeclarations is goDeclarations(): top-level functions and methods.
func defGoDeclarations(text, file string) []definition {
	tokens := defTokensOf(text, true)
	lines := defLineSplit.Split(text, -1)
	found := []definition{}
	for i := 0; i < len(tokens); i++ {
		if tokens[i].value == "{" {
			end := defClosing(tokens, i)
			if end < 0 {
				break
			}
			i = end
			continue
		}
		if tokens[i].value != "func" {
			continue
		}
		name := i + 1
		if defIs(tokens, name, "(") {
			receiverEnd := defClosing(tokens, name)
			if receiverEnd < 0 {
				continue
			}
			name = receiverEnd + 1
		}
		nameValue, _ := defAt(tokens, name)
		if !defGoIdentRE.MatchString(nameValue) {
			continue
		}
		body := name + 1
		if defIs(tokens, body, "[") {
			body = defClosing(tokens, body) + 1
		}
		if !defIs(tokens, body, "(") {
			continue
		}
		paramsEnd := defClosing(tokens, body)
		if paramsEnd < 0 {
			continue
		}
		body = paramsEnd + 1
		for ; body < len(tokens); body++ {
			value := tokens[body].value
			pv, pok := defAt(tokens, body-1)
			if value == "(" || value == "[" || (value == "{" && defIn(pv, pok, "struct", "interface")) {
				end := defClosing(tokens, body)
				if end < 0 {
					break
				}
				body = end
			} else if value == "{" || value == "func" || value == ";" {
				break
			}
		}
		if !defIs(tokens, body, "{") {
			continue
		}
		end := defClosing(tokens, body)
		if end < 0 {
			continue
		}
		found = append(found, defShown(file, nameValue, lines, tokens[i].line, tokens[end].line))
		i = end
	}
	return found
}

// defIO is findDefinitions' io: a cached repo-relative read, the listing,
// and the current file's shown windows.
type defIO struct {
	read    func(rel string) *string
	list    func() []string
	windows []window
	cache   map[string]*string
}

func (io *defIO) cached(rel string) *string {
	if !defSafePath(rel) {
		return nil
	}
	if io.cache == nil {
		io.cache = map[string]*string{}
	}
	if v, ok := io.cache[rel]; ok {
		return v
	}
	v := io.read(rel)
	io.cache[rel] = v
	return v
}

func (io *defIO) inWindow(d *definition) bool {
	for _, w := range io.windows {
		if w.start <= d.Start && d.End <= w.end {
			return true
		}
	}
	return false
}

func defFind(list []definition, name string) *definition {
	for k := range list {
		if list[k].Name == name {
			d := list[k]
			return &d
		}
	}
	return nil
}

// defGoDefinitions is goDefinitions().
func defGoDefinitions(text, file string, diffLines []string, io *defIO) []definition {
	added := map[int]bool{}
	line := 0
	for _, dl := range diffLines {
		if m := defHunkRE.FindStringSubmatch(dl); m != nil {
			n, err := strconv.Atoi(m[1])
			if err != nil {
				n = 1 << 53
			}
			line = n
		} else if line > 0 && strings.HasPrefix(dl, "+") {
			added[line] = true
			line++
		} else if line > 0 && strings.HasPrefix(dl, " ") {
			line++
		}
	}
	local := defGoDeclarations(text, file)
	defined := map[string]bool{}
	for _, d := range local {
		if added[d.Start] {
			defined[d.Name] = true
		}
	}
	siblings := []string{}
	if io.list != nil {
		for _, f := range unique(io.list()) {
			if defSafePath(f) && f != file && path.Dir(f) == path.Dir(file) && strings.HasSuffix(f, ".go") &&
				(strings.HasSuffix(file, "_test.go") || !strings.HasSuffix(f, "_test.go")) {
				siblings = append(siblings, f)
			}
		}
		sortUTF16(siblings)
	}
	declarations := map[string][]definition{file: local}
	found := []definition{}
	seen := map[string]bool{}
	tokens := defTokensOf(text, true)
	for i := range tokens {
		name := tokens[i].value
		if !added[tokens[i].line] || !defIs(tokens, i+1, "(") || !defGoIdentRE.MatchString(name) || defKeywords[name] || defined[name] {
			continue
		}
		d := defFind(local, name)
		if d != nil && io.inWindow(d) {
			d = nil
		}
		if d == nil {
			for _, f := range siblings {
				if _, ok := declarations[f]; !ok {
					if src := io.cached(f); src != nil {
						declarations[f] = defGoDeclarations(*src, f)
					} else {
						declarations[f] = []definition{}
					}
				}
				if d = defFind(declarations[f], name); d != nil {
					break
				}
			}
		}
		if d == nil {
			continue
		}
		key := d.Path + ":" + strconv.Itoa(d.Start)
		if !seen[key] {
			seen[key] = true
			found = append(found, *d)
		}
	}
	return found
}

type defBinding struct {
	name, specifier string
	namespace       bool
}

// defImportsOf is importsOf().
func defImportsOf(header string) map[string]defBinding {
	tokens := defTokensOf(header, false)
	imports := map[string]defBinding{}
	for i := 0; i < len(tokens); i++ {
		if tokens[i].value != "import" {
			continue
		}
		if n, ok := defAt(tokens, i+1); defIn(n, ok, "(", ".") {
			continue
		}
		end := i + 1
		for end < len(tokens) && !defIn(tokens[end].value, true, "from", ";", "import") {
			end++
		}
		if !defIs(tokens, end, "from") {
			continue
		}
		source, _ := defAt(tokens, end+1)
		if !defQuotedRel.MatchString(source) {
			continue
		}
		specifier := jsSlice(source)
		for j := i + 1; j < end; j++ {
			name := tokens[j].value
			if !defIdentRE.MatchString(name) || name == "as" || name == "type" {
				continue
			}
			if defIs(tokens, j-1, "as") {
				continue
			}
			alias, aliasOK := name, true
			if defIs(tokens, j+1, "as") {
				alias, aliasOK = defAt(tokens, j+2)
			}
			if aliasOK {
				imports[alias] = defBinding{name, specifier, defIs(tokens, j-1, "*")}
			}
		}
		if defIs(tokens, i+1, "*") && defIs(tokens, i+2, "as") {
			if owner, ok := defAt(tokens, i+3); ok {
				imports[owner] = defBinding{"*", specifier, true}
			}
		}
		i = end + 1
	}
	return imports
}

// findDefinitions is definitions.mjs findDefinitions.
func findDefinitions(file string, diffLines []string, header string, io *defIO) []definition {
	found := []definition{}
	goFile := lowerExt(file) == ".go"
	if !defSafePath(file) {
		return found
	}
	if !goFile {
		known := false
		for _, e := range defExtensions {
			if lowerExt(file) == e {
				known = true
			}
		}
		if !known {
			return found
		}
	}
	text := io.cached(file)
	if text == nil {
		return found
	}
	if goFile {
		return defGoDefinitions(*text, file, diffLines, io)
	}
	addedLines := []string{}
	for _, l := range diffLines {
		if strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++") {
			addedLines = append(addedLines, l[1:])
		}
	}
	added := defTokensOf(strings.Join(addedLines, "\n"), false)
	defined := map[string]bool{}
	for i, t := range added {
		p, ok := defAt(added, i-1)
		if defIn(p, ok, "function", "const", "class", "let", "var") ||
			(defIs(added, i+1, "(") && defIs(added, defClosing(added, i+1)+1, "{")) {
			defined[t.value] = true
		}
	}
	imports := defImportsOf(header)
	seen := map[string]bool{}
	for i := range added {
		name := added[i].value
		if !defIdentRE.MatchString(name) || !defIs(added, i+1, "(") || defKeywords[name] || defined[name] {
			continue
		}
		receiver, hasReceiver := "", false
		if defIs(added, i-1, ".") {
			receiver, hasReceiver = defAt(added, i-2)
		}
		key := name
		if hasReceiver {
			key = receiver
		}
		binding, hasBinding := imports[key]
		d := defDeclaration(*text, file, name, false)
		if d != nil && io.inWindow(d) {
			d = nil
		}
		if d == nil && hasBinding && (!hasReceiver || binding.namespace) {
			target := path.Join(path.Dir(file), binding.specifier)
			if strings.HasSuffix(binding.specifier, "/") || !defSafePath(target) {
				continue
			}
			candidates := []string{target}
			if posixExtname(target) == "" {
				for _, e := range defExtensions {
					candidates = append(candidates, target+e)
				}
				for _, e := range defExtensions {
					candidates = append(candidates, target+"/index"+e)
				}
			}
			for _, c := range candidates {
				imported := io.cached(c)
				if imported == nil {
					continue
				}
				want := binding.name
				if binding.namespace {
					want = name
				}
				d = defDeclaration(*imported, c, want, true)
				break
			}
		}
		if d == nil {
			continue
		}
		k := d.Path + ":" + strconv.Itoa(d.Start)
		if !seen[k] {
			seen[k] = true
			found = append(found, *d)
		}
	}
	return found
}

// definitionsSection is packet.mjs definitionsSection.
func definitionsSection(defs []definition) string {
	if len(defs) == 0 {
		return ""
	}
	parts := []string{"## definitions (called by changed lines)"}
	for _, d := range defs {
		parts = append(parts, "### "+d.Path+":"+strconv.Itoa(d.Start)+"-"+strconv.Itoa(d.End))
		for i, l := range d.Lines {
			if l == "…" && i+d.Start > d.End {
				parts = append(parts, l)
			} else {
				parts = append(parts, strconv.Itoa(d.Start+i)+"| "+l)
			}
		}
	}
	return strings.Join(parts, "\n") + "\n"
}
