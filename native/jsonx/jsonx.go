// Package jsonx reads and writes JSON the way Node's JSON.parse and
// JSON.stringify do, including the strings Go's encoding/json cannot carry.
//
// JavaScript strings are UTF-16 and may hold a lone surrogate (JSON.parse of
// "\ud83d" keeps it; JSON.stringify writes it back as the escape \ud83d).
// encoding/json turns such an escape into U+FFFD, so a value read and written
// again changes. jsonx keeps a lone surrogate in a Go string as WTF-8: the
// three-byte generalized UTF-8 form of the code point (ED A0..BF 80..BF). A
// surrogate pair is always joined into its supplementary character, so two
// JavaScript strings are equal exactly when their jsonx Go strings are equal.
//
// Numbers are float64 as in JavaScript: a literal too large for a double is
// ±Infinity (JSON.parse("1e400") is Infinity), and Stringify writes a
// non-finite number as null. Objects are map[string]any from Parse, or Object
// (insertion order, as JSON.parse gives it) from ParseOrdered.
package jsonx

import (
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Member is one property of an ordered object.
type Member struct {
	Key   string
	Value any
}

// Object is a JSON object in property insertion order.
type Object []Member

// Get returns a property's value and whether the object has it.
func (o Object) Get(key string) (any, bool) {
	for _, m := range o {
		if m.Key == key {
			return m.Value, true
		}
	}
	return nil, false
}

// Set replaces a property in place or appends it, as JavaScript assignment.
func (o *Object) Set(key string, value any) {
	for i := range *o {
		if (*o)[i].Key == key {
			(*o)[i].Value = value
			return
		}
	}
	*o = append(*o, Member{key, value})
}

// DecodeUTF8 is the WHATWG UTF-8 decoder with replacement, the one Node uses
// for Buffer#toString('utf8'), readFile(..., 'utf8') and a readline stream:
// every maximal invalid subpart becomes one U+FFFD.
func DecodeUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var out strings.Builder
	out.Grow(len(b) + 8)
	for i := 0; i < len(b); {
		c := b[i]
		if c < 0x80 {
			out.WriteByte(c)
			i++
			continue
		}
		need, lower, upper := 0, byte(0x80), byte(0xBF)
		switch {
		case c >= 0xC2 && c <= 0xDF:
			need = 1
		case c >= 0xE0 && c <= 0xEF:
			need = 2
			if c == 0xE0 {
				lower = 0xA0
			} else if c == 0xED {
				upper = 0x9F
			}
		case c >= 0xF0 && c <= 0xF4:
			need = 3
			if c == 0xF0 {
				lower = 0x90
			} else if c == 0xF4 {
				upper = 0x8F
			}
		default:
			out.WriteString("�")
			i++
			continue
		}
		j, whole := i+1, true
		for k := 0; k < need; k++ {
			if j >= len(b) || b[j] < lower || b[j] > upper {
				whole = false
				break
			}
			lower, upper = 0x80, 0xBF
			j++
		}
		if !whole {
			// The lead and its valid continuations are one U+FFFD; the byte
			// that broke the sequence is read again.
			out.WriteString("�")
			i = j
			continue
		}
		out.Write(b[i:j])
		i = j
	}
	return out.String()
}

// surrogateAt reports the lone surrogate code unit WTF-8 holds at s[i:i+3].
func surrogateAt(s string, i int) (uint16, bool) {
	if i+2 < len(s) && s[i] == 0xED && s[i+1] >= 0xA0 && s[i+1] <= 0xBF && s[i+2] >= 0x80 && s[i+2] <= 0xBF {
		return 0xD000 | uint16(s[i+1]&0x3F)<<6 | uint16(s[i+2]&0x3F), true
	}
	return 0, false
}

func appendSurrogate(dst []byte, unit uint16) []byte {
	return append(dst, 0xED, 0x80|byte(unit>>6&0x3F), 0x80|byte(unit&0x3F))
}

