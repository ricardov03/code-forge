package review

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B538: engine.mjs runOne with its broken-answer recovery, ported whole:
// a levels.<L> fallback ladder (session-retry.mjs spawnWithTimeoutRetry's
// ladder on timeout or capacity, spawnWithBrokenFallback on a schema miss),
// review.routes recovery (each ladder entry pinned, advanced on schema,
// timeout or exit) and review.second_levels (schema-fallback.mjs
// secondLevelFor, afterSchemaMiss, logSecondLevelMiss).

// levelEntry is one model of a level's ladder, its provider defaulted to the
// level's (entry.provider ?? resolved.provider).
type levelEntry struct {
	provider  string
	model     string
	effort    any
	hasEffort bool // entry.effort !== undefined
	raw       map[string]any
}

// spread is `{...m, provider}` of a fallback entry.
func (e levelEntry) spread() map[string]any {
	out := map[string]any{}
	for k, v := range e.raw {
		out[k] = v
	}
	out["provider"] = e.provider
	return out
}

// resolvedLevel is known-ids.mjs resolveLevel.
type resolvedLevel struct {
	provider, model string
	effort          any
	hasEffort       bool
	fallback        []map[string]any
}

// resolveLevelOf is resolveLevel; false where Node throws (or where a
// fallback entry is not an object, which recoveryGuards refuses first).
func resolveLevelOf(cfg map[string]any, level string) (resolvedLevel, bool) {
	l, _ := section(cfg, "levels")[level].(map[string]any)
	if l == nil {
		return resolvedLevel{}, false
	}
	model, _ := l["model"].(string)
	if model == "" {
		return resolvedLevel{}, false
	}
	p := l["provider"]
	if p == nil {
		p = cfg["provider"]
	}
	provider, _ := p.(string)
	if provider == "" {
		return resolvedLevel{}, false
	}
	out := resolvedLevel{provider: provider, model: model}
	out.effort, out.hasEffort = l["effort"]
	if raw := l["fallback"]; raw != nil {
		list, ok := raw.([]any)
		if !ok {
			return resolvedLevel{}, false
		}
		for _, f := range list {
			m, ok := f.(map[string]any)
			if !ok {
				return resolvedLevel{}, false
			}
			out.fallback = append(out.fallback, m)
		}
	}
	return out, true
}

// entries is `[resolved, ...resolved.fallback]` with each provider defaulted.
func (r resolvedLevel) entries() []levelEntry {
	out := []levelEntry{{provider: r.provider, model: r.model, effort: r.effort, hasEffort: r.hasEffort}}
	for _, f := range r.fallback {
		e := levelEntry{provider: r.provider, raw: f}
		if p, ok := f["provider"].(string); ok {
			e.provider = p
		}
		e.model, _ = f["model"].(string)
		e.effort, e.hasEffort = f["effort"]
		out = append(out, e)
	}
	return out
}

// pinLevel is `{...cfg, levels: {...cfg.levels, [level]: {provider, model,
// effort?, fallback}}}`.
func pinLevel(cfg map[string]any, level string, e levelEntry, fallback []any) map[string]any {
	next := make(map[string]any, len(cfg)+1)
	for k, v := range cfg {
		next[k] = v
	}
	levels := map[string]any{}
	for k, v := range section(cfg, "levels") {
		levels[k] = v
	}
	entry := map[string]any{"provider": e.provider, "model": e.model, "fallback": fallback}
	if e.hasEffort {
		entry["effort"] = e.effort
	}
	levels[level] = entry
	next["levels"] = levels
	return next
}

func openBookAllowed(cfg map[string]any) bool {
	return section(cfg, "review")["allow_open_book_codex"] == true
}

// reviewClosedBook is closed-book.mjs closedBookRefused.
func reviewClosedBook(provider any, role string, openBook bool) bool {
	norm := func(v any) string {
		s, _ := v.(string)
		return strings.ToLower(strings.TrimFunc(s, jsSpace))
	}
	r := norm(role)
	noTool := r == "reviewer" || r == "judge" || r == "s2" || r == "author" || r == "delegate"
	return norm(provider) == "openai" && noTool && (!openBook || r == "delegate")
}

// secondLevelSet is `cfg.review?.second_levels?.[level]` truthy (recoveryGuards
// leaves only an object or nothing).
func secondLevelSet(cfg map[string]any, level string) bool {
	return section(cfg, "review", "second_levels")[level] != nil
}

