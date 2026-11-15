package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/autopilot"
	"github.com/ricardov03/code-forge/native/blockstate"
	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/humanask"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/sign"
	"github.com/ricardov03/code-forge/native/state"
)

// B684: the owner's human close paths that used to hand over to Node.

const closeNoReviewsNoTTY = "--no-require-reviews needs the human at a terminal to confirm; the block stays open (there is no --yes)"

// closeNoReviews is Node's confirmNoReviews: no terminal is a usage error; at a
// terminal an active autopilot grant refuses (after the lazy expiry check),
// then the owner is asked. A yes runs the close with the reviews waived.
func closeNoReviews(args []string, step *landStep, handover *handoverReason, requireHuman bool, home, run, id string, stderr io.Writer,
	fallback func(handoverReason) int, fail func(error) int, usage func(string) int, endHook ...*func()) int {
	if !humanTTY() {
		return usage(closeNoReviewsNoTTY)
	}
	// The whole read-only boundary runs first: a hand-over never follows the
	// owner's answer. A refusal there is Node's, after the question.
	why := handoverReason(-1)
	if blockCloseHumanStep(args, &landStep{preflight: true}, &why, false) == landHandover {
		return fallback(why)
	}
	if code := closeNoReviewsAsk(home, run, id, requireHuman, stderr, fail, usage); code != 0 {
		return code
	}
	code := blockCloseHumanStep(args, step, handover, true, endHook...)
	if code == landHandover {
		fmt.Fprintln(stderr, "block close: state changed after human confirmation")
		return 1
	}
	return code
}

// closeNoReviewsAsk is confirmNoReviews at a terminal: Node's per-command
// expiry check (when close.require_human is off), checkExpiry, the refusal
// under an active grant, then the owner's question. 0 means yes.
func closeNoReviewsAsk(home, run, id string, requireHuman bool, stderr io.Writer, fail func(error) int, usage func(string) int) int {
	if !requireHuman {
		// Node's per-command expiry check ran before the close handler.
		closeGrantExpiry(home, run, stderr)
	}
	grant, err := autopilot.CheckExpiryNode(home, run, time.Now())
	if err != nil {
		return fail(err)
	}
	if grant.State == "active" {
		return fail(&state.Error{Code: "autopilot-active", Message: fmt.Sprintf("--no-require-reviews is refused while an autopilot grant is active on run %s (stop it first: code-forge autopilot stop --run %s)", run, run)})
	}
	result, err := humanAsk(fmt.Sprintf("Close block %s of run %s WITHOUT the per-file review checks? Only the owner may say yes.", id, run))
	if result == humanask.NoTTY {
		return usage(closeNoReviewsNoTTY)
	}
	if err != nil || result != humanask.Confirmed {
		fmt.Fprintf(stderr, "block %s close: cancelled — the review checks stay on\n", id)
		return 1
	}
	return 0
}

// closeNodeGrantState is Node's grantState for a grant Go's reader refuses:
// the status first, then `now >= Date.parse(until)` (B697: Date.parse as
// gatecheck reproduces it; NaN, such as an absent until, is active). known is
// false for an until whose Date.parse Go cannot reproduce.
func closeNodeGrantState(grant ledger.Object, now time.Time) (string, bool) {
	switch grant.Get("status") {
	case "stopped", "expired":
		return grant.Get("status").(string), true
	}
	until, has := grant.Get("until"), false
	for _, f := range grant {
		has = has || f.Key == "until"
	}
	if _, object := until.(ledger.Object); object {
		// String({...}) is "[object Object]": NaN.
		until = map[string]any{}
	}
	ms, finite, known := gatecheck.JSDateParse(until, has)
	if !known {
		return "", false
	}
	if finite && float64(now.UnixMilli()) >= ms {
		return "expired", true
	}
	return "active", true
}

// closeAckDecision is Node's decide ack action (prepareAckWaivers): one
// agent_waivers.acked row over the counted agent waivers, labelled per waiver.
func closeAckDecision(id string, rows []gatecheck.Row, key []byte, claimOf func(file, finding string) (string, bool)) (humanDecision, bool) {
	counted := gatecheck.AgentWaivers(rows, id, key)
	if len(counted) == 0 {
		return humanDecision{}, false
	}
	digest, ok := gatecheck.AgentWaiverDigest(counted)
	if !ok {
		return humanDecision{}, false
	}
	labels := []string{}
	for _, w := range counted {
		for _, k := range []string{"file", "finding", "severity", "reason", "verdict"} {
			if _, text := w[k].(string); !text {
				return humanDecision{}, false
			}
		}
		file, finding := w["file"].(string), w["finding"].(string)
		claim, named := claimOf(file, finding)
		if !named {
			claim = w["reason"].(string)
		}
		labels = append(labels, fmt.Sprintf("%s %s %s: agent-waivers-not-acked %s; reason: %s; verdict: %s", file, finding, w["severity"], claim, w["reason"], w["verdict"]))
	}
	label := strings.Join(labels, "; ")
	if !closeTextSafe(label) {
		return humanDecision{}, false
	}
	row := ledger.Object{{Key: "event", Value: "agent_waivers.acked"}, {Key: "block", Value: id}, {Key: "digest", Value: digest}, {Key: "count", Value: float64(len(counted))}, {Key: "by", Value: "human"}}
	return humanDecision{label, row}, true
}

