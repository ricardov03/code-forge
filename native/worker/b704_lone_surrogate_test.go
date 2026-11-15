package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// b704Lone is "x" plus the lone high surrogate U+D83D in WTF-8, the string
// JSON.parse gives Node for "x\ud83d".
const b704Lone = "x\xed\xa0\xbd"

// TestB704LoadStateKeepsLoneSurrogate: a review state whose text holds a
// lone surrogate (JSON.stringify writes it as the \ud83d escape, as Node's
// saveState does) loads back "ok" with the surrogate kept, as Node's
// loadState reads it with JSON.parse; the signed body still verifies.
func TestB704LoadStateKeepsLoneSurrogate(t *testing.T) {
	home := t.TempDir()
	var rows []ledger.Object
	write := func(row ledger.Object) error {
		row = append(ledger.Object{{Key: "run", Value: "r1"}}, row...)
		signed, err := signAt(row, b496Key, b496Clock())
		rows = append(rows, signed)
		return err
	}
	state := ledger.Object{{Key: "file", Value: "a.go"}, {Key: "note", Value: b704Lone}, {Key: "reviewed_content", Value: nil}}
	if err := SaveState(SaveRequest{Home: home, RunID: "r1", Block: "B1", File: "a.go", Key: b496Key, State: state, WriteRow: write, Now: b496Clock}); err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(StatePath(home, "r1", "B1", "a.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(text), `"note":"x\ud83d"`) {
		t.Fatalf("state text is not JSON.stringify's: %s", text)
	}
	got, err := LoadState(StateWhere{Home: home, RunID: "r1", Block: "B1", File: "a.go", Key: b496Key, Rows: rows}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "ok" || got.Seq != 1 {
		t.Fatalf("load %+v", got)
	}
	if got.State.Get("note") != b704Lone {
		t.Fatalf("note %q, want the lone surrogate kept", got.State.Get("note"))
	}
}

// TestB704ReadLegacyKeepsLoneSurrogate: queue.mjs readWorker reads worker.json
// with JSON.parse, so a field with a lone surrogate escape is kept and the
// worker is read, not left undecided.
func TestB704ReadLegacyKeepsLoneSurrogate(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(QueueDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"pid":4242,"run":"r1","note":"x\ud83d"}`
	if err := os.WriteFile(filepath.Join(QueueDir(root), WorkerFile), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := ReadLegacy(root)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if w == nil || w.Get("run") != "r1" || w.Get("note") != b704Lone || w[0].Key != "pid" {
		t.Fatalf("worker %#v", w)
	}
}

// b704NodeTicket is the ticket text Node's JSON.stringify(t, null, 2) wrote
// for content_hash "h\ud83d" and acceptance_revision "rev \udc00"; Node's
// readTicket read it back with both lone surrogates kept and the id matching.
const b704NodeTicket = "{\n  \"ticket\": \"0ac46b97319f26e8b7d572d6\",\n  \"run\": \"r1\",\n  \"block\": \"B1\",\n  \"file\": \"a.md\",\n  \"content_hash\": \"h\\ud83d\",\n  \"acceptance_revision\": \"rev \\udc00\",\n  \"enqueued_at\": \"2026-11-15T10:00:00.000Z\"\n}\n"

// TestB704DecideReadKeepsLoneSurrogate: the worker reads Node's ticket as
// readTicket does (the id check hashes each lone surrogate as U+FFFD), and a
// ticket whose file holds one stays with Node (it names a path).
func TestB704DecideReadKeepsLoneSurrogate(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(QueueDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	id := "0ac46b97319f26e8b7d572d6"
	if err := os.WriteFile(filepath.Join(QueueDir(root), id+".json"), []byte(b704NodeTicket), 0o600); err != nil {
		t.Fatal(err)
	}
	w := &TicketRunner{RepoRoot: root, Home: t.TempDir()}
	out, tk, err := w.decideRead(id)
	if err != nil || out != nil {
		t.Fatalf("decideRead: %v %#v", err, out)
	}
	if tk.Get("content_hash") != "h\xed\xa0\xbd" || tk.Get("acceptance_revision") != "rev \xed\xb0\x80" || tk[0].Key != "ticket" {
		t.Fatalf("ticket %#v", tk)
	}
	path := strings.Replace(b704NodeTicket, `"a.md"`, `"a\ud83d.md"`, 1)
	if err := os.WriteFile(filepath.Join(QueueDir(root), id+".json"), []byte(path), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.decideRead(id); !IsUnsupported(err) {
		t.Fatalf("a ticket path with a lone surrogate: %v", err)
	}
}

// b704NodeRow is ledger_b716_test.go's Node-written signed row (lone
// surrogates in file and reason).
const b704NodeRow = `{"event":"review.waived","run":"r-b716","block":"B1","file":"a\ud83d.md","finding":"F1","by":"human","reason":"lone \ud800 high, \udc00 low, pair 😀","ts":"2026-11-15T10:19:11.101Z","mac":"de87a0b135e8963aa4cd0e6d887f4a553c1db4e57ed9f67c382f8c1e66c98672","tokens_source":null,"cost_source":null}`

// TestB704LedgerViewKeepsLoneSurrogate: the worker's ledger view (tail
// reader) reads Node's row with lone surrogates as JSON.parse does, in both
// the plain and the ordered form, instead of handing the ticket over.
func TestB704LedgerViewKeepsLoneSurrogate(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".code-forge", "ledger")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data := `{"event":"x","n":1}` + "\n" + b704NodeRow + "\n"
	if err := os.WriteFile(filepath.Join(dir, "p.jsonl"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	w := &TicketRunner{Home: home, Slug: "p"}
	rows, err := w.readRows()
	if err != nil || len(rows) != 2 {
		t.Fatalf("readRows %d %v", len(rows), err)
	}
	row, _ := rows[1].(map[string]any)
	if row["file"] != "a\xed\xa0\xbd.md" || row["reason"] != "lone \xed\xa0\x80 high, \xed\xb0\x80 low, pair 😀" {
		t.Fatalf("readRows row %#v", row)
	}
	if !verified(ledger.ObjectFromMap(row), b704Key()) {
		t.Fatal("Node's row does not verify after readRows")
	}
	objs, err := w.readObjects()
	if err != nil || len(objs) != 2 {
		t.Fatalf("readObjects %d %v", len(objs), err)
	}
	if objs[1][3].Key != "file" || objs[1].Get("file") != "a\xed\xa0\xbd.md" || !verified(objs[1], b704Key()) {
		t.Fatalf("readObjects row %#v", objs[1])
	}
}

func b704Key() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = 7
	}
	return key
}
