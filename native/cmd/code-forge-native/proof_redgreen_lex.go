package main

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// B481: the text rules of `proof red-green`, as src/proof/red-parse.mjs
// (parseRed) and src/proof/red-green.mjs (deleteAssertions). The CLI only
// reaches deleteAssertions with a node:test file (.mjs, .cjs, .js), so the
// JavaScript rule is the one ported; JavaScript regular expressions are
// written with JS's whitespace (\s) and line-terminator classes.

const (
	rgJSWS  = `[\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]`
	jsEOL   = `[\n\r\x{2028}\x{2029}]`
	rgJSDot = `[^\n\r\x{2028}\x{2029}]`
	jsBOL   = `(?:^|` + jsEOL + `)` // ^ under the m flag (only tested, never sliced)
	jsEOM   = `(?:$|` + jsEOL + `)` // $ under the m flag
)

func jsRe(pattern string) *regexp.Regexp {
	r := strings.NewReplacer(`\s`, rgJSWS, `<BOL>`, jsBOL, `<EOL>`, jsEOM, `<DOT>`, rgJSDot)
	return regexp.MustCompile(r.Replace(pattern))
}

// jsSpace is JS's WhiteSpace or LineTerminator (String.prototype.trim).
func jsSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

func rgJSTrim(s string) string  { return strings.TrimFunc(s, jsSpace) }
func jsTrimEnd(s string) string { return strings.TrimRightFunc(s, jsSpace) }

// jsDecode is Buffer.toString('utf8'): every maximal invalid subpart is one
// U+FFFD, as the WHATWG decoder.
func jsDecode(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var out strings.Builder
	for i := 0; i < len(b); {
		c := b[i]
		if c < 0x80 {
			out.WriteByte(c)
			i++
			continue
		}
		need, lo, hi := 0, byte(0x80), byte(0xbf)
		switch {
		case c >= 0xc2 && c <= 0xdf:
			need = 1
		case c == 0xe0:
			need, lo = 2, 0xa0
		case c == 0xed:
			need, hi = 2, 0x9f
		case c >= 0xe1 && c <= 0xef:
			need = 2
		case c == 0xf0:
			need, lo = 3, 0x90
		case c == 0xf4:
			need, hi = 3, 0x8f
		case c >= 0xf1 && c <= 0xf3:
			need = 3
		default:
			out.WriteRune(utf8.RuneError)
			i++
			continue
		}
		j := i + 1
		for k := 0; k < need; k++ {
			if j >= len(b) || b[j] < lo || b[j] > hi {
				break
			}
			lo, hi = 0x80, 0xbf
			j++
		}
		if j-i == need+1 {
			out.Write(b[i:j])
		} else {
			out.WriteRune(utf8.RuneError)
		}
		i = j
	}
	return out.String()
}

// proofRedParse is parseRed's verdict, kind and failed count.
type proofRedParse struct {
	verdict, kind string
	failed        any
}

var (
	proofANSI          = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	proofInvalidFamily = []struct {
		kind     string
		patterns []*regexp.Regexp
	}{
		{"import", []*regexp.Regexp{
			jsRe(`\bERR_MODULE_NOT_FOUND\b`), jsRe(`\bERR_PACKAGE_PATH_NOT_EXPORTED\b`), jsRe(`\bCannot find (?:module|package) ['"]`),
			jsRe(`\bdoes not provide an export named\b`), jsRe(`\bClass "?[A-Za-z_\\][\w\\]*"? not found\b`), jsRe(`\bFailed opening required\b`),
			jsRe(`\bInterface "?[A-Za-z_\\][\w\\]*"? not found\b`), jsRe(`\b(?:Class|Interface) \\"[A-Za-z_\\][\w\\]*\\" not found\b`),
		}},
		{"compile", []*regexp.Regexp{jsRe(`<BOL>\s*SyntaxError\b`), jsRe(`\bPHP Parse error\b`), jsRe(`\bParseError\b`), jsRe(`<BOL>\s*Parse error:`)}},
		{"fatal", []*regexp.Regexp{jsRe(`\bPHP Fatal error\b`), jsRe(`<BOL>\s*Fatal error:`), jsRe(`\bSegmentation fault\b`), jsRe(`\bAllowed memory size of \d+ bytes exhausted\b`)}},
	}
	proofAssertion = []*regexp.Regexp{
		jsRe(`\bERR_ASSERTION\b`), jsRe(`\bAssertionError\b`), jsRe(`\bFailed asserting that\b`), jsRe(`\bExpectationFailedException\b`), jsRe(`\bexpect\(<DOT>*\)\.\w+\(`),
	}
	proofFailedCount = []*regexp.Regexp{
		jsRe(`<BOL>\s*ℹ fail (\d+)\s*<EOL>`), jsRe(`<BOL># fail (\d+)\s*<EOL>`), jsRe(`<BOL>\s*Tests:\s+(\d+) failed\b`),
	}
)

