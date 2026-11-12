package review

// B517: the fix loop's recheck and patch_check rounds (src/review/fixloop.mjs
// runRound with kind `recheck` or `patch_check`) on the fix-hunk path:
// fixHunkDiff, the recheck packet (buildRecheckPacket: the `recheck` lens in
// hunk context, then the open findings), one L2 recheck session through the
// stub guard (recheckSession: B30's same-model timeout retry, the schema-miss
// row, the open-findings coverage check), stale answers, late findings and
// the terminal stop before a round (stopBeforeRound). B533: with deps.Jev,
// S1's resolved band closes an open finding before the packet is built; the
// rest go to the session (the dry pass plans with every open finding).
//
// Not ported (ErrUnsupported before anything runs): review.recheck_scope
// file, moved-code hints and cross-file evidence (a base with peers), the
// import-header context of a JS/TS/CSS/HTML/Astro/Vue packet, Markdown
// section rechecks (a Markdown packet over the budget), and every config
// shape levelGuards refuses. B545: review.second_levels, a levels.L2 ladder
// and review.routes run like Node's recheckSession.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
)

// importHeaderExts are packet.mjs importHeaderEnd's files: a recheck packet
// of one of them gets the import-header context, which is not ported.
var importHeaderExts = regexp.MustCompile(`(?i)\.(?:[cm]?[jt]sx?|css|scss|html?|astro|vue)$`)

// normaliseWhitespace is fixloop.mjs normaliseWhitespace.
func normaliseWhitespace(text string) string {
	return strings.Join(strings.FieldsFunc(trimJS(text), jsSpace), " ")
}

func valueOf(f ledger.Object, key string) (any, bool) { return lookup(f, key) }

// openFindingLine is fixloop.mjs openFindingLine.
func openFindingLine(f ledger.Object) string {
	id, hasID := valueOf(f, "id")
	sev, hasSev := valueOf(f, "severity")
	start, hasStart := valueOf(f, "line_start")
	end, hasEnd := valueOf(f, "line_end")
	claim := ""
	if c, ok := valueOf(f, "claim"); ok && c != nil {
		claim = jsString(c, true)
	}
	return "- " + jsString(id, hasID) + " (" + jsString(sev, hasSev) + ", lines " + jsString(start, hasStart) + "-" + jsString(end, hasEnd) + "): " + claim
}

// openFindingsText is fixloop.mjs openFindingsText.
func openFindingsText(open []any) string {
	evidence := ""
	if len(open) > 0 {
		lines := []string{}
		for _, v := range open {
			f := asObject(v)
			id, hasID := valueOf(f, "id")
			ev := ""
			if e, ok := valueOf(f, "evidence"); ok && e != nil {
				ev = jsString(e, true)
			}
			lines = append(lines, jsString(id, hasID)+": "+cutUTF16(normaliseWhitespace(ev), 300))
		}
		evidence = "## finding evidence\n" + strings.Join(lines, "\n") + "\n"
	}
	list := "(none)"
	if len(open) > 0 {
		lines := []string{}
		for _, v := range open {
			lines = append(lines, openFindingLine(asObject(v)))
		}
		list = strings.Join(lines, "\n")
	}
	return evidence + "## open findings\n" + list + "\n"
}

// fixHunkDiff is fixloop.mjs fixHunkDiff: `git diff --no-index` between the
// content reviewed last round and the current content, named `file`.
func fixHunkDiff(file, previous, current, workDir string, deps Deps) (*FileDiff, error) {
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return nil, err
	}
	tag, err := randomHex(deps.Rand)
	if err != nil {
		return nil, err
	}
	before := filepath.Join(workDir, "prev-"+tag)
	after := filepath.Join(workDir, "cur-"+tag)
	defer os.Remove(before)
	defer os.Remove(after)
	if err := os.WriteFile(before, []byte(previous), 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(after, []byte(current), 0o600); err != nil {
		return nil, err
	}
	out, code, ok := gitOK(workDir, gitx.ChildEnv(), []int{1}, "diff", "--no-index", "--no-color", "--no-ext-diff", "--no-textconv", "-U3", "--", before, after)
	if !ok {
		return nil, fmt.Errorf("git diff --no-index failed (exit %d)", code)
	}
	body := ""
	if strings.HasPrefix(out, "@@") {
		body = out
	} else if at := strings.Index(out, "\n@@"); at >= 0 {
		body = out[at+1:]
	}
	diffText := ""
	if body != "" {
		diffText = "--- a/" + file + "\n+++ b/" + file + "\n" + body
	}
	hunks, plus, minus := ParseDiff(diffText)
	content := current
	return &FileDiff{File: file, Kind: "tracked", DiffText: diffText, Content: &content, Hunks: hunks, PlusCount: plus, MinusCount: minus}, nil
}

