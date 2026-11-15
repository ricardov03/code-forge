package queue

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ricardov03/code-forge/native/ticket"
)

// TestB755TicketRuns: TicketRuns names exactly the runs PruneRun removes a
// ticket from; a run with only marker files is not named, and a pruned run
// is no longer named (B755).
func TestB755TicketRuns(t *testing.T) {
	idx := &Index{Dir: t.TempDir()}
	write := func(run, suffix string) {
		p := ticket.Parts{Run: run, Block: "B1", File: "a.go", ContentHash: "hash"}
		id := ticket.ReviewID(p)
		data, err := json.Marshal(Object{"ticket": id, "run": run, "block": "B1", "file": p.File, "content_hash": p.ContentHash})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(idx.Dir, id+suffix), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("r-a", ".json")
	write("r-a", ".done")
	write("r-b", ".json")
	write("r-markers", ".done")
	if err := idx.Refresh(); err != nil {
		t.Fatal(err)
	}
	if got, want := idx.TicketRuns(), map[string]bool{"r-a": true, "r-b": true}; !reflect.DeepEqual(got, want) {
		t.Fatalf("TicketRuns = %v, want %v", got, want)
	}
	if n, _, err := idx.PruneRun("r-markers"); n != 0 || err != nil {
		t.Fatalf("PruneRun(r-markers) = %d, %v; want 0 tickets", n, err)
	}
	if n, _, err := idx.PruneRun("r-a"); n != 1 || err != nil {
		t.Fatalf("PruneRun(r-a) = %d, %v; want 1 ticket", n, err)
	}
	if got, want := idx.TicketRuns(), map[string]bool{"r-b": true}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after prune TicketRuns = %v, want %v", got, want)
	}
}
