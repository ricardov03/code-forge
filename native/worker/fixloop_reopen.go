package worker

// B623: the two fix-loop inputs Go handed to Node before — the signed
// `block reopen` row (review/reopen.mjs reopenRowFor) and the autopilot extra
// rounds (review/fixloop.mjs extraRoundsFor with gate-check.mjs
// grantCoverProblem and decisionProblem). A date Go cannot prove it parses as
// V8's Date.parse does stays Node's (ErrUnsupported).

import (
	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/sign"
)

// ReopenedEvent and AutoRequeueReason are reopen.mjs REOPENED_EVENT and
// AUTO_REQUEUE_REASON.
const (
	ReopenedEvent     = "review.reopened"
	AutoRequeueReason = "auto-requeue: content changed after stopped review"
)

// reopenDecisionMaxAgeMs is gate-check.mjs DECISION_MAX_AGE_MS.
const reopenDecisionMaxAgeMs = 30 * 60 * 1000

// reopenAt is reopenRowFor's {runId, block, file, key, status, state, seq, contentHash}.
type reopenAt struct {
	runID, block, file string
	key                []byte
	loaded             Loaded
	contentHash        string
}

// isAutomaticReopen is reopen.mjs isAutomaticReopen.
func isAutomaticReopen(row ledger.Object) bool {
	from, okFrom := row.Get("from_hash").(string)
	to, okTo := row.Get("to_hash").(string)
	return row.Get("by") == "auto" && row.Get("reason") == AutoRequeueReason && row.Get("from") == "stopped" && okFrom && okTo && from != to
}

func (a reopenAt) mine(r ledger.Object) bool {
	return r.Get("run") == a.runID && r.Get("block") == a.block && r.Get("file") == a.file && verified(r, a.key)
}

// latestRowAt is reopen.mjs latestRowAt.
func (a reopenAt) latestRowAt(rows []ledger.Object) ledger.Object {
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		if r.Get("event") != ReopenedEvent || r.Get("run") != a.runID || r.Get("block") != a.block || r.Get("file") != a.file {
			continue
		}
		by := r.Get("by")
		agreed := false
		if v, ok := r.Get("verdict").(ledger.Object); ok && by == "agent" {
			agreed = v.Get("verdict") == "agree"
		}
		if (by != "human" && !agreed && !isAutomaticReopen(r)) || r.Get("at_seq") != a.loaded.Seq || !verified(r, a.key) {
			continue
		}
		return r
	}
	return nil
}

// stopContentHash is reopen.mjs stopContentHash (known false is null).
func (a reopenAt) stopContentHash(state ledger.Object, rows []ledger.Object) (hash string, known bool) {
	if state == nil || state.Get("status") != "stopped" {
		return "", false
	}
	if s, ok := state.Get("stop_content_hash").(string); ok && s != "" {
		return s, true
	}
	var reason any
	if next, ok := state.Get("next").(ledger.Object); ok {
		reason = next.Get("reason")
	}
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		if r.Get("event") == ReopenedEvent && a.mine(r) {
			return "", false
		}
		if r.Get("event") != "review.cap" || !a.mine(r) {
			continue
		}
		h, ok := r.Get("content_hash").(string)
		if !ok || !jsStrictEqual(r.Get("round"), state.Get("round")) || !jsStrictEqual(r.Get("reason"), reason) {
			return "", false
		}
		return h, true
	}
	return "", false
}

// jsStrictEqual is === for JSON scalars (objects and arrays are never equal
// across two parsed values).
func jsStrictEqual(x, y any) bool {
	switch x.(type) {
	case nil, bool, string, float64:
		return x == y
	}
	return false
}

// hasReviewedHash is reopen.mjs hasReviewedHash.
func (a reopenAt) hasReviewedHash(rows []ledger.Object) bool {
	for _, r := range rows {
		if r.Get("event") == "review.round" && r.Get("content_hash") == a.contentHash && a.mine(r) {
			return true
		}
	}
	return false
}

// jsHasLength is `v?.length > 0` for a JSON value.
func jsHasLength(v any) bool {
	switch x := v.(type) {
	case []any:
		return len(x) > 0
	case string:
		return x != ""
	case ledger.Object:
		n, ok := x.Get("length").(float64)
		return ok && n > 0
	}
	return false
}

// reopenRowFor is reopen.mjs reopenRowFor: the verified reopen row that
// restarts the file now, or nil.
func (a reopenAt) reopenRowFor(rows []ledger.Object) ledger.Object {
	row := a.latestRowAt(rows)
	if row == nil {
		return nil
	}
	status, state := a.loaded.Status, a.loaded.State
	if status == "lost" {
		if row.Get("from") == "lost" {
			return row
		}
		return nil
	}
	live := status == "ok" || status == "restored"
	if live && state != nil && state.Get("status") == "open" && jsHasLength(state.Get("open")) &&
		row.Get("from") == "open" && row.Get("by") == "agent" && row.Get("to_hash") == a.contentHash {
		return row
	}
	if live && state != nil && state.Get("status") == "stopped" && row.Get("from") == "stopped" {
		stopHash, known := a.stopContentHash(state, rows)
		if (known && a.contentHash == stopHash) || row.Get("from_hash") == a.contentHash {
			return nil
		}
		// from_hash !== null: a missing from_hash is undefined, not null
		if !known && (!hasKey(row, "from_hash") || row.Get("from_hash") != nil || row.Get("to_hash") != a.contentHash || a.hasReviewedHash(rows)) {
			return nil
		}
		return row
	}
	return nil
}

