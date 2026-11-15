// Package canon implements the canonical JSON representation used by code-forge.
package canon

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/jsonx"
)

// Marshal returns Node canonicalJSON bytes for a JSON-compatible value. Numbers
// are interpreted as IEEE-754 doubles, including Go integers and json.Number.
// Structs and custom marshalers first pass through encoding/json, like the JSON
// round trip performed by Node's signer. Unsupported values return a JSON error.
//
// A lone surrogate held as WTF-8 (jsonx's form of a JavaScript string) stays a
// lone surrogate and is written as JSON.stringify writes it, \udXXX in
// lower-case hex, in strings and keys at any depth, and inside a custom
// marshaler's output (B696). Other invalid UTF-8 bytes become U+FFFD, as
// encoding/json does.
func Marshal(value any) ([]byte, error) {
	normalized, err := normalize(value)
	if err != nil {
		return nil, err
	}
	return appendValue(nil, normalized), nil
}

// normalize turns value into nil, bool, float64, string, []any and
// map[string]any. JSON-shaped values are walked directly so a WTF-8 lone
// surrogate survives; anything else takes the encoding/json round trip, read
// back with jsonx so a \udXXX escape a marshaler wrote stays a lone surrogate.
func normalize(value any) (any, error) {
	switch v := value.(type) {
	case nil, bool:
		return v, nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			_, err := json.Marshal(v)
			return nil, err
		}
		return v, nil
	case string:
		return canonText(v), nil
	case []string:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = canonText(item)
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			n, err := normalize(item)
			if err != nil {
				return nil, err
			}
			out[i] = n
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			n, err := normalize(item)
			if err != nil {
				return nil, err
			}
			out[canonText(key)] = n
		}
		return out, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return jsonx.Parse(string(data))
}

// canonText keeps valid UTF-8 and WTF-8 lone surrogates; each other invalid
// byte becomes U+FFFD, as encoding/json writes it.
func canonText(text string) string {
	if utf8.ValidString(text) {
		return text
	}
	out := make([]byte, 0, len(text)+8)
	for i := 0; i < len(text); {
		if i+2 < len(text) && text[i] == 0xED && text[i+1] >= 0xA0 && text[i+1] <= 0xBF && text[i+2] >= 0x80 && text[i+2] <= 0xBF {
			out = append(out, text[i:i+3]...)
			i += 3
			continue
		}
		r, size := utf8.DecodeRuneInString(text[i:])
		if r == utf8.RuneError && size == 1 {
			out = append(out, "\uFFFD"...)
		} else {
			out = append(out, text[i:i+size]...)
		}
		i += size
	}
	return string(out)
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

func lessUTF16(a, b string) bool { return jsonx.CompareUTF16(a, b) < 0 }

func appendString(dst []byte, text string) []byte { return append(dst, jsonx.Quote(text)...) }
