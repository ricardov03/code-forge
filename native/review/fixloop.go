package review

// B495 step 1: the fix loop's first round (src/review/fixloop.mjs runRound
// with kind `full`), its triage without System 1 (src/review/triage.mjs
// triageFindings with no Jev and no S2 rule: every finding is mid with p
// null, so its verdict is the severity's) and the escalation ladder
// (src/decide/escalation.mjs escalationAfterAttempt). B517 added the recheck
// and patch_check rounds on the fix-hunk path (recheck.go); the parts of them
// not ported are refused with ErrUnsupported before anything runs.

import (
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
)

// LoopDeps are runRound's seams for a full round.
type LoopDeps struct {
	RepoRoot    string
	Cfg         map[string]any
	ExtraRounds int
	// WriteRow writes one ledger row (nil: no writer). The triage and
	// approval rows are best effort; a failed review.round or review.cap
	// write leaves the state as it was with next: retry.
	WriteRow func(ledger.Object) error
	// Review runs the file's review (reviewFile); an error is a throw.
	Review func() (ledger.Object, error)
	// Input is the review input of a recheck or patch_check round (its
	// packet, paired context and guards; B517).
	Input Input
	// Session spawns a recheck's L2 session (Spawn, Warn, SchemaHistory,
	// Rand; B517). WriteRow comes from the field above. Session.Warn also
	// writes triage's s1_fallback line (B533).
	Session Deps
	// Jev asks System 1 for the triage and the recheck's resolved band
	// (B533); nil = no key (the §3.5 fallback, no warning).
	Jev JevAsk
}

var levelOrder = []string{"L0", "L1", "L2", "L3"}

func levelIndex(level string) int {
	for i, l := range levelOrder {
		if l == level {
			return i
		}
	}
	return -1
}

// NewFileState is fixloop.mjs newFileState.
func NewFileState(file, level string, l3RungUsed bool) ledger.Object {
	return ledger.Object{{Key: "file", Value: file}, {Key: "level", Value: level}, {Key: "round", Value: 0.0},
		{Key: "rounds_at_level", Value: 0.0}, {Key: "warn_rounds_at_level", Value: 0.0}, {Key: "open", Value: []any{}},
		{Key: "late", Value: []any{}}, {Key: "reviewed_content", Value: nil}, {Key: "l3_rung_used", Value: l3RungUsed},
		{Key: "status", Value: "open"}, {Key: "next", Value: nil}, {Key: "last_packet", Value: nil}, {Key: "pending_kind", Value: nil}}
}

func intOr(v any, d int) int {
	if n, ok := intOf(v); ok && n >= 0 {
		return n
	}
	return d
}

// RoundCap is fixloop.mjs roundCap.
func RoundCap(cfg map[string]any, extraRounds int) int {
	return intOr(section(cfg, "review")["max_rounds_per_file"], 4) + max(extraRounds, 0)
}

type loopSettings struct {
	maxRounds, perLevel, warnAfter int
	warnThreshold                  any
	stopAt, l3Mode                 string
}

func settingsFor(cfg map[string]any, extraRounds int) loopSettings {
	e := section(cfg, "escalation")
	s := loopSettings{maxRounds: RoundCap(cfg, extraRounds), perLevel: intOr(e["review_rounds_per_level"], 2),
		warnAfter: intOr(e["after_rounds_with_warnings"], 1), warnThreshold: e["warning_threshold"], stopAt: "L3", l3Mode: "patch"}
	if v, ok := e["stop_at"].(string); ok {
		s.stopAt = v
	}
	if e["l3_mode"] == "code" {
		s.l3Mode = "code"
	}
	return s
}

// heavyRound is escalation.mjs heavyRound.
func heavyRound(findings []any, threshold any) bool {
	min := 2
	if n, ok := intOf(threshold); ok && n >= 1 {
		min = n
	}
	warnings := 0
	for _, f := range findings {
		switch get(asObject(f), "severity") {
		case "critical":
			return true
		case "warning":
			warnings++
		}
	}
	return warnings >= min
}

func asObject(v any) ledger.Object { o, _ := v.(ledger.Object); return o }

type escalation struct{ trigger, action, level, reason string }

func runningCeiling(stopAt string) string {
	return levelOrder[min(levelIndex("L2"), levelIndex(stopAt))]
}

