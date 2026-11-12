package main

import (
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/ledger"
)

const validateUsage = "usage: code-forge validate [--file <path>]\n"

func init() {
	routes["validate"] = smallVerb{name: "validate", usage: validateUsage, prepare: prepareValidate}.run
}

type validateIssue struct{ rule, message string }

// prepareValidate is src/cli/validate.mjs with validateConfig from
// src/config/validate.mjs. Node keeps three cases: a config the schema
// refuses (Ajv's own wording and order), a config holding a secret-shaped
// string (the message scrub over the config's own text) and a non-empty
// coder.sandbox.add_dirs (rule 19 resolves each dir on the filesystem).
func prepareValidate(args []string) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
	usage := func(message string) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
		return func(_, stderr io.Writer) int {
			nodeLog(stderr, "error", maskTokens(message))
			return 2
		}, 0, true
	}
	cwd := physicalCwd()
	file := filepath.Join(cwd, config.Filename)
	for i := 0; i < len(args); i++ {
		if args[i] != "--file" {
			return usage("unknown argument: " + args[i])
		}
		if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "--") {
			return usage("--file requires a path argument")
		}
		file = args[i+1]
		if !filepath.IsAbs(file) {
			file = filepath.Join(cwd, file)
		}
		file = filepath.Clean(file)
		i++
	}
	cfg, order, err := config.LoadFileOrdered(file)
	if yamlSyntaxError(err) {
		return nil, hConfigParseError, false
	}
	if err != nil {
		e, ok := err.(*config.Error)
		if !ok {
			return nil, hValidateUnported, false
		}
		return func(_, stderr io.Writer) int {
			nodeLog(stderr, "error", maskTokens(e.Kind+": "+e.Message))
			return 1
		}, 0, true
	}
	if len(config.SchemaIssues(cfg, "")) > 0 {
		return nil, hValidateSchemaIssues, false
	}
	if configHoldsSecretShape(cfg) {
		return nil, hValidateSecretValues, false
	}
	if sandbox, ok := objAt(cfg, "coder", "sandbox"); ok {
		if dirs, ok := sandbox["add_dirs"].([]any); ok && len(dirs) > 0 {
			return nil, hValidateSandboxDirs, false
		}
	}
	v := configValidator{cfg: cfg, order: order, seen: config.LoadSeenInCache(os.Getenv("HOME")), hasCli: hasCliOnPath}
	errs, warns := v.run()
	var pipeline goPipeline
	if len(errs) == 0 {
		pipeline = loadPipelineGo(filepath.Dir(file))
		if !pipeline.supported {
			return nil, hPipelineUnsupported, false
		}
	}
	return func(stdout, stderr io.Writer) int {
		for _, issue := range errs {
			nodeLog(stderr, "error", maskTokens("["+issue.rule+"] "+issue.message))
		}
		for _, issue := range warns {
			nodeLog(stderr, "warn", maskTokens("["+issue.rule+"] "+issue.message))
		}
		if len(errs) == 0 {
			if pipeline.exists && len(pipeline.problems) > 0 {
				for _, problem := range pipeline.problems {
					nodeLog(stderr, "error", maskTokens(problem))
				}
				return 1
			}
			nodeLog(stdout, "info", file+": valid ("+plural(len(warns), "warning")+")")
			return 0
		}
		nodeLog(stdout, "info", file+": invalid ("+plural(len(errs), "error")+")")
		return 1
	}, 0, true
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// physicalCwd is process.cwd(): the directory with symlinks resolved.
func physicalCwd() string {
	cwd, _ := os.Getwd()
	if real, err := filepath.EvalSymlinks(cwd); err == nil {
		return real
	}
	return cwd
}

