package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B556: steps 1 and 3 of src/session/facts.mjs (extractClaims, the read-only
// check rules, validateFacts and renderSheet), byte-equal to Node. The JS
// regular expressions are hand-written scanners: \w is ASCII, \s is jsSpace.

type fcRoot struct {
	dir, label, prefix, file string
}

type fcClaim struct {
	token, kind  string
	source       string
	hasSource    bool
	checkPath    string
	hasCheckPath bool
	root         *fcRoot
	unavailable  string
}

type fcFact struct {
	factID, claim, kind string
	source              string
	hasSource           bool
	command, excerpt    string
	tag                 string
	why                 *string
}

type fcDowngrade struct{ factID, reason string }

func fcWord(r rune) bool {
	return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}
func fcDigit(r rune) bool          { return r >= '0' && r <= '9' }
func fcLower(r rune) bool          { return r >= 'a' && r <= 'z' }
func fcUpper(r rune) bool          { return r >= 'A' && r <= 'Z' }
func fcIn(r rune, set string) bool { return strings.ContainsRune(set, r) }

func fcAt(s []rune, i int) rune {
	if i < 0 || i >= len(s) {
		return -1
	}
	return s[i]
}

// fcTrimTail is trimTail: the token without a run of [.,:;] at its end.
func fcTrimTail(token string) string { return strings.TrimRight(token, ".,:;") }

type fcMatch struct{ start, end int }

// fcScanEndpoint is /https?:\/\/[^\s`'"<>)\]|]+/g.
func fcScanEndpoint(s []rune) []fcMatch {
	out := []fcMatch{}
	for i := 0; i < len(s); {
		p := i
		if strings.HasPrefix(string(s[i:min(i+8, len(s))]), "https://") {
			p = i + 8
		} else if strings.HasPrefix(string(s[i:min(i+7, len(s))]), "http://") {
			p = i + 7
		} else {
			i++
			continue
		}
		e := p
		for e < len(s) && !jsSpace(s[e]) && !fcIn(s[e], "`'\"<>)]|") {
			e++
		}
		if e == p {
			i++
			continue
		}
		out = append(out, fcMatch{i, e})
		i = e
	}
	return out
}

// fcScanPackage is /(?<![\w./@-])@[a-z0-9][a-z0-9._-]*\/[a-z0-9][a-z0-9._-]*/g.
func fcScanPackage(s []rune) []fcMatch {
	out := []fcMatch{}
	part := func(r rune) bool { return fcLower(r) || fcDigit(r) || r == '.' || r == '_' || r == '-' }
	head := func(r rune) bool { return fcLower(r) || fcDigit(r) }
	for i := 0; i < len(s); i++ {
		if s[i] != '@' || (i > 0 && (fcWord(s[i-1]) || fcIn(s[i-1], "./@-"))) {
			continue
		}
		if !head(fcAt(s, i+1)) {
			continue
		}
		j := i + 2
		for j < len(s) && part(s[j]) {
			j++
		}
		if fcAt(s, j) != '/' || !head(fcAt(s, j+1)) {
			continue
		}
		e := j + 2
		for e < len(s) && part(s[e]) {
			e++
		}
		out = append(out, fcMatch{i, e})
		i = e - 1
	}
	return out
}

// fcScanPath is the globPaths path pattern
// /(?<![\w/.~-])(?:~|\.{1,2}|[\w.-]+)?\/[^\s`'"<>)|;]*/g.
func fcScanPath(s []rune) []fcMatch {
	out := []fcMatch{}
	run := func(r rune) bool { return fcWord(r) || r == '.' || r == '-' }
	for i := 0; i < len(s); i++ {
		if i > 0 && (fcWord(s[i-1]) || fcIn(s[i-1], "/.~-")) {
			continue
		}
		slash := -1
		switch {
		case s[i] == '~' && fcAt(s, i+1) == '/':
			slash = i + 1
		case run(s[i]):
			j := i
			for j < len(s) && run(s[j]) {
				j++
			}
			if fcAt(s, j) == '/' {
				slash = j
			}
		case s[i] == '/':
			slash = i
		}
		if slash < 0 {
			continue
		}
		e := slash + 1
		for e < len(s) && !jsSpace(s[e]) && !fcIn(s[e], "`'\"<>)|;") {
			e++
		}
		out = append(out, fcMatch{i, e})
		i = e - 1
	}
	return out
}

// fcScanFlag is /(?<![\w-])--[a-z][a-z0-9-]*/g.
func fcScanFlag(s []rune) []fcMatch {
	out := []fcMatch{}
	for i := 0; i+2 < len(s); i++ {
		if s[i] != '-' || s[i+1] != '-' || !fcLower(s[i+2]) || (i > 0 && (fcWord(s[i-1]) || s[i-1] == '-')) {
			continue
		}
		e := i + 3
		for e < len(s) && (fcLower(s[e]) || fcDigit(s[e]) || s[e] == '-') {
			e++
		}
		out = append(out, fcMatch{i, e})
		i = e - 1
	}
	return out
}

// fcScanVersion is /(?<![\w.§])(?<!§ )\d+\.\d+(?:\.\d+)*/g.
func fcScanVersion(s []rune) []fcMatch {
	out := []fcMatch{}
	for i := 0; i < len(s); i++ {
		if !fcDigit(s[i]) {
			continue
		}
		if p := fcAt(s, i-1); p != -1 && (fcWord(p) || p == '.' || p == '§') {
			continue
		}
		if fcAt(s, i-2) == '§' && fcAt(s, i-1) == ' ' {
			continue
		}
		j := i
		for j < len(s) && fcDigit(s[j]) {
			j++
		}
		if fcAt(s, j) != '.' || !fcDigit(fcAt(s, j+1)) {
			continue
		}
		e := j + 1
		for e < len(s) && fcDigit(s[e]) {
			e++
		}
		for fcAt(s, e) == '.' && fcDigit(fcAt(s, e+1)) {
			e++
			for e < len(s) && fcDigit(s[e]) {
				e++
			}
		}
		out = append(out, fcMatch{i, e})
		i = e - 1
	}
	return out
}

// fcEnvWord is ^[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+$ over one whole \w run.
func fcEnvWord(w []rune) bool {
	if len(w) == 0 || !fcUpper(w[0]) {
		return false
	}
	parts := strings.Split(string(w), "_")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, r := range part {
			if !fcUpper(r) && !fcDigit(r) {
				return false
			}
		}
	}
	return true
}

// fcScanEnv is /(?<![\w])\$?[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+(?![\w])/g: a whole
// \w run, with the "$" before it when that "$" follows no word character.
func fcScanEnv(s []rune) []fcMatch {
	out := []fcMatch{}
	for i := 0; i < len(s); i++ {
		if !fcWord(s[i]) || (i > 0 && fcWord(s[i-1])) {
			continue
		}
		e := i
		for e < len(s) && fcWord(s[e]) {
			e++
		}
		if fcEnvWord(s[i:e]) {
			start := i
			if fcAt(s, i-1) == '$' && !fcWord(fcAt(s, i-2)) {
				start = i - 1
			}
			out = append(out, fcMatch{start, e})
		}
		i = e - 1
	}
	return out
}

