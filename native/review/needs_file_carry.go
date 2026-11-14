package review

import "github.com/ricardov03/code-forge/native/ledger"

// B627 (issue #37 C1): a needs_file round whose session timed out is not
// lost. The engine writes one review.needs_file_timeout row (the ticket, the
// lens, the requested paths, the attempt and whether a retry follows); the
// worker reads the file's last such row back (Deps.NeedsFileCarry) and the
// next ticket's reviewer starts with those files attached, in a session of
// its own (each spawn has its own timeout clock). One retry: a timeout of
// the retry writes attempt 2 with retry false, and the file is reviewed as
// before B627 from then on.

// attachCarry is the carried files' section, attached with the needs_file
// round's own rules (AttachFiles: a path outside the project, absolute,
// secret-like, unowned, unchanged or gitignored is refused). ok false when
// the round itself would not run (nothing attachable, no policy refusal).
func attachCarry(paths []string, in Input) (string, bool) {
	changed := in.ChangedFiles
	if changed == nil {
		changed = in.Peers
	}
	var d AttachDiagnostics
	extra := AttachFiles(in.RepoRoot, paths, BudgetFor(in.Cfg, "full_in"), in.OwnedFiles, changed, &d)
	return extra, d.Attached+d.PolicyRefused > 0
}

// noteNeedsFileTimeout writes the review.needs_file_timeout row when a
// ticket's reviewer's result is unavailable: timeout after a needs_file round
// (attempt 1, retry true) or in a carried retry (the carry's attempt,
// retry false).
func noteNeedsFileTimeout(res ledger.Object, spec sessionSpec, carry *NeedsFileCarry, carried bool, in Input, deps Deps, rowExtra ledger.Object) {
	// Only a worker ticket writes it: the carry is read back per ticket, and an
	// engine call without one (the Node-parity path) stays byte-equal.
	if in.Ticket == "" || spec.role != "reviewer" || res.Get("status") != "unavailable" || res.Get("reason") != "timeout" {
		return
	}
	var requested []any
	attempt, retry := 1, true
	switch {
	case carried:
		requested = []any{}
		for _, p := range carry.Paths {
			requested = append(requested, p)
		}
		attempt, retry = carry.Attempt, false
	case res.Get("needs_file_round") == true:
		requested, _ = res.Get("needs_file_requested").([]any)
		if requested == nil {
			requested = []any{}
		}
	default:
		return
	}
	row := ledger.Object{{Key: "event", Value: "review.needs_file_timeout"}, {Key: "ticket", Value: in.Ticket}, {Key: "lens", Value: spec.lens},
		{Key: "role", Value: spec.role}, {Key: "level", Value: spec.level}, {Key: "requested_paths", Value: requested},
		{Key: "attempt", Value: float64(attempt)}, {Key: "retry", Value: retry}}
	deps.note(append(row, rowExtra...))
}
