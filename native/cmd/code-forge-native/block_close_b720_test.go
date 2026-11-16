package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
	"github.com/ricardov03/code-forge/native/ticket"
)

// B720: block close no longer hands the queue identity or the pin check to
// Node. reviewIdentity's sort runs with V8's TimSort and localeCompare (a
// TypeError there prints "no ticket" as Node's toolLines does), readTicket's
// reopened_at is checked with V8's Date.parse, and closeBlock's claim check
// (a claim file Go cannot read or parse, odd worker pins, a dead pinned
// worker holding a ticket) fails or stops the block as Node's does, also
// under --decide, --no-require-reviews and --auto-claim. Every case runs on
// the B684 fixture; testdata/b720/<case>.txt is Node's run of the same
// fixture (B720_RECORD_NODE=<repo root>).

type b720Case struct {
	name, ask, args string
	finding         string // "" approves sample.mjs; else F1 stays open at this severity
	// setup writes the queue, claims and record changes; it returns the
	// ticket ids to name as <T0>, <T1>, ... in the report.
	setup func(f *b684Fixture, hash string) []string
}

func b720Tree(f *b684Fixture) string { return filepath.Join(f.repo, ".code-forge/worktrees/B1") }

// b720Ticket writes a ticket of B1's sample.mjs; enqueued is the raw JSON
// of enqueued_at and reopened, when not nil, its reopened_at.
func b720Ticket(f *b684Fixture, hash, enqueued string, reopened *string) string {
	tree := b720Tree(f)
	p := ticket.Parts{Run: f.run, Block: "B1", File: "sample.mjs", ContentHash: hash, Tree: &tree, ReopenedAt: reopened}
	id := ticket.ReviewID(p)
	body := `{"ticket":"` + id + `","run":"` + f.run + `","block":"B1","file":"sample.mjs","content_hash":"` + hash + `","tree":"` + tree + `","enqueued_at":` + enqueued
	if reopened != nil {
		body += `,"reopened_at":"` + *reopened + `"`
	}
	b718Raw(id, body+`}`)(f, hash)
	return id
}

func b720At(s string) *string { return &s }

// b720Claim writes the claim lock of a ticket under the temp HOME.
func b720Claim(f *b684Fixture, id string, write func(path string)) {
	sum := sha256.Sum256([]byte("ticket\x00" + f.run + "\x00" + id))
	dir := filepath.Join(f.home, ".code-forge", "locks")
	f.must(os.MkdirAll(dir, 0o700))
	write(filepath.Join(dir, hex.EncodeToString(sum[:])+".lock"))
}

func b720ClaimText(text string) func(f *b684Fixture, hash string) []string {
	return func(f *b684Fixture, hash string) []string {
		id := b720Ticket(f, hash, `"2026-11-01T00:00:01.000Z"`, nil)
		b720Claim(f, id, func(path string) { f.must(os.WriteFile(path, []byte(text), 0o600)) })
		return []string{id}
	}
}

// b720Record sets run record fields after the fixture saved it.
func b720Record(f *b684Fixture, fields ledger.Object) {
	record, err := state.ReadRun(f.home, f.run)
	f.must(err)
	for _, field := range fields {
		record.Set(field.Key, field.Value)
	}
	f.must(state.SaveRun(f.home, record))
}

func b720Live() ledger.Object {
	pid := float64(os.Getpid())
	return ledger.Object{{Key: "pid", Value: pid}, {Key: "started_at", Value: statusStart(pid)}}
}

// b720Pins sets the record's workers (and a queued ticket, so the claim
// loop runs) after the fixture saved it.
func b720Pins(fields ledger.Object) func(f *b684Fixture, hash string) []string {
	return func(f *b684Fixture, hash string) []string {
		id := b720Ticket(f, hash, `"2026-11-01T00:00:01.000Z"`, nil)
		b720Record(f, fields)
		return []string{id}
	}
}

// b720Dead: a second pinned worker that is not running holds B1's ticket.
func b720Dead(f *b684Fixture, hash string) []string {
	dead := ledger.Object{{Key: "pid", Value: float64(999999)}, {Key: "started_at", Value: "Sat Nov  1 00:00:00 2026"}}
	id := b720Ticket(f, hash, `"2026-11-01T00:00:01.000Z"`, nil)
	b720Record(f, ledger.Object{{Key: "workers", Value: []any{b720Live(), dead}}})
	b720Claim(f, id, func(path string) {
		f.must(os.WriteFile(path, []byte(`{"pid":999999,"start":"Sat Nov  1 00:00:00 2026"}`), 0o600))
	})
	return []string{id}
}

