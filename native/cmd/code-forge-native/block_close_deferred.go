package main

import (
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"time"

	"github.com/ricardov03/code-forge/native/autopilot"
	"github.com/ricardov03/code-forge/native/blockclose"
	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/humancheck"
	"github.com/ricardov03/code-forge/native/ledger"
)

// B632 (issue #37 B, part 2): scope human-pass:deferred. Under an active
// autopilot grant that allows it (and does not deny it), a block that passed
// every close gate and whose only open items are human_check clauses closes
// as closed_deferred_human instead of awaiting_human_check. Its clauses stay
// open in the run's human-check ledger (B631) for the owner to clear with
// `run human-checks clear`; one signed autopilot.decision row records the
// deferral. Without the scope nothing changes.

// closeDeferNow is the clock seam for the grant read under the run lock.
var closeDeferNow = time.Now

// closeDeferClearCommand is the owner's command for one deferred clause.
func closeDeferClearCommand(run, id, clause string) string {
	return fmt.Sprintf("code-forge run human-checks clear --run %s --block %s --clause %s --note \"<what you checked>\"", run, id, clause)
}

// closeDeferral decides under the run lock. The grant must be active with
// the scope and have its signed autopilot.grant row; the block must have at
// least one open human check. *row receives the decision row it returned.
func closeDeferral(home, run, id string, row *ledger.Object) blockclose.Deferral {
	return func(record, _ ledger.Object) ledger.Object {
		now := closeDeferNow()
		grant, err := autopilot.ReadGrant(record, now)
		if err != nil || autopilot.ScopeRefusal(grant, autopilot.DeferHumanScope) != "" {
			return nil
		}
		key, err := blockclose.ReadKey(home, run)
		if err != nil {
			return nil
		}
		slug, _ := record.Get("project").(string)
		if slug == "" {
			return nil
		}
		all, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge", "ledger"), slug)
		if err != nil {
			return nil
		}
		rows := []gatecheck.Row{}
		for _, v := range all {
			if r, ok := v.(map[string]any); ok {
				rows = append(rows, r)
			}
		}
		grantID := blockString(grant.Grant.Get("grant_id"))
		if autopilotGrantRow(rows, run, grantID, key) == nil {
			return nil
		}
		clauses, waits := []any{}, []any{}
		for _, e := range humancheck.Open(run, record, all, key) {
			if e.Block != id {
				continue
			}
			clauses = append(clauses, ledger.Object{{Key: "clause", Value: e.Clause}, {Key: "check", Value: e.Check}, {Key: "waits_for", Value: e.WaitsFor}})
			if s, ok := e.WaitsFor.(string); ok && !slices.Contains(waits, any(s)) {
				waits = append(waits, s)
			}
		}
		if len(clauses) == 0 {
			return nil
		}
		decisionID, err := autopilotDecisionID()
		if err != nil {
			return nil
		}
		*row = ledger.Object{
			{Key: "event", Value: "autopilot.decision"}, {Key: "decision_id", Value: decisionID}, {Key: "grant_id", Value: grantID},
			{Key: "scope", Value: autopilot.DeferHumanScope}, {Key: "subject", Value: ledger.Object{{Key: "block", Value: id}}},
			{Key: "question", Value: "close block " + id + " with its human checks deferred to the owner"},
			{Key: "options", Value: []any{"defer", "owner"}}, {Key: "decision", Value: "defer"}, {Key: "within_scope", Value: true},
			{Key: "reason", Value: "every close gate passed; only human checks remain open"},
			{Key: "escalate", Value: false}, {Key: "acted", Value: true}, {Key: "to_owner", Value: false}, {Key: "level", Value: "agent"},
			{Key: "block", Value: id}, {Key: "clauses", Value: clauses}, {Key: "waits_for", Value: waits}, {Key: "ts", Value: autopilotStamp(now)},
		}
		return *row
	}
}

// closeDeferredChecks is the decision row's clauses with each clear command.
func closeDeferredChecks(run, id string, row ledger.Object) []any {
	out := []any{}
	clauses, _ := row.Get("clauses").([]any)
	for _, v := range clauses {
		c := blockObject(v)
		o := c.Clone()
		o.Set("clear_command", closeDeferClearCommand(run, id, blockString(c.Get("clause"))))
		out = append(out, o)
	}
	return out
}

// closeDeferredText prints the deferred close for the orchestrator.
func closeDeferredText(stdout io.Writer, run, id string, row ledger.Object) {
	fmt.Fprintf(stdout, "block %s %s (scope %s, decision %s): its human checks stay open for the owner\n", id, humancheck.DeferredStatus, autopilot.DeferHumanScope, blockString(row.Get("decision_id")))
	for _, v := range closeDeferredChecks(run, id, row) {
		c := blockObject(v)
		waits := ""
		if s, ok := c.Get("waits_for").(string); ok {
			waits = "; waits for: " + s
		}
		fmt.Fprintf(stdout, "block %s deferred human check %s: %s%s\n", id, c.Get("clause"), statusQuote(blockString(c.Get("check"))), waits)
		fmt.Fprintf(stdout, "owner: %s\n", c.Get("clear_command"))
	}
}
