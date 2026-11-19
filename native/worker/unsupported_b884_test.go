package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/sign"
	"github.com/ricardov03/code-forge/native/state"
)

// b884Rows is the fixture ledger's rows of event, each checked to verify
// with the run key.
func b884Rows(t *testing.T, fx *b488Fix, event string) []ledger.Object {
	t.Helper()
	return b884LedgerRows(t, filepath.Join(fx.home, ".code-forge", "ledger", "unit.jsonl"), fx.key, event)
}

func b884LedgerRows(t *testing.T, file string, key []byte, event string) []ledger.Object {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	out := []ledger.Object{}
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		obj, err := ledger.DecodeObject([]byte(l))
		if err != nil {
			t.Fatal(err)
		}
		if obj.Get("event") != event {
			continue
		}
		m := obj.Map()
		delete(m, "tokens_source")
		delete(m, "cost_source")
		if res, err := sign.VerifyRow(m, key); err != nil || !res.OK {
			t.Fatalf("%s row does not verify: %s", event, l)
		}
		out = append(out, obj)
	}
	return out
}

// Run keeps serving after an unsupported ticket: the next ticket queued
// after it is reviewed by the same loop, with no hand-over.
func TestB884RunServesAfterUnsupportedTicket(t *testing.T) {
	clock := fixedNow
	fx, w, _ := b497Fixture(t, &clock)
	bad := b489Unported(t, fx, 0)
	l := b489Loop(t, fx, w, 1)
	ended := make(chan struct{})
	go func() { l.Run(); close(ended) }()
	waitFor(t, "the unsupported ticket's done marker", func() bool { return exists(filepath.Join(QueueDir(fx.root), bad+".done")) })
	good := fx.ticket(t, "r1", "B1", "a.txt", sha("hello\nworld\n"))
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
	if reason, detail := l.ExitReason(); reason != "signal" || detail != "stopped by signal terminated" {
		t.Fatalf("exit reason %q %q", reason, detail)
	}
	done, _ := readJSON(filepath.Join(QueueDir(fx.root), good+".done"))
	if done.Get("status") != "reviewed" || reviewResults(t, fx, good) != 1 {
		t.Fatalf("next ticket: %v", done)
	}
}

// The first stop reason wins; a hand-over is reported as reason handover.
func TestB884ExitReasonFirstWins(t *testing.T) {
	clock := fixedNow
	fx, w, _ := b497Fixture(t, &clock)
	l := b489Loop(t, fx, w, 1)
	if r, d := l.ExitReason(); r != "" || d != "" {
		t.Fatalf("fresh loop exit reason %q %q", r, d)
	}
	l.StopWith("idle", "no activity")
	l.StopWith("signal", "later")
	if r, d := l.ExitReason(); r != "idle" || d != "no activity" {
		t.Fatalf("exit reason %q %q", r, d)
	}
	l2 := b489Loop(t, fx, w, 1)
	l2.HandOff("the commit watch")
	if r, d := l2.ExitReason(); r != "handover" || d != "the commit watch" {
		t.Fatalf("hand-over exit reason %q %q", r, d)
	}
}

// ExitRow writes one signed worker.exit row with the reason and the detail,
// the run's secrets masked.
func TestB884ExitRowIsSignedAndRedacted(t *testing.T) {
	clock := fixedNow
	fx, w, _ := b497Fixture(t, &clock)
	w.Secrets = []string{"sk-b884-secret-value"}
	if err := w.ExitRow("run-record-missing", "token sk-b884-secret-value leaked", w.Worker); err != nil {
		t.Fatal(err)
	}
	rows := b884Rows(t, fx, ExitEvent)
	if len(rows) != 1 {
		t.Fatalf("worker.exit rows %v", rows)
	}
	detail, _ := rows[0].Get("detail").(string)
	if rows[0].Get("reason") != "run-record-missing" || rows[0].Get("run") != "r1" || strings.Contains(detail, "sk-b884-secret-value") || !strings.Contains(detail, "token ") {
		t.Fatalf("worker.exit row %v", rows[0])
	}
}

