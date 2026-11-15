package ledger_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/ledger"
)

// b716NodeLine is a decision row Node's signRow signed with
// Buffer.alloc(32, 7) and Node's appendRow wrote (HOME in a temp dir); Node's
// readAllRows + verifyRow gave {"ok":true} for it. Its text holds lone
// surrogates, which JSON.stringify writes as \udXXX escapes.
const b716NodeLine = `{"event":"review.waived","run":"r-b716","block":"B1","file":"a\ud83d.md","finding":"F1","by":"human","reason":"lone \ud800 high, \udc00 low, pair 😀","ts":"2026-11-15T10:19:11.101Z","mac":"de87a0b135e8963aa4cd0e6d887f4a553c1db4e57ed9f67c382f8c1e66c98672","tokens_source":null,"cost_source":null}`

func b716Key() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = 7
	}
	return key
}

// TestB716NodeRowWithLoneSurrogateVerifies: every ledger reader keeps a lone
// surrogate as JSON.parse does (WTF-8, not U+FFFD), so the gate verifies a
// row Node signed and wrote, as Node's verifyRow does.
func TestB716NodeRowWithLoneSurrogateVerifies(t *testing.T) {
	dir := t.TempDir()
	plain := `{"event":"x","n":1,"s":"\\ud800 is text, é"}`
	data := plain + "\n" + b716NodeLine + "\n"
	if err := os.WriteFile(filepath.Join(dir, "b716.jsonl"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	check := func(name string, rows []ledger.Row, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(rows) != 2 {
			t.Fatalf("%s: %d rows", name, len(rows))
		}
		first, _ := rows[0].(map[string]any)
		if first["s"] != `\ud800 is text, é` || first["n"] != 1.0 {
			t.Fatalf("%s: plain row changed: %#v", name, first)
		}
		row, _ := rows[1].(map[string]any)
		if row["reason"] != "lone \xed\xa0\x80 high, \xed\xb0\x80 low, pair 😀" || row["file"] != "a\xed\xa0\xbd.md" {
			t.Fatalf("%s: lone surrogates lost: reason %q file %q", name, row["reason"], row["file"])
		}
		if !gatecheck.Verified(row, b716Key()) {
			t.Fatalf("%s: Node's signed row does not verify in Go", name)
		}
		if gatecheck.Verified(row, make([]byte, 32)) {
			t.Fatalf("%s: verifies with the wrong key", name)
		}
	}
	rows, err := ledger.ReadAllRows(dir, "b716")
	check("ReadAllRows", rows, err)
	rows, err = ledger.ReadRows(dir, "b716")
	check("ReadRows", rows, err)
	var idx ledger.Index
	rows, err = idx.Refresh(dir, "b716")
	check("Index.Refresh", rows, err)

	row, err := ledger.ParseRow([]byte(b716NodeLine))
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := row.(map[string]any); !gatecheck.Verified(m, b716Key()) {
		t.Fatal("ParseRow: Node's signed row does not verify in Go")
	}
}
