package main

import (
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/ledger"
)

// errorLogNormalize is Node's normalizeMessage (src/util/error-log.mjs): the
// text an error fingerprint hashes, with quoted values, placeholders, paths,
// dates, clock times, hex ids and long numbers taken out.
func errorLogNormalize(message string) string {
	s := errorLogDropQuotes(message)
	s = errorLogPlaceholders.ReplaceAllString(s, " ")
	s = errorLogDropPaths(s)
	s = errorLogISODate.ReplaceAllString(s, " ")
	s = errorLogClock.ReplaceAllString(s, " ")
	s = errorLogHexWord.ReplaceAllStringFunc(s, func(word string) string {
		if strings.ContainsAny(word, "0123456789") {
			return " "
		}
		return word
	})
	s = errorLogDropNumbers(s)
	var out strings.Builder
	space := false
	for _, r := range s {
		if errorLogSpace(r) {
			space = true
			continue
		}
		if space {
			out.WriteByte(' ')
			space = false
		}
		out.WriteRune(r)
	}
	if space {
		out.WriteByte(' ')
	}
	return strings.TrimFunc(out.String(), errorLogSpace)
}

var errorLogPlaceholders = regexp.MustCompile(`<[^<>\n]*>`)
var errorLogISODate = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}(?:[T ]\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?)?\b`)
var errorLogClock = regexp.MustCompile(`\b\d{1,2}:\d{2}(?::\d{2}(?:\.\d+)?)?\b`)

// Without a lookahead: a whole hex word (word boundaries both sides) of 7 or
// more characters that holds a digit; no shorter match can start or end inside it.
var errorLogHexWord = regexp.MustCompile(`(?i)\b[0-9a-f]{7,}\b`)

// errorLogSpace is the JavaScript \s set (and String.prototype.trim's).
func errorLogSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

func errorLogAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// /"[^"\n]*"|(?<![A-Za-z0-9])'[^'\n]*'|`[^`\n]*`/g → ' '
func errorLogDropQuotes(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		if c == '"' || c == '`' || c == '\'' && (i == 0 || !errorLogAlnum(s[i-1])) {
			end := strings.IndexAny(s[i+1:], string(c)+"\n")
			if end >= 0 && s[i+1+end] == c {
				out.WriteByte(' ')
				i += end + 2
				continue
			}
		}
		out.WriteByte(c)
		i++
	}
	return out.String()
}

// /\S*\/\S*/g → ' ' with JavaScript's \s: every maximal non-space run holding a slash.
func errorLogDropPaths(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if errorLogSpace(r) {
			out.WriteString(s[i : i+size])
			i += size
			continue
		}
		j := i
		for j < len(s) {
			r, size := utf8.DecodeRuneInString(s[j:])
			if errorLogSpace(r) {
				break
			}
			j += size
		}
		if strings.Contains(s[i:j], "/") {
			out.WriteByte(' ')
		} else {
			out.WriteString(s[i:j])
		}
		i = j
	}
	return out.String()
}

func errorLogWord(c byte) bool { return errorLogAlnum(c) || c == '_' }

// /(?<![A-Za-z0-9_.])\d{4,}(?:\.\d+)?(?![A-Za-z0-9_])/g → ' ' (with the regex's
// backtracking: a fraction followed by a word character drops back to the integer).
func errorLogDropNumbers(s string) string {
	digit := func(c byte) bool { return c >= '0' && c <= '9' }
	var out strings.Builder
	for i := 0; i < len(s); {
		if !digit(s[i]) || i > 0 && (errorLogWord(s[i-1]) || s[i-1] == '.') {
			out.WriteByte(s[i])
			i++
			continue
		}
		j := i
		for j < len(s) && digit(s[j]) {
			j++
		}
		end := -1
		if j-i >= 4 {
			if j+1 < len(s) && s[j] == '.' && digit(s[j+1]) {
				k := j + 1
				for k < len(s) && digit(s[k]) {
					k++
				}
				if k == len(s) || !errorLogWord(s[k]) {
					end = k
				} else {
					end = j
				}
			} else if j == len(s) || !errorLogWord(s[j]) {
				end = j
			}
		}
		if end < 0 {
			out.WriteString(s[i:j])
			i = j
			continue
		}
		out.WriteByte(' ')
		i = end
	}
	return out.String()
}

// errorLogTrim is String.prototype.trim: JavaScript's \s set from both ends
// (U+FEFF, NBSP and \v go; U+0085 stays).
func errorLogTrim(s string) string { return strings.TrimFunc(s, errorLogSpace) }

// errorLogSpaceClass is JavaScript's \s inside a character class (RE2's \s
// is ASCII only).
const errorLogSpaceClass = `\t\n\x{0B}\f\r \x{A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}`

// errorLogScrubRules is SCRUB_RULES' order: the order counts() lists rules in.
var errorLogScrubRules = []string{"home", "project_path", "project_name", "email", "op_ref", "item_id"}

