package state

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
)

// ErrUnchanged ends an UpdateRun change without saving the record.
var ErrUnchanged = errors.New("run record unchanged")

// UpdateRun is the short read-modify-write of a run record (B426): lock,
// re-read the record, apply change, save, unlock. change must never run git,
// tests, builds, network calls or sleeps; do that work before, on a snapshot
// read with ReadRun, and commit only the result here. A change that returns
// ErrUnchanged saves nothing and UpdateRun returns nil.
func UpdateRun(home, runID string, change func(record *ledger.Object) error) error {
	return WithRunLock(home, runID, func() error {
		record, err := ReadRun(home, runID)
		if err != nil {
			return err
		}
		if err := change(&record); err != nil {
			if errors.Is(err, ErrUnchanged) {
				return nil
			}
			return err
		}
		return SaveRun(home, record)
	})
}

// BlockChanged is the refusal of a verb whose block was changed by another
// verb while it worked without the run lock.
func BlockChanged(runID, blockID string) error {
	return &Error{"block-changed", fmt.Sprintf("block-changed: block %s of run %s was changed by another verb while this one ran; nothing was saved for it; run the verb again", blockID, runID)}
}

// CloneObject is a deep copy of an ordered object (nested objects included).
func CloneObject(o ledger.Object) ledger.Object {
	data, err := ledger.Stringify(o, false)
	if err != nil {
		return o.Clone()
	}
	clone, err := ledger.DecodeObject(data)
	if err != nil {
		return o.Clone()
	}
	return clone
}

func sameObject(a, b ledger.Object) bool {
	x, e1 := ledger.Stringify(a, false)
	y, e2 := ledger.Stringify(b, false)
	return e1 == nil && e2 == nil && bytes.Equal(x, y)
}

// CommitBlocks publishes the blocks one verb owns with a short UpdateRun. On
// the fresh record each block in updates must still equal its expected
// snapshot (what the verb read or last committed), else block-changed names
// it and nothing is saved. Only those block keys change, so verbs on other
// blocks never lose an update. rows are signed in order before the save, the
// same WriteSigned/SaveRun order as Node.
func CommitBlocks(home, runID string, expected, updates map[string]ledger.Object, rows ...ledger.Object) error {
	ids := make([]string, 0, len(updates))
	for id := range updates {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return UpdateRun(home, runID, func(record *ledger.Object) error {
		blocks, _ := record.Get("blocks").(ledger.Object)
		for _, id := range ids {
			current, _ := blocks.Get(id).(ledger.Object)
			if !sameObject(current, expected[id]) {
				return BlockChanged(runID, id)
			}
		}
		for _, id := range ids {
			blocks.Set(id, updates[id])
		}
		record.Set("blocks", blocks)
		for _, row := range rows {
			if _, err := WriteSigned(home, runID, row); err != nil {
				return err
			}
		}
		return nil
	})
}

// WithBlockLock keeps two long verbs off the same block (a rebase or a land
// works in its worktree for minutes) without holding the run lock. It is the
// run lock's design on a per-block dir: owner.json names the holder, a lock
// whose owner is gone is broken once (B423), a live one is waited on up to the
// run lock deadline, then refused with block-busy.
func WithBlockLock(home, runID, blockID string, fn func() error) (err error) {
	if _, err := recordPath(home, runID); err != nil {
		return err
	}
	if err := os.MkdirAll(runsDir(home), 0700); err != nil {
		return err
	}
	lock := filepath.Join(runsDir(home), runID+".block-"+hex.EncodeToString([]byte(blockID))+".lock")
	deadline := time.Now().Add(lockTimeout)
	for {
		err := os.Mkdir(lock, 0755)
		if err == nil {
			break
		}
		if !os.IsExist(err) {
			return err
		}
		took, breakErr := breakStaleLock(runsDir(home), lock)
		if breakErr != nil {
			return breakErr
		}
		if took {
			break
		}
		if time.Now().After(deadline) {
			return &Error{"block-busy", fmt.Sprintf("block-busy: block %s of run %s is in use by another verb (%s); try again when it ends", blockID, runID, lock)}
		}
		time.Sleep(lockPoll)
	}
	defer func() {
		// owner.json goes first, then the dir.
		removeLockOwner(lock)
		if releaseErr := os.Remove(lock); releaseErr != nil && err == nil {
			err = releaseErr
		}
	}()
	if err := writeLockOwner(lock); err != nil {
		return err
	}
	return fn()
}

// WithBlockLocks holds the block locks of every id (sorted, so two verbs
// never take them in opposite order).
func WithBlockLocks(home, runID string, ids []string, fn func() error) error {
	sorted := append([]string{}, ids...)
	sort.Strings(sorted)
	var take func(i int) error
	take = func(i int) error {
		if i == len(sorted) {
			return fn()
		}
		if i > 0 && sorted[i] == sorted[i-1] {
			return take(i + 1)
		}
		return WithBlockLock(home, runID, sorted[i], func() error { return take(i + 1) })
	}
	return take(0)
}