func finalizeEscalate(trigger, current, stopAt, l3Mode string, rungUsed bool) escalation {
	ceiling := runningCeiling(stopAt)
	if levelIndex(current) < levelIndex(ceiling) {
		return escalation{trigger: trigger, action: "escalate", level: levelOrder[min(levelIndex(current)+1, levelIndex(ceiling))]}
	}
	if stopAt != "L3" {
		return escalation{trigger: trigger, action: "stop", reason: "stop_at"}
	}
	if rungUsed {
		if l3Mode == "code" {
			return escalation{trigger: trigger, action: "stop", reason: "l3_code_exhausted"}
		}
		return escalation{trigger: trigger, action: "stop", reason: "l3_patch_exhausted"}
	}
	if l3Mode == "code" {
		return escalation{trigger: trigger, action: "escalate", level: "L3"}
	}
	return escalation{trigger: trigger, action: "l3_rung"}
}

type escalationCtx struct {
	reviewRoundsAtLevel, reviewRoundsPerLevel, warningRoundsAtLevel, afterRoundsWithWarnings, roundsAtFile, maxRoundsPerFile int
	openFixNow, reviewStall, rungUsed                                                                                        bool
	current, stopAt, l3Mode                                                                                                  string
}

// escalationAfterAttempt is escalation.mjs escalationAfterAttempt for a review
// round (no attempt counter, no S1 next).
func escalationAfterAttempt(c escalationCtx) escalation {
	if c.reviewStall && c.openFixNow {
		return finalizeEscalate("review_stall", c.current, c.stopAt, c.l3Mode, c.rungUsed)
	}
	if c.afterRoundsWithWarnings > 0 && c.warningRoundsAtLevel >= c.afterRoundsWithWarnings && c.openFixNow && levelIndex(c.current) < levelIndex(runningCeiling(c.stopAt)) {
		return finalizeEscalate("review_warnings", c.current, c.stopAt, c.l3Mode, c.rungUsed)
	}
	if c.reviewRoundsAtLevel >= c.reviewRoundsPerLevel && c.openFixNow {
		return finalizeEscalate("review_rounds", c.current, c.stopAt, c.l3Mode, c.rungUsed)
	}
	if c.roundsAtFile >= c.maxRoundsPerFile && c.openFixNow {
		if c.rungUsed {
			return escalation{trigger: "review_cap", action: "stop", reason: "review_cap"}
		}
		return escalation{trigger: "review_cap", action: "l3_rung"}
	}
	return escalation{action: "none"}
}

func num(o ledger.Object, key string) int { n, _ := intOf(o.Get(key)); return n }

func nextStep(fields ...any) ledger.Object {
	out := ledger.Object{}
	for i := 0; i+1 < len(fields); i += 2 {
		out = append(out, ledger.Field{Key: fields[i].(string), Value: fields[i+1]})
	}
	return out
}

func triggerValue(t string) any {
	if t == "" {
		return nil
	}
	return t
}

// decideNext is fixloop.mjs decideNext for a full or recheck round.
func decideNext(state ledger.Object, openBefore int, s loopSettings) ledger.Object {
	open, _ := state.Get("open").([]any)
	if len(open) == 0 {
		return nextStep("action", "complete")
	}
	round, level, rung := num(state, "round"), state.Get("level").(string), state.Get("l3_rung_used") == true
	if round >= s.maxRounds {
		esc := escalationAfterAttempt(escalationCtx{current: level, roundsAtFile: round, maxRoundsPerFile: s.maxRounds, openFixNow: true, rungUsed: rung,
			reviewRoundsPerLevel: 2, afterRoundsWithWarnings: 1, stopAt: "L3", l3Mode: "patch"})
		if esc.action == "l3_rung" {
			return nextStep("action", "patch", "level", "L3", "trigger", "review_cap")
		}
		return nextStep("action", "stop", "reason", "review_cap", "trigger", "review_cap")
	}
	esc := escalationAfterAttempt(escalationCtx{current: level, reviewStall: round >= 2 && len(open) >= openBefore, openFixNow: true,
		reviewRoundsAtLevel: num(state, "rounds_at_level"), reviewRoundsPerLevel: s.perLevel, warningRoundsAtLevel: num(state, "warn_rounds_at_level"),
		afterRoundsWithWarnings: s.warnAfter, stopAt: s.stopAt, l3Mode: s.l3Mode, rungUsed: rung, maxRoundsPerFile: 4})
	switch {
	case esc.action == "escalate" && esc.level != "":
		if esc.level != level {
			state.Set("rounds_at_level", 0.0)
			state.Set("warn_rounds_at_level", 0.0)
		}
		state.Set("level", esc.level)
		return nextStep("action", "fix", "level", esc.level, "trigger", triggerValue(esc.trigger))
	case esc.action == "l3_rung":
		return nextStep("action", "patch", "level", "L3", "trigger", triggerValue(esc.trigger))
	case esc.action == "stop":
		return nextStep("action", "stop", "reason", esc.reason, "trigger", triggerValue(esc.trigger))
	}
	return nextStep("action", "fix", "level", level, "trigger", nil)
}

