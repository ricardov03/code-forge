package review

// B533: System 1 (Jev) in the fix loop's triage (src/review/triage.mjs
// askNoul, resolvedBand and triageFindings with a Jev call and no S2 rule:
// the worker passes none). The `defect` and `resolved` questions are
// src/decide/questions.mjs buildQuestionPayload; the bands are
// src/decide/thresholds.mjs bandFinding and bandResolved. A Jev failure
// falls back to the rules with one s1_fallback warning line; Jev writes no
// cost row. CheckJev refuses, before anything runs, the configs Go cannot
// render like Node.

import (
	"math"
	"regexp"
	"sort"
	"strconv"

	"github.com/ricardov03/code-forge/native/jev"
	"github.com/ricardov03/code-forge/native/ledger"
)

// JevAsk is triage.mjs JevAsk: one askJev call with the key bound. An error
// is a call that threw.
type JevAsk func(state, questions any) (jev.Result, error)

type noulQuestion struct{ instructions, yes, no string }

var noulQuestions = map[string]noulQuestion{
	"defect": {"Is `finding` a real defect in `file_diff_hunk` that must be fixed before merge (not a style preference)?",
		"real correctness, safety or rule-violation defect", "style, naming or preference only"},
	"resolved": {"Does `fix_hunk` resolve `finding` without introducing a new problem in the shown lines?", "yes", "no"},
}

// fallbackReasons is triage.mjs FALLBACK_REASONS.
var fallbackReasons = map[string]bool{"unauthorized": true, "invalid_request": true, "rate_limited": true, "unavailable": true,
	"network": true, "timeout": true, "unexpected_status": true, "invalid_response": true, "error": true, "invalid_answer": true}

// jsIndexKey is a JS array-index property name (ordered first, ascending).
var jsIndexKey = regexp.MustCompile(`^(0|[1-9]\d*)$`)

func questionDisabled(id string, cfg map[string]any) bool {
	list, ok := section(cfg, "system1")["disable"].([]any)
	if !ok {
		return false
	}
	for _, v := range list {
		if v == id {
			return true
		}
	}
	return false
}

// noulPayload is buildQuestionPayload([id], cfg) for a noul question; ok
// false for a criteria_extra Go cannot order like Node's object spread.
func noulPayload(id string, cfg map[string]any) (ledger.Object, bool) {
	q := noulQuestions[id]
	instructions := q.instructions
	criteria := ledger.Object{{Key: "true", Value: q.yes}, {Key: "false", Value: q.no}}
	extras, _ := section(cfg, "system1")["criteria_extra"].(map[string]any)
	if extra, ok := extras[id].(map[string]any); ok {
		if text, ok := extra["instructions"].(string); ok && text != "" {
			instructions = text
		}
		switch more := extra["criteria"].(type) {
		case nil, float64, bool:
		case map[string]any:
			added := []string{}
			for k, v := range more {
				switch v.(type) {
				case string, float64, bool, nil:
				default:
					return nil, false // a nested value: its key order is not kept
				}
				if _, has := lookup(criteria, k); has {
					criteria.Set(k, v)
				} else {
					added = append(added, k)
				}
			}
			named := 0
			for _, k := range added {
				if !jsIndexKey.MatchString(k) || len(k) > 15 {
					named++
				}
			}
			if named > 1 {
				return nil, false // the config map lost their order
			}
			for _, k := range added {
				criteria = append(criteria, ledger.Field{Key: k, Value: more[k]})
			}
			// JS puts array-index keys first, ascending
			sort.SliceStable(criteria, func(i, j int) bool {
				a, aIdx := indexOf(criteria[i].Key)
				b, bIdx := indexOf(criteria[j].Key)
				if aIdx && bIdx {
					return a < b
				}
				return aIdx && !bIdx
			})
		default:
			return nil, false // spreading a string or a list makes index keys
		}
	}
	return ledger.Object{{Key: id, Value: ledger.Object{{Key: "type", Value: "noul"}, {Key: "instructions", Value: instructions}, {Key: "criteria", Value: criteria}}}}, true
}

func indexOf(k string) (int, bool) {
	if !jsIndexKey.MatchString(k) || len(k) > 15 {
		return 0, false
	}
	n, err := strconv.Atoi(k)
	return n, err == nil
}

// findingThresholds is thresholds.mjs findingsThresholds; ok false for a
// value that is not a number (JS compares it with coercion).
func findingThresholds(cfg map[string]any) (fix, nit, resolved float64, ok bool) {
	raw := section(cfg, "thresholds", "findings")
	pick := func(key string, d float64) (float64, bool) {
		switch v := raw[key].(type) {
		case nil:
			return d, true
		case float64:
			return v, true
		}
		return d, false
	}
	fix, okFix := pick("fix", 0.9)
	nit, okNit := pick("nit", 0.4)
	resolved, okRes := pick("resolved", 0.9)
	return fix, nit, resolved, okFix && okNit && okRes
}

