package jsonx

import (
	"errors"
	"math"
	"testing"
)

// B687: JSON.parse then JSON.stringify, as Node runs them, for the texts Go's
// encoding/json reads differently. Each want is what Node 22 printed for
// JSON.stringify(JSON.parse(in)).
func TestB687ParseThenStringifyAsNode(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`"\ud800"`, `"\ud800"`},
		{`"\ud83d\ude00"`, "\"\U0001F600\""},
		{`["\udc00\ud83d","a\udfffb"]`, `["\udc00\ud83d","a\udfffb"]`},
		{`"\u2028\u2029\u0000\u001f\u007f"`, "\"\u2028\u2029\\u0000\\u001f\u007f\""},
		{`"\b\f\n\r\t\"\\\/"`, `"\b\f\n\r\t\"\\/"`},
		{`1e400`, `null`},
		{`[-1e400,1e21,1e-7,0.1,-0,2.50,123456789012345678901]`, `[null,1e+21,1e-7,0.1,0,2.5,123456789012345680000]`},
		{`{"b":1,"a":2,"b":3,"2":4,"1":5}`, `{"1":5,"2":4,"b":3,"a":2}`},
		{` {"x" : [ ] } `, `{"x":[]}`},
	} {
		v, err := ParseOrdered(c.in)
		if err != nil {
			t.Fatalf("Parse(%s): %v", c.in, err)
		}
		got, err := Stringify(v)
		if err != nil || got != c.want {
			t.Fatalf("%s: got %q (%v), want %q", c.in, got, err, c.want)
		}
	}
}

// JSON.parse throws a SyntaxError for each of these.
func TestB687ParseRejectsWhatNodeRejects(t *testing.T) {
	for _, in := range []string{``, ` `, `nope`, "\ufeff{}", `{"a":1,}`, `[1,]`, `01`, `1.`, `.5`, `"\x01"`, `"\u12"`, `{"a" 1}`, `{} x`, `NaN`, `Infinity`, `'a'`} {
		if _, err := Parse(in); !errors.Is(err, ErrSyntax) {
			t.Fatalf("Parse(%q) = %v, want ErrSyntax", in, err)
		}
	}
	if v, _ := Parse(`-1e400`); v != math.Inf(-1) {
		t.Fatalf("-1e400 = %v", v)
	}
}

// TextDecoder('utf-8') (WHATWG): each maximal bad subpart is one U+FFFD.
func TestB687DecodeUTF8AsWHATWG(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"\xff\xe2\x82a", "\ufffd\ufffda"},
		{"a\xed\xa0\x80b\xf0\x9f\x98", "a\ufffd\ufffd\ufffdb\ufffd"},
		{"\xc0\xaf\xf4\x90\x80\x80", "\ufffd\ufffd\ufffd\ufffd\ufffd\ufffd"},
		{"\xe2\x80\xa8ok\U0001F600", "\u2028ok\U0001F600"},
	} {
		if got := DecodeUTF8([]byte(c.in)); got != c.want {
			t.Fatalf("DecodeUTF8(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Canonical sorts keys as JavaScript's default sort does (UTF-16 code
// units), so a key above U+FFFF sorts before U+FF61, unlike byte order.
func TestB687CanonicalSortsByUTF16(t *testing.T) {
	v, err := Parse(`{"\uff61":1,"\ud83d\ude00":2,"\ud800":3,"a":{"z":null,"y":1e400}}`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Canonical(v)
	want := "{\"a\":{\"y\":null,\"z\":null},\"\\ud800\":3,\"\U0001F600\":2,\"\uff61\":1}"
	if err != nil || got != want {
		t.Fatalf("Canonical = %q (%v), want %q", got, err, want)
	}
	if CompareUTF16("\U0001F600", "\uff61") >= 0 || CompareUTF16("b", "a") <= 0 || CompareUTF16("a", "a") != 0 {
		t.Fatal("CompareUTF16 does not order by UTF-16 code units")
	}
}

// OutsideEncodingJSON finds a lone surrogate or a non-finite number at any depth.
func TestB687OutsideEncodingJSON(t *testing.T) {
	for in, want := range map[string]bool{
		`{"a":["x",{"b":"\ud800"}]}`: true,
		`{"\udfff":1}`:               true,
		`[1,[2,[1e400]]]`:            true,
		`{"a":"\ud83d\ude00","n":1}`: false,
	} {
		v, err := Parse(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := OutsideEncodingJSON(v); got != want {
			t.Fatalf("OutsideEncodingJSON(%s) = %v", in, got)
		}
	}
}