func proofParseRed(run *proofExecResult) proofRedParse {
	text := proofANSI.ReplaceAllString(run.stdout+"\n"+run.stderr, "")
	var failed any
	for _, pattern := range proofFailedCount {
		if m := pattern.FindStringSubmatch(text); m != nil {
			var n float64
			fmt.Sscan(m[1], &n)
			failed = n
			break
		}
	}
	if run.code != nil && *run.code == 0 && run.signal == "" && !run.timedOut {
		return proofRedParse{"NOT_RED", "none", failed}
	}
	if run.timedOut || run.signal != "" {
		return proofRedParse{"RED_INVALID", "fatal", failed}
	}
	for _, family := range proofInvalidFamily {
		for _, pattern := range family.patterns {
			if pattern.MatchString(text) {
				return proofRedParse{"RED_INVALID", family.kind, failed}
			}
		}
	}
	for _, pattern := range proofAssertion {
		if pattern.MatchString(text) {
			return proofRedParse{"RED", "assertion", failed}
		}
	}
	return proofRedParse{"RED_INVALID", "error", failed}
}

const proofDeletedMarker = "code-forge: assertion deleted"

var (
	proofJSStatement = jsRe(`^(\s*)(?:(\w+)\.)?(assert|expect)\b(?:\.\w+)?\s*\(`)
	proofJSToken     = jsRe(`\bassert\b|\bexpect\s*\(`)
	proofJSExempt    = jsRe(`^\s*(?:import\b|export\b<DOT>*\bfrom\b)|\brequire\s*\(`)
	proofContinues   = regexp.MustCompile("^(?:[()\\[\\]`.,?:+\\-*/%<>=&|^~!]|(?:in|instanceof)\\b)")
	proofStmtBefore  = map[string]bool{"": true, ";": true, "{": true, "}": true}
)

type proofLexed struct {
	masked, start, end, openAtStart, openAtEnd, prev string
	complex                                          bool
}

// proofLex is lex() of red-green.mjs for JavaScript, over UTF-16 units as
// JavaScript strings are.
func proofLex(lines []string) ([]proofLexed, string, string, int) {
	state := "code"
	open := []uint16{}
	mismatch := -1
	prev := ""
	lexed := make([]proofLexed, 0, len(lines))
	closer := map[uint16]uint16{')': '(', ']': '[', '}': '{'}
	for index, text := range lines {
		line := utf16.Encode([]rune(text))
		start := state
		openAtStart := string(utf16.Decode(open))
		masked := []uint16{}
		complex := state == "block" || state == "template"
		for i := 0; i < len(line); i++ {
			ch := line[i]
			var next uint16
			if i+1 < len(line) {
				next = line[i+1]
			}
			if state == "block" {
				if ch == '*' && next == '/' {
					state = "code"
					i++
				}
				continue
			}
			if state == "template" || state == "'" || state == `"` {
				close := uint16('`')
				if state != "template" {
					close = uint16(state[0])
				}
				if ch == '\\' {
					i++
				} else if ch == close {
					masked = append(masked, ch)
					state = "code"
				} else {
					masked = append(masked, ' ')
				}
				continue
			}
			switch {
			case ch == '/' && next == '*':
				state = "block"
				complex = true
				i++
			case ch == '/' && next == '/':
				i = len(line)
			case ch == '`':
				state = "template"
				complex = true
				masked = append(masked, ch)
			case ch == '\'' || ch == '"':
				state = string(rune(ch))
				masked = append(masked, ch)
			default:
				if ch == '(' || ch == '[' || ch == '{' {
					open = append(open, ch)
				} else if opener, ok := closer[ch]; ok {
					var popped uint16
					if len(open) > 0 {
						popped = open[len(open)-1]
						open = open[:len(open)-1]
					}
					if popped != opener && mismatch < 0 {
						mismatch = index
					}
				}
				masked = append(masked, ch)
			}
		}
		maskedText := string(utf16.Decode(masked))
		lexed = append(lexed, proofLexed{masked: maskedText, start: start, end: state, complex: complex, openAtStart: openAtStart, openAtEnd: string(utf16.Decode(open)), prev: prev})
		code := utf16.Encode([]rune(jsTrimEnd(maskedText)))
		if len(code) > 0 {
			prev = string(utf16.Decode(code[len(code)-1:]))
		}
	}
	return lexed, state, string(utf16.Decode(open)), mismatch
}

