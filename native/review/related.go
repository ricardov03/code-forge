package review

// B537: src/review/related.mjs — the names-only related files of a packet:
// outgoing local imports, tracked importers (at most 2000 candidates), a
// stylesheet's cascade siblings, and a Go file's same-package files.

import (
	"path"
	"regexp"
	"strings"
)

var relExtensions = []string{".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts", ".css", ".scss", ".html", ".htm", ".astro", ".vue"}

func relIsStyle(ext string) bool { return ext == ".css" || ext == ".scss" }

func relKnownExt(ext string) bool {
	for _, e := range relExtensions {
		if e == ext {
			return true
		}
	}
	return false
}

var (
	relTokenRE   = regexp.MustCompile("//[^\\r\\n]*|/\\*" + jsAny + "*?\\*/|`(?:\\\\" + jsAny + "|[^`\\\\])*`|'(?:\\\\" + jsAny + "|[^'\\\\])*'|\"(?:\\\\" + jsAny + "|[^\"\\\\])*\"|[\\w$]+|" + jsNonWS)
	relCommentRE = regexp.MustCompile(`/\*` + jsAny + `*?\*/|<!--` + jsAny + `*?-->`)
	relImportRE  = regexp.MustCompile(`@` + ciLiteral("import") + jsWS + `+(?:` + ciLiteral("url") + `\(` + jsWS + `*(?:'([^']+)'|"([^"]+)"|([^\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff})]+))` + jsWS + `*\)|'([^']+)'|"([^"]+)")`)
	relTagRE     = regexp.MustCompile(`<(` + ciLiteral("link") + `|` + ciLiteral("script") + `)\b[^>]*>`)
	relAttrRE    = map[string]*regexp.Regexp{
		"href": regexp.MustCompile(jsWS + ciLiteral("href") + jsWS + `*=` + jsWS + `*(?:"([^"]+)"|'([^']+)'|([^\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}>]+))`),
		"src":  regexp.MustCompile(jsWS + ciLiteral("src") + jsWS + `*=` + jsWS + `*(?:"([^"]+)"|'([^']+)'|([^\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}>]+))`),
	}
	relURLSkip = regexp.MustCompile(`^(?:[a-zA-Z][a-zA-Z\d+.-]*:|/|[?#])`)
	relDotRel  = regexp.MustCompile(`^\.\.?/`)
)

// firstGroup is match.slice(1).find((v) => v !== undefined).
func firstGroup(text string, m []int) string {
	for g := 1; 2*g < len(m); g++ {
		if m[2*g] >= 0 {
			return text[m[2*g]:m[2*g+1]]
		}
	}
	return ""
}

// relSpecifiers is related.mjs specifiers.
func relSpecifiers(text, file string) []string {
	ext := lowerExt(file)
	found := []string{}
	url := func(v string) {
		if !relURLSkip.MatchString(v) {
			if relDotRel.MatchString(v) {
				found = append(found, v)
			} else {
				found = append(found, "./"+v)
			}
		}
	}
	if !relIsStyle(ext) {
		tokens := []string{}
		for _, t := range jsTokens(relTokenRE, text) {
			if !strings.HasPrefix(t.value, "//") && !strings.HasPrefix(t.value, "/*") && !strings.HasPrefix(t.value, "`") {
				tokens = append(tokens, t.value)
			}
		}
		at := func(i int) (string, bool) {
			if i < 0 || i >= len(tokens) {
				return "", false
			}
			return tokens[i], true
		}
		quoted := func(i int) bool {
			t, ok := at(i)
			return ok && (strings.HasPrefix(t, `"`) || strings.HasPrefix(t, "'"))
		}
		for i, token := range tokens {
			if token != "import" && token != "require" {
				continue
			}
			if p, ok := at(i - 1); ok && p == "." {
				continue
			}
			if n, _ := at(i + 1); n == "(" && quoted(i+2) && tokens[i+2] != "" {
				if c, ok := at(i + 3); ok && c == ")" {
					found = append(found, jsSlice(tokens[i+2]))
					continue
				}
			}
			if token == "import" {
				if quoted(i + 1) {
					found = append(found, jsSlice(tokens[i+1]))
				} else {
					for j := i + 1; j < len(tokens); j++ {
						t := tokens[j]
						if t == ";" || t == "import" || t == "require" || t == "(" {
							break
						}
						if t == "from" && quoted(j+1) {
							found = append(found, jsSlice(tokens[j+1]))
							break
						}
					}
				}
			}
		}
	}
	if relIsStyle(ext) || ext == ".html" || ext == ".htm" || ext == ".astro" || ext == ".vue" {
		markup := relCommentRE.ReplaceAllString(text, "")
		for _, m := range relImportRE.FindAllStringSubmatchIndex(markup, -1) {
			url(firstGroup(markup, m))
		}
		if !relIsStyle(ext) {
			for _, m := range relTagRE.FindAllStringSubmatchIndex(markup, -1) {
				tag := markup[m[0]:m[1]]
				attr := "src"
				if asciiLower(markup[m[2]:m[3]]) == "link" {
					attr = "href"
				}
				if a := relAttrRE[attr].FindStringSubmatchIndex(tag); a != nil {
					url(firstGroup(tag, a))
				}
			}
		}
	}
	return found
}