// fcSpans is /`([^`\n]+)`/g: the content bounds of each span.
func fcSpans(s []rune) []fcMatch {
	out := []fcMatch{}
	for i := 0; i < len(s); i++ {
		if s[i] != '`' {
			continue
		}
		j := i + 1
		for j < len(s) && s[j] != '`' && s[j] != '\n' {
			j++
		}
		if j > i+1 && j < len(s) && s[j] == '`' {
			out = append(out, fcMatch{i + 1, j})
			i = j
		}
	}
	return out
}

func fcTrim(s string) string { return strings.TrimFunc(s, jsSpace) }

// fcIdentifierSymbol is IDENTIFIER_SYMBOL.
func fcIdentifierSymbol(text string) bool {
	s := []rune(text)
	if len(s) == 0 {
		return false
	}
	if fcLower(s[0]) {
		parts := strings.Split(text, "_")
		if len(parts) < 2 {
			return false
		}
		for _, part := range parts {
			if part == "" {
				return false
			}
			for _, r := range part {
				if !fcLower(r) && !fcDigit(r) {
					return false
				}
			}
		}
		return true
	}
	var units func(pos, count int) bool
	units = func(pos, count int) bool {
		if pos == len(s) {
			return count >= 2
		}
		if !fcUpper(s[pos]) {
			return false
		}
		k := pos + 1
		for k < len(s) && (fcLower(s[k]) || fcDigit(s[k])) {
			k++
		}
		for end := k; end > pos+1; end-- {
			if units(end, count+1) {
				return true
			}
		}
		k = pos
		for k < len(s) && fcUpper(s[k]) {
			k++
		}
		for end := k; end > pos; end-- {
			if (end == len(s) || (fcUpper(s[end]) && fcLower(fcAt(s, end+1)))) && units(end, count+1) {
				return true
			}
		}
		return false
	}
	return units(0, 0)
}

var fcFileExts = []string{"php", "mjs", "cjs", "js", "ts", "tsx", "jsx", "go", "py", "rb", "swift", "kt", "java", "rs", "cs", "vue", "svelte", "sql"}

// fcFileSymbol is FILE_SYMBOL.
func fcFileSymbol(s string) bool {
	for _, r := range s {
		if !fcWord(r) && r != '.' && r != '-' {
			return false
		}
	}
	for _, ext := range fcFileExts {
		if strings.HasSuffix(s, "."+ext) && len(s) > len(ext)+1 {
			return true
		}
	}
	return false
}

// fcCommandInSpan is COMMAND_IN_SPAN: the two words and the matched length.
func fcCommandInSpan(span []rune) (string, string, int, bool) {
	word := func(r rune) bool { return fcLower(r) || fcDigit(r) || r == '-' }
	if len(span) == 0 || !fcLower(span[0]) {
		return "", "", 0, false
	}
	i := 1
	for i < len(span) && word(span[i]) {
		i++
	}
	if fcAt(span, i) != ' ' || !fcLower(fcAt(span, i+1)) {
		return "", "", 0, false
	}
	j := i + 2
	for j < len(span) && word(span[j]) {
		j++
	}
	if next := fcAt(span, j); next != -1 && (fcWord(next) || fcIn(next, "./-")) {
		return "", "", 0, false
	}
	return string(span[:i]), string(span[i+1 : j]), j, true
}

var fcWellKnownCLIs = map[string]bool{"git": true, "gh": true, "npm": true, "npx": true, "node": true, "pnpm": true, "yarn": true, "bun": true, "deno": true, "ls": true, "cat": true, "grep": true, "which": true, "find": true, "test": true, "make": true, "docker": true, "curl": true, "claude": true, "codex": true, "python": true, "python3": true, "pip": true, "go": true, "cargo": true, "op": true}

// fcBareCLI is BARE_CLI.
func fcBareCLI(s string) bool {
	if s == "" || !(fcLower(rune(s[0])) || fcDigit(rune(s[0]))) {
		return false
	}
	for _, r := range s {
		if !fcLower(r) && !fcDigit(r) && !fcIn(r, "._-") {
			return false
		}
	}
	return true
}

// fcCLIWord is cliWordRe(word).test(text).
func fcCLIWord(word, text string) bool {
	s, w := []rune(text), []rune(word)
	for i := 0; i+len(w) <= len(s); i++ {
		if string(s[i:i+len(w)]) != word {
			continue
		}
		if p := fcAt(s, i-1); p != -1 && (fcWord(p) || fcIn(p, "./-")) {
			continue
		}
		n := fcAt(s, i+len(w))
		if n != -1 && (fcWord(n) || n == '/' || n == '-') {
			continue
		}
		if n == '.' && fcWord(fcAt(s, i+len(w)+1)) {
			continue
		}
		return true
	}
	return false
}

// fcNamedAsCLI is namedAsCli.
func fcNamedAsCLI(word, brief string) bool {
	s := []rune(brief)
	for _, m := range fcSpans(s) {
		span := fcTrim(string(s[m.start:m.end]))
		if span == word {
			return true
		}
		if strings.HasPrefix(span, word+" ") {
			rest := strings.TrimLeftFunc(span[len(word)+1:], jsSpace)
			if rest != "" && strings.ContainsRune("-./~", rune(rest[0])) {
				return true
			}
		}
	}
	return false
}

// fcWithProjectCLI is withProjectCli (briefText = text).
func fcWithProjectCLI(token, brief string, projectBins []string) string {
	bins := []string{}
	for _, b := range projectBins {
		if fcBareCLI(b) {
			bins = append(bins, b)
		}
	}
	first := strings.Split(token, " ")[0]
	isBin := false
	for _, b := range bins {
		if b == first {
			isBin = true
		}
	}
	if len(bins) == 0 || isBin || fcWellKnownCLIs[first] || fcNamedAsCLI(first, brief) {
		return token
	}
	for _, b := range bins {
		if fcCLIWord(b, brief) {
			return b + " " + token
		}
	}
	return token
}

func fcHasGlob(s string) bool { return strings.ContainsAny(s, "*?[]{}") }

// fcPathExt is /\.[a-zA-Z0-9][\w-]*$/.
func fcPathExt(token string) bool {
	s := []rune(token)
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '.' {
			if i+1 < len(s) && (fcWord(s[i+1]) && s[i+1] != '_') {
				return true
			}
			return false
		}
		if !fcWord(s[i]) && s[i] != '-' {
			return false
		}
	}
	return false
}

func fcPathRooted(token string) bool {
	for _, p := range []string{"/", "~/", "./", "../"} {
		if strings.HasPrefix(token, p) {
			return true
		}
	}
	return false
}

