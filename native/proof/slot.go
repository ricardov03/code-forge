// Package proof shares the authoritative run-record proof slot with Node.
package proof

import (
	"fmt"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
	"os"
)

// WithSlot sets the running marker under the run lock and clears it even on error.
// The proof lock is a field of the record, not a separate file lock.
func WithSlot(home, run, block string, fn func(ledger.Object, ledger.Object) error) (holder string, err error) {
	var record, entry ledger.Object
	err = state.WithRunLock(home, run, func() error {
		var e error
		record, e = state.ReadRun(home, run)
		if e != nil {
			return e
		}
		if record.Get("status") != "active" {
			return fmt.Errorf("run %s has ended", run)
		}
		blocks, _ := record.Get("blocks").(ledger.Object)
		entry, _ = blocks.Get(block).(ledger.Object)
		if entry.Get("status") != "open" {
			return fmt.Errorf("block %s is not open in run %s", block, run)
		}
		lock, _ := record.Get("proof_lock").(ledger.Object)
		holder, _ = lock.Get("block").(string)
		if holder != "" && holder != block {
			return nil
		}
		holder = ""
		entry.Set("gate_running", true)
		entry.Set("gate_pid", os.Getpid())
		blocks.Set(block, entry)
		record.Set("blocks", blocks)
		return state.SaveRun(home, record)
	})
	if err != nil || holder != "" {
		return
	}
	defer func() {
		clean := state.WithRunLock(home, run, func() error {
			r, e := state.ReadRun(home, run)
			if e != nil {
				return e
			}
			bs, _ := r.Get("blocks").(ledger.Object)
			b, ok := bs.Get(block).(ledger.Object)
			if !ok {
				return nil
			}
			b.Delete("gate_running")
			b.Delete("gate_pid")
			bs.Set(block, b)
			r.Set("blocks", bs)
			return state.SaveRun(home, r)
		})
		if clean != nil {
			err = clean
		}
	}()
	err = fn(record, entry)
	return
}
