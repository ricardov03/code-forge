package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/sign"
	"github.com/ricardov03/code-forge/native/ticket"
)

func TestSweepGraceAndSignatures(t *testing.T) {
	s := &server{home: t.TempDir(), repo: t.TempDir(), run: "r-current", project: "test"}
	s.queue.Dir = filepath.Join(s.repo, ".code-forge", "queue")
	writeObject(t, filepath.Join(s.repo, ".code-forge", "runs", s.run+".json"), object{"run_id": s.run, "project": s.project})
	key := make([]byte, 32)
	keyDir := filepath.Join(s.home, ".code-forge", "runs")
	if err := os.MkdirAll(keyDir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, run := range []string{s.run, "r-ended", "r-forged"} {
		if err := os.WriteFile(filepath.Join(keyDir, run+".key"), key, 0600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-11 * time.Minute).UTC().Format(time.RFC3339Nano)
	fresh := time.Now().UTC().Format(time.RFC3339Nano)
	rows := []object{
		{"run": s.run, "block": "old", "event": "block.close", "status": "complete", "ts": old},
		{"run": s.run, "block": "fresh", "event": "block.close", "status": "complete", "ts": fresh},
		{"run": s.run, "block": "reopened", "event": "block.close", "status": "complete", "ts": old},
		{"run": s.run, "block": "reopened", "event": "block.open", "ts": fresh},
		{"run": "r-ended", "block": "B1", "event": "run.end", "ts": old},
		{"run": "r-forged", "block": "B1", "event": "run.end", "ts": old},
		{"run": "r-missing", "block": "B1", "event": "run.end", "ts": old},
	}
	data := []byte{}
	for _, row := range rows {
		signed, err := sign.SignRow(row, key)
		if err != nil {
			t.Fatal(err)
		}
		if row["run"] == "r-forged" {
			signed["mac"] = "forged"
		}
		line, err := json.Marshal(signed)
		if err != nil {
			t.Fatal(err)
		}
		data = append(append(data, line...), '\n')
		p := ticket.Parts{Run: text(row, "run"), Block: text(row, "block"), File: "a.go", ContentHash: "hash"}
		id := ticket.ReviewID(p)
		writeObject(t, filepath.Join(s.queue.Dir, id+".json"), object{"ticket": id, "run": p.Run, "block": p.Block, "file": p.File, "content_hash": p.ContentHash})
	}
	file := filepath.Join(s.home, ".code-forge", "ledger", "test.jsonl")
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODE_FORGE_PRUNE_GRACE", "10m")
	if got := s.handle(context.Background(), request{Method: "ping"}); got["ok"] != true {
		t.Fatal(got)
	}
	for _, row := range rows {
		block, run := text(row, "block"), text(row, "run")
		want := 1
		if block == "old" || run == "r-ended" {
			want = 0
		}
		if got := len(s.queue.Block(run, block, nil, nil)); got != want {
			t.Errorf("%s/%s tickets = %d, want %d", run, block, got, want)
		}
	}
	// A second request inside the minute must not sweep even after changing grace.
	t.Setenv("CODE_FORGE_PRUNE_GRACE", "0s")
	s.handle(context.Background(), request{Method: "ping"})
	if got := len(s.queue.Block(s.run, "fresh", nil, nil)); got != 1 {
		t.Errorf("within-minute fresh tickets = %d, want 1", got)
	}
}