// fcExtractClaims is extractClaims(text, {symbols: true, globPaths: true,
// projectBins, briefText: text, projectDir}).
func fcExtractClaims(text string, projectBins []string, topLevel map[string]bool) []fcClaim {
	masked := []rune(text)
	type found struct {
		index       int
		token, kind string
	}
	all := []found{}
	mask := func(start, end int) {
		for i := start; i < end; i++ {
			masked[i] = ' '
		}
	}
	scan := func(kind string, matches []fcMatch, src []rune) {
		for _, m := range matches {
			raw := string(src[m.start:m.end])
			if kind == "env" {
				raw = strings.TrimPrefix(raw, "$")
			}
			token := fcTrimTail(raw)
			if token == "" || (kind == "path" && (token == "/" || token == "~/" || token == "./" || token == "../")) {
				continue
			}
			if kind == "path" && !fcPathRooted(token) && !fcHasGlob(token) && !fcPathExt(token) && !topLevel[strings.Split(token, "/")[0]] {
				continue
			}
			all = append(all, found{m.start, token, kind})
			mask(m.start, m.end)
		}
	}
	snap := func() []rune { return append([]rune{}, masked...) }
	s := snap()
	scan("endpoint", fcScanEndpoint(s), s)
	s = snap()
	scan("package", fcScanPackage(s), s)
	s = snap()
	scan("path", fcScanPath(s), s)
	s = snap()
	for _, m := range fcSpans(s) {
		content := s[m.start:m.end]
		symbol := fcTrim(string(content))
		if fcIdentifierSymbol(symbol) || fcFileSymbol(symbol) {
			all = append(all, found{m.start, symbol, "symbol"})
			mask(m.start, m.end)
			continue
		}
		a, b, n, ok := fcCommandInSpan(content)
		if !ok {
			continue
		}
		all = append(all, found{m.start, fcWithProjectCLI(a+" "+b, text, projectBins), "command"})
		mask(m.start, m.start+n)
	}
	s = snap()
	scan("flag", fcScanFlag(s), s)
	s = snap()
	scan("version", fcScanVersion(s), s)
	s = snap()
	scan("env", fcScanEnv(s), s)
	sort.SliceStable(all, func(i, j int) bool { return all[i].index < all[j].index })
	seen := map[string]bool{}
	claims := []fcClaim{}
	for _, f := range all {
		key := f.kind + "\x00" + f.token
		if seen[key] {
			continue
		}
		seen[key] = true
		claims = append(claims, fcClaim{token: f.token, kind: f.kind})
	}
	return claims
}

// fcProjectBins is readProjectBins with every failure read as none (buildFacts).
func fcProjectBins(data []byte) []string {
	v, err := ledger.DecodeValue(data)
	if err != nil {
		return []string{}
	}
	pkg, ok := v.(ledger.Object)
	if !ok {
		return []string{}
	}
	if bin, ok := pkg.Get("bin").(string); ok {
		_ = bin
		if name, ok := pkg.Get("name").(string); ok {
			if strings.HasPrefix(name, "@") {
				if i := strings.Index(name, "/"); i > 1 {
					name = name[i+1:]
				}
			}
			return []string{name}
		}
	}
	if bin, ok := pkg.Get("bin").(ledger.Object); ok {
		keys := []string{}
		ints := []string{}
		for _, f := range bin {
			if fcArrayIndex(f.Key) {
				ints = append(ints, f.Key)
			} else {
				keys = append(keys, f.Key)
			}
		}
		sort.SliceStable(ints, func(i, j int) bool {
			if len(ints[i]) != len(ints[j]) {
				return len(ints[i]) < len(ints[j])
			}
			return ints[i] < ints[j]
		})
		return append(ints, keys...)
	}
	return []string{}
}

func fcArrayIndex(key string) bool {
	if key == "" || (len(key) > 1 && key[0] == '0') || len(key) > 10 {
		return false
	}
	var n uint64
	for _, r := range key {
		if !fcDigit(r) {
			return false
		}
		n = n*10 + uint64(r-'0')
	}
	return n < 4294967295
}

// ---------------------------------------------------------------------------
// step 3: the read-only check rules

// fcSecretPath is SECRET_PATH.some((re) => re.test(t)).
func fcSecretPath(t string) bool {
	segs := strings.Split(t, "/")
	last := segs[len(segs)-1]
	lastTwo := func(a, b string) bool {
		return len(segs) >= 2 && segs[len(segs)-2] == a && segs[len(segs)-1] == b
	}
	hasSeg := func(name string) bool {
		for _, s := range segs {
			if s == name {
				return true
			}
		}
		return false
	}
	if strings.HasPrefix(last, ".env") || last == ".npmrc" || last == ".netrc" || last == ".git-credentials" || last == ".pgpass" {
		return true
	}
	if strings.Contains(t, "id_rsa") || strings.Contains(t, "id_ed25519") || strings.HasSuffix(t, ".pem") || strings.HasSuffix(t, ".key") {
		return true
	}
	if hasSeg(".ssh") || hasSeg(".aws") || hasSeg(".code-forge") {
		return true
	}
	for i := 0; i+1 < len(segs); i++ {
		if segs[i] == ".config" && segs[i+1] == "gh" {
			return true
		}
	}
	if lastTwo(".docker", "config.json") || lastTwo(".kube", "config") || lastTwo(".claude", ".credentials.json") {
		return true
	}
	return len(segs) == 4 && segs[0] == "" && segs[1] == "proc" && segs[2] != "" && segs[3] == "environ"
}

func fcDotDotSegment(word string) bool {
	for _, s := range strings.Split(word, "/") {
		if s == ".." {
			return true
		}
	}
	return false
}

var fcToolRoots = []string{"/usr", "/opt/homebrew", "/bin"}

// fcPathViolation is pathViolation ("" = none).
func fcPathViolation(word string) string {
	if strings.HasPrefix(word, "~") {
		return "paths under ~ are not allowed"
	}
	if fcDotDotSegment(word) {
		return "paths with a .. segment are not allowed"
	}
	if strings.HasPrefix(word, "/") {
		for _, root := range fcToolRoots {
			if word == root || strings.HasPrefix(word, root+"/") {
				return ""
			}
		}
		return "absolute paths outside /usr, /opt/homebrew and /bin are not allowed"
	}
	return ""
}

type fcSplit struct {
	segments   [][]string
	separators []string
	pipedOut   []bool
	refused    string
}

// fcSplitCommand is splitCommand(command) (allowGlob false).
func fcSplitCommand(command string) fcSplit { return fcSplitCommandGlob(command, false) }

