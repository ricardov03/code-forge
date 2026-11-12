package main

// JSON the way the Node scripts read and write it: JSON.parse keeps an object's key order (and a
// repeated key keeps its first place with the last value), JSON.stringify(value, null, indent)
// writes the layout the release tool requires.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// jsObject is a JSON object with its key order.
type jsObject struct {
	keys []string
	vals map[string]any
}

func (o *jsObject) get(key string) (any, bool) {
	v, ok := o.vals[key]
	return v, ok
}

func (o *jsObject) set(key string, v any) {
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = v
}

// jsNum is a JSON number kept as a float64, like JavaScript.
type jsNum float64

// jsParse parses JSON text into nil, bool, jsNum, string, []any or *jsObject.
func jsParse(text string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	v, err := jsParseValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("Unexpected non-whitespace character after JSON")
	}
	return v, nil
}

func jsParseValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := &jsObject{vals: map[string]any{}}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := kt.(string)
				if !ok {
					return nil, errors.New("bad object key")
				}
				v, err := jsParseValue(dec)
				if err != nil {
					return nil, err
				}
				obj.set(key, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return obj, nil
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := jsParseValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return arr, nil
		}
		return nil, fmt.Errorf("unexpected %v", t)
	case json.Number:
		f, err := strconv.ParseFloat(string(t), 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return nil, err
		}
		return jsNum(f), nil
	default:
		return t, nil
	}
}

// jsField walks keys like `obj?.[k]`; ok is false when the value is undefined.
func jsField(v any, keys []string) (any, bool) {
	cur := v
	for _, k := range keys {
		switch c := cur.(type) {
		case *jsObject:
			next, ok := c.get(k)
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(k)
			if err != nil || i < 0 || i >= len(c) || strconv.Itoa(i) != k {
				return nil, false
			}
			cur = c[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

// jsNumberString is JavaScript's Number#toString for a finite number.
func jsNumberString(f float64) string {
	if math.IsNaN(f) {
		return "NaN"
	}
	if math.IsInf(f, 1) {
		return "Infinity"
	}
	if math.IsInf(f, -1) {
		return "-Infinity"
	}
	if f == 0 {
		return "0"
	}
	abs := math.Abs(f)
	if abs >= 1e-6 && abs < 1e21 {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	s := strconv.FormatFloat(f, 'e', -1, 64)
	mant, exp, _ := strings.Cut(s, "e")
	sign := exp[0]
	digits := strings.TrimLeft(exp[1:], "0")
	if digits == "" {
		digits = "0"
	}
	return mant + "e" + string(sign) + digits
}

// jsQuote is JSON.stringify for a string.
func jsQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// jsStringify is JSON.stringify(v, null, indent); indent "" is the compact form.
func jsStringify(v any, indent string) string {
	var b bytes.Buffer
	jsWrite(&b, v, indent, "")
	return b.String()
}

func jsWrite(b *bytes.Buffer, v any, indent, cur string) {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case jsNum:
		f := float64(t)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			b.WriteString("null")
		} else {
			b.WriteString(jsNumberString(f))
		}
	case string:
		b.WriteString(jsQuote(t))
	case []any:
		if len(t) == 0 {
			b.WriteString("[]")
			return
		}
		inner := cur + indent
		b.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			if indent != "" {
				b.WriteString("\n" + inner)
			}
			jsWrite(b, e, indent, inner)
		}
		if indent != "" {
			b.WriteString("\n" + cur)
		}
		b.WriteByte(']')
	case *jsObject:
		if len(t.keys) == 0 {
			b.WriteString("{}")
			return
		}
		inner := cur + indent
		b.WriteByte('{')
		for i, k := range t.keys {
			if i > 0 {
				b.WriteByte(',')
			}
			if indent != "" {
				b.WriteString("\n" + inner)
			}
			b.WriteString(jsQuote(k))
			b.WriteByte(':')
			if indent != "" {
				b.WriteByte(' ')
			}
			jsWrite(b, t.vals[k], indent, inner)
		}
		if indent != "" {
			b.WriteString("\n" + cur)
		}
		b.WriteByte('}')
	}
}

// jsTemplate is how a value prints in a JavaScript template literal (`${v}`).
func jsTemplate(v any, defined bool) string {
	if !defined {
		return "undefined"
	}
	switch t := v.(type) {
	case nil:
		return "null"
	case bool:
		return strconv.FormatBool(t)
	case jsNum:
		return jsNumberString(float64(t))
	case string:
		return t
	case []any:
		parts := make([]string, len(t))
		for i, e := range t {
			if e != nil {
				parts[i] = jsTemplate(e, true)
			}
		}
		return strings.Join(parts, ",")
	default:
		return "[object Object]"
	}
}

// jsLen is a string's length in UTF-16 code units, like JavaScript's String#length.
func jsLen(s string) int {
	n := 0
	for _, r := range s {
		n += len(utf16.Encode([]rune{r}))
	}
	return n
}
