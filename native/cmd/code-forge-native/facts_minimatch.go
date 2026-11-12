package main

import (
	"strings"
	"unicode/utf16"
)

// B577: path.matchesGlob is Node 24's bundled minimatch with nocase on macOS
// and Windows, windowsPathsNoEscape, nonegate, nocomment, optimizationLevel 2
// and nocaseMagicOnly. fcMM ports it: backslashes become slashes, braceExpand,
// slashSplit, the two preprocess phases, parse() with its fast tests and the
// AST regexp source, and match() with levelTwoFileOptimize and the globstar
// walk. A segment regexp runs on UTF-16 units with the non-unicode /i
// Canonicalize of Node's ICU (facts_glob_case.go).
//
// Not modelled (fcMM.unmodelled, the run hands over first): a segment whose
// regexp needs the u flag (a POSIX class other than [:ascii:] and
// [:xdigit:]), an extglob opener !( ?( +( *( @( with a ) after it (an
// extglob that can close), and a nocase fast-test extension with a sigma
// (String#toLowerCase's Final_Sigma rule).

type fcMMTok struct {
	kind   byte // 'l' literal unit, 'c' class, '?' one unit, '*' units, '+' one or more, 'n' never ($.)
	unit   uint16
	negate bool
	ranges [][2]uint16
}

type fcMMRe struct {
	src  []uint16 // the RegExp source minimatch builds (between ^ and $)
	u    bool
	head byte // 0, 'd' (?!\.), 't' (?!(?:^|/)\.\.?(?:$|/))
	toks []fcMMTok
	fast func(f string) bool
}

type fcMMPart struct {
	kind byte // 's' string, 'g' GLOBSTAR, 'r' RegExp
	lit  []uint16
	re   *fcMMRe
}

type fcMM struct {
	nocase     bool
	unmodelled bool
	globParts  [][]string
	set        [][]fcMMPart
}

// fcSlashSplit is p.split(/\/+/).
func fcSlashSplit(p string) []string {
	var out []string
	start := 0
	for i := 0; i < len(p); {
		if p[i] != '/' {
			i++
			continue
		}
		out = append(out, p[start:i])
		for i < len(p) && p[i] == '/' {
			i++
		}
		start = i
	}
	return append(out, p[start:])
}

func fcIndexOfFrom(parts []string, s string, from int) int {
	if from < 0 {
		from = 0
	}
	for i := from; i < len(parts); i++ {
		if parts[i] == s {
			return i
		}
	}
	return -1
}

func fcSplice(parts []string, at, del int, ins ...string) []string {
	out := append([]string{}, parts[:at]...)
	out = append(out, ins...)
	return append(out, parts[at+del:]...)
}

