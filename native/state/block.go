package state

import (
	"fmt"

	"github.com/ricardov03/code-forge/native/ledger"
)

// BlockMutation changes ordered record objects and returns rows to publish.
// An empty blockID skips the open-block check (Node mutateRun's null id).
type BlockMutation func(record, block *ledger.Object) ([]ledger.Object, error)

// UpdateBlock mirrors block.mjs mutateRun: lock, read, validate, mutate, sign
// every row, then save. Failed mutation/signing never saves the changed record.
// This is serialized publication, not a rollback transaction: as in Node a
// later row/save failure can leave earlier signed rows in the ledger.
func UpdateBlock(home, runID, blockID string, change BlockMutation) error {
	return WithRunLock(home, runID, func() error {
		record, err := ReadRun(home, runID)
		if err != nil {
			return err
		}
		if record.Get("status") != "active" {
			return &Error{"run-ended", fmt.Sprintf("run %s has ended", runID)}
		}
		var block, blocks ledger.Object
		if blockID != "" {
			blocks, _ = record.Get("blocks").(ledger.Object)
			block, _ = blocks.Get(blockID).(ledger.Object)
			if block.Get("status") != "open" {
				return &Error{"no-block", fmt.Sprintf("block %s is not open in run %s", blockID, runID)}
			}
		}
		var target *ledger.Object
		if blockID != "" {
			target = &block
		}
		rows, err := change(&record, target)
		if err != nil {
			return err
		}
		if blockID != "" {
			blocks, _ = record.Get("blocks").(ledger.Object)
			blocks.Set(blockID, block)
			record.Set("blocks", blocks)
		}
		for _, row := range rows {
			if _, err := WriteSigned(home, runID, row); err != nil {
				return err
			}
		}
		return SaveRun(home, record)
	})
}