// HasLoneSurrogate reports whether s holds a lone surrogate (WTF-8).
func HasLoneSurrogate(s string) bool {
	for i := 0; i+2 < len(s); i++ {
		if _, ok := surrogateAt(s, i); ok {
			return true
		}
	}
	return false
}

// ContainsLoneSurrogate reports whether a parsed value holds a lone
// surrogate in any string or object key, at any depth.
func ContainsLoneSurrogate(v any) bool {
	switch x := v.(type) {
	case string:
		return HasLoneSurrogate(x)
	case []any:
		for _, item := range x {
			if ContainsLoneSurrogate(item) {
				return true
			}
		}
	case map[string]any:
		for k, item := range x {
			if HasLoneSurrogate(k) || ContainsLoneSurrogate(item) {
				return true
			}
		}
	case Object:
		for _, m := range x {
			if HasLoneSurrogate(m.Key) || ContainsLoneSurrogate(m.Value) {
				return true
			}
		}
	}
	return false
}

// OutsideEncodingJSON reports whether a parsed value holds what Go's
// encoding/json cannot write as JSON.stringify does: a lone surrogate (in a
// string or a key) or a non-finite number, at any depth.
func OutsideEncodingJSON(v any) bool {
	switch x := v.(type) {
	case float64:
		return math.IsInf(x, 0) || math.IsNaN(x)
	case string:
		return HasLoneSurrogate(x)
	case []any:
		for _, item := range x {
			if OutsideEncodingJSON(item) {
				return true
			}
		}
	case map[string]any:
		for k, item := range x {
			if HasLoneSurrogate(k) || OutsideEncodingJSON(item) {
				return true
			}
		}
	case Object:
		for _, m := range x {
			if HasLoneSurrogate(m.Key) || OutsideEncodingJSON(m.Value) {
				return true
			}
		}
	}
	return false
}

// WellFormed is String.prototype.toWellFormed: each lone surrogate becomes
// U+FFFD. Node encodes a string this way for a file path or a Buffer.
func WellFormed(s string) string {
	if !HasLoneSurrogate(s) {
		return s
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		if _, ok := surrogateAt(s, i); ok {
			out = append(out, "�"...)
			i += 3
			continue
		}
		out = append(out, s[i])
		i++
	}
	return string(out)
}