// fcNewMM is new Minimatch(pattern, path.matchesGlob options).
func fcNewMM(pattern string, nocase bool) *fcMM {
	mm := &fcMM{nocase: nocase}
	pattern = strings.ReplaceAll(pattern, `\`, "/")
	if pattern == "" {
		mm.set = nil
		return mm
	}
	seen := map[string]bool{}
	var raw [][]string
	for _, s := range fcBraceExpand(pattern) {
		if !seen[s] {
			seen[s] = true
			raw = append(raw, fcSlashSplit(s))
		}
	}
	mm.globParts = fcMMSecondPhase(fcMMFirstPhase(raw))
	for _, parts := range mm.globParts {
		row := make([]fcMMPart, len(parts))
		for i, p := range parts {
			row[i] = mm.parse(p)
		}
		mm.set = append(mm.set, row)
	}
	return mm
}

func fcMMFirstPhase(globParts [][]string) [][]string {
	for didSomething := true; didSomething; {
		didSomething = false
		for gi := 0; gi < len(globParts); gi++ {
			parts := globParts[gi]
			for gs := fcIndexOfFrom(parts, "**", 0); gs != -1; gs = fcIndexOfFrom(parts, "**", gs+1) {
				gss := gs
				for gss+1 < len(parts) && parts[gss+1] == "**" {
					gss++
				}
				if gss > gs {
					parts = fcSplice(parts, gs+1, gss-gs)
				}
				at := func(i int) (string, bool) {
					if i < len(parts) {
						return parts[i], true
					}
					return "", false
				}
				next, _ := at(gs + 1)
				p, okP := at(gs + 2)
				p2, okP2 := at(gs + 3)
				if next != ".." || gs+1 >= len(parts) {
					continue
				}
				if !okP || p == "" || p == "." || p == ".." || !okP2 || p2 == "" || p2 == "." || p2 == ".." {
					continue
				}
				didSomething = true
				parts = fcSplice(parts, gs, 1)
				other := append([]string{}, parts...)
				other[gs] = "**"
				globParts[gi] = parts
				globParts = append(globParts, other)
				gs--
			}
			for i := 1; i < len(parts)-1; i++ {
				p := parts[i]
				if i == 1 && p == "" && parts[0] == "" {
					continue
				}
				if p == "." || p == "" {
					didSomething = true
					parts = fcSplice(parts, i, 1)
					i--
				}
			}
			if len(parts) == 2 && parts[0] == "." && (parts[1] == "." || parts[1] == "") {
				didSomething = true
				parts = parts[:1]
			}
			for dd := fcIndexOfFrom(parts, "..", 1); dd != -1; dd = fcIndexOfFrom(parts, "..", dd+1) {
				p := ""
				if dd-1 >= 0 {
					p = parts[dd-1]
				}
				if p != "" && p != "." && p != ".." && p != "**" {
					didSomething = true
					needDot := dd == 1 && dd+1 < len(parts) && parts[dd+1] == "**"
					var splin []string
					if needDot {
						splin = []string{"."}
					}
					parts = fcSplice(parts, dd-1, 2, splin...)
					if len(parts) == 0 {
						parts = []string{""}
					}
					dd -= 2
				}
			}
			globParts[gi] = parts
		}
	}
	return globParts
}

func fcMMPartsMatch(a, b []string) ([]string, bool) {
	ai, bi := 0, 0
	result := []string{}
	which := byte(0)
	for ai < len(a) && bi < len(b) {
		switch {
		case a[ai] == b[bi]:
			if which == 'b' {
				result = append(result, b[bi])
			} else {
				result = append(result, a[ai])
			}
			ai++
			bi++
		case a[ai] == "**" && ai+1 < len(a) && b[bi] == a[ai+1]:
			result = append(result, a[ai])
			ai++
		case b[bi] == "**" && bi+1 < len(b) && a[ai] == b[bi+1]:
			result = append(result, b[bi])
			bi++
		case a[ai] == "*" && b[bi] != "" && !strings.HasPrefix(b[bi], ".") && b[bi] != "**":
			if which == 'b' {
				return nil, false
			}
			which = 'a'
			result = append(result, a[ai])
			ai++
			bi++
		case b[bi] == "*" && a[ai] != "" && !strings.HasPrefix(a[ai], ".") && a[ai] != "**":
			if which == 'a' {
				return nil, false
			}
			which = 'b'
			result = append(result, b[bi])
			ai++
			bi++
		default:
			return nil, false
		}
	}
	return result, len(a) == len(b)
}

func fcMMSecondPhase(globParts [][]string) [][]string {
	for i := 0; i < len(globParts)-1; i++ {
		for j := i + 1; j < len(globParts); j++ {
			if matched, ok := fcMMPartsMatch(globParts[i], globParts[j]); ok {
				globParts[i] = []string{}
				globParts[j] = matched
				break
			}
		}
	}
	var out [][]string
	for _, gs := range globParts {
		if len(gs) > 0 {
			out = append(out, gs)
		}
	}
	return out
}

// fcJSRegExpSpecial: the chars regExpEscape escapes (/[-[\]{}()*+?.,\\^$|#\s]/).
func fcJSRegExpSpecial(u uint16) bool {
	return strings.ContainsRune(`-[]{}()*+?.,\^$|#`, rune(u)) || u < 0xd800 && jsSpace(rune(u)) || u >= 0xe000 && jsSpace(rune(u))
}

func fcMMEscape(dst []uint16, u uint16) []uint16 {
	if fcJSRegExpSpecial(u) {
		dst = append(dst, '\\')
	}
	return append(dst, u)
}

var fcMMPosix = []struct {
	name   string
	src    string
	u, neg bool
	ranges [][2]uint16
}{
	{"[:alnum:]", `\p{L}\p{Nl}\p{Nd}`, true, false, nil},
	{"[:alpha:]", `\p{L}\p{Nl}`, true, false, nil},
	{"[:ascii:]", `\x00-\x7f`, false, false, [][2]uint16{{0, 0x7f}}},
	{"[:blank:]", `\p{Zs}\t`, true, false, nil},
	{"[:cntrl:]", `\p{Cc}`, true, false, nil},
	{"[:digit:]", `\p{Nd}`, true, false, nil},
	{"[:graph:]", `\p{Z}\p{C}`, true, true, nil},
	{"[:lower:]", `\p{Ll}`, true, false, nil},
	{"[:print:]", `\p{C}`, true, false, nil},
	{"[:punct:]", `\p{P}`, true, false, nil},
	{"[:space:]", `\p{Z}\t\r\n\v\f`, true, false, nil},
	{"[:upper:]", `\p{Lu}`, true, false, nil},
	{"[:word:]", `\p{L}\p{Nl}\p{Nd}\p{Pc}`, true, false, nil},
	{"[:xdigit:]", `A-Fa-f0-9`, false, false, [][2]uint16{{'A', 'F'}, {'a', 'f'}, {'0', '9'}}},
}

func fcUnitsHasPrefix(s []uint16, at int, prefix string) bool {
	p := utf16.Encode([]rune(prefix))
	if at+len(p) > len(s) {
		return false
	}
	for i, u := range p {
		if s[at+i] != u {
			return false
		}
	}
	return true
}

func fcBraceEscapeUnit(dst []uint16, u uint16) []uint16 {
	if u == '[' || u == ']' || u == '\\' || u == '-' {
		dst = append(dst, '\\')
	}
	return append(dst, u)
}

type fcMMRange struct {
	src    []uint16
	single bool
	ranges [][2]uint16
}

// fcMMParseClass is parseClass(glob, pos): source, u flag, consumed, magic,
// and the token.
func fcMMParseClass(glob []uint16, pos int) ([]uint16, bool, int, bool, fcMMTok) {
	never := []uint16{'$', '.'}
	var ranges, negs []fcMMRange
	i, sawStart, uflag, escaping, negate, endPos := pos+1, false, false, false, false, pos
	rangeStart, haveRangeStart := uint16(0), false
outer:
	for i < len(glob) {
		c := glob[i]
		if (c == '!' || c == '^') && i == pos+1 {
			negate = true
			i++
			continue
		}
		if c == ']' && sawStart && !escaping {
			endPos = i + 1
			break
		}
		sawStart = true
		if c == '\\' && !escaping {
			escaping = true
			i++
			continue
		}
		if c == '[' && !escaping {
			for _, pc := range fcMMPosix {
				if fcUnitsHasPrefix(glob, i, pc.name) {
					if haveRangeStart {
						return never, false, len(glob) - pos, true, fcMMTok{kind: 'n'}
					}
					i += len(pc.name)
					r := fcMMRange{src: utf16.Encode([]rune(pc.src)), ranges: pc.ranges}
					if pc.neg {
						negs = append(negs, r)
					} else {
						ranges = append(ranges, r)
					}
					uflag = uflag || pc.u
					continue outer
				}
			}
		}
		escaping = false
		if haveRangeStart {
			if c > rangeStart {
				src := fcBraceEscapeUnit(nil, rangeStart)
				src = append(src, '-')
				ranges = append(ranges, fcMMRange{src: fcBraceEscapeUnit(src, c), ranges: [][2]uint16{{rangeStart, c}}})
			} else if c == rangeStart {
				ranges = append(ranges, fcMMRange{src: fcBraceEscapeUnit(nil, c), single: true, ranges: [][2]uint16{{c, c}}})
			}
			haveRangeStart = false
			i++
			continue
		}
		if fcUnitsHasPrefix(glob, i+1, "-]") {
			src := fcBraceEscapeUnit(fcBraceEscapeUnit(nil, c), '-')
			ranges = append(ranges, fcMMRange{src: src, ranges: [][2]uint16{{c, c}, {'-', '-'}}})
			i += 2
			continue
		}
		if fcUnitsHasPrefix(glob, i+1, "-") {
			rangeStart, haveRangeStart = c, true
			i += 2
			continue
		}
		ranges = append(ranges, fcMMRange{src: fcBraceEscapeUnit(nil, c), single: true, ranges: [][2]uint16{{c, c}}})
		i++
	}
	if endPos < i {
		return nil, false, 0, false, fcMMTok{}
	}
	if len(ranges) == 0 && len(negs) == 0 {
		return never, false, len(glob) - pos, true, fcMMTok{kind: 'n'}
	}
	if len(negs) == 0 && len(ranges) == 1 && ranges[0].single && !negate {
		u := ranges[0].ranges[0][0]
		return fcMMEscape(nil, u), false, endPos - pos, false, fcMMTok{kind: 'l', unit: u}
	}
	join := func(open []uint16, rs []fcMMRange) []uint16 {
		s := append([]uint16{}, open...)
		for _, r := range rs {
			s = append(s, r.src...)
		}
		return append(s, ']')
	}
	var sranges, snegs []uint16
	if negate {
		sranges, snegs = join([]uint16{'[', '^'}, ranges), join([]uint16{'['}, negs)
	} else {
		sranges, snegs = join([]uint16{'['}, ranges), join([]uint16{'[', '^'}, negs)
	}
	var src []uint16
	switch {
	case len(ranges) > 0 && len(negs) > 0:
		src = append(append(append(append([]uint16{'('}, sranges...), '|'), snegs...), ')')
	case len(ranges) > 0:
		src = sranges
	default:
		src = snegs
	}
	tok := fcMMTok{kind: 'c', negate: negate}
	for _, r := range ranges {
		tok.ranges = append(tok.ranges, r.ranges...)
	}
	// a u-flag class is never matched here (the pattern is unmodelled)
	return src, uflag, endPos - pos, true, tok
}

var (
	fcMMStar        = utf16.Encode([]rune("[^/]*?"))
	fcMMStarNoEmpty = utf16.Encode([]rune("[^/]+?"))
	fcMMQmark       = utf16.Encode([]rune("[^/]"))
)

// fcMMParseGlob is AST.#parseGlob: source, magic, u flag and tokens.
func fcMMParseGlob(glob []uint16, noEmpty bool) ([]uint16, bool, bool, []fcMMTok) {
	var re []uint16
	var toks []fcMMTok
	hasMagic, uflag, inStar := false, false, false
	allStars := len(glob) > 0
	for _, u := range glob {
		allStars = allStars && u == '*'
	}
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		if c == '*' {
			if inStar {
				continue
			}
			inStar = true
			if noEmpty && allStars {
				re = append(re, fcMMStarNoEmpty...)
				toks = append(toks, fcMMTok{kind: '+'})
			} else {
				re = append(re, fcMMStar...)
				toks = append(toks, fcMMTok{kind: '*'})
			}
			hasMagic = true
			continue
		}
		inStar = false
		if c == '[' {
			src, needU, consumed, magic, tok := fcMMParseClass(glob, i)
			if consumed > 0 {
				re = append(re, src...)
				toks = append(toks, tok)
				uflag = uflag || needU
				i += consumed - 1
				hasMagic = hasMagic || magic
				continue
			}
		}
		if c == '?' {
			re = append(re, fcMMQmark...)
			toks = append(toks, fcMMTok{kind: '?'})
			hasMagic = true
			continue
		}
		re = fcMMEscape(re, c)
		toks = append(toks, fcMMTok{kind: 'l', unit: c})
	}
	return re, hasMagic, uflag, toks
}

