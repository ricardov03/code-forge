package worker

// B496 step 3: the file's L3 rung (engine.mjs rungTaken / mayTakeRung) and
// playRound's wrapper around one fix-loop round: the approval names the
// ticket's bytes (a review.approved row for other content is refused
// 'hash-moved' and the approval counts as not written), and a throw mid-round
// is saved as next: retry with the pending kind (failureReason: the error's
// code, else its message with the secrets masked, else 'round-failed').

import (
	"errors"
	"time"

	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/review"
)

// RungTaken is engine.mjs rungTaken (review-state.mjs fileRungUsed).
func RungTaken(runID, block, file string, key []byte, rows []ledger.Object) (status string, used bool) {
	anchors, ok := blockAnchors(runID, block, key, rows)
	if !ok {
		return "tampered", true
	}
	for _, a := range anchors {
		if a.Get("file") == file && a.Get("l3_rung_used") == true {
			return "ok", true
		}
	}
	return "ok", false
}

func cfgInt(cfg map[string]any, section, key string, d float64) float64 {
	m, _ := obj(cfg[section])
	if v := m[key]; isInteger(v) && v.(float64) >= 0 {
		return v.(float64)
	}
	return d
}

// MayTakeRung is engine.mjs mayTakeRung: whether the round runs under the
// block lock.
func MayTakeRung(state ledger.Object, kind string, cfg map[string]any, rungUsed bool) bool {
	if rungUsed || kind == "patch_check" {
		return false
	}
	maxRounds := cfgInt(cfg, "review", "max_rounds_per_file", 4)
	perLevel := cfgInt(cfg, "escalation", "review_rounds_per_level", 2)
	if kind == "full" && state.Get("round") == 0.0 && state.Get("rounds_at_level") == 0.0 && maxRounds > 1 && perLevel > 1 {
		return false
	}
	return true
}

// FixLoopTicket is the queue ticket's {block, file, content_hash}.
type FixLoopTicket struct{ Block, File, ContentHash string }

// RoundError is a throw with an optional `code` (Node's err.code).
type RoundError struct{ Code, Message string }

func (e *RoundError) Error() string { return e.Message }

// FixLoopRequest is fixLoopRound's context. ReadRows and WriteRow are the
// worker's (nil: no ledger); WriteRow stamps and signs the row. Review is
// the file's review session (reviewFile) given playRound's writer; an
// ErrUnsupported from it is passed up, never turned into a crash.
type FixLoopRequest struct {
	Home, RunID, RunRootDir, ContentRoot, Level string
	Cfg                                         map[string]any
	Key                                         []byte
	Base                                        *string
	Owned                                       []any
	OwnedKinds                                  ledger.Object
	Secrets                                     []string
	ReadRows                                    func() ([]ledger.Object, error)
	WriteRow                                    func(ledger.Object) error
	Review                                      func(write func(ledger.Object) error) (ledger.Object, error)
	Now                                         func() time.Time
	// Input and Session are a recheck or patch_check round's review input
	// and session seams (B517); Session.WriteRow is playRound's writer.
	Input   review.Input
	Session review.Deps
	// Jev is System 1 with the worker's key bound (B533; nil: no key).
	Jev review.JevAsk
}

// failureReason is engine.mjs failureReason.
func (r FixLoopRequest) failureReason(err error) string {
	var re *RoundError
	if errors.As(err, &re) && re.Code != "" {
		return re.Code
	}
	secrets := []string{}
	for _, s := range r.Secrets {
		if s != "" {
			secrets = append(secrets, s)
		}
	}
	if safe := gates.Mask(err.Error(), secrets); safe != "" {
		return safe
	}
	return "round-failed"
}

// played is one playRound before its outcome is mapped.
type played struct {
	State           ledger.Object
	Crashed         *string
	ApprovalWritten bool
	Engine          ledger.Object
	Rechecks        []any // the recheck sessions, as engine.mjs playRound records them
}