// closeDecideTicket is resultFilesFor's ticket id form.
var closeDecideTicket = regexp.MustCompile(`^[0-9a-f]{24}$`)

// closeClaimOf is the decide preview's claimOf (B697): over the block's rows
// of the file whose MAC verifies, the row itself when it names the finding,
// its open list, then the findings of its ticket's signed result file (every
// review.result with a ticket, current ones included); the last text claim
// of the finding wins. named is false when none has one.
func closeClaimOf(mainTop, run, id string, rows []gatecheck.Row, key []byte) func(file, finding string) (string, bool) {
	results := map[string]any{}
	read := map[string]bool{}
	for _, r := range rows {
		ticket, isString := r["ticket"].(string)
		if r["event"] != "review.result" || r["block"] != id || gatecheck.NeverReviewed(r) || !isString || read[ticket] {
			continue
		}
		read[ticket] = true
		if !closeDecideTicket.MatchString(ticket) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(mainTop, ".code-forge", "reviews", run, ticket+".json"))
		if err != nil {
			continue
		}
		var result map[string]any
		if json.Unmarshal(data, &result) != nil || result == nil || result["ticket"] != ticket || result["run"] != run || !gatecheck.Verified(result, key) {
			continue
		}
		results[ticket] = result["findings"]
	}
	return func(file, finding string) (string, bool) {
		claim, named := "", false
		for _, r := range rows {
			if r["block"] != id || r["file"] != file || !gatecheck.Verified(r, key) {
				continue
			}
			found := []any{}
			if r["finding"] == finding {
				found = append(found, r)
			}
			found = append(found, blockstate.Array(r["open"])...)
			if t, has := r["ticket"]; has {
				// descriptions[r.ticket]: the key is String(ticket).
				found = append(found, blockstate.Array(results[gatecheck.JSString(t, true)])...)
			}
			for _, v := range found {
				f := blockstate.Map(v)
				if f == nil || f["id"] != finding && f["finding"] != finding {
					continue
				}
				if s, ok := f["claim"].(string); ok {
					claim, named = s, true
				}
			}
		}
		return claim, named
	}
}

// closeDecideSigned is a decision row as the preview's gate reads it: signed
// with the run, through the ledger's JSON shape.
func closeDecideSigned(run string, row ledger.Object, key []byte) (gatecheck.Row, bool) {
	signed := row.Map()
	signed["run"] = run
	signed, err := sign.SignRow(signed, key)
	if err != nil {
		return nil, false
	}
	data, err := ledger.Stringify(signed, false)
	if err != nil {
		return nil, false
	}
	var r map[string]any
	if json.Unmarshal(data, &r) != nil || !gatecheck.Verified(r, key) {
		return nil, false
	}
	return r, true
}

// closeDecideInput is what the owner's --decide preview reads.
type closeDecideInput struct {
	run, id, reason, prefix    string
	entry                      ledger.Object
	gate                       gatecheck.Result
	rows                       []gatecheck.Row
	key                        []byte
	claimOf                    func(file, finding string) (string, bool)
	ack                        *humanDecision
	transcript, transcriptFile string
}

