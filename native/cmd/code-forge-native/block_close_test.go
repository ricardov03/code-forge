package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/queue"
	"github.com/ricardov03/code-forge/native/state"
	"github.com/ricardov03/code-forge/native/ticket"
)

type closeReviewFixture struct {
	home, top, report, transcript string
	parts                         []ticket.Parts
}

func newCloseReviewFixture(t *testing.T, count int) *closeReviewFixture {
	t.Helper()
	f := &closeReviewFixture{home: t.TempDir(), top: t.TempDir()}
	t.Setenv("HOME", f.home)
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	t.Setenv("CODE_FORGE_NATIVE", "0")
	t.Setenv("CODE_FORGE_NODE", "/nonexistent")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = f.top
		data, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s %v", args, data, err)
		}
		return strings.TrimSpace(string(data))
	}
	git("init", "-q")
	owned := []any{}
	for i := 0; i < count; i++ {
		file := fmt.Sprintf("file%d.txt", i)
		owned = append(owned, file)
		closeFixtureWrite(t, filepath.Join(f.top, file), []byte("before\n"))
	}
	git("add", ".")
	git("-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "base")
	base := git("rev-parse", "HEAD")
	for _, value := range owned {
		file := value.(string)
		closeFixtureWrite(t, filepath.Join(f.top, file), []byte("after\n"))
		hash, err := gitx.ContentHash(f.top, file)
		if err != nil {
			t.Fatal(err)
		}
		f.parts = append(f.parts, ticket.Parts{Run: "r-close-fixture", Block: "B1", File: file, ContentHash: hash})
	}
	cfg := ledger.Object{
		{Key: "review", Value: ledger.Object{{Key: "close_wait_s", Value: 1}}},
		{Key: "context", Value: ledger.Object{{Key: "require_coder_tests", Value: true}}},
	}
	encoded, err := canon.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	hash := hex.EncodeToString(digest[:])
	entry := ledger.Object{{Key: "status", Value: "open"}, {Key: "base_sha", Value: base}, {Key: "owned_files", Value: owned}}
	record := ledger.Object{
		{Key: "run_id", Value: "r-close-fixture"}, {Key: "workspace", Value: f.top}, {Key: "project", Value: "fixture"},
		{Key: "status", Value: "active"}, {Key: "orphans", Value: []any{}},
		{Key: "blocks", Value: ledger.Object{{Key: "B1", Value: entry}}},
		{Key: "worker", Value: ledger.Object{{Key: "pid", Value: os.Getpid()}, {Key: "started_at", Value: statusStart(float64(os.Getpid()))}}},
		{Key: "config", Value: ledger.Object{{Key: "hash", Value: hash}, {Key: "snapshots", Value: ledger.Object{{Key: hash, Value: cfg}}}}},
	}
	if err := state.SaveRun(f.home, record); err != nil {
		t.Fatal(err)
	}
	closeFixtureWrite(t, filepath.Join(f.home, ".code-forge/runs/r-close-fixture.key"), bytes.Repeat([]byte{7}, 32))
	if _, err := state.WriteSigned(f.home, "r-close-fixture", ledger.Object{{Key: "event", Value: "dispatch"}, {Key: "block", Value: "B1"}}); err != nil {
		t.Fatal(err)
	}
	f.report, f.transcript = filepath.Join(f.home, "report.txt"), filepath.Join(f.home, "transcript.txt")
	closeFixtureWrite(t, f.report, []byte("TESTS go test ./... exit=0\n===BLOCK B1 COMPLETE===\n"))
	closeFixtureWrite(t, f.transcript, nil)
	return f
}

func closeFixtureWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func (f *closeReviewFixture) result(p ticket.Parts, outcome string) error {
	id := ticket.ReviewID(p)
	row := ledger.Object{{Key: "event", Value: "review.result"}, {Key: "block", Value: "B1"}, {Key: "file", Value: p.File}, {Key: "ticket", Value: id}, {Key: "content_hash", Value: p.ContentHash}, {Key: "status", Value: "reviewed"}, {Key: "approved", Value: outcome == "approved"}, {Key: "open", Value: []any{}}}
	if outcome == "findings" {
		row.Set("open", []any{ledger.Object{{Key: "id", Value: "F1"}, {Key: "severity", Value: "warning"}, {Key: "message", Value: "handle the missing input"}}})
	}
	if outcome == "unavailable" {
		row.Set("status", "unavailable")
	}
	if outcome != "unsigned" {
		if _, err := state.WriteSigned(f.home, p.Run, row); err != nil {
			return err
		}
		if outcome == "approved" {
			row.Set("event", "review.approved")
			if _, err := state.WriteSigned(f.home, p.Run, row); err != nil {
				return err
			}
		}
	}
	data, err := json.Marshal(map[string]any{"ticket": id, "status": row.Get("status"), "approved": outcome == "approved"})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(f.top, ".code-forge/queue", id+".done"), data, 0600)
}

