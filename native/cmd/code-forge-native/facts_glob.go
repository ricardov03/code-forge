package main

import (
	"runtime"
	"strings"
	"unicode/utf16"
)

// B568: glob path claims (src/session/facts.mjs globRule and the glob hint of
// buildFactsPacket). A VERIFIED glob row is checked with Node's
// path.matchesGlob (minimatch: nocase on macOS and Windows, nocaseMagicOnly,
// windowsPathsNoEscape, nonegate, nocomment, optimizationLevel 2).
// fcMatchesGlob models the patterns made of ASCII text, * ? ** and [...]
// classes; a glob with a backslash, a brace, a paren, a POSIX class or a
// non-ASCII character is not modelled and hands over before the run.

// fcGlobNocase is minimatch's nocase default for path.matchesGlob.
var fcGlobNocase = runtime.GOOS == "darwin" || runtime.GOOS == "windows"

// fcGlobUnmodelled: a glob fcMatchesGlob does not model.
func fcGlobUnmodelled(token string) bool {
	if strings.ContainsAny(token, "\\(){}") || strings.Contains(token, "[:") {
		return true
	}
	for i := 0; i < len(token); i++ {
		if token[i] >= 0x80 {
			return true
		}
	}
	return false
}

// fcPosixDirname is path.posix.dirname.
func fcPosixDirname(p string) string {
	if p == "" {
		return "."
	}
	abs := p[0] == '/'
	end := len(p)
	for end > 1 && p[end-1] == '/' {
		end--
	}
	i := strings.LastIndex(p[:end], "/")
	if i < 0 {
		return "."
	}
	for i > 0 && p[i-1] == '/' {
		i--
	}
	if i == 0 {
		if abs {
			return "/"
		}
		return "."
	}
	return p[:i]
}

// fcPosixBasename is path.posix.basename.
func fcPosixBasename(p string) string {
	end := len(p)
	for end > 0 && p[end-1] == '/' {
		end--
	}
	if end == 0 {
		return ""
	}
	return p[strings.LastIndex(p[:end], "/")+1 : end]
}

// fcShellQuote is shellQuote.
func fcShellQuote(value string, always bool) string {
	if !always && value != "" && strings.Trim(value, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./-") == "" {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// fcGlobHint is buildFactsPacket's check hint for a glob path claim.
func fcGlobHint(token string) string {
	dir := fcPosixDirname(token)
	if fcHasGlob(dir) {
		return " [check with ls -d " + fcShellQuote(token, false) + "]"
	}
	return " [check with find " + fcShellQuote(dir, false) + " -name " + fcShellQuote(fcPosixBasename(token), true) + "]"
}

const fcGlobProofMessage = "a glob needs find <literal dir> -name <quoted pattern> or ls -d <exact glob>; not counted"

// fcFindNameQuoted is /-name (['"])[^'"]+\1$/.
func fcFindNameQuoted(command string) bool {
	if command == "" {
		return false
	}
	q := command[len(command)-1]
	if q != '\'' && q != '"' {
		return false
	}
	body := command[:len(command)-1]
	open := strings.LastIndexAny(body, `'"`)
	if open < 0 || body[open] != q || open == len(body)-1 {
		return false
	}
	return strings.HasSuffix(body[:open], "-name ")
}

// fcGlobRule is globRule.
func fcGlobRule(row fcFact, claim fcClaim) string {
	token := claim.token
	if claim.hasCheckPath {
		token = claim.checkPath
	}
	split := fcSplitCommandGlob(row.command, true)
	var argv []string
	if len(split.segments) > 0 {
		argv = split.segments[0]
	}
	dir := fcPosixDirname(token)
	ls := len(argv) == 3 && argv[0] == "ls" && argv[1] == "-d" && argv[2] == token
	find := !fcHasGlob(dir) && len(argv) == 4 && argv[0] == "find" && argv[1] == dir && argv[2] == "-name" &&
		argv[3] == fcPosixBasename(token) && fcReadOnlyViolation(row.command) == "" && fcFindNameQuoted(row.command)
	if split.refused != "" || len(split.separators) > 0 || len(split.segments) != 1 || (!ls && !find) || fcSegmentViolation(argv) != "" {
		return fcGlobProofMessage
	}
	if row.tag == "NOT-FOUND" {
		if strings.Contains(strings.ToLower(row.excerpt), "no such file") {
			return ""
		}
		return "a NOT-FOUND glob needs No such file output; not counted"
	}
	pattern := fcRelPosix(token)
	for _, line := range fcGlobLines(row.excerpt) {
		if fcMatchesGlob(fcRelPosix(fcTrim(line)), pattern, fcGlobNocase) {
			return ""
		}
	}
	return "VERIFIED glob without a matching output path; not counted"
}

// fcGlobLines is text.split(/\r?\n/).
func fcGlobLines(text string) []string {
	parts := strings.Split(text, "\n")
	for i, p := range parts {
		parts[i] = strings.TrimSuffix(p, "\r")
	}
	return parts
}

type fcGlobTok struct {
	kind   byte // 'l' literal unit, '*' star, '?' one unit, '[' class, 'x' never matches
	unit   uint16
	negate bool
	ranges [][2]uint16
}

type fcGlobSeg struct {
	globstar bool
	literal  bool
	text     string
	fast     func(f string, nocase bool) bool
	toks     []fcGlobTok
}

// fcJSLower is String#toLowerCase for the ends-with checks of the fast tests.
func fcJSLower(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "İ", "i̇"))
}

