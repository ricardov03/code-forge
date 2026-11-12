package worker

import (
	"reflect"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/review"
	"github.com/ricardov03/code-forge/native/sign"
)

// B514: the worker's seams for review.File's broken-answer steps: Node's
// ledgerSchemaHistory (schema-fallback.mjs) and timeoutFallbackFor
// (route.mjs) for the configs Go reviews (no levels.<L> fallback, no
// review.second_levels, no review.routes: review.File refuses those first).

// schemaHistory is ledgerSchemaHistory over the run's ledger: signed
// review.schema_invalid / review.session_timeout rows of the level's own
// ladder for this packet count as failures, a signed review.schema_fallback
// row is fellBack.
func schemaHistory(readRows func() ([]any, error), key []byte, runID, file, contentHash string) func(hash, level, role string) (int, bool, error) {
	return func(hash, level, role string) (int, bool, error) {
		rows, err := readRows()
		if err != nil {
			return 0, false, err
		}
		failures, fellBack := 0, false
		for _, raw := range rows {
			r, ok := raw.(map[string]any)
			if !ok || r["run"] != runID || r["file"] != file || r["content_hash"] != contentHash || r["packet_hash"] != hash || r["level"] != level || r["role"] != role {
				continue
			}
			ev := r["event"]
			if ev != "review.schema_invalid" && ev != "review.session_timeout" && ev != "review.schema_fallback" {
				continue
			}
			if res, err := sign.VerifyRow(r, key); err != nil || !res.OK {
				continue
			}
			if ev == "review.schema_fallback" {
				fellBack = true
			} else if r["second_level"] != true {
				failures++
			}
		}
		return failures, fellBack, nil
	}
}

// closedBookRefused is closed-book.mjs closedBookRefused.
func closedBookRefused(provider any, role string, openBook bool) bool {
	norm := func(v any) string {
		s, _ := v.(string)
		return strings.ToLower(strings.Trim(s, jsSpace))
	}
	r := norm(role)
	noTool := r == "reviewer" || r == "judge" || r == "s2" || r == "author" || r == "delegate"
	return norm(provider) == "openai" && noTool && (!openBook || r == "delegate")
}

type fallbackEntry struct {
	model, provider string
	effort          any
	hasEffort       bool
}

// timeoutFallbackFor is route.mjs timeoutFallbackFor for a config with no
// review alternatives at the level: every candidate is a configured model the
// ledger confirms in this role. errRouteUnsure where Go does not model the
// input (Node's order of a key it cannot see, a value Node would throw on).
func timeoutFallbackFor(cfg map[string]any, in review.TimeoutFallbackInput, now time.Time) (*review.Fallback, error) {
	levels, _ := obj(cfg["levels"])
	if cfg["levels"] != nil && levels == nil {
		return nil, errRouteUnsure
	}
	primary, _ := obj(levels[in.Level])
	if fb, ok := primary["fallback"]; ok && fb != nil {
		if list, ok := fb.([]any); !ok || len(list) > 0 {
			return nil, errRouteUnsure
		}
	}
	rv := reviewOf(cfg)
	if rv["second_levels"] != nil {
		return nil, errRouteUnsure
	}
	if rules, err := routeRules(cfg); err != nil || len(rules) > 0 {
		return nil, errRouteUnsure
	}
	// configured: every levels entry and its ladder, provider defaulted to
	// the entry's own, then the top-level one
	configured := []map[string]any{}
	for _, raw := range levels {
		entry, ok := obj(raw)
		if !ok {
			return nil, errRouteUnsure
		}
		own, has := entry["provider"]
		if !has || own == nil {
			own = cfg["provider"]
		}
		withProvider := copyMap(entry)
		withProvider["provider"] = own
		configured = append(configured, withProvider)
		if fb, has := entry["fallback"]; has && fb != nil {
			list, ok := fb.([]any)
			if !ok {
				return nil, errRouteUnsure
			}
			for _, f := range list {
				fe, ok := obj(f)
				if !ok {
					return nil, errRouteUnsure
				}
				c := copyMap(fe)
				if p, has := fe["provider"]; !has || p == nil {
					c["provider"] = own
				}
				configured = append(configured, c)
			}
		}
	}
	// configured.find: Go's map has lost the config's key order, so two
	// entries for one model+provider must agree on what is carried (effort)
	find := func(model, provider string) (fallbackEntry, bool) {
		var out fallbackEntry
		found := false
		for _, e := range configured {
			if e["model"] != model || e["provider"] != provider {
				continue
			}
			effort, has := e["effort"]
			cand := fallbackEntry{model: model, provider: provider, effort: effort, hasEffort: has}
			if found && (cand.hasEffort != out.hasEffort || !reflect.DeepEqual(cand.effort, out.effort)) {
				return fallbackEntry{}, false
			}
			out, found = cand, true
		}
		return out, found
	}
	entries := []fallbackEntry{}
	unsure := false
	confirm := func(s map[string]any) {
		if s == nil || s["role"] != in.Role {
			return
		}
		model, mok := s["model"].(string)
		provider, pok := s["provider"].(string)
		if !mok || !pok || model == "" || provider == "" {
			return // a non-text match is filtered out of the candidates anyway
		}
		e, ok := find(model, provider)
		if !ok {
			for _, c := range configured {
				if c["model"] == model && c["provider"] == provider {
					unsure = true
				}
			}
			return
		}
		entries = append(entries, e)
	}
	for _, raw := range in.Rows {
		if raw == nil {
			return nil, errRouteUnsure // row.event on null throws in Node
		}
		row, ok := obj(raw)
		if !ok {
			continue
		}
		if row["event"] == "session" {
			confirm(row)
		}
		if row["event"] == "review.result" {
			if list, ok := row["sessions"].([]any); ok {
				for _, s := range list {
					m, _ := obj(s)
					confirm(m)
				}
			}
		}
	}
	if unsure {
		return nil, errRouteUnsure
	}
	openBook := rv["allow_open_book_codex"] == true
	fromModel, _ := in.FromModel.(string)
	_, fromIsText := in.FromModel.(string)
	candidates := []candidate{}
	effortOf := map[candidate]fallbackEntry{}
	for _, e := range entries {
		c := candidate{model: e.model, provider: e.provider}
		if (fromIsText && e.model == fromModel) || primary["model"] == e.model || closedBookRefused(e.provider, in.Role, openBook) {
			continue
		}
		if _, dup := effortOf[c]; dup {
			continue
		}
		effortOf[c] = e
		candidates = append(candidates, c)
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	if raw, has := rv["adaptive"]; has && raw == nil {
		return nil, errRouteUnsure // modelStats(rows, null) throws in Node
	}
	opts, err := adaptiveOf(cfg)
	if err != nil {
		return nil, err
	}
	stats, err := modelStats(in.Rows, opts.windowDays, now)
	if err != nil {
		return nil, err
	}
	choice, reason := pickReviewer(candidates, stats, opts, in.TicketID)
	e := effortOf[choice]
	entry := map[string]any{"model": e.model, "provider": e.provider, "fallback": []any{}}
	if e.hasEffort {
		entry["effort"] = e.effort
	}
	next := copyMap(cfg)
	nextLevels := copyMap(levels)
	nextLevels[in.Level] = entry
	next["levels"] = nextLevels
	return &review.Fallback{Cfg: next, Provider: e.provider, Model: e.model, Reason: reason}, nil
}