// CheckJev reports ErrUnsupported for a config whose Jev questions or
// thresholds Go does not render like Node. Called before anything runs.
func CheckJev(cfg map[string]any) error {
	for _, id := range []string{"defect", "resolved"} {
		if questionDisabled(id, cfg) {
			continue
		}
		if _, ok := noulPayload(id, cfg); !ok {
			return unsupported("system1.criteria_extra." + id + " in an order Go does not keep")
		}
	}
	if _, _, _, ok := findingThresholds(cfg); !ok {
		return unsupported("thresholds.findings values that are not numbers")
	}
	return nil
}

// askNoul is triage.mjs askNoul: the probability, or nil when Jev is not
// asked or gave no usable answer (one s1_fallback warning).
func askNoul(id string, state ledger.Object, deps LoopDeps) any {
	if deps.Jev == nil || questionDisabled(id, deps.Cfg) {
		return nil
	}
	questions, ok := noulPayload(id, deps.Cfg)
	if !ok {
		return nil // CheckJev refused this config before the round
	}
	res, err := deps.Jev(state, questions)
	if err != nil {
		fellBack(deps, "error")
		return nil
	}
	if res.OK {
		answers, _ := res.Answers.(map[string]any)
		answer, _ := answers[id].(map[string]any)
		if p, isNum := answer["noul"].(float64); isNum && !math.IsInf(p, 0) && !math.IsNaN(p) && p >= 0 && p <= 1 {
			return p
		}
		fellBack(deps, "invalid_answer")
		return nil
	}
	fellBack(deps, res.Kind)
	return nil
}

func fellBack(deps LoopDeps, reason string) {
	if !fallbackReasons[reason] {
		reason = "other"
	}
	if deps.Session.Warn != nil {
		deps.Session.Warn("s1_fallback", "System 1 (Jev) gave no answer ("+reason+"); fell back to the rules")
	}
}

// bandFinding is thresholds.mjs bandFinding.
func bandFinding(p float64, cfg map[string]any) string {
	fix, nit, _, _ := findingThresholds(cfg)
	if p >= fix {
		return "fix_now"
	}
	if p >= nit {
		return "judge"
	}
	return "nit"
}

// resolvedClosed is resolvedBand's band == closed.
func resolvedClosed(finding any, fixHunk string, deps LoopDeps) bool {
	p := askNoul("resolved", ledger.Object{{Key: "finding", Value: finding}, {Key: "fix_hunk", Value: fixHunk}}, deps)
	if p == nil {
		return false
	}
	_, _, resolved, _ := findingThresholds(deps.Cfg)
	return p.(float64) >= resolved
}

// triage is triageFindings with deps.Jev (nil: the §3.5 fallback) and no
// rule: the fix_now findings, writing one review.triage row per finding
// (best effort).
func triage(file string, findings []any, fromJudge bool, diffText string, deps LoopDeps) []any {
	fixNow := []any{}
	write := deps.WriteRow
	severityVerdict := func(f ledger.Object) string {
		if f.Get("severity") == "nit" {
			return "nit"
		}
		return "fix_now"
	}
	keepCritical := func(f ledger.Object, verdict string) string {
		if f.Get("severity") == "critical" {
			return "fix_now"
		}
		return verdict
	}
	row := func(f ledger.Object, source string, p, band any, verdict string) ledger.Object {
		r := ledger.Object{{Key: "event", Value: "review.triage"}, {Key: "file", Value: file}}
		r = present(r, "finding", renameID(f))
		r = present(r, "severity", f)
		return append(r, ledger.Field{Key: "source", Value: source}, ledger.Field{Key: "p", Value: p}, ledger.Field{Key: "band", Value: band}, ledger.Field{Key: "verdict", Value: verdict})
	}
	type midFinding struct {
		v any
		p any
	}
	mid := []midFinding{}
	for _, v := range findings {
		f := asObject(v)
		p := askNoul("defect", ledger.Object{{Key: "file_diff_hunk", Value: diffText}, {Key: "finding", Value: v}}, deps)
		if fromJudge {
			verdict := severityVerdict(f)
			if verdict == "fix_now" {
				fixNow = append(fixNow, v)
			}
			var band any
			if p != nil {
				band = bandFinding(p.(float64), deps.Cfg)
			}
			note(write, row(f, "shadow", p, band, verdict))
			continue
		}
		if p == nil {
			mid = append(mid, midFinding{v, nil})
			continue
		}
		band := bandFinding(p.(float64), deps.Cfg)
		if band == "judge" {
			mid = append(mid, midFinding{v, p})
			continue
		}
		verdict := keepCritical(f, band)
		if verdict == "fix_now" {
			fixNow = append(fixNow, v)
		}
		note(write, row(f, "jev", p, band, verdict))
	}
	for _, m := range mid {
		f := asObject(m.v)
		verdict := keepCritical(f, severityVerdict(f))
		if verdict == "fix_now" {
			fixNow = append(fixNow, m.v)
		}
		if m.p == nil {
			note(write, row(f, "s2-fallback", nil, nil, verdict))
		} else {
			note(write, row(f, "s2", m.p, "judge", verdict))
		}
	}
	return fixNow
}
