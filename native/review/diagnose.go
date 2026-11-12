package review

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/ledger"
)

// Port of diagnoseAnswer (src/review/schema-fallback.mjs, B55): the
// structural diagnosis of a schema miss, never the answer's text. A non-JSON
// answer is described by V8's JSON.parse error (cut as plainParseError cuts
// it); a JSON one by its top-level keys and the finding-schema errors in
// Ajv's order (allErrors).

const (
	diagnosisMaxErrors = 20
	diagnosisMaxKeys   = 30
	diagnosisMessage   = 120
)

var plainKeyRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,40}$`)

// jsonTypeOf is schema-fallback.mjs jsonType.
func jsonTypeOf(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case []any:
		return "array"
	case float64:
		if !math.IsInf(x, 0) && !math.IsNaN(x) && x == math.Trunc(x) {
			return "integer"
		}
		return "number"
	case string:
		return "string"
	case bool:
		return "boolean"
	}
	return "object"
}

// jsOrder is Object.entries order: array-index keys first, ascending.
func jsOrder(o ledger.Object) ledger.Object {
	idx := ledger.Object{}
	rest := ledger.Object{}
	for _, f := range o {
		if _, ok := arrayIndexKey(f.Key); ok {
			idx = append(idx, f)
		} else {
			rest = append(rest, f)
		}
	}
	for i := 1; i < len(idx); i++ {
		for j := i; j > 0; j-- {
			a, _ := arrayIndexKey(idx[j-1].Key)
			b, _ := arrayIndexKey(idx[j].Key)
			if a <= b {
				break
			}
			idx[j-1], idx[j] = idx[j], idx[j-1]
		}
	}
	return append(idx, rest...)
}

func arrayIndexKey(key string) (uint64, bool) {
	n, err := strconv.ParseUint(key, 10, 32)
	return n, err == nil && n < 4294967295 && strconv.FormatUint(n, 10) == key
}

// schemaError is one Ajv error, already reduced to the kept params.
type schemaError struct {
	path, keyword, message string
	params                 ledger.Object
}

func (s *schemaNode) errorsAt(v any, path string, out *[]schemaError) {
	typeErr := func(t string) {
		*out = append(*out, schemaError{path, "type", "must be " + t, ledger.Object{{Key: "type", Value: t}}})
	}
	switch s.typ {
	case "object":
		o, ok := v.(ledger.Object)
		if !ok {
			typeErr("object")
			return
		}
		for _, name := range s.required {
			if _, present := lookup(o, name); !present {
				*out = append(*out, schemaError{path, "required", "must have required property '" + name + "'", ledger.Object{{Key: "missingProperty", Value: plainKeyOf(name)}}})
			}
		}
		if s.closed {
			for _, f := range jsOrder(o) {
				known := false
				for _, p := range s.props {
					if p.name == f.Key {
						known = true
					}
				}
				if !known {
					*out = append(*out, schemaError{path, "additionalProperties", "must NOT have additional properties", ledger.Object{{Key: "additionalProperty", Value: plainKeyOf(f.Key)}}})
				}
			}
		}
		for _, p := range s.props {
			if child, present := lookup(o, p.name); present {
				p.node.errorsAt(child, path+"/"+p.name, out)
			}
		}
	case "array":
		arr, ok := v.([]any)
		if !ok {
			typeErr("array")
			return
		}
		for i, item := range arr {
			s.items.errorsAt(item, path+"/"+strconv.Itoa(i), out)
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			typeErr("boolean")
		}
	case "integer":
		n, isNum := v.(float64)
		if !isNum || n != math.Trunc(n) || math.IsInf(n, 0) {
			typeErr("integer")
		}
		if isNum && s.minimum != nil && !(n >= *s.minimum) {
			*out = append(*out, schemaError{path, "minimum", "must be >= " + jsNumber(*s.minimum), ledger.Object{{Key: "limit", Value: *s.minimum}, {Key: "comparison", Value: ">="}}})
		}
	case "string":
		x, ok := v.(string)
		if !ok {
			typeErr("string")
		} else {
			n := codePoints(x)
			if s.maxLength >= 0 && n > s.maxLength {
				*out = append(*out, schemaError{path, "maxLength", "must NOT have more than " + strconv.Itoa(s.maxLength) + " characters", ledger.Object{{Key: "limit", Value: float64(s.maxLength)}}})
			}
			if n < s.minLength {
				*out = append(*out, schemaError{path, "minLength", "must NOT have fewer than " + strconv.Itoa(s.minLength) + " characters", ledger.Object{{Key: "limit", Value: float64(s.minLength)}}})
			}
		}
		if len(s.enum) > 0 {
			match := false
			for _, e := range s.enum {
				if ok && e == x {
					match = true
				}
			}
			if !match {
				// allowedValues is not a kept param
				*out = append(*out, schemaError{path, "enum", "must be equal to one of the allowed values", ledger.Object{}})
			}
		}
	}
}

func plainKeyOf(key string) string {
	if plainKeyRe.MatchString(key) {
		return key
	}
	return "*"
}

// DiagnoseAnswer is schema-fallback.mjs diagnoseAnswer for a session result.
func DiagnoseAnswer(res ledger.Object) ledger.Object {
	text := ""
	if s, ok := get(res, "text").(string); ok && s != "" {
		text = s
	} else if s, ok := get(res, "answer").(string); ok {
		text = s
	}
	bytes := float64(len(text))
	value := get(res, "answer")
	if _, isString := value.(string); isString {
		value = nil
	}
	if value == nil {
		if trimJS(text) == "" {
			return ledger.Object{{Key: "answer_bytes", Value: bytes}, {Key: "answer_kind", Value: "empty"}}
		}
		parsed, perr := parseV8(text)
		if perr != nil {
			out := ledger.Object{{Key: "answer_bytes", Value: bytes}, {Key: "answer_kind", Value: "not-json"}, {Key: "parse_error", Value: cut120(perr.plain)}}
			switch {
			case perr.at >= 0:
				out.Set("parse_offset", float64(perr.at))
			case perr.eos:
				out.Set("parse_offset", bytes)
			default:
				out.Set("parse_offset", nil)
			}
			return out
		}
		value = parsed
	}
	topKeys := ledger.Object{}
	other := 0
	if o, ok := value.(ledger.Object); ok {
		for _, f := range jsOrder(o) {
			if plainKeyRe.MatchString(f.Key) && f.Key != "__proto__" && len(topKeys) < diagnosisMaxKeys {
				topKeys = append(topKeys, ledger.Field{Key: f.Key, Value: jsonTypeOf(f.Value)})
			} else {
				other++
			}
		}
	}
	var errs []schemaError
	findingSchema.errorsAt(value, "", &errs)
	list := []any{}
	for i, e := range errs {
		if i >= diagnosisMaxErrors {
			break
		}
		list = append(list, ledger.Object{{Key: "path", Value: e.path}, {Key: "keyword", Value: e.keyword}, {Key: "params", Value: e.params}, {Key: "message", Value: cut120(e.message)}})
	}
	return ledger.Object{{Key: "answer_bytes", Value: bytes}, {Key: "answer_kind", Value: "json"}, {Key: "top_type", Value: jsonTypeOf(value)},
		{Key: "top_keys", Value: topKeys}, {Key: "other_keys", Value: float64(other)}, {Key: "schema_errors", Value: list}, {Key: "errors_total", Value: float64(len(errs))}}
}

// cut120 is .slice(0, 120) in UTF-16 units.
func cut120(s string) string {
	n := 0
	for i, r := range s {
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if n+w > diagnosisMessage {
			if n < diagnosisMessage {
				return s[:i] + "�" // a cut surrogate pair
			}
			return s[:i]
		}
		n += w
	}
	return s
}

// v8Error is a JSON.parse failure as plainParseError leaves it: plain is the
// cut message; at is the byte offset of V8's position (-1 when the message
// has none); eos marks "Unexpected end of JSON input".
type v8Error struct {
	plain string
	at    int
	eos   bool
}

type v8Parser struct {
	src   string
	i     int
	units int // the source's UTF-16 length
}

func (p *v8Parser) pos(at int) int { return utf16Len(p.src[:at]) }

func (p *v8Parser) eosErr() *v8Error {
	return &v8Error{plain: "Unexpected end of JSON input", at: -1, eos: true}
}

func (p *v8Parser) positioned(msg string, at int) *v8Error {
	return &v8Error{plain: msg + " in JSON at position " + strconv.Itoa(p.pos(at)), at: at}
}

var v8Special = map[string]bool{"NaN": true, "Infinity": true, "undefined": true, "[object Object]": true}

// unexpected is V8's ReportUnexpectedToken without an expectation message.
func (p *v8Parser) unexpected(at int) *v8Error {
	if at >= len(p.src) {
		return p.eosErr()
	}
	c := p.src[at]
	switch {
	case c == '-' || (c >= '0' && c <= '9'):
		return p.positioned("Unexpected number", at)
	case c == '"':
		return p.positioned("Unexpected string", at)
	}
	if v8Special[p.src] {
		return &v8Error{plain: "", at: -1}
	}
	r, _ := utf8.DecodeRuneInString(p.src[at:])
	msg := "Unexpected token"
	switch r {
	case '\'':
		msg = "Unexpected token'"
	case ' ', ' ':
		msg = "Unexpected token '" + string(r) + "'"
	}
	if p.units > 20 && p.pos(at) >= 10 {
		msg += ", ..."
	}
	return &v8Error{plain: msg, at: -1}
}

func (p *v8Parser) skipWS() {
	for p.i < len(p.src) {
		switch p.src[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

func (p *v8Parser) peek() (byte, bool) {
	if p.i >= len(p.src) {
		return 0, false
	}
	return p.src[p.i], true
}

// parseV8 is JSON.parse: the value (ledger.Object, []any, string, float64,
// bool, nil) or V8's error.
func parseV8(src string) (any, *v8Error) {
	p := &v8Parser{src: src, units: utf16Len(src)}
	v, err := p.value()
	if err != nil {
		return nil, err
	}
	p.skipWS()
	if p.i < len(src) {
		return nil, &v8Error{plain: "Unexpected non-whitespace character after JSON at position " + strconv.Itoa(p.pos(p.i)), at: p.i}
	}
	return v, nil
}

func (p *v8Parser) value() (any, *v8Error) {
	p.skipWS()
	c, ok := p.peek()
	if !ok {
		return nil, p.eosErr()
	}
	switch {
	case c == '"':
		return p.str()
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number()
	case c == '{':
		return p.object()
	case c == '[':
		return p.array()
	case c == 't':
		return p.literal("true", true)
	case c == 'f':
		return p.literal("false", false)
	case c == 'n':
		return p.literal("null", nil)
	}
	return nil, p.unexpected(p.i)
}

func (p *v8Parser) literal(word string, v any) (any, *v8Error) {
	for k := 0; k < len(word); k++ {
		at := p.i + k
		if at >= len(p.src) {
			return nil, p.eosErr()
		}
		if p.src[at] != word[k] {
			return nil, p.unexpected(at)
		}
	}
	p.i += len(word)
	return v, nil
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func (p *v8Parser) digitAt(at int) bool { return at < len(p.src) && isDigit(p.src[at]) }

func (p *v8Parser) number() (any, *v8Error) {
	start := p.i
	if p.src[p.i] == '-' {
		p.i++
		if !p.digitAt(p.i) {
			return nil, p.positioned("No number after minus sign", p.i)
		}
	}
	if p.src[p.i] == '0' {
		p.i++
		if p.digitAt(p.i) {
			return nil, p.positioned("Unexpected number", p.i)
		}
	} else {
		for p.digitAt(p.i) {
			p.i++
		}
	}
	if p.i < len(p.src) && p.src[p.i] == '.' {
		p.i++
		if !p.digitAt(p.i) {
			return nil, p.positioned("Unterminated fractional number", p.i)
		}
		for p.digitAt(p.i) {
			p.i++
		}
	}
	if p.i < len(p.src) && (p.src[p.i] == 'e' || p.src[p.i] == 'E') {
		p.i++
		if p.i < len(p.src) && (p.src[p.i] == '+' || p.src[p.i] == '-') {
			p.i++
		}
		if !p.digitAt(p.i) {
			return nil, p.positioned("Exponent part is missing a number", p.i)
		}
		for p.digitAt(p.i) {
			p.i++
		}
	}
	f, err := strconv.ParseFloat(p.src[start:p.i], 64)
	if err != nil && !math.IsInf(f, 0) {
		return nil, p.positioned("Unexpected number", start)
	}
	return f, nil
}

func hexVal(c byte) (rune, bool) {
	switch {
	case c >= '0' && c <= '9':
		return rune(c - '0'), true
	case c >= 'a' && c <= 'f':
		return rune(c-'a') + 10, true
	case c >= 'A' && c <= 'F':
		return rune(c-'A') + 10, true
	}
	return 0, false
}

func (p *v8Parser) str() (string, *v8Error) {
	p.i++ // the opening quote
	var b strings.Builder
	var units []uint16
	flush := func() {
		if len(units) > 0 {
			b.WriteString(string(utf16.Decode(units)))
			units = units[:0]
		}
	}
	for {
		if p.i >= len(p.src) {
			return "", p.positioned("Unterminated string", len(p.src))
		}
		c := p.src[p.i]
		switch {
		case c == '"':
			p.i++
			flush()
			return b.String(), nil
		case c < 0x20:
			return "", p.positioned("Bad control character in string literal", p.i)
		case c == '\\':
			p.i++
			if p.i >= len(p.src) {
				return "", p.eosErr()
			}
			e := p.src[p.i]
			simple := map[byte]rune{'"': '"', '\\': '\\', '/': '/', 'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t'}
			if r, ok := simple[e]; ok {
				flush()
				b.WriteRune(r)
				p.i++
				continue
			}
			if e == 'u' {
				var r rune
				for k := 1; k <= 4; k++ {
					at := p.i + k
					if at >= len(p.src) {
						return "", p.positioned("Bad Unicode escape", at)
					}
					h, ok := hexVal(p.src[at])
					if !ok {
						return "", p.positioned("Bad Unicode escape", at)
					}
					r = r<<4 | h
				}
				units = append(units, uint16(r))
				p.i += 5
				continue
			}
			if r, _ := utf8.DecodeRuneInString(p.src[p.i:]); r > 0xFF {
				return "", p.unexpected(p.i)
			}
			return "", p.positioned("Bad escaped character", p.i)
		default:
			flush()
			r, w := utf8.DecodeRuneInString(p.src[p.i:])
			b.WriteRune(r)
			p.i += w
		}
	}
}

func (p *v8Parser) object() (any, *v8Error) {
	p.i++
	o := ledger.Object{}
	p.skipWS()
	if c, ok := p.peek(); ok && c == '}' {
		p.i++
		return o, nil
	}
	expect := "Expected property name or '}'"
	for {
		if c, ok := p.peek(); !ok || c != '"' {
			return nil, p.positioned(expect, p.i)
		}
		key, err := p.str()
		if err != nil {
			return nil, err
		}
		p.skipWS()
		if c, ok := p.peek(); !ok || c != ':' {
			return nil, p.positioned("Expected ':' after property name", p.i)
		}
		p.i++
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		o.Set(key, v)
		p.skipWS()
		c, ok := p.peek()
		if ok && c == '}' {
			p.i++
			return o, nil
		}
		if !ok || c != ',' {
			return nil, p.positioned("Expected ',' or '}' after property value", p.i)
		}
		p.i++
		p.skipWS()
		expect = "Expected double-quoted property name"
	}
}

func (p *v8Parser) array() (any, *v8Error) {
	p.i++
	arr := []any{}
	p.skipWS()
	if c, ok := p.peek(); ok && c == ']' {
		p.i++
		return arr, nil
	}
	for {
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		arr = append(arr, v)
		p.skipWS()
		c, ok := p.peek()
		if ok && c == ']' {
			p.i++
			return arr, nil
		}
		if !ok || c != ',' {
			return nil, p.positioned("Expected ',' or ']' after array element", p.i)
		}
		p.i++
	}
}
