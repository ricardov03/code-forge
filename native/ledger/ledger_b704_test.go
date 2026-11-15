package ledger_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/ledger"
)

// TestB704ReadAllOrderedKeepsLoneSurrogate: ReadAllOrdered reads the
// Node-written row with lone surrogates (b716NodeLine) as JSON.parse does:
// the surrogates stay (WTF-8), the property order stays, and the row Node
// signed still verifies.
func TestB704ReadAllOrderedKeepsLoneSurrogate(t *testing.T) {
	dir := t.TempDir()
	plain := `{"z":1,"a":"\\ud800 is text"}`
	if err := os.WriteFile(filepath.Join(dir, "b704.jsonl"), []byte(plain+"\n"+b716NodeLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rows, err := ledger.ReadAllOrdered(dir, "b704")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("%d rows", len(rows))
	}
	first, _ := rows[0].(ledger.Object)
	if len(first) != 2 || first[0].Key != "z" || first[0].Value != 1.0 || first[1].Value != `\ud800 is text` {
		t.Fatalf("plain row changed: %#v", first)
	}
	row, ok := rows[1].(ledger.Object)
	if !ok {
		t.Fatalf("row is %T", rows[1])
	}
	want := []string{"event", "run", "block", "file", "finding", "by", "reason", "ts", "mac", "tokens_source", "cost_source"}
	if len(row) != len(want) {
		t.Fatalf("keys %#v", row)
	}
	for i, k := range want {
		if row[i].Key != k {
			t.Fatalf("key %d = %q, want %q", i, row[i].Key, k)
		}
	}
	if row.Get("file") != "a\xed\xa0\xbd.md" || row.Get("reason") != "lone \xed\xa0\x80 high, \xed\xb0\x80 low, pair 😀" {
		t.Fatalf("lone surrogates lost: %q %q", row.Get("file"), row.Get("reason"))
	}
	if !ledger.HasLoneSurrogate(row) || ledger.HasLoneSurrogate(first) {
		t.Fatal("HasLoneSurrogate wrong")
	}
	if !gatecheck.Verified(row.Map(), b716Key()) {
		t.Fatal("Node's signed row does not verify after ReadAllOrdered")
	}
	if got, err := ledger.Stringify(row, false); err != nil || string(got) != b716NodeLine {
		t.Fatalf("stringify differs from Node's line:\n%s", got)
	}
}

// TestB704ParseObjectKeepsLoneSurrogate: ParseObject (state bodies, records,
// tickets) keeps a nested lone surrogate and refuses a non-object.
func TestB704ParseObjectKeepsLoneSurrogate(t *testing.T) {
	obj, err := ledger.ParseObject([]byte(`{"b":{"x":["\ud83d"]},"a":2}`))
	if err != nil {
		t.Fatal(err)
	}
	inner, _ := obj.Get("b").(ledger.Object)
	arr, _ := inner.Get("x").([]any)
	if obj[0].Key != "b" || len(arr) != 1 || arr[0] != "\xed\xa0\xbd" || obj.Get("a") != 2.0 {
		t.Fatalf("got %#v", obj)
	}
	if _, err := ledger.ParseObject([]byte(`["\ud83d"]`)); err == nil {
		t.Fatal("array accepted as an object")
	}
	if _, err := ledger.ParseObject([]byte(`{"a":"\ud83d"`)); err == nil {
		t.Fatal("bad JSON accepted")
	}
}
