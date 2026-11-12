package worker

import (
	"os"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B504: review-state.mjs missingStateFiles for doctor's `review state` row.

// MissingState is one state file the latest anchor names but no disk holds.
type MissingState struct {
	Block, File string
	Rebuildable bool
}

// MissingStateFiles is review-state.mjs missingStateFiles: per block of the
// run (first-seen order), per file of its anchors, the files whose latest
// anchor has no state file at the new path (nor at the legacy path under
// runRootDir when it is not ""). ok is false when an anchor of the run does
// not verify (Node returns null).
func MissingStateFiles(home, runID string, key []byte, rows []ledger.Object, runRootDir string) ([]MissingState, bool) {
	var blocks []string
	seen := map[string]bool{}
	found := false
	for _, r := range rows {
		if r.Get("event") != StateEvent || r.Get("run") != runID {
			continue
		}
		found = true
		if b, ok := r.Get("block").(string); ok && !seen[b] {
			seen[b] = true
			blocks = append(blocks, b)
		}
	}
	out := []MissingState{}
	if !found {
		return out, true
	}
	// blockAnchors checks every anchor of the run on the first block, whatever its type.
	if _, ok := blockAnchors(runID, "", key, rows); !ok {
		return nil, false
	}
	for _, block := range blocks {
		anchors, _ := blockAnchors(runID, block, key, rows)
		var files []string
		seenFile := map[string]bool{}
		for _, a := range anchors {
			if f, ok := a.Get("file").(string); ok && !seenFile[f] {
				seenFile[f] = true
				files = append(files, f)
			}
		}
		for _, file := range files {
			anchor := latestAnchor(anchors, file)
			if anchor == nil || !runIDPattern.MatchString(runID) || !blockIDPattern.MatchString(block) || rowPathError(file) {
				continue // a block id or a path no state file can have
			}
			paths := []string{StatePath(home, runID, block, file)}
			if runRootDir != "" {
				paths = append(paths, LegacyStatePath(runRootDir, block, file))
			}
			there := false
			for _, p := range paths {
				if _, err := os.Stat(p); err == nil {
					there = true
				}
			}
			if there {
				continue
			}
			_, rebuildable := anchorHas(anchor, "state_body")
			out = append(out, MissingState{Block: block, File: file, Rebuildable: rebuildable})
		}
	}
	return out, true
}

func anchorHas(o ledger.Object, key string) (any, bool) {
	for _, f := range o {
		if f.Key == key {
			return f.Value, true
		}
	}
	return nil, false
}