func fcMMExtType(u uint16) bool { return u == '!' || u == '?' || u == '+' || u == '*' || u == '@' }

// fcMMStart2 is toRegExpSource's start for a node whose first part is the
// string src came from.
func fcMMStart2(src []uint16, justDots bool) byte {
	if justDots {
		return 0
	}
	at := func(i int) uint16 {
		if i < len(src) {
			return src[i]
		}
		return 0
	}
	aps := func(u uint16) bool { return u == '[' || u == '.' }
	if fcUnitsHasPrefix(src, 0, `\.`) && aps(at(2)) || fcUnitsHasPrefix(src, 0, `\.\.`) && aps(at(4)) {
		return 't'
	}
	if aps(at(0)) {
		return 'd'
	}
	return 0
}

var (
	fcMMNoTrav = utf16.Encode([]rune(`(?!(?:^|/)\.\.?(?:$|/))`))
	fcMMNoDot  = utf16.Encode([]rune(`(?!\.)`))
)

// parse is Minimatch#parse for one pattern portion.
func (mm *fcMM) parse(seg string) fcMMPart {
	switch seg {
	case "**":
		return fcMMPart{kind: 'g'}
	case "":
		return fcMMPart{kind: 's'}
	}
	glob := utf16.Encode([]rune(seg))
	// AST.fromGlob: the root splits at the first extglob opener outside a
	// [...]; with no ) after it the extglob never closes and the rest is a
	// plain child node.
	split := -1
	inBrace, braceStart, braceNeg := false, -1, false
	for i2 := 0; i2 < len(glob); {
		c := glob[i2]
		i2++
		if inBrace {
			if i2 == braceStart+1 {
				if c == '^' || c == '!' {
					braceNeg = true
				}
			} else if c == ']' && !(i2 == braceStart+2 && braceNeg) {
				inBrace = false
			}
			continue
		} else if c == '[' {
			inBrace, braceStart, braceNeg = true, i2, false
			continue
		}
		if fcMMExtType(c) && i2 < len(glob) && glob[i2] == '(' {
			split = i2 - 1
			break
		}
	}
	var src []uint16
	var toks []fcMMTok
	var head byte
	magic, uflag := false, false
	if split < 0 {
		src, magic, uflag, toks = fcMMParseGlob(glob, true)
		head = fcMMStart2(src, seg == "." || seg == "..")
	} else {
		for _, u := range glob[split:] {
			if u == ')' {
				mm.unmodelled = true
			}
		}
		prefix, child := glob[:split], glob[split:]
		psrc, pmagic, pu, ptoks := fcMMParseGlob(prefix, false)
		csrc, cmagic, cu, ctoks := fcMMParseGlob(child, split == 0)
		if split == 0 {
			head = fcMMStart2(csrc, false)
		}
		src = append(append([]uint16{}, psrc...), csrc...)
		toks = append(ptoks, ctoks...)
		magic, uflag = pmagic || cmagic, pu || cu
		if split > 0 {
			head = fcMMStart2(src, false)
		}
	}
	if uflag {
		mm.unmodelled = true
	}
	if !magic {
		lit := make([]uint16, len(toks))
		for i, t := range toks {
			lit[i] = t.unit
		}
		return fcMMPart{kind: 's', lit: lit}
	}
	switch head {
	case 't':
		src = append(append([]uint16{}, fcMMNoTrav...), src...)
	case 'd':
		src = append(append([]uint16{}, fcMMNoDot...), src...)
	}
	return fcMMPart{kind: 'r', re: &fcMMRe{src: src, u: uflag, head: head, toks: toks, fast: mm.fast(seg)}}
}

