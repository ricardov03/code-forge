package review

// B495: the block's review budget (src/review/budget.mjs planBudget,
// budgetRows, planAndRecord, tierOf) and the rules risk
// (src/review/engine.mjs rulesRisk over proof/tiers.mjs tierFor and
// decide/fallback-rules.mjs fallbackRisk).

import (
	"regexp"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
)

var (
	migrationPath = regexp.MustCompile(`(?i)migration`)
	policyPath    = regexp.MustCompile(`(?i)polic(y|ies)|middleware`)
)

// RulesRisk is engine.mjs rulesRisk: a proof.tiers.high.paths match ⇒ 3, a
// migration/policy/middleware path ⇒ 2, more than 200 changed lines ⇒ 1.
func RulesRisk(file string, plusCount, minusCount int, cfg map[string]any) float64 {
	highPaths, _ := section(cfg, "proof", "tiers", "high")["paths"].([]any)
	if len(highPaths) > 0 && owned.OwnsFile(highPaths, nil, file) {
		return 3
	}
	if migrationPath.MatchString(file) || policyPath.MatchString(file) {
		return 2
	}
	if plusCount+minusCount > 200 {
		return 1
	}
	return 0
}

// TierOf is budget.mjs tierOf (no high path, not security sensitive).
func TierOf(risk float64) string {
	if risk >= 2 {
		return "high"
	}
	if risk >= 1 {
		return "standard"
	}
	return "light"
}

var perFile = map[string]int{"quick": 7000, "full": 13000, "dual": 34000, "dual-block-judge": 26000,
	"full+judge": 13000 + 8000, "full+block-judge": 13000, "consensus": 30000, "recheck": 5000}

const judgeCost = 34000 - 2*13000

// BudgetFile is one file of the forecast (no token count, no recheck: the
// worker's block budget sends neither).
type BudgetFile struct{ File, Tier string }

type planEntry struct {
	file, tier, unconstrained, chosen string
	step, forecast                    int
}

func forecastTotal(plan []*planEntry, judgePerBlock bool) int {
	total, blockJudge := 0, false
	for _, e := range plan {
		e.forecast = perFile[e.chosen]
		total += e.forecast
		if e.chosen == "dual-block-judge" || e.chosen == "full+block-judge" {
			blockJudge = true
		}
	}
	if judgePerBlock && blockJudge {
		total += judgeCost
	}
	return total
}

// BudgetRows is planAndRecord with blockOnly: the review.budget row and,
// when over, review.over_budget (the review.plan rows are not written).
func BudgetRows(files []BudgetFile, cfg map[string]any, block string) []ledger.Object {
	budget := 150000
	if v, ok := intOf(section(cfg, "review")["block_budget_tokens"]); ok && v > 0 {
		budget = v
	}
	consensus := section(cfg, "review")["multimodel"] == true
	plan := []*planEntry{}
	for _, f := range files {
		depth := "quick"
		switch {
		case consensus:
			depth = "consensus"
		case f.Tier == "high":
			depth = "dual"
		case f.Tier == "standard":
			depth = "full"
		}
		plan = append(plan, &planEntry{file: f.File, tier: f.Tier, unconstrained: depth, chosen: depth})
	}
	judgePerBlock := false
	unconstrained := forecastTotal(plan, false)
	chosen, step := unconstrained, 0
	steps := []func(){
		func() {}, // batching needs token counts the block budget does not have
		func() {
			judgePerBlock = true
			for _, e := range plan {
				if e.chosen == "dual" {
					e.chosen, e.step = "dual-block-judge", 2
				}
			}
		},
		func() {
			for _, e := range plan {
				if e.chosen == "full" && e.tier == "standard" {
					e.chosen, e.step = "quick", 3
				}
			}
		},
		func() {
			for _, e := range plan {
				if e.chosen == "dual-block-judge" {
					e.chosen, e.step = "full+block-judge", 4
				} else if e.chosen == "dual" {
					e.chosen, e.step = "full+judge", 4
				}
			}
		},
	}
	for chosen > budget && step < len(steps) {
		steps[step]()
		step++
		chosen = forecastTotal(plan, judgePerBlock)
	}
	over := chosen > budget
	rows := []ledger.Object{{{Key: "event", Value: "review.budget"}, {Key: "block", Value: block}, {Key: "budget", Value: float64(budget)},
		{Key: "forecast_unconstrained", Value: float64(unconstrained)}, {Key: "forecast_chosen", Value: float64(chosen)}, {Key: "actual", Value: nil},
		{Key: "raised", Value: 0.0}, {Key: "over_budget", Value: over}, {Key: "degrade_step", Value: float64(step)}}}
	if over {
		rows = append(rows, ledger.Object{{Key: "event", Value: "review.over_budget"}, {Key: "block", Value: block}, {Key: "budget", Value: float64(budget)}, {Key: "forecast_chosen", Value: float64(chosen)}})
	}
	return rows
}