// b720Two writes two attempts of sample.mjs: the ticket whose id sorts
// first (the queue is read in name order, as libuv's scandir sorts it) gets
// the raw enqueued_at first, the other second. V8's insertion sort of two
// calls compare(second, first), so the order decides which value's
// localeCompare runs.
func b720Two(first, second string) func(f *b684Fixture, hash string) []string {
	return func(f *b684Fixture, hash string) []string {
		a, b := b720At("2026-10-01T00:00:00.000Z"), b720At("2026-10-02T00:00:00.000Z")
		tree := b720Tree(f)
		idA := ticket.ReviewID(ticket.Parts{Run: f.run, Block: "B1", File: "sample.mjs", ContentHash: hash, Tree: &tree, ReopenedAt: a})
		idB := ticket.ReviewID(ticket.Parts{Run: f.run, Block: "B1", File: "sample.mjs", ContentHash: hash, Tree: &tree, ReopenedAt: b})
		if idB < idA {
			a, b = b, a
		}
		return []string{b720Ticket(f, hash, first, a), b720Ticket(f, hash, second, b)}
	}
}

func b720One(enqueued, reopened string) func(f *b684Fixture, hash string) []string {
	return func(f *b684Fixture, hash string) []string {
		return []string{b720Ticket(f, hash, enqueued, b720At(reopened))}
	}
}

const b720Decide = "|--decide|--reason|naming only"

var b720Cases = []b720Case{
	// reviewIdentity: a TypeError of the sort prints "no ticket".
	{"identity-numbers", "notty", "", "", b720Two(`5`, `6`)},
	{"identity-number-text", "notty", "", "", b720Two(`5`, `"2026-11-01T00:00:01.000Z"`)},
	{"identity-text-number", "notty", "", "", b720Two(`"2026-11-01T00:00:01.000Z"`, `5`)},
	{"identity-object", "notty", "", "", b720Two(`{"toString":1}`, `"a"`)},
	{"identity-text-object", "notty", "", "", b720Two(`"a"`, `{"toString":1}`)},
	{"identity-text-object-plain", "notty", "", "", b720Two(`"a"`, `{}`)},
	{"identity-text-array", "notty", "", "", b720Two(`"a"`, `["b",1]`)},
	{"identity-object-plain-text", "notty", "", "", b720Two(`{}`, `"a"`)},
	{"identity-array-text", "notty", "", "", b720Two(`["b",1]`, `"b,0"`)},
	{"identity-true-text", "notty", "", "", b720Two(`true`, `"u"`)},
	{"identity-null", "notty", "", "", b720Two(`null`, `"a"`)},
	{"identity-text-null", "notty", "", "", b720Two(`"a"`, `null`)},
	{"identity-text-true", "notty", "", "", b720Two(`"u"`, `true`)},
	// localeCompare order, not byte order: "a" sorts before "B".
	{"identity-locale-case", "notty", "", "", b720Two(`"B"`, `"a"`)},
	{"identity-locale-case-json", "notty", "|--json", "", b720Two(`"a"`, `"B"`)},
	{"identity-locale-stamps", "notty", "", "", b720Two(`"2026-11-01 10:00"`, `"2026-11-01T09:00:00.000Z"`)},
	{"identity-same-stamp", "notty", "", "", b720Two(`"2026-11-01T00:00:01.000Z"`, `"2026-11-01T00:00:01.000Z"`)},
	// readTicket's reopened_at check is V8's Date.parse.
	{"reopened-local", "notty", "", "", b720One(`"2026-11-01T00:00:01.000Z"`, "2026-11-01T00:00:00")},
	{"reopened-legacy", "notty", "", "", b720One(`"2026-11-01T00:00:01.000Z"`, "Nov 1 2026 10:00 PM")},
	{"reopened-day-31", "notty", "", "", b720One(`"2026-11-01T00:00:01.000Z"`, "2026-02-31T00:00:00Z")},
	{"reopened-garbage", "notty", "", "", b720One(`"2026-11-01T00:00:01.000Z"`, "soon")},
	// closeBlock's claim check.
	{"claim-empty", "notty", "", "", b720ClaimText(``)},
	{"claim-not-json", "notty", "", "", b720ClaimText(`{`)},
	{"claim-not-json-json", "notty", "|--json", "", b720ClaimText(`{"pid":`)},
	{"claim-null", "notty", "", "", b720ClaimText(`null`)},
	{"claim-number", "notty", "", "", b720ClaimText(`5`)},
	{"claim-other-worker", "notty", "", "", b720ClaimText(`{"pid":999999,"start":"x"}`)},
	{"claim-live-worker", "notty", "", "", func(f *b684Fixture, hash string) []string {
		return b720ClaimText(`{"pid":`+strconv.Itoa(os.Getpid())+`,"start":"`+statusStart(float64(os.Getpid()))+`"}`)(f, hash)
	}},
	{"claim-directory", "notty", "", "", func(f *b684Fixture, hash string) []string {
		id := b720Ticket(f, hash, `"2026-11-01T00:00:01.000Z"`, nil)
		b720Claim(f, id, func(path string) { f.must(os.MkdirAll(path, 0o700)) })
		return []string{id}
	}},
	{"dead-pinned-worker", "notty", "", "", b720Dead},
	{"dead-pinned-worker-json", "notty", "|--json", "", b720Dead},
	// odd worker pins in the run record.
	{"workers-number", "notty", "", "", b720Pins(ledger.Object{{Key: "workers", Value: float64(5)}})},
	{"workers-object", "notty", "", "", b720Pins(ledger.Object{{Key: "workers", Value: ledger.Object{}}})},
	{"workers-text", "notty", "", "", b720Pins(ledger.Object{{Key: "workers", Value: "ab"}})},
	{"workers-null-entry", "notty", "", "", b720Pins(ledger.Object{{Key: "workers", Value: []any{nil, b720Live()}}})},
	{"history-number", "notty", "", "", b720Pins(ledger.Object{{Key: "worker_history", Value: float64(5)}})},
	{"history-text", "notty", "", "", b720Pins(ledger.Object{{Key: "worker_history", Value: "ab"}})},
	// the owner's closes: the --decide preview, the waived close, --auto-claim.
	{"decide-claim-not-json", "yes", b720Decide, "warning", b720ClaimText(`{`)},
	{"decide-claim-not-json-notty", "notty", b720Decide, "warning", b720ClaimText(`{`)},
	{"decide-dead-pinned-worker", "yes", b720Decide, "warning", b720Dead},
	{"decide-identity-locale", "yes", b720Decide, "warning", b720Two(`"B"`, `"a"`)},
	{"nrr-claim-not-json", "yes", "|--no-require-reviews", "major", b720ClaimText(`{`)},
	{"nrr-dead-pinned-worker", "yes", "|--no-require-reviews", "major", b720Dead},
	{"auto-claim-claim-not-json", "notty", "|--auto-claim", "", b720ClaimText(`{`)},
	{"auto-claim-dead-pinned-worker", "notty", "|--auto-claim", "", b720Dead},
}

