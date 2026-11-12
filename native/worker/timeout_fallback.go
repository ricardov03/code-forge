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
// (route.mjs); B538 adds the level's explicit alternatives (its ladder,
// review.second_levels, review.routes).

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

// timeoutFallbackFor is route.mjs timeoutFallbackFor: the level's explicit
// review alternatives (its ladder, review.second_levels.<L>, the level's
// review.routes rules and their seconds), then every configured model the
// ledger confirms in this role. errRouteUnsure where Go does not model the
// input (Node's order of a key it cannot see, a value Node would throw on).
func timeoutFallbackFor(cfg map[string]any, in review.TimeoutFallbackInput, now time.Time) (*review.Fallback, error) {
	levels, _ := obj(cfg["levels"])
	if cfg["levels"] != nil && levels == nil {
		return nil, errRouteUnsure
	}
	primary, _ := obj(levels[in.Level])
	rv := reviewOf(cfg)
	rules, err := routeRules(cfg)
	if err != nil {
		return nil, errRouteUnsure // routeRules throws
	}
	secondLevels, _ := obj(rv["second_levels"])
	if rv["second_levels"] != nil && secondLevels == nil {
		return nil, errRouteUnsure
	}
	secondProvider := rv["second_provider"]
	if secondProvider == nil {
		secondProvider = cfg["provider"]
	}
	// add is timeoutFallbackFor's add: the entry with its provider defaulted,
	// then its own ladder with each provider defaulted to the entry's
	add := func(target *[]map[string]any, raw any, provider any) error {
		if raw == nil {
			return nil
		}
		entry, ok := obj(raw)
		if !ok {
			return errRouteUnsure
		}
		own, has := entry["provider"]
		if !has || own == nil {
			own = provider
		}
		withProvider := copyMap(entry)
		withProvider["provider"] = own
		*target = append(*target, withProvider)
		if fb, has := entry["fallback"]; has && fb != nil {
			list, ok := fb.([]any)
			if !ok {
				return errRouteUnsure
			}
			for _, f := range list {
				fe, ok := obj(f)
				if !ok {
					return errRouteUnsure
				}
				c := copyMap(fe)
				if p, has := fe["provider"]; !has || p == nil {
					c["provider"] = own
				}
				*target = append(*target, c)
			}
		}
		return nil
	}
	explicit := []map[string]any{}
	if fb, has := primary["fallback"]; has && fb != nil {
		list, ok := fb.([]any)
		if !ok {
			return nil, errRouteUnsure
		}
		pp := primary["provider"]
		if pp == nil {
			pp = cfg["provider"]
		}
		for _, f := range list {
			if err := add(&explicit, f, pp); err != nil {
				return nil, err
			}
		}
	}
	if err := add(&explicit, secondLevels[in.Level], secondProvider); err != nil {
		return nil, err
	}
	for _, rule := range rules {
		if ruleLevel(rule) != in.Level {
			continue
		}
		provider, err := providerOf(cfg, rule)
		if err != nil {
			return nil, errRouteUnsure // providerOf throws
		}
		if err := add(&explicit, entryOf(rule), provider); err != nil {
			return nil, err
		}
		if err := add(&explicit, rule["second"], secondProvider); err != nil {
			return nil, err
		}
	}
	// configured: every levels entry, second level and rule with its ladder
	configured := []map[string]any{}
	for _, raw := range levels {
		if err := add(&configured, raw, cfg["provider"]); err != nil {
			return nil, err
		}
	}
	for _, raw := range secondLevels {
		if err := add(&configured, raw, secondProvider); err != nil {
			return nil, err
		}
	}
	for _, rule := range rules {
		provider, err := providerOf(cfg, rule)
		if err != nil {
			return nil, errRouteUnsure
		}
		if err := add(&configured, entryOf(rule), provider); err != nil {
			return nil, err
		}
		if err := add(&configured, rule["second"], secondProvider); err != nil {
			return nil, err
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
	for _, e := range explicit {
		// the explicit alternatives are candidates as written (a non-text
		// model or provider is filtered out below)
		model, _ := e["model"].(string)
		provider, _ := e["provider"].(string)
		if model == "" || provider == "" {
			continue
		}
		effort, has := e["effort"]
		entries = append(entries, fallbackEntry{model: model, provider: provider, effort: effort, hasEffort: has})
	}
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
