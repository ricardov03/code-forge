package review

// B537: the small JavaScript semantics the packet parts share: Node's
// path.posix.extname, the related/definitions safe-path rules, a JS regex
// token stream whose one-character token splits an astral character into its
// two UTF-16 halves (a JS regex without the u flag sees code units), and
// UTF-16 string order.

import (
	"regexp"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// jsNonWS is the JavaScript [^\s] class (jsRe cannot negate jsWS).
const jsNonWS = `[^\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]`

// jsAny is the JavaScript [\s\S] class.
const jsAny = `(?s:.)`

// jsDot is the JavaScript `.` without the s flag.
const jsDot = `[^\n\r\x{2028}\x{2029}]`

// posixExtname is Node's path.posix.extname for a path with no trailing slash.
func posixExtname(p string) string {
	base := p[strings.LastIndex(p, "/")+1:]
	idx := strings.LastIndex(base, ".")
	if idx <= 0 || base == ".." {
		return ""
	}
	return base[idx:]
}

// asciiLower is String.prototype.toLowerCase for the ASCII letters only
// (an extension Node lower-cases is compared with ASCII names).
func asciiLower(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// lowerExt is path.posix.extname(file).toLowerCase(), compared with ASCII
// extensions: a non-ASCII extension never equals one, whatever its case.
func lowerExt(file string) string {
	ext := posixExtname(file)
	for _, r := range ext {
		if r >= utf8.RuneSelf {
			return ext
		}
	}
	return asciiLower(ext)
}

var relUnsafeChar = regexp.MustCompile(`[\\\x00-\x1f\x7f]`)

// relSafePath is related.mjs safePath.
func relSafePath(file string) bool {
	if strings.HasPrefix(file, "/") || relUnsafeChar.MatchString(file) {
		return false
	}
	for _, part := range strings.Split(file, "/") {
		switch part {
		case "", ".", "..", ".git", ".code-forge":
			return false
		}
	}
	return true
}

// defSafePath is definitions.mjs safePath.
func defSafePath(file string) bool { return relSafePath(file) && !IsSecretLike(file) }

// jsToken is one regex match: its text and byte offset.
type jsToken struct {
	value string
	index int
}

// surrogateHalf encodes one UTF-16 code unit the way a lone surrogate would
// be held (three bytes, never valid UTF-8), so it equals only itself.
func surrogateHalf(u uint16) string {
	return string([]byte{0xe0 | byte(u>>12), 0x80 | byte(u>>6)&0x3f, 0x80 | byte(u)&0x3f})
}

// jsTokens is text.matchAll(re) for a JS regex without the u flag: a match
// that is one astral character (the `[^\s]` alternative) is two tokens, its
// UTF-16 halves.
func jsTokens(re *regexp.Regexp, text string) []jsToken {
	out := []jsToken{}
	for _, m := range re.FindAllStringIndex(text, -1) {
		v := text[m[0]:m[1]]
		if r, w := utf8.DecodeRuneInString(v); w == len(v) && r >= 0x10000 {
			hi, lo := utf16.EncodeRune(r)
			out = append(out, jsToken{surrogateHalf(uint16(hi)), m[0]}, jsToken{surrogateHalf(uint16(lo)), m[0]})
			continue
		}
		out = append(out, jsToken{v, m[0]})
	}
	return out
}

// lessUTF16Str is JS `a < b` for strings (UTF-16 code-unit order).
func lessUTF16Str(a, b string) bool {
	x, y := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for k := 0; k < len(x) && k < len(y); k++ {
		if x[k] != y[k] {
			return x[k] < y[k]
		}
	}
	return len(x) < len(y)
}

// utf16Units is the UTF-16 length of s[:i] (a byte offset).
func utf16Units(s string, i int) int { return utf16Len(s[:i]) }

// ciLiteral is a case-insensitive pattern for an ASCII word without (?i),
// which in Go also folds K/k with the Kelvin sign and s with the long s.
func ciLiteral(word string) string {
	var b strings.Builder
	for _, r := range word {
		lo, up := r, r
		if r >= 'a' && r <= 'z' {
			up = r - ('a' - 'A')
		}
		if lo != up {
			b.WriteString("[" + string(lo) + string(up) + "]")
		} else {
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	return b.String()
}