// fcSplitCommandGlob is splitCommand(command, allowGlob).
func fcSplitCommandGlob(command string, allowGlob bool) fcSplit {
	refuse := func(why string) fcSplit { return fcSplit{refused: why} }
	if strings.Contains(command, "`") {
		return refuse("backticks are not allowed")
	}
	if strings.Contains(command, "$") {
		return refuse("$ expansion is not allowed")
	}
	if strings.ContainsAny(command, "<>") {
		return refuse("redirection is not allowed")
	}
	c := []rune(command)
	segments := [][]string{{}}
	separators := []string{}
	piped := []bool{false}
	word, inWord := "", false
	flush := func() {
		if inWord {
			segments[len(segments)-1] = append(segments[len(segments)-1], word)
		}
		word, inWord = "", false
	}
	for i := 0; i < len(c); i++ {
		ch := c[i]
		switch {
		case ch == '\'' || ch == '"':
			end := -1
			for k := i + 1; k < len(c); k++ {
				if c[k] == ch {
					end = k
					break
				}
			}
			if end < 0 {
				return refuse("unbalanced quote")
			}
			word += string(c[i+1 : end])
			inWord = true
			i = end
		case ch == '\\':
			if i+1 >= len(c) || c[i+1] == '\n' || c[i+1] == '\r' {
				return refuse("line continuation is not allowed")
			}
			word += string(c[i+1])
			inWord = true
			i++
		case fcIn(ch, ";&|\n\r"):
			flush()
			if (ch == '&' || ch == '|') && fcAt(c, i+1) == ch {
				separators = append(separators, string([]rune{ch, ch}))
				i++
			} else {
				separators = append(separators, string(ch))
				if ch == '|' {
					piped[len(piped)-1] = true
				}
			}
			segments = append(segments, []string{})
			piped = append(piped, false)
		case ch == ' ' || ch == '\t':
			flush()
		default:
			if !allowGlob && fcIn(ch, "*?[]{}") {
				return refuse("unquoted glob characters (* ? [ ] { }) are not allowed")
			}
			word += string(ch)
			inWord = true
		}
	}
	flush()
	out := fcSplit{separators: separators}
	for i, s := range segments {
		if len(s) > 0 {
			out.segments = append(out.segments, s)
			out.pipedOut = append(out.pipedOut, piped[i])
		}
	}
	return out
}

var fcPlainReaders = map[string]bool{"ls": true, "cat": true, "head": true, "tail": true, "wc": true, "grep": true, "stat": true, "file": true, "uname": true, "sw_vers": true, "jq": true, "which": true}
var fcGitReads = map[string]bool{"log": true, "show": true, "status": true, "rev-parse": true, "ls-files": true, "cat-file": true}
var fcFindPredicates = []string{"-name", "-iname", "-path", "-ipath", "-type", "-maxdepth", "-mindepth", "-print", "-print0", "-newer", "-size", "-empty", "-mtime", "-mmin", "-not", "!", "-a", "-o", "-and", "-or"}
var fcFindTakesValue = map[string]bool{"-name": true, "-iname": true, "-path": true, "-ipath": true, "-type": true, "-maxdepth": true, "-mindepth": true, "-newer": true, "-size": true, "-mtime": true, "-mmin": true}
var fcBuildRunners = map[string]bool{"make": true, "gmake": true, "just": true, "task": true, "rake": true, "gradle": true, "gradlew": true, "mvn": true, "mvnw": true, "ant": true, "ninja": true, "cmake": true, "bazel": true, "bazelisk": true, "tox": true, "nox": true, "invoke": true, "inv": true, "doit": true, "earthly": true, "mage": true, "npx": true, "pnpx": true, "bunx": true}

const fcCurlMessage = "curl: only -s -S -I -L -f --head --silent --show-error --location --fail --max-time <N> and exactly one http(s) URL, as a HEAD request"
const fcGrepMessage = "grep: options only -n -c -i -w -F before --, or the source grep grep -rn -- <flag> <path>"
const fcSourceGrepMessage = "grep -rn: only grep -rn -- <--long-flag> <path> (one relative subdirectory or file, never . or the root)"

func fcFindViolation(rest []string) string {
	message := "find: only " + strings.Join(fcFindPredicates, " ") + " and paths"
	allowed := map[string]bool{}
	for _, p := range fcFindPredicates {
		allowed[p] = true
	}
	for i := 0; i < len(rest); i++ {
		t := rest[i]
		if allowed[t] {
			if fcFindTakesValue[t] {
				if i+1 >= len(rest) {
					return message
				}
				i++
			}
		} else if strings.HasPrefix(t, "-") || t == "(" || t == ")" || t == "," {
			return message
		}
	}
	return ""
}

func fcAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !fcDigit(r) {
			return false
		}
	}
	return true
}

func fcCurlViolation(rest []string) string {
	head, urls := false, 0
	for i := 0; i < len(rest); i++ {
		t := rest[i]
		switch {
		case strings.HasPrefix(t, "http://") || strings.HasPrefix(t, "https://"):
			urls++
		case len(t) > 1 && t[0] == '-' && strings.Trim(t[1:], "sSILf") == "":
			if strings.Contains(t, "I") {
				head = true
			}
		case t == "--head" || t == "--silent" || t == "--show-error" || t == "--location" || t == "--fail":
			if t == "--head" {
				head = true
			}
		case t == "--max-time" && i+1 < len(rest) && fcAllDigits(rest[i+1]):
			i++
		default:
			return fcCurlMessage
		}
	}
	if head && urls == 1 {
		return ""
	}
	return fcCurlMessage
}

func fcGrepOptionWords(rest []string) []string {
	end := len(rest)
	for i, t := range rest {
		if t == "--" {
			end = i
			break
		}
	}
	out := []string{}
	for _, t := range rest[:end] {
		if strings.HasPrefix(t, "-") {
			out = append(out, t)
		}
	}
	return out
}

func fcGrepViolation(rest []string) string {
	recurses := false
	for _, t := range fcGrepOptionWords(rest) {
		switch t {
		case "-n", "-c", "-i", "-w", "-F":
		case "-rn", "-nr":
			recurses = true
		default:
			return fcGrepMessage
		}
	}
	if recurses {
		if _, _, ok := fcSourceGrep(rest); !ok {
			return fcSourceGrepMessage
		}
	}
	return ""
}

func fcVerbWord(verb string) bool {
	if verb == "" {
		return false
	}
	for _, r := range verb {
		if !fcWord(r) && r != '.' && r != '-' {
			return false
		}
	}
	return true
}

// fcSegmentViolation is segmentViolation ("" = allowed).
func fcSegmentViolation(argv []string) string {
	for _, t := range argv {
		if fcSecretPath(t) {
			return "reads a secret-looking file"
		}
	}
	verb, rest := argv[0], argv[1:]
	if strings.Contains(verb, "/") {
		return "a verb may not be a path"
	}
	if !fcVerbWord(verb) {
		return "not an allowed read-only form: \"" + verb + "\""
	}
	for _, word := range rest {
		if why := fcPathViolation(word); why != "" {
			return why
		}
	}
	if fcPlainReaders[verb] {
		if verb == "grep" {
			return fcGrepViolation(rest)
		}
		return ""
	}
	switch verb {
	case "command":
		if len(rest) == 2 && rest[0] == "-v" {
			return ""
		}
		return "command: only command -v <name>"
	case "find":
		return fcFindViolation(rest)
	case "git":
		if len(rest) == 0 || !fcGitReads[rest[0]] {
			return "git: only log, show, status, rev-parse, ls-files or cat-file, with no global option"
		}
		for _, t := range rest {
			if strings.HasPrefix(t, "--output") || strings.HasPrefix(t, "--ext-diff") || strings.HasPrefix(t, "--textconv") {
				return "git: --output, --ext-diff and --textconv are not allowed"
			}
		}
		return ""
	case "npm":
		if len(rest) > 0 && (rest[0] == "view" || rest[0] == "ls") {
			return ""
		}
		return "npm: only view or ls"
	case "test":
		if len(rest) == 2 && (rest[0] == "-e" || rest[0] == "-f" || rest[0] == "-d" || rest[0] == "-n") {
			return ""
		}
		return "test: only -e, -f, -d or -n with one operand"
	case "curl":
		return fcCurlViolation(rest)
	}
	if fcBuildRunners[verb] {
		return verb + ": build and task runners are not allowed"
	}
	return "not an allowed read-only form: \"" + verb + "\""
}

