package blockclose

import (
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B714: human_checks that is an object with its own length key. Node's
// closeBlock reads `checks.length > 0`: the length value is turned into a
// number as JavaScript's `>` does (ToPrimitive, then ToNumber), so " 3 ",
// "0x10", true and [1] await and "abc", null, [] or [1, 2] close.

// ObjectLengthKnown reports whether Go reads such a value's `.length > 0`
// exactly as V8 does. A length (or an array item inside it) that is an
// object with its own toString or valueOf key makes V8 throw "Cannot convert
// object to primitive value" inside the run lock; Go does not reproduce that.
func ObjectLengthKnown(v any) bool {
	obj, ok := v.(ledger.Object)
	if !ok {
		return true
	}
	length, has := ownLength(obj)
	return !has || primitiveSafe(length)
}

// ObjectHasLength reports whether v is an object with its own length key.
func ObjectHasLength(v any) bool {
	obj, ok := v.(ledger.Object)
	if !ok {
		return false
	}
	_, has := ownLength(obj)
	return has
}

func ownLength(obj ledger.Object) (any, bool) {
	var value any
	has := false
	for _, f := range obj {
		if f.Key == "length" {
			value, has = f.Value, true // JSON.parse keeps the last duplicate
		}
	}
	return value, has
}

func primitiveSafe(v any) bool {
	switch x := v.(type) {
	case ledger.Object:
		for _, f := range x {
			if f.Key == "toString" || f.Key == "valueOf" {
				return false
			}
		}
	case []any:
		for _, item := range x {
			if !primitiveSafe(item) {
				return false
			}
		}
	}
	return true
}

// objectLengthAwaits is `obj.length > 0` for an object with its own length.
func objectLengthAwaits(obj ledger.Object) bool {
	length, _ := ownLength(obj)
	n := jsLooseNumber(length)
	return !math.IsNaN(n) && n > 0
}

// jsLooseNumber is ToNumber(ToPrimitive(v)) for a decoded JSON value.
func jsLooseNumber(v any) float64 {
	switch x := v.(type) {
	case nil:
		return 0
	case bool:
		if x {
			return 1
		}
		return 0
	case float64:
		return x
	case string:
		return jsStringNumber(x)
	case []any:
		// Array.prototype.join(","): two or more items always hold a comma.
		switch len(x) {
		case 0:
			return 0
		case 1:
			switch item := x[0].(type) {
			case nil:
				return 0
			case bool, ledger.Object:
				return math.NaN() // "true", "false", "[object Object]"
			default:
				return jsLooseNumber(item)
			}
		}
		return math.NaN()
	}
	return math.NaN() // "[object Object]"
}

var jsDecimal = regexp.MustCompile(`^[+-]?(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

func jsSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// jsStringNumber is Number(string).
func jsStringNumber(raw string) float64 {
	s := strings.TrimFunc(raw, jsSpace)
	switch s {
	case "":
		return 0
	case "Infinity", "+Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	if len(s) > 2 && s[0] == '0' {
		if base := map[byte]int{'x': 16, 'X': 16, 'o': 8, 'O': 8, 'b': 2, 'B': 2}[s[1]]; base != 0 {
			n, ok := new(big.Int).SetString(s[2:], base)
			if !ok || strings.ContainsAny(s[2:], "+-_") {
				return math.NaN()
			}
			f, _ := new(big.Float).SetInt(n).Float64()
			return f
		}
	}
	if !jsDecimal.MatchString(s) {
		return math.NaN()
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		if ne, ok := err.(*strconv.NumError); !ok || ne.Err != strconv.ErrRange {
			return math.NaN()
		}
	}
	return f
}
