package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ricardov03/code-forge/native/blockclose"
	"github.com/ricardov03/code-forge/native/humancheck"
	"github.com/ricardov03/code-forge/native/ledger"
)

// B633 (issue #37 B, part 3): the run's final gate. `run end` refuses while
// the run has an open human check of a block past open (closed_deferred_human,
// awaiting_human_check, closed, ...), and the binnacle names those checks
// instead of calling the run or the block ready. A block still open is left
// to the existing "still has open blocks" refusal; a stopped block's checks
// are not open (humancheck.Open). With every check cleared, nothing changes.

// rehHeld reports whether any block past open has a human_check clause, so a
// run without one is not read for clear rows at all.
func rehHeld(record ledger.Object) bool {
	for _, field := range blockObject(record.Get("blocks")) {
		entry := blockObject(field.Value)
		if entry == nil || !rehPastOpen(entry.Get("status")) {
			continue
		}
		clauses, _ := entry.Get("acceptance").([]any)
		for _, c := range clauses {
			if blockObject(c).Get("kind") == "human_check" {
				return true
			}
		}
	}
	return false
}

func rehPastOpen(status any) bool { return status != "open" && status != "stopped" }

// rehHolding keeps the open checks whose block is past open.
func rehHolding(record ledger.Object, open []humancheck.Entry) []humancheck.Entry {
	blocks := blockObject(record.Get("blocks"))
	out := []humancheck.Entry{}
	for _, e := range open {
		if rehPastOpen(blockObject(blocks.Get(e.Block)).Get("status")) {
			out = append(out, e)
		}
	}
	return out
}

// rehOpenHolding reads the project ledger and the run key and returns the
// checks that hold the run's end.
func rehOpenHolding(home, runID string, record ledger.Object) ([]humancheck.Entry, error) {
	if !rehHeld(record) {
		return nil, nil
	}
	slug, _ := record.Get("project").(string)
	if slug == "" {
		return nil, &runFail{fmt.Sprintf("run %s names no project, so its human checks cannot be read", runID), false}
	}
	rows, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge", "ledger"), slug)
	if err != nil {
		return nil, err
	}
	key, _ := blockclose.ReadKey(home, runID) // no valid key: no clear row verifies
	return rehHolding(record, humancheck.Open(runID, record, rows, key)), nil
}

// rehState names a holding check's block state: deferred, awaiting, or the
// block's status.
func rehState(record ledger.Object, e humancheck.Entry) string {
	switch status := blockObject(blockObject(record.Get("blocks")).Get(e.Block)).Get("status"); status {
	case humancheck.DeferredStatus:
		return "deferred"
	case "awaiting_human_check":
		return "awaiting"
	default:
		if s, ok := status.(string); ok && s != "" {
			return s
		}
		return "unknown"
	}
}

// rehClearCommand is the owner's command that clears one check.
func rehClearCommand(runID string, e humancheck.Entry) string {
	return "code-forge run human-checks clear --run " + runID + " --block " + e.Block + " --clause " + e.Clause + " --note \"<what you checked>\""
}

// rehHead is the refusal's first line (without the list).
func rehHead(runID string, n int) string {
	return fmt.Sprintf("run %s has %d open human check(s); it cannot end until each is cleared", runID, n)
}

// rehRefusalText is the text refusal: the head, then each check and its
// clear command.
func rehRefusalText(runID string, record ledger.Object, held []humancheck.Entry) string {
	var b strings.Builder
	b.WriteString(rehHead(runID, len(held)) + ":\n")
	for _, e := range held {
		detail := rehState(record, e)
		if w, ok := e.WaitsFor.(string); ok {
			detail += "; waits for: " + w
		}
		fmt.Fprintf(&b, "  %s %s %s (%s)\n    %s\n", e.Block, e.Clause, statusQuote(e.Check), detail, rehClearCommand(runID, e))
	}
	return b.String()
}

// rehRefusalJSON is the --json refusal document.
func rehRefusalJSON(runID string, record ledger.Object, held []humancheck.Entry) string {
	list := make([]any, len(held))
	for i, e := range held {
		o := e.Object()
		o.Set("state", rehState(record, e))
		o.Set("clear", rehClearCommand(runID, e))
		list[i] = o
	}
	data, _ := ledger.Stringify(ledger.Object{{Key: "run", Value: runID}, {Key: "ended", Value: false}, {Key: "refused", Value: "human-checks-open"}, {Key: "open", Value: list}}, false)
	return string(data) + "\n"
}

// rehHeldError carries the holding checks out of the run lock.
type rehHeldError struct {
	record ledger.Object
	held   []humancheck.Entry
}

func (e *rehHeldError) Error() string { return "human checks are open" }

// --- binnacle (autopilot binnacle / record) ---

// bnHumanChecksItem is the status value naming the holding checks.
func bnHumanChecksItem(runID string, held []humancheck.Entry) string {
	names := make([]string, len(held))
	for i, e := range held {
		names[i] = e.Block + " " + e.Clause
	}
	return fmt.Sprintf("%d open, so the run cannot end: %s (code-forge run human-checks list --run %s)", len(held), strings.Join(names, ", "), runID)
}

// bnHeldFor returns the holding checks of one block.
func bnHeldFor(held []humancheck.Entry, id string) []humancheck.Entry {
	out := []humancheck.Entry{}
	for _, e := range held {
		if e.Block == id {
			out = append(out, e)
		}
	}
	return out
}

// bnHeldState is the block state of a closed block with open checks.
func bnHeldState(n int) string {
	if n == 1 {
		return "Closed, but 1 human check is open; not ready"
	}
	return fmt.Sprintf("Closed, but %d human checks are open; not ready", n)
}

// bnHeldAction is the owner action for a closed block with open checks.
func bnHeldAction(runID, id string, held []humancheck.Entry) string {
	checks, clauses := []string{}, []string{}
	for _, e := range held {
		text := e.Clause + " " + statusQuote(e.Check)
		if w, ok := e.WaitsFor.(string); ok {
			text += " (waits for: " + w + ")"
		}
		checks = append(checks, text)
		clauses = append(clauses, e.Clause)
	}
	clause := clauses[0]
	if len(clauses) > 1 {
		clause = "<" + strings.Join(clauses, "|") + ">"
	}
	return "Check block " + id + " by hand: " + strings.Join(checks, "; ") + "; then clear each with code-forge run human-checks clear --run " + runID + " --block " + id + " --clause " + clause + " --note \"<what you checked>\""
}
