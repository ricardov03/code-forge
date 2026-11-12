package jev

import (
	"regexp"
	"sort"
	"strconv"

	"github.com/ricardov03/code-forge/native/ledger"
)

// The `risk` question of questions.mjs QUESTIONS.
const riskInstructions = "How much damage could a subtle mistake here cause in production?"

var riskCriteria = []string{
	"cosmetic",
	"wrong behaviour, easy to notice and revert",
	"silent wrong data for some users",
	"silent wrong money or cross-tenant leak",
}

var integerKey = regexp.MustCompile(`^(0|[1-9]\d*)$`)

// RiskPayload is buildQuestionPayload(['risk'], cfg): {risk: {type,
// instructions, criteria}} with system1.criteria_extra.risk merged and the
// criteria sent as the list Jev takes. ok is false where Node throws (risk
// listed in system1.disable, merged keys that are not exactly 0..n) and for
// an extra Go does not read the same way (criteria that are not a mapping);
// the caller then leaves the call to Node.
func RiskPayload(cfg map[string]any) (ledger.Object, bool) {
	sys, _ := cfg["system1"].(map[string]any)
	if disabled, ok := sys["disable"].([]any); ok {
		for _, id := range disabled {
			if id == "risk" {
				return nil, false
			}
		}
	}
	instructions := riskInstructions
	criteria := map[string]any{}
	for i, text := range riskCriteria {
		criteria[strconv.Itoa(i)] = text
	}
	extras, _ := sys["criteria_extra"].(map[string]any)
	if extra, ok := extras["risk"].(map[string]any); ok {
		if text, ok := extra["instructions"].(string); ok && text != "" {
			instructions = text
		}
		switch more := extra["criteria"].(type) {
		case nil:
		case map[string]any:
			for k, v := range more {
				switch v.(type) {
				case string, float64, bool, nil:
				default:
					return nil, false // a nested value: its key order is not kept
				}
				criteria[k] = v
			}
		default:
			return nil, false // spreading a string or a list makes index keys
		}
	}
	keys := make([]string, 0, len(criteria))
	for k := range criteria {
		if !integerKey.MatchString(k) || len(k) > 15 {
			return nil, false
		}
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { a, _ := strconv.Atoi(keys[i]); b, _ := strconv.Atoi(keys[j]); return a < b })
	list := make([]any, len(keys))
	for i, k := range keys {
		if k != strconv.Itoa(i) {
			return nil, false // a gap or a start past 0
		}
		list[i] = criteria[k]
	}
	return ledger.Object{{Key: "risk", Value: ledger.Object{{Key: "type", Value: "score"}, {Key: "instructions", Value: instructions}, {Key: "criteria", Value: list}}}}, true
}
