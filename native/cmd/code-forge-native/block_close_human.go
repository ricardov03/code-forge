package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/autopilot"
	"github.com/ricardov03/code-forge/native/blockstate"
	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/humanask"
	"github.com/ricardov03/code-forge/native/ledger"
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
	if !requireHuman {
		// Node's per-command expiry check ran before the close handler.
		blockGrantExpiry(home, run, stderr)
	}
	grant, err := autopilot.CheckExpiry(home, run, time.Now())
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
	code := blockCloseHumanStep(args, step, handover, true, endHook...)
	if code == landHandover {
		fmt.Fprintln(stderr, "block close: state changed after human confirmation")
		return 1
	}
	return code
}

// closeNodeGrantState is Node's grantState for a grant Go's reader refuses:
// the status first, then the until clock (an absent until is NaN, so active).
// known is false for an until text whose Date.parse Go cannot reproduce.
func closeNodeGrantState(grant ledger.Object, now time.Time) (string, bool) {
	switch grant.Get("status") {
	case "stopped", "expired":
		return grant.Get("status").(string), true
	}
	until, has := grant.Get("until"), false
	for _, f := range grant {
		has = has || f.Key == "until"
	}
	if !has {
		return "active", true
	}
	text, ok := until.(string)
	if !ok {
		return "", false
	}
	at, err := time.Parse("2006-01-02T15:04:05.000Z", text)
	if err != nil {
		return "", false
	}
	if !now.Before(at) {
		return "expired", true
	}
	return "active", true
}

// closeAckDecision is Node's decide ack action (prepareAckWaivers): one
// agent_waivers.acked row over the counted agent waivers, labelled per waiver.
func closeAckDecision(run, id string, rows []gatecheck.Row, key []byte) (humanDecision, bool) {
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
		claim, named, supported := closeAckClaim(run, id, file, finding, rows, key)
		if !supported {
			return humanDecision{}, false
		}
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

// closeAckClaim is Node's claimOf over signed rows: the last text claim of the
// finding in the row itself or its open list. A row with a ticket may name a
// result file's findings, which stays with Node.
func closeAckClaim(run, id, file, finding string, rows []gatecheck.Row, key []byte) (string, bool, bool) {
	claim, named := "", false
	for _, r := range rows {
		if r["run"] != run || r["block"] != id || r["file"] != file {
			continue
		}
		if r["ticket"] != nil {
			return "", false, false
		}
		if !gatecheck.Verified(r, key) {
			continue
		}
		found := []map[string]any{}
		if r["finding"] == finding {
			found = append(found, r)
		}
		for _, v := range blockstate.Array(r["open"]) {
			found = append(found, blockstate.Map(v))
		}
		for _, f := range found {
			if f == nil || f["id"] != finding && f["finding"] != finding {
				continue
			}
			if s, ok := f["claim"].(string); ok {
				claim, named = s, true
			}
		}
	}
	return claim, named, true
}