func fallbackLen(cfg map[string]any, level string) int {
	l, _ := section(cfg, "levels")[level].(map[string]any)
	list, _ := l["fallback"].([]any)
	return len(list)
}

// recoveryGuards refuses, before anything is written, the config shapes the
// port does not read the way Node does (a value Node would throw on or read
// as something other than a list or object).
func recoveryGuards(cfg map[string]any, level string) error {
	review := section(cfg, "review")
	if raw := review["routes"]; raw != nil {
		list, ok := raw.([]any)
		if !ok {
			return unsupported("review.routes that is not a list")
		}
		for _, r := range list {
			if _, ok := r.(map[string]any); !ok {
				return unsupported("a review.routes rule that is not an object")
			}
		}
	}
	if raw := review["second_levels"]; raw != nil {
		sl, ok := raw.(map[string]any)
		if !ok {
			return unsupported("review.second_levels that is not an object")
		}
		for _, v := range sl {
			if _, ok := v.(map[string]any); v != nil && !ok {
				return unsupported("a review.second_levels entry that is not an object")
			}
		}
	}
	l, _ := section(cfg, "levels")[level].(map[string]any)
	if raw := l["fallback"]; raw != nil {
		list, ok := raw.([]any)
		if !ok {
			return unsupported("a levels." + level + ".fallback that is not a list")
		}
		for _, f := range list {
			m, ok := f.(map[string]any)
			if !ok {
				return unsupported("a levels." + level + ".fallback entry that is not an object")
			}
			if s, _ := m["model"].(string); s == "" {
				return unsupported("a levels." + level + ".fallback entry with no model")
			}
			if p, has := m["provider"]; has && p != nil {
				if s, _ := p.(string); s == "" {
					return unsupported("a levels." + level + ".fallback entry with a provider that is not text")
				}
			}
			if e, has := m["effort"]; has && e != nil {
				if _, ok := e.(string); !ok {
					return unsupported("a levels." + level + ".fallback entry with an effort that is not text")
				}
			}
		}
	}
	return nil
}

// ruleProviderOf is route.mjs ruleProvider: the provider of the level the
// rule's entry replaces; false where Node returns undefined.
func ruleProviderOf(cfg map[string]any, rule map[string]any) (string, bool) {
	if m, _ := rule["model"].(string); m == "" {
		return "", false
	}
	if fb := rule["fallback"]; fb != nil {
		if _, ok := fb.([]any); !ok {
			return "", false
		}
	}
	p := rule["provider"]
	if p == nil {
		p = cfg["provider"]
	}
	s, _ := p.(string)
	return s, s != ""
}

// secondLevel is secondLevelFor's answer.
type secondLevel struct {
	ok              bool
	reason          string
	cfg             map[string]any
	provider, model string
}

// secondLevelFor is schema-fallback.mjs secondLevelFor (role always given).
func secondLevelFor(cfg map[string]any, level string, fromProvider, fromModel any, role string) secondLevel {
	review := section(cfg, "review")
	override, _ := section(review, "second_levels")[level].(map[string]any)
	if m, _ := override["model"].(string); override == nil || m == "" {
		return secondLevel{reason: "no-second-level"}
	}
	lp := override["provider"]
	if lp == nil {
		lp = review["second_provider"]
	}
	if lp == nil {
		lp = cfg["provider"]
	}
	levelProvider, _ := lp.(string)
	if levelProvider == "" {
		return secondLevel{reason: "no-second-level"}
	}
	type model struct {
		provider, model, effort string
		hasEffort               bool
	}
	all := []any{override}
	if ladder, ok := override["fallback"].([]any); ok {
		all = append(all, ladder...)
	}
	models := []model{}
	for _, raw := range all {
		e, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := e["model"].(string)
		if name == "" {
			continue
		}
		m := model{provider: levelProvider, model: name}
		if p, _ := e["provider"].(string); p != "" {
			m.provider = p
		}
		m.effort, m.hasEffort = e["effort"].(string)
		if fromProvider == any(m.provider) && fromModel == any(m.model) {
			continue
		}
		models = append(models, m)
	}
	if len(models) == 0 {
		return secondLevel{reason: "same-model"}
	}
	runnable := []model{}
	for _, m := range models {
		if !reviewClosedBook(m.provider, role, openBookAllowed(cfg)) {
			runnable = append(runnable, m)
		}
	}
	if len(runnable) == 0 {
		return secondLevel{reason: "closed-book"}
	}
	first := runnable[0]
	entry := map[string]any{}
	for k, v := range override {
		if k != "fallback" && k != "effort" {
			entry[k] = v
		}
	}
	entry["provider"], entry["model"] = first.provider, first.model
	if first.effort != "" {
		entry["effort"] = first.effort
	}
	rest := []any{}
	for _, m := range runnable[1:] {
		e := map[string]any{"provider": m.provider, "model": m.model}
		if m.hasEffort {
			e["effort"] = m.effort
		}
		rest = append(rest, e)
	}
	entry["fallback"] = rest
	next := make(map[string]any, len(cfg)+1)
	for k, v := range cfg {
		next[k] = v
	}
	levels := map[string]any{}
	for k, v := range section(cfg, "levels") {
		levels[k] = v
	}
	levels[level] = entry
	next["levels"] = levels
	return secondLevel{ok: true, cfg: next, provider: first.provider, model: first.model}
}

