package main

import (
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B922: no waiting on a person for a speed-only choice. `autopilot ask
// --speed` under an active grant that lets the scope through takes the
// option named by --fastest at once (B983: never the option order; each
// option keeps every gate), runs no delegate session and never goes to
// the owner. One signed autopilot.decision row (speed: true) records it, and
// the binnacle tells the owner after. A refused grant still goes to the
// owner, as for any ask; an ask without --speed is unchanged.

const apdSpeedReason = "speed choice (B922): the fastest option that keeps every gate, taken at once; told after in the binnacle"

// apdSpeedRun decides a speed ask: the --fastest option, acted, one signed row.
func apdSpeedRun(o *runOut, home, runID string, flags autopilotFlags, scope string, grant ledger.Object) int {
	options := []any{}
	for _, option := range autopilotItems(flags.values["options"]) {
		option = apdScrub(option)
		if !apdHasAny(options, option) {
			options = append(options, option)
		}
	}
	// B983: the option the asker named with --fastest (checked to be one of
	// the options), never the first one listed
	decision := apdScrub(autopilotJSTrim(flags.values["fastest"]))
	if !apdHasAny(options, decision) {
		return autopilotReport(o, "ask", autopilotUsageFail("--fastest must be one of --options (B983)"))
	}
	decisionID, err := autopilotDecisionID()
	if err != nil {
		return autopilotReport(o, "ask", err)
	}
	var subject any
	sub := ledger.Object{}
	for _, name := range []string{"block", "file", "finding"} {
		if value, ok := flags.values[name]; ok {
			if name == "file" {
				value = apdNormalize(value)
			}
			sub = append(sub, ledger.Field{Key: name, Value: value})
		}
	}
	if len(sub) > 0 {
		subject = sub
	}
	question := apdCutBytes(apdScrub(autopilotJSTrim(flags.values["question"])), apdQuestionMaxBytes)
	row := ledger.Object{
		{Key: "event", Value: "autopilot.decision"}, {Key: "decision_id", Value: decisionID}, {Key: "grant_id", Value: grant.Get("grant_id")},
		{Key: "scope", Value: scope}, {Key: "subject", Value: subject}, {Key: "question", Value: question},
		{Key: "options", Value: options}, {Key: "decision", Value: decision}, {Key: "within_scope", Value: true},
		{Key: "confidence", Value: nil}, {Key: "reason", Value: apdSpeedReason}, {Key: "escalate", Value: false},
		{Key: "acted", Value: true}, {Key: "to_owner", Value: false}, {Key: "owner_reason", Value: nil},
		{Key: "failure", Value: nil}, {Key: "level", Value: "agent"}, {Key: "provider", Value: nil},
		{Key: "model", Value: nil}, {Key: "speed", Value: true}, {Key: "ts", Value: autopilotStamp(autopilotNow())},
	}
	if _, err := autopilotWriteSigned(home, runID, row); err != nil {
		return autopilotReport(o, "ask", err)
	}
	// B49a: the owner's files follow the decision; best effort
	_, _, _ = bnRefresh(home, runID, autopilotNow().Truncate(time.Millisecond))
	if flags.bools["json"] {
		data, err := ledger.Stringify(ledger.Object{
			{Key: "run", Value: runID}, {Key: "scope", Value: flags.values["scope"]}, {Key: "grant_id", Value: grant.Get("grant_id")},
			{Key: "decision_id", Value: decisionID}, {Key: "answered", Value: true}, {Key: "acted", Value: true},
			{Key: "to_owner", Value: false}, {Key: "reason", Value: nil}, {Key: "decision", Value: decision},
			{Key: "confidence", Value: nil}, {Key: "within_scope", Value: true}, {Key: "escalate", Value: false},
			{Key: "answer_reason", Value: apdSpeedReason}, {Key: "speed", Value: true},
		}, false)
		if err != nil {
			return autopilotReport(o, "ask", err)
		}
		o.out(string(data) + "\n")
		return 0
	}
	o.out("autopilot ask run " + runID + ": acted · decision " + decisionID + " (speed choice, B922)\n" +
		"  decision  " + decision + " · the fastest option that keeps every gate, taken at once; the binnacle tells the owner\n")
	return 0
}

func apdHasAny(list []any, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}
