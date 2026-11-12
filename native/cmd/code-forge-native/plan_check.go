package main

// plan_check.go is `plan check` past its usage and reads (B475): the project
// config, the ledger lanes and checkPlan (src/session/plan-check.mjs with
// plan-sections.mjs and facts.mjs's claim extractor), with Node's output.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/blockopen"
	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
)

// jsWS is JavaScript's \s (WhiteSpace and LineTerminator) as a class body.
const jsWS = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

// jsDot is JavaScript's `.`: anything but a line terminator.
const jsDot = `[^\n\r\x{2028}\x{2029}]`

func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

func isJSLineEnd(r rune) bool { return r == '\n' || r == '\r' || r == 0x2028 || r == 0x2029 }

func isWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// prevRune is the character before byte offset i (utf8.RuneError at 0).
func prevRune(s string, i int) rune {
	if i <= 0 {
		return utf8.RuneError
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return r
}

func isWordRune(r rune) bool { return r < 0x80 && isWordByte(byte(r)) }

// jsLower is toLowerCase: Go's simple mapping plus the one unconditional
// full mapping (İ → i̇).
func jsLower(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "İ", "i̇"))
}

// jsUpperSpecial holds the toUpperCase expansions that yield ASCII letters
// (SpecialCasing.txt); Go's simple mapping keeps those runes as they are.
var jsUpperSpecial = map[rune]string{
	'ß': "SS", 'ŉ': "ʼN", 'ǰ': "J̌", 'ẖ': "H̱", 'ẗ': "T̈", 'ẘ': "W̊", 'ẙ': "Y̊",
	'ẚ': "Aʾ", 'ﬀ': "FF", 'ﬁ': "FI", 'ﬂ': "FL", 'ﬃ': "FFI", 'ﬄ': "FFL", 'ﬅ': "ST", 'ﬆ': "ST",
}

func jsUpper(s string) string {
	var b strings.Builder
	for _, r := range s {
		if x, ok := jsUpperSpecial[r]; ok {
			b.WriteString(x)
		} else {
			b.WriteString(strings.ToUpper(string(r)))
		}
	}
	return b.String()
}

// planPlain is plain(): `**` removed, then trimmed.
func planPlain(s string) string { return jsTrim(strings.ReplaceAll(s, "**", "")) }

// jsNumber is a number as JavaScript prints it.
func jsNumber(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	b, _ := ledger.Stringify(f, false)
	return string(b)
}