// SpawnCheck is one level, role and config a review may spawn.
type SpawnCheck struct {
	Level, Role string
	Cfg         map[string]any
}

// SpawnChecks lists, before anything is written, every config File's
// session may spawn on (the level's config, each pinned ladder entry, the
// second level and its entries); nil when File ends before any session. The
// timeout fallback's one config is checked when it is chosen.
func SpawnChecks(in Input) ([]SpawnCheck, error) {
	p, err := prepare(in)
	if err != nil || p.early != nil {
		return nil, err
	}
	spec := p.spec
	out := []SpawnCheck{{Level: spec.level, Role: spec.role, Cfg: in.Cfg}}
	add := func(cfg map[string]any, own bool) {
		lv, ok := resolveLevelOf(cfg, spec.level)
		if !ok {
			return
		}
		if own {
			out = append(out, SpawnCheck{Level: spec.level, Role: spec.role, Cfg: cfg})
		}
		for _, e := range lv.entries() {
			if !reviewClosedBook(e.provider, spec.role, openBookAllowed(cfg)) {
				out = append(out, SpawnCheck{Level: spec.level, Role: spec.role, Cfg: pinLevel(cfg, spec.level, e, []any{})})
			}
		}
	}
	add(in.Cfg, false)
	if second := secondLevelFor(in.Cfg, spec.level, nil, nil, spec.role); second.ok {
		add(second.cfg, true)
	}
	return out, nil
}

// oneRun is one runOne call's state.
type oneRun struct {
	spec           sessionSpec
	text, hash     string
	promptPath     string
	packet         *Packet
	in             Input
	deps           Deps
	schemaFallback bool
	attempts       int
	tried          ledger.Object
	timeouts       map[string]int
}

func (r *oneRun) rowExtra() ledger.Object {
	return ledger.Object{{Key: "lens", Value: r.spec.lens}, {Key: "context_mode", Value: r.packet.ContextMode}, {Key: "ctx_tokens_in", Value: float64(r.packet.TokensIn)}}
}

// spawn is `truncateReviewFields(await deps.spawn(...))`; false when it threw.
func (r *oneRun) spawn(cfg map[string]any) (ledger.Object, bool) {
	res, err := r.deps.Spawn(SpawnOpts{Level: r.spec.level, Role: r.spec.role, PromptPath: r.promptPath, RowExtra: r.rowExtra(), Cfg: cfg})
	if err != nil {
		return nil, false
	}
	out, _ := TruncateReviewFields(res)
	return out, true
}

func (r *oneRun) validate(res ledger.Object) Verdict {
	return ValidateReview(res, r.packet.HunkHeaders, MinTokensOut(r.in.Cfg))
}

// baseCfg is `spec.cfg ?? cfg`.
func (r *oneRun) baseCfg() map[string]any {
	if r.spec.cfg != nil {
		return r.spec.cfg
	}
	return r.in.Cfg
}

type retryResult struct {
	res      ledger.Object
	attempts int
	to       ledger.Object
	broken   ledger.Object
}

