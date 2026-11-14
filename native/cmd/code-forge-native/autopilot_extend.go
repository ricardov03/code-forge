package main

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/autopilot"
	"github.com/ricardov03/code-forge/native/humanask"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B634 (issue #37 D): `autopilot start --until-done [--deadline <time>]
// [--blocks <id,…>]` grants run until every planned block of the run is
// closed (autopilot.RunDone), or to the deadline, whichever is first; and
// `autopilot extend --until|--deadline <time>` moves a live grant's end
// later with one signed autopilot.extend row. New behaviour, Go only.

const autopilotDoneText = "every planned block closed"

// autopilotEndText is the grant's end in words: "until <time>" for a clock
// grant, "until done (every planned block closed)" with " or <deadline>"
// for an until-done grant.
func autopilotEndText(until any, untilDone bool) string {
	if !untilDone {
		return "until " + autopilotText(until)
	}
	text := "until done (" + autopilotDoneText + ")"
	if until != nil {
		text += " or " + autopilotText(until)
	}
	return text
}

// autopilotGrantEnd is the text after "until" in the messages that name a
// grant's end (the active-grant refusal, the stopped and ended heads).
func autopilotGrantEnd(grant ledger.Object) string {
	return strings.TrimPrefix(autopilotEndText(grant.Get("until"), autopilot.UntilDone(grant)), "until ")
}

// autopilotExpiredMsg is expiredMessage, or the until-done end when the
// grant ended because its planned blocks were all closed.
func autopilotExpiredMsg(runID string, grant ledger.Object) string {
	if at, done := grant.Get("done_at").(string); done {
		return "run " + runID + "'s autopilot grant already ended at " + at + ": " + autopilotDoneText
	}
	return autopilot.ExpiredMessage(runID, autopilotText(grant.Get("until")))
}

// autopilotPlannedText is the status line of an until-done grant's planned
// blocks: how many are done and each block's state.
func autopilotPlannedText(record, grant ledger.Object) (string, []any, int) {
	planned := autopilot.Planned(record, grant)
	ids := []any{}
	done := 0
	parts := []string{}
	blocks, _ := record.Get("blocks").(ledger.Object)
	for _, id := range planned {
		ids = append(ids, id)
		status := "not opened"
		if entry, ok := blocks.Get(id).(ledger.Object); ok {
			status = "open"
			if s, ok := entry.Get("status").(string); ok {
				status = s
			}
		}
		if autopilot.BlockDone(record, id) {
			done++
		}
		parts = append(parts, id+" "+status)
	}
	if len(planned) == 0 {
		return "none yet (the run has no blocks; the grant does not end before one is closed)", ids, done
	}
	return strconv.Itoa(done) + " of " + strconv.Itoa(len(planned)) + " done (" + strings.Join(parts, ", ") + ")", ids, done
}

// autopilotExtendInput reads extend's flags: exactly one of --until and
// --deadline, an ISO-8601 time after now and at most 24 h ahead.
func autopilotExtendInput(flags autopilotFlags, now time.Time) (string, string, error) {
	untilText, until := flags.values["until"]
	deadlineText, deadline := flags.values["deadline"]
	switch {
	case until && deadline:
		return "", "", autopilotUsageFail("give --until or --deadline, not both")
	case !until && !deadline:
		return "", "", autopilotUsageFail("--until is required: the grant's new end, an ISO-8601 time with an offset, e.g. " + autopilotUntilExample + " (--deadline for an until-done grant)")
	case deadline:
		end, err := autopilotEndTime("--deadline", deadlineText, now)
		return "deadline", end, err
	}
	end, err := autopilotEndTime("--until", untilText, now)
	return "until", end, err
}

// autopilotExtendRefusal is the reason the grant's end cannot move to end
// ("" when it can): a live grant, the flag that fits its kind, a later time.
func autopilotExtendRefusal(runID string, record, grant ledger.Object, flag, end string, now time.Time) string {
	if grant == nil {
		return "run " + runID + " has no autopilot grant"
	}
	if record.Get("status") != "active" {
		return "run " + runID + " has ended"
	}
	stateName, _ := autopilot.GrantState(grant, now)
	switch {
	case stateName == "stopped":
		return "run " + runID + "'s autopilot grant was already stopped at " + autopilotField(grant, "stopped_at") + "; start a new one with code-forge autopilot start"
	case stateName == "expired":
		return autopilotExpiredMsg(runID, grant)
	}
	untilDone := autopilot.UntilDone(grant)
	current := grant.Get("until")
	switch {
	case untilDone && flag == "until" && current == nil:
		return "run " + runID + "'s autopilot grant runs until done with no deadline; give --deadline <time> to set one"
	case !untilDone && flag == "deadline":
		return "run " + runID + "'s autopilot grant is not an until-done grant; move its end with --until <time>"
	}
	if current != nil {
		from, _ := time.Parse("2006-01-02T15:04:05.000Z", autopilotText(current))
		to, _ := time.Parse("2006-01-02T15:04:05.000Z", end)
		if !to.After(from) {
			return "--" + flag + " " + end + " is not after the grant's current end " + autopilotText(current) + "; extend only moves the end later"
		}
	}
	return ""
}

func autopilotExtend(o *runOut, home, runID string, flags autopilotFlags, now time.Time) int {
	flag, end, err := autopilotExtendInput(flags, now) // refuse bad input before asking anyone
	if err != nil {
		return autopilotReport(o, "extend", err)
	}
	// the expiry check first: a lapsed grant gets its one expire row, then the refusal
	checked, err := autopilotCheckExpiry(home, runID, now)
	if err != nil {
		return autopilotReport(o, "extend", err)
	}
	record, err := state.ReadRun(home, runID)
	if err != nil {
		return autopilotReport(o, "extend", err)
	}
	if why := autopilotExtendRefusal(runID, record, checked.Grant, flag, end, now); why != "" {
		return autopilotReport(o, "extend", &runFail{why, false})
	}
	if !flags.bools["yes"] {
		if !autopilotIsTTY() || !autopilotPromptable() {
			o.err("autopilot extend: no terminal to confirm; the owner extends autopilot — pass --yes to extend it without a prompt\n")
			return 2
		}
		answer, err := autopilotConfirm("Extend autopilot for run " + runID + " to " + end + "?")
		if err != nil {
			o.err("autopilot extend: " + err.Error() + "\n")
			return 1
		}
		if answer != humanask.Confirmed {
			o.out("cancelled — the grant's end is unchanged\n")
			return 1
		}
		// the prompt may have waited: the time is checked again against now
		now = autopilotNow().Truncate(time.Millisecond)
		if flag, end, err = autopilotExtendInput(flags, now); err != nil {
			return autopilotReport(o, "extend", err)
		}
		if _, err := autopilotCheckExpiry(home, runID, now); err != nil {
			return autopilotReport(o, "extend", err)
		}
	}
	var extended ledger.Object
	var from any
	err = state.UpdateRun(home, runID, func(record *ledger.Object) error {
		grant, _ := record.Get("autopilot").(ledger.Object)
		if why := autopilotExtendRefusal(runID, *record, grant, flag, end, now); why != "" {
			return &runFail{why, false}
		}
		from = grant.Get("until")
		// the row first: an end moved without its signed row would act unrecorded
		if _, err := autopilotWriteSigned(home, runID, ledger.Object{
			{Key: "event", Value: "autopilot.extend"}, {Key: "grant_id", Value: grant.Get("grant_id")},
			{Key: "from", Value: from}, {Key: "until", Value: end}, {Key: "ts", Value: autopilotStamp(now)},
		}); err != nil {
			return err
		}
		grant.Set("until", end)
		record.Set("autopilot", grant)
		extended = grant
		return nil
	})
	if err != nil {
		return autopilotReport(o, "extend", err)
	}
	to, _ := time.Parse("2006-01-02T15:04:05.000Z", end)
	was := "no deadline"
	if from != nil {
		was = "was " + autopilotText(from)
	}
	o.out("autopilot run " + runID + ": grant " + autopilotText(extended.Get("grant_id")) + " now runs " +
		autopilotEndText(end, autopilot.UntilDone(extended)) + " (" + autopilotLeft(to.UnixMilli()-now.UnixMilli()) + "; " + was + ")\n")
	return 0
}

// autopilotTimeLeft is status's time_left_s: whole seconds to the grant's
// clock end while it is active, null for an until-done grant with no deadline.
func autopilotTimeLeft(grant ledger.Object, stateName string, now time.Time) any {
	if grant.Get("until") == nil {
		return nil
	}
	if stateName != "active" {
		return 0.0
	}
	until, _ := time.Parse("2006-01-02T15:04:05.000Z", autopilotText(grant.Get("until")))
	return math.Floor(float64(until.UnixMilli()-now.UnixMilli()) / 1000)
}