func fcUTF16Len(s string) int { return len(utf16.Encode([]rune(s))) }

// fcGlobFast is minimatch parse()'s fast tests for whole-segment shapes.
func fcGlobFast(seg string) func(f string, nocase bool) bool {
	stars := len(seg) - len(strings.TrimLeft(seg, "*"))
	endsWith := func(f, ext string, nocase bool) bool {
		if nocase {
			return strings.HasSuffix(fcJSLower(f), strings.ToLower(ext))
		}
		return strings.HasSuffix(f, ext)
	}
	switch {
	case stars == len(seg): // /^\*+$/
		return func(f string, _ bool) bool { return f != "" && !strings.HasPrefix(f, ".") }
	case stars > 0 && seg[stars] == '.' && !strings.ContainsAny(seg[stars:], "!?*[("): // /^\*+(\.[^!?\*\[\(]*)$/
		ext := seg[stars:]
		return func(f string, nocase bool) bool { return !strings.HasPrefix(f, ".") && endsWith(f, ext, nocase) }
	}
	if q := len(seg) - len(strings.TrimLeft(seg, "?")); q > 0 && !strings.ContainsAny(seg[q:], "+@!?*[(") { // /^\?+([^+@!?\*\[\(]*)?$/
		ext, n := seg[q:], len(seg)
		return func(f string, nocase bool) bool {
			if fcUTF16Len(f) != n || strings.HasPrefix(f, ".") {
				return false
			}
			return ext == "" || endsWith(f, ext, nocase)
		}
	}
	if stars > 0 && seg[stars] == '.' && strings.Trim(seg[stars+1:], "*") == "" && len(seg) > stars+1 { // /^\*+\.\*+$/
		return func(f string, _ bool) bool { return !strings.HasPrefix(f, ".") && strings.Contains(f, ".") }
	}
	if len(seg) > 1 && seg[0] == '.' && strings.Trim(seg[1:], "*") == "" { // /^\.\*+$/
		return func(f string, _ bool) bool { return f != "." && f != ".." && strings.HasPrefix(f, ".") }
	}
	return nil
}

// fcGlobClass is minimatch's parseClass at seg[pos] == '['; consumed 0 =
// not a class (the '[' is a literal).
func fcGlobClass(seg string, pos int) (tok fcGlobTok, consumed int) {
	var ranges [][2]uint16
	i, sawStart, negate, endPos := pos+1, false, false, pos
	rangeStart := -1
	for i < len(seg) {
		c := seg[i]
		if (c == '!' || c == '^') && i == pos+1 {
			negate = true
			i++
			continue
		}
		if c == ']' && sawStart {
			endPos = i + 1
			break
		}
		sawStart = true
		if rangeStart >= 0 {
			if int(c) > rangeStart {
				ranges = append(ranges, [2]uint16{uint16(rangeStart), uint16(c)})
			} else if int(c) == rangeStart {
				ranges = append(ranges, [2]uint16{uint16(c), uint16(c)})
			}
			rangeStart = -1
			i++
			continue
		}
		if strings.HasPrefix(seg[i+1:], "-]") {
			ranges = append(ranges, [2]uint16{uint16(c), uint16(c)}, [2]uint16{'-', '-'})
			i += 2
			continue
		}
		if strings.HasPrefix(seg[i+1:], "-") {
			rangeStart = int(c)
			i += 2
			continue
		}
		ranges = append(ranges, [2]uint16{uint16(c), uint16(c)})
		i++
	}
	if endPos < i {
		return fcGlobTok{}, 0
	}
	if len(ranges) == 0 {
		return fcGlobTok{kind: 'x'}, len(seg) - pos
	}
	if len(ranges) == 1 && ranges[0][0] == ranges[0][1] && !negate {
		return fcGlobTok{kind: 'l', unit: ranges[0][0]}, endPos - pos
	}
	return fcGlobTok{kind: '[', negate: negate, ranges: ranges}, endPos - pos
}