// timeoutRetry is spawnWithTimeoutRetry through runOne's attempt(sessionCfg,
// done, secondLevel).
func (r *oneRun) timeoutRetry(cfg map[string]any, done int, secondLevel bool) (retryResult, error) {
	level, role := r.spec.level, r.spec.role
	configured, _ := section(cfg, "levels")[level].(map[string]any)
	hasLadder := false
	if list, ok := configured["fallback"].([]any); ok {
		for _, raw := range list {
			e, _ := raw.(map[string]any)
			p := e["provider"]
			if p == nil {
				p = configured["provider"]
			}
			if p == nil {
				p = cfg["provider"]
			}
			if e["model"] != configured["model"] && !reviewClosedBook(p, role, openBookAllowed(cfg)) {
				hasLadder = true
				break
			}
		}
	}
	current := cfg
	attempts := 0
	failed := map[string]bool{}
	for {
		attempts++
		r.attempts = done + attempts
		res, ok := r.spawn(current)
		if !ok {
			return retryResult{attempts: attempts}, nil
		}
		capacity := get(res, "status") == "unavailable" && get(res, "reason") == "at-capacity"
		if get(res, "status") != "timeout" && !capacity {
			return retryResult{res: res, attempts: attempts}, nil
		}
		var next map[string]any
		if hasLadder {
			lv, ok := resolveLevelOf(current, level)
			if !ok {
				return retryResult{}, &GapError{"fallback-ladder"}
			}
			failed[lv.model] = true
			if s, ok := res.Get("model").(string); ok && s != "" {
				failed[s] = true
			}
			models := lv.entries()
			at := -1
			for i, m := range models {
				if res.Get("model") == any(m.model) && res.Get("provider") == any(m.provider) {
					at = i
					break
				}
			}
			remaining := []levelEntry{}
			for _, m := range models[max(0, at)+1:] {
				if !reviewClosedBook(m.provider, role, openBookAllowed(cfg)) && !failed[m.model] {
					remaining = append(remaining, m)
				}
			}
			if len(remaining) > 0 {
				fb := []any{}
				for _, m := range remaining[1:] {
					fb = append(fb, m.spread())
				}
				next = pinLevel(cfg, level, remaining[0], fb)
			}
		}
		if capacity {
			if next == nil {
				return retryResult{res: res, attempts: attempts}, nil
			}
			current = next
			continue
		}
		retried := !hasLadder && attempts <= timeoutRetries
		row := timeoutRow(r.spec, res, attempts, retried, r.hash)
		if secondLevel {
			row.Set("second_level", true)
		}
		r.deps.note(row)
		if next != nil {
			current = next
			continue
		}
		if !secondLevel {
			taken, err := r.tryNext(res, "timeout")
			if err != nil {
				return retryResult{}, err
			}
			if taken != nil {
				return retryResult{res: taken.res, attempts: attempts + taken.attempts, to: taken.to, broken: res}, nil
			}
		}
		if !retried {
			return retryResult{res: res, attempts: attempts}, nil
		}
		if r.deps.Warn != nil {
			r.deps.Warn("review_retry", "a review session timed out; it was retried once")
		}
	}
}

// tryNext is runOne's tryNext(missed, trigger).
func (r *oneRun) tryNext(missed ledger.Object, trigger string) (*brokenTry, error) {
	cfg := r.in.Cfg
	if trigger == "timeout" && r.schemaFallback && r.spec.cfg == nil && !secondLevelSet(cfg, r.spec.level) && r.deps.WriteRow != nil {
		return r.timeoutFallback(missed)
	}
	return r.afterMiss(missed, r.baseCfg(), r.schemaFallback && r.spec.cfg == nil, trigger, func(c map[string]any, to ledger.Object) (retryResult, error) {
		r.tried = to // the try started: whatever happens next, the session fell back
		return r.timeoutRetry(c, r.attempts, true)
	})
}

