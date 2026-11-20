package worker

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// b919Fixture: block B1 is closed (status from the record), B2 is open; both
// have a queued ticket for a deleted file (content_hash "deleted"), as the
// customer's queue held (bug note 2026-11-19).
func b919Fixture(t *testing.T, status string) (*b488Fix, *TicketRunner, *bytes.Buffer, string, string) {
	t.Helper()
	clock := fixedNow
	fx, w, stderr := b497Fixture(t, &clock)
	base := "e64a56881e75eab80bd5dcda472ab5182c0b9604"
	fx.blocksRecord(t, `,"blocks":{"B1":{"base_sha":"`+base+`","owned_files":["gone.txt"],`+status+`},"B2":{"base_sha":"`+base+`","owned_files":["gone2.txt"]}}`)
	closed := fx.ticket(t, "r1", "B1", "gone.txt", "deleted")
	open := fx.ticket(t, "r1", "B2", "gone2.txt", "deleted")
	return fx, w, stderr, closed, open
}

// b919TicketRows is every ledger row that names ticket id.
func b919TicketRows(t *testing.T, fx *b488Fix, id string) []string {
	t.Helper()
	data, _ := os.ReadFile(filepath.Join(fx.home, ".code-forge", "ledger", "unit.jsonl"))
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.Contains(l, `"ticket":"`+id+`"`) {
			out = append(out, l)
		}
	}
	return out
}

// A queued ticket of a closed block for a deleted file is not served: it
// ends stale with reason block-closed, one review.result row says so, and
// no review session is started; the open block's ticket is still served.
func TestB919ClosedBlockDeletedTicketDroppedStale(t *testing.T) {
	for _, status := range []string{`"status":"closed"`, `"status":"closed_deferred_human"`, `"status":"closed","landed":{"sha":"x"}`} {
		t.Run(status, func(t *testing.T) {
			fx, w, stderr, closed, open := b919Fixture(t, status)
			if n, poison, err := w.RunTicket(closed); n != 1 || poison || err != nil {
				t.Fatalf("RunTicket %d %v %v", n, poison, err)
			}
			done, _ := readJSON(filepath.Join(QueueDir(fx.root), closed+".done"))
			if done == nil || done.Get("status") != "stale" {
				t.Fatalf("done marker %v", done)
			}
			result := b918Verified(t, filepath.Join(fx.root, ".code-forge", "reviews", "r1", closed+".json"), fx.key)
			if result.Get("status") != "stale" || result.Get("reason") != ClosedBlockReason || result.Get("approved") != false || result.Get("block") != "B1" || result.Get("content_hash") != "deleted" {
				t.Fatalf("result %v", result)
			}
			rows := b884Rows(t, fx, "review.result")
			if len(rows) != 1 || rows[0].Get("ticket") != closed || rows[0].Get("status") != "stale" || rows[0].Get("reason") != ClosedBlockReason || rows[0].Get("file") != "gone.txt" {
				t.Fatalf("review.result rows %v", rows)
			}
			// no review session ran: no route, session or round row, no
			// session output and nothing under the run root
			if all := b919TicketRows(t, fx, closed); len(all) != 1 {
				t.Fatalf("ledger rows of the dropped ticket %v", all)
			}
			if stderr.Len() != 0 {
				t.Fatalf("session output %q", stderr.String())
			}
			if entries, _ := os.ReadDir(w.RunRootDir); len(entries) != 0 {
				t.Fatalf("run root entries %v", entries)
			}
			if p := Pending(fx.root); len(p) != 1 || p[0] != open {
				t.Fatalf("pending after the drop %v", p)
			}
			// the open block's deleted file is still served (not dropped as block-closed)
			if n, _, err := w.RunTicket(open); n != 1 || err != nil {
				t.Fatalf("RunTicket open %d %v", n, err)
			}
			other, _ := readJSON(filepath.Join(fx.root, ".code-forge", "reviews", "r1", open+".json"))
			if other == nil || other.Get("reason") == ClosedBlockReason {
				t.Fatalf("open block's ticket was dropped: %v", other)
			}
		})
	}
}

// A closed block's ticket for a file that still exists, or a rebase-check
// ticket (it names the block's tree), is not dropped as block-closed: the
// block may be re-reviewed after a rebase.
func TestB919ClosedBlockKeepsOtherTickets(t *testing.T) {
	fx, w, _, _, _ := b919Fixture(t, `"status":"closed"`)
	if err := os.WriteFile(filepath.Join(fx.root, "kept.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	live := fx.ticket(t, "r1", "B1", "kept.txt", sha("hello\n"))
	tree := fx.ticket(t, "r1", "B1", "gone.txt", "deleted", ledger.Field{Key: "tree", Value: fx.root})
	for _, id := range []string{live, tree} {
		if n, _, err := w.RunTicket(id); n != 1 || err != nil {
			t.Fatalf("RunTicket %s %d %v", id, n, err)
		}
		r, _ := readJSON(filepath.Join(fx.root, ".code-forge", "reviews", "r1", id+".json"))
		if r == nil || r.Get("reason") == ClosedBlockReason {
			t.Fatalf("ticket %s dropped as block-closed: %v", id, r)
		}
	}
}
