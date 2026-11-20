package main

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/autopilot"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
	"github.com/ricardov03/code-forge/native/stepbudget"
)

// B950: a speed budget per step (F018 part 2). Land and gate record their
// wall time themselves; the coder session and the CI wait report theirs:
//
//	code-forge autopilot step --run <id> --step <land|gate|coder|ci-wait> --ms <n> [--block <id>] [--json]
//
// The budget is the median of the project's earlier samples plus
// autopilot.step_budgets.margin (default 0.5), once min_samples (default 5)
// samples exist, or a fixed number of minutes under
// autopilot.step_budgets.<land|gate|coder|ci_wait>. A step over its budget
// prints a WARN line, writes a signed step.over_budget row (the binnacle
// shows it) and, while an autopilot grant is active, records an
// `autopilot found` finding (source speed), deduped per step.

// stepNow is the clock land reads its wall time from (a test seam).
var stepNow = time.Now

// stepBudgetConfig reads autopilot.step_budgets from the run's config snapshot.
func stepBudgetConfig(record ledger.Object) stepbudget.Config {
	cfg := stepbudget.DefaultConfig()
	snap, has, valid := sweepConfigInForce(record)
	if !has || !valid {
		return cfg
	}
	section, _ := snap.Get("autopilot").(ledger.Object)
	budgets, _ := section.Get("step_budgets").(ledger.Object)
	if v, ok := budgets.Get("margin").(float64); ok && v >= 0 && v <= 10 {
		cfg.Margin = v
	}
	if v, ok := budgets.Get("min_samples").(float64); ok && v >= 1 && v <= 50 && v == math.Trunc(v) {
		cfg.MinSamples = int(v)
	}
	for _, step := range stepbudget.Steps {
		if v, ok := budgets.Get(strings.ReplaceAll(step, "-", "_")).(float64); ok && v > 0 {
			cfg.FixedMs[step] = int64(math.Round(v * 60_000))
		}
	}
	return cfg
}

// stepGrantActive reports an autopilot grant in force on an active run: its
// state at now, or its recorded status when Go cannot read the grant's shape.
func stepGrantActive(record ledger.Object, now time.Time) bool {
	grant, ok := record.Get("autopilot").(ledger.Object)
	if !ok || record.Get("status") != "active" {
		return false
	}
	if st, err := autopilot.GrantState(grant, now); err == nil {
		return st == "active"
	}
	return grant.Get("status") == "active"
}

// apfAuto records one finding the run found by itself (B821, B950): only
// while an autopilot grant is active; the outcome goes to w. It never fails
// the caller: an error is a WARN line.
func apfAuto(w io.Writer, home, runID string, in apfInput) {
	record, err := state.ReadRun(home, runID)
	if err != nil || !stepGrantActive(record, autopilotNow()) {
		return
	}
	results, err := apfRecord(home, runID, []apfInput{in}, autopilotNow().Truncate(time.Millisecond))
	if err != nil {
		fmt.Fprintf(w, "WARN autopilot found (%s): %s\n", in.source, blockIOMessage(err))
		return
	}
	for _, r := range results {
		fmt.Fprint(w, "autopilot "+apfText(runID, r))
	}
}

// stepBudgetObserve records a finished step and reports it when it ran over
// its budget. Errors are WARN lines: a budget never fails the step.
func stepBudgetObserve(w io.Writer, home, runID, block, step string, ms int64) (stepbudget.Verdict, error) {
	record, err := state.ReadRun(home, runID)
	if err != nil {
		return stepbudget.Verdict{}, err
	}
	workspace, _ := record.Get("workspace").(string)
	if workspace == "" {
		workspace = "run:" + runID
	}
	v, err := stepbudget.Observe(home, workspace, step, ms, stepBudgetConfig(record))
	if err != nil {
		return v, err
	}
	where := ""
	if block != "" {
		where = " (block " + block + ")"
	}
	stepbudget.Report(v, block, stepbudget.Sink{
		Warn: func(line string) { fmt.Fprint(w, line) },
		Row: func(v stepbudget.Verdict) {
			row := ledger.Object{
				{Key: "event", Value: "step.over_budget"},
				{Key: "block", Value: nil},
				{Key: "step", Value: step},
				{Key: "ms", Value: float64(v.Ms)},
				{Key: "budget_ms", Value: float64(v.BudgetMs)},
				{Key: "median_ms", Value: float64(v.MedianMs)},
				{Key: "samples", Value: float64(v.Samples)},
				{Key: "source", Value: v.Source},
				{Key: "ts", Value: autopilotStamp(autopilotNow())},
			}
			if block != "" {
				row.Set("block", block)
			}
			if _, err := autopilotWriteSigned(home, runID, row); err != nil {
				fmt.Fprintf(w, "WARN step budget: the step.over_budget row was not written: %s\n", blockIOMessage(err))
			}
		},
		Found: func(v stepbudget.Verdict) {
			apfAuto(w, home, runID, apfInput{
				source:      "speed",
				title:       "the " + step + " step runs over its speed budget",
				evidence:    step + " took " + stepbudget.Duration(v.Ms) + "; budget " + stepbudget.Duration(v.BudgetMs) + " (" + v.Basis() + ")" + where + ". Find the slow part and make it fast, or set autopilot.step_budgets." + strings.ReplaceAll(step, "-", "_") + " when this time is right.",
				from:        block,
				fingerprint: "step-budget:" + step,
				labels:      []string{"speed"},
			})
		},
	})
	return v, nil
}