func fcParseGlobSeg(seg string) fcGlobSeg {
	if seg == "**" {
		return fcGlobSeg{globstar: true}
	}
	if seg == "" {
		return fcGlobSeg{literal: true}
	}
	if fast := fcGlobFast(seg); fast != nil {
		return fcGlobSeg{fast: fast}
	}
	var toks []fcGlobTok
	magic := false
	for i := 0; i < len(seg); {
		switch c := seg[i]; c {
		case '*':
			toks = append(toks, fcGlobTok{kind: '*'})
			magic = true
			i++
		case '?':
			toks = append(toks, fcGlobTok{kind: '?'})
			magic = true
			i++
		case '[':
			tok, n := fcGlobClass(seg, i)
			if n == 0 {
				toks = append(toks, fcGlobTok{kind: 'l', unit: '['})
				i++
				continue
			}
			toks = append(toks, tok)
			if tok.kind != 'l' {
				magic = true
			}
			i += n
		default:
			toks = append(toks, fcGlobTok{kind: 'l', unit: uint16(c)})
			i++
		}
	}
	if !magic {
		units := make([]uint16, len(toks))
		for i, t := range toks {
			units[i] = t.unit
		}
		return fcGlobSeg{literal: true, text: string(utf16.Decode(units))}
	}
	return fcGlobSeg{toks: toks}
}

func fcGlobFold(u uint16) uint16 {
	if u >= 'a' && u <= 'z' {
		return u - 32
	}
	return u
}

func fcGlobUnit(t fcGlobTok, u uint16, nocase bool) bool {
	switch t.kind {
	case '?':
		return true
	case 'l':
		return u == t.unit || nocase && fcGlobFold(u) == fcGlobFold(t.unit)
	case '[':
		in := false
		for _, r := range t.ranges {
			if u >= r[0] && u <= r[1] {
				in = true
				break
			}
			if nocase && u < 0x80 && (u|0x20) >= 'a' && (u|0x20) <= 'z' {
				for _, alt := range []uint16{u | 0x20, (u | 0x20) - 32} {
					if alt >= r[0] && alt <= r[1] {
						in = true
					}
				}
			}
		}
		return in != t.negate
	}
	return false
}

func fcGlobToks(toks []fcGlobTok, f []uint16, nocase bool) bool {
	if len(toks) == 0 {
		return len(f) == 0
	}
	t := toks[0]
	if t.kind == '*' {
		for k := 0; k <= len(f); k++ {
			if fcGlobToks(toks[1:], f[k:], nocase) {
				return true
			}
		}
		return false
	}
	if t.kind == 'x' || len(f) == 0 || !fcGlobUnit(t, f[0], nocase) {
		return false
	}
	return fcGlobToks(toks[1:], f[1:], nocase)
}

func (s fcGlobSeg) match(f string, nocase bool) bool {
	switch {
	case s.literal:
		return f == s.text
	case s.fast != nil:
		return s.fast(f, nocase)
	}
	first := s.toks[0].kind
	if (first == '*' || first == '?' || first == '[') && strings.HasPrefix(f, ".") {
		return false
	}
	// a leading literal dot (or two) before a magic token: never . or ..
	lead := 0
	for lead < len(s.toks) && lead < 2 && s.toks[lead].kind == 'l' && s.toks[lead].unit == '.' {
		lead++
	}
	if lead > 0 && lead < len(s.toks) && s.toks[lead].kind != 'l' && (f == "." || f == "..") {
		return false
	}
	return fcGlobToks(s.toks, utf16.Encode([]rune(f)), nocase)
}

func fcGlobDotSeg(f string) bool { return strings.HasPrefix(f, ".") }

func fcGlobSegs(pat []fcGlobSeg, file []string, nocase bool) bool {
	if len(pat) == 0 {
		return len(file) == 0
	}
	if pat[0].globstar {
		if len(pat) == 1 {
			if len(file) == 0 {
				return false
			}
			for _, f := range file {
				if fcGlobDotSeg(f) {
					return false
				}
			}
			return true
		}
		for k := 0; k <= len(file); k++ {
			if fcGlobSegs(pat[1:], file[k:], nocase) {
				return true
			}
			if k < len(file) && fcGlobDotSeg(file[k]) {
				return false
			}
		}
		return false
	}
	if len(file) == 0 || !pat[0].match(file[0], nocase) {
		return false
	}
	return fcGlobSegs(pat[1:], file[1:], nocase)
}

// fcMatchesGlob is path.matchesGlob(file, pattern) for a normalized pattern
// that fcGlobUnmodelled accepts.
func fcMatchesGlob(file, pattern string, nocase bool) bool {
	var segs []fcGlobSeg
	for _, part := range strings.Split(pattern, "/") {
		if part == "**" && len(segs) > 0 && segs[len(segs)-1].globstar {
			continue
		}
		segs = append(segs, fcParseGlobSeg(part))
	}
	return fcGlobSegs(segs, strings.Split(file, "/"), nocase)
}
