package ledger_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/sign"
)

// b696Object is Node's row {event, section, tail, pair, nested, list, n} with
// lone surrogates held as WTF-8 (jsonx's form of a JavaScript lone surrogate).
func b696Object() ledger.Object {
	return ledger.Object{
		{Key: "event", Value: "review.result"},
		{Key: "section", Value: "# aaa\xed\xa0\xbd"},
		{Key: "tail", Value: "\xed\xb8\x80x"},
		{Key: "pair", Value: "😀"},
		{Key: "nested", Value: ledger.Object{
			{Key: "headings", Value: []any{"\xed\xaf\xbf", "ok"}},
			{Key: "\xed\xa0\x80k", Value: 1.0},
			{Key: "z", Value: 2.0},
			{Key: "￿", Value: 3.0},
			{Key: "\U00010000", Value: 4.0},
		}},
		{Key: "list", Value: []any{ledger.Object{{Key: "s", Value: "x\xed\xb0\x80y"}}}},
		{Key: "n", Value: 1.5},
	}
}

// Recorded from Node: JSON.stringify(row) and macFor(row, Buffer.alloc(32, 7)).
const (
	b696NodeStringify = `{"event":"review.result","section":"# aaa\ud83d","tail":"\ude00x","pair":"😀","nested":{"headings":["\udbff","ok"],"\ud800k":1,"z":2,"￿":3,"𐀀":4},"list":[{"s":"x\udc00y"}],"n":1.5}`
	b696NodeMAC       = "ef0f02fe5624c4b37766eb71eac2c910ff354605b8fc040d524155d5670627fe"
)

func TestB696StringifyAndAppendKeepLoneSurrogatesLikeNode(t *testing.T) {
	got, err := ledger.Stringify(b696Object(), false)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != b696NodeStringify {
		t.Fatalf("stringify\n got %s\nwant %s", got, b696NodeStringify)
	}

	// The signer sees nested Objects through MarshalJSON; the MAC is Node's.
	key := make([]byte, 32)
	for i := range key {
		key[i] = 7
	}
	mac, err := sign.MACFor(b696Object().Map(), key)
	if err != nil {
		t.Fatal(err)
	}
	if mac != b696NodeMAC {
		t.Fatalf("mac %s, want Node's %s", mac, b696NodeMAC)
	}

	// The ledger line on disk carries the same escapes.
	dir := t.TempDir()
	row := b696Object()
	row.Set("ts", "2026-11-15T00:00:00.000Z")
	if _, err := ledger.AppendObject(dir, "b696", row, ledger.DefaultRotateAtBytes); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "b696.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	want := b696NodeStringify[:len(b696NodeStringify)-1] + `,"ts":"2026-11-15T00:00:00.000Z","tokens_source":null,"cost_source":null}` + "\n"
	if string(data) != want {
		t.Fatalf("ledger line\n got %s\nwant %s", data, want)
	}
}