// fcIsSubPath is isSubPath.
func fcIsSubPath(where string) bool {
	if where == "" || strings.HasPrefix(where, "/") || strings.HasPrefix(where, "~") || strings.HasPrefix(where, "-") {
		return false
	}
	if fcPathViolation(where) != "" || fcSecretPath(where) {
		return false
	}
	norm := fcPosixNormalize(where)
	if norm == "." || norm == "./" || strings.HasPrefix(norm, "..") {
		return false
	}
	for _, seg := range strings.Split(norm, "/") {
		if fcSecretPath(seg) {
			return false
		}
	}
	return true
}

func fcIdentifierWord(s string) bool {
	if s == "" || !(fcWord(rune(s[0])) && !fcDigit(rune(s[0]))) {
		return false
	}
	for _, r := range s {
		if !fcWord(r) {
			return false
		}
	}
	return true
}

var fcKeywords = []string{"class", "interface", "trait", "enum", "struct", "type", "function", "func", "def"}

// fcKeywordPrefix reports pattern starting with "<keyword> ".
func fcKeywordPrefix(pattern string) bool {
	for _, k := range fcKeywords {
		if strings.HasPrefix(pattern, k+" ") {
			return true
		}
	}
	return false
}

// fcSourceGrepFlag is SOURCE_GREP_FLAG.
func fcSourceGrepFlag(p string) bool {
	if len(p) < 5 || !strings.HasPrefix(p, "--") || !fcLower(rune(p[2])) {
		return false
	}
	for _, r := range p[3:] {
		if !fcLower(r) && !fcDigit(r) && r != '-' {
			return false
		}
	}
	return true
}

// fcSourceGrep is sourceGrep(rest): the pattern and the path.
func fcSourceGrep(rest []string) (string, string, bool) {
	if len(rest) != 4 || (rest[0] != "-rn" && rest[0] != "-nr") || rest[1] != "--" {
		return "", "", false
	}
	pattern, where := rest[2], rest[3]
	symbol := fcIdentifierWord(pattern)
	if !symbol {
		for _, k := range fcKeywords {
			if strings.HasPrefix(pattern, k+" ") && fcIdentifierWord(pattern[len(k)+1:]) {
				symbol = true
			}
		}
	}
	if (fcSourceGrepFlag(pattern) || symbol) && fcIsSubPath(where) {
		return pattern, where, true
	}
	return "", "", false
}

func fcSourceGrepOf(command string) (string, string, bool) {
	sp := fcSplitCommand(command)
	if sp.refused != "" || len(sp.segments) != 1 || len(sp.separators) != 0 || sp.segments[0][0] != "grep" {
		return "", "", false
	}
	return fcSourceGrep(sp.segments[0][1:])
}

// fcPosixNormalize is path.posix.normalize.
func fcPosixNormalize(p string) string {
	if p == "" {
		return "."
	}
	abs := strings.HasPrefix(p, "/")
	trailing := strings.HasSuffix(p, "/")
	stack := []string{}
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "", ".":
		case "..":
			if len(stack) > 0 && stack[len(stack)-1] != ".." {
				stack = stack[:len(stack)-1]
			} else if !abs {
				stack = append(stack, "..")
			}
		default:
			stack = append(stack, seg)
		}
	}
	out := strings.Join(stack, "/")
	if out == "" {
		if abs {
			return "/"
		}
		if trailing {
			return "./"
		}
		return "."
	}
	if trailing {
		out += "/"
	}
	if abs {
		return "/" + out
	}
	return out
}

// fcRelPosix is relPosix.
func fcRelPosix(p string) string {
	n := fcPosixNormalize(p)
	for strings.HasPrefix(n, "./") {
		n = n[2:]
	}
	return strings.TrimRight(n, "/")
}

var fcNonSourceDirs = map[string]bool{"docs": true, "plans": true, "test": true, "tests": true, "fixtures": true, "node_modules": true}
var fcNonSourceFolders = map[string]bool{"docs": true, "plans": true, "test": true, "tests": true, "fixtures": true, "node_modules": true, "dist": true, "build": true, "coverage": true, "out": true}

func fcIsSourceFile(file string, briefRel *string) bool {
	rel := fcRelPosix(file)
	if briefRel != nil && rel == fcRelPosix(*briefRel) {
		return false
	}
	lower := strings.ToLower(rel)
	if strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".markdown") || strings.HasSuffix(lower, ".txt") {
		return false
	}
	segs := strings.Split(rel, "/")
	for _, seg := range segs[:len(segs)-1] {
		if fcNonSourceDirs[seg] {
			return false
		}
	}
	return true
}

// fcLines is text.split(/\r?\n/).
func fcLines(text string) []string {
	parts := strings.Split(text, "\n")
	for i := 0; i < len(parts)-1; i++ {
		parts[i] = strings.TrimSuffix(parts[i], "\r")
	}
	return parts
}

// fcHitLine is /^([^:\n]+):(\d+):(.*)$/.
func fcHitLine(line string) (string, string, bool) {
	i := strings.IndexByte(line, ':')
	if i <= 0 || strings.Contains(line[:i], "\n") {
		return "", "", false
	}
	j := i + 1
	for j < len(line) && fcDigit(rune(line[j])) {
		j++
	}
	if j == i+1 || j >= len(line) || line[j] != ':' {
		return "", "", false
	}
	text := line[j+1:]
	if strings.ContainsAny(text, "\n\r  ") {
		return "", "", false
	}
	return line[:i], text, true
}

// fcHoldsFlag is holdsFlag.
func fcHoldsFlag(text, flag string) bool {
	s, f := []rune(text), []rune(flag)
	for i := 0; i+len(f) <= len(s); i++ {
		if string(s[i:i+len(f)]) != flag {
			continue
		}
		if p := fcAt(s, i-1); p != -1 && (fcWord(p) || p == '-') {
			continue
		}
		if n := fcAt(s, i+len(f)); n != -1 && (fcWord(n) || n == '-') {
			continue
		}
		return true
	}
	return false
}

// fcWholeWord is new RegExp(`\b${token}\b`).test(text) for an identifier token.
func fcWholeWord(text, token string) bool {
	s, t := []rune(text), []rune(token)
	if len(t) == 0 {
		return false
	}
	for i := 0; i+len(t) <= len(s); i++ {
		if string(s[i:i+len(t)]) != token {
			continue
		}
		before := fcAt(s, i-1)
		if fcWord(t[0]) == (before != -1 && fcWord(before)) {
			continue
		}
		after := fcAt(s, i+len(t))
		if fcWord(t[len(t)-1]) == (after != -1 && fcWord(after)) {
			continue
		}
		return true
	}
	return false
}