// timeoutFallback is tryNext's timeout branch with no second level: at the
// second timeout of one provider/model, timeoutFallbackFor's one other model.
func (r *oneRun) timeoutFallback(missed ledger.Object) (*brokenTry, error) {
	spec, deps := r.spec, r.deps
	provider, model := get(missed, "provider"), get(missed, "model")
	if provider == nil || model == nil {
		lv, _ := resolveLevelOf(r.in.Cfg, spec.level)
		if provider == nil {
			provider = lv.provider
		}
		if model == nil {
			model = lv.model
		}
	}
	key := jsString(provider, true) + "\x00" + jsString(model, true)
	r.timeouts[key]++
	count := r.timeouts[key]
	if count < 2 || r.tried != nil {
		return nil, nil
	}
	if deps.TimeoutFallback == nil {
		return nil, &GapError{"timeout-fallback"}
	}
	rows := []any{}
	var routingError any
	readErr := func() error {
		if deps.SchemaHistory != nil {
			_, fellBack, err := deps.SchemaHistory(r.hash, spec.level, spec.role)
			if err != nil {
				return err
			}
			if fellBack {
				return errFellBack
			}
		}
		if deps.ReadRows != nil {
			got, err := deps.ReadRows()
			if err != nil {
				return err
			}
			if got != nil {
				rows = got
			}
		}
		return nil
	}()
	if readErr == errFellBack {
		return nil, nil
	}
	if readErr != nil {
		// a read failure must not strand the ticket: the local count still
		// proves two timeouts; the degraded choice is recorded
		rows, routingError = []any{}, "ledger-read-failed"
	}
	fb, err := deps.TimeoutFallback(TimeoutFallbackInput{Cfg: r.in.Cfg, Level: spec.level, Role: spec.role, FromProvider: provider, FromModel: model, Rows: rows, TicketID: r.hash})
	if err != nil {
		return nil, &GapError{"timeout-fallback"}
	}
	if fb == nil {
		return nil, nil
	}
	mark := ledger.Object{{Key: "event", Value: "review.schema_fallback"}, {Key: "lens", Value: spec.lens}, {Key: "role", Value: spec.role}, {Key: "level", Value: spec.level},
		{Key: "packet_hash", Value: r.hash}, {Key: "from_provider", Value: provider}, {Key: "from_model", Value: model}, {Key: "to", Value: "review.timeout_fallback"},
		{Key: "to_provider", Value: fb.Provider}, {Key: "to_model", Value: fb.Model}, {Key: "attempts", Value: float64(count)},
		{Key: "trigger", Value: "timeout"}, {Key: "reason", Value: "repeated-timeout"}, {Key: "routing_reason", Value: fb.Reason}}
	if routingError != nil {
		mark.Set("routing_error", routingError)
	}
	if err := deps.WriteRow(mark); err != nil {
		return nil, nil // never run a recovery that could not be recorded
	}
	to := ledger.Object{{Key: "level", Value: "review.timeout_fallback"}, {Key: "provider", Value: fb.Provider}, {Key: "model", Value: fb.Model}}
	r.tried = to
	res, _ := r.spawn(fb.Cfg)
	return &brokenTry{res: res, attempts: 1, to: to}, nil
}

// afterMiss is schema-fallback.mjs afterSchemaMiss with history =
// deps.SchemaHistory (when allowed) and writeRow = deps.WriteRow.
func (r *oneRun) afterMiss(res ledger.Object, cfg map[string]any, allowed bool, trigger string, spawnAt func(map[string]any, ledger.Object) (retryResult, error)) (*brokenTry, error) {
	spec, deps := r.spec, r.deps
	if deps.WriteRow == nil {
		return nil, nil
	}
	timedOut := trigger == "timeout"
	history := allowed && deps.SchemaHistory != nil
	if timedOut && !history {
		return nil, nil
	}
	fromProvider, fromModel := stringOrNull(get(res, "provider")), stringOrNull(get(res, "model"))
	if timedOut && (fromProvider == nil || fromModel == nil) {
		if lv, ok := resolveLevelOf(cfg, spec.level); ok {
			if fromProvider == nil {
				fromProvider = lv.provider
			}
			if fromModel == nil {
				fromModel = lv.model
			}
		}
	}
	if timedOut && secondLevelFor(cfg, spec.level, fromProvider, fromModel, spec.role).reason == "no-second-level" {
		return nil, nil
	}
	var goCfg map[string]any
	var goTo ledger.Object
	err := deps.lock("schema-fallback\x00"+r.hash+"\x00"+spec.level+"\x00"+spec.role, func() error {
		seen, failures, fellBack := false, 0, false
		if history {
			f, fb, err := deps.SchemaHistory(r.hash, spec.level, spec.role)
			if err != nil {
				if deps.Warn != nil {
					deps.Warn("review_schema_history", "the schema-miss history could not be read; no second-level try")
				}
			} else {
				seen, failures, fellBack = true, f, fb
			}
		}
		if !timedOut {
			_ = deps.WriteRow(schemaInvalidRow(spec, res, r.hash, false))
		}
		broken := 0
		if seen {
			broken = failures
			if !timedOut {
				broken++
			}
		}
		if !seen || fellBack || broken < 2 {
			return nil
		}
		second := secondLevelFor(cfg, spec.level, fromProvider, fromModel, spec.role)
		to := "review.second_levels." + spec.level
		mark := ledger.Object{{Key: "event", Value: "review.schema_fallback"}, {Key: "lens", Value: spec.lens}, {Key: "role", Value: spec.role}, {Key: "level", Value: spec.level},
			{Key: "packet_hash", Value: r.hash}, {Key: "from_provider", Value: fromProvider}, {Key: "from_model", Value: fromModel}, {Key: "to", Value: to},
			{Key: "to_provider", Value: nil}, {Key: "to_model", Value: nil}, {Key: "attempts", Value: float64(broken)}, {Key: "trigger", Value: trigger}}
		if !second.ok {
			if second.reason == "no-second-level" {
				return nil
			}
			mark.Set("refused", second.reason)
		} else {
			mark.Set("to_provider", second.provider)
			mark.Set("to_model", second.model)
		}
		if err := deps.WriteRow(mark); err != nil {
			return err // the once-per-packet mark: a failed write ⇒ no try
		}
		if second.ok {
			goCfg = second.cfg
			goTo = ledger.Object{{Key: "level", Value: to}, {Key: "provider", Value: second.provider}, {Key: "model", Value: second.model}}
		}
		return nil
	})
	if err != nil || goCfg == nil {
		return nil, nil
	}
	if deps.Warn != nil {
		deps.Warn("review_schema_fallback", "a review broke twice on one packet (schema or timeout); the second level was tried once")
	}
	out, err := spawnAt(goCfg, goTo)
	if err != nil {
		return nil, err
	}
	return &brokenTry{res: out.res, attempts: out.attempts, to: goTo}, nil
}

