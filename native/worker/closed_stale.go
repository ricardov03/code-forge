package worker

// B919: a queued ticket of a closed block for a deleted file (content_hash
// "deleted") was still served: the worker started a review session for a
// file that is gone, of a block whose gate had already passed (the
// customer's queue, bug note 2026-11-19). The worker now drops it as stale
// with reason block-closed before any review: a signed stale result, its
// done marker and one signed review.result row (status stale, reason
// block-closed) name the ticket. A stale row is never-reviewed to the gate,
// so it changes no close or land. A rebase-check ticket (it names the
// block's tree) and a ticket for a file that exists are still served: a
// closed block is re-reviewed after a rebase.

import (
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
)

// ClosedBlockReason is the reason of a dropped stale ticket of a closed block.
const ClosedBlockReason = "block-closed"

// closedBlockStatuses are the run record statuses of a closed block
// ("closed_deferred_human" is humancheck.DeferredStatus).
var closedBlockStatuses = map[string]bool{"closed": true, "closed_deferred_human": true}

// closedBlockStale is true when ticket t is a stale ticket of a closed
// block: its content is a deleted file, it names no tree, and the run
// record marks its block closed or landed. An unreadable record is false
// (the ticket's other checks decide).
func (w *TicketRunner) closedBlockStale(t ledger.Object) bool {
	if t.Get("content_hash") != gitx.DeletedHash || hasKey(t, "tree") {
		return false
	}
	block, _ := t.Get("block").(string)
	rec, syntax, err := w.readRecord()
	if err != nil || syntax || rec == nil {
		return false
	}
	blocks, _ := rec.Get("blocks").(ledger.Object)
	entry, _ := blocks.Get(block).(ledger.Object)
	if entry == nil {
		return false
	}
	status, _ := entry.Get("status").(string)
	return closedBlockStatuses[status] || entry.Get("landed") != nil
}
