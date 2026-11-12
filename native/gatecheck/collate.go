package gatecheck

import "strings"

// collationOrder is the ICU root order of printable ASCII, the order Node's
// String.prototype.localeCompare uses: punctuation is not ignorable, letters
// compare without case first and lower case wins the tie.
const collationOrder = " _-,;:!?.'\"()[]{}@*/\\&#%`^+<=>|~$0123456789abcdefghijklmnopqrstuvwxyz"

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
		case r >= 0x20 && r <= 0x7e:
			out = append(out, base(byte(r)))
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
