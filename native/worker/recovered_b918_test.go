package worker

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/queue"
	"github.com/ricardov03/code-forge/native/sign"
)

// b918CustomerTicket writes the customer's ticket (bug note 2026-11-19): a
// closed block's deleted file, started and finished on 2026-11-18, no done
// marker, queued before everything else.
func b918CustomerTicket(t *testing.T, fx *b488Fix) string {
	t.Helper()
	id := "bb8e6124c7f942cbdc7a058f"
	writeJSON(t, filepath.Join(QueueDir(fx.root), id+".json"), `{"ticket":"`+id+`","run":"r1","block":"B14","file":"app/Mseller/CredentialSet.php","content_hash":"deleted","enqueued_at":"2026-11-10T09:00:00.000Z","queued_at":"2026-11-10T09:00:00.000Z","started_at":"2026-11-18T10:00:00.000Z","finished_at":"2026-11-18T10:00:05.000Z","duration_ms":5000}`+"\n")
	return id
}

func b918Verified(t *testing.T, file string, key []byte) ledger.Object {
	t.Helper()
	obj, err := readJSON(file)
	if err != nil || obj == nil {
		t.Fatalf("%s: %v", file, err)
	}
	if res, err := sign.VerifyRow(obj.Map(), key); err != nil || !res.OK {
		t.Fatalf("%s does not verify", file)
	}
	return obj
}

// The customer's ticket is closed as recovered, not served, and the same
// worker goes on to review the next ticket.
func TestB918CustomerFinishedTicketIsNotServed(t *testing.T) {
	clock := fixedNow
	fx, w, _ := b497Fixture(t, &clock)
	stuck := b918CustomerTicket(t, fx)
	good := fx.ticket(t, "r1", "B1", "a.txt", sha("hello\nworld\n"))
	if p := Pending(fx.root); len(p) != 2 || p[0] != stuck {
		t.Fatalf("pending before the drain %v", p)
	}
	l := b489Loop(t, fx, w, 1)
	ended := make(chan struct{})
	go func() { l.Run(); close(ended) }()
	waitFor(t, "the next ticket's done marker", func() bool { return exists(filepath.Join(QueueDir(fx.root), good+".done")) })
	if l.Worker.Stopping() || l.Handoff() != "" {
		t.Fatalf("the worker stopped: handoff %q", l.Handoff())
	}
	l.StopWith("signal", "stopped by signal terminated")
	select {
	case <-ended:
	case <-time.After(60 * time.Second):
		t.Fatal("Run did not end after Stop")
	}
	done, _ := readJSON(filepath.Join(QueueDir(fx.root), stuck+".done"))
	if done.Get("status") != RecoveredStatus || done.Get("reason") != RecoveredReason || done.Get("finished_at") != "2026-11-18T10:00:05.000Z" || done.Get("result") != ".code-forge/reviews/r1/"+stuck+".json" {
		t.Fatalf("recovered done marker %v", done)
	}
	result := b918Verified(t, filepath.Join(fx.root, ".code-forge", "reviews", "r1", stuck+".json"), fx.key)
	if result.Get("status") != RecoveredStatus || result.Get("approved") != false || result.Get("block") != "B14" || result.Get("content_hash") != "deleted" {
		t.Fatalf("recovered result %v", result)
	}
	if n := reviewResults(t, fx, stuck); n != 0 {
		t.Fatalf("the finished ticket was reviewed again (%d review.result rows)", n)
	}
	rows := b884Rows(t, fx, RecoveredEvent)
	if len(rows) != 1 || rows[0].Get("ticket") != stuck || rows[0].Get("status") != RecoveredStatus || rows[0].Get("file") != "app/Mseller/CredentialSet.php" || rows[0].Get("run") != "r1" {
		t.Fatalf("review.recovered rows %v", rows)
	}
	next, _ := readJSON(filepath.Join(QueueDir(fx.root), good+".done"))
	if next.Get("status") != "reviewed" || reviewResults(t, fx, good) != 1 {
		t.Fatalf("next ticket: %v", next)
	}
	if p := Pending(fx.root); len(p) != 0 {
		t.Fatalf("pending after the drain %v", p)
	}
}

