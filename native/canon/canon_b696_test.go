package canon_test

import (
	"testing"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/sign"
)

// WTF-8 lone surrogates, jsonx's form of a JavaScript lone surrogate.
const (
	b696HighD83D = "\xed\xa0\xbd" // \ud83d
	b696LowDE00  = "\xed\xb8\x80" // \ude00
	b696HighDBFF = "\xed\xaf\xbf" // \udbff
	b696HighD800 = "\xed\xa0\x80" // \ud800
	b696LowDC00  = "\xed\xb0\x80" // \udc00
)

func b696Row() map[string]any {
	return map[string]any{
		"event":   "review.result",
		"section": "# aaa" + b696HighD83D,
		"tail":    b696LowDE00 + "x",
		"pair":    "😀",
		"nested": map[string]any{
			"headings":         []any{b696HighDBFF, "ok"},
			b696HighD800 + "k": 1,
			"z":                2,
			"￿":                3,
			"\U00010000":       4,
		},
		"list": []any{map[string]any{"s": "x" + b696LowDC00 + "y"}},
		"n":    1.5,
	}
}

// Recorded from Node: canonicalJSON(row) and macFor(row, Buffer.alloc(32, 7))
// of src/state/signer.mjs for the same row built from JavaScript strings.
const (
	b696NodeCanonical = `{"event":"review.result","list":[{"s":"x\udc00y"}],"n":1.5,"nested":{"headings":["\udbff","ok"],"z":2,"\ud800k":1,"𐀀":4,"￿":3},"pair":"😀","section":"# aaa\ud83d","tail":"\ude00x"}`
	b696NodeMAC       = "ef0f02fe5624c4b37766eb71eac2c910ff354605b8fc040d524155d5670627fe"
)

func b696Key() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = 7
	}
	return key
}

func TestB696CanonicalKeepsLoneSurrogatesLikeNode(t *testing.T) {
	got, err := canon.Marshal(b696Row())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != b696NodeCanonical {
		t.Fatalf("canonical\n got %s\nwant %s", got, b696NodeCanonical)
	}
	mac, err := sign.MACFor(b696Row(), b696Key())
	if err != nil {
		t.Fatal(err)
	}
	if mac != b696NodeMAC {
		t.Fatalf("mac %s, want Node's %s", mac, b696NodeMAC)
	}
}

func TestB696CanonicalOtherInvalidBytesStayReplaced(t *testing.T) {
	// A stray byte that is no surrogate still becomes U+FFFD, as before.
	got, err := canon.Marshal(map[string]any{"a\xff": "b\xc3"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\"a�\":\"b�\"}"; string(got) != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}