// proofStatementUnit is /[\w\s.$>-]/ on one UTF-16 unit.
func proofStatementUnit(ch uint16) bool {
	switch {
	case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9', ch == '_', ch == '.', ch == '$', ch == '>', ch == '-':
		return true
	}
	return (ch < 0xd800 || ch > 0xdfff) && jsSpace(rune(ch))
}

// proofOneStatement is oneStatement() for JavaScript.
func proofOneStatement(code string) string {
	text := utf16.Encode([]rune(jsTrimEnd(code)))
	depth := 0
	first := -1
	for i, ch := range text {
		if ch == '(' {
			first = i
			break
		}
	}
	unit := func(i int) string {
		if i < 0 || i >= len(text) {
			return "undefined"
		}
		return string(utf16.Decode(text[i : i+1]))
	}
	for i := first; i < len(text); i++ {
		var ch uint16
		if i >= 0 {
			ch = text[i]
		}
		if i >= 0 && ch == '(' {
			depth++
		} else if i >= 0 && ch == ')' {
			depth--
			if depth < 0 {
				return ""
			}
		} else if depth == 0 {
			if i >= 0 && ch == ';' {
				if i == len(text)-1 {
					return "semicolon"
				}
				return ""
			}
			if i >= 0 && !proofStatementUnit(ch) {
				return ""
			}
			if i >= 0 && ch == '-' && unit(i+1) != ">" {
				return ""
			}
			if i >= 0 && ch == '>' && unit(i-1) != "-" {
				return ""
			}
		}
	}
	if depth != 0 {
		return ""
	}
	if len(text) > 0 && text[len(text)-1] == ')' {
		return "bare"
	}
	return ""
}

// proofDeleteAssertions is deleteAssertions() for a JavaScript test file.
func proofDeleteAssertions(file string, data []byte) ([]byte, error) {
	lines := strings.Split(jsDecode(data), "\n")
	lexed, end, open, mismatch := proofLex(lines)
	if end != "code" || open != "" {
		return nil, &proofError{"unparseable-test", "assertion deletion: " + file + " ends inside a comment, string, heredoc or an open bracket"}
	}
	if mismatch >= 0 {
		return nil, &proofError{"unparseable-test", fmt.Sprintf("assertion deletion: %s closes a bracket it did not open at line %d", file, mismatch+1)}
	}
	refuse := func(i int) error {
		return &proofError{"unclassified-assertion", fmt.Sprintf("assertion deletion: the assertion at line %d is not one whole single-line statement at statement position", i+1)}
	}
	deleted := 0
	out := append([]string{}, lines...)
	for i := 0; i < len(lines); i++ {
		l := lexed[i]
		if l.start != "code" {
			continue
		}
		m := proofJSStatement.FindStringSubmatchIndex(l.masked)
		if m == nil {
			if proofJSToken.MatchString(l.masked) && !proofJSExempt.MatchString(l.masked) {
				return nil, refuse(i)
			}
			continue
		}
		if l.complex || l.end != "code" {
			return nil, refuse(i)
		}
		if (l.openAtStart != "" && !strings.HasSuffix(l.openAtStart, "{")) || !proofStmtBefore[l.prev] {
			return nil, refuse(i)
		}
		last := i
		if proofOneStatement(l.masked) == "" || lexed[last].openAtEnd != l.openAtStart {
			return nil, refuse(i)
		}
		for _, nextLine := range lexed[last+1:] {
			trimmed := rgJSTrim(nextLine.masked)
			if trimmed == "" {
				continue
			}
			if proofContinues.MatchString(trimmed) {
				return nil, refuse(i)
			}
			break
		}
		indent := l.masked[m[2]:m[3]]
		owner := ""
		if m[4] >= 0 {
			owner = l.masked[m[4]:m[5]]
		}
		var replacement string
		if l.masked[m[6]:m[7]] == "expect" {
			if owner != "" {
				return nil, refuse(i)
			}
			replacement = indent + "expect('" + proofDeletedMarker + "').toBe('');"
		} else {
			if owner != "" {
				owner += "."
			}
			replacement = indent + owner + "assert.fail('" + proofDeletedMarker + "');"
		}
		deleted++
		out[i] = replacement
	}
	if deleted == 0 {
		return nil, &proofError{"no-assertions", "assertion deletion: " + file + " has no single-line assertion statement"}
	}
	return []byte(strings.Join(out, "\n")), nil
}
