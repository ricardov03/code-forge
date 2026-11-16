package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/ticket"
)

// B718: block close reads the review queue as Node's closeBlock does. A
// ticket readTicket throws on prints Node's WorkerError, a queue readdir
// cannot list prints the raw scandir error, and a ticket with invalid UTF-8
// or a lone surrogate escape is read like readJSON, so the reviewed line
// names Node's ticket. Every case runs on the B684 fixture;
// testdata/b718/<case>.txt is Node's run of the same fixture
// (B718_RECORD_NODE=<repo root>).

const b718Other = "0123456789abcdef01234567" // a queue name no content matches

type b718Case struct {
	name, args string
	review     func(f *b684Fixture, hash string)
	// queue writes the review queue; reopened is the ticket id the
	// reviewed line must name (shown as <REOPENED>), or "".
	queue func(f *b684Fixture, hash string) (reopened string)
}

func b718Dir(f *b684Fixture) string { return filepath.Join(f.repo, ".code-forge", "queue") }

// b718Raw writes each name's bytes as a queue file.
func b718Raw(files ...string) func(f *b684Fixture, hash string) string {
	return func(f *b684Fixture, hash string) string {
		f.must(os.MkdirAll(b718Dir(f), 0o755))
		for i := 0; i < len(files); i += 2 {
			f.must(os.WriteFile(filepath.Join(b718Dir(f), files[i]+".json"), []byte(files[i+1]), 0o644))
		}
		return ""
	}
}

func b718One(body string) func(f *b684Fixture, hash string) string {
	return b718Raw(b718Other, body)
}

// b718Reopened writes a valid reopened ticket of B1's file whose raw JSON
// carries extra (spliced in before the closing brace).
func b718Reopened(extra string) func(f *b684Fixture, hash string) string {
	return func(f *b684Fixture, hash string) string {
		tree := filepath.Join(f.repo, ".code-forge/worktrees/B1")
		at := "2026-11-01T00:00:00.000Z"
		p := ticket.Parts{Run: f.run, Block: "B1", File: "sample.mjs", ContentHash: hash, Tree: &tree, ReopenedAt: &at}
		id := ticket.ReviewID(p)
		body := `{"ticket":"` + id + `","run":"` + f.run + `","block":"B1","file":"sample.mjs","content_hash":"` + hash + `","tree":"` + tree + `","enqueued_at":"2026-11-01T00:00:01.000Z","reopened_at":"` + at + `"` + extra + `}`
		b718Raw(id, body)(f, hash)
		return id
	}
}

// b718Replaced: a counted review result names a worker that is not pinned.
func b718Replaced(f *b684Fixture, hash string) {
	b684Approved(f, hash)
	f.row(ledger.Object{{Key: "event", Value: "review.result"}, {Key: "block", Value: "B1"}, {Key: "file", Value: "sample.mjs"}, {Key: "content_hash", Value: hash}, {Key: "status", Value: "done"}, {Key: "approved", Value: true}, {Key: "worker", Value: ledger.Object{{Key: "pid", Value: float64(1)}, {Key: "started_at", Value: "elsewhere"}}}})
}

