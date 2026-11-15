package review

import (
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// JSONParseMessage is the message of the SyntaxError V8's JSON.parse throws
// for src (Node v24), or ok false when src parses. B720: the close's pin
// check prints it for a worker claim file that is not JSON.
func JSONParseMessage(src string) (message string, ok bool) {
	if _, err := parseV8(src); err != nil {
		return err.full, true
	}
	return "", false
}

// location is V8's CalculateFileLocation: " (line L column C)", where \r\n,
// \r and \n each end a line and the column counts UTF-16 units.
func (p *v8Parser) location(at int) string {
	line, column := 1, 1
	for i := 0; i < at; {
		c := p.src[i]
		if c == '\r' && i+1 < at && p.src[i+1] == '\n' {
			i++
			c = '\n'
		}
		if c == '\r' || c == '\n' {
			line++
			column = 1
			i++
			continue
		}
		r, w := utf8.DecodeRuneInString(p.src[i:])
		column++
		if r >= 0x10000 {
			column++
		}
		i += w
	}
	return " (line " + strconv.Itoa(line) + " column " + strconv.Itoa(column) + ")"
}

// tokenMessage is V8's unexpected-token message: the UTF-16 unit at the
// cursor, then the whole source when it is shorter than 21 units, else ten
// units of context on each side of the position. A cut surrogate pair prints
// as U+FFFD, as Node writes a lone surrogate to stderr.
func (p *v8Parser) tokenMessage(at int) string {
	units := utf16.Encode([]rune(p.src))
	pos := p.pos(at)
	token := string(utf16.Decode(units[pos : pos+1]))
	text := func(from, to int) string { return string(utf16.Decode(units[from:to])) }
	head := "Unexpected token '" + token + "', "
	n := len(units)
	switch {
	case n < 21:
		return head + `"` + p.src + `" is not valid JSON`
	case pos < 10:
		return head + `"` + text(0, pos+10) + `"... is not valid JSON`
	case pos < n-10:
		return head + `..."` + text(pos-10, pos+10) + `"... is not valid JSON`
	}
	return head + `..."` + text(pos-10, n) + `" is not valid JSON`
}