// jsSlice is token.slice(1, -1) for a quoted token (at least one character:
// a lone quote gives ”).
func jsSlice(t string) string {
	if len(t) < 2 {
		return ""
	}
	return t[1 : len(t)-1]
}

// relNormalizeJoin is path.posix.normalize(path.posix.join(dir, spec)) for a
// spec starting with ./ or ../; ok false when Node's result keeps a trailing
// slash (never a safe path).
func relNormalizeJoin(dir, spec string) (string, bool) {
	if strings.HasSuffix(spec, "/") {
		return "", false
	}
	return path.Join(dir, spec), true
}

// relatedIO is the packet reader's I/O: a repo-relative read (nil: missing,
// unreadable or refused) and the tracked listing (nil: listing failed).
type relatedIO struct {
	read  func(rel string) *string
	list  func() []string
	cache map[string]*string
}

func (io *relatedIO) cached(rel string) *string {
	if !relSafePath(rel) {
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

// relatedFiles is related.mjs relatedFiles.
func relatedFiles(file string, rio relatedIO) []string {
	if !relSafePath(file) {
		return []string{}
	}
	if lowerExt(file) == ".go" {
		out := []string{}
		for _, rel := range unique(rio.list()) {
			if relSafePath(rel) && rel != file && path.Dir(rel) == path.Dir(file) && strings.HasSuffix(rel, ".go") &&
				(strings.HasSuffix(file, "_test.go") || !strings.HasSuffix(rel, "_test.go")) {
				out = append(out, rel)
			}
		}
		return relFirst12(out)
	}
	if !relKnownExt(lowerExt(file)) {
		return []string{}
	}
	cache := &relatedIO{read: rio.read}
	imports := func(rel string) (map[string]bool, []string) {
		set := map[string]bool{}
		order := []string{}
		text := cache.cached(rel)
		if text == nil {
			return set, order
		}
		for _, spec := range relSpecifiers(*text, rel) {
			if !relDotRel.MatchString(spec) {
				continue
			}
			part := spec
			if i := strings.IndexAny(part, "?#"); i >= 0 {
				part = part[:i]
			}
			target, ok := relNormalizeJoin(path.Dir(rel), part)
			if !ok || !relSafePath(target) {
				continue
			}
			candidates := []string{target}
			if posixExtname(target) == "" {
				for _, e := range relExtensions {
					candidates = append(candidates, target+e)
				}
				for _, e := range relExtensions {
					candidates = append(candidates, target+"/index"+e)
				}
			}
			for _, c := range candidates {
				if cache.cached(c) != nil {
					if !set[c] {
						set[c] = true
						order = append(order, c)
					}
					break
				}
			}
		}
		return set, order
	}
	_, own := imports(file)
	related := map[string]bool{}
	for _, r := range own {
		related[r] = true
	}
	tracked := []string{}
	for _, rel := range unique(rio.list()) {
		if relSafePath(rel) && relKnownExt(lowerExt(rel)) {
			tracked = append(tracked, rel)
		}
	}
	if len(tracked) <= 2000 {
		for _, importer := range tracked {
			if importer == file {
				continue
			}
			targets, order := imports(importer)
			if !targets[file] {
				continue
			}
			related[importer] = true
			if relIsStyle(lowerExt(file)) {
				for _, sibling := range order {
					if relIsStyle(lowerExt(sibling)) {
						related[sibling] = true
					}
				}
			}
		}
	}
	delete(related, file)
	out := []string{}
	for r := range related {
		out = append(out, r)
	}
	return relFirst12(out)
}

func relFirst12(list []string) []string {
	sorted := append([]string{}, list...)
	sortUTF16(sorted)
	if len(sorted) > 12 {
		sorted = sorted[:12]
	}
	return sorted
}

func sortUTF16(list []string) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && lessUTF16Str(list[j], list[j-1]); j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}
