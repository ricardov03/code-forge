package worker

// B918: a ticket with finished_at but no done marker is finished. Before,
// complete wrote the ticket's finish timings first, then the result and the
// done marker; a crash or a failed write in between left the ticket queued
// with finished_at set, so every new worker took it first (the customer's
// stale ticket of a closed block, content_hash "deleted"). complete now
// writes the finish timings after the done marker, and every re-queue clears
// them before it drops the marker (queue.ResetTimings), so finished_at with
// no done marker only remains from such an interrupted finish.
//
// The drain closes such a ticket instead of serving it again: the done
// marker is rebuilt from the ticket's signed result of that same finish
// (status, timings and its review.result row, as complete writes them), else
// a signed `recovered` result is written; one signed review.recovered row
// names the ticket. A `recovered` result is re-queued by review-file, like an
// unsupported one.

import (
	"path/filepath"

	"github.com/ricardov03/code-forge/native/ledger"
)

// RecoveredStatus is the done marker status of a finished ticket that had
// no done marker and no result of that finish.
const RecoveredStatus = "recovered"

// RecoveredEvent is the signed ledger row of a recovered ticket.
const RecoveredEvent = "review.recovered"

// RecoveredReason is the reason of a recovered result.
const RecoveredReason = "finished-without-done-marker"

// recoverFinished closes ticket id when it has finished_at but no done
// marker: true when it did (the ticket is not served). A ticket of another
// run is left to decideRead.
func (w *TicketRunner) recoverFinished(id string) (bool, error) {
	if !ticketID.MatchString(id) || IsDone(w.RepoRoot, id) {
		return false, nil
	}
	t, err := readJSON(filepath.Join(QueueDir(w.RepoRoot), id+".json"))
	if err != nil || t == nil {
		return false, nil
	}
	finishedAt, ok := t.Get("finished_at").(string)
	if !ok || finishedAt == "" || t.Get("run") != w.RunID {
		return false, nil
	}
	resultFile := filepath.Join(w.RepoRoot, ".code-forge", "reviews", w.RunID, id+".json")
	status := RecoveredStatus
	if r, err := readJSON(resultFile); err == nil && r != nil && r.Get("ticket") == id && r.Get("run") == w.RunID && r.Get("finished_at") == finishedAt {
		if s, ok := r.Get("status").(string); ok && s != "" {
			// the result of that same finish: publish what complete would have
			timings := ledger.Object{}
			for _, k := range []string{"queued_at", "started_at", "finished_at", "duration_ms"} {
				timings = append(timings, ledger.Field{Key: k, Value: r.Get(k)})
			}
			if err := w.publishResult(id, r, measuredOf(r), timings); err != nil {
				return false, err
			}
			status = s
		}
	}
	if status == RecoveredStatus {
		result := ledger.Object{{Key: "block", Value: t.Get("block")}, {Key: "file", Value: t.Get("file")}, {Key: "content_hash", Value: t.Get("content_hash")},
			{Key: "status", Value: RecoveredStatus}, {Key: "reason", Value: RecoveredReason}, {Key: "approved", Value: false},
			{Key: "started_at", Value: t.Get("started_at")}, {Key: "finished_at", Value: finishedAt}, {Key: "worker", Value: w.Worker}, {Key: "run", Value: w.RunID}, {Key: "ticket", Value: id}}
		signed, err := w.signObject(result)
		if err != nil {
			return false, err
		}
		data, err := line(signed)
		if err != nil {
			return false, err
		}
		if err := writeAtomic(resultFile, data, 0o644); err != nil {
			return false, err
		}
		rel, _ := filepath.Rel(w.RepoRoot, resultFile)
		done := ledger.Object{{Key: "ticket", Value: id}, {Key: "status", Value: RecoveredStatus}, {Key: "reason", Value: RecoveredReason}, {Key: "finished_at", Value: finishedAt}, {Key: "result", Value: rel}}
		if data, err = line(done); err != nil {
			return false, err
		}
		if err := writeAtomic(filepath.Join(QueueDir(w.RepoRoot), id+".done"), data, 0o644); err != nil {
			return false, err
		}
	}
	row := ledger.Object{{Key: "run", Value: w.RunID}, {Key: "event", Value: RecoveredEvent}, {Key: "ticket", Value: id},
		{Key: "block", Value: t.Get("block")}, {Key: "file", Value: t.Get("file")}, {Key: "content_hash", Value: t.Get("content_hash")},
		{Key: "status", Value: status}, {Key: "finished_at", Value: finishedAt}, {Key: "worker", Value: w.Worker}}
	if signedRow, err := w.signObject(row); err == nil {
		_ = w.writeRow(signedRow)
	}
	return true, nil
}