// closeHumanDecisions is Node's owner --decide actions (B697): a waiver for
// each not_approved, late_unruled, review_cap and autopilot_waiver_invalid
// finding (after waivePrecheck), the agent-waiver acknowledgement, then an
// accepted hit for each remaining transcript rule_break. precheck is the
// error waivePrecheck throws, reported where Node reports it. supported is
// false for a shape Go does not reproduce (the close-decide handover).
func closeHumanDecisions(in closeDecideInput) (actions []humanDecision, preview []gatecheck.Row, precheck error, supported bool) {
	actions = []humanDecision{}
	preview = append([]gatecheck.Row{}, in.rows...)
	reason := humanTrim(in.reason)
	seen := map[string]bool{}
	for _, b := range in.gate.Blockers {
		switch b.Code {
		case "not_approved", "late_unruled", "review_cap", "autopilot_waiver_invalid":
		default:
			continue
		}
		if b.Raw != nil {
			// A row's file or finding that is not text: Node decides it only
			// when both are truthy, with the values as they are.
			if b.File != "" && gatecheck.Truthy(b.Raw.Finding, b.Raw.HasFinding) {
				return nil, nil, nil, false
			}
			continue
		}
		if b.File == "" || b.Finding == "" || b.Finding == "proof" {
			continue
		}
		pair := b.File + "\x00" + b.Finding
		if seen[pair] {
			continue
		}
		seen[pair] = true
		if err := closeWaivePrecheck(in, b.File, b.Finding); err != nil {
			return actions, in.rows, err, true
		}
		severity := blockstate.Severity(in.rows, in.id, b.File, b.Finding, in.key)
		if severity == "not-recorded" || severity == "unverified" {
			// recordedSeverity's severity is null for both: `?? 'unknown'`.
			severity = "unknown"
		}
		claim, named := in.claimOf(b.File, b.Finding)
		if !named {
			claim = b.Detail
		}
		label := fmt.Sprintf("%s %s %s: %s", b.File, b.Finding, severity, claim)
		if !closeTextSafe(label) {
			return nil, nil, nil, false
		}
		row := ledger.Object{{Key: "event", Value: "review.waived"}, {Key: "block", Value: in.id}, {Key: "file", Value: b.File}, {Key: "finding", Value: b.Finding}, {Key: "reason", Value: reason}, {Key: "by", Value: "human"}}
		signed, ok := closeDecideSigned(in.run, row, in.key)
		if !ok {
			return nil, nil, nil, false
		}
		preview = append(preview, signed)
		actions = append(actions, humanDecision{label, row})
	}
	if in.ack != nil {
		actions = append(actions, *in.ack)
	}
	lines := strings.Split(in.transcript, "\n")
	hitSeen := map[string]bool{}
	for _, b := range in.gate.Blockers {
		if b.Code != "rule_break" {
			continue
		}
		m := acceptHitLine.FindStringSubmatch(b.Detail)
		if m == nil {
			// A ledger rule_break row: no action; the preview refuses it.
			continue
		}
		line, err := strconv.Atoi(m[1])
		if err != nil || line < 1 || line > len(lines) || hitSeen[b.Detail] || in.transcriptFile == "" ||
			blockControl.MatchString(in.transcriptFile) || blockControl.MatchString(m[2]) {
			// prepareAcceptHit's own refusals and a hit named twice stay with Node.
			return nil, nil, nil, false
		}
		hitSeen[b.Detail] = true
		label := fmt.Sprintf("%s %s severity rule_break line %d: %s", in.transcriptFile, m[2], line, lines[line-1])
		if !closeTextSafe(label) {
			return nil, nil, nil, false
		}
		row := ledger.Object{{Key: "event", Value: "rule_break.accepted"}, {Key: "block", Value: in.id}, {Key: "rule", Value: m[2]}, {Key: "line", Value: float64(line)}, {Key: "line_sha256", Value: blockSHA([]byte(lines[line-1]))}, {Key: "reason", Value: reason}, {Key: "by", Value: "human"}}
		signed, ok := closeDecideSigned(in.run, row, in.key)
		if !ok {
			return nil, nil, nil, false
		}
		preview = append(preview, signed)
		actions = append(actions, humanDecision{label, row})
	}
	return actions, preview, nil, true
}

// closeWaivePrecheck is block.mjs's waivePrecheck for one decided finding: the
// finding and file forms, ownership, then a review row of the run naming it.
func closeWaivePrecheck(in closeDecideInput, file, finding string) error {
	if msg := blockstate.Finding(finding); msg != "" {
		return &state.Error{Code: "usage", Message: msg}
	}
	if msg := blockstate.File(file, false); msg != "" {
		return &state.Error{Code: "usage", Message: msg}
	}
	if !gatecheck.Owns(in.prefix, in.entry, file) {
		return &state.Error{Code: "waive-not-owned", Message: fmt.Sprintf("waive-not-owned: block %s does not own %s; nothing was recorded", in.id, file)}
	}
	for _, r := range in.rows {
		if r["run"] != in.run || r["block"] != in.id || r["file"] != file {
			continue
		}
		switch r["event"] {
		case "review.triage", "review.late_finding":
			if r["finding"] == finding {
				return nil
			}
		case "review.result":
			if slices.Contains(gatecheck.OpenFindingIDs(r["open"]), finding) {
				return nil
			}
		case "review.cap":
			if list, ok := r["open"].([]any); ok && slices.Contains(list, any(finding)) {
				return nil
			}
		}
	}
	return &state.Error{Code: "waive-unknown-finding", Message: fmt.Sprintf("waive-unknown-finding: no review row of block %s names finding %s in %s; nothing was recorded", in.id, finding, file)}
}

// closeGrantEndSafe is blockGrantEndSafe with the grant's until dated as
// Node's grantState dates it (B709: a date-only until is UTC midnight, a
// legacy form gatecheck.JSDateParse reads like V8 too).
func closeGrantEndSafe(record ledger.Object) bool {
	if !autopilot.SupportedNode(record) {
		return false
	}
	without := record.Clone()
	without.Delete("autopilot")
	return blockEndSafe(without)
}

// closeGrantExpiry is blockGrantExpiry with the until dated like Node.
func closeGrantExpiry(home, run string, stderr io.Writer) {
	if _, err := autopilot.CheckExpiryNode(home, run, time.Now()); err != nil {
		fmt.Fprintf(stderr, "autopilot: WARN the expiry check failed (%s)\n", blockIOMessage(err))
	}
}