// insideHunks is fixloop.mjs insideHunks.
func insideHunks(f ledger.Object, hunks []Hunk) bool {
	start, sok := f.Get("line_start").(float64)
	end, eok := f.Get("line_end").(float64)
	if !sok || !eok {
		return false
	}
	for _, h := range hunks {
		last := float64(h.NewStart + max(h.NewLines, 1) - 1)
		if start >= float64(h.NewStart) && end <= last && start <= end {
			return true
		}
	}
	return false
}

// buildRecheckPacket is fixloop.mjs buildRecheckPacket with no moved code and
// no cross-file evidence.
func buildRecheckPacket(diff *FileDiff, open []any, deps LoopDeps, paired string) (*Packet, error) {
	p, err := assemblePacketMode(diff, "recheck", deps.Input.RulesDigest, deps.Input.FactsExcerpt, deps.Cfg, paired, "recheck")
	if err != nil || p.Status != "ok" {
		return p, err
	}
	appended := openFindingsText(open)
	out := *p
	out.Text = p.Text + appended
	out.TokensIn = p.TokensIn + EstimateTokens(appended)
	return &out, nil
}

// recheckPlan is runRound's recheck before any session or write.
type recheckPlan struct {
	rel, content, hash string
	diff               *FileDiff
	noFixHunks         bool
	pending            []any
	jevClosed          int     // open findings S1's resolved band closed (B533)
	whole              *Packet // nil: no session (no hunks or nothing open)
}

func idKey(v any) string {
	b, _ := ledger.Stringify(v, false)
	return string(b)
}

// planRecheck is runRound up to the recheck session: the fix hunks and the
// whole packet. dry: the dry pass (no seam randomness, the work dir it made
// is removed again). ErrUnsupported for a part not ported.
func planRecheck(state ledger.Object, kind string, deps LoopDeps, dry bool) (*recheckPlan, error) {
	review := section(deps.Cfg, "review")
	if review["recheck_scope"] == "file" {
		return nil, unsupported("review.recheck_scope file (a recheck against the block base) is not ported")
	}
	if deps.Input.Base != "" {
		if deps.Input.HasPeerDiffs {
			return nil, unsupported("moved-code hints and cross-file evidence of a recheck are not ported")
		}
		peers := deps.Input.Peers
		if peers == nil {
			peers = deps.Input.ChangedFiles
		}
		file, _ := state.Get("file").(string)
		for _, p := range peers {
			if p != file {
				return nil, unsupported("moved-code hints and cross-file evidence of a recheck are not ported")
			}
		}
	}
	if deps.Input.WorkDir == "" {
		return nil, unsupported("a recheck with no work dir")
	}
	previous := ""
	switch rc := state.Get("reviewed_content").(type) {
	case nil:
	case string:
		previous = rc
	default:
		return nil, unsupported("a review state whose reviewed_content is not a string")
	}
	file := state.Get("file").(string)
	hash, err := gitx.ContentHash(deps.RepoRoot, file)
	if err != nil {
		return nil, err
	}
	content := ""
	if data, err := os.ReadFile(filepath.Join(deps.RepoRoot, file)); err == nil {
		content = string(data)
	}
	p := &recheckPlan{rel: file, content: content, hash: hash}
	session := deps.Session
	if dry {
		session.Rand = nil
		if _, err := os.Stat(deps.Input.WorkDir); os.IsNotExist(err) {
			defer os.Remove(deps.Input.WorkDir)
		}
	}
	if p.diff, err = fixHunkDiff(file, previous, content, deps.Input.WorkDir, session); err != nil {
		return nil, err
	}
	reviewed, isString := state.Get("reviewed_content").(string)
	if kind == "recheck" && ((isString && reviewed == content) || len(p.diff.Hunks) == 0) {
		p.noFixHunks = true
		return p, nil
	}
	if len(p.diff.Hunks) == 0 {
		return p, nil
	}
	open, _ := state.Get("open").([]any)
	p.pending = open
	if !dry && deps.Jev != nil {
		p.pending = []any{}
		for _, f := range open {
			if resolvedClosed(f, p.diff.DiffText, deps) {
				p.jevClosed++
			} else {
				p.pending = append(p.pending, f)
			}
		}
	}
	if len(p.pending) == 0 {
		return p, nil
	}
	if importHeaderExts.MatchString(file) {
		return nil, unsupported("the import-header context of a recheck packet is not ported")
	}
	changed := deps.Input.ChangedFiles
	if changed == nil {
		changed = deps.Input.Peers
	}
	paired := PairedContext(PairedInput{RepoRoot: deps.RepoRoot, File: file, OwnedFiles: deps.Input.OwnedFiles, ChangedFiles: changed,
		TestFiles: deps.Input.TestFiles, Secrets: deps.Input.Secrets})
	whole, err := buildRecheckPacket(p.diff, p.pending, deps, paired)
	if err != nil {
		return nil, err
	}
	if whole.Status != "ok" && markdownFile.MatchString(file) {
		return nil, unsupported("a Markdown recheck section by section is not ported")
	}
	if whole.Status == "ok" {
		// B545: the session's ladder, second level and routes run like
		// Node's; only a config shape the port does not read stays with Node
		if err := levelGuards(deps.Cfg, "L2"); err != nil {
			return nil, err
		}
	}
	p.whole = whole
	return p, nil
}

