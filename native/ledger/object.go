package ledger

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/ricardov03/code-forge/native/canon"
)

// Field is one property of an insertion-ordered JSON object.
type Field struct {
	Key   string
	Value any
}

// Object preserves JSON.parse/JSON.stringify property order, recursively. Use
// this rather than a Go map when bytes must retain the order of an input file.
// Integer-index properties are enumerated first, as in ECMAScript Object.keys.
type Object []Field

// DecodeObject reads exactly one JSON object, retaining nested property order.
func DecodeObject(data []byte) (Object, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("expected exactly one JSON object")
	}
	obj, ok := v.(Object)
	if !ok {
		return nil, fmt.Errorf("expected a JSON object")
	}
	return obj, nil
}

// DecodeValue reads exactly one JSON value of any kind, keeping the property
// order of every object inside it, as JSON.parse does.
func DecodeValue(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("expected exactly one JSON value")
	}
	return v, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	token, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delim {
	case '{':
		obj := Object{}
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return nil, err
			}
			value, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			obj.Set(key.(string), value)
		}
		_, err := dec.Token()
		return obj, err
	case '[':
		array := []any{}
		for dec.More() {
			value, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		_, err := dec.Token()
		return array, err
	default:
		return nil, fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
}

// Get returns a property's value, or nil when absent.
func (o Object) Get(key string) any {
	for _, field := range o {
		if field.Key == key {
			return field.Value
		}
	}
	return nil
}

// Set replaces in place or adds at the end, just like JS property assignment.
func (o *Object) Set(key string, value any) {
	for i := range *o {
		if (*o)[i].Key == key {
			(*o)[i].Value = value
			return
		}
	}
	*o = append(*o, Field{key, value})
}

// Delete removes a property, preserving the remaining order.
func (o *Object) Delete(key string) {
	for i := range *o {
		if (*o)[i].Key == key {
			*o = append((*o)[:i], (*o)[i+1:]...)
			return
		}
	}
}

// Clone returns an independent shallow copy.
func (o Object) Clone() Object { return append(Object{}, o...) }

// Map exposes the values for the map-based signer. Nested Objects implement
// MarshalJSON, so the signer's canonical round trip still sees JSON objects.
func (o Object) Map() map[string]any {
	m := make(map[string]any, len(o))
	for _, field := range o {
		m[field.Key] = field.Value
	}
	return m
}

// ObjectFromMap uses sorted keys: Go maps have no recoverable insertion order.
// Callers with ordered source data must use DecodeObject or construct Object.
func ObjectFromMap(m map[string]any) Object {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	o := Object{}
	for _, key := range keys {
		o.Set(key, m[key])
	}
	return o
}

func arrayIndex(key string) (uint64, bool) {
	n, err := strconv.ParseUint(key, 10, 32)
	return n, err == nil && n < 4294967295 && strconv.FormatUint(n, 10) == key
}

func (o Object) ordered() Object {
	fields := o.Clone()
	sort.SliceStable(fields, func(i, j int) bool {
		left, a := arrayIndex(fields[i].Key)
		right, b := arrayIndex(fields[j].Key)
		if a != b {
			return a
		}
		return a && left < right
	})
	return fields
}

// MarshalJSON returns compact JSON.stringify bytes, without Go's HTML escaping.
func (o Object) MarshalJSON() ([]byte, error) { return Stringify(o, false) }

// Stringify encodes ordered objects with JSON.stringify's scalar encoding.
// Pretty output uses two-space indents; the caller supplies any final newline.
func Stringify(value any, pretty bool) ([]byte, error) {
	return stringify(nil, value, pretty, 0)
}

func stringify(dst []byte, value any, pretty bool, depth int) ([]byte, error) {
	newline := func(dst []byte, level int) []byte {
		if pretty {
			dst = append(dst, '\n')
			dst = append(dst, strings.Repeat("  ", level)...)
		}
		return dst
	}
	if m, ok := value.(map[string]any); ok {
		value = ObjectFromMap(m)
	}
	switch v := value.(type) {
	case Object:
		dst = append(dst, '{')
		for i, field := range v.ordered() {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = newline(dst, depth+1)
			key, err := canon.Marshal(field.Key)
			if err != nil {
				return nil, err
			}
			dst = append(dst, key...)
			dst = append(dst, ':')
			if pretty {
				dst = append(dst, ' ')
			}
			dst, err = stringify(dst, field.Value, pretty, depth+1)
			if err != nil {
				return nil, err
			}
		}
		if len(v) > 0 {
			dst = newline(dst, depth)
		}
		return append(dst, '}'), nil
	case []any:
		dst = append(dst, '[')
		for i, item := range v {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = newline(dst, depth+1)
			var err error
			dst, err = stringify(dst, item, pretty, depth+1)
			if err != nil {
				return nil, err
			}
		}
		if len(v) > 0 {
			dst = newline(dst, depth)
		}
		return append(dst, ']'), nil
	default:
		data, err := canon.Marshal(value)
		return append(dst, data...), err
	}
}
