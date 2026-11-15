package yamledit

import (
	"strconv"
	"strings"
)

// B732: a string with a lone surrogate (WTF-8, as jsonx and ledger hold the
// lone UTF-16 unit JSON.parse keeps) is what `autopilot approve --value
// '"x\ud800y"'` writes. The yaml package's stringifyString makes such a
// string QUOTE_DOUBLE, and doubleQuotedString is JSON.stringify of it: `\`
// and `"` escaped, each lone surrogate a lowercase \udxxx escape. Go writes
// that for printable ASCII around the surrogates and reads it back (the
// restore at until) only when writing the value again gives the same text.

const surrogateHex = "0123456789abcdef"

// surrogateUnitAt is the lone surrogate code unit WTF-8 holds at s[i:i+3].
func surrogateUnitAt(s string, i int) (int, bool) {
	if i+2 < len(s) && s[i] == 0xED && s[i+1] >= 0xA0 && s[i+1] <= 0xBF && s[i+2] >= 0x80 && s[i+2] <= 0xBF {
		return 0xD000 | int(s[i+1]&0x3F)<<6 | int(s[i+2]&0x3F), true
	}
	return 0, false
}

// surrogateQuoted is JSON.stringify of v for printable ASCII and at least one
// lone surrogate (a high one never followed by a low one: that pair is one
// character in JavaScript), or ok false.
func surrogateQuoted(v string) (string, bool) {
	var b strings.Builder
	b.WriteByte('"')
	seen, prevHigh := false, false
	for i := 0; i < len(v); i++ {
		if unit, ok := surrogateUnitAt(v, i); ok {
			if prevHigh && unit >= 0xDC00 {
				return "", false
			}
			b.WriteString(`\u`)
			for shift := 12; shift >= 0; shift -= 4 {
				b.WriteByte(surrogateHex[unit>>shift&15])
			}
			seen, prevHigh = true, unit < 0xDC00
			i += 2
			continue
		}
		prevHigh = false
		c := v[i]
		if c < 0x20 || c > 0x7e {
			return "", false
		}
		if c == '"' || c == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	if !seen {
		return "", false
	}
	b.WriteByte('"')
	return b.String(), true
}

// surrogateUnquote reads the inside of a double-quoted scalar that holds a
// \uD800-\uDFFF escape as the yaml package does, for the escapes
// surrogateQuoted writes (\\, \" and \uXXXX); ok false for anything else or
// for a text surrogateQuoted would not write back the same.
func surrogateUnquote(inside string) (string, bool) {
	var out []byte
	for i := 0; i < len(inside); i++ {
		c := inside[i]
		if c != '\\' {
			out = append(out, c)
			continue
		}
		if i+1 >= len(inside) {
			return "", false
		}
		switch inside[i+1] {
		case '\\', '"':
			out = append(out, inside[i+1])
			i++
		case 'u':
			if i+6 > len(inside) {
				return "", false
			}
			unit, err := strconv.ParseUint(inside[i+2:i+6], 16, 16)
			if err != nil || unit < 0xD800 || unit > 0xDFFF {
				return "", false
			}
			out = append(out, 0xED, 0x80|byte(unit>>6&0x3F), 0x80|byte(unit&0x3F))
			i += 5
		default:
			return "", false
		}
	}
	v := string(out)
	if q, ok := surrogateQuoted(v); !ok || q != `"`+inside+`"` {
		return "", false
	}
	return v, true
}
