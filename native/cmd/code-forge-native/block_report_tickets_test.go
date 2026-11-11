package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ricardov03/code-forge/native/gatecheck"
)

// B405: block ready / close find a done review-file ticket for the file's
// current content by themselves; report ticket lines stay optional.

const (
	b405Run   = "r-20261107-034743-406858"
	b405Block = "B405"
	b405File  = "src/a.go"
	b405Hash  = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	b405Old   = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

func b405Ticket(t *testing.T, dir, id, block, hash, enqueued string, done bool) {
	t.Helper()
	write := func(name string, v any) {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(id+".json", map[string]any{"ticket": id, "run": b405Run, "block": block, "file": b405File, "content_hash": hash, "enqueued_at": enqueued, "queued_at": enqueued, "started_at": nil, "finished_at": nil, "duration_ms": nil})
	if done {
		write(id+".done", map[string]any{"ticket": id, "status": "reviewed", "result": ".code-forge/reviews/" + b405Run + "/" + id + ".json"})
	}
}

func b405Check(t *testing.T, report, qdir string) closeCheck {
	t.Helper()
	files := []gatecheck.File{{File: b405File, Hash: b405Hash}}
	check, _ := closeReport(report, b405Run, b405Block, qdir, files, nil, nil, nil)
	return check
}

func TestReportCreditsDoneTicketForCurrentContent(t *testing.T) {
	qdir := t.TempDir()
	b405Ticket(t, qdir, "aaaaaaaaaaaaaaaaaaaaaaaa", b405Block, b405Hash, "2026-11-11T01:00:00.000Z", true)
	check := b405Check(t, "===BLOCK B405 COMPLETE===\n", qdir)
	if check.Reason != "" || len(check.Blockers) != 0 {
		t.Fatalf("report without ticket lines and a done ticket for the current content: want 0 blockers, got reason %q blockers %+v", check.Reason, check.Blockers)
	}
}

func TestReportRefusesTicketForOtherContentBlockOrNotDone(t *testing.T) {
	qdir := t.TempDir()
	b405Ticket(t, qdir, "bbbbbbbbbbbbbbbbbbbbbbbb", b405Block, b405Old, "2026-11-11T02:00:00.000Z", true)
	b405Ticket(t, qdir, "cccccccccccccccccccccccc", "B999", b405Hash, "2026-11-11T02:00:00.000Z", true)
	b405Ticket(t, qdir, "dddddddddddddddddddddddd", b405Block, b405Hash, "2026-11-11T02:00:00.000Z", false)
	check := b405Check(t, "===BLOCK B405 COMPLETE===\n", qdir)
	want := "coder report FAILED: no review-file ticket id for " + b405File
	if check.Reason != want {
		t.Fatalf("reason: want %q, got %q", want, check.Reason)
	}
	if len(check.Blockers) != 1 || check.Blockers[0].Code != "coder_report_failed" || check.Blockers[0].File != b405File || check.Blockers[0].Detail != want {
		t.Fatalf("want exactly one coder_report_failed blocker for %s, got %+v", b405File, check.Blockers)
	}
}

func TestReportHandLineStillAccepted(t *testing.T) {
	qdir := t.TempDir()
	check := b405Check(t, "reviewed "+b405File+" ticket eeeeeeeeeeeeeeeeeeeeeeee\n===BLOCK B405 COMPLETE===\n", qdir)
	if check.Reason != "" || len(check.Blockers) != 0 {
		t.Fatalf("explicit report line: want 0 blockers, got reason %q blockers %+v", check.Reason, check.Blockers)
	}
}