func fcSourceHit(excerpt, flag, under string, briefRel *string, symbol bool, prefix string) bool {
	scope := fcRelPosix(under)
	for _, line := range fcLines(excerpt) {
		name, text, ok := fcHitLine(fcTrim(line))
		if !ok {
			continue
		}
		file := fcRelPosix(name)
		sourceFile := file
		var rel *string = briefRel
		if prefix != "" {
			if len(file) > len(prefix) {
				sourceFile = file[len(prefix)+1:]
			} else {
				sourceFile = ""
			}
			rel = nil
		}
		if !(file == scope || strings.HasPrefix(file, scope+"/")) || (briefRel != nil && file == *briefRel) || !fcIsSourceFile(sourceFile, rel) {
			continue
		}
		if symbol && fcWholeWord(text, flag) || !symbol && fcHoldsFlag(text, flag) {
			return true
		}
	}
	return false
}

func fcIsSourceFolder(where, cwd string, topLevel bool, prefix string) bool {
	rel := fcRelPosix(where)
	if prefix != "" && !strings.HasPrefix(rel, prefix+"/") {
		return false
	}
	inner := rel
	if prefix != "" {
		inner = rel[len(prefix)+1:]
	}
	segs := strings.Split(inner, "/")
	if cwd == "" || rel == "" || rel == "." || (topLevel && len(segs) != 1) {
		return false
	}
	for _, seg := range segs {
		if seg == "_sources" || strings.HasPrefix(seg, ".") || fcNonSourceFolders[seg] {
			return false
		}
	}
	return fcIsDir(filepath.Join(cwd, rel))
}

const fcOneCommandMessage = "one command per row: no pipe or list (| ; && || & or a newline)"

// fcReadOnlyViolation is readOnlyViolation ("" = allowed).
func fcReadOnlyViolation(command string) string {
	sp := fcSplitCommand(command)
	if sp.refused != "" {
		return sp.refused
	}
	if len(sp.segments) == 0 {
		return "empty command"
	}
	if len(sp.separators) > 0 {
		return fcOneCommandMessage
	}
	for _, argv := range sp.segments {
		if why := fcSegmentViolation(argv); why != "" {
			return why
		}
	}
	return ""
}

// fcEnvCheck is ENV_CHECK: the variable name.
func fcEnvCheck(command string) (string, bool) {
	const head, tail = "printenv ", " >/dev/null"
	if !strings.HasPrefix(command, head) || !strings.HasSuffix(command, tail) || len(command) < len(head)+len(tail)+1 {
		return "", false
	}
	name := command[len(head) : len(command)-len(tail)]
	if !fcUpper(rune(name[0])) {
		return "", false
	}
	for _, r := range name {
		if !fcUpper(r) && !fcDigit(r) && r != '_' {
			return "", false
		}
	}
	return name, true
}

func fcUnquoteBackticks(text string) string {
	t := fcTrim(text)
	if len(t) < 2 || t[0] != '`' || t[len(t)-1] != '`' {
		return t
	}
	if strings.Trim(t, "`") == "" {
		return ""
	}
	inner := strings.TrimLeft(t, "`")
	inner = strings.TrimRight(inner, "`")
	return fcTrim(inner)
}

// fcEchoedClaim is echoedClaim.
func fcEchoedClaim(claim, kind string) string {
	t := fcUnquoteBackticks(claim)
	t = strings.TrimPrefix(t, kind+": ")
	return fcUnquoteBackticks(t)
}

func fcShortCluster(t string, letter byte) bool {
	if len(t) < 2 || t[0] != '-' {
		return false
	}
	for i := 1; i < len(t); i++ {
		c := t[i]
		if c == letter {
			return true
		}
		if !fcUpper(rune(c)) && !fcLower(rune(c)) {
			return false
		}
	}
	return false
}

func fcIsSilentSegment(argv []string) bool {
	verb, rest := argv[0], argv[1:]
	if verb == "test" {
		return true
	}
	end := len(rest)
	for i, t := range rest {
		if t == "--" {
			end = i
			break
		}
	}
	for _, t := range rest[:end] {
		if (verb == "grep" || verb == "rg") && (fcShortCluster(t, 'q') || t == "--quiet" || t == "--silent") {
			return true
		}
		if verb == "which" && fcShortCluster(t, 's') {
			return true
		}
	}
	return false
}

func fcIsSilentCommand(command string) bool {
	sp := fcSplitCommand(command)
	if len(sp.segments) == 0 {
		return false
	}
	for i, argv := range sp.segments {
		if !(i < len(sp.pipedOut) && sp.pipedOut[i]) && !fcIsSilentSegment(argv) {
			return false
		}
	}
	return true
}

const (
	fcSourceGrepClaimMessage = "a source grep counts only for a flag claim, with that flag as its pattern; not counted"
	fcSourceGrepHitMessage   = "VERIFIED source grep without a file:line: hit in a source file naming the claimed flag (docs, plans, tests, fixtures, text files and the brief do not count); not counted"
	fcSourceGrepScopeMessage = "NOT-FOUND source grep must search one whole top-level source folder (not docs, plans, test, tests, fixtures or node_modules); not counted"
	fcFlagProofMessage       = "a flag claim is proved only by grep -rn -- <flag> <source folder>; not counted"
	fcSymbolProofMessage     = "a symbol claim needs grep -rn -- <quoted token or keyword token> <top-level source folder> with a file:line: source hit, or find <root> -name <file name>; not counted"
)

type fcCtx struct {
	briefRel *string
	cwd      string
}

func (c fcClaim) prefix() string {
	if c.root == nil {
		return ""
	}
	return c.root.prefix
}

// fcQuotedGrep is /^grep -rn -- (['"])[^'"]+\1(?:\s|$)/.
func fcQuotedGrep(command string) bool {
	const head = "grep -rn -- "
	if !strings.HasPrefix(command, head) || len(command) < len(head)+3 {
		return false
	}
	rest := []rune(command[len(head):])
	q := rest[0]
	if q != '\'' && q != '"' {
		return false
	}
	i := 1
	for i < len(rest) && rest[i] != '\'' && rest[i] != '"' {
		i++
	}
	if i == 1 || i >= len(rest) || rest[i] != q {
		return false
	}
	return i+1 == len(rest) || jsSpace(rest[i+1])
}

func fcSymbolRule(row fcFact, claim fcClaim, ctx fcCtx) string {
	if fcReadOnlyViolation(row.command) != "" {
		return fcSymbolProofMessage
	}
	prefix := claim.prefix()
	if fcFileSymbol(claim.token) {
		var argv []string
		if sp := fcSplitCommand(row.command); len(sp.segments) > 0 {
			argv = sp.segments[0]
		}
		root := prefix
		if root == "" {
			root = "."
		}
		if len(argv) != 4 || argv[0] != "find" || argv[1] != root || argv[2] != "-name" || argv[3] != claim.token {
			return fcSymbolProofMessage
		}
		if row.tag == "VERIFIED" {
			hit := false
			for _, line := range fcLines(row.excerpt) {
				trimmed := fcTrim(line)
				file := fcRelPosix(trimmed)
				under := !strings.HasPrefix(file, "_sources/")
				if prefix != "" {
					under = strings.HasPrefix(file, prefix+"/")
				}
				if strings.HasSuffix(trimmed, "/"+claim.token) && under && fcIsSubPath(file) {
					hit = true
					break
				}
			}
			if !hit {
				return fcSymbolProofMessage
			}
		}
		return ""
	}
	pattern, where, ok := fcSourceGrepOf(row.command)
	named := pattern == claim.token
	if !named && fcKeywordPrefix(pattern) {
		parts := strings.Split(pattern, " ")
		named = len(parts) > 1 && parts[1] == claim.token
	}
	if !ok || !fcQuotedGrep(row.command) || !named || !fcIsSourceFolder(where, ctx.cwd, true, prefix) {
		return fcSymbolProofMessage
	}
	if row.tag == "VERIFIED" && !fcSourceHit(row.excerpt, claim.token, where, ctx.briefRel, true, prefix) {
		return fcSymbolProofMessage
	}
	return ""
}