// A finish whose signed result was written but whose done marker was lost:
// the marker and the review.result row are published from that result.
func TestB918RecoverFromTheResultOfTheSameFinish(t *testing.T) {
	clock := fixedNow
	fx := newB488Fix(t)
	w := fx.runner(&clock)
	id := fx.ticket(t, "r1", "B1", "a.txt", sha("hello\n"), ledger.Field{Key: "queued_at", Value: "2026-11-11T09:00:00.000Z"},
		ledger.Field{Key: "started_at", Value: "2026-11-18T10:00:00.000Z"}, ledger.Field{Key: "finished_at", Value: "2026-11-18T10:00:05.000Z"}, ledger.Field{Key: "duration_ms", Value: float64(5000)})
	result := ledger.Object{{Key: "block", Value: "B1"}, {Key: "file", Value: "a.txt"}, {Key: "content_hash", Value: sha("hello\n")},
		{Key: "event", Value: "review.result"}, {Key: "status", Value: "approved"}, {Key: "approved", Value: true}, {Key: "open", Value: float64(0)},
		{Key: "queued_at", Value: "2026-11-11T09:00:00.000Z"}, {Key: "started_at", Value: "2026-11-18T10:00:00.000Z"}, {Key: "finished_at", Value: "2026-11-18T10:00:05.000Z"}, {Key: "duration_ms", Value: float64(5000)},
		{Key: "worker", Value: w.Worker}, {Key: "run", Value: "r1"}, {Key: "ticket", Value: id}}
	signed, err := w.signObject(result)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := line(signed)
	writeJSON(t, filepath.Join(fx.root, ".code-forge", "reviews", "r1", id+".json"), string(data))
	n, poison, err := w.RunTicket(id)
	if n != 1 || poison || err != nil {
		t.Fatalf("RunTicket %d %v %v", n, poison, err)
	}
	done, _ := readJSON(filepath.Join(QueueDir(fx.root), id+".done"))
	if done.Get("status") != "approved" || done.Get("finished_at") != "2026-11-18T10:00:05.000Z" || done.Get("result") != ".code-forge/reviews/r1/"+id+".json" {
		t.Fatalf("done marker %v", done)
	}
	if rows := b884Rows(t, fx, "review.result"); len(rows) != 1 || rows[0].Get("status") != "approved" || rows[0].Get("approved") != true {
		t.Fatalf("review.result rows %v", rows)
	}
	if rows := b884Rows(t, fx, RecoveredEvent); len(rows) != 1 || rows[0].Get("status") != "approved" {
		t.Fatalf("review.recovered rows %v", rows)
	}
}

// When the result or done marker cannot be written, the ticket keeps no
// finish time (before B918 it kept finished_at with no done marker).
func TestB918FailedFinishLeavesNoFinishTime(t *testing.T) {
	clock := fixedNow
	fx := newB488Fix(t)
	w := fx.runner(&clock)
	id := fx.ticket(t, "r1", "B1", "a.txt", sha("not the content\n"))
	// a non-empty directory where the result goes: its write fails
	if err := os.MkdirAll(filepath.Join(fx.root, ".code-forge", "reviews", "r1", id+".json", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	n, poison, err := w.RunTicket(id)
	if n != 0 || !poison || err != nil {
		t.Fatalf("RunTicket %d %v %v", n, poison, err)
	}
	ticket, _ := readJSON(filepath.Join(QueueDir(fx.root), id+".json"))
	if ticket.Get("finished_at") != nil || exists(filepath.Join(QueueDir(fx.root), id+".done")) {
		t.Fatalf("ticket after a failed finish %v", ticket)
	}
	if ticket.Get("started_at") == nil {
		t.Fatalf("the start time is kept for the retry: %v", ticket)
	}
}

// A finished ticket keeps its finish time, written after its done marker;
// a re-queue (queue.DropDone) clears it, so the rerun is served, not
// recovered.
func TestB918RequeuedTicketIsServedAgain(t *testing.T) {
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
	marker := filepath.Join(QueueDir(fx.root), id+".done")
	seen, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := queue.DropDone(QueueDir(fx.root), id, seen); out != "dropped" || err != nil {
		t.Fatalf("DropDone %q %v", out, err)
	}
	ticket, _ = readJSON(filepath.Join(QueueDir(fx.root), id+".json"))
	if ticket.Get("finished_at") != nil || ticket.Get("started_at") == nil || ticket.Get("content_hash") != sha("not the content\n") {
		t.Fatalf("re-queued ticket %v", ticket)
	}
	clock = clock.Add(time.Minute)
	if n, poison, err := w.RunTicket(id); n != 1 || poison || err != nil {
		t.Fatalf("second RunTicket %d %v %v", n, poison, err)
	}
	done, _ := readJSON(marker)
	if done.Get("status") != "stale" || reviewResults(t, fx, id) != 2 || len(b884Rows(t, fx, RecoveredEvent)) != 0 {
		t.Fatalf("rerun done %v, results %d", done, reviewResults(t, fx, id))
	}
}