// hasCliOnPath is hasCliOnPath in src/cli/validate.mjs.
func hasCliOnPath(provider string) bool {
	name, ok := providerCLINames[provider]
	if !ok {
		return false
	}
	for _, dir := range strings.Split(os.Getenv("PATH"), string(os.PathListSeparator)) {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

// secretShape is SECRET_LOOKING_PATTERNS without the lookbehind: a string
// with any such candidate goes to Node, which scrubs it.
var secretShape = regexp.MustCompile(`sk-[A-Za-z0-9_-]{10,}|xai-[A-Za-z0-9]{20,}|xox[baprs]-[A-Za-z0-9-]{10,}|gh[opsu]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|AKIA[0-9A-Z]{12,}|AIza[0-9A-Za-z_-]{20,}|eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{5,}`)

func secretCandidate(s string) bool {
	if strings.HasPrefix(s, "env:") || strings.HasPrefix(s, "op://") || s == "user" {
		return false
	}
	return secretShape.MatchString(s)
}

func configHoldsSecretShape(node any) bool {
	switch v := node.(type) {
	case string:
		return secretCandidate(v)
	case []any:
		for _, item := range v {
			if configHoldsSecretShape(item) {
				return true
			}
		}
	case map[string]any:
		for key, value := range v {
			if secretCandidate(key) || configHoldsSecretShape(value) {
				return true
			}
		}
	}
	return false
}

// ── small JS-shaped helpers over a parsed config ──

func objAt(node any, path ...string) (map[string]any, bool) {
	cur, ok := node.(map[string]any)
	for _, key := range path {
		if !ok {
			return nil, false
		}
		cur, ok = cur[key].(map[string]any)
	}
	return cur, ok
}

// valueAt is optional chaining: the value and whether it is not undefined.
func valueAt(node any, path ...string) (any, bool) {
	cur := node
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		v, present := m[key]
		if !present {
			return nil, false
		}
		cur = v
	}
	return cur, true
}

func isTrue(v any, present bool) bool { b, ok := v.(bool); return present && ok && b }

func jsTruthy(v any, present bool) bool {
	if !present || v == nil {
		return false
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0 && !math.IsNaN(x)
	}
	return true
}

func jsInteger(v any) (float64, bool) {
	n, ok := v.(float64)
	return n, ok && !math.IsNaN(n) && !math.IsInf(n, 0) && math.Trunc(n) == n
}

func isPlainObj(v any) bool { _, ok := v.(map[string]any); return ok }

// jsKeys is Object.keys: array-index keys first in numeric order, then the
// rest in insertion order.
func (v configValidator) jsKeys(m map[string]any) []string {
	keys := v.order.Keys(m)
	var index, rest []string
	for _, key := range keys {
		if n, err := strconv.ParseUint(key, 10, 32); err == nil && n < math.MaxUint32 && strconv.FormatUint(n, 10) == key {
			index = append(index, key)
		} else {
			rest = append(rest, key)
		}
	}
	sort.SliceStable(index, func(i, j int) bool {
		a, _ := strconv.ParseUint(index[i], 10, 32)
		b, _ := strconv.ParseUint(index[j], 10, 32)
		return a < b
	})
	return append(index, rest...)
}

func jsonOf(v any) string { b, _ := ledger.Stringify(v, false); return string(b) }

func inList(s string, list []string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

func ownStringList(m any, key string) []string {
	obj, ok := m.(map[string]any)
	if !ok || key == "__proto__" {
		return nil
	}
	list, ok := obj[key].([]any)
	if !ok {
		return nil
	}
	out := []string{}
	for _, item := range list {
		s, ok := item.(string)
		if !ok {
			return nil
		}
		out = append(out, s)
	}
	return out
}

// ── the domain rules ──

type configValidator struct {
	cfg    map[string]any
	order  config.Ordered
	seen   map[string][]string
	hasCli func(provider string) bool
}

type effLevel struct {
	provider, model string
	effort          any
	hasEffort       bool
}

var levelNames = []string{"L0", "L1", "L2", "L3"}
var providerNames = []string{"anthropic", "openai", "xai"}

func levelHasModel(level any) bool {
	m, ok := level.(map[string]any)
	if !ok {
		return false
	}
	s, ok := m["model"].(string)
	return ok && s != ""
}

func (v configValidator) effectiveLevel(name string) (effLevel, bool) {
	level, _ := valueAt(v.cfg, "levels", name)
	if !levelHasModel(level) {
		return effLevel{}, false
	}
	m := level.(map[string]any)
	provider, present := m["provider"]
	if !present || provider == nil {
		provider = v.cfg["provider"]
	}
	p, ok := provider.(string)
	if !ok {
		return effLevel{}, false
	}
	effort, has := m["effort"]
	return effLevel{provider: p, model: m["model"].(string), effort: effort, hasEffort: has}, true
}

func (v configValidator) secondL2Provider() (any, bool) {
	if p, ok := valueAt(v.cfg, "review", "second_levels", "L2", "provider"); ok && p != nil {
		return p, true
	}
	if p, ok := valueAt(v.cfg, "review", "second_provider"); ok && p != nil {
		return p, true
	}
	return nil, false
}

func (v configValidator) multimodel() bool { return isTrue(valueAt(v.cfg, "review", "multimodel")) }

func safeProvider(p any) string {
	if s, ok := p.(string); ok && inList(s, providerNames) {
		return `"` + s + `"`
	}
	return "(unrecognised provider)"
}

func (v configValidator) knownModel(provider, model string) bool {
	return inList(model, knownIDsFor(provider)) || inList(model, ownStringList(v.cfg["known_extra"], provider)) || inList(model, v.seenList(provider))
}

func (v configValidator) seenList(provider string) []string {
	if provider == "__proto__" || provider == "constructor" || provider == "prototype" {
		return nil
	}
	return v.seen[provider]
}

func (v configValidator) run() (errs, warns []validateIssue) {
	add := func(severity, rule string, messages ...string) {
		for _, message := range messages {
			if severity == "error" {
				errs = append(errs, validateIssue{rule, message})
			} else {
				warns = append(warns, validateIssue{rule, message})
			}
		}
	}
	// 1 level-missing
	for _, name := range levelNames {
		if _, ok := v.effectiveLevel(name); ok {
			continue
		}
		level, _ := valueAt(v.cfg, "levels", name)
		if levelHasModel(level) {
			add("error", "level-missing", "levels."+name+" has no provider (neither levels."+name+".provider nor the top-level provider is set)")
		} else {
			add("error", "level-missing", "levels."+name+" is missing or has no model")
		}
	}
	// 2 unknown-model-id
	for _, name := range levelNames {
		if l, ok := v.effectiveLevel(name); ok && !v.knownModel(l.provider, l.model) {
			add("warning", "unknown-model-id", "levels."+name+".model is not a known "+safeProvider(l.provider)+" id, not in known_extra, and not seen-in-cache")
		}
	}
	// 3 duplicate-level-tuple
	tuples := map[string]string{}
	for _, name := range levelNames {
		l, ok := v.effectiveLevel(name)
		if !ok {
			continue
		}
		effort := l.effort
		if !l.hasEffort || effort == nil {
			effort = ""
		}
		key := jsonOf([]any{l.provider, l.model, effort})
		if first, dup := tuples[key]; dup {
			add("error", "duplicate-level-tuple", "levels."+first+" and levels."+name+" resolve to the same (provider, model, effort)")
		} else {
			tuples[key] = name
		}
	}
	// 4 stop-at-not-l3
	if stop, ok := valueAt(v.cfg, "escalation", "stop_at"); ok && stop != "L3" {
		add("error", "stop-at-not-l3", `escalation.stop_at must be "L3"`)
	}
	// 5 multimodel-missing-second-provider
	if v.multimodel() && !jsTruthy(valueAt(v.cfg, "review", "second_provider")) {
		add("error", "multimodel-missing-second-provider", "review.multimodel is true but review.second_provider is empty")
	}
	second, hasSecond := v.secondL2Provider()
	secondStr, secondIsStr := second.(string)
	// 6 multimodel-effective-l2-collision
	if v.multimodel() {
		if primary, ok := v.effectiveLevel("L2"); ok && jsTruthy(second, hasSecond) && secondIsStr && primary.provider == secondStr {
			add("error", "multimodel-effective-l2-collision", "the effective L2 provider ("+safeProvider(primary.provider)+") equals the second reviewer's effective L2 provider (review.second_levels.L2.provider, else review.second_provider) — multimodel needs two different providers")
		}
	}
	// 7 judge-family-collision
	if v.multimodel() {
		if judge, ok := v.effectiveLevel("L3"); ok {
			r1, ok1 := v.effectiveLevel("L2")
			c1 := ok1 && judge.provider == r1.provider
			c2 := jsTruthy(second, hasSecond) && secondIsStr && judge.provider == secondStr
			if c1 || c2 {
				which := "reviewer 2 (second_levels.L2)"
				if c1 && c2 {
					which = "both reviewers"
				} else if c1 {
					which = "reviewer 1 (L2)"
				}
				add("error", "judge-family-collision", "the L3 judge's provider ("+safeProvider(judge.provider)+") equals "+which+"'s provider — the judge can't independently check a reviewer it shares a family with")
			}
		}
	}
	// 8 orchestrator-l3-multimodel-off
	if orch, ok := valueAt(v.cfg, "orchestrator"); ok && orch == "L3" && !v.multimodel() {
		add("warning", "orchestrator-l3-multimodel-off", "orchestrator is L3 but review.multimodel is off")
	}
	// 9 gates-not-argv-array
	add("error", "gates-not-argv-array", v.gateRuleMessages()...)
	// 10 secret-looking-value: a config holding a secret shape went to Node.
	// 11 fallback-unknown-or-cli-absent
	for _, name := range levelNames {
		list, _ := valueAt(v.cfg, "levels", name, "fallback")
		items, _ := list.([]any)
		for i, item := range items {
			fb, ok := item.(map[string]any)
			if !ok {
				continue
			}
			provider, ok1 := fb["provider"].(string)
			model, ok2 := fb["model"].(string)
			if !ok1 || !ok2 {
				continue
			}
			unknown := !v.knownModel(provider, model)
			absent := !v.hasCli(provider)
			if unknown || absent {
				msg := fmt.Sprintf("levels.%s.fallback[%d] (provider %s)", name, i, safeProvider(provider))
				if unknown {
					msg += " has an unknown model id"
				}
				if unknown && absent {
					msg += " and"
				}
				if absent {
					msg += " has no CLI on PATH"
				}
				add("warning", "fallback-unknown-or-cli-absent", msg)
			}
		}
	}
	// 12 engine-subprocess-no-cli
	if engine, ok := valueAt(v.cfg, "engine"); ok && engine == "subprocess" {
		var providers []string
		addProvider := func(p string) {
			if !inList(p, providers) {
				providers = append(providers, p)
			}
		}
		for _, name := range levelNames {
			if l, ok := v.effectiveLevel(name); ok {
				addProvider(l.provider)
			}
		}
		if v.multimodel() && secondIsStr {
			addProvider(secondStr)
		}
		var named []string
		for _, p := range providers {
			if !v.hasCli(p) && !inList(safeProvider(p), named) {
				named = append(named, safeProvider(p))
			}
		}
		if len(named) > 0 {
			sort.Strings(named)
			add("error", "engine-subprocess-no-cli", `engine is "subprocess" but no CLI was found on PATH for provider(s): `+strings.Join(named, ", "))
		}
	}
	// 13 shadow-rate-range
	if rate, ok := valueAt(v.cfg, "calibration", "shadow_rate"); ok {
		if n, isNum := rate.(float64); isNum && !(n >= 0 && n <= 0.5) {
			add("error", "shadow-rate-range", "calibration.shadow_rate must be a number within [0, 0.5]")
		}
	}
	// 14 caps-coders-exceeds-cap
	if coders, ok := valueAt(v.cfg, "caps", "coders"); ok {
		if n, isNum := coders.(float64); isNum && !math.IsInf(n, 0) && !math.IsNaN(n) && n > 2 {
			add("warning", "caps-coders-exceeds-cap", "caps.coders is above the R10 cap of 2 concurrent coders — allowed, but recorded as a deviation")
		}
	}
	// 15 effort-not-valid-for-provider
	for _, name := range levelNames {
		if l, ok := v.effectiveLevel(name); ok {
			if msg, bad := effortIssue("levels."+name+".effort", l.provider, l.effort); bad {
				add("error", "effort-not-valid-for-provider", msg)
			}
		}
		list, _ := valueAt(v.cfg, "levels", name, "fallback")
		items, _ := list.([]any)
		for i, item := range items {
			fb, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if msg, bad := effortIssue(fmt.Sprintf("levels.%s.fallback[%d].effort", name, i), fb["provider"], fb["effort"]); bad {
				add("error", "effort-not-valid-for-provider", msg)
			}
		}
	}
	// 16 closed-book-on-openai
	add("warning", "closed-book-on-openai", v.closedBookMessages()...)
	// 17 proof-runners-invalid
	if runners, ok := valueAt(v.cfg, "proof", "runners"); ok {
		list, isList := runners.([]any)
		switch {
		case !isList:
			add("error", "proof-runners-invalid", "proof.runners must be a list of runner entries")
		case len(list) == 0:
			add("error", "proof-runners-invalid", "proof.runners must list at least one runner (leave the key out for none)")
		default:
			for i, entry := range list {
				add("error", "proof-runners-invalid", v.runnerMessages(entry, i)...)
			}
		}
	}
	// 18 review-routes-invalid
	add("error", "review-routes-invalid", v.routeMessages()...)
	// 19 and 20: an empty coder.sandbox.add_dirs yields nothing; a non-empty one went to Node.
	return errs, warns
}

var providerEfforts = map[string][]string{
	"anthropic": {"low", "medium", "high", "xhigh", "max"},
	"openai":    {"minimal", "low", "medium", "high"},
	"xai":       nil,
}

func displayEffort(effort string) string {
	if inList(effort, providerEfforts["anthropic"]) || inList(effort, providerEfforts["openai"]) {
		return `"` + effort + `"`
	}
	return "(unrecognised value)"
}

func effortIssue(keyPath string, provider, effort any) (string, bool) {
	e, ok := effort.(string)
	if !ok {
		return "", false
	}
	p, ok := provider.(string)
	if !ok {
		return "", false
	}
	allowed, known := providerEfforts[p]
	if !known {
		return "", false
	}
	if allowed == nil {
		if e != "" {
			return "", false
		}
		return keyPath + " is empty; provider " + p + " needs a non-empty effort or none at all", true
	}
	if inList(e, allowed) {
		return "", false
	}
	return keyPath + " " + displayEffort(e) + " is not valid for provider " + p + "; use one of: " + strings.Join(allowed, ", "), true
}

// resolveProvider is resolveLevel(cfg, level).provider in
// src/config/known-ids.mjs, with its refusals as false.
func resolveProvider(levels map[string]any, top any, name string) (string, []any, bool) {
	level, ok := levels[name].(map[string]any)
	if !ok {
		return "", nil, false
	}
	if model, ok := level["model"].(string); !ok || model == "" {
		return "", nil, false
	}
	raw, present := level["provider"]
	if !present || raw == nil {
		raw = top
	}
	p, ok := raw.(string)
	if !ok || p == "" {
		return "", nil, false
	}
	fallback, present := level["fallback"]
	if !present || fallback == nil {
		return p, nil, true
	}
	list, ok := fallback.([]any)
	if !ok {
		return "", nil, false
	}
	return p, list, true
}

func providerOf(item any) any {
	if m, ok := item.(map[string]any); ok {
		return m["provider"]
	}
	return nil
}

// ruleProvider is ruleProvider in src/review/route.mjs.
func (v configValidator) ruleProvider(rule map[string]any) any {
	level := "L2"
	if raw, ok := rule["level"]; ok && raw != nil {
		s, isStr := raw.(string)
		if !isStr {
			return nil
		}
		level = s
	}
	levels := map[string]any{}
	if existing, ok := v.cfg["levels"].(map[string]any); ok {
		for key, value := range existing {
			levels[key] = value
		}
	}
	entry := map[string]any{}
	for _, key := range []string{"model", "provider", "effort", "fallback"} {
		if value, ok := rule[key]; ok {
			entry[key] = value
		}
	}
	levels[level] = entry
	if p, _, ok := resolveProvider(levels, v.cfg["provider"], level); ok {
		return p
	}
	return nil
}

func (v configValidator) closedBookMessages() []string {
	var where []string
	levels, _ := v.cfg["levels"].(map[string]any)
	for _, name := range []string{"L2", "L3"} {
		p, fallback, ok := resolveProvider(levels, v.cfg["provider"], name)
		if !ok {
			continue
		}
		if isNoClosedBookProvider(p) {
			where = append(where, "levels."+name)
		}
		for i, fb := range fallback {
			if isNoClosedBookProvider(providerOf(fb)) {
				where = append(where, fmt.Sprintf("levels.%s.fallback[%d]", name, i))
			}
		}
	}
	second, _ := v.secondL2Provider()
	if v.multimodel() && isNoClosedBookProvider(second) {
		where = append(where, "review.second_levels.L2 (else review.second_provider)")
	}
	routes, _ := valueAt(v.cfg, "review", "routes")
	list, _ := routes.([]any)
	for i, item := range list {
		rule, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if isNoClosedBookProvider(v.ruleProvider(rule)) {
			where = append(where, fmt.Sprintf("review.routes[%d]", i))
		}
		if fallback, ok := rule["fallback"].([]any); ok {
			for j, fb := range fallback {
				if isNoClosedBookProvider(providerOf(fb)) {
					where = append(where, fmt.Sprintf("review.routes[%d].fallback[%d]", i, j))
				}
			}
		}
		if own, ok := rule["second"].(map[string]any); ok && v.multimodel() {
			p, present := own["provider"]
			if !present || p == nil {
				p = second
			}
			if isNoClosedBookProvider(p) {
				where = append(where, fmt.Sprintf("review.routes[%d].second", i))
			}
		}
	}
	text := codexClosedBookRefusal
	if isTrue(valueAt(v.cfg, "review", "allow_open_book_codex")) {
		text = openBookCodexWarning
	}
	out := make([]string, len(where))
	for i, key := range where {
		out[i] = key + " resolves to openai — " + text
	}
	return out
}

func (v configValidator) routeMessages() []string {
	routes, present := valueAt(v.cfg, "review", "routes")
	if !present || routes == nil {
		return nil
	}
	list, ok := routes.([]any)
	if !ok {
		return []string{"review.routes must be a list of rules"}
	}
	var messages []string
	seen := map[string]int{}
	second, hasSecond := v.secondL2Provider()
	for i, item := range list {
		at := fmt.Sprintf("review.routes[%d]", i)
		rule, ok := item.(map[string]any)
		if !ok {
			messages = append(messages, at+" must be a rule object")
			continue
		}
		level := any("L2")
		if raw, ok := rule["level"]; ok && raw != nil {
			level = raw
		}
		if level != "L2" && level != "L3" {
			messages = append(messages, at+".level must be L2 or L3")
			continue
		}
		if n, ok := jsInteger(rule["min_lines"]); ok {
			key := level.(string) + "\x00" + jsKey(n)
			if first, dup := seen[key]; dup {
				messages = append(messages, fmt.Sprintf("%s has the same level and min_lines as review.routes[%d]", at, first))
			} else {
				seen[key] = i
			}
		}
		if v.multimodel() && level == "L2" {
			first := v.ruleProvider(rule)
			var own any
			ownPresent := false
			if sec, ok := rule["second"].(map[string]any); ok {
				own, ownPresent = sec["provider"]
			}
			if first == nil {
				// an unresolvable rule (undefined) equals neither provider
			} else if ownPresent && jsEqual(first, own) {
				messages = append(messages, at+".provider (else provider) equals "+at+".second.provider — multimodel needs two different providers")
			} else if hasSecond && jsEqual(first, second) {
				messages = append(messages, at+".provider (else provider) equals the second reviewer's effective L2 provider (review.second_levels.L2.provider, else review.second_provider) — multimodel needs two different providers")
			}
		}
	}
	return messages
}

// jsEqual is === over the scalar values a config holds.
func jsEqual(a, b any) bool {
	switch x := a.(type) {
	case nil:
		return b == nil
	case string, bool:
		return a == b
	case float64:
		y, ok := b.(float64)
		return ok && x == y
	}
	return false
}

func jsKey(n float64) string { return strconv.FormatFloat(n, 'f', -1, 64) }

// ── rule 9 ──

var gateKeys = []string{"test", "lint", "types", "format"}
var gateTokens = []string{"{test_files}", "{changed_files}"}
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var customGateName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
var driveSegment = regexp.MustCompile(`^[A-Za-z]:`)

func isArgvArray(value any) bool {
	list, ok := value.([]any)
	if !ok || len(list) == 0 {
		return false
	}
	for _, item := range list {
		if _, ok := item.(string); !ok {
			return false
		}
	}
	return list[0].(string) != ""
}

func argvStrings(value any) []string {
	list := value.([]any)
	out := make([]string, len(list))
	for i, item := range list {
		out[i] = item.(string)
	}
	return out
}

func gateTokenMessages(key string, argv []string) []string {
	if len(argv) == 1 && inList(argv[0], gateTokens) {
		return []string{"gates." + key + ": '" + argv[0] + "' cannot be the only item (the full suite would run an empty command)"}
	}
	if inList(argv[0], gateTokens) {
		return []string{"gates." + key + ": '" + argv[0] + "' cannot be the command (argv[0])"}
	}
	return nil
}

func (v configValidator) gateMessages(key string, value any, present bool, keys []string, gate map[string]any) []string {
	if !present || (value == nil && gate == nil) {
		return nil
	}
	if gate == nil {
		if !isArgvArray(value) {
			return []string{"gates." + key + " must be a non-empty argv array of strings, an {argv, env} object, or null"}
		}
		return gateTokenMessages(key, argvStrings(value))
	}
	var messages []string
	for _, k := range keys {
		if k != "argv" && k != "env" {
			messages = append(messages, "gates."+key+": unknown key "+jsonOf(k)+" (an object gate has only argv and env)")
		}
	}
	if !isArgvArray(gate["argv"]) {
		messages = append(messages, "gates."+key+".argv must be a non-empty argv array of strings")
	} else {
		messages = append(messages, gateTokenMessages(key, argvStrings(gate["argv"]))...)
	}
	if env, present := gate["env"]; present {
		envMap, ok := env.(map[string]any)
		if !ok {
			messages = append(messages, "gates."+key+".env must be a map of variable names to string values")
		} else {
			for _, name := range v.jsKeys(envMap) {
				if !envName.MatchString(name) {
					messages = append(messages, "gates."+key+".env: "+jsonOf(name)+" is not a variable name (letters, digits and _, not starting with a digit)")
				} else if _, ok := envMap[name].(string); !ok {
					messages = append(messages, "gates."+key+".env."+name+" must be a string")
				}
			}
		}
	}
	return messages
}

func (v configValidator) gateRuleMessages() []string {
	gates, _ := v.cfg["gates"].(map[string]any)
	var messages []string
	for _, key := range gateKeys {
		value, present := gates[key]
		obj, _ := value.(map[string]any)
		messages = append(messages, v.gateMessages(key, value, present, v.jsKeys(obj), obj)...)
	}
	messages = append(messages, v.customGateMessages(gates)...)
	messages = append(messages, v.kindGateMessages(gates)...)
	if n, present := gates["repeat_new_tests"]; present {
		if x, ok := jsInteger(n); !ok || x < 2 || x > 10 {
			messages = append(messages, "gates.repeat_new_tests must be an integer 2..10")
		}
	}
	if n, present := gates["timeout_s"]; present {
		if x, ok := jsInteger(n); !ok || x < 30 || x > 7200 {
			messages = append(messages, "gates.timeout_s must be an integer 30..7200")
		}
	}
	return messages
}

func (v configValidator) customGateMessages(gates map[string]any) []string {
	custom, present := gates["custom"]
	if !present {
		return nil
	}
	list, ok := custom.([]any)
	if !ok {
		return []string{"gates.custom must be an array of at most 20 named object gates"}
	}
	var messages []string
	if len(list) > 20 {
		messages = append(messages, "gates.custom must contain at most 20 entries")
	}
	names := map[string]bool{}
	for i, value := range list {
		key := fmt.Sprintf("custom[%d]", i)
		entry, ok := value.(map[string]any)
		if !ok {
			messages = append(messages, "gates."+key+" must be a named object gate")
			continue
		}
		name, nameIsStr := entry["name"].(string)
		label := key
		if nameIsStr {
			label += " (" + config.SafeSegment(name) + ")"
		}
		if !nameIsStr || !customGateName.MatchString(name) {
			messages = append(messages, "gates."+label+".name must match ^[a-z][a-z0-9_-]{0,31}$")
		}
		if nameIsStr && inList(name, gateKeys) {
			messages = append(messages, "gates."+label+".name must not be a fixed gate name")
		}
		rawName, namePresent := entry["name"]
		nameKey, comparable := setKey(rawName, namePresent)
		if comparable && names[nameKey] {
			messages = append(messages, "gates."+label+".name must be unique")
		}
		if comparable {
			names[nameKey] = true
		}
		command := map[string]any{}
		var commandKeys []string
		for _, k := range v.jsKeys(entry) {
			if k != "name" && k != "evidence" {
				command[k] = entry[k]
				commandKeys = append(commandKeys, k)
			}
		}
		messages = append(messages, v.gateMessages(label, command, true, commandKeys, command)...)
		if argv, ok := command["argv"].([]any); ok {
			for _, arg := range argv {
				if s, ok := arg.(string); ok && (strings.Contains(s, gateTokens[0]) || strings.Contains(s, gateTokens[1])) {
					messages = append(messages, "gates."+label+".argv: {test_files} and {changed_files} are refused in custom gates")
					break
				}
			}
		}
		if evidence, present := entry["evidence"]; present {
			patterns, ok := evidence.([]any)
			if !ok {
				messages = append(messages, "gates."+label+".evidence must be an array of safe relative paths or simple globs")
				continue
			}
			for j, item := range patterns {
				if !safeEvidence(item) {
					messages = append(messages, fmt.Sprintf("gates.%s.evidence[%d] must be a non-empty relative path or glob without absolute paths or a .. segment", label, j))
				}
			}
		}
	}
	return messages
}

// setKey is a Set key under SameValueZero for a scalar; an object or array
// is never equal to another entry's.
func setKey(v any, present bool) (string, bool) {
	if !present {
		return "u", true
	}
	switch x := v.(type) {
	case nil:
		return "null", true
	case string:
		return "s" + x, true
	case bool:
		return "b" + strconv.FormatBool(x), true
	case float64:
		if math.IsNaN(x) {
			return "nan", true
		}
		if x == 0 {
			return "n0", true
		}
		return "n" + jsKey(x), true
	}
	return "", false
}

func safeEvidence(item any) bool {
	s, ok := item.(string)
	if !ok || s == "" || strings.HasPrefix(s, "/") || strings.ContainsAny(s, "\\\x00") {
		return false
	}
	for _, segment := range strings.Split(s, "/") {
		if segment == ".." || driveSegment.MatchString(segment) {
			return false
		}
	}
	return true
}

func (v configValidator) kindGateMessages(gates map[string]any) []string {
	custom := map[string]bool{}
	if list, ok := gates["custom"].([]any); ok {
		for _, gate := range list {
			if m, ok := gate.(map[string]any); ok {
				if name, ok := m["name"].(string); ok {
					custom[name] = true
				}
			}
		}
	}
	kinds, _ := gates["kinds"].(map[string]any)
	var messages []string
	for _, kind := range []string{"code", "docs", "contract"} {
		names, ok := kinds[kind].([]any)
		if !ok {
			continue
		}
		for _, item := range names {
			name, ok := item.(string)
			if !ok {
				continue
			}
			label := "gates.kinds." + kind + ": " + config.SafeSegment(name)
			fixed := inList(name, gateKeys)
			value, present := gates[name]
			if !fixed && !custom[name] {
				messages = append(messages, label+" is an unknown gate name")
			} else if fixed && present && value == nil {
				messages = append(messages, label+" is a null fixed gate")
			}
		}
	}
	return messages
}

// ── rule 17 ──

var runnerAdapters = []string{"junit", "swift", "xcodebuild"}
var runnerAdapterTokens = map[string][]string{
	"junit":      {"{test_file}", "{test_id}", "{test_filter}", "{report}"},
	"swift":      {"{test_file}", "{test_id}", "{test_filter}", "{report}"},
	"xcodebuild": {"{workspace}", "{project}", "{scheme}", "{destination}", "{only_testing}", "{result_bundle}"},
}
var runnerTestTokens = []string{"{test_file}", "{test_id}", "{test_filter}"}
var allRunnerTokens = []string{"{test_file}", "{test_id}", "{test_filter}", "{report}", "{workspace}", "{project}", "{scheme}", "{destination}", "{only_testing}", "{result_bundle}"}
var anyAdapterTokens = allRunnerTokens
var anyReportTokens = []string{"{report}", "{result_bundle}"}
var anyTestTokens = []string{"{test_file}", "{test_id}", "{test_filter}", "{only_testing}"}
var runnerKeys = []string{"match", "adapter", "argv", "env", "timeout_s", "xcode"}
var xcodeKeys = []string{"workspace", "project", "scheme", "destination", "test_target"}
var xcodeRequired = []string{"scheme", "destination", "test_target"}
var xcodeRequiredTokens = [][2]string{
	{"{scheme}", "the xcodebuild adapter fills it from xcode.scheme"},
	{"{destination}", "the xcodebuild adapter fills it from xcode.destination, the one the precheck checks"},
	{"{only_testing}", "a proof must name its test"},
	{"{result_bundle}", "the xcodebuild adapter reads the result bundle code-forge places there"},
}
var tokenShape = regexp.MustCompile(`^\{[a-z_]+\}$`)

func (v configValidator) runnerMessages(entry any, i int) []string {
	at := fmt.Sprintf("proof.runners[%d]", i)
	runner, ok := entry.(map[string]any)
	if !ok {
		return []string{at + " must be an object with match, adapter and argv"}
	}
	var messages []string
	adapter, _ := runner["adapter"].(string)
	if !inList(adapter, runnerAdapters) {
		adapter = ""
		messages = append(messages, at+".adapter must be one of: "+strings.Join(runnerAdapters, ", "))
	}
	for _, key := range v.jsKeys(runner) {
		if !inList(key, runnerKeys) {
			messages = append(messages, at+": "+jsonOf(key)+" is not a runner key ("+strings.Join(runnerKeys, ", ")+")")
		}
	}
	matchOK := false
	if globs, ok := runner["match"].([]any); ok && len(globs) > 0 {
		matchOK = true
		for _, glob := range globs {
			if s, ok := glob.(string); !ok || s == "" {
				matchOK = false
			}
		}
	}
	if !matchOK {
		messages = append(messages, at+".match must be a non-empty list of globs")
	}
	if !isArgvArray(runner["argv"]) {
		messages = append(messages, at+".argv must be a non-empty argv array of strings")
	} else {
		argv := argvStrings(runner["argv"])
		if tokenShape.MatchString(argv[0]) {
			messages = append(messages, at+".argv[0]: a token cannot be the command")
		}
		known := runnerAdapterTokens[adapter]
		for j, item := range argv {
			shaped := tokenShape.MatchString(item)
			switch {
			case strings.Contains(item, "\x00"):
				messages = append(messages, fmt.Sprintf("%s.argv[%d] contains a NUL character", at, j))
			case j > 0 && shaped && adapter != "" && !inList(item, known):
				messages = append(messages, fmt.Sprintf("%s.argv[%d] is not a token of the %s adapter (%s)", at, j, adapter, strings.Join(known, ", ")))
			case j > 0 && shaped && adapter == "" && !inList(item, anyAdapterTokens):
				messages = append(messages, fmt.Sprintf("%s.argv[%d] is not a token of any adapter (%s)", at, j, strings.Join(anyAdapterTokens, ", ")))
			case !shaped && containsAny(item, allRunnerTokens):
				messages = append(messages, fmt.Sprintf("%s.argv[%d]: a token must be a whole argv item (give it as its own item)", at, j))
			}
		}
		items := argv[1:]
		switch adapter {
		case "junit", "swift":
			if !inList("{report}", items) {
				messages = append(messages, at+".argv needs a {report} item (the "+adapter+" adapter reads the report code-forge places there)")
			}
			if !anyIn(items, runnerTestTokens) {
				messages = append(messages, at+".argv needs a test token ("+strings.Join(runnerTestTokens, ", ")+"): a proof must name its test")
			}
		case "xcodebuild":
			for _, pair := range xcodeRequiredTokens {
				if !inList(pair[0], items) {
					messages = append(messages, at+".argv needs a "+pair[0]+" item ("+pair[1]+")")
				}
			}
		default:
			if !anyIn(items, anyReportTokens) {
				messages = append(messages, at+".argv needs a report item ("+strings.Join(anyReportTokens, ", ")+"): every adapter reads the report code-forge places there")
			}
			if !anyIn(items, anyTestTokens) {
				messages = append(messages, at+".argv needs a test token ("+strings.Join(anyTestTokens, ", ")+"): a proof must name its test")
			}
		}
	}
	messages = append(messages, v.xcodeMessages(runner, adapter, at)...)
	if env, present := runner["env"]; present {
		envMap, ok := env.(map[string]any)
		if !ok {
			messages = append(messages, at+".env must be a map of variable names to string values")
		} else {
			for _, name := range v.jsKeys(envMap) {
				value, isStr := envMap[name].(string)
				switch {
				case !envName.MatchString(name):
					messages = append(messages, at+".env: "+jsonOf(name)+" is not a variable name (letters, digits and _, not starting with a digit)")
				case !isStr:
					messages = append(messages, at+".env."+name+" must be a string")
				case strings.Contains(value, "\x00"):
					messages = append(messages, at+".env."+name+" contains a NUL character")
				}
			}
		}
	}
	if t, present := runner["timeout_s"]; present {
		if n, ok := jsInteger(t); !ok || n < 60 || n > 7200 {
			messages = append(messages, at+".timeout_s must be a whole number of seconds from 60 to 7200")
		}
	}
	return messages
}

func containsAny(s string, tokens []string) bool {
	for _, token := range tokens {
		if strings.Contains(s, token) {
			return true
		}
	}
	return false
}

func anyIn(items, set []string) bool {
	for _, item := range items {
		if inList(item, set) {
			return true
		}
	}
	return false
}

func (v configValidator) xcodeMessages(runner map[string]any, adapter, at string) []string {
	raw, present := runner["xcode"]
	if !present {
		if adapter == "xcodebuild" {
			return []string{at + ".xcode is required for the xcodebuild adapter (" + strings.Join(xcodeRequired, ", ") + ")"}
		}
		return nil
	}
	if adapter != "" && adapter != "xcodebuild" {
		return []string{at + ".xcode is only for the xcodebuild adapter"}
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return []string{at + ".xcode must be an object with " + strings.Join(xcodeRequired, ", ")}
	}
	var messages []string
	for _, key := range v.jsKeys(values) {
		if !inList(key, xcodeKeys) {
			messages = append(messages, at+".xcode: "+jsonOf(key)+" is not an xcode key ("+strings.Join(xcodeKeys, ", ")+")")
		}
	}
	for _, key := range xcodeKeys {
		value, present := values[key]
		s, isStr := value.(string)
		switch {
		case !present:
			if inList(key, xcodeRequired) {
				messages = append(messages, at+".xcode."+key+" is required")
			}
		case !isStr || s == "" || strings.HasPrefix(s, "-"):
			messages = append(messages, at+".xcode."+key+` must be a non-empty string that does not start with "-"`)
		case strings.Contains(s, "\x00"):
			messages = append(messages, at+".xcode."+key+" contains a NUL character")
		case key == "test_target" && strings.Contains(s, "/"):
			messages = append(messages, at+`.xcode.test_target must be one target name, without "/"`)
		}
	}
	_, hasWorkspace := values["workspace"]
	_, hasProject := values["project"]
	if hasWorkspace && hasProject {
		messages = append(messages, at+".xcode: give workspace or project, not both")
	}
	if isArgvArray(runner["argv"]) {
		argv := argvStrings(runner["argv"])[1:]
		for _, key := range []string{"workspace", "project"} {
			token := "{" + key + "}"
			_, set := values[key]
			if inList(token, argv) && !set {
				messages = append(messages, at+".argv has "+token+" but "+at+".xcode."+key+" is not set")
			}
			if !inList(token, argv) && set {
				messages = append(messages, at+".xcode."+key+" is set but "+at+".argv has no "+token+" item")
			}
		}
	}
	return messages
}