// fcRuleBroken is ruleBroken ("" = none).
func fcRuleBroken(row fcFact, claim fcClaim, ctx fcCtx) string {
	if row.tag == "UNVERIFIABLE" {
		return ""
	}
	if fcTrim(row.command) == "" {
		return row.tag + " without a check command; not counted"
	}
	if claim.kind == "path" && (strings.HasPrefix(claim.token, "~") || fcDotDotSegment(claim.token)) {
		return "check command not allowed: " + fcPathViolation(claim.token) + "; not counted"
	}
	if claim.kind == "symbol" {
		return fcSymbolRule(row, claim, ctx)
	}
	if claim.kind == "path" && fcHasGlob(claim.token) {
		return fcGlobRule(row, claim)
	}
	if claim.kind == "env" {
		if name, ok := fcEnvCheck(row.command); !ok || name != claim.token {
			return "an env claim may only be checked with printenv " + claim.token + " >/dev/null; not counted"
		}
	} else {
		if why := fcReadOnlyViolation(row.command); why != "" {
			return "check command not allowed: " + why + "; not counted"
		}
		if row.tag == "VERIFIED" && fcIsSilentCommand(row.command) {
			return "silent command; excerpt cannot be its output; not counted"
		}
		pattern, where, grep := fcSourceGrepOf(row.command)
		prefix := claim.prefix()
		if grep && (claim.kind != "flag" || pattern != claim.token) {
			return fcSourceGrepClaimMessage
		}
		if claim.kind == "flag" && (!grep || !fcIsSourceFolder(where, ctx.cwd, false, prefix)) {
			return fcFlagProofMessage
		}
		if grep && row.tag == "VERIFIED" && !fcSourceHit(row.excerpt, pattern, where, ctx.briefRel, false, prefix) {
			return fcSourceGrepHitMessage
		}
		if grep && row.tag == "NOT-FOUND" && !fcIsSourceFolder(where, ctx.cwd, true, prefix) {
			return fcSourceGrepScopeMessage
		}
	}
	if row.tag == "VERIFIED" && fcTrim(row.excerpt) == "" {
		return "VERIFIED with an empty output excerpt; not counted"
	}
	return ""
}

var fcKinds = []string{"flag", "command", "path", "version", "endpoint", "package", "env", "symbol"}
var fcTags = []string{"VERIFIED", "NOT-FOUND", "UNVERIFIABLE"}
var fcFieldOrder = []string{"fact_id", "claim", "kind", "command", "output_excerpt", "tag", "why"}

func fcOneOf(v any, list []string) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// fcValidAnswer is matchesSchema(answer, FACTS_SESSION_SCHEMA).
func fcValidAnswer(answer any) bool {
	obj, ok := answer.(ledger.Object)
	if !ok || len(obj) != 1 || obj[0].Key != "facts" {
		return false
	}
	rows, ok := obj[0].Value.([]any)
	if !ok {
		return false
	}
	for _, r := range rows {
		row, ok := r.(ledger.Object)
		if !ok || len(row) != len(fcFieldOrder) {
			return false
		}
		for _, key := range fcFieldOrder {
			found := false
			for _, f := range row {
				if f.Key == key {
					found = true
				}
			}
			if !found {
				return false
			}
		}
		for _, key := range []string{"fact_id", "claim", "command", "output_excerpt"} {
			if _, ok := row.Get(key).(string); !ok {
				return false
			}
		}
		if !fcOneOf(row.Get("kind"), fcKinds) || !fcOneOf(row.Get("tag"), fcTags) {
			return false
		}
		if why := row.Get("why"); why != nil {
			if _, ok := why.(string); !ok {
				return false
			}
		}
	}
	return true
}

// fcCapExcerpt caps an excerpt at 200 UTF-16 units: the first 199 and "…".
// A surrogate pair cut in half leaves its high half as WTF-8 bytes, which
// fcJSONString writes as \udXXX and fcWTF8ToUTF8 as U+FFFD, as Node does.
func fcCapExcerpt(s string) string {
	units := 0
	for _, r := range s {
		if r > 0xFFFF {
			units += 2
		} else {
			units++
		}
	}
	if units <= 200 {
		return s
	}
	var b strings.Builder
	n := 0
	for _, r := range s {
		w := 1
		if r > 0xFFFF {
			w = 2
		}
		if n+w > 199 {
			if n < 199 {
				hi := 0xD800 + ((r - 0x10000) >> 10)
				b.Write([]byte{0xED, byte(0xA0 | ((hi >> 6) & 0x0F)), byte(0x80 | (hi & 0x3F))})
			}
			break
		}
		b.WriteRune(r)
		n += w
	}
	return b.String() + "…"
}

// fcFactIDPattern is ^F[0-9]+$.
func fcFactIDPattern(id string) bool {
	return len(id) > 1 && id[0] == 'F' && fcAllDigits(id[1:])
}

type fcRefused struct{ message string }

func (e *fcRefused) Error() string { return e.message }

