//go:build unix

package daemon

import (
	"encoding/json"
	"errors"
	"math"

	"github.com/ricardov03/code-forge/native/runstatus"
)

// The status answer is computed in Go over the daemon's cached rows (B576: no
// Node companion). The one case Go cannot order like Node, a block id outside
// printable ASCII among two or more listed blocks (ICU localeCompare), answers
// status-unreadable, as the daemon did with no Node; the status client then
// computes the run's status itself.
func (s *server) status() object {
	if summary, ok := runstatus.Compute(s.record, s.rows, s.home); ok {
		// Decode once to preserve the daemon's map-shaped protocol envelope.
		data, err := json.Marshal(summary)
		var decoded object
		if err == nil && json.Unmarshal(data, &decoded) == nil {
			return object{"runs": []any{decoded}}
		}
	}
	if result, ok := s.statusBrief(); ok {
		return result
	}
	return failure("status-unreadable")
}

// statusBrief is the Node companion's answer in Go (B503, after B501's
// native-status rules): statusOf(record, rows, {gateDetails: false}), a throw
// written as status-unreadable, a non-finite spend written as null by
// JSON.stringify, then the daemon's own gate details. ok is false only for a
// block id outside printable ASCII among two or more listed blocks, which
// needs Node's ICU localeCompare, or a summary Go cannot encode.
func (s *server) statusBrief() (object, bool) {
	summary, err := runstatus.ComputeBriefErr(s.record, s.rows, s.home)
	if errors.Is(err, runstatus.ErrUnorderedBlockID) {
		return nil, false
	}
	if err != nil {
		return failure("status-unreadable"), true
	}
	if n, ok := summary.Get("spent_usd").(float64); ok && (math.IsInf(n, 0) || math.IsNaN(n)) {
		summary.Set("spent_usd", nil)
	}
	data, err := json.Marshal(summary)
	var decoded object
	if err != nil || json.Unmarshal(data, &decoded) != nil {
		return nil, false
	}
	result := object{"runs": []any{decoded}}
	s.statusGateDetails(result)
	return result, true
}

// Match Node timeoutStatus using the ledger rows already cached by refresh.
// A failed gate.done retains a timeout only from its pending gate.red; another
// done without a red, a successful done, or a non-timeout red clears it.
func (s *server) statusGateDetails(result object) {
	runs, _ := result["runs"].([]any)
	for _, value := range runs {
		run := mapping(value)
		if run["run_id"] != s.run {
			continue
		}
		runstatus.Details(run, s.rows, s.run)
	}
}
