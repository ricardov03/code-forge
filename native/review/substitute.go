package review

import (
	"sort"
	"strings"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B630 (issue #37 C3/C4): a substitute review. When a file's review stays
// unavailable after the built-in recovery and the review.on_unavailable
// chain, an autopilot grant with scope review:substitute lets the worker run
// one more review: a closed-book reviewer session with the review schema, on
// a provider none of the failed sessions used. Its result carries
// source: substitute, and so do the review.round and review.approved rows it
// leads to.

// SubstituteSource is the source value a substitute review carries.
const SubstituteSource = "substitute"

// SubstituteRoute is the route a substitute review runs on: its provider and
// model, and the config that pins it as the only L2 reviewer.
type SubstituteRoute struct {
	Provider, Model string
	Cfg             map[string]any
}

// SubstituteApplies: res is a review left unavailable because the reviewer
// was not there (a timeout or a failed session), not a broken answer.
func SubstituteApplies(res ledger.Object) bool {
	if res == nil || res.Get("status") != "unavailable" {
		return false
	}
	reason, _ := res.Get("reason").(string)
	return onUnavailableTriggers(reason)
}

// substProviderKey is a provider as closed-book.mjs compares it.
func substProviderKey(p string) string {
	return strings.ToLower(strings.TrimFunc(p, jsSpace))
}

// substCandidates is every route cfg names, in order: the L2 ladder, then
// L3, L1 and the other levels (sorted), each with its fallback list, then the
// review.on_unavailable routes.
func substCandidates(cfg map[string]any) []levelEntry {
	levels := []string{"L2", "L3", "L1"}
	rest := []string{}
	for name := range section(cfg, "levels") {
		if name != "L1" && name != "L2" && name != "L3" {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	levels = append(levels, rest...)
	out := []levelEntry{}
	for _, name := range levels {
		if lv, ok := resolveLevelOf(cfg, name); ok {
			out = append(out, lv.entries()...)
		}
	}
	list, _ := section(cfg, "review")["on_unavailable"].([]any)
	for _, raw := range list {
		m, _ := raw.(map[string]any)
		model, _ := m["model"].(string)
		if model == "" {
			continue
		}
		level, _ := m["level"].(string)
		if level == "" {
			level = "L2"
		}
		e := levelEntry{model: model, raw: m}
		e.provider, _ = OnUnavailableProvider(cfg, m, level).(string)
		e.effort, e.hasEffort = m["effort"]
		out = append(out, e)
	}
	return out
}

// substConfig pins e as cfg's only L2 reviewer: no fallback, no
// on_unavailable chain, no routes or second levels, one reviewer session.
func substConfig(cfg map[string]any, e levelEntry) map[string]any {
	next := pinLevel(cfg, "L2", e, []any{})
	rv := map[string]any{}
	for k, v := range section(cfg, "review") {
		rv[k] = v
	}
	for _, k := range []string{"on_unavailable", "routes", "second_levels"} {
		delete(rv, k)
	}
	rv["multimodel"] = false
	rv["single_reviewer_max_risk"] = 3.0
	next["review"] = rv
	return next
}

// PickSubstitute is the first route of cfg whose provider is none of failed,
// that a closed-book reviewer may use, and that check (the session layer's
// spawn check; nil = every route) can run. false when there is none.
func PickSubstitute(cfg map[string]any, failed []string, check func(cfg map[string]any, level, role string) error) (SubstituteRoute, bool) {
	used := map[string]bool{}
	for _, p := range failed {
		used[substProviderKey(p)] = true
	}
	for _, e := range substCandidates(cfg) {
		if e.provider == "" || e.model == "" || used[substProviderKey(e.provider)] {
			continue
		}
		if reviewClosedBook(e.provider, "reviewer", openBookAllowed(cfg)) {
			continue
		}
		pinned := substConfig(cfg, e)
		if check != nil && check(pinned, "L2", "reviewer") != nil {
			continue
		}
		return SubstituteRoute{Provider: e.provider, Model: e.model, Cfg: pinned}, true
	}
	return SubstituteRoute{}, false
}

// Substitute runs the file's review once on route (no timeout fallback, no
// other model) and marks its result source: substitute.
func Substitute(in Input, deps Deps, route SubstituteRoute) (ledger.Object, error) {
	in.Cfg = route.Cfg
	deps.TimeoutFallback = func(TimeoutFallbackInput) (*Fallback, error) { return nil, nil }
	res, err := File(in, deps)
	if err != nil || res == nil {
		return res, err
	}
	res.Set("source", SubstituteSource)
	res.Set("substitute", ledger.Object{{Key: "provider", Value: route.Provider}, {Key: "model", Value: route.Model}})
	return res, nil
}