var errorLogOpRef = regexp.MustCompile("op://(?:[^/\\r\\n\"'`<>]+/[^/\\r\\n\"'`<>]+/)?[^" + errorLogSpaceClass + "\"'`<>)\\]]+")
var errorLogEmail = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)
var errorLogItemID = regexp.MustCompile(`[a-z0-9]{26}`)

// errorLogJSLen is a string's JavaScript length (UTF-16 code units).
func errorLogJSLen(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r > 0xFFFF {
			n++ // a surrogate pair
		}
	}
	return n
}

// errorLogPathVariants is pathVariants: the path without trailing slashes or
// backslashes, plus its real path when realpathSync succeeds; 2+ characters,
// longest first.
func errorLogPathVariants(p string) []string {
	if errorLogJSLen(p) < 2 {
		return nil
	}
	out := []string{strings.TrimRight(p, "/\\")}
	if abs, err := filepath.Abs(p); err == nil {
		if real, err := filepath.EvalSymlinks(abs); err == nil && real != out[0] {
			out = append(out, real)
		}
	}
	kept := out[:0]
	for _, v := range out {
		if errorLogJSLen(v) >= 2 {
			kept = append(kept, v)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return errorLogJSLen(kept[i]) > errorLogJSLen(kept[j]) })
	return kept
}

// errorLogScrubber is createScrubber in src/util/error-log.mjs: op:// refs,
// the working directory (when not a home form), the home directory, emails,
// the project slug as a whole word and 26-character item IDs, each pass a
// global JavaScript replace (a match a lookaround refuses is retried one
// character on). counts() lists the replacements in SCRUB_RULES order.
func errorLogScrubber(home, cwd, slug string) (func(string) string, func() []any) {
	type step struct {
		rule, to string
		re       *regexp.Regexp
		behind   func(byte) bool   // (?<![...]): the byte before the match
		ahead    func(string) bool // (?!...): the text after the match
	}
	alnum := func(c byte) bool { return errorLogAlnum(c) }
	homes := errorLogPathVariants(home)
	type pathForm struct{ rule, path, to string }
	paths := []pathForm{}
	for _, c := range errorLogPathVariants(cwd) {
		if !slices.Contains(homes, c) {
			paths = append(paths, pathForm{"project_path", c, "<project>"})
		}
	}
	for _, h := range homes {
		paths = append(paths, pathForm{"home", h, "~"})
	}
	sort.SliceStable(paths, func(i, j int) bool { return errorLogJSLen(paths[i].path) > errorLogJSLen(paths[j].path) })
	steps := []step{{rule: "op_ref", to: "op://<ref>", re: errorLogOpRef}}
	for _, p := range paths {
		steps = append(steps, step{rule: p.rule, to: p.to, re: regexp.MustCompile(regexp.QuoteMeta(p.path)), ahead: func(rest string) bool {
			return rest != "" && (alnum(rest[0]) || rest[0] == '_' || rest[0] == '-')
		}})
	}
	steps = append(steps, step{rule: "email", to: "<email>", re: errorLogEmail})
	if slug != "" {
		steps = append(steps, step{rule: "project_name", to: "<slug>", re: regexp.MustCompile(regexp.QuoteMeta(slug)),
			behind: func(c byte) bool { return alnum(c) || c == '_' || c == '<' || c == '-' },
			ahead: func(rest string) bool {
				return rest != "" && (alnum(rest[0]) || rest[0] == '_' || rest[0] == '>' || rest[0] == '-') || strings.HasPrefix(rest, "://")
			}})
	}
	steps = append(steps, step{rule: "item_id", to: "<item-id>", re: errorLogItemID, behind: alnum, ahead: func(rest string) bool {
		return rest != "" && alnum(rest[0])
	}})
	tally := map[string]int{}
	clean := func(s string) string {
		for _, st := range steps {
			var out strings.Builder
			copied, at := 0, 0
			for at <= len(s) {
				loc := st.re.FindStringIndex(s[at:])
				if loc == nil {
					break
				}
				a, b := at+loc[0], at+loc[1]
				if st.behind != nil && a > 0 && st.behind(s[a-1]) || st.ahead != nil && st.ahead(s[b:]) {
					at = a + 1
					continue
				}
				out.WriteString(s[copied:a])
				out.WriteString(st.to)
				tally[st.rule]++
				copied, at = b, b
			}
			if copied > 0 {
				out.WriteString(s[copied:])
				s = out.String()
			}
		}
		return s
	}
	counts := func() []any {
		out := []any{}
		for _, rule := range errorLogScrubRules {
			if tally[rule] > 0 {
				out = append(out, ledger.Object{{Key: "rule", Value: rule}, {Key: "count", Value: tally[rule]}})
			}
		}
		return out
	}
	return clean, counts
}