// missNoTry is afterSchemaMiss with allowed false: the signed schema_invalid
// row under the packet's lock, never a try.
func (r *oneRun) missNoTry(res ledger.Object, cfg map[string]any) {
	_, _ = r.afterMiss(res, cfg, false, "schema", nil)
}

// routedRecovery is runOne's routedRecovery condition.
func (r *oneRun) routedRecovery() bool {
	if !r.schemaFallback || r.spec.cfg != nil {
		return false
	}
	cfg, level := r.in.Cfg, r.spec.level
	l, _ := section(cfg, "levels")[level].(map[string]any)
	lv, resolved := resolveLevelOf(cfg, level)
	routes, _ := section(cfg, "review")["routes"].([]any)
	match := false
	for _, raw := range routes {
		rule, _ := raw.(map[string]any)
		rl := rule["level"]
		if rl == nil {
			rl = "L2"
		}
		if rl != any(level) || rule["model"] != l["model"] {
			continue
		}
		if p, ok := ruleProviderOf(cfg, rule); ok && resolved && p == lv.provider {
			match = true
			break
		}
	}
	return match && fallbackLen(cfg, level) > 0
}

// routed is runOne's routed branch: each ladder entry pinned, advanced at
// once on a schema miss, a timeout or an exit.
func (r *oneRun) routed() (res ledger.Object, ran bool, tokensIn, tokensOut any, fellBackTo ledger.Object) {
	cfg, spec := r.in.Cfg, r.spec
	lv, _ := resolveLevelOf(cfg, spec.level)
	seen := map[string]bool{}
	var from *levelEntry
	trigger := "schema"
	for _, e := range lv.entries() {
		identity := e.provider + "\x00" + e.model
		if seen[identity] || reviewClosedBook(e.provider, spec.role, openBookAllowed(cfg)) {
			continue
		}
		seen[identity] = true
		sessionCfg := pinLevel(cfg, spec.level, e, []any{})
		r.attempts++
		result, _ := r.spawn(sessionCfg)
		res, ran = result, true
		tokensIn = addTokens(tokensIn, get(usageOf(result), "tokens_in"))
		tokensOut = addTokens(tokensOut, get(usageOf(result), "tokens_out"))
		if from != nil {
			fellBackTo = ledger.Object{{Key: "level", Value: "levels." + spec.level + ".fallback"}, {Key: "provider", Value: e.provider}, {Key: "model", Value: e.model}}
		}
		checked := r.validate(result)
		if checked.OK || (checked.Reason != "schema" && checked.Reason != "timeout" && checked.Reason != "exit") {
			break
		}
		if checked.Reason == "schema" {
			r.missNoTry(result, sessionCfg)
		} else if checked.Reason == "timeout" {
			r.deps.note(ledger.Object{{Key: "event", Value: "review.session_timeout"}, {Key: "lens", Value: spec.lens}, {Key: "role", Value: spec.role}, {Key: "level", Value: spec.level},
				{Key: "provider", Value: e.provider}, {Key: "model", Value: e.model}, {Key: "attempt", Value: float64(r.attempts)}, {Key: "retried", Value: false},
				{Key: "packet_hash", Value: r.hash}, {Key: "second_level", Value: false}})
		}
		if from == nil {
			first := e
			from = &first
			trigger = checked.Reason
		}
	}
	if fellBackTo != nil {
		r.deps.note(ledger.Object{{Key: "event", Value: "review.schema_fallback"}, {Key: "lens", Value: spec.lens}, {Key: "role", Value: spec.role}, {Key: "level", Value: spec.level},
			{Key: "packet_hash", Value: r.hash}, {Key: "from_provider", Value: from.provider}, {Key: "from_model", Value: from.model}, {Key: "to", Value: fellBackTo.Get("level")},
			{Key: "to_provider", Value: fellBackTo.Get("provider")}, {Key: "to_model", Value: fellBackTo.Get("model")}, {Key: "attempts", Value: float64(r.attempts)}, {Key: "trigger", Value: trigger}})
	}
	return res, ran, tokensIn, tokensOut, fellBackTo
}

