package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/queue"
	"github.com/ricardov03/code-forge/native/ticket"
)

// B944 (B918 leftover): the answers retry of review-file (an `unavailable`
// answers result) clears the finished ticket's finish time before it drops
// the done marker, so the worker serves the retry instead of closing it as a
// finished ticket that lost its marker.
func TestB944AnswersRetryClearsFinishTime(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	run, block, file, byFile := "r-b944", "B1", "src/b.txt", "src/a.txt"
	hash, byHash := "aaaa", "bbbb"
	ids := []string{"F1"}
	p := ticket.Parts{Run: run, Block: block, File: file, ContentHash: hash}
	id := queue.AnswersID(p, byFile, byHash, ids)
	dir := filepath.Join(root, ".code-forge", "queue")
	write := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// a finished answers ticket: its timings, an unavailable result and its done marker
	write(filepath.Join(dir, id+".json"), `{"ticket":"`+id+`","kind":"answers","run":"`+run+`","block":"`+block+`","file":"`+file+`","content_hash":"`+hash+`","by_file":"`+byFile+`","by_hash":"`+byHash+`","answers":["F1"],"enqueued_at":"2026-11-19T09:00:00.000Z","queued_at":"2026-11-19T09:00:00.000Z","started_at":"2026-11-19T09:00:01.000Z","finished_at":"2026-11-19T09:00:05.000Z","duration_ms":4000}`+"\n")
	rel := filepath.Join(".code-forge", "reviews", run, id+".json")
	write(filepath.Join(root, rel), `{"ticket":"`+id+`","status":"unavailable","approved":false}`+"\n")
	write(filepath.Join(dir, id+".done"), `{"ticket":"`+id+`","status":"unavailable","result":"`+rel+`"}`+"\n")
	out, code, ok := reviewEnqueueAnswers(home, root, run, block, root, file, byFile, hash, byHash, ids)
	if !ok || code != 0 || out.Get("retry") != true || out.Get("status") != "queued" {
		t.Fatalf("answers retry = %v %d %v", out, code, ok)
	}
	if _, err := os.Stat(filepath.Join(dir, id+".done")); !os.IsNotExist(err) {
		t.Fatalf("the done marker was not dropped: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ledger.ParseObject(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Get("finished_at") != nil || got.Get("duration_ms") != nil {
		t.Fatalf("the retried answers ticket keeps its finish time: %s", data)
	}
	if got.Get("started_at") != "2026-11-19T09:00:01.000Z" || got.Get("kind") != "answers" || got.Get("ticket") != id {
		t.Fatalf("the retry changed other fields: %s", data)
	}
}
