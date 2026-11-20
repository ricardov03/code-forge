package worker

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/queue"
)

// B944 (B918 leftover): the one automatic retry (queue.Index.RetryOnce, the
// .retried path) clears the finished ticket's finish time before it drops
// the done marker, so the worker serves the rerun instead of closing it as a
// finished ticket that lost its marker (recovered).
func TestB944RetriedTicketIsServedAgain(t *testing.T) {
	clock := fixedNow
	fx := newB488Fix(t)
	w := fx.runner(&clock)
	id := fx.ticket(t, "r1", "B1", "a.txt", sha("not the content\n"))
	if n, poison, err := w.RunTicket(id); n != 1 || poison || err != nil {
		t.Fatalf("first RunTicket %d %v %v", n, poison, err)
	}
	ticket, _ := readJSON(filepath.Join(QueueDir(fx.root), id+".json"))
	if ticket.Get("finished_at") == nil {
		t.Fatalf("finished ticket %v", ticket)
	}
	idx := &queue.Index{Dir: QueueDir(fx.root)}
	if err := idx.Refresh(); err != nil {
		t.Fatal(err)
	}
	marker, err := idx.Marker(id)
	if err != nil || marker["status"] != "stale" {
		t.Fatalf("marker %v %v", marker, err)
	}
	if got, err := idx.RetryOnce(fx.root, id, marker); got != nil || err != nil {
		t.Fatalf("RetryOnce = %v, %v; want the retry taken", got, err)
	}
	if !exists(filepath.Join(QueueDir(fx.root), id+".retried")) || exists(filepath.Join(QueueDir(fx.root), id+".done")) {
		t.Fatal("the retry did not drop the marker and spend the retry")
	}
	ticket, _ = readJSON(filepath.Join(QueueDir(fx.root), id+".json"))
	if ticket.Get("finished_at") != nil || ticket.Get("duration_ms") != nil || ticket.Get("started_at") == nil {
		t.Fatalf("retried ticket keeps its finish time: %v", ticket)
	}
	clock = clock.Add(time.Minute)
	if n, poison, err := w.RunTicket(id); n != 1 || poison || err != nil {
		t.Fatalf("second RunTicket %d %v %v", n, poison, err)
	}
	done, _ := readJSON(filepath.Join(QueueDir(fx.root), id+".done"))
	if done.Get("status") != "stale" || reviewResults(t, fx, id) != 2 || len(b884Rows(t, fx, RecoveredEvent)) != 0 {
		t.Fatalf("rerun done %v, results %d, recovered rows %d", done, reviewResults(t, fx, id), len(b884Rows(t, fx, RecoveredEvent)))
	}
	if _, err := os.Stat(filepath.Join(QueueDir(fx.root), id+".retried")); err != nil {
		t.Fatalf("retry marker: %v", err)
	}
}
