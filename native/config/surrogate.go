package config

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// B732: a double-quoted YAML scalar may hold a \uD800-\uDFFF escape. The yaml
// package (Node) reads it into a JavaScript string: a lone escape stays a lone
// UTF-16 unit, a high escape followed by a low escape is one character. It
// writes a lone surrogate back the same way ("\ud800", JSON.stringify's
// escape), so `autopilot approve --value '"x\ud800y"'` leaves such a scalar
// in .code-forge.yml. yaml.v3 refuses the escape ("found invalid Unicode
// character escape code").
//
// parseSurrogates reads such a source the way Node does: each \udXXX text is
// swapped for a supplementary private-use escape, \U000Fnnnn, that yaml.v3
// reads; every string the document yields then gets back what stood there:
// the private-use character becomes the surrogate (WTF-8, jsonx's form of a
// lone surrogate; an adjacent high and low pair is joined into its
// character), and the escape text itself (outside a double-quoted scalar,
// where nothing is unescaped) becomes the original \udXXX text again.

var surrogateEscapeText = regexp.MustCompile(`\\u[dD][89a-fA-F][0-9a-fA-F]{2}`)

const (
	surrogateSlotBase = 0xF0000
	surrogateSlotMax  = 0xFFFD
)

type surrogateSlots struct {
	texts []string // the original \udXXX text of each slot
	units []uint16 // its surrogate code unit
}

// substituteSurrogates is the source with each \udXXX text in a slot, or
// ok false for a source this cannot read without doubt: none of them, more
// than the slots, a private-use character of the slot range or slot escape
// text already there, or a tag (a !!binary or !!timestamp of the text).
func substituteSurrogates(source string) (string, *surrogateSlots, bool) {
	matches := surrogateEscapeText.FindAllStringIndex(source, -1)
	if len(matches) == 0 || len(matches) > surrogateSlotMax || strings.Contains(source, "!!") || strings.Contains(strings.ToUpper(source), `\U000F`) {
		return "", nil, false
	}
	for _, r := range source {
		if r >= surrogateSlotBase && r <= 0xFFFFF {
			return "", nil, false
		}
	}
	slots := &surrogateSlots{}
	var out strings.Builder
	last := 0
	for i, m := range matches {
		text := source[m[0]:m[1]]
		unit, _ := strconv.ParseUint(text[2:], 16, 16)
		slots.texts = append(slots.texts, text)
		slots.units = append(slots.units, uint16(unit))
		out.WriteString(source[last:m[0]])
		fmt.Fprintf(&out, `\U000F%04X`, i)
		last = m[1]
	}
	out.WriteString(source[last:])
	return out.String(), slots, true
}

// restore is a string of the substituted document as Node reads it.
func (s *surrogateSlots) restore(text string) string {
	if !strings.Contains(text, `\U000F`) && !strings.ContainsFunc(text, func(r rune) bool { return r >= surrogateSlotBase && r <= 0xFFFFF }) {
		return text
	}
	var units []rune // code points, surrogates as -unit until joined
	out := make([]byte, 0, len(text))
	flush := func() {
		for i := 0; i < len(units); i++ {
			u := -units[i]
			if u >= 0xD800 && u <= 0xDBFF && i+1 < len(units) && -units[i+1] >= 0xDC00 && -units[i+1] <= 0xDFFF {
				out = utf8.AppendRune(out, 0x10000+(u-0xD800)<<10+(-units[i+1]-0xDC00))
				i++
				continue
			}
			out = append(out, 0xED, 0x80|byte(u>>6&0x3F), 0x80|byte(u&0x3F))
		}
		units = units[:0]
	}
	for i := 0; i < len(text); {
		if strings.HasPrefix(text[i:], `\U000F`) && i+10 <= len(text) {
			slot, err := strconv.ParseUint(text[i+6:i+10], 16, 32)
			if err == nil && int(slot) < len(s.texts) && fmt.Sprintf("%04X", slot) == text[i+6:i+10] {
				flush()
				out = append(out, s.texts[slot]...)
				i += 10
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(text[i:])
		if r >= surrogateSlotBase && int(r-surrogateSlotBase) < len(s.units) {
			units = append(units, -rune(s.units[r-surrogateSlotBase]))
			i += size
			continue
		}
		flush()
		out = append(out, text[i:i+size]...)
		i += size
	}
	flush()
	return string(out)
}
