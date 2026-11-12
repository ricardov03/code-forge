package worker

// B496 step 4: engine.mjs fixLoopRound around one round, and its outcome.
// Under the block lock: the run's rows read once, the block's budget row
// written once (check-then-write), the state loaded (tampered / lost
// refusals, a restored state saved at a new seq + review.state_restored),
// the file's rung read, and a round that could take the rung run before the
// lock is let go; any other round runs after it. The outcome maps the saved
// state: approval-not-written, findings-open, no-fix-hunks, stopped,
// unavailable (crash or retry).
//
// Go serves a ticket only when every part it needs is ported. A dry pass
// (no writes) decides first, so a ticket that needs a `block reopen` row, an
// autopilot extra round, a stop a raised cap or a new packet budget
// reopens, a recheck or a patch_check returns ErrUnsupported with nothing
// written.

import (
	"github.com/ricardov03/code-forge/native/filelock"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/review"
)

// LostMessage is engine.mjs LOST_MESSAGE.
const LostMessage = "the review state file is gone and the ledger cannot rebuild it (its anchor carries no state_body); the file is not reviewed again from round 1 — the owner can run `code-forge block reopen`"

func fixLoopRefusal(status, reason string, message ...string) ledger.Object {
	out := ledger.Object{{Key: "status", Value: status}, {Key: "reason", Value: reason}}
	for _, m := range message {
		out = append(out, ledger.Field{Key: "message", Value: m})
	}
	return append(out, ledger.Field{Key: "approved", Value: false}, ledger.Field{Key: "engine", Value: "adaptive"}, ledger.Field{Key: "sessions", Value: []any{}})
}

// prepared is fixLoopRound's decision under the lock.
type prepared struct {
	outcome   ledger.Object // a refusal or a stop: no round
	restore   bool          // save loaded.State at seq, then seq + 1 and review.state_restored
	state     ledger.Object
	kind      string
	seq       int
	underLock bool
}

func lateLen(state ledger.Object) (float64, bool) {
	late, ok := state.Get("late").([]any)
	return float64(len(late)), ok
}

// loopFields is engine.mjs loopFields (kind "" is null).
func loopFields(state ledger.Object, kind string) ledger.Object {
	out := ledger.Object{{Key: "round", Value: state.Get("round")}}
	if kind != "" {
		out = append(out, ledger.Field{Key: "kind", Value: kind})
	}
	next, _ := state.Get("next").(ledger.Object)
	var trigger any
	if next != nil {
		trigger = next.Get("trigger")
	}
	late, _ := lateLen(state)
	return append(out, ledger.Field{Key: "level", Value: state.Get("level")}, ledger.Field{Key: "next", Value: state.Get("next")},
		ledger.Field{Key: "trigger", Value: trigger}, ledger.Field{Key: "late", Value: late})
}

func finiteOrNull(v any) any {
	if f, ok := v.(float64); ok {
		return f
	}
	return nil
}

// stopSize is engine.mjs stopSize.
func stopSize(state ledger.Object) ledger.Object {
	next, _ := state.Get("next").(ledger.Object)
	if next == nil || next.Get("reason") != "split_required" {
		return nil
	}
	out := ledger.Object{{Key: "tokens_in", Value: finiteOrNull(next.Get("tokens_in"))}, {Key: "budget", Value: finiteOrNull(next.Get("budget"))}}
	if s, ok := next.Get("section").(string); ok {
		out = append(out, ledger.Field{Key: "section", Value: s})
	}
	return out
}

func orDefault(v any, d any) any {
	if v == nil {
		return d
	}
	return v
}

func stoppedOutcome(state ledger.Object, kind string, head ledger.Object) ledger.Object {
	next, _ := state.Get("next").(ledger.Object)
	var reason any
	if next != nil {
		reason = next.Get("reason")
	}
	out := append(head, ledger.Field{Key: "status", Value: "stopped"}, ledger.Field{Key: "stopped", Value: orDefault(reason, "stopped")})
	out = append(out, stopSize(state)...)
	return out
}