// fast is parse()'s fast test for whole-portion shapes (nil: none).
func (mm *fcMM) fast(seg string) func(f string) bool {
	stars := len(seg) - len(strings.TrimLeft(seg, "*"))
	q := len(seg) - len(strings.TrimLeft(seg, "?"))
	noDot := func(f string) bool { return !strings.HasPrefix(f, ".") }
	endsWith := func(ext string) func(f string) bool {
		if !mm.nocase {
			return func(f string) bool { return strings.HasSuffix(f, ext) }
		}
		ext = fcJSLowerString(ext)
		if strings.ContainsAny(ext, "σς") {
			mm.unmodelled = true
		}
		return func(f string) bool { return strings.HasSuffix(fcJSLowerString(f), ext) }
	}
	switch {
	case stars == len(seg): // /^\*+$/
		return func(f string) bool { return f != "" && noDot(f) }
	case stars > 0 && !strings.ContainsAny(seg[stars:], "+@!?*[("): // /^\*+([^+@!?*[(]*)$/
		ends := endsWith(seg[stars:])
		return func(f string) bool { return noDot(f) && ends(f) }
	case q > 0 && !strings.ContainsAny(seg[q:], "+@!?*[("): // /^\?+([^+@!?*[(]*)?$/
		n, ext := fcUTF16Len(seg), seg[q:]
		noext := func(f string) bool { return fcUTF16Len(f) == n && noDot(f) }
		if ext == "" {
			return noext
		}
		ends := endsWith(ext)
		return func(f string) bool { return noext(f) && ends(f) }
	case stars > 0 && strings.HasPrefix(seg[stars:], ".") && len(seg) > stars+1 && strings.Trim(seg[stars+1:], "*") == "": // /^\*+\.\*+$/
		return func(f string) bool { return noDot(f) && strings.Contains(f, ".") }
	case len(seg) > 1 && seg[0] == '.' && strings.Trim(seg[1:], "*") == "": // /^\.\*+$/
		return func(f string) bool { return f != "." && f != ".." && strings.HasPrefix(f, ".") }
	}
	return nil
}

