package blockopen

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/ricardov03/code-forge/native/gitx"
)

// APIUse is one brief line `<file>: <symbol>(<param names>)`: the block
// calls that symbol and needs it to take those named parameters.
type APIUse struct {
	File, Symbol string
	Params       []string
}

// APIExtend is one brief line `<file>: <symbol>` under extends: the block may
// add parameters with defaults to that symbol, nothing else.
type APIExtend struct{ File, Symbol string }

type APIFit struct {
	Uses    []APIUse
	Extends []APIExtend
}

var (
	fitSymbol  = `[A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*)*`
	fitUse     = regexp.MustCompile(`^(` + fitSymbol + `)\s*\(([^()]*)\)$`)
	fitExtend  = regexp.MustCompile(`^(` + fitSymbol + `)(?:\s*\([^()]*\))?$`)
	fitParam   = regexp.MustCompile(`^[A-Za-z_$][\w$]*$`)
	fitFence   = regexp.MustCompile("^\\s*```\\s*uses\\s*$")
	fitFenceAt = regexp.MustCompile("^\\s*```")
)

// ParseAPIFit reads the uses:/extends: lines of a brief, from its YAML front
// matter or from fenced ```uses blocks. A brief without them yields nothing.
func ParseAPIFit(brief []byte) (APIFit, error) {
	var fit APIFit
	lines := strings.Split(strings.ReplaceAll(string(brief), "\r\n", "\n"), "\n")
	add := func(section, line string) error {
		entry := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
		if len(entry) >= 2 && (entry[0] == '"' || entry[0] == '\'') && entry[len(entry)-1] == entry[0] {
			entry = entry[1 : len(entry)-1]
		}
		file, rest, ok := strings.Cut(entry, ": ")
		file, rest = strings.TrimSpace(file), strings.TrimSpace(rest)
		bad := func(why string) error {
			return refusal("bad-uses", fmt.Sprintf("brief %s line %s: %s; write <file>: <symbol>(<param names>) under uses: and <file>: <symbol> under extends:", section, quote(entry), why))
		}
		if !ok || file == "" || rest == "" {
			return bad("no <file>: <symbol>")
		}
		if strings.HasPrefix(file, "/") || strings.ContainsAny(file, `*?{}[]\`) || slices.ContainsFunc(strings.Split(file, "/"), func(p string) bool { return p == "" || p == "." || p == ".." }) {
			return bad("the file must be one exact repo-relative path")
		}
		if section == "extends" {
			m := fitExtend.FindStringSubmatch(rest)
			if m == nil {
				return bad("the symbol is not a name")
			}
			fit.Extends = append(fit.Extends, APIExtend{File: file, Symbol: m[1]})
			return nil
		}
		m := fitUse.FindStringSubmatch(rest)
		if m == nil {
			return bad("the symbol needs its parameter names in parentheses")
		}
		params := []string{}
		for _, p := range strings.Split(m[2], ",") {
			if p = strings.TrimSpace(p); p == "" {
				continue
			}
			if !fitParam.MatchString(p) {
				return bad("parameter " + quote(p) + " is not a name")
			}
			params = append(params, p)
		}
		fit.Uses = append(fit.Uses, APIUse{File: file, Symbol: m[1], Params: params})
		return nil
	}
	// header switches the section on `uses:` / `extends:`; an inline entry
	// after the colon counts as the first line of that section.
	header := func(line string, section *string) (bool, error) {
		trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
		for _, name := range []string{"uses", "extends"} {
			if rest, ok := strings.CutPrefix(trimmed, name+":"); ok {
				*section = name
				if rest = strings.TrimSpace(rest); rest != "" {
					return true, add(name, rest)
				}
				return true, nil
			}
		}
		return false, nil
	}
	start := 0
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		section := ""
		for i := 1; i < len(lines); i++ {
			line := lines[i]
			if t := strings.TrimSpace(line); t == "---" || t == "..." {
				start = i + 1
				break
			}
			if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			indented := line[0] == ' ' || line[0] == '\t' || strings.HasPrefix(line, "- ")
			if !indented {
				section = ""
			}
			if isHeader, err := header(line, &section); err != nil {
				return fit, err
			} else if isHeader || section == "" {
				continue
			}
			if err := add(section, line); err != nil {
				return fit, err
			}
		}
	}
	section := ""
	inFence := false
	for _, line := range lines[start:] {
		if !inFence {
			if fitFence.MatchString(line) {
				inFence, section = true, "uses"
			}
			continue
		}
		if fitFenceAt.MatchString(line) {
			inFence = false
			continue
		}
		if t := strings.TrimSpace(line); t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if isHeader, err := header(line, &section); err != nil {
			return fit, err
		} else if isHeader {
			continue
		}
		if err := add(section, line); err != nil {
			return fit, err
		}
	}
	return fit, nil
}

type fitLang struct {
	defs []string // patterns with %s for the quoted symbol, each ending at "("
	body *regexp.Regexp
	name func(string) string
}

var fitLangs = map[string]fitLang{
	".kt":    {defs: []string{`\bfun\s+(?:<[^>]*>\s*)?(?:[\w.<>?, ]+\.)?%s\s*\(`, `\bclass\s+%s\s*(?:<[^>]*>)?\s*(?:[\w@]+\s+)*(?:constructor\s*)?\(`}, name: colonName},
	".swift": {defs: []string{`\bfunc\s+%s\s*(?:<[^>]*>)?\s*\(`}, name: swiftName},
	".go":    {defs: []string{`\bfunc\s+(?:\([^)]*\)\s*)?%s\s*(?:\[[^\]]*\])?\s*\(`}, name: goName},
	".ts":    {defs: jsDefs, body: jsBody, name: colonName},
	".js":    {defs: jsDefs, body: jsBody, name: colonName},
	".mjs":   {defs: jsDefs, body: jsBody, name: colonName},
	".php":   {defs: []string{`\bfunction\s+&?%s\s*\(`}, name: phpName},
	".py":    {defs: []string{`\bdef\s+%s\s*\(`}, name: pyName},
	".java":  {defs: []string{`(?m)^[ \t]*(?:@[\w.]+(?:\([^)]*\))?\s+)*(?:(?:public|private|protected|static|final|abstract|synchronized|native|default|strictfp)\s+)*(?:<[^>]*>\s+)?[\w$.<>\[\]?, ]*?[\w$>\]]\s+%s\s*\(`}, body: regexp.MustCompile(`^\s*(?:throws[^{;]*)?[{;]`), name: javaName},
}

var (
	jsDefs = []string{`\bfunction\s*\*?\s*%s\s*(?:<[^>]*>)?\s*\(`, `\b(?:const|let|var)\s+%s\s*(?::[^=]*)?=\s*(?:async\s+)?(?:function\b\s*\*?\s*[\w$]*\s*)?(?:<[^>]*>\s*)?\(`, `(?m)^[ \t]*(?:(?:public|private|protected|static|async|readonly|override|abstract|get|set)\s+)*\*?%s\s*(?:<[^>]*>)?\s*\(`}
	jsBody = regexp.MustCompile(`^\s*(?::[^;{=]*)?(?:\{|=>)`)
	fitAnn = regexp.MustCompile(`@[\w.]+(?:\([^)]*\))?\s*`)
)

// CheckAPIFit reads each used file at base and compares the symbol's
// parameter names. It returns one api-fit line per mismatch and one note per
// line it could not judge (unknown extension).
func CheckAPIFit(workspace, base string, uses []APIUse) (mismatches, notes []string) {
	for _, use := range uses {
		ext := filepath.Ext(use.File)
		lang, known := fitLangs[ext]
		if !known {
			notes = append(notes, fmt.Sprintf("api-fit: %s %s skipped (no rule for %q files)", use.File, use.Symbol, ext))
			continue
		}
		found := "no file at base"
		if source, err := gitx.Run(workspace, "show", base+":"+use.File); err == nil {
			found = "no definition"
			if lists := definitions(lang, source, use.Symbol); len(lists) > 0 {
				found = strings.Join(lists[0], ",")
				if slices.ContainsFunc(lists, func(names []string) bool { return covers(names, use.Params) }) {
					continue
				}
			}
		}
		mismatches = append(mismatches, fmt.Sprintf("api-fit: %s %s expects (%s) found (%s)", use.File, use.Symbol, strings.Join(use.Params, ","), found))
	}
	return mismatches, notes
}

func covers(names, want []string) bool {
	for _, w := range want {
		if !slices.Contains(names, w) {
			return false
		}
	}
	return true
}

// definitions lists the parameter names of every definition of symbol (its
// last dotted part) in source, in source order.
func definitions(lang fitLang, source, symbol string) [][]string {
	name := symbol[strings.LastIndex(symbol, ".")+1:]
	type hit struct {
		at    int
		names []string
	}
	hits := []hit{}
	seen := map[int]bool{}
	for _, def := range lang.defs {
		pattern := regexp.MustCompile(strings.ReplaceAll(def, "%s", regexp.QuoteMeta(name)))
		for _, loc := range pattern.FindAllStringIndex(source, -1) {
			open := loc[1] - 1
			end := closing(source, open)
			if end < 0 || seen[open] {
				continue
			}
			if lang.body != nil && !lang.body.MatchString(source[end+1:]) {
				continue
			}
			seen[open] = true
			names := []string{}
			for _, p := range splitParams(source[open+1 : end]) {
				if n := lang.name(p); n != "" {
					names = append(names, n)
				}
			}
			hits = append(hits, hit{loc[0], names})
		}
	}
	slices.SortStableFunc(hits, func(a, b hit) int { return a.at - b.at })
	out := [][]string{}
	for _, h := range hits {
		out = append(out, h.names)
	}
	return out
}

// closing returns the index of the ")" that closes the "(" at open, skipping
// nested brackets and quoted strings, or -1.
func closing(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch c := s[i]; c {
		case '"', '\'', '`':
			for i++; i < len(s) && s[i] != c; i++ {
				if s[i] == '\\' {
					i++
				}
			}
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return -1
}

// splitParams splits a parameter list at top-level commas; generics such as
// Map<A, B> stay whole, while -> and => are not closing angles.
func splitParams(list string) []string {
	parts := []string{}
	depth, angle, from := 0, 0, 0
	for i := 0; i < len(list); i++ {
		switch c := list[i]; c {
		case '"', '\'', '`':
			for i++; i < len(list) && list[i] != c; i++ {
				if list[i] == '\\' {
					i++
				}
			}
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case '<':
			if i > 0 && (isWord(list[i-1]) || list[i-1] == '>') {
				angle++
			}
		case '>':
			if angle > 0 && i > 0 && list[i-1] != '-' && list[i-1] != '=' {
				angle--
			}
		case ',':
			if depth == 0 && angle == 0 {
				parts = append(parts, list[from:i])
				from = i + 1
			}
		}
	}
	parts = append(parts, list[from:])
	out := []string{}
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func isWord(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func lastWord(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

func beforeDefault(p string) string {
	p = fitAnn.ReplaceAllString(p, "")
	if i := strings.Index(p, "="); i >= 0 {
		p = p[:i]
	}
	return strings.TrimSpace(p)
}

// colonName: Kotlin, TypeScript and JavaScript put the name before ":".
func colonName(p string) string {
	p = beforeDefault(p)
	if i := strings.Index(p, ":"); i >= 0 {
		p = p[:i]
	}
	return strings.TrimPrefix(strings.TrimSuffix(lastWord(p), "?"), "...")
}

// swiftName returns the call label: the external label unless it is "_".
func swiftName(p string) string {
	p = beforeDefault(p)
	if i := strings.Index(p, ":"); i >= 0 {
		p = p[:i]
	}
	fields := strings.Fields(p)
	if len(fields) == 2 && fields[0] == "_" {
		return fields[1]
	}
	if len(fields) > 0 {
		return fields[0]
	}
	return ""
}

func goName(p string) string {
	if fields := strings.Fields(p); len(fields) > 0 {
		return fields[0]
	}
	return ""
}

func javaName(p string) string { return strings.TrimSuffix(lastWord(beforeDefault(p)), "[]") }

func phpName(p string) string {
	for _, field := range strings.Fields(beforeDefault(p)) {
		if i := strings.Index(field, "$"); i >= 0 {
			return field[i+1:]
		}
	}
	return ""
}

func pyName(p string) string {
	p = beforeDefault(p)
	if i := strings.Index(p, ":"); i >= 0 {
		p = p[:i]
	}
	name := strings.TrimLeft(strings.TrimSpace(p), "*")
	if name == "self" || name == "cls" || name == "/" {
		return ""
	}
	return name
}