// brokenFallback is spawnWithBrokenFallback over runOne's attempt: a schema
// miss gets one more try on its model, then the next distinct, runnable model
// of the level's ladder, each pinned.
func (r *oneRun) brokenFallback(initial retryResult) (retryResult, any, any, error) {
	cfg, spec := r.in.Cfg, r.spec
	lv, _ := resolveLevelOf(cfg, spec.level)
	models := lv.entries()
	startAt := 0
	for i, e := range models {
		if get(initial.res, "model") == any(e.model) && get(initial.res, "provider") == any(e.provider) {
			startAt = i
			break
		}
	}
	pending := &initial
	seen := map[string]bool{}
	out := retryResult{}
	var tokensIn, tokensOut any
	for index, e := range models {
		if index < startAt {
			continue
		}
		key := e.provider + "\x00" + e.model
		if seen[key] || reviewClosedBook(e.provider, spec.role, openBookAllowed(cfg)) {
			continue
		}
		seen[key] = true
		pinned := pinLevel(cfg, spec.level, e, []any{})
		if index > 0 {
			out.to = ledger.Object{{Key: "level", Value: "levels." + spec.level + ".fallback"}, {Key: "provider", Value: e.provider}, {Key: "model", Value: e.model}}
		}
		for n := 0; n < 2; n++ {
			var got retryResult
			if pending != nil {
				got, pending = *pending, nil
			} else {
				var err error
				if got, err = r.timeoutRetry(pinned, r.attempts, false); err != nil {
					return retryResult{}, nil, nil, err
				}
			}
			out.res = got.res
			out.attempts += got.attempts
			if x, ok := get(usageOf(got.res), "tokens_in").(float64); ok {
				tokensIn = addTokens(tokensIn, x)
			}
			if x, ok := get(usageOf(got.res), "tokens_out").(float64); ok {
				tokensOut = addTokens(tokensOut, x)
			}
			if r.validate(got.res).Reason != "schema" {
				return out, tokensIn, tokensOut, nil
			}
			r.missNoTry(got.res, pinned)
		}
	}
	return out, tokensIn, tokensOut, nil
}

