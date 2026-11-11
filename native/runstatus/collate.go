package runstatus

import "strings"

// collationOrder is the ICU root primary order of printable ASCII (the order
// Node's localeCompare uses); the digit run of a numeric collation sorts where
// the digits sit, by value.
const collationOrder = " _-,;:!?.'\"()[]{}@*/\\&#%`^+<=>|~$0123456789abcdefghijklmnopqrstuvwxyz"

type collationElement struct {
	weight int
	digits string // the digit run without leading zeros; weight is the digit group
	upper  bool
}

func collationElements(s string) ([]collationElement, bool) {
	out := []collationElement{}
	for i := 0; i < len(s); {
		c := s[i]
		if c < 0x20 || c > 0x7e {
			return nil, false
		}
		if c >= '0' && c <= '9' {
			j := i
			for j < len(s) && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			out = append(out, collationElement{weight: strings.IndexByte(collationOrder, '0'), digits: strings.TrimLeft(s[i:j], "0")})
			i = j
			continue
		}
		upper := c >= 'A' && c <= 'Z'
		if upper {
			c += 'a' - 'A'
		}
		out = append(out, collationElement{weight: strings.IndexByte(collationOrder, c), upper: upper})
		i++
	}
	return out, true
}

// blockIDCompare is a.localeCompare(b, 'en', {numeric: true}) || code unit
// order, for printable ASCII ids. ok is false for any other character.
func blockIDCompare(a, b string) (int, bool) {
	ea, okA := collationElements(a)
	eb, okB := collationElements(b)
	if !okA || !okB {
		return 0, false
	}
	for i := 0; i < len(ea) && i < len(eb); i++ {
		x, y := ea[i], eb[i]
		if x.weight != y.weight {
			if x.weight < y.weight {
				return -1, true
			}
			return 1, true
		}
		if x.digits != y.digits || x.weight == strings.IndexByte(collationOrder, '0') {
			if len(x.digits) != len(y.digits) {
				if len(x.digits) < len(y.digits) {
					return -1, true
				}
				return 1, true
			}
			if c := strings.Compare(x.digits, y.digits); c != 0 {
				return c, true
			}
		}
	}
	if len(ea) != len(eb) {
		if len(ea) < len(eb) {
			return -1, true
		}
		return 1, true
	}
	// Equal primary weights: the first case difference decides, lower case first.
	for i := range ea {
		if ea[i].upper != eb[i].upper {
			if !ea[i].upper {
				return -1, true
			}
			return 1, true
		}
	}
	return strings.Compare(a, b), true
}