// jsBasename is path.basename: trailing slashes dropped, "" for the root.
func jsBasename(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// idIndex is idRe(id).search(s) in bytes: id as a whole word (not inside
// B1.1 or B10), or -1.
func idIndex(s, id string) int {
	if id == "" {
		return -1
	}
	for at := 0; at+len(id) <= len(s); {
		k := strings.Index(s[at:], id)
		if k < 0 {
			return -1
		}
		i := at + k
		p := prevRune(s, i)
		end := i + len(id)
		after := end < len(s) && (isWordByte(s[end]) || s[end] == '.' && end+1 < len(s) && isWordByte(s[end+1]))
		if !(i > 0 && (isWordRune(p) || p == '.')) && !after {
			return i
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		at = i + size
	}
	return -1
}

// planCells is cells(): the cells of a table row; `\|` stays in its cell.
func planCells(line string) []string {
	t := jsTrim(line)
	var parts []string
	start := 0
	for i := 0; i < len(t); i++ {
		if t[i] == '|' && !(i > 0 && t[i-1] == '\\') {
			parts = append(parts, t[start:i])
			start = i + 1
		}
	}
	parts = append(parts, t[start:])
	if len(parts) < 2 {
		return []string{}
	}
	out := make([]string, 0, len(parts)-2)
	for _, c := range parts[1 : len(parts)-1] {
		out = append(out, jsTrim(c))
	}
	return out
}

var codeSpanRe = regexp.MustCompile("`([^`]+)`")

func isNoneWord(s string) bool {
	l := jsLower(s)
	return l == "—" || l == "-" || l == "none"
}

// listCell: code spans, else comma-separated words; —/-/none give nothing.
func listCell(cell string) []string {
	spans := []string{}
	for _, m := range codeSpanRe.FindAllStringSubmatch(cell, -1) {
		spans = append(spans, jsTrim(m[1]))
	}
	if len(spans) > 0 {
		return spans
	}
	out := []string{}
	for _, s := range strings.Split(planPlain(cell), ",") {
		s = jsTrim(s)
		if s != "" && !isNoneWord(s) {
			out = append(out, s)
		}
	}
	return out
}

// dropParens is replace(/\(.*?\)/g, ”): `.` never crosses a line end.
func dropParens(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '(' {
			j := i + 1
			for j < len(s) {
				r, size := utf8.DecodeRuneInString(s[j:])
				if r == ')' || isJSLineEnd(r) {
					break
				}
				j += size
			}
			if j < len(s) && s[j] == ')' {
				i = j + 1
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func forecastNumber(raw string, present bool) (float64, bool) {
	if !present {
		return 0, false
	}
	text := strings.Map(func(r rune) rune {
		if isJSSpace(r) {
			return -1
		}
		return r
	}, dropParens(planPlain(raw)))
	if text == "" {
		return 0, false
	}
	for i := 0; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			return 0, false
		}
	}
	f, _ := strconv.ParseFloat(text, 64)
	return f, true
}

type planBlock struct {
	id, wave, level, acceptance string
	dependsOn, owned, imports   []string
	cases, lines                float64
	hasCases, hasLines          bool
}

type planSection struct {
	title string
	level int
	body  string
}

type planDispatch struct{ wave, text string }

var (
	waveRe   = regexp.MustCompile(`\b(?i:wave)[` + jsWS + `]+([0-9A-Za-z_.]+)`)
	levelRe  = regexp.MustCompile(`\bL[0-3]\b`)
	casesRe  = regexp.MustCompile(`\bcases\b`)
	depSplit = regexp.MustCompile(`[,` + jsWS + `]+`)
)

// planHeading is /^(#{1,6})\s+(.*)$/ on one line.
func planHeading(line string) (int, string, bool) {
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	if n < 1 || n > 6 {
		return 0, "", false
	}
	rest := line[n:]
	r, size := utf8.DecodeRuneInString(rest)
	if !isJSSpace(r) {
		return 0, "", false
	}
	for rest != "" {
		r, size = utf8.DecodeRuneInString(rest)
		if !isJSSpace(r) {
			break
		}
		rest = rest[size:]
	}
	if strings.ContainsFunc(rest, isJSLineEnd) {
		return 0, "", false
	}
	return n, rest, true
}

// asciiFoldIndex finds word (lower-case ASCII) in s ignoring ASCII case only,
// as a non-unicode /i RegExp does, from byte offset from.
func asciiFoldIndex(s, word string, from int) int {
	for i := from; i+len(word) <= len(s); i++ {
		ok := true
		for k := 0; k < len(word); k++ {
			c := s[i+k]
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			if c != word[k] {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

// dispatchAfter is the dispatch-order test and the text after its colon, or
// false when the line has no `dispatch order …:`.
func dispatchAfter(s string) (string, bool) {
	const key = "dispatch order"
	found := false
	for i := asciiFoldIndex(s, key, 0); i >= 0; i = asciiFoldIndex(s, key, i+1) {
		colon := strings.IndexByte(s[i+len(key):], ':')
		if colon < 0 {
			continue
		}
		found = true
		if strings.ContainsFunc(s[:i], isJSLineEnd) {
			continue
		}
		return s[i+len(key)+colon+1:], true
	}
	return s, found
}

func isSeparatorRow(line string) bool {
	t := jsTrim(line)
	if len(t) < 3 || t[0] != '|' || t[len(t)-1] != '|' {
		return false
	}
	for _, r := range t[1 : len(t)-1] {
		if r != '|' && r != ':' && r != '-' && !isJSSpace(r) {
			return false
		}
	}
	return true
}

func parsePlan(text string) ([]planBlock, []planSection, []planDispatch) {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	type head struct {
		title        string
		level, start int
	}
	var blocks []planBlock
	var heads []head
	var dispatch []planDispatch
	wave := "default"
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if n, rest, ok := planHeading(line); ok {
			heads = append(heads, head{planPlain(rest), n, i + 1})
			if m := waveRe.FindStringSubmatch(rest); m != nil {
				wave = m[1]
			}
			continue
		}
		if after, ok := dispatchAfter(planPlain(line)); ok {
			dispatch = append(dispatch, planDispatch{wave, after})
			continue
		}
		if !strings.HasPrefix(jsTrim(line), "|") || i+1 >= len(lines) || !isSeparatorRow(lines[i+1]) {
			continue
		}
		header := planCells(line)
		for k, h := range header {
			header[k] = jsLower(planPlain(h))
		}
		col := func(name string) int {
			for k, h := range header {
				if h == name || strings.HasPrefix(h, name+" ") || strings.HasPrefix(h, name+"(") {
					return k
				}
			}
			return -1
		}
		find := func(pred func(string) bool) int {
			for k, h := range header {
				if pred(h) {
					return k
				}
			}
			return -1
		}
		idCol := col("id")
		ownedCol := find(func(h string) bool { return strings.HasPrefix(h, "owned_files") })
		accCol := find(func(h string) bool { return strings.HasPrefix(h, "acceptance") })
		if idCol < 0 || ownedCol < 0 || accCol < 0 {
			continue
		}
		forecastCol := find(func(h string) bool { return casesRe.MatchString(h) && strings.Contains(h, "→") })
		var parts []string
		if forecastCol >= 0 {
			for _, p := range strings.Split(header[forecastCol], "→") {
				parts = append(parts, jsTrim(p))
			}
		}
		j := i + 2
		for ; j < len(lines) && strings.HasPrefix(jsTrim(lines[j]), "|"); j++ {
			row := planCells(lines[j])
			at := func(c int) string {
				if c >= 0 && c < len(row) {
					return row[c]
				}
				return ""
			}
			id := planPlain(at(idCol))
			if id == "" {
				continue
			}
			var forecast []string
			if forecastCol >= 0 {
				forecast = strings.Split(at(forecastCol), "→")
			}
			pick := func(name string) (float64, bool) {
				if own := col(name); own >= 0 && own != forecastCol {
					return forecastNumber(at(own), true)
				}
				for k, p := range parts {
					if p == name {
						if k < len(forecast) {
							return forecastNumber(forecast[k], true)
						}
						return 0, false
					}
				}
				return 0, false
			}
			b := planBlock{id: id, wave: wave, acceptance: at(accCol)}
			b.level = levelRe.FindString(planPlain(at(col("level"))))
			b.dependsOn = []string{}
			for _, s := range depSplit.Split(planPlain(at(col("depends_on"))), -1) {
				if s != "" && !isNoneWord(s) {
					b.dependsOn = append(b.dependsOn, s)
				}
			}
			b.owned = listCell(at(ownedCol))
			b.imports = listCell(at(col("imports")))
			b.cases, b.hasCases = pick("cases")
			b.lines, b.hasLines = pick("lines")
			blocks = append(blocks, b)
		}
		i = j - 1
	}
	sections := make([]planSection, 0, len(heads))
	for k, h := range heads {
		end := len(lines)
		for _, n := range heads[k+1:] {
			if n.level <= h.level {
				end = n.start - 1
				break
			}
		}
		body := ""
		if h.start < end {
			body = strings.Join(lines[h.start:end], "\n")
		}
		sections = append(sections, planSection{h.title, h.level, body})
	}
	return blocks, sections, dispatch
}

// requiredSection is plan-sections.mjs's REQUIRED_SECTIONS entry.
type requiredSection struct {
	id, heading, label string
	position           *regexp.Regexp
	keywords           []string
}

var requiredSections = []requiredSection{
	{"unbackable", "Acceptance clauses the facts sheet cannot back", "§0.x", regexp.MustCompile(`^0\.\w+$`), []string{"unbackable", "cannot back"}},
	{"callerMap", "Caller map", "§3", regexp.MustCompile(`^3$`), []string{"caller map", "callers"}},
}

var splitHeadingRe = regexp.MustCompile(`^§?[` + jsWS + `]*(\d+(?:\.\w+)*)\.?[` + jsWS + `]+(` + jsDot + `*)$`)

func splitHeading(title string) (string, bool, string) {
	t := jsTrim(title)
	if m := splitHeadingRe.FindStringSubmatch(t); m != nil {
		return m[1], true, jsTrim(m[2])
	}
	return "", false, t
}

func findSection(sections []planSection, spec requiredSection) (*planSection, bool) {
	textOf := func(s planSection) string { _, _, t := splitHeading(s.title); return jsLower(t) }
	want := jsLower(spec.heading)
	for i := range sections {
		if textOf(sections[i]) == want {
			return &sections[i], true
		}
	}
	for i := range sections {
		number, has, _ := splitHeading(sections[i].title)
		text := textOf(sections[i])
		if !has || !spec.position.MatchString(number) {
			continue
		}
		other := false
		for _, o := range requiredSections {
			if o.id != spec.id && jsLower(o.heading) == text {
				other = true
			}
		}
		if other {
			continue
		}
		for _, k := range spec.keywords {
			if strings.Contains(text, k) {
				return &sections[i], false
			}
		}
	}
	return nil, false
}

var (
	listItemRe   = regexp.MustCompile(`^[` + jsWS + `]*(?:[-*]|\d+\.)[` + jsWS + `]+`)
	toleranceRe  = regexp.MustCompile(`\b[tT][oO][lL][eE][rR][aA][nN][cC][eE]\b`)
	dashCellRe   = regexp.MustCompile(`^:?-+:?$`)
	emptyTolCell = map[string]bool{"": true, "—": true, "–": true, "-": true, "--": true, "…": true, "...": true, "n/a": true}
)

func toleranceLines(body string) []string {
	out := []string{}
	raws := strings.Split(body, "\n")
	for _, l := range raws {
		if listItemRe.MatchString(l) && toleranceRe.MatchString(jsTrim(l)) {
			out = append(out, jsTrim(l))
		}
	}
	inTable, col := false, -1
	for _, raw := range raws {
		line := jsTrim(raw)
		if !strings.HasPrefix(line, "|") {
			inTable = false
			continue
		}
		row := planCells(line)
		if !inTable {
			inTable, col = true, -1
			for k, c := range row {
				if toleranceRe.MatchString(planPlain(c)) {
					col = k
					break
				}
			}
			continue
		}
		every := true
		for _, c := range row {
			if !dashCellRe.MatchString(c) {
				every = false
			}
		}
		if col < 0 || every {
			continue
		}
		cell := ""
		if col < len(row) {
			cell = row[col]
		}
		if !emptyTolCell[jsLower(planPlain(cell))] {
			out = append(out, line)
		}
	}
	return out
}

// splitClauses splits on `;` and on ` · (n)` clause markers.
func splitClauses(acceptance string) []string {
	var parts []string
	start := 0
	s := acceptance
	for i := 0; i < len(s); {
		if s[i] == ';' {
			parts = append(parts, s[start:i])
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if isJSSpace(r) && strings.HasPrefix(s[i+size:], "·") {
			j := i + size + len("·")
			k := j
			for k < len(s) {
				r2, sz := utf8.DecodeRuneInString(s[k:])
				if !isJSSpace(r2) {
					break
				}
				k += sz
			}
			if k > j && k < len(s) && s[k] == '(' {
				d := k + 1
				for d < len(s) && s[d] >= '0' && s[d] <= '9' {
					d++
				}
				if d > k+1 && d < len(s) && s[d] == ')' {
					parts = append(parts, s[start:i])
					i = k
					start = k
					continue
				}
			}
		}
		i += size
	}
	parts = append(parts, s[start:])
	out := []string{}
	for _, p := range parts {
		if p = planPlain(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

type planClaim struct{ token, kind string }

// jsScan is matchAll with a lookbehind: re is anchored (^...), tried at each
// position where before allows it; after a hit the scan resumes at its end.
func jsScan(s string, re *regexp.Regexp, before func(s string, i int) bool) [][2]int {
	var hits [][2]int
	for i := 0; i < len(s); {
		if before(s, i) {
			if loc := re.FindStringIndex(s[i:]); loc != nil && loc[1] > 0 {
				hits = append(hits, [2]int{i, i + loc[1]})
				i += loc[1]
				continue
			}
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return hits
}

func prevNotIn(set string) func(string, int) bool {
	return func(s string, i int) bool {
		p := prevRune(s, i)
		return i == 0 || !(isWordRune(p) || strings.ContainsRune(set, p))
	}
}

var (
	endpointRe   = regexp.MustCompile("https?://[^" + jsWS + "`'\"<>)\\]|]+")
	packageRe    = regexp.MustCompile(`^@[a-z0-9][a-z0-9._-]*/[a-z0-9][a-z0-9._-]*`)
	pathRe       = regexp.MustCompile("^(?:~|\\.{1,2}|[\\w.-]+)?/[^" + jsWS + "`'\"<>)\\]|,;]*")
	flagRe       = regexp.MustCompile(`^--[a-z][a-z0-9-]*`)
	versionRe    = regexp.MustCompile(`^\d+\.\d+(?:\.\d+)*`)
	envRe        = regexp.MustCompile(`^\$?[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+`)
	commandRe    = regexp.MustCompile(`^([a-z][a-z0-9-]*) ([a-z][a-z0-9-]*)`)
	trimTailRe   = regexp.MustCompile(`[.,:;]+$`)
	bareRootRe   = regexp.MustCompile(`^(~|\.{1,2})?/$`)
	rootedRe     = regexp.MustCompile(`^(~|\.{1,2})?/`)
	extensionRe  = regexp.MustCompile(`\.[a-zA-Z0-9][\w-]*$`)
	testCitation = regexp.MustCompile("\\((?:[" + jsWS + "]*`(?:\\./)?(?:test|tests|spec)/[^`\\n]+\\.[\\w]+`[" + jsWS + ",]*)+\\)[" + jsWS + "]*$")
)

// extractClaims is facts.mjs's extractClaims with no options.
func extractClaims(text string) []planClaim {
	masked := []byte(text)
	type hit struct {
		index       int
		token, kind string
	}
	var found []hit
	mask := func(start, length int) {
		for k := start; k < start+length; k++ {
			masked[k] = ' '
		}
	}
	take := func(kind string, hits [][2]int) {
		cur := string(masked)
		for _, h := range hits {
			raw := cur[h[0]:h[1]]
			token := raw
			if kind == "env" {
				token = strings.TrimPrefix(token, "$")
			}
			token = trimTailRe.ReplaceAllString(token, "")
			if token == "" || kind == "path" && bareRootRe.MatchString(token) {
				continue
			}
			if kind == "path" && !rootedRe.MatchString(token) && !extensionRe.MatchString(token) {
				continue
			}
			found = append(found, hit{h[0], token, kind})
			mask(h[0], h[1]-h[0])
		}
	}
	var endpoints [][2]int
	for _, loc := range endpointRe.FindAllStringIndex(string(masked), -1) {
		endpoints = append(endpoints, [2]int{loc[0], loc[1]})
	}
	take("endpoint", endpoints)
	take("package", jsScan(string(masked), packageRe, prevNotIn("./@-")))
	take("path", jsScan(string(masked), pathRe, prevNotIn("/.~-")))
	cur := string(masked)
	for _, m := range codeSpanRe.FindAllStringSubmatchIndex(cur, -1) {
		span := cur[m[2]:m[3]]
		cmd := commandRe.FindStringSubmatch(span)
		if cmd == nil {
			continue
		}
		if end := len(cmd[0]); end < len(span) {
			c := span[end]
			if isWordByte(c) || c == '.' || c == '/' || c == '-' {
				continue
			}
		}
		found = append(found, hit{m[2], cmd[1] + " " + cmd[2], "command"})
		mask(m[2], len(cmd[0]))
	}
	take("flag", jsScan(string(masked), flagRe, prevNotIn("-")))
	take("version", jsScan(string(masked), versionRe, func(s string, i int) bool {
		p := prevRune(s, i)
		if i > 0 && (isWordRune(p) || p == '.' || p == '§') {
			return false
		}
		return !(p == ' ' && i >= 1 && prevRune(s, i-1) == '§')
	}))
	var envs [][2]int
	for _, h := range jsScanEnv(string(masked)) {
		envs = append(envs, h)
	}
	take("env", envs)
	sort.SliceStable(found, func(i, j int) bool { return found[i].index < found[j].index })
	seen := map[string]bool{}
	claims := []planClaim{}
	for _, f := range found {
		key := f.kind + "\x00" + f.token
		if seen[key] {
			continue
		}
		seen[key] = true
		claims = append(claims, planClaim{f.token, f.kind})
	}
	return claims
}

// jsScanEnv is the env pattern with its (?<![\w]) and (?![\w]) guards.
func jsScanEnv(s string) [][2]int {
	var hits [][2]int
	for i := 0; i < len(s); {
		if i == 0 || !isWordRune(prevRune(s, i)) {
			if loc := envRe.FindStringIndex(s[i:]); loc != nil {
				end := i + loc[1]
				if end >= len(s) || !isWordByte(s[end]) {
					hits = append(hits, [2]int{i, end})
					i = end
					continue
				}
			}
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return hits
}

// factsSheet is parseSheet's result.
type factsSheet struct {
	briefRef, briefSha string
	facts              []any
}

// lineStarts are the offsets where a multiline ^ matches.
func lineStarts(text string) []int {
	starts := []int{0}
	for i, r := range text {
		if isJSLineEnd(r) {
			starts = append(starts, i+utf8.RuneLen(r))
		}
	}
	return starts
}

// atLineEnd is a multiline $ at offset i.
func atLineEnd(text string, i int) bool {
	if i >= len(text) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(text[i:])
	return isJSLineEnd(r)
}

// sheetField is /^- <name>: `(<value>)`$/m with value made of allowed runes.
func sheetField(text, prefix string, allowed func(rune) bool, minLen, maxLen int) (string, bool) {
	for _, p := range lineStarts(text) {
		if !strings.HasPrefix(text[p:], prefix) {
			continue
		}
		start := p + len(prefix)
		end := strings.IndexByte(text[start:], '`')
		if end < 0 {
			continue
		}
		value := text[start : start+end]
		n := utf8.RuneCountInString(value)
		if n < minLen || maxLen > 0 && n > maxLen || !atLineEnd(text, start+end+1) {
			continue
		}
		ok := true
		for _, r := range value {
			if !allowed(r) {
				ok = false
			}
		}
		if ok {
			return value, true
		}
	}
	return "", false
}

// parseSheet returns ok=false when no sheet is there, and crash=true where
// Node would throw reading it.
func parseSheet(text string) (factsSheet, bool) {
	hexRune := func(r rune) bool { return r >= '0' && r <= '9' || r >= 'a' && r <= 'f' }
	sha, ok1 := sheetField(text, "- brief_sha256: `", hexRune, 64, 64)
	ref, ok2 := sheetField(text, "- brief: `", func(r rune) bool { return r != '`' }, 1, 0)
	if !ok1 || !ok2 {
		return factsSheet{}, false
	}
	const open, close = "<!-- facts-json\n", "\n-->"
	for _, p := range lineStarts(text) {
		if !strings.HasPrefix(text[p:], open) {
			continue
		}
		start := p + len(open)
		for k := start; k <= len(text)-len(close); k++ {
			if strings.HasPrefix(text[k:], close) && atLineEnd(text, k+len(close)) {
				var facts any
				if err := json.Unmarshal([]byte(text[start:k]), &facts); err != nil {
					return factsSheet{}, false
				}
				list, isList := facts.([]any)
				if !isList {
					return factsSheet{}, false
				}
				return factsSheet{ref, sha, list}, true
			}
		}
	}
	return factsSheet{}, false
}

// errPlanNodeThrows marks a case where Node's check throws (a stack trace).
var errPlanNodeThrows = errors.New("node throws")

// readProjectBins is facts.mjs's readProjectBins; message is a FactsError.
func readProjectBins(root string) ([]string, string, error) {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, "", nil
		}
		for _, code := range []struct {
			err  error
			name string
		}{{syscall.EISDIR, "EISDIR"}, {syscall.EACCES, "EACCES"}, {syscall.EPERM, "EPERM"}, {syscall.ENOTDIR, "ENOTDIR"}, {syscall.ELOOP, "ELOOP"}, {syscall.ENAMETOOLONG, "ENAMETOOLONG"}} {
			if errors.Is(err, code.err) {
				return nil, "package.json at the project root cannot be read (" + code.name + ")", nil
			}
		}
		return nil, "", errPlanNodeThrows
	}
	var pkg any
	if err := json.Unmarshal([]byte(nodeUTF8(string(data))), &pkg); err != nil {
		return nil, "package.json at the project root is not valid JSON", nil
	}
	obj, _ := pkg.(map[string]any)
	if bin, ok := obj["bin"].(string); ok {
		_ = bin
		if name, ok := obj["name"].(string); ok {
			return []string{regexp.MustCompile(`^@[^/]+/`).ReplaceAllString(name, "")}, "", nil
		}
	}
	if bin, ok := obj["bin"].(map[string]any); ok {
		out := []string{}
		for k := range bin {
			out = append(out, k)
		}
		return out, "", nil
	}
	return []string{}, "", nil
}

type planLane struct{ lane, source string }

func normaliseLane(answer string) string {
	if token := levelRe.FindString(jsUpper(jsTrim(answer))); token != "" {
		return token
	}
	return jsTrim(answer)
}

func recordedLanes(rows []ledger.Row, planName string) map[string]planLane {
	out := map[string]planLane{}
	for _, value := range rows {
		row, ok := value.(map[string]any)
		if !ok || row["event"] != "decision" || row["question"] != "lane" {
			continue
		}
		block, ok := row["block"].(string)
		source, _ := row["source"].(string)
		answer, ok2 := row["answer"].(string)
		if !ok || (source != "jev" && source != "rules") || !ok2 {
			continue
		}
		if plan, present := row["plan"]; present {
			name, isString := plan.(string)
			if !isString || jsBasename(name) != jsBasename(planName) {
				continue
			}
		}
		out[block] = planLane{normaliseLane(answer), source}
	}
	return out
}

type planLimits struct{ coders, blockCases, blockLines float64 }

// planCheckLimits reads caps.coders and budget.block_* (defaults 2, 80,
// 2000); ok=false for a value that is set but not a number, or null.
func planCheckLimits(cfg map[string]any) (planLimits, bool) {
	limits := planLimits{2, 80, 2000}
	get := func(section, key string, dest *float64) bool {
		m, isMap := cfg[section].(map[string]any)
		if !isMap {
			return true
		}
		v, present := m[key]
		if !present {
			return true
		}
		if v == nil {
			// the config loader turns .inf/.nan into null like a JSON copy,
			// so a null here may be a number Node compares with
			return false
		}
		f, isNum := v.(float64)
		if !isNum {
			return false
		}
		*dest = f
		return true
	}
	ok := get("caps", "coders", &limits.coders)
	ok = get("budget", "block_cases", &limits.blockCases) && ok
	ok = get("budget", "block_lines", &limits.blockLines) && ok
	return limits, ok
}

type planCheckInput struct {
	text, planPath, factsPath, factsText string
	hasFacts                             bool
	limits                               planLimits
	decisions                            []ledger.Row
	root                                 string
}

type planCheckResult struct {
	ok              bool
	errors          []string
	warnings        []string
	blocks          int
	lanes           []string
	factsError      string
	hasFactsMessage bool
}

// globChars is GLOB_CHARS in registry.mjs.
func globChars(p string) bool { return strings.ContainsAny(p, "*?") }

func planExpandBraces(pattern string) []string {
	open := strings.IndexByte(pattern, '{')
	if open < 0 {
		return []string{pattern}
	}
	depth, close := 0, -1
	bounds := []int{open}
	for i := open; i < len(pattern) && close < 0; i++ {
		switch pattern[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				close = i
			}
		case ',':
			if depth == 1 {
				bounds = append(bounds, i)
			}
		}
	}
	if close < 0 {
		return []string{pattern}
	}
	bounds = append(bounds, close)
	out := []string{}
	for i := 0; i < len(bounds)-1; i++ {
		out = append(out, planExpandBraces(pattern[:open]+pattern[bounds[i]+1:bounds[i+1]]+pattern[close+1:])...)
	}
	return out
}

// segmentMatch is segmentRegExp(pattern).test(name): * and ? per UTF-16 unit.
func segmentMatch(name, pattern string) bool {
	n, p := utf16.Encode([]rune(name)), utf16.Encode([]rune(pattern))
	var match func(i, j int) bool
	match = func(i, j int) bool {
		if j == len(p) {
			return i == len(n)
		}
		if p[j] == '*' {
			for k := i; k <= len(n); k++ {
				if match(k, j+1) {
					return true
				}
			}
			return false
		}
		return i < len(n) && (p[j] == '?' || p[j] == n[i]) && match(i+1, j+1)
	}
	return match(0, 0)
}

func planGlobMatch(file, pattern string) bool {
	f, p := strings.Split(file, "/"), strings.Split(pattern, "/")
	var match func(i, j int) bool
	match = func(i, j int) bool {
		if j == len(p) {
			return i == len(f)
		}
		if p[j] == "**" {
			for k := i; k <= len(f); k++ {
				if match(k, j+1) {
					return true
				}
			}
			return false
		}
		return i < len(f) && segmentMatch(f[i], p[j]) && match(i+1, j+1)
	}
	return match(0, 0)
}

func planGlobBelow(dir, pattern string) bool {
	f, p := strings.Split(dir, "/"), strings.Split(pattern, "/")
	for i := 0; ; i++ {
		if i < len(p) && p[i] == "**" {
			return true
		}
		if i == len(f) {
			return i < len(p)
		}
		if i == len(p) || !segmentMatch(f[i], p[i]) {
			return false
		}
	}
}

type planAlt struct {
	p    string
	tree bool
}

func planAlternatives(entry string, kinds ledger.Object) []planAlt {
	if !owned.IsExactEntry(entry) {
		out := []planAlt{}
		for _, p := range planExpandBraces(entry) {
			out = append(out, planAlt{p, false})
		}
		return out
	}
	p := strings.TrimRight(entry, "/")
	if p == "" {
		return nil
	}
	return []planAlt{{p, kinds.Get(entry) != "file"}}
}

func coversPath(root, file string) bool { return file == root || strings.HasPrefix(file, root+"/") }

func staticSegments(p string) []string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if globChars(s) {
			return segs[:i]
		}
	}
	return segs
}

func segmentPrefix(a, b []string) bool {
	if len(a) > len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func patternsOverlap(a, b planAlt) bool {
	if b.tree && !a.tree {
		return patternsOverlap(b, a)
	}
	if a.tree {
		if b.tree {
			return coversPath(a.p, b.p) || coversPath(b.p, a.p)
		}
		if !globChars(b.p) {
			return coversPath(a.p, b.p)
		}
		return planGlobMatch(a.p, b.p) || planGlobBelow(a.p, b.p)
	}
	ag, bg := globChars(a.p), globChars(b.p)
	switch {
	case !ag && !bg:
		return a.p == b.p
	case !ag:
		return planGlobMatch(a.p, b.p)
	case !bg:
		return planGlobMatch(b.p, a.p)
	}
	sa, sb := staticSegments(a.p), staticSegments(b.p)
	return segmentPrefix(sa, sb) || segmentPrefix(sb, sa)
}

func planFindOverlap(a, b []string, kindsA, kindsB ledger.Object) (string, string, bool) {
	for _, x := range a {
		for _, y := range b {
			for _, ax := range planAlternatives(x, kindsA) {
				for _, by := range planAlternatives(y, kindsB) {
					if patternsOverlap(ax, by) {
						return x, y, true
					}
				}
			}
		}
	}
	return "", "", false
}

func planOwnsFile(entries []string, file string, kinds ledger.Object) bool {
	list := make([]any, len(entries))
	for i, e := range entries {
		list[i] = e
	}
	return owned.OwnsFile(list, kinds, file)
}

// checkPlan is plan-check.mjs's checkPlan; err is errPlanNodeThrows where
// Node throws.
func checkPlan(in planCheckInput) (planCheckResult, error) {
	limits := in.limits
	blocks, sections, dispatch := parsePlan(in.text)
	if len(blocks) == 0 {
		return planCheckResult{errors: []string{"no block table found (a table with id, owned_files and acceptance columns)"}, warnings: []string{}}, nil
	}
	errs := []string{}
	sheetText := in.text
	if in.hasFacts {
		sheetText = in.factsText
	}
	sheet, hasSheet := parseSheet(sheetText)
	if !hasSheet {
		errs = append(errs, "facts sheet missing: run forge facts and paste the sheet into the plan §0")
	} else {
		if in.hasFacts && !strings.Contains(in.text, "brief_sha256: `"+sheet.briefSha+"`") {
			errs = append(errs, "the plan §0 is not the facts sheet: its brief_sha256 is absent from the plan")
		}
		base := in.planPath
		if in.hasFacts {
			base = in.factsPath
		}
		brief := sheet.briefRef
		if !filepath.IsAbs(brief) {
			brief = filepath.Join(filepath.Dir(base), brief)
		} else {
			brief = filepath.Clean(brief)
		}
		if _, err := os.Stat(brief); err != nil {
			if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
				return planCheckResult{}, errPlanNodeThrows
			}
			errs = append(errs, "facts sheet brief not found: "+sheet.briefRef)
		} else {
			data, err := os.ReadFile(brief)
			if err != nil {
				return planCheckResult{}, errPlanNodeThrows
			}
			sum := sha256.Sum256(data)
			if hex.EncodeToString(sum[:]) != sheet.briefSha {
				errs = append(errs, "facts sheet is stale: brief changed after it was built")
			}
		}
	}
	verified := map[string]bool{}
	for _, f := range sheet.facts {
		if f == nil {
			return planCheckResult{}, errPlanNodeThrows
		}
		if obj, ok := f.(map[string]any); ok && obj["tag"] == "VERIFIED" {
			if claim, ok := obj["claim"].(string); ok {
				verified[claim] = true
			}
		}
	}
	warnings := []string{}
	required := func(spec requiredSection) *planSection {
		hit, exact := findSection(sections, spec)
		if hit != nil && !exact {
			warnings = append(warnings, `WARN section "`+hit.title+`" at `+spec.label+` read as "`+spec.heading+`" — use the exact heading "`+spec.heading+`"`)
		}
		return hit
	}
	unbackable := required(requiredSections[0])
	if unbackable == nil {
		errs = append(errs, `unbackable-clauses section missing (write "none" when there is none)`)
	}
	tolerances := []string{}
	if unbackable != nil {
		tolerances = toleranceLines(unbackable.body)
	}
	var bins []string
	binsRead := false
	projectBins := func() ([]string, string, error) {
		if binsRead {
			return bins, "", nil
		}
		got, message, err := readProjectBins(in.root)
		if err != nil || message != "" {
			return nil, message, err
		}
		bins, binsRead = got, true
		return bins, "", nil
	}
	for _, b := range blocks {
		for _, clause := range splitClauses(b.acceptance) {
			factual := clause
			if loc := testCitation.FindStringIndex(clause); loc != nil {
				factual = clause[:loc[0]]
			}
			backed := true
			for _, c := range extractClaims(factual) {
				ok := verified[c.token]
				if !ok && c.kind == "command" {
					list, message, err := projectBins()
					if err != nil {
						return planCheckResult{}, err
					}
					if message != "" {
						return planCheckResult{factsError: message, hasFactsMessage: true}, nil
					}
					for _, bin := range list {
						if verified[bin+" "+c.token] {
							ok = true
							break
						}
					}
				}
				if ok {
					continue
				}
				tolerated := false
				for _, t := range tolerances {
					if strings.Contains(t, c.token) && idIndex(t, b.id) >= 0 {
						tolerated = true
						break
					}
				}
				if !tolerated {
					backed = false
				}
			}
			if !backed {
				errs = append(errs, "unbackable clause without tolerance: "+b.id+" "+clause)
			}
		}
	}
	callerMap := required(requiredSections[1])
	if callerMap == nil {
		errs = append(errs, "caller map missing")
	} else {
		for _, b := range blocks {
			if idIndex(callerMap.body, b.id) < 0 {
				errs = append(errs, "caller map: block "+b.id+" is absent")
			}
		}
	}
	lanes := recordedLanes(in.decisions, jsBasename(in.planPath))
	for _, b := range blocks {
		recorded, has := lanes[b.id]
		switch {
		case b.level == "":
			errs = append(errs, "block "+b.id+": no lane (level L0–L3)")
		case !has:
			errs = append(errs, "block "+b.id+": level "+b.level+" has no recorded lane decision — run forge jev ask lane --block "+b.id+" --state <file> (or --rules when Jev is unavailable)")
		case recorded.lane != b.level:
			errs = append(errs, "block "+b.id+": level "+b.level+" differs from the recorded lane "+recorded.lane+" ("+recorded.source+")")
		}
		if err := blockopen.AssertOwned(b.owned, "owned files", "bad-owned"); err != nil {
			errs = append(errs, "block "+b.id+": "+err.Error())
		}
		if !b.hasCases {
			errs = append(errs, "block "+b.id+": no cases forecast")
		} else if b.cases > limits.blockCases {
			errs = append(errs, "block "+b.id+": cases forecast "+jsNumber(b.cases)+" exceeds budget.block_cases "+jsNumber(limits.blockCases)+" — split it")
		}
		if !b.hasLines {
			errs = append(errs, "block "+b.id+": no lines forecast")
		} else if b.lines > limits.blockLines {
			errs = append(errs, "block "+b.id+": lines forecast "+jsNumber(b.lines)+" exceeds budget.block_lines "+jsNumber(limits.blockLines)+" — split it")
		}
	}
	kindsOf := map[string]ledger.Object{}
	for _, b := range blocks {
		kinds := ledger.Object{}
		if blockopen.AssertOwned(b.owned, "owned files", "bad-owned") == nil {
			entries := make([]any, len(b.owned))
			for i, e := range b.owned {
				entries[i] = e
			}
			kinds = owned.Kinds(in.root, entries)
		}
		kindsOf[b.id] = kinds
	}
	for i := 0; i < len(blocks); i++ {
		for j := i + 1; j < len(blocks); j++ {
			a, b := blocks[i], blocks[j]
			if a.wave != b.wave {
				continue
			}
			if x, y, hit := planFindOverlap(a.owned, b.owned, kindsOf[a.id], kindsOf[b.id]); hit {
				errs = append(errs, "blocks "+a.id+" and "+b.id+" (wave "+a.wave+") overlap: "+x+" / "+y)
			}
		}
	}
	errs = append(errs, dependencyCycles(blocks)...)
	for _, b := range blocks {
		deps := closure(blocks, b.id)
		for _, imported := range b.imports {
			for _, o := range blocks {
				if o.id != b.id && planOwnsFile(o.owned, imported, kindsOf[o.id]) {
					if !deps[o.id] {
						errs = append(errs, "block "+b.id+" imports "+imported+" owned by "+o.id+", which is not in its depends_on")
					}
					break
				}
			}
		}
	}
	for _, d := range dispatch {
		for k, step := range strings.Split(d.text, "→") {
			var running []string
			for _, seg := range strings.Split(step, "∥") {
				best, bestAt := "", -1
				for _, b := range blocks {
					if at := idIndex(seg, b.id); at >= 0 && (bestAt < 0 || at < bestAt) {
						best, bestAt = b.id, at
					}
				}
				if bestAt >= 0 {
					running = append(running, best)
				}
			}
			if float64(len(running)) > limits.coders {
				errs = append(errs, fmt.Sprintf("dispatch step %d (wave %s) runs %d blocks concurrently (caps.coders %s): %s", k+1, d.wave, len(running), jsNumber(limits.coders), strings.Join(running, " ∥ ")))
			}
		}
	}
	lanesOut := []string{}
	for _, b := range blocks {
		if l, has := lanes[b.id]; has {
			lanesOut = append(lanesOut, b.id+" "+l.lane+" ("+l.source+")")
		} else {
			lanesOut = append(lanesOut, b.id+" none")
		}
	}
	return planCheckResult{ok: len(errs) == 0, errors: errs, warnings: warnings, blocks: len(blocks), lanes: lanesOut}, nil
}

func dependencyCycles(blocks []planBlock) []string {
	byID := map[string]planBlock{}
	for _, b := range blocks {
		if _, dup := byID[b.id]; !dup {
			byID[b.id] = b
		}
	}
	// Map in Node keeps the LAST block for a duplicated id.
	for _, b := range blocks {
		byID[b.id] = b
	}
	state := map[string]string{}
	out := []string{}
	var visit func(id string, trail []string)
	visit = func(id string, trail []string) {
		if state[id] == "done" {
			return
		}
		if state[id] == "open" {
			at := 0
			for i, t := range trail {
				if t == id {
					at = i
					break
				}
			}
			cycle := append(append([]string{}, trail[at:]...), id)
			out = append(out, "depends_on cycle: "+strings.Join(cycle, " → "))
			return
		}
		state[id] = "open"
		for _, dep := range byID[id].dependsOn {
			if _, ok := byID[dep]; ok {
				visit(dep, append(append([]string{}, trail...), id))
			}
		}
		state[id] = "done"
	}
	for _, b := range blocks {
		visit(b.id, nil)
	}
	return out
}

func closure(blocks []planBlock, id string) map[string]bool {
	byID := map[string]planBlock{}
	for _, b := range blocks {
		byID[b.id] = b
	}
	seen := map[string]bool{}
	stack := append([]string{}, byID[id].dependsOn...)
	for len(stack) > 0 {
		dep := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[dep] {
			continue
		}
		seen[dep] = true
		stack = append(stack, byID[dep].dependsOn...)
	}
	return seen
}

var planSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// planSlugFor is slugFor in src/config/load.mjs.
func planSlugFor(cfg map[string]any, root string) string {
	if project, ok := cfg["project"].(map[string]any); ok {
		if slug, ok := project["slug"].(string); ok && slug != "" {
			return slug
		}
	}
	derived := jsLower(filepath.Base(config.ProjectRootFor(root)))
	derived = regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(derived, "-")
	derived = regexp.MustCompile(`^-+|-+$`).ReplaceAllString(derived, "")
	if derived == "" {
		return "project"
	}
	return derived
}

var configReadErrno = regexp.MustCompile(`: E[A-Z]+$`)

// preparePlanCheck runs the check after the plan and facts reads.
func preparePlanCheck(cwd, planPath, factsPath string, hasFacts bool, text, factsText string, flags map[string]string) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
	refuse := func(message string) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
		return func(_, stderr io.Writer) int {
			fmt.Fprint(stderr, "plan: "+message+"\n")
			return 2
		}, 0, true
	}
	root := config.ProjectRootFor(cwd)
	cfg, _, err := config.LoadOrdered(root)
	if err != nil {
		if yamlSyntaxError(err) {
			return nil, hConfigParseError, false
		}
		failure, known := err.(*config.Error)
		if !known || failure.Kind == "read-error" && !configReadErrno.MatchString(failure.Message) {
			return nil, hPlanCheckNodeThrows, false
		}
		if failure.Kind != "not-found" {
			return refuse(failure.Message)
		}
		cfg = nil
	}
	slug, given := flags["slug"]
	if !given {
		slug = planSlugFor(cfg, root)
	}
	if !planSlugPattern.MatchString(slug) {
		return refuse("--slug must be lowercase letters, digits and hyphens")
	}
	limits, ok := planCheckLimits(cfg)
	if !ok {
		return nil, hPlanCheckLimits, false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, hPlanCheckNodeThrows, false
	}
	rows, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge", "ledger"), slug)
	if err != nil {
		return nil, hPlanCheckLedgerRead, false
	}
	result, err := checkPlan(planCheckInput{text: text, planPath: planPath, factsPath: factsPath, factsText: factsText,
		hasFacts: hasFacts, limits: limits, decisions: rows, root: root})
	if err != nil {
		return nil, hPlanCheckNodeThrows, false
	}
	if result.hasFactsMessage {
		return refuse(result.factsError)
	}
	return func(stdout, stderr io.Writer) int {
		var e strings.Builder
		for _, line := range result.warnings {
			e.WriteString(line + "\n")
		}
		for _, line := range result.errors {
			e.WriteString(line + "\n")
		}
		fmt.Fprint(stderr, e.String())
		var o strings.Builder
		if result.ok {
			fmt.Fprintf(&o, "plan check: ok (%d blocks)\n", result.blocks)
		}
		if len(result.lanes) > 0 {
			o.WriteString("lanes: " + strings.Join(result.lanes, " · ") + "\n")
		}
		fmt.Fprint(stdout, o.String())
		if result.ok {
			return 0
		}
		return 1
	}, 0, true
}