// Units returns s as JavaScript sees it: its UTF-16 code units.
func Units(s string) []uint16 {
	units := make([]uint16, 0, len(s))
	for i := 0; i < len(s); {
		if unit, ok := surrogateAt(s, i); ok {
			units = append(units, unit)
			i += 3
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r > 0xFFFF {
			r -= 0x10000
			units = append(units, uint16(0xD800+(r>>10)), uint16(0xDC00+(r&0x3FF)))
		} else {
			units = append(units, uint16(r))
		}
		i += size
	}
	return units
}

// CompareUTF16 orders two strings by UTF-16 code units, the order of
// JavaScript's < and of Array#sort without a comparator.
func CompareUTF16(a, b string) int {
	if a == b {
		return 0
	}
	x, y := Units(a), Units(b)
	for i := 0; i < len(x) && i < len(y); i++ {
		if x[i] != y[i] {
			if x[i] < y[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(x) < len(y):
		return -1
	case len(x) > len(y):
		return 1
	}
	return 0
}

// ErrSyntax is the error of every text JSON.parse rejects.
var ErrSyntax = errors.New("jsonx: invalid JSON")

// Parse is JSON.parse of text (a JavaScript string held as UTF-8 or WTF-8):
// objects are map[string]any, arrays []any, numbers float64, a duplicate key
// keeps its last value.
func Parse(text string) (any, error) { return parse(text, false) }

// ParseOrdered is Parse with objects as Object, in JSON.parse's insertion
// order (a duplicate key keeps its first place and its last value).
func ParseOrdered(text string) (any, error) { return parse(text, true) }

func parse(text string, ordered bool) (any, error) {
	p := &parser{s: text, ordered: ordered}
	p.space()
	v, err := p.value()
	if err != nil {
		return nil, err
	}
	p.space()
	if p.i != len(p.s) {
		return nil, ErrSyntax
	}
	return v, nil
}

type parser struct {
	s       string
	i       int
	ordered bool
}

func (p *parser) space() {
	for p.i < len(p.s) {
		switch p.s[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

func (p *parser) literal(word string, v any) (any, error) {
	if strings.HasPrefix(p.s[p.i:], word) {
		p.i += len(word)
		return v, nil
	}
	return nil, ErrSyntax
}

func (p *parser) value() (any, error) {
	if p.i >= len(p.s) {
		return nil, ErrSyntax
	}
	switch c := p.s[p.i]; {
	case c == '{':
		return p.object()
	case c == '[':
		return p.array()
	case c == '"':
		return p.str()
	case c == 't':
		return p.literal("true", true)
	case c == 'f':
		return p.literal("false", false)
	case c == 'n':
		return p.literal("null", nil)
	case c == '-' || c >= '0' && c <= '9':
		return p.number()
	}
	return nil, ErrSyntax
}

func (p *parser) digits() int {
	start := p.i
	for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
		p.i++
	}
	return p.i - start
}

func (p *parser) number() (any, error) {
	start := p.i
	if p.s[p.i] == '-' {
		p.i++
	}
	if p.i < len(p.s) && p.s[p.i] == '0' {
		p.i++
	} else if p.digits() == 0 {
		return nil, ErrSyntax
	}
	if p.i < len(p.s) && p.s[p.i] == '.' {
		p.i++
		if p.digits() == 0 {
			return nil, ErrSyntax
		}
	}
	if p.i < len(p.s) && (p.s[p.i] == 'e' || p.s[p.i] == 'E') {
		p.i++
		if p.i < len(p.s) && (p.s[p.i] == '+' || p.s[p.i] == '-') {
			p.i++
		}
		if p.digits() == 0 {
			return nil, ErrSyntax
		}
	}
	f, err := strconv.ParseFloat(p.s[start:p.i], 64)
	var numErr *strconv.NumError
	if err != nil && !(errors.As(err, &numErr) && numErr.Err == strconv.ErrRange) {
		return nil, ErrSyntax
	}
	// Out of range: ParseFloat gives ±Inf (or the underflowed value), as
	// JavaScript's Number does.
	return f, nil
}

func hexValue(c byte) (uint16, bool) {
	switch {
	case c >= '0' && c <= '9':
		return uint16(c - '0'), true
	case c >= 'a' && c <= 'f':
		return uint16(c-'a') + 10, true
	case c >= 'A' && c <= 'F':
		return uint16(c-'A') + 10, true
	}
	return 0, false
}

func (p *parser) unit() (uint16, bool) {
	if p.i+4 > len(p.s) {
		return 0, false
	}
	var u uint16
	for k := 0; k < 4; k++ {
		h, ok := hexValue(p.s[p.i+k])
		if !ok {
			return 0, false
		}
		u = u<<4 | h
	}
	p.i += 4
	return u, true
}

func (p *parser) str() (string, error) {
	p.i++ // the opening quote
	start := p.i
	for p.i < len(p.s) {
		c := p.s[p.i]
		if c == '"' {
			out := p.s[start:p.i]
			p.i++
			return out, nil
		}
		if c == '\\' || c < 0x20 {
			break
		}
		p.i++
	}
	out := []byte(p.s[start:p.i])
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch {
		case c == '"':
			p.i++
			return string(out), nil
		case c < 0x20:
			return "", ErrSyntax
		case c != '\\':
			out = append(out, c)
			p.i++
			continue
		}
		p.i++
		if p.i >= len(p.s) {
			return "", ErrSyntax
		}
		e := p.s[p.i]
		p.i++
		switch e {
		case '"', '\\', '/':
			out = append(out, e)
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'u':
			u, ok := p.unit()
			if !ok {
				return "", ErrSyntax
			}
			if u >= 0xD800 && u <= 0xDBFF && strings.HasPrefix(p.s[p.i:], `\u`) {
				save := p.i
				p.i += 2
				low, ok := p.unit()
				if ok && low >= 0xDC00 && low <= 0xDFFF {
					out = utf8.AppendRune(out, 0x10000+(rune(u)-0xD800)<<10+(rune(low)-0xDC00))
					continue
				}
				p.i = save
			}
			if u >= 0xD800 && u <= 0xDFFF {
				out = appendSurrogate(out, u)
			} else {
				out = utf8.AppendRune(out, rune(u))
			}
		default:
			return "", ErrSyntax
		}
	}
	return "", ErrSyntax
}

func (p *parser) array() (any, error) {
	p.i++
	list := []any{}
	p.space()
	if p.i < len(p.s) && p.s[p.i] == ']' {
		p.i++
		return list, nil
	}
	for {
		p.space()
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		list = append(list, v)
		p.space()
		if p.i >= len(p.s) {
			return nil, ErrSyntax
		}
		switch p.s[p.i] {
		case ',':
			p.i++
		case ']':
			p.i++
			return list, nil
		default:
			return nil, ErrSyntax
		}
	}
}

func (p *parser) object() (any, error) {
	p.i++
	var m map[string]any
	var o Object
	if p.ordered {
		o = Object{}
	} else {
		m = map[string]any{}
	}
	result := func() any {
		if p.ordered {
			return o
		}
		return m
	}
	p.space()
	if p.i < len(p.s) && p.s[p.i] == '}' {
		p.i++
		return result(), nil
	}
	for {
		p.space()
		if p.i >= len(p.s) || p.s[p.i] != '"' {
			return nil, ErrSyntax
		}
		key, err := p.str()
		if err != nil {
			return nil, err
		}
		p.space()
		if p.i >= len(p.s) || p.s[p.i] != ':' {
			return nil, ErrSyntax
		}
		p.i++
		p.space()
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		if p.ordered {
			o.Set(key, v)
		} else {
			m[key] = v
		}
		p.space()
		if p.i >= len(p.s) {
			return nil, ErrSyntax
		}
		switch p.s[p.i] {
		case ',':
			p.i++
		case '}':
			p.i++
			return result(), nil
		default:
			return nil, ErrSyntax
		}
	}
}

// Number is ECMAScript Number::toString(10) for a finite number: the
// shortest digits that read back the same, in decimal notation from 1e-6 up
// to (not including) 1e21 and exponent notation outside it; -0 is "0".
func Number(f float64) string {
	if f == 0 {
		return "0"
	}
	if math.IsNaN(f) {
		return "NaN"
	}
	if math.IsInf(f, 1) {
		return "Infinity"
	}
	if math.IsInf(f, -1) {
		return "-Infinity"
	}
	if a := math.Abs(f); a >= 1e-6 && a < 1e21 {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	text := strconv.FormatFloat(f, 'e', -1, 64)
	at := strings.IndexByte(text, 'e')
	return text[:at+2] + strings.TrimLeft(text[at+2:], "0")
}

// Quote is JSON.stringify of a string: the short escapes, \u00XX for the
// other control characters, \udXXX for a lone surrogate (lower-case hex),
// every other character as it is.
func Quote(s string) string { return string(appendQuote(nil, s)) }

func appendQuote(dst []byte, s string) []byte {
	const hex = "0123456789abcdef"
	dst = append(dst, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"', '\\':
			dst = append(dst, '\\', c)
			continue
		case '\b':
			dst = append(dst, '\\', 'b')
			continue
		case '\t':
			dst = append(dst, '\\', 't')
			continue
		case '\n':
			dst = append(dst, '\\', 'n')
			continue
		case '\f':
			dst = append(dst, '\\', 'f')
			continue
		case '\r':
			dst = append(dst, '\\', 'r')
			continue
		}
		if c < 0x20 {
			dst = append(dst, '\\', 'u', '0', '0', hex[c>>4], hex[c&15])
			continue
		}
		if unit, ok := surrogateAt(s, i); ok {
			dst = append(dst, '\\', 'u', hex[unit>>12], hex[unit>>8&15], hex[unit>>4&15], hex[unit&15])
			i += 2
			continue
		}
		dst = append(dst, c)
	}
	return append(dst, '"')
}

// ErrUnsupported is Stringify's error for a Go value with no JSON form.
var ErrUnsupported = errors.New("jsonx: value has no JSON form")

// arrayIndex is an ECMAScript array index key ("0" to "4294967294").
func arrayIndex(key string) (uint64, bool) {
	n, err := strconv.ParseUint(key, 10, 32)
	return n, err == nil && n < 4294967295 && strconv.FormatUint(n, 10) == key
}

// propertyOrder is Object.keys order: array index keys ascending first, then
// the rest in their given order.
func propertyOrder(keys []string) []string {
	out := append([]string{}, keys...)
	sort.SliceStable(out, func(i, j int) bool {
		a, isA := arrayIndex(out[i])
		b, isB := arrayIndex(out[j])
		if isA != isB {
			return isA
		}
		return isA && a < b
	})
	return out
}

// Stringify is JSON.stringify(v) with no indent. Object members follow
// Object.keys order; a map has no insertion order, so its other keys follow
// UTF-16 order. A non-finite number is null.
func Stringify(v any) (string, error) {
	out, err := appendValue(nil, v, false)
	return string(out), err
}

// Canonical is Node's canonicalJSON (src/state/signer.mjs): JSON.stringify
// with the keys of every object sorted by UTF-16 code units, the bytes a row
// MAC covers.
func Canonical(v any) (string, error) {
	out, err := appendValue(nil, v, true)
	return string(out), err
}

func appendValue(dst []byte, v any, canonical bool) ([]byte, error) {
	switch x := v.(type) {
	case nil:
		return append(dst, "null"...), nil
	case bool:
		return strconv.AppendBool(dst, x), nil
	case float64:
		if math.IsInf(x, 0) || math.IsNaN(x) {
			return append(dst, "null"...), nil
		}
		return append(dst, Number(x)...), nil
	case int:
		return strconv.AppendInt(dst, int64(x), 10), nil
	case int64:
		return strconv.AppendInt(dst, x, 10), nil
	case string:
		return appendQuote(dst, x), nil
	case []string:
		dst = append(dst, '[')
		for i, item := range x {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = appendQuote(dst, item)
		}
		return append(dst, ']'), nil
	case []any:
		dst = append(dst, '[')
		for i, item := range x {
			if i > 0 {
				dst = append(dst, ',')
			}
			var err error
			if dst, err = appendValue(dst, item, canonical); err != nil {
				return nil, err
			}
		}
		return append(dst, ']'), nil
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return CompareUTF16(keys[i], keys[j]) < 0 })
		if !canonical {
			keys = propertyOrder(keys)
		}
		return appendMembers(dst, keys, func(k string) any { return x[k] }, canonical)
	case Object:
		keys := make([]string, len(x))
		values := make(map[string]any, len(x))
		for i, m := range x {
			keys[i] = m.Key
			values[m.Key] = m.Value
		}
		if canonical {
			sort.SliceStable(keys, func(i, j int) bool { return CompareUTF16(keys[i], keys[j]) < 0 })
		} else {
			keys = propertyOrder(keys)
		}
		return appendMembers(dst, keys, func(k string) any { return values[k] }, canonical)
	}
	return nil, ErrUnsupported
}

func appendMembers(dst []byte, keys []string, get func(string) any, canonical bool) ([]byte, error) {
	dst = append(dst, '{')
	for i, k := range keys {
		if i > 0 {
			dst = append(dst, ',')
		}
		dst = appendQuote(dst, k)
		dst = append(dst, ':')
		var err error
		if dst, err = appendValue(dst, get(k), canonical); err != nil {
			return nil, err
		}
	}
	return append(dst, '}'), nil
}