// The fake reviewer observes the real queue. It waits for every file before
// completing any, so a sequential enqueue-and-wait implementation cannot pass.
func (f *closeReviewFixture) reviewer(outcome string) <-chan error {
	done := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for {
			idx := queue.Index{Dir: filepath.Join(f.top, ".code-forge/queue")}
			if err := idx.Refresh(); err != nil {
				done <- err
				return
			}
			if len(idx.Block("r-close-fixture", "B1", nil, nil)) == len(f.parts) {
				break
			}
			if time.Now().After(deadline) {
				done <- fmt.Errorf("reviewer saw fewer than %d tickets", len(f.parts))
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		for _, p := range f.parts {
			if err := f.result(p, outcome); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	return done
}

func (f *closeReviewFixture) close() (int, string, string) {
	var out, errs bytes.Buffer
	var reason handoverReason
	code := blockCloseStepWithReason([]string{"B1", "--run", "r-close-fixture", "--report", f.report, "--transcript", f.transcript}, &landStep{stdout: &out, stderr: &errs}, &reason)
	return code, out.String(), errs.String() + fmt.Sprint(reason)
}

func TestCloseQueuesCurrentContentReviews(t *testing.T) {
	f := newCloseReviewFixture(t, 2)
	// An approval for old content must not suppress the current review.
	old := f.parts[0]
	old.ContentHash = "old-content"
	if err := os.MkdirAll(filepath.Join(f.top, ".code-forge/queue"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := f.result(old, "approved"); err != nil {
		t.Fatal(err)
	}
	done := f.reviewer("approved")
	code, out, errs := f.close()
	if err := <-done; err != nil {
		t.Error(err)
	}
	if code != 0 || strings.Count(out, "close: queued review ") != 2 || strings.Count(out, "\nreviewed ") != 2 || !strings.Contains(out, "block B1 closed") {
		t.Fatalf("exit=%d out=%s err=%s; want two queued and two accepted reviews and closed", code, out, errs)
	}
	for _, p := range f.parts {
		want := "close: queued review " + p.File + " ticket " + ticket.ReviewID(p) + "\n"
		if strings.Count(out, want) != 1 {
			t.Errorf("want exactly one %q in %s", want, out)
		}
	}
}

func TestCloseQueuedReviewRefusals(t *testing.T) {
	for _, outcome := range []string{"findings", "hung", "unavailable", "unsigned"} {
		t.Run(outcome, func(t *testing.T) {
			count := 2
			if outcome == "hung" {
				// Leave room for git/process probes under suite load while still
				// rejecting ten separate one-second waits.
				count = 10
			}
			f := newCloseReviewFixture(t, count)
			var done <-chan error
			if outcome != "hung" {
				done = f.reviewer(outcome)
			}
			start := time.Now()
			code, out, errs := f.close()
			elapsed := time.Since(start)
			if done != nil {
				if err := <-done; err != nil {
					t.Error(err)
				}
			}
			want := "review_pending"
			if outcome == "findings" {
				want = "not_approved"
			}
			if code != 1 || strings.Count(out, want+" file") != count || strings.Contains(out, "block B1 closed") {
				t.Fatalf("exit=%d out=%s err=%s; want %d %s blockers and no close", code, out, errs, count, want)
			}
			if outcome == "findings" && strings.Count(out, "F1 open") != 2 {
				t.Fatalf("want both F1 findings: %s", out)
			}
			if outcome == "hung" && (elapsed < time.Second || elapsed > 8*time.Second) {
				t.Fatalf("shared 1s wait took %s", elapsed)
			}
			record, err := state.ReadRun(f.home, "r-close-fixture")
			if err != nil || blockObject(blockObject(record.Get("blocks")).Get("B1")).Get("status") != "open" {
				t.Fatalf("refused block must remain open: %v %v", record, err)
			}
		})
	}
}

func TestCloseReportWithoutTicketLines(t *testing.T) {
	for _, report := range []string{"TESTS go test ./... exit=0\n===BLOCK B1 COMPLETE===\n", "===BLOCK B1 COMPLETE===\n", "TESTS go test ./... exit=0\n===BLOCK B1 FAILED: broken===\n"} {
		t.Run(strings.TrimSpace(report), func(t *testing.T) {
			f := newCloseReviewFixture(t, 2)
			closeFixtureWrite(t, f.report, []byte(report))
			// One existing signed approval and one review completed during close.
			if err := os.MkdirAll(filepath.Join(f.top, ".code-forge/queue"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := f.result(f.parts[0], "approved"); err != nil {
				t.Fatal(err)
			}
			success := strings.Contains(report, "TESTS ") && strings.Contains(report, " COMPLETE")
			var done <-chan error
			if success {
				f.parts = f.parts[1:]
				done = f.reviewer("approved")
			}
			code, out, errs := f.close()
			if done != nil {
				if err := <-done; err != nil {
					t.Error(err)
				}
			}
			if success {
				if code != 0 || strings.Count(out, "close: queued review ") != 1 || strings.Count(out, "\nreviewed ") != 2 {
					t.Fatalf("exit=%d out=%s err=%s; want one queue, two accepted lines and close without report tickets", code, out, errs)
				}
			} else if code != 1 || strings.Count(out, "coder_report_failed -") != 1 || strings.Contains(out, "close: queued review") {
				t.Fatalf("exit=%d out=%s err=%s; want report failure before enqueue", code, out, errs)
			}
		})
	}
}

func TestBlockCloseNativeUsage(t *testing.T) {
	t.Setenv("CODE_FORGE_NODE", "/nonexistent")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	for _, args := range [][]string{{"close"}, {"close", "B1"}, {"close", "B1", "--run"}, {"close", "B1", "--run", "r-test", "--json=true"}} {
		if code := runBlock(args); code != 2 {
			t.Errorf("block %v: usage exit = %d, want 2 without Node", args, code)
		}
	}
}

func TestConfirmedBlockCloseNeverHandsOverToNode(t *testing.T) {
	t.Setenv("CODE_FORGE_NODE", "/nonexistent")
	for _, step := range []*landStep{nil, {}} {
		reason := handoverReason(-1)
		// B451: a --help=value spelling is Node's unknown flag usage (exit 2),
		// served in Go with no hand-over sentinel.
		code := blockCloseHumanStep([]string{"--help=yes"}, step, &reason, true)
		if code != 2 || reason != -1 {
			t.Fatalf("step=%v: code=%d reason=%v; want usage exit 2 and no hand-over", step, code, reason)
		}
	}
}

func TestB380BlockCloseHelpIsNative(t *testing.T) {
	t.Setenv("CODE_FORGE_NODE", "/nonexistent")
	for _, args := range [][]string{{"--help"}, {"-h"}, {"B1", "--run", "r-x", "--auto-claim", "--help"}} {
		reason := handoverReason(-1)
		if code := blockCloseHumanStep(args, nil, &reason, false); code != 0 || reason != -1 {
			t.Errorf("%v: code=%d reason=%v; want native usage exit 0 with no hand-over", args, code, reason)
		}
	}
}

func TestCloseDoneRowsDaemonAndFallback(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "cf-close-unit-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NATIVE", "1")
	run := "r-close-unit"
	dir := filepath.Join(home, ".code-forge/runs", run)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	ledgerDir := filepath.Join(home, ".code-forge/ledger")
	if err := os.MkdirAll(ledgerDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ledgerDir, "fixture.jsonl"), []byte("{\"event\":\"ledger\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	record := ledger.Object{{Key: "project", Value: "fixture"}}
	for _, answering := range []bool{true, false} {
		listener, err := net.Listen("unix", filepath.Join(dir, "native.sock"))
		if err != nil {
			t.Fatal(err)
		}
		requests := make(chan map[string]any, 2)
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
			scan := bufio.NewScanner(conn)
			for scan.Scan() {
				var request map[string]any
				if json.Unmarshal(scan.Bytes(), &request) != nil {
					return
				}
				requests <- request
				if request["method"] == "ping" {
					json.NewEncoder(conn).Encode(map[string]any{"ok": true, "run": run, "version": version})
				} else if answering {
					json.NewEncoder(conn).Encode(map[string]any{"rows": []any{map[string]any{"event": "daemon"}}})
					return
				} else {
					return
				}
			}
		}()
		rows, err := closeDoneRows(home, run, "B1", record)
		listener.Close()
		<-finished
		if err != nil {
			t.Fatal(err)
		}
		want := "ledger"
		if answering {
			want = "daemon"
		}
		if len(rows) != 1 || rows[0].(map[string]any)["event"] != want {
			t.Fatalf("answering=%v: rows=%v, want %s", answering, rows, want)
		}
		<-requests
		request := <-requests
		if request["method"] != "rows" || request["block"] != "B1" {
			t.Fatalf("wrong request: %v", request)
		}
	}
	t.Setenv("CODE_FORGE_NATIVE", "0")
	rows, err := closeDoneRows(home, run, "B1", record)
	if err != nil || len(rows) != 1 || rows[0].(map[string]any)["event"] != "ledger" {
		t.Fatalf("disabled: %v %v", rows, err)
	}
}
