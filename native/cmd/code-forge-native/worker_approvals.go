package main

import (
	"errors"
	"time"

	"github.com/ricardov03/code-forge/native/autopilot"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B616: the worker drain's checkApprovals (src/autopilot/limits.mjs, run by
// expiryChecks after the grant expiry) in Go, on the restore autopilot
// commands and block rebase already run (autopilot_restore.go). The worker
// drops the check's answer, as loop.mjs does: nothing is printed for a
// restored, skipped or failed approval; a thrown check is the drain's
// "expiry check failed" warning. An approval the restore plan does not serve
// keeps the worker's hand-over to Node, decided before anything is written.

// workerApprovalsTypeError is the TypeError Node's `a.status` throws on a
// null entry in autopilot_approvals (approvalsOf(record).some(...)); every
// pass over the list reaches it, so the check always throws.
type workerApprovalsTypeError struct{}

func (workerApprovalsTypeError) Error() string {
	return "Cannot read properties of null (reading 'status')"
}
func (workerApprovalsTypeError) Kind() string { return "TypeError" }

// workerApprovalsNull: the run's approvals list holds a JSON null.
func workerApprovalsNull(record ledger.Object) bool {
	values, _ := record.Get("autopilot_approvals").([]any)
	for _, value := range values {
		if value == nil {
			return true
		}
	}
	return false
}

// workerApprovalsServed is the Loop's pre-check: Go serves checkApprovals on
// record at now (it writes nothing).
func workerApprovalsServed(home, run string) func(ledger.Object, time.Time) bool {
	return func(record ledger.Object, now time.Time) bool {
		if workerApprovalsNull(record) {
			return true
		}
		_, reason := autopilotRestorePlan(home, run, record, now)
		return reason == 0
	}
}

// workerCheckApprovals is checkApprovals for the worker: handOver is true
// when the restore found, before writing, a step Go does not serve.
func workerCheckApprovals(home, run string) func(time.Time) (bool, error) {
	return func(now time.Time) (bool, error) {
		record, err := state.ReadRun(home, run)
		if err != nil {
			if autopilot.IsNoRun(err) {
				return false, nil
			}
			return false, err
		}
		if workerApprovalsNull(record) {
			return false, workerApprovalsTypeError{}
		}
		switch err := autopilot.Approvals(record, now); {
		case err == nil:
			return false, nil // no approval is due: Node writes nothing
		case !errors.Is(err, autopilot.ErrApprovalDue):
			return true, nil
		}
		_, restoreErr, reason := autopilotRestore(home, run, record, now)
		if reason != 0 {
			return true, nil
		}
		return false, restoreErr
	}
}