// fcJSLowerString is String#toLowerCase without the Final_Sigma context
// (callers keep sigma out).
func fcJSLowerString(s string) string {
	var b strings.Builder
	for _, r := range s {
		if m, ok := fcJSLowerMulti[r]; ok {
			b.WriteString(m)
		} else if l, ok := fcJSLowerMap[r]; ok {
			b.WriteRune(l)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

var fcJSLowerMap, fcJSCanonMap, fcJSCanonPre = func() (map[rune]rune, map[uint16]uint16, map[uint16][]uint16) {
	lower := make(map[rune]rune, len(fcJSLowerPairs)/2)
	for i := 0; i < len(fcJSLowerPairs); i += 2 {
		lower[fcJSLowerPairs[i]] = fcJSLowerPairs[i+1]
	}
	canon := make(map[uint16]uint16, len(fcJSCanonPairs)/2)
	pre := map[uint16][]uint16{}
	for i := 0; i < len(fcJSCanonPairs); i += 2 {
		canon[fcJSCanonPairs[i]] = fcJSCanonPairs[i+1]
		pre[fcJSCanonPairs[i+1]] = append(pre[fcJSCanonPairs[i+1]], fcJSCanonPairs[i])
	}
	return lower, canon, pre
}()

// fcJSCanon is the non-unicode RegExp Canonicalize of one UTF-16 unit.
func fcJSCanon(u uint16) uint16 {
	if c, ok := fcJSCanonMap[u]; ok {
		return c
	}
	return u
}

func fcMMInRanges(ranges [][2]uint16, u uint16) bool {
	for _, r := range ranges {
		if u >= r[0] && u <= r[1] {
			return true
		}
	}
	return false
}

func (t fcMMTok) matches(u uint16, nocase bool) bool {
	switch t.kind {
	case '?', '*', '+':
		return u != '/'
	case 'l':
		return u == t.unit || nocase && fcJSCanon(u) == fcJSCanon(t.unit)
	case 'c':
		in := fcMMInRanges(t.ranges, u)
		if !in && nocase {
			cu := fcJSCanon(u)
			in = fcJSCanon(cu) == cu && fcMMInRanges(t.ranges, cu)
			for _, m := range fcJSCanonPre[cu] {
				in = in || fcMMInRanges(t.ranges, m)
			}
		}
		return in != t.negate
	}
	return false
}

// test is RegExp#test (or the fast test that replaces it) on one file portion.
func (re *fcMMRe) test(f string, nocase bool) bool {
	if re.fast != nil {
		return re.fast(f)
	}
	units := utf16.Encode([]rune(f))
	switch re.head {
	case 'd':
		if len(units) > 0 && units[0] == '.' {
			return false
		}
	case 't':
		if f == "." || f == ".." {
			return false
		}
	}
	n := len(units)
	cur := make([]bool, n+1)
	cur[0] = true
	for _, t := range re.toks {
		next := make([]bool, n+1)
		switch t.kind {
		case 'n':
			return false
		case '*', '+':
			reach := false
			for j := 0; j <= n; j++ {
				if t.kind == '+' {
					// one or more: reach j from a start k < j
					if j > 0 && reach && t.matches(units[j-1], nocase) {
						next[j] = true
					} else if j > 0 && !t.matches(units[j-1], nocase) {
						reach = false
					}
					if cur[j] {
						reach = true
					}
					continue
				}
				if j > 0 && !t.matches(units[j-1], nocase) {
					reach = false
				}
				reach = reach || cur[j]
				next[j] = reach
			}
		default:
			for j := 0; j < n; j++ {
				if cur[j] && t.matches(units[j], nocase) {
					next[j+1] = true
				}
			}
		}
		cur = next
	}
	return cur[n]
}

// levelTwoFileOptimize for a file split into portions.
func fcMMFileOptimize(parts []string) []string {
	for didSomething := true; didSomething; {
		didSomething = false
		for i := 1; i < len(parts)-1; i++ {
			p := parts[i]
			if i == 1 && p == "" && parts[0] == "" {
				continue
			}
			if p == "." || p == "" {
				didSomething = true
				parts = fcSplice(parts, i, 1)
				i--
			}
		}
		if len(parts) == 2 && parts[0] == "." && (parts[1] == "." || parts[1] == "") {
			didSomething = true
			parts = parts[:1]
		}
		for dd := fcIndexOfFrom(parts, "..", 1); dd != -1; dd = fcIndexOfFrom(parts, "..", dd+1) {
			p := ""
			if dd-1 >= 0 {
				p = parts[dd-1]
			}
			if p != "" && p != "." && p != ".." && p != "**" {
				didSomething = true
				parts = fcSplice(parts, dd-1, 2)
				dd -= 2
			}
		}
	}
	if len(parts) == 0 {
		return []string{""}
	}
	return parts
}

func (mm *fcMM) partHit(p fcMMPart, f string) bool {
	switch p.kind {
	case 's':
		return fcUnitsEqual(utf16.Encode([]rune(f)), p.lit)
	case 'r':
		return p.re.test(f, mm.nocase)
	}
	return false
}

func fcUnitsEqual(a, b []uint16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// matchOneAt is Minimatch#matchOne's inner loop.
func (mm *fcMM) matchOneAt(file []string, pattern []fcMMPart, fi, pi int) bool {
	fl, pl := len(file), len(pattern)
	for ; fi < fl && pi < pl; fi, pi = fi+1, pi+1 {
		p := pattern[pi]
		if p.kind == 'g' || !mm.partHit(p, file[fi]) {
			return false
		}
	}
	switch {
	case fi == fl && pi == pl:
		return true
	case fi == fl:
		return false
	}
	return fi == fl-1 && file[fi] == ""
}

func fcMMDotPortion(f string) bool { return f == "." || f == ".." || strings.HasPrefix(f, ".") }

func (mm *fcMM) matchGlobstar(file []string, pattern []fcMMPart) bool {
	firstgs, lastgs := -1, -1
	for i, p := range pattern {
		if p.kind == 'g' {
			if firstgs < 0 {
				firstgs = i
			}
			lastgs = i
		}
	}
	head, tail := pattern[:firstgs], pattern[lastgs+1:]
	var body []fcMMPart
	if lastgs > firstgs {
		body = pattern[firstgs+1 : lastgs]
	}
	fileIndex := 0
	if len(head) > 0 {
		end := len(head)
		if end > len(file) {
			end = len(file)
		}
		if !mm.matchOneAt(file[:end], head, 0, 0) {
			return false
		}
		fileIndex += len(head)
	}
	fileTailMatch := 0
	if len(tail) > 0 {
		if len(tail)+fileIndex > len(file) {
			return false
		}
		tailStart := len(file) - len(tail)
		if mm.matchOneAt(file, tail, tailStart, 0) {
			fileTailMatch = len(tail)
		} else {
			if file[len(file)-1] != "" || fileIndex+len(tail) == len(file) {
				return false
			}
			tailStart--
			if !mm.matchOneAt(file, tail, tailStart, 0) {
				return false
			}
			fileTailMatch = len(tail) + 1
		}
	}
	if len(body) == 0 {
		sawSome := fileTailMatch > 0
		for i := fileIndex; i < len(file)-fileTailMatch; i++ {
			sawSome = true
			if fcMMDotPortion(file[i]) {
				return false
			}
		}
		return sawSome
	}
	type section struct {
		body  []fcMMPart
		after int
	}
	sections := []section{{}}
	nonGs := 0
	sums := []int{0}
	for _, b := range body {
		if b.kind == 'g' {
			sums = append(sums, nonGs)
			sections = append(sections, section{})
		} else {
			sections[len(sections)-1].body = append(sections[len(sections)-1].body, b)
			nonGs++
		}
	}
	fileLength := len(file) - fileTailMatch
	for i, k := len(sections)-1, 0; k < len(sections); i, k = i-1, k+1 {
		sections[k].after = fileLength - (sums[i] + len(sections[k].body))
	}
	var walk func(fileIndex, bodyIndex, depth int, sawTail bool) int // 1 true, 0 false, -1 null
	walk = func(fileIndex, bodyIndex, depth int, sawTail bool) int {
		if bodyIndex >= len(sections) {
			for i := fileIndex; i < len(file); i++ {
				sawTail = true
				if fcMMDotPortion(file[i]) {
					return 0
				}
			}
			if sawTail {
				return 1
			}
			return 0
		}
		bs := sections[bodyIndex]
		for fileIndex <= bs.after {
			end := fileIndex + len(bs.body)
			if end > len(file) {
				end = len(file)
			}
			if mm.matchOneAt(file[:end], bs.body, fileIndex, 0) && depth < 200 {
				if sub := walk(fileIndex+len(bs.body), bodyIndex+1, depth+1, sawTail); sub != 0 {
					return sub
				}
			}
			if fileIndex >= len(file) || fcMMDotPortion(file[fileIndex]) {
				return 0
			}
			fileIndex++
		}
		return -1
	}
	return walk(fileIndex, 0, 0, fileTailMatch > 0) == 1
}

// match is Minimatch#match(f).
func (mm *fcMM) match(f string) bool {
	if len(mm.set) == 0 && mm.globParts == nil {
		return f == ""
	}
	file := fcMMFileOptimize(fcSlashSplit(f))
	for _, pattern := range mm.set {
		hasGS := false
		for _, p := range pattern {
			hasGS = hasGS || p.kind == 'g'
		}
		if hasGS && mm.matchGlobstar(file, pattern) || !hasGS && mm.matchOneAt(file, pattern, 0, 0) {
			return true
		}
	}
	return false
}