// playRound is engine.mjs playRound up to the state save: the round
// (review.RunRound) with the approval check on its rows, a throw saved as
// next: retry, the stop's content hash, then saveState at seq.
func playRound(t FixLoopTicket, r FixLoopRequest, state ledger.Object, kind string, seq int) (played, error) {
	out := played{}
	write := func(row ledger.Object) error {
		if row.Get("event") == "review.approved" {
			// the approval names the reviewed bytes: a file that moved since the ticket is not approved
			if row.Get("content_hash") != t.ContentHash {
				return errors.New("hash-moved")
			}
			if err := r.WriteRow(row); err != nil {
				return err
			}
			out.ApprovalWritten = true
			return nil
		}
		return r.WriteRow(row)
	}
	before, err := ledger.Stringify(state, false)
	if err != nil {
		return out, err
	}
	deps := review.LoopDeps{RepoRoot: r.ContentRoot, Cfg: r.Cfg, WriteRow: write, Review: func() (ledger.Object, error) {
		res, err := r.Review(write)
		if err == nil {
			out.Engine = res
		}
		return res, err
	}, Input: r.Input, Session: r.Session, Jev: r.Jev}
	if spawn := r.Session.Spawn; spawn != nil {
		deps.Session.Spawn = func(opts review.SpawnOpts) (ledger.Object, error) {
			res, err := spawn(opts)
			if err != nil {
				return res, err
			}
			out.Rechecks = append(out.Rechecks, recheckRow(opts, res))
			return res, nil
		}
	}
	next, err := review.RunRound(state, deps, kind)
	if IsUnsupported(err) || review.IsUnsupported(err) {
		return out, unsupported(err.Error())
	}
	if err != nil {
		// RunRound commits a round only after its gate rows: a throw leaves the state as it was
		reason := r.failureReason(err)
		out.Crashed = &reason
		if now, serr := ledger.Stringify(next, false); serr == nil && string(now) == string(before) {
			next.Set("next", ledger.Object{{Key: "action", Value: "retry"}, {Key: "reason", Value: reason}})
			next.Set("pending_kind", kind)
		}
	}
	if next.Get("status") == "stopped" {
		next.Set("stop_content_hash", t.ContentHash)
	} else {
		next.Delete("stop_content_hash")
	}
	out.State = next
	err = SaveState(SaveRequest{Home: r.Home, RunID: r.RunID, Block: t.Block, File: t.File, ContentRoot: r.ContentRoot, Key: r.Key,
		State: next, Seq: seq, WriteRow: r.WriteRow, Now: r.Now})
	return out, err
}

// recheckRow is engine.mjs playRound's record of one recheck session.
func recheckRow(opts review.SpawnOpts, res ledger.Object) ledger.Object {
	get := func(o ledger.Object, key string) any {
		if o == nil {
			return nil
		}
		return o.Get(key)
	}
	var lens any
	if opts.RowExtra != nil {
		lens = opts.RowExtra.Get("lens")
	}
	usage, _ := get(res, "usage").(ledger.Object)
	row, _ := get(res, "row").(ledger.Object)
	tokens := func(key string) any {
		if v := get(usage, key); v != nil {
			return v
		}
		return get(row, key)
	}
	out := ledger.Object{{Key: "lens", Value: lens}, {Key: "role", Value: emptyNull(opts.Role)}, {Key: "level", Value: emptyNull(opts.Level)},
		{Key: "provider", Value: get(res, "provider")}, {Key: "model", Value: get(res, "model")}, {Key: "status", Value: get(res, "status")},
		{Key: "tokens_in", Value: tokens("tokens_in")}, {Key: "tokens_out", Value: tokens("tokens_out")}}
	if st, ok := get(res, "status").(string); ok && st != "ok" {
		reason := get(res, "reason")
		if reason == nil {
			reason = st
		}
		out = append(out, ledger.Field{Key: "reason", Value: reason})
	}
	return out
}

func emptyNull(s string) any {
	if s == "" {
		return nil
	}
	return s
}
