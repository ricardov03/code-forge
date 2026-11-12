package review

// B537: src/review/test-quality.mjs — the `## test-quality` hint: a test file
// that reads a source file and asserts on its text gets its line numbers named.

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ricardov03/code-forge/native/gates"
)

const tqMaxLines = 10
const tqSpanLines = 3

var (
	tqReadCalls   = []string{"readFileSync", "readFile", "open(", "read_text", "String(contentsOf", "contentsOfFile", "File.read", "os.ReadFile", "fs::read_to_string", "readText(", "readString(", "ReadAllText("}
	tqAssertCalls = []string{"includes(", "contains(", "toContain(", "toMatch(", "assert.match(", "assertIn(", "strings.Contains("}
	tqDocs        = regexp.MustCompile(`\.(` + ciLiteral("md") + `|` + ciLiteral("txt") + `)$`)
	// LITERAL `(['"`])([^'"`\n]*)\1` without a backreference: the body holds no quote.
	tqLiteral   = regexp.MustCompile("'([^'\"`\\n]*)'|\"([^'\"`\\n]*)\"|`([^'\"`\\n]*)`")
	tqWhole     = regexp.MustCompile("^(?:'[^'\"`\\n]*'|\"[^'\"`\\n]*\"|`[^'\"`\\n]*`)$")
	tqSourceEnd = regexp.MustCompile(`\.(swift|ts|tsx|js|mjs|cjs|py|go|rs|kt|java|rb|php|cs)$`)
	tqTestName  = regexp.MustCompile(`(Tests|Test)\.[^./]+$`)
	tqPathCall  = regexp.MustCompile(`^(?:path\.)?(?:join|resolve)\((` + jsDot + `*)\)$`)
	tqSemicolon = regexp.MustCompile(`;$`)
	// BINDING; `=(?!=)` is `(=)(?:[^=]|$)` and the match ends at that `=`.
	tqBinding   = jsRe(`^\s*(?:(?:export|public|private|protected|internal|fileprivate|open|static|final|readonly|lazy|const)\s+)*(?:(?:const|let|var|val)\s+([A-Za-z_$][\w$]*)|(?:[A-Za-z_][\w.<>\[\],]*\s+)?([A-Za-z_$][\w$]*))\s*(?::[^=]*)?(=)(?:[^=]|$)`)
	tqLineSplit = regexp.MustCompile(`\r?\n`)
)

func tqUnderFixtures(p string) bool {
	for _, s := range strings.Split(p, "/") {
		if s == "fixtures" || s == "fixture" || s == "__fixtures__" {
			return true
		}
	}
	return false
}

// tqCheckedTestFile is isCheckedTestFile.
func tqCheckedTestFile(file string) bool {
	if file == "" || tqDocs.MatchString(file) || tqUnderFixtures(file) {
		return false
	}
	if gates.InTestTree(file) {
		return true
	}
	parts := strings.Split(file, "/")
	for _, d := range parts[:len(parts)-1] {
		if d == "Tests" {
			return true
		}
	}
	return tqTestName.MatchString(parts[len(parts)-1])
}

func tqLiterals(line string) []string {
	out := []string{}
	for _, m := range tqLiteral.FindAllStringSubmatch(line, -1) {
		out = append(out, m[1]+m[2]+m[3])
	}
	return out
}

func tqHasSourceLiteral(line string) bool {
	for _, v := range tqLiterals(line) {
		if tqSourceEnd.MatchString(v) && !tqUnderFixtures(v) {
			return true
		}
	}
	return false
}

func tqHasFixtureLiteral(line string) bool {
	for _, v := range tqLiterals(line) {
		if tqUnderFixtures(v) {
			return true
		}
	}
	return false
}

func tqParenDelta(line string) int {
	bare := tqLiteral.ReplaceAllString(line, "")
	return strings.Count(bare, "(") - strings.Count(bare, ")")
}