// runOneWith is engine.mjs runOne (schemaFallback false: consensus).
func runOneWith(spec sessionSpec, text string, packet *Packet, in Input, deps Deps, schemaFallback bool) (ledger.Object, error) {
	suffix, err := randomHex(deps.Rand)
	if err != nil {
		return nil, err
	}
	promptPath := filepath.Join(in.WorkDir, spec.lens+"-"+suffix+".md")
	if err := os.WriteFile(promptPath, []byte(text), 0o600); err != nil {
		return nil, err
	}
	defer os.Remove(promptPath)
	sum := sha256.Sum256([]byte(text))
	r := &oneRun{spec: spec, text: text, hash: hex.EncodeToString(sum[:]), promptPath: promptPath, packet: packet, in: in, deps: deps,
		schemaFallback: schemaFallback, timeouts: map[string]int{}}
	cfg := in.Cfg
	routed := r.routedRecovery()
	levelRecovery := !routed && schemaFallback && spec.cfg == nil && !secondLevelSet(cfg, spec.level) && fallbackLen(cfg, spec.level) > 0
	var res, fellBackTo ledger.Object
	var tokensIn, tokensOut any
	var first retryResult
	if routed {
		got, ran, in, out, to := r.routed()
		tokensIn, tokensOut, fellBackTo = in, out, to
		first = retryResult{res: got, attempts: r.attempts}
		if !ran {
			first.attempts = max(r.attempts, 1)
		}
	} else if first, err = r.timeoutRetry(r.baseCfg(), 0, false); err != nil {
		return nil, err
	}
	if levelRecovery && r.validate(first.res).Reason == "schema" {
		var in, out any
		if first, in, out, err = r.brokenFallback(first); err != nil {
			return nil, err
		}
		fellBackTo, tokensIn, tokensOut = first.to, in, out
	}
	res = first.res
	r.attempts = first.attempts
	var verdict Verdict
	if first.to != nil && !levelRecovery {
		// B91: a timed-out attempt was the packet's second broken result
		fellBackTo = first.to
		verdict = r.validate(res)
		tokensIn = addTokens(orDefault(get(usageOf(first.broken), "tokens_in"), nil), get(usageOf(res), "tokens_in"))
		tokensOut = addTokens(orDefault(get(usageOf(first.broken), "tokens_out"), nil), get(usageOf(res), "tokens_out"))
		if !verdict.OK && verdict.Reason == "schema" {
			deps.note(schemaInvalidRow(spec, res, r.hash, true))
		}
	} else {
		if tokensIn == nil {
			tokensIn = get(usageOf(res), "tokens_in")
		}
		if tokensOut == nil {
			tokensOut = get(usageOf(res), "tokens_out")
		}
		verdict = r.validate(res)
	}
	if !routed && !levelRecovery && fellBackTo == nil && !verdict.OK && verdict.Reason == "schema" {
		done := r.attempts
		second, err := r.tryNext(res, "schema")
		if err != nil {
			return nil, err
		}
		if second != nil {
			res = second.res
			r.attempts = done + second.attempts
			fellBackTo = second.to
			verdict = r.validate(res)
			tokensIn = addTokens(tokensIn, get(usageOf(res), "tokens_in"))
			tokensOut = addTokens(tokensOut, get(usageOf(res), "tokens_out"))
			if !verdict.OK && verdict.Reason == "schema" {
				deps.note(schemaInvalidRow(spec, res, r.hash, true))
			}
		}
	}
	meta := ledger.Object{{Key: "lens", Value: spec.lens}, {Key: "role", Value: spec.role}, {Key: "level", Value: spec.level}, {Key: "slot", Value: spec.slotValue()},
		{Key: "context_mode", Value: packet.ContextMode}, {Key: "ctx_tokens_in", Value: float64(packet.TokensIn)},
		{Key: "provider", Value: orDefault(get(res, "provider"), nil)}, {Key: "model", Value: orDefault(get(res, "model"), nil)},
		{Key: "fallback_step", Value: orDefault(get(res, "fallback_step"), 0.0)}, {Key: "attempts", Value: float64(r.attempts)}}
	if cuts, ok := get(res, "truncated_fields").([]any); ok && len(cuts) > 0 {
		meta.Set("truncated_fields", cuts)
	}
	if verdict.PassedCoerced {
		meta.Set("passed_coerced", true)
	}
	if fellBackTo != nil {
		meta.Set("schema_fallback", fellBackTo)
	}
	meta.Set("tokens_in", orDefault(tokensIn, nil))
	meta.Set("tokens_out", orDefault(tokensOut, nil))
	if !verdict.OK {
		meta.Set("status", "unavailable")
		meta.Set("reason", verdict.Reason)
		meta.Set("review", nil)
		return meta, nil
	}
	meta.Set("status", "ok")
	meta.Set("reason", nil)
	meta.Set("review", verdict.Review)
	return meta, nil
}

// levelGuards is prepare's pre-write check: the recovery shapes the port
// reads (recoveryGuards) and the level config the session layer needs.
func levelGuards(cfg map[string]any, level string) error {
	if err := recoveryGuards(cfg, level); err != nil {
		return err
	}
	l, _ := section(cfg, "levels")[level].(map[string]any)
	if l == nil {
		return unsupported("levels." + level + " is not configured")
	}
	if m, _ := l["model"].(string); m == "" {
		return unsupported("levels." + level + " has no model")
	}
	p, _ := l["provider"].(string)
	if top, _ := cfg["provider"].(string); p == "" && top == "" {
		return unsupported("levels." + level + " has no provider")
	}
	return nil
}