var b718Cases = []b718Case{
	// readTicket's messages, in its order.
	{"not-json", "", b684Approved, b718One(`{`)},
	{"bom", "", b684Approved, b718One("\xef\xbb\xbf{}")},
	{"number", "", b684Approved, b718One(`5`)},
	{"null", "", b684Approved, b718One(`null`)},
	{"array", "", b684Approved, b718One(`[]`)},
	{"empty-object", "", b684Approved, b718One(`{}`)},
	{"file-backslash", "", b684Approved, b718One(`{"file":"a\\b"}`)},
	{"file-absolute", "", b684Approved, b718One(`{"file":"/a"}`)},
	{"file-dot", "", b684Approved, b718One(`{"file":"a/./b"}`)},
	{"file-git-upper", "", b684Approved, b718One(`{"file":".GIT/x"}`)},
	{"tree-relative", "", b684Approved, b718One(`{"file":"a","tree":"rel"}`)},
	{"run-number", "", b684Approved, b718One(`{"file":"a","run":5,"block":"B1","content_hash":"h"}`)},
	{"kind-other", "", b684Approved, b718One(`{"file":"a","run":"r","block":"B1","content_hash":"h","kind":"other"}`)},
	{"kind-null", "", b684Approved, b718One(`{"file":"a","run":"r","block":"B1","content_hash":"h","kind":null}`)},
	{"answers-by-file", "", b684Approved, b718One(`{"file":"a","run":"r","block":"B1","content_hash":"h","kind":"answers","by_file":"/x"}`)},
	{"answers-ids", "", b684Approved, b718One(`{"file":"a","run":"r","block":"B1","content_hash":"h","kind":"answers","by_file":"b","answers":["bad id"]}`)},
	{"answers-no-hash", "", b684Approved, b718One(`{"file":"a","run":"r","block":"B1","content_hash":"h","kind":"answers","by_file":"b","answers":["F1"]}`)},
	{"revision-number", "", b684Approved, b718One(`{"file":"a","run":"r","block":"B1","content_hash":"h","acceptance_revision":5}`)},
	{"reopened-number", "", b684Approved, b718One(`{"file":"a","run":"r","block":"B1","content_hash":"h","reopened_at":5}`)},
	{"reopened-month-13", "", b684Approved, b718One(`{"file":"a","run":"r","block":"B1","content_hash":"h","reopened_at":"2026-13-01T00:00:00.000Z"}`)},
	{"wrong-id", "", b684Approved, b718One(`{"file":"a","run":"r","block":"B1","content_hash":"h"}`)},
	{"wrong-id-json", "|--json", b684Approved, b718One(`{"file":"a","run":"r","block":"B1","content_hash":"h"}`)},
	{"two-bad", "", b684Approved, b718Raw("1"+b718Other[1:], `{"file":"/a"}`, b718Other, `{`)},
	{"directory", "", b684Approved, func(f *b684Fixture, hash string) string {
		f.must(os.MkdirAll(filepath.Join(b718Dir(f), b718Other+".json"), 0o755))
		return ""
	}},
	{"dangling-link", "", b684Approved, func(f *b684Fixture, hash string) string {
		f.must(os.MkdirAll(b718Dir(f), 0o755))
		f.must(os.Symlink(filepath.Join(f.tmp, "missing"), filepath.Join(b718Dir(f), b718Other+".json")))
		return ""
	}},
	// readdirSync's raw errors.
	{"queue-file", "", b684Approved, func(f *b684Fixture, hash string) string {
		f.must(os.WriteFile(b718Dir(f), []byte("not a directory\n"), 0o644))
		return ""
	}},
	{"queue-unlisted", "", b684Approved, func(f *b684Fixture, hash string) string {
		f.must(os.MkdirAll(b718Dir(f), 0o755))
		f.must(os.Chmod(b718Dir(f), 0))
		f.t.Cleanup(func() { _ = os.Chmod(b718Dir(f), 0o755) })
		return ""
	}},
	// A replaced worker fails the pin first: Node reads no ticket.
	{"replaced-skips-queue", "", b718Replaced, b718One(`{`)},
	// Other tickets read with readFileSync's replacement: still valid.
	{"other-run-invalid-utf8", "", b684Approved, func(f *b684Fixture, hash string) string {
		p := ticket.Parts{Run: "r-other", Block: "B1", File: "a�b", ContentHash: "h"}
		id := ticket.ReviewID(p)
		return b718Raw(id, `{"run":"r-other","block":"B1","file":"a`+"\xff"+`b","content_hash":"h"}`)(f, hash)
	}},
	// The reviewed line names the reopened attempt.
	{"reopened-invalid-utf8", "", b684Approved, b718Reopened(`,"note":"bad ` + "\xc3\x28" + ` byte"`)},
	{"reopened-lone-surrogate", "", b684Approved, b718Reopened(`,"note":"\ud800"`)},
	{"reopened-lone-surrogate-json", "|--json", b684Approved, b718Reopened(`,"note":"x\udfffy"`)},
}

func TestB718CloseQueueMatchesNode(t *testing.T) {
	if os.Getenv("BLOCK_HV_CHILD") == "1" {
		b684Child()
	}
	b760NoNodeRecorder(t, "B718_RECORD_NODE")
	record := os.Getenv("B718_RECORD_NODE")
	for _, c := range b718Cases {
		t.Run(c.name, func(t *testing.T) {
			var reopened string
			var f *b684Fixture
			f = newB684Fixture(t, b684Setup{review: func(fx *b684Fixture, hash string) {
				c.review(fx, hash)
				reopened = c.queue(fx, hash)
			}})
			args := f.args(c.args)
			before := len(f.ledgerLines())
			golden := filepath.Join("testdata", "b718", c.name+".txt")
			var r hvResult
			if record != "" {
				r = f.nodeRun(record, "notty", args)
			} else {
				r = f.goRun("TestB718CloseQueueMatchesNode", "notty", args)
			}
			if reopened != "" {
				r.stdout = strings.ReplaceAll(r.stdout, reopened, "<REOPENED>")
			}
			got := b697Mac.ReplaceAllString(f.report(r, before), `"mac":"<MAC>"`)
			if record != "" {
				f.must(os.MkdirAll(filepath.Dir(golden), 0o755))
				f.must(os.WriteFile(golden, []byte(got), 0o644))
				return
			}
			want, err := os.ReadFile(golden)
			f.must(err)
			if got != string(want) {
				t.Fatalf("report differs from Node's\n--- got\n%s\n--- want (Node)\n%s", got, want)
			}
		})
	}
}

// --dry-run is Go only (B622): a ticket readTicket refuses is one blocker
// with Node's code and message, and nothing is written.
func TestB718CloseDryRunBadTicket(t *testing.T) {
	if os.Getenv("BLOCK_HV_CHILD") == "1" {
		b684Child()
	}
	f := newB684Fixture(t, b684Setup{review: func(f *b684Fixture, hash string) {
		b684Approved(f, hash)
		b718One(`{"file":"a","run":"r","block":"B1","content_hash":"h"}`)(f, hash)
	}})
	before := len(f.ledgerLines())
	r := f.goRun("TestB718CloseDryRunBadTicket", "notty", f.args("|--dry-run|--json"))
	want := `{"code":"bad-ticket","file":null,"detail":"ticket ` + b718Other + ` does not match its id"`
	if r.code != 1 || !strings.Contains(r.stdout, want) || len(f.ledgerLines()) != before {
		t.Fatalf("exit %d, stdout %s, stderr %s; want %s", r.code, r.stdout, r.stderr, want)
	}
}