// recheckSpawns is whether a planned recheck runs its session.
func (p *recheckPlan) spawns() bool {
	return p != nil && !p.noFixHunks && p.whole != nil && p.whole.Status == "ok"
}

type recheckResult struct {
	verdict             Verdict
	tokensIn, tokensOut any
}

// recheckSession is fixloop.mjs recheckSession (B545: with its recovery):
// the prompt file, spawnWithTimeoutRetry on the run's config (B30's
// same-model timeout retry; a timed-out packet's second broken result tries
// review.second_levels.L2 through afterSchemaMiss), the stub guard plus the
// open-findings coverage check, and a schema miss's signed row and its one
// second-level try (that level's own ladder on a timeout or capacity).
func recheckSession(packet *Packet, deps LoopDeps, open []any) (recheckResult, error) {
	sd := deps.Session
	sd.WriteRow = deps.WriteRow
	workDir := deps.Input.WorkDir
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return recheckResult{}, err
	}
	suffix, err := randomHex(sd.Rand)
	if err != nil {
		return recheckResult{}, err
	}
	promptPath := filepath.Join(workDir, "recheck-"+suffix+".md")
	if err := os.WriteFile(promptPath, []byte(packet.Text), 0o600); err != nil {
		return recheckResult{}, err
	}
	defer os.Remove(promptPath)
	spec := sessionSpec{lens: "recheck", role: "reviewer", level: "L2"}
	sum := sha256.Sum256([]byte(packet.Text))
	r := &oneRun{spec: spec, text: packet.Text, hash: hex.EncodeToString(sum[:]), promptPath: promptPath, packet: packet,
		in: Input{Cfg: deps.Cfg, WorkDir: workDir}, deps: sd, recheck: true, timeouts: map[string]int{}}
	validate := func(res ledger.Object) Verdict {
		verdict := ValidateReview(res, packet.HunkHeaders, MinTokensOut(deps.Cfg))
		if !verdict.OK {
			return verdict
		}
		ids := map[string]bool{}
		resolved, _ := verdict.Review.Get("resolved").([]any)
		for _, entry := range resolved {
			ids[idKey(asObject(entry).Get("id"))] = true
		}
		for _, f := range open {
			if !ids[idKey(asObject(f).Get("id"))] {
				return Verdict{Reason: "schema", Detail: "resolved does not cover the packet open findings"}
			}
		}
		return verdict
	}
	var first retryResult
	if sd.Spawn != nil {
		// the first attempt runs on the spawner's own config (no cfg: no ladder)
		if first, err = r.timeoutRetry(nil, 0, false); err != nil {
			return recheckResult{}, err
		}
	}
	res := first.res
	verdict := validate(res)
	var tokensIn, tokensOut any
	if first.to != nil {
		if !verdict.OK && verdict.Reason == "schema" {
			sd.note(schemaInvalidRow(spec, res, r.hash, true))
		}
		tokensIn = addTokens(get(usageOf(first.broken), "tokens_in"), get(usageOf(res), "tokens_in"))
		tokensOut = addTokens(get(usageOf(first.broken), "tokens_out"), get(usageOf(res), "tokens_out"))
	} else {
		tokensIn = get(usageOf(res), "tokens_in")
		tokensOut = get(usageOf(res), "tokens_out")
	}
	if first.to == nil && !verdict.OK && verdict.Reason == "schema" {
		second, err := r.tryNext(res, "schema")
		if err != nil {
			return recheckResult{}, err
		}
		if second != nil {
			verdict = validate(second.res)
			tokensIn = addTokens(tokensIn, get(usageOf(second.res), "tokens_in"))
			tokensOut = addTokens(tokensOut, get(usageOf(second.res), "tokens_out"))
			if !verdict.OK && verdict.Reason == "schema" {
				sd.note(schemaInvalidRow(spec, second.res, r.hash, true))
			}
		}
	}
	return recheckResult{verdict: verdict, tokensIn: tokensIn, tokensOut: tokensOut}, nil
}

