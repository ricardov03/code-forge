package main

import (
	"regexp"
	"strings"
	"unicode/utf8"
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