// Unsupported masks the reason and names the ticket's block, file and hash.
func TestB884UnsupportedRowRedactsTheReason(t *testing.T) {
	clock := fixedNow
	fx, w, _ := b497Fixture(t, &clock)
	w.Secrets = []string{"sk-b884-secret-value"}
	id := fx.ticket(t, "r1", "B1", "a.txt", sha("hello\nworld\n"))
	if err := w.Unsupported(id, "the review session: key sk-b884-secret-value refused"); err != nil {
		t.Fatal(err)
	}
	rows := b884Rows(t, fx, UnsupportedEvent)
	if len(rows) != 1 {
		t.Fatalf("rows %v", rows)
	}
	r := rows[0]
	reason, _ := r.Get("reason").(string)
	if r.Get("ticket") != id || r.Get("block") != "B1" || r.Get("file") != "a.txt" || r.Get("content_hash") != sha("hello\nworld\n") ||
		strings.Contains(reason, "sk-b884") || !strings.HasPrefix(reason, "the review session: key ") {
		t.Fatalf("row %v", r)
	}
	done, err := readJSON(filepath.Join(QueueDir(fx.root), id+".done"))
	if err != nil || done.Get("status") != UnsupportedStatus || strings.Contains(done.Get("reason").(string), "sk-b884") {
		t.Fatalf("done %v %v", done, err)
	}
	result, err := readJSON(filepath.Join(fx.root, ".code-forge", "reviews", "r1", id+".json"))
	if err != nil || result.Get("approved") != false || result.Get("status") != UnsupportedStatus {
		t.Fatalf("result %v %v", result, err)
	}
	if err := w.Unsupported("not-a-ticket", "x"); err == nil {
		t.Fatal("a bad ticket id was accepted")
	}
}

// An autopilot grant whose until Go cannot read (Date.parse undecided)
// skips the autopilot work only: no hand-over, the grant kept, one signed
// worker.autopilot_unsupported row however many drains meet it.
func TestB884AutopilotGrantUnreadableKeepsWorker(t *testing.T) {
	home := t.TempDir()
	run := "r-b884"
	grant, err := ledger.DecodeObject([]byte(`{"grant_id":"ap-b884","status":"active","until":"11 Nov 26","scopes":["close:routine"],"deny":[],"delegate":"L2","caps":{},"stop_at":0.9,"started_at":"2026-01-01T00:00:00.000Z","stopped_at":null,"expired_at":null,"link":null}`))
	if err != nil {
		t.Fatal(err)
	}
	r := ledger.Object{{Key: "run_id", Value: run}, {Key: "workspace", Value: t.TempDir()}, {Key: "project", Value: "b884"}, {Key: "status", Value: "active"},
		{Key: "blocks", Value: ledger.Object{}}, {Key: "autopilot", Value: grant}}
	if err := state.SaveRun(home, r); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if err := os.WriteFile(filepath.Join(home, ".code-forge/runs", run+".key"), key, 0o600); err != nil {
		t.Fatal(err)
	}
	now, _ := time.Parse(time.RFC3339, "2026-01-01T12:00:00Z")
	l := &Loop{Runner: &TicketRunner{Home: home, RunID: run, Slug: "b884", Key: key}, Now: func() time.Time { return now }}
	for i := 0; i < 3; i++ {
		if err := l.ExpiryCheck(); err != nil || l.Handoff() != "" {
			t.Fatalf("drain %d: %v, handoff %q", i, err, l.Handoff())
		}
	}
	if reason, _ := l.ExitReason(); reason != "" {
		t.Fatalf("the loop recorded a stop: %q", reason)
	}
	saved, _ := state.ReadRun(home, run)
	if saved.Get("autopilot").(ledger.Object).Get("status") != "active" {
		t.Fatalf("grant changed: %v", saved.Get("autopilot"))
	}
	rows := b884LedgerRows(t, filepath.Join(home, ".code-forge/ledger", "b884.jsonl"), key, AutopilotUnsupportedEvent)
	if len(rows) != 1 || rows[0].Get("reason") != AutopilotGrantUnsupported || rows[0].Get("run") != run {
		t.Fatalf("rows %v", rows)
	}
}
