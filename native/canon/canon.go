// Package canon implements the canonical JSON representation used by code-forge.
package canon

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Marshal returns Node canonicalJSON bytes for a JSON-compatible value. Numbers
// are interpreted as IEEE-754 doubles, including Go integers and json.Number.
// Structs and custom marshalers first pass through encoding/json, like the JSON
// round trip performed by Node's signer. Unsupported values return a JSON error.
func Marshal(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var normalized any
	if err := json.Unmarshal(data, &normalized); err != nil {
		return nil, err
	}
	return appendValue(nil, normalized), nil
}

func appendValue(dst []byte, value any) []byte {
	switch v := value.(type) {
	case nil:
		return append(dst, "null"...)
	case bool:
		return strconv.AppendBool(dst, v)
	case float64:
		// JSON.stringify collapses negative zero and uses decimal notation in
		// [1e-6, 1e21); Go's default JSON encoder retains negative zero.
		if v == 0 {
			return append(dst, '0')
		}
		magnitude := v
		if magnitude < 0 {
			magnitude = -magnitude
		}
		if magnitude >= 1e-6 && magnitude < 1e21 {
			return strconv.AppendFloat(dst, v, 'f', -1, 64)
		}
		text := strconv.FormatFloat(v, 'e', -1, 64)
		// ECMAScript exponents have a sign but no zero padding.
		at := strings.IndexByte(text, 'e')
		exponent := strings.TrimLeft(text[at+2:], "0")
		return append(dst, (text[:at+2] + exponent)...)
	case string:
		return appendString(dst, v)
	case []any:
		dst = append(dst, '[')
		for i, item := range v {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = appendValue(dst, item)
		}
		return append(dst, ']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool { return lessUTF16(keys[i], keys[j]) })
		dst = append(dst, '{')
		for i, key := range keys {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = appendString(dst, key)
			dst = append(dst, ':')
			dst = appendValue(dst, v[key])
		}
		return append(dst, '}')
	default:
		panic("canon: unexpected value after JSON normalization")
	}
}

func lessUTF16(a, b string) bool {
	left, right := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(left) && i < len(right); i++ {
		if left[i] != right[i] {
			return left[i] < right[i]
		}
	}
	return len(left) < len(right)
}

func appendString(dst []byte, text string) []byte {
	const hex = "0123456789abcdef"
	dst = append(dst, '"')
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch c {
		case '"', '\\':
			dst = append(dst, '\\', c)
		case '\b':
			dst = append(dst, '\\', 'b')
		case '\t':
			dst = append(dst, '\\', 't')
		case '\n':
			dst = append(dst, '\\', 'n')
		case '\f':
			dst = append(dst, '\\', 'f')
		case '\r':
			dst = append(dst, '\\', 'r')
		default:
			if c < 0x20 {
				dst = append(dst, '\\', 'u', '0', '0', hex[c>>4], hex[c&15])
			} else {
				dst = append(dst, c)
			}
		}
	}
	return append(dst, '"')
}