// prepare is the decision part of fixLoopRound's locked section. It writes
// nothing; ErrUnsupported for a ticket Go does not serve.
func (r FixLoopRequest) prepare(t FixLoopTicket, rows []ledger.Object, loaded Loaded) (prepared, error) {
	rungStatus, rungUsed := RungTaken(r.RunID, t.Block, t.File, r.Key, rows)
	if loaded.Status == "tampered" || rungStatus == "tampered" {
		return prepared{outcome: fixLoopRefusal("refused", "review-state-tampered")}, nil
	}
	amendedAt, anchoredAt := -1, -1
	for i, row := range rows {
		if row.Get("block") != t.Block {
			continue
		}
		ev := row.Get("event")
		if row.Get("file") == t.File && (ev == "review.reopened" || (ev == "autopilot.extra_round" && isString(row.Get("grant_id")))) {
			return prepared{}, unsupported("a " + ev.(string) + " row for the file (block reopen, autopilot extra round)")
		}
		if (ev != "block.acceptance_amended" && !(ev == StateEvent && row.Get("file") == t.File)) || !verified(row, r.Key) {
			continue
		}
		if ev == "block.acceptance_amended" {
			amendedAt = i
		} else {
			anchoredAt = i
		}
	}
	reopened := amendedAt > anchoredAt
	if loaded.Status == "lost" && !reopened {
		return prepared{outcome: fixLoopRefusal("refused", "review-state-lost", LostMessage)}, nil
	}
	p := prepared{seq: int(loaded.Seq)}
	var state ledger.Object
	if !reopened {
		state = loaded.State
	}
	if loaded.Status == "restored" && state != nil {
		p.restore = true
		p.seq++
	}
	if state == nil || state.Get("status") == "complete" {
		state = review.NewFileState(t.File, r.Level, rungUsed)
	} else {
		state = state.Clone()
	}
	if !truthy(state.Get("l3_rung_used")) {
		state.Set("l3_rung_used", rungUsed)
	}
	if _, ok := state.Get("open").([]any); !ok {
		return prepared{}, unsupported("a review state whose open is not an array")
	}
	if _, ok := lateLen(state); !ok {
		return prepared{}, unsupported("a review state whose late is not an array")
	}
	next, _ := state.Get("next").(ledger.Object)
	if state.Get("status") == "stopped" && next != nil {
		budget, finite := next.Get("budget").(float64)
		if next.Get("reason") == "split_required" && state.Get("stop_lens") == "recheck" && finite && budget > 0 {
			return prepared{}, unsupported("a split stop a new packet budget may reopen")
		}
		round, isNum := state.Get("round").(float64)
		if next.Get("reason") == "review_cap" && (!isNum || round < float64(review.RoundCap(r.Cfg, 0))) {
			return prepared{}, unsupported("a cap stop the raised cap reopens (a recheck)")
		}
	}
	if state.Get("status") == "stopped" {
		p.outcome = append(stoppedOutcome(state, "", loopFields(state, "")),
			ledger.Field{Key: "approved", Value: false}, ledger.Field{Key: "engine", Value: "adaptive"}, ledger.Field{Key: "sessions", Value: []any{}},
			ledger.Field{Key: "findings", Value: state.Get("open")})
		return p, nil
	}
	kind := "recheck"
	if state.Get("round") == 0.0 {
		kind = "full"
	}
	if next != nil && next.Get("action") == "retry" {
		if pk := state.Get("pending_kind"); pk != nil {
			s, ok := pk.(string)
			if !ok {
				return prepared{}, unsupported("a pending_kind that is not a string")
			}
			kind = s
		}
	} else if next != nil && next.Get("action") == "patch" {
		kind = "patch_check"
	}
	if err := review.CheckLoopInput(state, kind, review.LoopDeps{RepoRoot: r.ContentRoot, Cfg: r.Cfg}); err != nil {
		return prepared{}, unsupported(err.Error())
	}
	p.state, p.kind = state, kind
	p.underLock = MayTakeRung(state, kind, r.Cfg, rungUsed)
	return p, nil
}

func isString(v any) bool { _, ok := v.(string); return ok }

// outcome is the end of engine.mjs playRound: the saved state as the
// ticket's outcome.
func roundOutcome(state ledger.Object, kind string, pl played) ledger.Object {
	eng := pl.Engine
	head := loopFields(state, kind)
	head = append(head, ledger.Field{Key: "engine", Value: orDefault(eng.Get("engine"), "adaptive")},
		ledger.Field{Key: "sessions", Value: orDefault(eng.Get("sessions"), []any{})})
	with := func(fields ...ledger.Field) ledger.Object {
		out := head.Clone()
		for _, f := range fields {
			out.Set(f.Key, f.Value)
		}
		return out
	}
	if pl.Crashed != nil {
		return with(ledger.Field{Key: "status", Value: "unavailable"}, ledger.Field{Key: "reason", Value: *pl.Crashed}, ledger.Field{Key: "approved", Value: false})
	}
	next, _ := state.Get("next").(ledger.Object)
	if next != nil && next.Get("action") == "retry" {
		if eng != nil && eng.Get("status") != "reviewed" {
			out := eng.Clone()
			for _, f := range loopFields(state, kind) {
				out.Set(f.Key, f.Value)
			}
			out.Set("approved", false)
			return out
		}
		return with(ledger.Field{Key: "status", Value: "unavailable"}, ledger.Field{Key: "reason", Value: orDefault(next.Get("reason"), "unavailable")}, ledger.Field{Key: "approved", Value: false})
	}
	switch state.Get("status") {
	case "complete":
		if !pl.ApprovalWritten {
			return with(ledger.Field{Key: "status", Value: "unavailable"}, ledger.Field{Key: "reason", Value: "approval-not-written"}, ledger.Field{Key: "approved", Value: false})
		}
		return with(ledger.Field{Key: "status", Value: "reviewed"}, ledger.Field{Key: "approved", Value: true}, ledger.Field{Key: "findings", Value: []any{}})
	case "stopped":
		out := stoppedOutcome(state, kind, head.Clone())
		return append(out, ledger.Field{Key: "approved", Value: false}, ledger.Field{Key: "findings", Value: state.Get("open")})
	}
	reason := "findings-open"
	if next != nil && next.Get("reason") == "no-fix-hunks" {
		reason = "no-fix-hunks"
	}
	return with(ledger.Field{Key: "status", Value: "reviewed"}, ledger.Field{Key: "approved", Value: false}, ledger.Field{Key: "reason", Value: reason},
		ledger.Field{Key: "findings", Value: state.Get("open")})
}

