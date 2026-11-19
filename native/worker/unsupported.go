package worker

// B884: a ticket Go cannot serve fails ONLY that ticket. Before, the first
// such ticket stopped the whole worker (the Node hand-over of B477, a plain
// exit since B690a); the ticket stayed queued, so every new worker claimed
// it first and died the same way (a poison loop), with no ledger row.
//
// Now the ticket is closed as `unsupported`: a signed result file, a done
// marker {ticket, status: "unsupported", reason, result} (the queue skips it)
// and one signed `review.unsupported` row naming the ticket, block, file and
// the reason (redacted). No review.result row is written, so the file stays
// unreviewed; block ready names it with the fix (a fresh review-file, else
// the owner's waive or --decide). The worker goes on serving.

import (
	"errors"
	"path/filepath"

	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/ledger"
)

// UnsupportedStatus is the done marker status of a ticket Go cannot serve.
const UnsupportedStatus = "unsupported"

// UnsupportedEvent is the signed ledger row of an unsupported ticket.
const UnsupportedEvent = "review.unsupported"

// Unsupported closes ticket id as unsupported with reason; it returns an
// error only when the done marker could not be written (the caller then
// skips the ticket for this worker's life).
func (w *TicketRunner) Unsupported(id, reason string) error {
	if !ticketID.MatchString(id) {
		return errors.New("a ticket id is 24 lowercase hex characters")
	}
	reason = gates.Mask(reason, w.secrets())
	var block, file, hash any
	if t, err := readJSON(filepath.Join(QueueDir(w.RepoRoot), id+".json")); err == nil && t != nil {
		block, file, hash = t.Get("block"), t.Get("file"), t.Get("content_hash")
	}
	at := iso(w.now())
	result := ledger.Object{{Key: "block", Value: block}, {Key: "file", Value: file}, {Key: "content_hash", Value: hash},
		{Key: "status", Value: UnsupportedStatus}, {Key: "reason", Value: reason}, {Key: "approved", Value: false},
		{Key: "finished_at", Value: at}, {Key: "worker", Value: w.Worker}, {Key: "run", Value: w.RunID}, {Key: "ticket", Value: id}}
	signed, err := w.signObject(result)
	if err != nil {
		return err
	}
	data, err := line(signed)
	if err != nil {
		return err
	}
	resultFile := filepath.Join(w.RepoRoot, ".code-forge", "reviews", w.RunID, id+".json")
	if err := writeAtomic(resultFile, data, 0o644); err != nil {
		return err
	}
	rel, _ := filepath.Rel(w.RepoRoot, resultFile)
	done := ledger.Object{{Key: "ticket", Value: id}, {Key: "status", Value: UnsupportedStatus}, {Key: "reason", Value: reason}, {Key: "finished_at", Value: at}, {Key: "result", Value: rel}}
	if data, err = line(done); err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(QueueDir(w.RepoRoot), id+".done"), data, 0o644); err != nil {
		return err
	}
	row := ledger.Object{{Key: "run", Value: w.RunID}, {Key: "event", Value: UnsupportedEvent}, {Key: "ticket", Value: id},
		{Key: "block", Value: block}, {Key: "file", Value: file}, {Key: "content_hash", Value: hash}, {Key: "reason", Value: reason}, {Key: "worker", Value: w.Worker}}
	if signedRow, err := w.signObject(row); err == nil {
		_ = w.writeRow(signedRow)
	}
	return nil
}

// ExitEvent is the signed row a serving worker writes when it stops (B884).
const ExitEvent = "worker.exit"

// ExitRow writes the signed worker.exit {reason, detail} row; detail is
// masked with the run's secrets.
func (w *TicketRunner) ExitRow(reason, detail string, identity any) error {
	return w.Ledger(ledger.Object{{Key: "event", Value: ExitEvent}, {Key: "ts", Value: ISO(w.now())},
		{Key: "reason", Value: reason}, {Key: "detail", Value: gates.Mask(detail, w.secrets())}, {Key: "worker", Value: identity}})
}