// decideNextKind is fixloop.mjs decideNext with its kind: a patch_check
// stops when it closed no old finding, then at the cap; else a fix (B517).
func decideNextKind(state ledger.Object, openBefore int, s loopSettings, kind string, closedIDs int) ledger.Object {
	open, _ := state.Get("open").([]any)
	if len(open) > 0 && kind == "patch_check" {
		if closedIDs == 0 {
			return nextStep("action", "stop", "reason", "l3_patch_exhausted", "trigger", "review_stall")
		}
		if num(state, "round") >= s.maxRounds {
			return nextStep("action", "stop", "reason", "review_cap", "trigger", "review_cap")
		}
		return nextStep("action", "fix", "level", state.Get("level"), "trigger", nil)
	}
	return decideNext(state, openBefore, s)
}

// findingKey is JSON.stringify([claim.trim(), evidence.trim()]).
func findingKey(f ledger.Object) string {
	claim, _ := f.Get("claim").(string)
	evidence, _ := f.Get("evidence").(string)
	b, _ := ledger.Stringify([]any{trimJS(claim), trimJS(evidence)}, false)
	return string(b)
}

func note(write func(ledger.Object) error, row ledger.Object) {
	if write != nil {
		_ = write(row)
	}
}

func mustNote(write func(ledger.Object) error, row ledger.Object) bool {
	return write == nil || write(row) == nil
}

// present appends key: value unless value is JS undefined (absent).
func present(row ledger.Object, key string, f ledger.Object) ledger.Object {
	for _, field := range f {
		if field.Key == key {
			return append(row, ledger.Field{Key: key, Value: field.Value})
		}
	}
	return row
}

// cameFromJudge is triage.mjs cameFromJudge.
func cameFromJudge(result ledger.Object) bool {
	sessions, _ := result.Get("sessions").([]any)
	for _, s := range sessions {
		if asObject(s).Get("role") == "judge" {
			return true
		}
	}
	return false
}

func renameID(f ledger.Object) ledger.Object {
	for _, field := range f {
		if field.Key == "id" {
			return ledger.Object{{Key: "finding", Value: field.Value}}
		}
	}
	return nil
}

func retry(state ledger.Object, kind string, reason any) ledger.Object {
	state.Set("next", nextStep("action", "retry", "reason", reason))
	state.Set("pending_kind", kind)
	return state
}

// CheckLoopInput reports ErrUnsupported for a round Go does not run (see
// CheckLoop). Called before the review session, never after.
func CheckLoopInput(state ledger.Object, kind string, deps LoopDeps) error {
	_, err := CheckLoop(state, kind, deps)
	return err
}

// CheckLoop reports ErrUnsupported for a round Go does not run: a kind other
// than full, recheck or patch_check, a level or escalation.stop_at Node's
// levelIndex throws on, a file whose text is not UTF-8 (Node decodes it
// lossily), or a recheck part not ported (planRecheck). spawns: the round
// runs a review or recheck session. It writes no row and no state.
func CheckLoop(state ledger.Object, kind string, deps LoopDeps) (spawns bool, err error) {
	if err := checkLoopBase(state, kind, deps); err != nil {
		return false, err
	}
	if kind == "full" {
		return true, nil
	}
	plan, err := planRecheck(state, kind, deps, true)
	if err != nil {
		return false, err
	}
	return plan.spawns(), nil
}

func checkLoopBase(state ledger.Object, kind string, deps LoopDeps) error {
	if kind != "full" && kind != "recheck" && kind != "patch_check" {
		return unsupported("a " + kind + " round is not ported")
	}
	if _, ok := state.Get("open").([]any); !ok {
		return unsupported("a review state with no open list")
	}
	if _, ok := state.Get("late").([]any); !ok && kind != "full" {
		return unsupported("a review state with no late list")
	}
	level, _ := state.Get("level").(string)
	if levelIndex(level) < 0 {
		return unsupported("a review state with an unknown level")
	}
	if levelIndex(settingsFor(deps.Cfg, deps.ExtraRounds).stopAt) < 0 {
		return unsupported("escalation.stop_at is not a level")
	}
	file, _ := state.Get("file").(string)
	if err := AssertPacketPath(deps.RepoRoot, file); err != nil {
		return unsupported("the packet path check refuses")
	}
	data, err := os.ReadFile(filepath.Join(deps.RepoRoot, file))
	if err != nil && !os.IsNotExist(err) {
		return unsupported("the reviewed file is unreadable")
	}
	if !utf8.Valid(data) {
		return unsupported("the reviewed file is not UTF-8")
	}
	return nil
}

