package review

// B497: paired.mjs (B97/B98) — stem pairs and bounded names for the
// `## paired` packet section — and engine.mjs pairedContext, plus
// util/gh.mjs findSecret, which each summarized name passes.

import (
	"math"
	"path"
	"regexp"
	"strings"

	"github.com/ricardov03/code-forge/native/contextpack"
	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/gitx"
)

type pairIdentity struct {
	ext, stem string
	test      bool
}

var (
	unsafePairPath = regexp.MustCompile(`[\\\x00-\x1f]`)
	testDirPath    = regexp.MustCompile(`(^|/)(?:test|tests)/`)
	jsTestStem     = regexp.MustCompile(`\.(test|spec)$`)
)

// identity is paired.mjs identity; ok=false is null.
func identity(file string) (pairIdentity, bool) {
	if strings.HasPrefix(file, "/") || unsafePairPath.MatchString(file) {
		return pairIdentity{}, false
	}
	for _, s := range strings.Split(file, "/") {
		if s == "." || s == ".." || s == "" {
			return pairIdentity{}, false
		}
	}
	ext := path.Ext(file)
	stem := strings.TrimSuffix(path.Base(file), ext)
	test := testDirPath.MatchString(file)
	switch ext {
	case ".mjs", ".ts":
		if jsTestStem.MatchString(stem) {
			test, stem = true, jsTestStem.ReplaceAllString(stem, "")
		}
	case ".php":
		if strings.HasSuffix(stem, "Test") {
			test, stem = true, stem[:len(stem)-4]
		}
	case ".go":
		if strings.HasSuffix(stem, "_test") {
			test, stem = true, stem[:len(stem)-5]
		}
	case ".py":
		if strings.HasPrefix(stem, "test_") {
			test, stem = true, stem[5:]
		} else if strings.HasSuffix(stem, "_test") {
			test, stem = true, stem[:len(stem)-5]
		}
	case ".swift":
		if strings.HasSuffix(stem, "Tests") {
			test, stem = true, stem[:len(stem)-5]
		}
	default:
		return pairIdentity{}, false
	}
	return pairIdentity{ext, stem, test}, true
}

