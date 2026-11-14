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
	"slices"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/sign"
)

// ClearedEvent is the signed row `run human-checks clear` writes.
const ClearedEvent = "human_check.cleared"

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
	cleared := map[pair]bool{}
	passed := map[string][]string{}
	for _, v := range rows {
		r, ok := v.(map[string]any)
		if !ok || r["run"] != run {
			continue
		}
		event := r["event"]
		if event != ClearedEvent && event != "block.human_pass" {
			continue
		}
		block, _ := r["block"].(string)
		if block == "" || key == nil {
			continue
		}
		if result, err := sign.VerifyRow(r, key); err != nil || !result.OK {
			continue
		}
		if event == ClearedEvent {
			clause, _ := r["clause"].(string)
			check, _ := r["check"].(string)
			cleared[pair{block, clause, check}] = true
			continue
		}
		checks, _ := r["checks"].([]any)
		for _, c := range checks {
			if s, ok := c.(string); ok {
				passed[block] = append(passed[block], s)
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
			if cleared[pair{field.Key, id, check}] || slices.Contains(passed[field.Key], check) {
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
