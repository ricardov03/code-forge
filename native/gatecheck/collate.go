package gatecheck

import (
	"os"
	"strings"
)

// collationOrder is the ICU root order of printable ASCII, the order Node's
// String.prototype.localeCompare uses: punctuation is not ignorable, letters
// compare without case first and lower case wins the tie. B720: the
// whitespace controls TAB to CR come first; the other C0 controls and DEL
// are completely ignorable (both measured with Node v24.22.1, ICU 78.3).
const collationOrder = "\t\n\v\f\r _-,;:!?.'\"()[]{}@*/\\&#%`^+<=>|~$0123456789abcdefghijklmnopqrstuvwxyz"

// collationElement is one ICU collation element: a primary weight (0 for a
// combining mark), a secondary weight (0 is the common weight of a base
// character) and a tertiary weight (1 for upper case).
type collationElement struct{ primary, secondary, tertiary int }

// collationElements expands s for localeCompare. B498: besides printable
// ASCII it reads the precomposed Latin letters in latinLetters as their NFD
// form, a base letter then its marks. ok is false for any other character.
func collationElements(s string) ([]collationElement, bool) {
	out := make([]collationElement, 0, len(s))
	base := func(c byte) collationElement {
		e := collationElement{}
		if c >= 'A' && c <= 'Z' {
			e.tertiary = 1
			c += 'a' - 'A'
		}
		e.primary = strings.IndexByte(collationOrder, c) + 1
		return e
	}
	for _, r := range s {
		switch {
		case r >= 0x09 && r <= 0x0d || r >= 0x20 && r <= 0x7e:
			out = append(out, base(byte(r)))
		case r < 0x20 || r == 0x7f:
			// completely ignorable
		case latinLetters[r] != "":
			for i, m := range latinLetters[r] {
				if i == 0 {
					out = append(out, base(byte(m)))
				} else {
					out = append(out, collationElement{secondary: latinMarks[m]})
				}
			}
		default:
			return nil, false
		}
	}
	return out, true
}

// localeCompare is a.localeCompare(b) (ICU root, tertiary strength) for the
// characters collationElements reads: primaries first, then the secondary
// weights of every element in order, then the tertiary weights. ok is false
// when either string holds another character.
func localeCompare(a, b string) (int, bool) {
	ea, okA := collationElements(a)
	eb, okB := collationElements(b)
	if !okA || !okB {
		return 0, false
	}
	level := func(es []collationElement, get func(collationElement) int, skipZero bool) []int {
		out := make([]int, 0, len(es))
		for _, e := range es {
			if w := get(e); !skipZero || w != 0 {
				out = append(out, w)
			}
		}
		return out
	}
	for i, get := range []func(collationElement) int{
		func(e collationElement) int { return e.primary },
		func(e collationElement) int { return e.secondary },
		func(e collationElement) int { return e.tertiary },
	} {
		x, y := level(ea, get, i == 0), level(eb, get, i == 0)
		for j := 0; j < len(x) && j < len(y); j++ {
			if x[j] != y[j] {
				if x[j] < y[j] {
					return -1, true
				}
				return 1, true
			}
		}
		if len(x) != len(y) {
			if len(x) < len(y) {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

// localeLess is a.localeCompare(b) < 0. ok is false when either string holds
// a character collationElements does not read.
func localeLess(a, b string) (less, ok bool) {
	c, ok := localeCompare(a, b)
	return c < 0, ok
}

// B720: rootLikeLanguages are the languages whose ICU collation orders every
// ASCII string as the root order does (measured with Node v24.22.1, ICU 78.3,
// over all two-character ASCII strings and 20000 random ones, with and
// without a region). Danish, Norwegian, Turkish, Czech and others differ.
var rootLikeLanguages = map[string]bool{}

func init() {
	for _, l := range strings.Fields("af ar be bg bn ca de el en eo es fa fi fr ga gl gu he hi hy id is it ja ka kk km kn ko ky lo mk ml mn mr ms my ne nl or pa pl ps pt ro ru si sl sr sv sw ta te tk uk ur vi zh zu") {
		rootLikeLanguages[l] = true
	}
}

// collationEnv is the environment Node's default locale comes from; tests
// replace it.
var collationEnv = os.LookupEnv

// defaultCollation reads Node's default locale as ICU does on POSIX: the
// first of LC_ALL, LC_MESSAGES and LANG that is set (LC_COLLATE and
// LANGUAGE play no part). root is true for an unset or empty value, C,
// POSIX and their .codeset forms (ICU's en-US or und, both the root order);
// ascii is true when the language orders ASCII as root does.
func defaultCollation() (root, ascii bool) {
	value, set := "", false
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v, ok := collationEnv(name); ok {
			value, set = v, true
			break
		}
	}
	if !set || value == "" {
		return true, true
	}
	if strings.Contains(value, "@") {
		return false, false
	}
	lang := value
	if i := strings.IndexAny(lang, "_.-"); i >= 0 {
		lang = lang[:i]
	}
	if lang == "C" || lang == "POSIX" {
		return true, true
	}
	return lang == "en", rootLikeLanguages[lang]
}

// LocaleCompare is a.localeCompare(b) in Node's default locale: -1, 0 or 1.
// ok is false when Go cannot prove the answer: a character collationElements
// does not read, a precomposed Latin letter under a locale other than English
// or root, or ASCII under a locale not measured to order it as root does.
// Equal strings always compare 0.
func LocaleCompare(a, b string) (int, bool) {
	if a == b {
		return 0, true
	}
	root, ascii := defaultCollation()
	if !ascii {
		return 0, false
	}
	if !root && (!isASCII(a) || !isASCII(b)) {
		return 0, false
	}
	return localeCompare(a, b)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
