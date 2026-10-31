package queue

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ricardov03/code-forge/native/ticket"
)

func TestNullTreeTicket(t *testing.T) {
	idx := &Index{Dir: t.TempDir()}
	id := ticket.ReviewID(ticket.Parts{Run: "r-test", Block: "B1", File: "src/a.mjs", ContentHash: "hash"})
	value := Object{"ticket": id, "run": "r-test", "block": "B1", "file": "src/a.mjs", "content_hash": "hash", "tree": nil}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(idx.Dir, id+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	for call := 1; call <= 2; call++ {
		if err := idx.Refresh(); err != nil {
			t.Fatalf("call %d: %v", call, err)
		}
		got := idx.Block("r-test", "B1", nil, nil)
		if len(got) != 1 || got[0]["ticket"] != id {
			t.Fatalf("call %d: null tree excluded: %v", call, got)
		}
	}
	for _, tree := range []any{"relative/tree", 42, false} {
		value["tree"] = tree
		if err := validate(id, value); err == nil {
			t.Fatalf("accepted invalid tree %v", tree)
		}
	}
}
