package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/blockstate"
	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/sign"
)

// B390: under an active autopilot grant a non-TTY `block close --decide`
// stands in for the owner on the decisions the owner delegated by granting
// autopilot: a transcript rule_break hit, and a warning (major) or nit (minor)
// finding the grant's waive:<severity> scope allows. Criticals, ledger
// rule_break rows, review caps and the rest stay with the owner. Every
// decision run is recorded first as one signed close.decision row, which
// `code-forge decisions list` prints for the owner's review.

var autopilotHitDetail = regexp.MustCompile(`^transcript line ([1-9][0-9]*): (.+)$`)

// autopilotDecideRefusal is the one line printed when autopilot may not decide.
func autopilotDecideRefusal(why string) string {
	return "block close: cannot decide under autopilot: " + why + "; nothing was recorded"
}

func autopilotStamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func autopilotDecisionID() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "ad-" + hex.EncodeToString(b), nil
}

// autopilotGrantRow is the run's signed autopilot.grant row for grantID; the
// gate accepts an autopilot decision only against it.
func autopilotGrantRow(rows []gatecheck.Row, run, grantID string, key []byte) gatecheck.Row {
	for _, r := range rows {
		if r["event"] == "autopilot.grant" && r["run"] == run && r["grant_id"] == grantID && gatecheck.Verified(r, key) {
			return r
		}
	}
	return nil
}