// fcValidateFacts is validateFacts for an answer that already matches the
// session schema (spawnSession checked it).
func fcValidateFacts(answer any, claims []fcClaim, ctx fcCtx) ([]fcFact, []fcDowngrade, error) {
	raw, _ := answer.(ledger.Object).Get("facts").([]any)
	rows := make([]fcFact, len(raw))
	errs := []string{}
	for i, r := range raw {
		obj := r.(ledger.Object)
		row := fcFact{factID: obj.Get("fact_id").(string), claim: obj.Get("claim").(string), kind: obj.Get("kind").(string),
			command: obj.Get("command").(string), excerpt: fcCapExcerpt(obj.Get("output_excerpt").(string)), tag: obj.Get("tag").(string)}
		if why, ok := obj.Get("why").(string); ok {
			row.why = &why
		}
		rows[i] = row
		if !fcFactIDPattern(row.factID) {
			errs = append(errs, "/facts/"+factsItoa(i)+"/fact_id must match pattern \"^F[0-9]+$\"")
		}
		if row.claim == "" {
			errs = append(errs, "/facts/"+factsItoa(i)+"/claim must NOT have fewer than 1 characters")
		}
	}
	if len(errs) > 0 {
		if len(errs) > 5 {
			errs = errs[:5]
		}
		return nil, nil, &fcRefused{"facts answer does not match facts.schema.json: " + strings.Join(errs, "; ")}
	}
	taken := make([]bool, len(rows))
	chosen := make([]int, len(claims))
	for i, claim := range claims {
		id := "F" + factsItoa(i+1)
		chosen[i] = -1
		for k, r := range rows {
			if !taken[k] && r.factID == id && r.kind == claim.kind {
				chosen[i] = k
				break
			}
		}
		if chosen[i] < 0 {
			for k, r := range rows {
				if !taken[k] && r.factID == id && fcEchoedClaim(r.claim, claim.kind) == claim.token {
					chosen[i] = k
					break
				}
			}
		}
		if chosen[i] >= 0 {
			taken[chosen[i]] = true
		}
	}
	for i, claim := range claims {
		if chosen[i] >= 0 {
			continue
		}
		same := 0
		for _, c := range claims {
			if c.token == claim.token {
				same++
			}
		}
		if same > 1 {
			continue
		}
		for k, r := range rows {
			if !taken[k] && r.kind == claim.kind && fcEchoedClaim(r.claim, claim.kind) == claim.token {
				chosen[i] = k
				taken[k] = true
				break
			}
		}
	}
	facts := []fcFact{}
	downgraded := []fcDowngrade{}
	for i, claim := range claims {
		base := fcFact{factID: "F" + factsItoa(i+1), claim: claim.token, kind: claim.kind, source: claim.source, hasSource: claim.hasSource}
		if claim.unavailable != "" || chosen[i] < 0 {
			why := claim.unavailable
			if why == "" {
				why = "no answer from the delegate"
			}
			base.tag, base.why = "UNVERIFIABLE", &why
			facts = append(facts, base)
			continue
		}
		row := rows[chosen[i]]
		base.command = row.command
		if claim.kind != "env" {
			base.excerpt = row.excerpt
		}
		if broken := fcRuleBroken(row, claim, ctx); broken != "" {
			downgraded = append(downgraded, fcDowngrade{base.factID, broken})
			base.tag, base.why = "UNVERIFIABLE", &broken
		} else {
			base.tag, base.why = row.tag, row.why
		}
		facts = append(facts, base)
	}
	return facts, downgraded, nil
}

func factsItoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for ; n > 0; n /= 10 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
	}
	return string(digits)
}

// fcJSONString is JSON.stringify(s) for a string (WTF-8 lone surrogates as
// \udXXX).
func fcJSONString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	const hex = "0123456789abcdef"
	for i := 0; i < len(s); {
		c := s[i]
		if c == 0xED && i+2 < len(s) && s[i+1] >= 0xA0 && s[i+1] <= 0xBF {
			v := (rune(c&0x0F) << 12) | (rune(s[i+1]&0x3F) << 6) | rune(s[i+2]&0x3F)
			b.WriteString("\\u")
			b.WriteByte(hex[v>>12&0xF])
			b.WriteByte(hex[v>>8&0xF])
			b.WriteByte(hex[v>>4&0xF])
			b.WriteByte(hex[v&0xF])
			i += 3
			continue
		}
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 {
				b.WriteString(`\u00`)
				b.WriteByte(hex[c>>4])
				b.WriteByte(hex[c&0xF])
			} else {
				b.WriteByte(c)
			}
		}
		i++
	}
	b.WriteByte('"')
	return b.String()
}

// fcWTF8ToUTF8 is how Node writes a string with a lone surrogate: U+FFFD.
func fcWTF8ToUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0xED && i+2 < len(s) && s[i+1] >= 0xA0 && s[i+1] <= 0xBF {
			b.WriteString("�")
			i += 3
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func fcFactJSON(f fcFact) string {
	parts := []string{`"fact_id":` + fcJSONString(f.factID), `"claim":` + fcJSONString(f.claim), `"kind":` + fcJSONString(f.kind)}
	if f.hasSource {
		parts = append(parts, `"source":`+fcJSONString(f.source))
	}
	parts = append(parts, `"command":`+fcJSONString(f.command), `"output_excerpt":`+fcJSONString(f.excerpt), `"tag":`+fcJSONString(f.tag))
	if f.why == nil {
		parts = append(parts, `"why":null`)
	} else {
		parts = append(parts, `"why":`+fcJSONString(*f.why))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func fcCell(text string) string {
	text = strings.ReplaceAll(text, "\r\n", " ")
	text = strings.ReplaceAll(text, "\n", " ")
	return strings.ReplaceAll(text, "|", "\\|")
}

func fcCode(text string) string {
	if text == "" {
		return "—"
	}
	if strings.Contains(text, "`") {
		return "`` " + text + " ``"
	}
	return "`" + text + "`"
}

// fcRenderSheet is renderSheet.
func fcRenderSheet(briefRef, briefSha, briefMtime, builtAt string, facts []fcFact) string {
	count := func(tag string) int {
		n := 0
		for _, f := range facts {
			if f.tag == tag {
				n++
			}
		}
		return n
	}
	sources := false
	for _, f := range facts {
		if f.hasSource {
			sources = true
		}
	}
	lines := []string{"# Facts sheet", "",
		"- brief: `" + briefRef + "`",
		"- brief_sha256: `" + briefSha + "`",
		"- brief_mtime: `" + briefMtime + "`",
		"- built_at: `" + builtAt + "`",
		"- claims: " + factsItoa(len(facts)) + " (VERIFIED " + factsItoa(count("VERIFIED")) + " · NOT-FOUND " + factsItoa(count("NOT-FOUND")) + " · UNVERIFIABLE " + factsItoa(count("UNVERIFIABLE")) + ")",
		""}
	head, rule := "| fact_id | tag | kind |", "|---|---|---|---|---|---|---|"
	if sources {
		head += " source |"
		rule += "---|"
	}
	lines = append(lines, head+" claim | command | output | why |", rule)
	jsonRows := []string{}
	for _, f := range facts {
		line := "| " + f.factID + " | " + f.tag + " | " + f.kind + " |"
		if sources {
			src := "—"
			if f.hasSource {
				src = f.source
			}
			line += " " + fcCell(src) + " |"
		}
		out := f.excerpt
		if out == "" {
			out = "—"
		}
		why := "—"
		if f.why != nil {
			why = *f.why
		}
		line += " " + fcCell(fcCode(f.claim)) + " | " + fcCell(fcCode(f.command)) + " | " + fcCell(out) + " | " + fcCell(why) + " |"
		lines = append(lines, line)
		jsonRows = append(jsonRows, fcFactJSON(f))
	}
	block := "[\n" + strings.Join(jsonRows, ",\n") + "\n]"
	block = strings.ReplaceAll(strings.ReplaceAll(block, "<", "\\u003c"), ">", "\\u003e")
	lines = append(lines, "", "<!-- facts-json", block, "-->", "")
	return fcWTF8ToUTF8(strings.Join(lines, "\n"))
}

// fcDowngradeLine is downgradeLine.
func fcDowngradeLine(d fcDowngrade) string {
	id := d.factID
	if !fcFactIDPattern(id) {
		id = "F?"
	}
	reason := []rune(d.reason)
	for i, r := range reason {
		if r <= 0x1f || (r >= 0x7f && r <= 0x9f) {
			reason[i] = ' '
		}
	}
	return "facts: " + id + " not counted — " + string(reason) + "\n"
}

// fcIsDir is statSync(p).isDirectory() with errors as false.
func fcIsDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