// stopBeforeRound is fixloop.mjs stopBeforeRound: a non-ok recheck packet is
// a terminal stop that is not a counted round.
func stopBeforeRound(state ledger.Object, deps LoopDeps, kind, file, contentHash string, packet *Packet) ledger.Object {
	reason := packet.Status
	open := []any{}
	for _, f := range state.Get("open").([]any) {
		open = append(open, asObject(f).Get("id"))
	}
	size := ledger.Object{{Key: "tokens_in", Value: float64(packet.TokensIn)}, {Key: "budget", Value: float64(packet.Budget)}}
	row := ledger.Object{{Key: "event", Value: "review.cap"}, {Key: "file", Value: file}, {Key: "round", Value: state.Get("round")}, {Key: "reason", Value: reason}, {Key: "open", Value: open}}
	row = append(row, size...)
	row = append(row, ledger.Field{Key: "lens", Value: kind})
	if !mustNote(deps.WriteRow, row) {
		return retry(state, kind, "ledger_write_failed")
	}
	state.Set("status", "stopped")
	state.Set("stop_content_hash", contentHash)
	state.Set("stop_lens", kind)
	state.Set("next", append(ledger.Object{{Key: "action", Value: "stop"}, {Key: "reason", Value: reason}}, size...))
	state.Set("pending_kind", nil)
	return state
}

// addAny is fixloop.mjs sum.
func addAny(a, b any) any { return addTokens(a, b) }