func (r FixLoopRequest) round(t FixLoopTicket, p prepared) (ledger.Object, error) {
	pl, err := playRound(t, r, p.state, p.kind, p.seq)
	if err != nil {
		return nil, err
	}
	return roundOutcome(pl.State, p.kind, pl), nil
}

// FixLoopCheck is FixLoopRound's dry pass, run before the worker writes its
// route rows: ErrUnsupported for a ticket FixLoopRound would leave to Node,
// else whether the round runs a review session. It reads, never writes.
func FixLoopCheck(t FixLoopTicket, r FixLoopRequest) (session bool, err error) {
	if r.ReadRows == nil || r.WriteRow == nil {
		return false, nil
	}
	all, err := r.ReadRows()
	if err != nil {
		if IsUnsupported(err) {
			return false, err
		}
		return false, nil
	}
	rows := []ledger.Object{}
	for _, row := range all {
		if row.Get("run") == r.RunID {
			rows = append(rows, row)
		}
	}
	where := StateWhere{Home: r.Home, RunRootDir: r.RunRootDir, RunID: r.RunID, Block: t.Block, File: t.File, ContentRoot: r.ContentRoot, Key: r.Key, Rows: rows}
	dry, err := LoadState(where, false)
	if err != nil {
		return false, err
	}
	p, err := r.prepare(t, rows, dry)
	if err != nil {
		return false, err
	}
	if !hasBudgetRow(rows, t.Block) {
		if _, err := BlockBudgetRows(BudgetRequest{ContentRoot: r.ContentRoot, Block: t.Block, File: t.File, Base: r.Base, Owned: r.Owned, OwnedKinds: r.OwnedKinds, Cfg: r.Cfg}); err != nil {
			return false, err
		}
	}
	return p.outcome == nil, nil
}

// FixLoopRound is engine.mjs fixLoopRound for one ticket.
func FixLoopRound(t FixLoopTicket, r FixLoopRequest) (ledger.Object, error) {
	if r.ReadRows == nil || r.WriteRow == nil {
		return fixLoopRefusal("unavailable", "no-ledger"), nil
	}
	var out ledger.Object
	var later *prepared
	err := filelock.WithLock(BlockLockKey(r.RunID, t.Block), func() error {
		all, err := r.ReadRows()
		if IsUnsupported(err) {
			return err
		}
		if err != nil {
			out = fixLoopRefusal("unavailable", r.failureReason(err))
			return nil
		}
		rows := []ledger.Object{}
		for _, row := range all {
			if row.Get("run") == r.RunID {
				rows = append(rows, row)
			}
		}
		where := StateWhere{Home: r.Home, RunRootDir: r.RunRootDir, RunID: r.RunID, Block: t.Block, File: t.File, ContentRoot: r.ContentRoot, Key: r.Key, Rows: rows}
		// the dry pass: a ticket Go does not serve stops here, nothing written
		dry, err := LoadState(where, false)
		if err != nil {
			return err
		}
		if _, err := r.prepare(t, rows, dry); err != nil {
			return err
		}
		var budget []ledger.Object
		if !hasBudgetRow(rows, t.Block) {
			if budget, err = BlockBudgetRows(BudgetRequest{ContentRoot: r.ContentRoot, Block: t.Block, File: t.File, Base: r.Base, Owned: r.Owned, OwnedKinds: r.OwnedKinds, Cfg: r.Cfg}); err != nil {
				return err
			}
		}
		writeBudget(budget, r.WriteRow)
		loaded, err := LoadState(where, true)
		if err != nil {
			return err
		}
		p, err := r.prepare(t, rows, loaded)
		if err != nil {
			return err
		}
		if p.restore {
			// B68: the lost file rebuilt from the anchor's signed body, saved as a new seq, then recorded
			if err := SaveState(SaveRequest{Home: r.Home, RunID: r.RunID, Block: t.Block, File: t.File, ContentRoot: r.ContentRoot, Key: r.Key,
				State: loaded.State, Seq: int(loaded.Seq), WriteRow: r.WriteRow, Now: r.Now}); err != nil {
				return err
			}
			if err := r.WriteRow(ledger.Object{{Key: "event", Value: RestoredEvent}, {Key: "block", Value: t.Block}, {Key: "file", Value: t.File},
				{Key: "seq", Value: float64(p.seq)}, {Key: "from_seq", Value: loaded.Seq}}); err != nil {
				return err
			}
		}
		if p.outcome != nil {
			out = p.outcome
			return nil
		}
		if p.underLock {
			out, err = r.round(t, p)
			return err
		}
		later = &p
		return nil
	}, filelock.Options{})
	if err != nil {
		return nil, err
	}
	if later != nil {
		return r.round(t, *later)
	}
	return out, nil
}
