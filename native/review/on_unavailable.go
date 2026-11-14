package review

import (
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B629 (issue #37 C2): review.on_unavailable, an owner-configured chain of
// routes a session walks, in order, when it is still unavailable (timeout,
// a provider down, a spawn that failed) after the built-in recovery. Each
// step waits review.on_unavailable_backoff_s and writes one
// review.unavailable_fallback row; the first route with a valid review ends
// the walk, and a used-up chain leaves the session unavailable as before.

// onUnavailableDefaultBackoff is review.on_unavailable_backoff_s's default.
var onUnavailableDefaultBackoff = []float64{0, 30, 120}

// OnUnavailableProvider is a review.on_unavailable route's provider: its
// own, else the level's, else the top-level provider.
func OnUnavailableProvider(cfg map[string]any, route map[string]any, level string) any {
	if p := route["provider"]; p != nil {
		return p
	}
	if l, _ := section(cfg, "levels")[level].(map[string]any); l != nil && l["provider"] != nil {
		return l["provider"]
	}
	return cfg["provider"]
}

// OnUnavailableRoutes is cfg's review.on_unavailable entries for level, in
// order, each provider defaulted (OnUnavailableProvider); entries that are
// not objects or have no model are left out (the schema refuses them
// first).
func OnUnavailableRoutes(cfg map[string]any, level string) []map[string]any {
	var out []map[string]any
	list, _ := section(cfg, "review")["on_unavailable"].([]any)
	for _, raw := range list {
		m, _ := raw.(map[string]any)
		if m == nil {
			continue
		}
		if model, _ := m["model"].(string); model == "" {
			continue
		}
		l, _ := m["level"].(string)
		if l == "" {
			l = "L2"
		}
		if l != level {
			continue
		}
		e := map[string]any{"provider": OnUnavailableProvider(cfg, m, level), "model": m["model"]}
		if v, ok := m["effort"]; ok {
			e["effort"] = v
		}
		out = append(out, e)
	}
	return out
}

func onUnavailableRoutesOf(cfg map[string]any, level string) []levelEntry {
	var out []levelEntry
	for _, m := range OnUnavailableRoutes(cfg, level) {
		e := levelEntry{raw: m}
		e.provider, _ = m["provider"].(string)
		e.model, _ = m["model"].(string)
		e.effort, e.hasEffort = m["effort"]
		out = append(out, e)
	}
	return out
}

// onUnavailableBackoff is the wait before the nth step that runs (0-based):
// entry n, the last entry repeated; an empty list never waits.
func onUnavailableBackoff(cfg map[string]any, n int) time.Duration {
	waits := onUnavailableDefaultBackoff
	if raw, ok := section(cfg, "review")["on_unavailable_backoff_s"].([]any); ok {
		waits = nil
		for _, v := range raw {
			if f, ok := v.(float64); ok && f >= 0 {
				waits = append(waits, min(f, 3600))
			}
		}
	}
	if len(waits) == 0 {
		return 0
	}
	return time.Duration(waits[min(n, len(waits)-1)] * float64(time.Second))
}

// onUnavailableTriggers: the verdict reasons that mean the reviewer was not
// there (a timeout; a failed, refused or unavailable session), not a broken
// answer.
func onUnavailableTriggers(reason string) bool {
	return reason == "timeout" || reason == "exit"
}

type unavailableWalk struct {
	res                 ledger.Object
	verdict             Verdict
	to                  ledger.Object // the route that answered; nil when none did
	tokensIn, tokensOut any
}

// walkUnavailable walks the chain after res failed with verdict; nil when
// the chain does not apply (no route for the level, a consensus or recheck
// session, a broken answer rather than a missing one).
func (r *oneRun) walkUnavailable(res ledger.Object, verdict Verdict) *unavailableWalk {
	if verdict.OK || !onUnavailableTriggers(verdict.Reason) || !r.schemaFallback || r.recheck {
		return nil
	}
	spec, cfg := r.spec, r.in.Cfg
	routes := onUnavailableRoutesOf(cfg, spec.level)
	if len(routes) == 0 {
		return nil
	}
	fromProvider, fromModel := get(res, "provider"), get(res, "model")
	if fromProvider == nil || fromModel == nil {
		if lv, ok := resolveLevelOf(r.baseCfg(), spec.level); ok {
			if fromProvider == nil {
				fromProvider = lv.provider
			}
			if fromModel == nil {
				fromModel = lv.model
			}
		}
	}
	var ticket any
	if r.in.Ticket != "" {
		ticket = r.in.Ticket
	}
	sleep := r.deps.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	out := &unavailableWalk{res: res, verdict: verdict}
	reason := verdict.Reason
	ran := 0
	for i, e := range routes {
		row := ledger.Object{{Key: "event", Value: "review.unavailable_fallback"}, {Key: "ticket", Value: ticket}, {Key: "file", Value: r.in.File},
			{Key: "lens", Value: spec.lens}, {Key: "role", Value: spec.role}, {Key: "level", Value: spec.level},
			{Key: "from_provider", Value: orDefault(fromProvider, nil)}, {Key: "from_model", Value: orDefault(fromModel, nil)},
			{Key: "to_provider", Value: e.provider}, {Key: "to_model", Value: e.model}, {Key: "reason", Value: reason},
			{Key: "attempt", Value: float64(i + 1)}, {Key: "waited_ms", Value: 0.0}, {Key: "skipped", Value: nil}, {Key: "packet_hash", Value: r.hash}}
		pinned := pinLevel(r.baseCfg(), spec.level, e, []any{})
		if reviewClosedBook(e.provider, spec.role, openBookAllowed(cfg)) {
			row.Set("skipped", "closed-book")
			r.deps.note(row)
			continue
		}
		if r.deps.CheckSpawn != nil {
			if err := r.deps.CheckSpawn(pinned, spec.level, spec.role); err != nil {
				row.Set("skipped", "not-runnable")
				r.deps.note(row)
				continue
			}
		}
		wait := onUnavailableBackoff(cfg, ran)
		if wait > 0 {
			sleep(wait)
		}
		ran++
		row.Set("waited_ms", float64(wait.Milliseconds()))
		r.deps.note(row)
		r.attempts++
		got, _ := r.spawn(pinned)
		v := r.validate(got)
		out.tokensIn = addTokens(out.tokensIn, get(usageOf(got), "tokens_in"))
		out.tokensOut = addTokens(out.tokensOut, get(usageOf(got), "tokens_out"))
		out.res, out.verdict = got, v
		if v.OK {
			out.to = ledger.Object{{Key: "level", Value: "review.on_unavailable"}, {Key: "provider", Value: e.provider}, {Key: "model", Value: e.model}}
			return out
		}
		if !onUnavailableTriggers(v.Reason) {
			if v.Reason == "schema" {
				r.deps.note(schemaInvalidRow(spec, got, r.hash, true))
			}
			return out
		}
		fromProvider, fromModel, reason = e.provider, e.model, v.Reason
	}
	return out
}