// runRecheck is runRound's recheck / patch_check branch.
func runRecheck(state ledger.Object, deps LoopDeps, kind string) (ledger.Object, error) {
	s := settingsFor(deps.Cfg, deps.ExtraRounds)
	late := "sweep"
	if section(deps.Cfg, "review")["late_findings"] == "block" {
		late = "block"
	}
	plan, err := planRecheck(state, kind, deps, false)
	if err != nil {
		return state, err
	}
	stateOpen, _ := state.Get("open").([]any)
	stateLate, _ := state.Get("late").([]any)
	openBefore := len(stateOpen)
	draft := state.Clone()
	draft.Set("next", nil)
	draft.Set("pending_kind", nil)
	draft.Set("open", append([]any{}, stateOpen...))
	draftLate := append([]any{}, stateLate...)
	closed, newInHunks := plan.jevClosed, 0
	lateRows, staleRows := []ledger.Object{}, []ledger.Object{}

	// the ordered sets of fixloop.mjs (JS Set insertion order)
	type orderedSet struct {
		seen  map[string]bool
		items []any
	}
	add := func(set *orderedSet, v any) {
		if set.seen == nil {
			set.seen = map[string]bool{}
		}
		if k := idKey(v); !set.seen[k] {
			set.seen[k] = true
			set.items = append(set.items, v)
		}
	}
	var history, knownIDs, roundFindings, roundIDs, repeated orderedSet
	if h, ok := state.Get("finding_history").([]any); ok {
		for _, k := range h {
			add(&history, k)
		}
	}
	for _, f := range stateOpen {
		add(&history, findingKey(asObject(f)))
	}
	for _, f := range stateLate {
		add(&history, findingKey(asObject(f)))
	}
	if ids, ok := state.Get("finding_ids").([]any); ok {
		for _, id := range ids {
			add(&knownIDs, id)
		}
	}
	for _, f := range stateOpen {
		add(&knownIDs, asObject(f).Get("id"))
	}
	for _, f := range stateLate {
		add(&knownIDs, asObject(f).Get("id"))
	}
	var tokensIn, tokensOut any
	var packetInfo ledger.Object

	if plan.noFixHunks {
		state.Set("next", nextStep("action", "fix", "level", state.Get("level"), "trigger", nil, "reason", "no-fix-hunks"))
		state.Set("pending_kind", nil)
		return state, nil
	}
	if len(plan.diff.Hunks) > 0 {
		pending := plan.pending
		draft.Set("open", []any{})
		if len(pending) > 0 {
			if plan.whole.Status != "ok" {
				return stopBeforeRound(state, deps, kind, plan.rel, plan.hash, plan.whole), nil
			}
			packet := plan.whole
			currentText := normaliseWhitespace(plan.content + "\n")
			packetInfo = ledger.Object{{Key: "context_mode", Value: packet.ContextMode}, {Key: "context_lines", Value: float64(packet.ContextLine)}}
			session, err := recheckSession(packet, deps, pending)
			if err != nil {
				return state, err
			}
			tokensIn = addAny(tokensIn, session.tokensIn)
			tokensOut = addAny(tokensOut, session.tokensOut)
			if !session.verdict.OK {
				note(deps.WriteRow, ledger.Object{{Key: "event", Value: "review.unavailable"}, {Key: "file", Value: plan.rel}, {Key: "lens", Value: "recheck"}, {Key: "reason", Value: session.verdict.Reason}})
				reason := session.verdict.Reason
				if reason == "" {
					reason = "unavailable"
				}
				return retry(state, kind, reason), nil
			}
			review := session.verdict.Review
			resolvedAnswers, _ := review.Get("resolved").([]any)
			resolved, stale := map[int]bool{}, map[int]bool{}
			nextRound := float64(num(state, "round") + 1)
			for i, v := range pending {
				f := asObject(v)
				var answer ledger.Object
				for _, r := range resolvedAnswers {
					if idKey(asObject(r).Get("id")) == idKey(f.Get("id")) {
						answer = asObject(r)
						break
					}
				}
				if answer != nil && answer.Get("resolved") == true {
					resolved[i] = true
					continue
				}
				lines := ""
				if answer != nil {
					if l, ok := answer.Get("current_lines").(string); ok {
						lines = l
					}
				}
				quote := normaliseWhitespace(lines)
				if quote != "" && !strings.Contains(currentText, quote) {
					stale[i] = true
					staleRows = append(staleRows, ledger.Object{{Key: "event", Value: "review.stale_finding"}, {Key: "file", Value: plan.rel}, {Key: "round", Value: nextRound},
						{Key: "finding", Value: f.Get("id")}, {Key: "reason", Value: "current_lines_not_found"}})
				}
			}
			found, _ := review.Get("findings").([]any)
			inside, blocking := []any{}, []any{}
			for _, v := range found {
				f := asObject(v)
				key := findingKey(f)
				add(&roundFindings, key)
				add(&roundIDs, f.Get("id"))
				if kind == "recheck" && !knownIDs.seen[idKey(f.Get("id"))] && history.seen[idKey(key)] {
					f.Set("repeated", true)
					add(&repeated, f.Get("id"))
				}
				if insideHunks(f, plan.diff.Hunks) {
					inside = append(inside, f)
					continue
				}
				draftLate = append(draftLate, f)
				row := ledger.Object{{Key: "event", Value: "review.late_finding"}, {Key: "file", Value: plan.rel}, {Key: "round", Value: nextRound}}
				row = present(row, "finding", renameID(f))
				row = present(row, "severity", f)
				row = present(row, "line_start", f)
				row = present(row, "line_end", f)
				lateRows = append(lateRows, append(row, ledger.Field{Key: "mode", Value: late}))
				if late == "block" && f.Get("severity") != "nit" {
					blocking = append(blocking, f)
				}
			}
			stillOpen := []any{}
			for i, f := range pending {
				if !resolved[i] && !stale[i] {
					stillOpen = append(stillOpen, f)
				}
			}
			closed += len(pending) - len(stillOpen)
			fixNow := triage(plan.rel, inside, false, plan.diff.DiffText, deps)
			newInHunks = len(fixNow)
			open := append(append(stillOpen, fixNow...), blocking...)
			draft.Set("open", open)
		}
	}
	// A patch_check with no fix hunks closed nothing: B73 still stops it.
	draft.Set("late", draftLate)
	draftOpen := draft.Get("open").([]any)
	allRepeated, anyRepeated := true, false
	for _, f := range draftOpen {
		if truthyValue(asObject(f).Get("repeated")) {
			anyRepeated = true
		} else {
			allRepeated = false
		}
	}
	unweighted := (len(staleRows) > 0 || len(repeated.items) > 0 || anyRepeated) && allRepeated
	if kind != "patch_check" {
		draft.Set("round", float64(num(draft, "round")+1))
		if !unweighted {
			draft.Set("rounds_at_level", float64(num(draft, "rounds_at_level")+1))
		}
		if !unweighted && heavyRound(draftOpen, s.warnThreshold) {
			draft.Set("warn_rounds_at_level", float64(num(draft, "warn_rounds_at_level")+1))
		}
	} else {
		draft.Set("rounds_at_level", 0.0)
		draft.Set("warn_rounds_at_level", 0.0)
	}
	draft.Set("reviewed_content", plan.content)
	if len(history.items) > 0 || len(roundFindings.items) > 0 {
		all := history
		for _, k := range roundFindings.items {
			add(&all, k)
		}
		draft.Set("finding_history", append([]any{}, all.items...))
	}
	if len(knownIDs.items) > 0 || len(roundIDs.items) > 0 {
		all := knownIDs
		for _, id := range roundIDs.items {
			add(&all, id)
		}
		draft.Set("finding_ids", append([]any{}, all.items...))
	}
	if packetInfo != nil {
		draft.Set("last_packet", packetInfo)
	} else {
		draft.Set("last_packet", nil)
	}
	roundRow := ledger.Object{{Key: "event", Value: "review.round"}, {Key: "file", Value: plan.rel}, {Key: "round", Value: draft.Get("round")},
		{Key: "level", Value: draft.Get("level")}, {Key: "kind", Value: kind}, {Key: "open_before", Value: float64(openBefore)},
		{Key: "closed", Value: float64(closed)}, {Key: "new_in_hunks", Value: float64(newInHunks)}, {Key: "late", Value: float64(len(lateRows))},
		{Key: "open_after", Value: float64(len(draftOpen))}, {Key: "tokens_in", Value: tokensIn}, {Key: "tokens_out", Value: tokensOut}}
	if packetInfo != nil {
		roundRow = append(roundRow, ledger.Field{Key: "context_mode", Value: packetInfo.Get("context_mode")})
	}
	if len(repeated.items) > 0 {
		roundRow = append(roundRow, ledger.Field{Key: "repeated", Value: append([]any{}, repeated.items...)})
	}
	if len(draftOpen) == 0 {
		draft.Set("status", "complete")
		draft.Set("next", nextStep("action", "complete"))
	} else {
		after := map[string]bool{}
		for _, f := range draftOpen {
			after[idKey(asObject(f).Get("id"))] = true
		}
		var closedSet orderedSet
		for _, f := range stateOpen {
			if id := asObject(f).Get("id"); !after[idKey(id)] {
				add(&closedSet, id)
			}
		}
		var next ledger.Object
		if unweighted && kind != "patch_check" {
			next = nextStep("action", "fix", "level", draft.Get("level"), "trigger", nil)
		} else {
			next = decideNextKind(draft, openBefore, s, kind, len(closedSet.items))
		}
		draft.Set("next", next)
		if next.Get("action") == "stop" {
			draft.Set("status", "stopped")
			draft.Set("stop_content_hash", plan.hash)
		}
	}
	rows := append(append(staleRows, lateRows...), roundRow)
	if draft.Get("status") == "stopped" {
		open := []any{}
		for _, f := range draftOpen {
			open = append(open, asObject(f).Get("id"))
		}
		rows = append(rows, ledger.Object{{Key: "event", Value: "review.cap"}, {Key: "file", Value: plan.rel}, {Key: "round", Value: draft.Get("round")},
			{Key: "reason", Value: draft.Get("next").(ledger.Object).Get("reason")}, {Key: "open", Value: open}})
	}
	for _, row := range rows {
		if !mustNote(deps.WriteRow, row) {
			return retry(state, kind, "ledger_write_failed"), nil
		}
	}
	for _, f := range draft {
		state.Set(f.Key, f.Value)
	}
	if state.Get("status") == "complete" {
		note(deps.WriteRow, ledger.Object{{Key: "event", Value: "review.approved"}, {Key: "file", Value: plan.rel}, {Key: "content_hash", Value: plan.hash}, {Key: "round", Value: state.Get("round")}})
	}
	return state, nil
}

// truthyValue is JS truthiness for a JSON value.
func truthyValue(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0 && x == x
	case string:
		return x != ""
	}
	return true
}
