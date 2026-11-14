package worker

import (
	"github.com/ricardov03/code-forge/native/review"
	"github.com/ricardov03/code-forge/native/sign"
)

// needsFileCarry is B627's read-back for review.Deps.NeedsFileCarry: the
// file's last signed review.needs_file_timeout row of this run and block,
// when it allows a retry (retry true) and no signed review.result row after
// it already used that retry (a session with needs_file_carried). Unsigned
// or forged rows are never read; a ledger read failure is no carry.
func needsFileCarry(readRows func() ([]any, error), key []byte, runID, block, file string) func() (*review.NeedsFileCarry, error) {
	return func() (*review.NeedsFileCarry, error) {
		rows, err := readRows()
		if err != nil {
			return nil, nil
		}
		var last map[string]any
		for _, raw := range rows {
			r, ok := raw.(map[string]any)
			if !ok || r["run"] != runID || r["block"] != block || r["file"] != file {
				continue
			}
			ev := r["event"]
			if ev != "review.needs_file_timeout" && ev != "review.result" {
				continue
			}
			if res, err := sign.VerifyRow(r, key); err != nil || !res.OK {
				continue
			}
			if ev == "review.needs_file_timeout" {
				last = r
			} else if last != nil && usedCarry(r["sessions"]) {
				last = nil
			}
		}
		if last == nil || last["retry"] != true {
			return nil, nil
		}
		attempt, _ := last["attempt"].(float64)
		list, _ := last["requested_paths"].([]any)
		paths := []string{}
		for _, p := range list {
			if s, ok := p.(string); ok && s != "" {
				paths = append(paths, s)
			}
		}
		if len(paths) == 0 || attempt < 1 {
			return nil, nil
		}
		return &review.NeedsFileCarry{Paths: paths, Attempt: int(attempt) + 1}, nil
	}
}

// usedCarry: a review.result row's sessions include a carried retry.
func usedCarry(v any) bool {
	list, _ := v.([]any)
	for _, s := range list {
		if m, ok := s.(map[string]any); ok && m["needs_file_carried"] != nil {
			return true
		}
	}
	return false
}