// RunRound is fixloop.mjs runRound for kind `full`, with deps.Jev and no S2
// rule (the worker passes none). It mutates and returns state; an error
// is a throw (the caller's `unavailable` with next: retry).
func RunRound(state ledger.Object, deps LoopDeps, kind string) (ledger.Object, error) {
	if err := checkLoopBase(state, kind, deps); err != nil {
		return state, err
	}
	if kind != "full" {
		return runRecheck(state, deps, kind)
	}
	s := settingsFor(deps.Cfg, deps.ExtraRounds)
	file := state.Get("file").(string)
	hash, err := gitx.ContentHash(deps.RepoRoot, file)
	if err != nil {
		return state, err
	}
	content := ""
	if data, err := os.ReadFile(filepath.Join(deps.RepoRoot, file)); err == nil {
		content = string(data)
	}
	openBefore := len(state.Get("open").([]any))
	draft := state.Clone()
	draft.Set("next", nil)
	draft.Set("pending_kind", nil)

	result, err := deps.Review()
	if err != nil {
		return state, err
	}
	if result.Get("status") != "reviewed" {
		var reason any = "unavailable"
		if r := result.Get("reason"); r != nil {
			reason = r
		} else if st := result.Get("status"); st != nil {
			reason = st
		}
		return retry(state, kind, reason), nil
	}
	findings, _ := result.Get("findings").([]any)
	history, ids := []any{}, []any{}
	seenKey, seenID := map[string]bool{}, map[string]bool{}
	for _, v := range findings {
		f := asObject(v)
		if k := findingKey(f); !seenKey[k] {
			seenKey[k] = true
			history = append(history, k)
		}
		id := f.Get("id")
		idKey, _ := ledger.Stringify(id, false)
		if !seenID[string(idKey)] {
			seenID[string(idKey)] = true
			ids = append(ids, id)
		}
	}
	fixNow := triage(file, findings, cameFromJudge(result), "", deps)
	draft.Set("open", fixNow)

	draft.Set("round", float64(num(draft, "round")+1))
	draft.Set("rounds_at_level", float64(num(draft, "rounds_at_level")+1))
	if heavyRound(fixNow, s.warnThreshold) {
		draft.Set("warn_rounds_at_level", float64(num(draft, "warn_rounds_at_level")+1))
	}
	draft.Set("reviewed_content", content)
	if len(history) > 0 {
		draft.Set("finding_history", history)
	}
	if len(ids) > 0 {
		draft.Set("finding_ids", ids)
	}
	draft.Set("last_packet", nil)
	roundRow := ledger.Object{{Key: "event", Value: "review.round"}, {Key: "file", Value: file}, {Key: "round", Value: draft.Get("round")},
		{Key: "level", Value: draft.Get("level")}, {Key: "kind", Value: kind}, {Key: "open_before", Value: float64(openBefore)},
		{Key: "closed", Value: 0.0}, {Key: "new_in_hunks", Value: float64(len(fixNow))}, {Key: "late", Value: 0.0},
		{Key: "open_after", Value: float64(len(fixNow))}, {Key: "tokens_in", Value: nil}, {Key: "tokens_out", Value: nil}}
	// B630: a substitute review's round and approval say so
	substitute := result.Get("source") == SubstituteSource
	if substitute {
		roundRow.Set("source", SubstituteSource)
	}
	if len(fixNow) == 0 {
		draft.Set("status", "complete")
		draft.Set("next", nextStep("action", "complete"))
	} else {
		next := decideNext(draft, openBefore, s)
		draft.Set("next", next)
		if next.Get("action") == "stop" {
			draft.Set("status", "stopped")
			draft.Set("stop_content_hash", hash)
		}
	}
	rows := []ledger.Object{roundRow}
	if draft.Get("status") == "stopped" {
		open := []any{}
		for _, f := range fixNow {
			open = append(open, asObject(f).Get("id"))
		}
		rows = append(rows, ledger.Object{{Key: "event", Value: "review.cap"}, {Key: "file", Value: file}, {Key: "round", Value: draft.Get("round")},
			{Key: "reason", Value: draft.Get("next").(ledger.Object).Get("reason")}, {Key: "open", Value: open}})
	}
	for _, row := range rows {
		if !mustNote(deps.WriteRow, row) {
			return retry(state, kind, "ledger_write_failed"), nil
		}
	}
	// Object.assign(state, draft): existing keys keep their place
	for _, f := range draft {
		state.Set(f.Key, f.Value)
	}
	if state.Get("status") == "complete" {
		approved := ledger.Object{{Key: "event", Value: "review.approved"}, {Key: "file", Value: file}, {Key: "content_hash", Value: hash}, {Key: "round", Value: state.Get("round")}}
		if substitute {
			approved.Set("source", SubstituteSource)
		}
		note(deps.WriteRow, approved)
	}
	return state, nil
}