func unique(list []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range list {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// PairsFor is paired.mjs pairsFor.
func PairsFor(file string, candidates []string) []string {
	source, ok := identity(file)
	if !ok {
		return []string{}
	}
	out := []string{}
	for _, c := range unique(candidates) {
		other, ok := identity(c)
		if c != file && ok && other.ext == source.ext && other.stem == source.stem && other.test != source.test {
			out = append(out, c)
		}
	}
	return out
}

// jsWS is the JavaScript \s class.
const jsWS = `[\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]`

func jsRe(src string) *regexp.Regexp {
	return regexp.MustCompile(strings.ReplaceAll(src, `\s`, jsWS))
}

var (
	testCallHead = jsRe(`^\s*(?:await\s+)?(?:test|it)(?:\.(?:only|skip|todo))?\(\s*`)
	phpTestFn    = jsRe(`^\s*(?:(?:public|protected)\s+)?function\s+(test\w+)\s*\(`)
	goTestFn     = jsRe(`^\s*func\s+(Test\w+)\s*\(`)
	pyTestFn     = jsRe(`^\s*(?:async\s+)?def\s+(test_\w+)\s*\(`)
	swiftTestFn  = jsRe(`^\s*func\s+(test\w+)\s*\(`)
	jsExport     = jsRe(`^\s*export\s+(?:default\s+)?(?:async\s+)?(?:function\*?|class|const|let|var|interface|type|enum)\s+(\w+)`)
	jsExportList = jsRe(`^\s*export\s*\{([^}]+)\}`)
	jsExportItem = regexp.MustCompile(`^(?:type` + jsWS + `+)?(\w+)(?:` + jsWS + `+as` + jsWS + `+(\w+))?$`)
	phpDecl      = jsRe(`^\s*(?:(?:public|protected|private|static|final|abstract)\s+)*(?:function|class|interface|trait)\s+(\w+)`)
	goDecl       = jsRe(`^\s*(?:func\s+(?:\([^)]*\)\s+)?|(?:type|var|const)\s+)([A-Z]\w*)`)
	pyDecl       = jsRe(`^\s*(?:async\s+)?(?:def|class)\s+([a-zA-Z]\w*)`)
	swiftDecl    = jsRe(`^\s*(?:public|open)\s+(?:final\s+)?(?:func|class|struct|enum|protocol|let|var)\s+(\w+)`)
	crlfSplit    = regexp.MustCompile(`\r?\n`)
)

// testCallName is /^\s*(?:await\s+)?(?:test|it)(?:\.(?:only|skip|todo))?\(\s*(['"`])([^\r\n]+?)\1/
// (Go's regexp has no backreference).
func testCallName(line string) (string, bool) {
	loc := testCallHead.FindStringIndex(line)
	if loc == nil || loc[1] >= len(line) {
		return "", false
	}
	q := line[loc[1]]
	if q != '\'' && q != '"' && q != '`' {
		return "", false
	}
	rest := line[loc[1]+1:]
	for i := 0; i < len(rest); i++ {
		if rest[i] == '\r' || rest[i] == '\n' {
			return "", false
		}
		if rest[i] == q && i >= 1 {
			return rest[:i], true
		}
	}
	return "", false
}

// trimJSString is String.prototype.trim.
func trimJSString(s string) string { return trimJS(s) }

// Summarize is paired.mjs summarize; secrets are the registered redact values.
func Summarize(text, file string, secrets []string) []string {
	id, ok := identity(file)
	if !ok {
		return []string{}
	}
	names := []string{}
	seen := map[string]bool{}
	add := func(n string) {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	for _, line := range crlfSplit.Split(text, -1) {
		var m []string
		if id.test {
			switch id.ext {
			case ".mjs", ".ts", ".php":
				if name, ok := testCallName(line); ok {
					add(name)
				} else if id.ext == ".php" {
					m = phpTestFn.FindStringSubmatch(line)
				} else {
					continue
				}
			case ".go":
				m = goTestFn.FindStringSubmatch(line)
			case ".py":
				m = pyTestFn.FindStringSubmatch(line)
			case ".swift":
				m = swiftTestFn.FindStringSubmatch(line)
			}
		} else {
			switch id.ext {
			case ".mjs", ".ts":
				m = jsExport.FindStringSubmatch(line)
				if list := jsExportList.FindStringSubmatch(line); list != nil {
					for _, item := range strings.Split(list[1], ",") {
						if s := jsExportItem.FindStringSubmatch(trimJSString(item)); s != nil {
							if s[2] != "" {
								add(s[2])
							} else {
								add(s[1])
							}
						}
					}
				}
			case ".php":
				m = phpDecl.FindStringSubmatch(line)
			case ".go":
				m = goDecl.FindStringSubmatch(line)
			case ".py":
				m = pyDecl.FindStringSubmatch(line)
			case ".swift":
				m = swiftDecl.FindStringSubmatch(line)
			}
		}
		if m != nil {
			add(m[1])
		}
		if len(names) >= 40 {
			break
		}
	}
	if len(names) > 40 {
		names = names[:40]
	}
	out := []string{}
	for _, name := range names {
		safe := gates.Mask(name, secrets)
		if FindSecret(safe) {
			safe = "[REDACTED]"
		}
		r := []rune(safe)
		if len(r) > 120 {
			r = r[:120]
		}
		out = append(out, string(r))
	}
	return out
}

// PairedInput is pairedContext's input.
type PairedInput struct {
	RepoRoot                            string
	File                                string
	OwnedFiles, ChangedFiles, TestFiles []string
	Secrets                             []string
}

// PairedContext is engine.mjs pairedContext (names only; missing, unsafe or
// unreadable pairs give no hint).
func PairedContext(in PairedInput) string {
	if len(in.OwnedFiles) == 0 {
		return ""
	}
	owned := map[string]bool{}
	for _, f := range in.OwnedFiles {
		owned[f] = true
	}
	lines := []string{}
	for _, c := range unique(append(PairsFor(in.File, in.ChangedFiles), in.TestFiles...)) {
		if c == in.File || !owned[c] {
			continue
		}
		if assertRowPath(c) != nil || IsSecretLike(c) {
			continue
		}
		data, err := gitx.ReadRegularFileNoFollow(in.RepoRoot, c)
		if err != nil || data == nil {
			continue
		}
		lines = append(lines, "- "+gates.Mask(c, in.Secrets))
		for _, n := range Summarize(contextpack.DecodeUTF8(data), c, in.Secrets) {
			lines = append(lines, "  - "+n)
		}
	}
	return strings.Join(lines, "\n")
}

// boundedPaired caps the section at full_in / 16 tokens without cutting a
// UTF-8 character.
func boundedPaired(paired string, fullIn int) string {
	if trimJS(paired) == "" {
		return ""
	}
	const heading = "## paired\n"
	left := (fullIn/16)*4 - len(heading) - 1
	var body strings.Builder
	for _, r := range paired {
		left -= len(string(r))
		if left < 0 {
			break
		}
		body.WriteRune(r)
	}
	if body.Len() == 0 {
		return ""
	}
	return heading + trimEnd(body.String())
}

// secretRule is one REPORT_SECRET_RULES entry: no character of `notBefore`
// right before the match, none of `notAfter` right after it.
type secretRule struct {
	re                  *regexp.Regexp
	notBefore, notAfter string
}

const (
	wordDash = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-"
	word     = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_"
	alnum    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	upperNum = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
)

var reportSecretRules = []secretRule{
	{regexp.MustCompile(`^sk-[A-Za-z0-9_-]{10,}`), wordDash, ""},
	{regexp.MustCompile(`^(?:gh[opsur]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})`), word, ""},
	{regexp.MustCompile(`^xox[abprs]-[A-Za-z0-9-]{10,}`), wordDash, ""},
	{regexp.MustCompile(`^AKIA[0-9A-Z]{16}`), alnum, upperNum},
	{regexp.MustCompile(`^-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`), "", ""},
	{regexp.MustCompile(`^eyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}`), wordDash, ""},
	// SECRET_LOOKING_PATTERNS
	{regexp.MustCompile(`^sk-[A-Za-z0-9_-]{10,}`), wordDash, ""},
	{regexp.MustCompile(`^xai-[A-Za-z0-9]{20,}`), wordDash, ""},
	{regexp.MustCompile(`^xox[baprs]-[A-Za-z0-9-]{10,}`), wordDash, ""},
	{regexp.MustCompile(`^gh[opsu]_[A-Za-z0-9]{20,}`), wordDash, ""},
	{regexp.MustCompile(`^github_pat_[A-Za-z0-9_]{20,}`), wordDash, ""},
	{regexp.MustCompile(`^AKIA[0-9A-Z]{12,}`), wordDash, ""},
	{regexp.MustCompile(`^AIza[0-9A-Za-z_-]{20,}`), wordDash, ""},
	{regexp.MustCompile(`^eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{5,}`), wordDash, ""},
}

// test is RegExp.test with the lookbehind (and AKIA's lookahead on a
// fixed-length tail) checked at every start.
func (r secretRule) test(line string) bool {
	for i := 0; i < len(line); i++ {
		if i > 0 && r.notBefore != "" && strings.IndexByte(r.notBefore, line[i-1]) >= 0 {
			continue
		}
		loc := r.re.FindStringIndex(line[i:])
		if loc == nil {
			continue
		}
		if r.notAfter != "" {
			end := i + loc[1]
			if end < len(line) && strings.IndexByte(r.notAfter, line[end]) >= 0 {
				continue
			}
		}
		return true
	}
	return false
}

var (
	entropyRun = regexp.MustCompile(`[A-Za-z0-9+/_=-]{32,}`)
	hexRun     = regexp.MustCompile(`^[0-9a-fA-F]+$`)
	upperRe    = regexp.MustCompile(`[A-Z]`)
	lowerRe    = regexp.MustCompile(`[a-z]`)
	digitRe    = regexp.MustCompile(`[0-9]`)
)

func entropy(s string) float64 {
	// Map insertion order, like Node's sum
	freq := map[rune]int{}
	order := []rune{}
	for _, r := range s {
		if freq[r] == 0 {
			order = append(order, r)
		}
		freq[r]++
	}
	n := len(s) // ASCII runs only: bytes = UTF-16 units
	bits := 0.0
	for _, r := range order {
		p := float64(freq[r]) / float64(n)
		bits -= p * math.Log2(p)
	}
	return bits
}

func hasHighEntropyRun(line string) bool {
	for _, run := range entropyRun.FindAllString(line, -1) {
		if hexRun.MatchString(run) {
			continue
		}
		classes := 0
		for _, re := range []*regexp.Regexp{upperRe, lowerRe, digitRe} {
			if re.MatchString(run) {
				classes++
			}
		}
		if classes >= 2 && entropy(run) >= 4 {
			return true
		}
	}
	return false
}

// FindSecret is util/gh.mjs findSecret != null.
func FindSecret(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		for _, r := range reportSecretRules {
			if r.test(line) {
				return true
			}
		}
		if hasHighEntropyRun(line) {
			return true
		}
	}
	return false
}
