package gatecheck

import "strings"

// collationOrder is the ICU root order of printable ASCII, the order Node's
// String.prototype.localeCompare uses: punctuation is not ignorable, letters
// compare without case first and lower case wins the tie.
const collationOrder = " _-,;:!?.'\"()[]{}@*/\\&#%`^+<=>|~$0123456789abcdefghijklmnopqrstuvwxyz"

// localeLess is a.localeCompare(b) < 0 for printable ASCII strings. ok is
// false when either string holds another character.
func localeLess(a, b string) (less, ok bool) {
	primary := func(c byte) int {
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		return strings.IndexByte(collationOrder, c)
	}
	for _, s := range []string{a, b} {
		for i := 0; i < len(s); i++ {
			if s[i] < 0x20 || s[i] > 0x7e {
				return false, false
			}
		}
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		if pa, pb := primary(a[i]), primary(b[i]); pa != pb {
			return pa < pb, true
		}
	}
	if len(a) != len(b) {
		return len(a) < len(b), true
	}
	for i := 0; i < len(a); i++ {
		if a[i] != b[i] {
			// Equal primary weights differ only in case: lower case first.
			return a[i] >= 'a', true
		}
	}
	return false, true
}
