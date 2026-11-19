package autopilot

import "github.com/ricardov03/code-forge/native/ledger"

// B800: work found during a run (a gate or land failure, a coder's
// leftover, a flaky test, a hand-over hit, a review) is recorded in the
// run record's `found` list by `code-forge autopilot found`. An entry the
// autopilot queued carries the id of the block it became; that block joins
// the run's planned blocks at once, so an until-done grant does not end
// before the fix is closed.

// FoundQueued lists the block ids of the run's queued found entries, in the
// order they were found. Entries that wait for the owner have no block.
func FoundQueued(record ledger.Object) []string {
	list, _ := record.Get("found").([]any)
	out := []string{}
	for _, item := range list {
		entry, ok := item.(ledger.Object)
		if !ok || entry.Get("state") != "queued" {
			continue
		}
		if id, ok := entry.Get("block").(string); ok && id != "" {
			out = append(out, id)
		}
	}
	return out
}
