package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/ticket"
)

// B645: close of a file whose review was reopened waits on the reopened
// attempt in Go, retires the earlier plain attempt and writes no hand-over.
func TestB645CloseReopenedReview(t *testing.T) {
	f := newCloseReviewFixture(t, 1)
	qdir := filepath.Join(f.top, ".code-forge/queue")
	plain := f.parts[0]
	reopenedAt := "2026-01-02T00:00:00.000Z"
	reopened := plain
	reopened.ReopenedAt = &reopenedAt
	plainID, reopenedID := ticket.ReviewID(plain), ticket.ReviewID(reopened)
	write := func(id, enqueued string, at *string) {
		body := ledger.Object{
			{Key: "ticket", Value: id}, {Key: "run", Value: plain.Run}, {Key: "block", Value: plain.Block}, {Key: "file", Value: plain.File}, {Key: "content_hash", Value: plain.ContentHash},
			{Key: "enqueued_at", Value: enqueued}, {Key: "queued_at", Value: enqueued}, {Key: "started_at", Value: nil}, {Key: "finished_at", Value: nil}, {Key: "duration_ms", Value: nil},
		}
		if at != nil {
			body.Set("reopened_at", *at)
		}
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		closeFixtureWrite(t, filepath.Join(qdir, id+".json"), append(data, '\n'))
	}
	write(plainID, "2026-01-01T00:00:00.000Z", nil)
	write(reopenedID, "2026-01-02T00:00:00.001Z", &reopenedAt)
	done := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(qdir, plainID+".done")); err == nil {
				done <- f.result(reopened, "approved")
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		done <- os.ErrDeadlineExceeded
	}()
	code, out, errs := f.close()
	if err := <-done; err != nil {
		t.Fatalf("reviewer: %v (exit=%d out=%s err=%s)", err, code, out, errs)
	}
	if code != 0 || !strings.Contains(out, "close: queued review file0.txt ticket "+reopenedID+"\n") || !strings.Contains(out, "block B1 closed") {
		t.Fatalf("exit=%d out=%s err=%s; want the reopened attempt queued and the block closed", code, out, errs)
	}
	if strings.Contains(out, plainID) {
		t.Errorf("close waited on the superseded attempt %s: %s", plainID, out)
	}
	data, err := os.ReadFile(filepath.Join(qdir, plainID+".done"))
	if err != nil || string(data) != `{"ticket":"`+plainID+`","status":"refused","reason":"superseded"}`+"\n" {
		t.Errorf("plain attempt marker = %q %v; want superseded", data, err)
	}
	if _, err := os.Stat(filepath.Join(f.home, ".code-forge/native/handovers.jsonl")); !os.IsNotExist(err) {
		t.Errorf("hand-over row written: %v", err)
	}
}

// B645: --auto-claim joins a file with a signed human review.reopened row in
// Go: the join enqueues the reopened attempt (reopened_at = the row's ts),
// waits on it and closes, with no hand-over.
func TestB645CloseAutoClaimReopenedReview(t *testing.T) {
	f := b493AutoClaimFixture(t, "2099-01-01T00:00:00.000Z")
	signed := b493Sign(t, f, ledger.Object{{Key: "event", Value: "review.reopened"}, {Key: "run", Value: "r-close-fixture"}, {Key: "block", Value: "B1"}, {Key: "file", Value: "file0.txt"}, {Key: "from", Value: "approved"}, {Key: "reason", Value: "look again"}, {Key: "by", Value: "human"}})
	at, ok := signed[0].Get("ts").(string)
	if !ok {
		t.Fatalf("signed row has no ts: %v", signed[0])
	}
	reopened := f.parts[0]
	reopened.ReopenedAt = &at
	id := ticket.ReviewID(reopened)
	qdir := filepath.Join(f.top, ".code-forge/queue")
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
			}
			if _, err := os.Stat(filepath.Join(qdir, id+".json")); err == nil {
				_ = f.result(reopened, "approved")
				return
			}
		}
	}()
	code, out, errs, reason := b493CloseAutoClaim(f)
	want := "block B1 auto-claimed extra.txt\nreviewed extra.txt ticket 0a276b0b4e62b3cc2d085cb0\nreviewed file0.txt ticket " + id + "\nblock B1 closed\n"
	if code != 0 || out != want || errs != "" || reason != hUnrouted {
		t.Fatalf("exit=%d reason=%v\nstdout %q\nstderr %q\nwant %q", code, reason, out, errs, want)
	}
	data, err := os.ReadFile(filepath.Join(qdir, id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil || body["reopened_at"] != at || body["file"] != "file0.txt" {
		t.Fatalf("reopened ticket %s: %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(f.home, ".code-forge/native/handovers.jsonl")); !os.IsNotExist(err) {
		t.Errorf("hand-over row written: %v", err)
	}
}