// reopenVerifyThrows is gate-check.mjs verified, whose verifyRow throw
// reaches extraRoundsFor's catch: ok, threw.
func reopenVerifyThrows(row ledger.Object, key []byte) (bool, bool) {
	res, err := sign.VerifyRow(row.Map(), key)
	if err != nil {
		return false, true
	}
	return res.OK, false
}

// extraRoundCheck is one check's result; a throw counts the row for nothing.
type extraRoundCheck int

const (
	extraRoundOK extraRoundCheck = iota
	extraRoundProblem
	extraRoundThrow
	extraRoundUnknown // a date Go cannot read as V8 does
)

func reopenDate(row ledger.Object, key string) (float64, bool, bool) {
	return gatecheck.JSDateParse(row.Get(key), hasKey(row, key))
}

// extraGrantCover is gate-check.mjs grantCoverProblem for scope round:extra
// with endRows = runRows.
func extraGrantCover(runRows []ledger.Object, grantID string, at float64, key []byte) extraRoundCheck {
	var grant ledger.Object
	for _, r := range runRows {
		if r.Get("event") != "autopilot.grant" || r.Get("grant_id") != grantID {
			continue
		}
		ok, threw := reopenVerifyThrows(r, key)
		if threw {
			return extraRoundThrow
		}
		if ok {
			grant = r
			break
		}
	}
	if grant == nil {
		return extraRoundProblem
	}
	from, fromOK, known := reopenDate(grant, "ts")
	if !known {
		return extraRoundUnknown
	}
	if !fromOK {
		return extraRoundProblem
	}
	until, untilOK, known := reopenDate(grant, "until")
	if !known {
		return extraRoundUnknown
	}
	if !untilOK || !(at >= from && at < until) {
		return extraRoundProblem
	}
	scopes, ok := grant.Get("scopes").([]any)
	if !ok || !containsString(scopes, "round:extra") {
		return extraRoundProblem
	}
	if deny, ok := grant.Get("deny").([]any); ok && containsString(deny, "round:extra") {
		return extraRoundProblem
	}
	for _, r := range runRows {
		if ev := r.Get("event"); (ev != "autopilot.stop" && ev != "autopilot.expire") || r.Get("grant_id") != grantID {
			continue
		}
		if _, isStr := r.Get("ts").(string); !isStr {
			return extraRoundProblem
		}
		ts, ok, known := reopenDate(r, "ts")
		if !known {
			return extraRoundUnknown
		}
		if !ok || ts <= at {
			return extraRoundProblem
		}
	}
	return extraRoundOK
}

func containsString(list []any, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// extraDecision is gate-check.mjs decisionProblem for an extra round
// (scope round:extra, decision allow, subject {block, file}).
func extraDecision(runRows []ledger.Object, decisionID any, grantID, block, file string, at float64, key []byte) extraRoundCheck {
	id, ok := decisionID.(string)
	if !ok || id == "" {
		return extraRoundProblem
	}
	var row ledger.Object
	for _, r := range runRows {
		if r.Get("event") == "autopilot.decision" && r.Get("decision_id") == id {
			row = r
			break
		}
	}
	if row == nil {
		return extraRoundProblem
	}
	vok, threw := reopenVerifyThrows(row, key)
	if threw {
		return extraRoundThrow
	}
	if !vok || row.Get("acted") != true || row.Get("grant_id") != grantID || row.Get("scope") != "round:extra" || row.Get("decision") != "allow" {
		return extraRoundProblem
	}
	named, _ := row.Get("subject").(ledger.Object) // an array or a scalar names nothing
	if named.Get("block") != block || named.Get("file") != file {
		return extraRoundProblem
	}
	if _, isStr := row.Get("ts").(string); !isStr {
		return extraRoundProblem
	}
	ts, ok, known := reopenDate(row, "ts")
	if !known {
		return extraRoundUnknown
	}
	if !ok || ts > at || at-ts > reopenDecisionMaxAgeMs {
		return extraRoundProblem
	}
	return extraRoundOK
}

// extraRoundsFor is fixloop.mjs extraRoundsFor over this run's rows: the
// number of distinct grants with an extra-round row for the file that holds
// up. ErrUnsupported when a date it reads is one Go cannot parse as V8 does.
func extraRoundsFor(rows []ledger.Object, runID, block, file string, key []byte) (int, error) {
	runRows := make([]ledger.Object, 0, len(rows))
	for _, r := range rows {
		if r.Get("run") == runID {
			runRows = append(runRows, r)
		}
	}
	grants := map[string]bool{}
	for _, r := range runRows {
		grantID, ok := r.Get("grant_id").(string)
		if r.Get("event") != "autopilot.extra_round" || r.Get("block") != block || r.Get("file") != file || !ok {
			continue
		}
		vok, threw := reopenVerifyThrows(r, key)
		if threw || !vok {
			continue
		}
		if _, isStr := r.Get("ts").(string); !isStr {
			continue
		}
		at, ok, known := reopenDate(r, "ts")
		if !known {
			return 0, unsupported("an autopilot.extra_round ts Date.parse undecided")
		}
		if !ok {
			continue
		}
		switch extraGrantCover(runRows, grantID, at, key) {
		case extraRoundUnknown:
			return 0, unsupported("an autopilot grant or stop date Date.parse undecided")
		case extraRoundOK:
		default:
			continue
		}
		switch extraDecision(runRows, r.Get("decision_id"), grantID, block, file, at, key) {
		case extraRoundUnknown:
			return 0, unsupported("an autopilot.decision ts Date.parse undecided")
		case extraRoundOK:
			grants[grantID] = true
		}
	}
	return len(grants), nil
}