// autopilotDecisions builds the signed rows for each decidable blocker. It
// returns the rows to write (the close.decision row first), the preview rows
// for re-evaluating the gate, or a refusal reason when any blocker must stay
// with the owner. supported=false means the shape is not understood here.
func autopilotDecisions(run, id, reason, transcript string, grant ledger.Object, gate gatecheck.Result, rows []gatecheck.Row, key []byte, now time.Time) (decisions []humanDecision, preview []gatecheck.Row, refusal string, supported bool) {
	grantID := blockString(grant.Get("grant_id"))
	if autopilotGrantRow(rows, run, grantID, key) == nil {
		return nil, nil, "grant " + grantID + " has no signed autopilot.grant row in this run", true
	}
	scopes, _ := grant.Get("scopes").([]any)
	deny, _ := grant.Get("deny").([]any)
	reason = humanTrim(reason)
	ts := autopilotStamp(now)
	lines := strings.Split(transcript, "\n")
	preview = append([]gatecheck.Row{}, rows...)
	ruleBreaks, findings := []any{}, []any{}
	seen := map[string]bool{}
	for _, b := range gate.Blockers {
		switch b.Code {
		case "rule_break":
			m := autopilotHitDetail.FindStringSubmatch(b.Detail)
			if m == nil {
				return nil, nil, b.Detail + " is a ledger rule_break, not a transcript hit; only the owner decides it", true
			}
			line, _ := strconv.Atoi(m[1])
			if line > len(lines) {
				return nil, nil, "", false
			}
			if seen["hit\x00"+b.Detail] {
				continue
			}
			seen["hit\x00"+b.Detail] = true
			sum := sha256.Sum256([]byte(lines[line-1]))
			row := ledger.Object{{Key: "event", Value: "rule_break.accepted"}, {Key: "block", Value: id}, {Key: "rule", Value: m[2]}, {Key: "line", Value: line}, {Key: "line_sha256", Value: hex.EncodeToString(sum[:])}, {Key: "reason", Value: reason}, {Key: "by", Value: "autopilot"}, {Key: "grant_id", Value: grantID}, {Key: "ts", Value: ts}}
			decisions = append(decisions, humanDecision{fmt.Sprintf("accept rule_break %s at transcript line %d", m[2], line), row})
			ruleBreaks = append(ruleBreaks, ledger.Object{{Key: "rule", Value: m[2]}, {Key: "line", Value: line}})
		case "not_approved", "late_unruled":
			if b.File == "" || b.Finding == "" || b.Finding == "proof" {
				continue
			}
			if blockstate.Finding(b.Finding) != "" || blockstate.File(b.File, false) != "" {
				return nil, nil, "", false
			}
			pair := b.File + "\x00" + b.Finding
			if seen[pair] {
				continue
			}
			seen[pair] = true
			severity := blockstate.Severity(rows, id, b.File, b.Finding, key)
			if severity != "warning" && severity != "nit" {
				if severity == "not-recorded" {
					return nil, nil, b.Finding + " in " + b.File + " has no recorded severity; only the owner decides it", true
				}
				return nil, nil, b.Finding + " in " + b.File + " is " + severity + "; only the owner decides it", true
			}
			scope := "waive:" + severity
			if !slices.Contains(scopes, any(scope)) || slices.Contains(deny, any(scope)) {
				return nil, nil, "grant " + grantID + " does not allow " + scope + " (" + b.Finding + " in " + b.File + "); the owner decides it", true
			}
			decisionID, err := autopilotDecisionID()
			if err != nil {
				return nil, nil, "", false
			}
			subject := ledger.Object{{Key: "block", Value: id}, {Key: "file", Value: b.File}, {Key: "finding", Value: b.Finding}}
			decision := ledger.Object{{Key: "event", Value: "autopilot.decision"}, {Key: "decision_id", Value: decisionID}, {Key: "grant_id", Value: grantID}, {Key: "scope", Value: scope}, {Key: "subject", Value: subject}, {Key: "question", Value: "waive " + b.Finding + " in " + b.File + " at block close"}, {Key: "options", Value: []any{"waive", "owner"}}, {Key: "decision", Value: "waive"}, {Key: "within_scope", Value: true}, {Key: "reason", Value: reason}, {Key: "escalate", Value: false}, {Key: "acted", Value: true}, {Key: "to_owner", Value: false}, {Key: "level", Value: "agent"}, {Key: "ts", Value: ts}}
			waiver := ledger.Object{{Key: "event", Value: "review.waived"}, {Key: "block", Value: id}, {Key: "file", Value: b.File}, {Key: "finding", Value: b.Finding}, {Key: "reason", Value: reason}, {Key: "by", Value: "autopilot"}, {Key: "grant_id", Value: grantID}, {Key: "decision_id", Value: decisionID}, {Key: "severity", Value: severity}, {Key: "ts", Value: ts}}
			label := fmt.Sprintf("waive %s %s %s", b.File, b.Finding, severity)
			decisions = append(decisions, humanDecision{label + " (decision)", decision}, humanDecision{label, waiver})
			findings = append(findings, ledger.Object{{Key: "file", Value: b.File}, {Key: "finding", Value: b.Finding}, {Key: "severity", Value: severity}, {Key: "decision_id", Value: decisionID}})
		case "review_cap", "autopilot_waiver_invalid", "agent-waivers-not-acked":
			return nil, nil, b.Code + " (" + b.Detail + ") stays with the owner", true
		}
	}
	if len(decisions) == 0 {
		return nil, nil, "nothing here is an autopilot decision (no transcript rule_break, no open warning or nit finding)", true
	}
	ledgerRow := ledger.Object{{Key: "event", Value: "close.decision"}, {Key: "block", Value: id}, {Key: "actor", Value: "autopilot"}, {Key: "grant_id", Value: grantID}, {Key: "reason", Value: reason}, {Key: "rule_breaks", Value: ruleBreaks}, {Key: "findings", Value: findings}, {Key: "ts", Value: ts}}
	decisions = append([]humanDecision{{"record the decision in the decisions ledger", ledgerRow}}, decisions...)
	for _, d := range decisions {
		signed := d.row.Map()
		signed["run"] = run
		signed, err := sign.SignRow(signed, key)
		if err != nil {
			return nil, nil, "", false
		}
		// Round-trip through JSON so the preview rows have the ledger's shape.
		data, err := ledger.Stringify(signed, false)
		if err != nil {
			return nil, nil, "", false
		}
		var r map[string]any
		if json.Unmarshal(data, &r) != nil || !gatecheck.Verified(r, key) {
			return nil, nil, "", false
		}
		preview = append(preview, r)
	}
	return decisions, preview, "", true
}