// stepSub: `autopilot step`.
func stepSub(sub string) bool { return sub == "step" }

func stepRun(o *runOut, home string, args []string) int {
	flags, err := autopilotParse("step", args)
	if err != nil {
		return autopilotReport(o, "step", err)
	}
	step := flags.values["step"]
	if !stepbudget.Known(step) {
		return autopilotReport(o, "step", autopilotUsageFail("--step must be one of "+strings.Join(stepbudget.Steps, ", ")+", not "+autopilotQuote(step)))
	}
	ms, perr := strconv.ParseInt(flags.values["ms"], 10, 64)
	if perr != nil || ms < 0 {
		return autopilotReport(o, "step", autopilotUsageFail("--ms needs the step's wall time in whole milliseconds (0 or more)"))
	}
	var buf strings.Builder
	v, err := stepBudgetObserve(&buf, home, flags.values["run"], flags.values["block"], step, ms)
	if err != nil {
		return autopilotReport(o, "step", err)
	}
	if buf.Len() > 0 {
		o.err(buf.String())
	}
	if flags.bools["json"] {
		data, _ := ledger.Stringify(ledger.Object{
			{Key: "step", Value: v.Step}, {Key: "ms", Value: float64(v.Ms)}, {Key: "budget_ms", Value: float64(v.BudgetMs)},
			{Key: "median_ms", Value: float64(v.MedianMs)}, {Key: "samples", Value: float64(v.Samples)}, {Key: "source", Value: v.Source}, {Key: "over", Value: v.Over},
		}, false)
		o.out(string(data) + "\n")
		return 0
	}
	switch {
	case v.Source == "":
		o.out(step + " " + stepbudget.Duration(ms) + " recorded; no budget yet (" + strconv.Itoa(v.Samples) + " earlier runs)\n")
	case v.Over:
		o.out(step + " " + stepbudget.Duration(ms) + " recorded; over budget " + stepbudget.Duration(v.BudgetMs) + "\n")
	default:
		o.out(step + " " + stepbudget.Duration(ms) + " recorded; within budget " + stepbudget.Duration(v.BudgetMs) + "\n")
	}
	return 0
}

// gatesErrWriter writes to the gate command's stderr.
type gatesErrWriter struct{}

func (gatesErrWriter) Write(p []byte) (int, error) { gatesErrf("%s", p); return len(p), nil }

// gatesAfterRun runs after the gate rows of a run's block are written: the
// gate step's speed budget (B950).
func gatesAfterRun(home, runID, block string, results []any, durationMs int64) {
	if durationMs > 0 {
		if _, err := stepBudgetObserve(gatesErrWriter{}, home, runID, block, "gate", durationMs); err != nil {
			gatesErrf("WARN step budget: %s\n", blockIOMessage(err))
		}
	}
}

// landAfter: a land that ended well records the land step's wall time (B950).
func landAfter(w io.Writer, home, runID, block string, start time.Time, ok bool) {
	if !ok || runID == "" {
		return
	}
	if _, err := stepBudgetObserve(w, home, runID, block, "land", stepbudget.Elapsed(start, stepNow)); err != nil {
		fmt.Fprintf(w, "WARN step budget: %s\n", blockIOMessage(err))
	}
}
