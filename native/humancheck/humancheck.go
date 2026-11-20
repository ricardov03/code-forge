// Package humancheck derives a run's human-check ledger (B631, issue #37 B).
//
// The open side comes from the run record: every acceptance clause with
// kind human_check of every block that is not stopped. The record is what
// block open and amend-acceptance already keep, in Go and in Node, so runs
// made before B631 list their checks too. The cleared side is signed rows in
// the run's ledger: a verified human_check.cleared row clears one clause and a
// verified block.human_pass row clears every check it names. Only rows that
// verify with the run key count, so an edited ledger line cannot clear a
// check.
package humancheck

import (
	"fmt"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/sign"
)

// ClearedEvent is the signed row `run human-checks clear` writes.
const ClearedEvent = "human_check.cleared"

const ReopenedEvent = "human_check.reopened"

// DeferredStatus (B632) is the block status of a block closed under an
// autopilot grant with scope human-pass:deferred while its human_check
// clauses stay open. Clearing its last open clause makes it closed.
const DeferredStatus = "closed_deferred_human"

// DeferredEvent is the signed row that turns a deferred block closed.
const DeferredEvent = "block.human_checks_cleared"

// Entry is one open human_check clause.
type Entry struct {
	Block    string
	Clause   string // C<n>: the clause's 1-based place in the block's acceptance list
	Check    string // the clause text
	WaitsFor any    // string, or nil when the clause names none
	OpenedAt any    // the block's opened_at
}

// Object is the entry as ordered JSON fields.
func (e Entry) Object() ledger.Object {
	return ledger.Object{{Key: "block", Value: e.Block}, {Key: "clause", Value: e.Clause}, {Key: "check", Value: e.Check}, {Key: "waits_for", Value: e.WaitsFor}, {Key: "opened_at", Value: e.OpenedAt}}
}

// ClauseID names the clause at index i (0-based) of a block's acceptance.
func ClauseID(i int) string { return fmt.Sprintf("C%d", i+1) }

func object(v any) ledger.Object { o, _ := v.(ledger.Object); return o }

// Open lists the open human checks of run in record order. rows are the
// project ledger rows (map[string]any); key is the run's signer key (nil
// means no clear row verifies).
func Open(run string, record ledger.Object, rows []any, key []byte) []Entry {
	type pair struct{ block, clause, check string }
	cleared := map[pair]int{}
	passed := map[pair]int{}
	reopened := map[pair]int{}
	for i, v := range rows {
		r, ok := v.(map[string]any)
		if !ok || r["run"] != run || key == nil {
			continue
		}
		event := r["event"]
		if event != ClearedEvent && event != "block.human_pass" && event != ReopenedEvent {
			continue
		}
		block, _ := r["block"].(string)
		if block == "" {
			continue
		}
		if result, err := sign.VerifyRow(r, key); err != nil || !result.OK {
			continue
		}
		clause, _ := r["clause"].(string)
		check, _ := r["check"].(string)
		switch event {
		case ReopenedEvent:
			reopened[pair{block, clause, ""}] = i + 1
		case ClearedEvent:
			cleared[pair{block, clause, check}] = i + 1
		case "block.human_pass":
			checks, _ := r["checks"].([]any)
			for _, c := range checks {
				if text, ok := c.(string); ok {
					passed[pair{block, "", text}] = i + 1
				}
			}
		}
	}
	out := []Entry{}
	for _, field := range object(record.Get("blocks")) {
		entry := object(field.Value)
		if entry == nil || entry.Get("status") == "stopped" {
			continue
		}
		clauses, _ := entry.Get("acceptance").([]any)
		for i, c := range clauses {
			clause := object(c)
			if clause.Get("kind") != "human_check" {
				continue
			}
			check, ok := clause.Get("clause").(string)
			if !ok {
				continue
			}
			id := ClauseID(i)
			if max(cleared[pair{field.Key, id, check}], passed[pair{field.Key, "", check}]) > reopened[pair{field.Key, id, ""}] {
				continue
			}
			var waits any
			if s, ok := clause.Get("waits_for").(string); ok {
				waits = s
			}
			out = append(out, Entry{Block: field.Key, Clause: id, Check: check, WaitsFor: waits, OpenedAt: entry.Get("opened_at")})
		}
	}
	return out
}

// Find returns the open entry for block and clause id.
func Find(open []Entry, block, clause string) (Entry, bool) {
	for _, e := range open {
		if e.Block == block && e.Clause == clause {
			return e, true
		}
	}
	return Entry{}, false
}

// AutoPasses returns the verified per-clause evidence passes in ledger order.
// Historical passes stay visible for audit even after the owner reopens them.
func AutoPasses(run string, rows []any, key []byte) []ledger.Object {
	out := []ledger.Object{}
	for _, v := range rows {
		r, ok := v.(map[string]any)
		if !ok || r["run"] != run || r["event"] != ClearedEvent || r["by"] != "autopilot" || key == nil {
			continue
		}
		if verified, err := sign.VerifyRow(r, key); err != nil || !verified.OK {
			continue
		}
		o := ledger.Object{}
		for _, k := range []string{"block", "clause", "check", "waits_for", "by", "grant_id", "decision_id", "proof", "proof_sha256", "evidence", "ts"} {
			o.Set(k, r[k])
		}
		out = append(out, o)
	}
	return out
}