func tqCallSpan(lines []string, i int) []string {
	line := lines[i]
	span := []string{line}
	at := -1
	for _, c := range tqReadCalls {
		if n := strings.Index(line, c); n >= 0 && (at < 0 || n < at) {
			at = n
		}
	}
	depth := 0
	if open := strings.Index(line[at:], "("); open >= 0 {
		depth = tqParenDelta(line[at+open:])
	}
	for j := i + 1; depth > 0 && j < len(lines) && j <= i+tqSpanLines; j++ {
		span = append(span, lines[j])
		depth += tqParenDelta(lines[j])
	}
	return span
}

func tqValueIsSourceLiteral(rhs string) bool {
	value := trimJS(tqSemicolon.ReplaceAllString(trimJS(rhs), ""))
	if tqWhole.MatchString(value) {
		return tqHasSourceLiteral(value)
	}
	m := tqPathCall.FindStringSubmatch(value)
	return m != nil && tqHasSourceLiteral(m[1])
}

func tqHoldsAny(line string, calls []string) bool {
	for _, c := range calls {
		if strings.Contains(line, c) {
			return true
		}
	}
	return false
}

func tqNames(set []string, line string) bool {
	for _, name := range set {
		re := regexp.MustCompile(`(?:^|[^\w$])` + regexp.QuoteMeta(name) + `(?:[^\w$]|$)`)
		if re.MatchString(line) {
			return true
		}
	}
	return false
}

func tqAdd(set []string, name string) []string {
	for _, s := range set {
		if s == name {
			return set
		}
	}
	return append(set, name)
}

// testQualityLines is test-quality.mjs testQualityLines.
func testQualityLines(file, content string) []int {
	if !tqCheckedTestFile(file) {
		return nil
	}
	lines := tqLineSplit.Split(content, -1)
	bound, fixtureBound := []string{}, []string{}
	reads, asserts := []int{}, []int{}
	for i, line := range lines {
		if tqHoldsAny(line, tqReadCalls) {
			span := tqCallSpan(lines, i)
			fixture, source := false, false
			for _, l := range span {
				if tqHasFixtureLiteral(l) || tqNames(fixtureBound, l) {
					fixture = true
				}
			}
			for _, l := range span {
				if tqHasSourceLiteral(l) || tqNames(bound, l) {
					source = true
				}
			}
			if !fixture && source {
				reads = append(reads, i+1)
			}
		}
		if tqHoldsAny(line, tqAssertCalls) {
			asserts = append(asserts, i+1)
		}
		if m := tqBinding.FindStringSubmatchIndex(line); m != nil {
			name := ""
			if m[2] >= 0 {
				name = line[m[2]:m[3]]
			} else if m[4] >= 0 {
				name = line[m[4]:m[5]]
			}
			if name != "" {
				if tqHasFixtureLiteral(line) {
					fixtureBound = tqAdd(fixtureBound, name)
				} else if tqValueIsSourceLiteral(line[m[7]:]) {
					bound = tqAdd(bound, name)
				}
			}
		}
	}
	if len(reads) == 0 || len(asserts) == 0 {
		return nil
	}
	seen := map[int]bool{}
	out := []int{}
	for _, n := range append(reads, asserts...) {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out
}

// testQualityNote is test-quality.mjs testQualityNote: ” for none.
func testQualityNote(file string, content *string) string {
	if content == nil {
		return ""
	}
	lines := testQualityLines(file, *content)
	if len(lines) == 0 {
		return ""
	}
	shown := lines
	if len(shown) > tqMaxLines {
		shown = shown[:tqMaxLines]
	}
	nums := make([]string, len(shown))
	for i, n := range shown {
		nums[i] = strconv.Itoa(n)
	}
	more := ""
	if len(lines) > len(shown) {
		more = " (" + strconv.Itoa(len(lines)-len(shown)) + " more not listed)"
	}
	return strings.Join([]string{
		"## test-quality",
		"file: " + file,
		"lines: " + strings.Join(nums, ", ") + more,
		"This test file reads a source file and asserts on its text (a hint from a text match, not a finding): check these lines and apply the lens rule on tests that read source text.",
	}, "\n")
}
