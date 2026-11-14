package main

import (
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/autopilot"
	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/ledger"
)

// B630 (issue #37 C3/C4): block close counts a substitute review (a
// review.approved row with source: substitute) only under an active
// autopilot grant with scope review:substitute, with the signed
// autopilot.decision row that ran it, and only when every gate is green (no
// gate blocker and a signed gate.done row with all_ok). A file with a normal
// signed approval at its content is not a substitute file. Criticals from the
// substitute already block: it writes no approval while findings are open.

const substCloseCode = "substitute_review"

// substCloseCheck is the check for every kept file approved only by a
// substitute review; a zero closeCheck when there is none or each counts.
func substCloseCheck(record ledger.Object, run, id string, rows []gatecheck.Row, key []byte, files []gatecheck.File, gate gatecheck.Result, now time.Time) closeCheck {
	var subst []gatecheck.File
	for _, f := range files {
		normal, substitute := false, false
		for _, r := range rows {
			if r["event"] != "review.approved" || r["run"] != run || r["block"] != id || r["file"] != f.File || r["content_hash"] != f.Hash || !gatecheck.Verified(r, key) {
				continue
			}
			if r["source"] == "substitute" {
				substitute = true
			} else {
				normal = true
			}
		}
		if substitute && !normal {
			subst = append(subst, f)
		}
	}
	if len(subst) == 0 {
		return closeCheck{}
	}
	shared := []string{}
	grant, err := autopilot.Read(record, now)
	if err != nil {
		shared = append(shared, "the autopilot grant cannot be read")
	} else if why := autopilot.ScopeRefusal(grant, autopilot.SubstituteScope); why != "" {
		shared = append(shared, "no active autopilot grant with scope "+autopilot.SubstituteScope+" ("+why+")")
	}
	if len(gate.Blockers) > 0 {
		shared = append(shared, "the gate is not green ("+gatecheck.Reason(gate.Blockers)+")")
	}
	green := false
	for _, r := range rows {
		if r["event"] == "gate.done" && r["run"] == run && r["block"] == id && r["all_ok"] == true && gatecheck.Verified(r, key) {
			green = true
		}
	}
	if !green {
		shared = append(shared, "no signed green gate row")
	}
	out := closeCheck{}
	for _, f := range subst {
		problems := append([]string{}, shared...)
		decided := false
		for _, r := range rows {
			if r["event"] == "autopilot.decision" && r["run"] == run && r["scope"] == autopilot.SubstituteScope && r["acted"] == true &&
				r["block"] == id && r["file"] == f.File && r["content_hash"] == f.Hash && gatecheck.Verified(r, key) {
				decided = true
			}
		}
		if !decided {
			problems = append(problems, "no signed autopilot.decision row for the substitute")
		}
		if apdgSubstReverted(rows, run, id, f.File, f.Hash, key) { // B635
			problems = append(problems, "the owner reverted the substitute (autopilot revert); a normal review: code-forge review-file "+f.File+" --block "+id+" --run "+run+" --fresh")
		}
		if len(problems) == 0 {
			continue
		}
		out.Blockers = append(out.Blockers, gatecheck.Blocker{Code: substCloseCode, File: f.File,
			Detail: f.File + " is approved only by a substitute review; it counts only under an active autopilot grant with scope " + autopilot.SubstituteScope +
				" and green gates: " + strings.Join(problems, "; ") + "; the owner grants the scope (the fix below), or a normal review of the changed file replaces it"})
	}
	if len(out.Blockers) > 0 {
		out.Reason = gatecheck.Reason(out.Blockers)
	}
	return out
}
