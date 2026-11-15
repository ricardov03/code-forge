package blockclose

import (
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B714: `.length > 0` of an object human_checks, as V8 reads the length.
func TestB714ObjectLengthAwaits(t *testing.T) {
	obj := func(v any) ledger.Object { return ledger.Object{{Key: "length", Value: v}} }
	for _, c := range []struct {
		value any
		want  int
	}{
		{float64(2), 1}, {float64(0), 0}, {float64(-1), 0}, {" 3 ", 1}, {"0x10", 1}, {"0b0", 0}, {"1_0", 0},
		{"abc", 0}, {"", 0}, {"Infinity", 1}, {"-Infinity", 0}, {" 7　", 1}, {true, 1}, {false, 0}, {nil, 0},
		{[]any{}, 0}, {[]any{float64(1)}, 1}, {[]any{"2"}, 1}, {[]any{[]any{float64(4)}}, 1}, {[]any{nil}, 0},
		{[]any{true}, 0}, {[]any{float64(1), float64(2)}, 0}, {ledger.Object{{Key: "n", Value: float64(1)}}, 0},
		{"1e999", 1},
	} {
		if got := humanChecksLength(obj(c.value)); got != c.want {
			t.Errorf("length %#v: got %d, want %d", c.value, got, c.want)
		}
	}
	if humanChecksLength(ledger.Object{{Key: "k", Value: float64(5)}}) != 0 {
		t.Error("an object with no length key closes")
	}
}

// A length V8 cannot turn into a primitive (an object with its own toString
// or valueOf, also inside an array) stays unknown, so close hands it over.
func TestB714ObjectLengthKnown(t *testing.T) {
	bad := ledger.Object{{Key: "toString", Value: "x"}}
	for _, c := range []struct {
		value any
		want  bool
	}{
		{ledger.Object{{Key: "length", Value: bad}}, false},
		{ledger.Object{{Key: "length", Value: []any{ledger.Object{{Key: "valueOf", Value: float64(1)}}}}}, false},
		{ledger.Object{{Key: "length", Value: ledger.Object{{Key: "n", Value: float64(1)}}}}, true},
		{ledger.Object{{Key: "toString", Value: "x"}}, true},
		{[]any{"a"}, true},
	} {
		if got := ObjectLengthKnown(c.value); got != c.want {
			t.Errorf("%#v: got %v, want %v", c.value, got, c.want)
		}
	}
}