func TestB720CloseQueueMatchesNode(t *testing.T) {
	if os.Getenv("BLOCK_HV_CHILD") == "1" {
		b684Child()
	}
	b760NoNodeRecorder(t, "B720_RECORD_NODE")
	record := os.Getenv("B720_RECORD_NODE")
	for _, c := range b720Cases {
		t.Run(c.name, func(t *testing.T) {
			var ids []string
			review := b684Approved
			if c.finding != "" {
				review = b684Finding(c.finding)
			}
			f := newB684Fixture(t, b684Setup{review: func(fx *b684Fixture, hash string) {
				review(fx, hash)
				ids = c.setup(fx, hash)
			}})
			args := f.args(c.args)
			before := len(f.ledgerLines())
			golden := filepath.Join("testdata", "b720", c.name+".txt")
			var r hvResult
			if record != "" {
				r = f.nodeRun(record, c.ask, args)
			} else {
				r = f.goRun("TestB720CloseQueueMatchesNode", c.ask, args)
			}
			for i, id := range ids {
				r.stdout = strings.ReplaceAll(r.stdout, id, "<T"+strconv.Itoa(i)+">")
				r.stderr = strings.ReplaceAll(r.stderr, id, "<T"+strconv.Itoa(i)+">")
			}
			got := b697Mac.ReplaceAllString(f.report(r, before), `"mac":"<MAC>"`)
			got = strings.ReplaceAll(got, statusStart(float64(os.Getpid())), "<LIVE-START>")
			got = strings.ReplaceAll(got, strconv.Itoa(os.Getpid()), "<LIVE>")
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
