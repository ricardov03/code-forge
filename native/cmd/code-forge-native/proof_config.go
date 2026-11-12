package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/ricardov03/code-forge/native/config"
)

// B462: configAt of src/cli/proof.mjs in Go. The project config is loaded
// with native/config and checked as validateConfig does (schema plus the
// error-severity domain rules). Only the count of errors and their places
// (schema path, else rule id) reach the refusal, so the rules below count
// issues and never build their messages. A config that needs a rule Go has
// not ported (review.routes under multimodel, coder.sandbox.add_dirs,
// proof.runners) or a merge-gate check Node's loader does not make still
// runs in Node.

// proofConfigAt returns the config ({} when no file), a refusal message, or
// a hand-over reason.
func proofConfigAt(dir string) (map[string]any, string, handoverReason) {
	cfg, err := config.Load(dir)
	if err != nil {
		var cfgErr *config.Error
		if !errors.As(err, &cfgErr) {
			return nil, "", hProofConfig
		}
		switch cfgErr.Kind {
		case "not-found":
			return map[string]any{}, "", 0
		case "read-error", "parse-error", "migrate-error":
			return nil, config.Filename + " does not load (" + cfgErr.Kind + ") — run code-forge validate", 0
		}
		return nil, "", hProofConfig
	}
	if proofConfigUnported(cfg) {
		return nil, "", hProofConfig
	}
	var where []string
	seen := map[string]bool{}
	count := 0
	add := func(place string, n int) {
		if n == 0 {
			return
		}
		count += n
		if !seen[place] {
			seen[place] = true
			where = append(where, place)
		}
	}
	for _, issue := range config.SchemaIssues(cfg, "") {
		add(issue.Path, 1)
	}
	for _, rule := range proofDomainRules {
		add(rule.id, rule.count(cfg))
	}
	if count > 0 {
		return nil, fmt.Sprintf("%s is invalid (%d error(s) at %s) — run code-forge validate", config.Filename, count, strings.Join(where, ", ")), 0
	}
	return cfg, "", 0
}

// proofConfigUnported names the configs whose domain rules Go does not count.
func proofConfigUnported(cfg map[string]any) bool {
	review := jsObject(cfg["review"])
	if routes, ok := review["routes"].([]any); ok && len(routes) > 0 && review["multimodel"] == true {
		return true
	}
	if sandbox := jsObject(jsObject(cfg["coder"])["sandbox"]); sandbox != nil {
		if dirs, ok := sandbox["add_dirs"].([]any); ok && len(dirs) > 0 {
			return true
		}
	}
	proof := jsObject(cfg["proof"])
	_, runners := proof["runners"]
	return runners
}

func jsObject(value any) map[string]any { out, _ := value.(map[string]any); return out }

func pcJSTruthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case float64:
		return v != 0 && !math.IsNaN(v)
	}
	return true
}

func pcJSInteger(value any) (float64, bool) {
	n, ok := value.(float64)
	return n, ok && !math.IsInf(n, 0) && math.Trunc(n) == n
}

type proofLevel struct {
	provider, model string
	effort          any
	hasEffort       bool
}

var proofLevelNames = []string{"L0", "L1", "L2", "L3"}

// effectiveLevel of validate.mjs.
func proofEffectiveLevel(cfg map[string]any, name string) *proofLevel {
	level := jsObject(jsObject(cfg["levels"])[name])
	model, ok := level["model"].(string)
	if !ok || model == "" {
		return nil
	}
	provider, present := level["provider"]
	if !present || provider == nil {
		provider = cfg["provider"]
	}
	text, ok := provider.(string)
	if !ok {
		return nil
	}
	effort, hasEffort := level["effort"]
	return &proofLevel{provider: text, model: model, effort: effort, hasEffort: hasEffort}
}

func proofSecondL2Provider(cfg map[string]any) any {
	review := jsObject(cfg["review"])
	if p, ok := jsObject(jsObject(review["second_levels"])["L2"])["provider"]; ok && p != nil {
		return p
	}
	if p, ok := review["second_provider"]; ok && p != nil {
		return p
	}
	return nil
}

var proofDomainRules = []struct {
	id    string
	count func(map[string]any) int
}{
	{"level-missing", func(cfg map[string]any) int {
		n := 0
		for _, name := range proofLevelNames {
			if proofEffectiveLevel(cfg, name) == nil {
				n++
			}
		}
		return n
	}},
	{"duplicate-level-tuple", func(cfg map[string]any) int {
		seen, n := map[string]bool{}, 0
		for _, name := range proofLevelNames {
			level := proofEffectiveLevel(cfg, name)
			if level == nil {
				continue
			}
			effort := level.effort
			if !level.hasEffort || effort == nil {
				effort = ""
			}
			key, _ := json.Marshal([]any{level.provider, level.model, effort})
			if seen[string(key)] {
				n++
			} else {
				seen[string(key)] = true
			}
		}
		return n
	}},
	{"stop-at-not-l3", func(cfg map[string]any) int {
		if stop, ok := jsObject(cfg["escalation"])["stop_at"]; ok && stop != "L3" {
			return 1
		}
		return 0
	}},
	{"multimodel-missing-second-provider", func(cfg map[string]any) int {
		review := jsObject(cfg["review"])
		if review["multimodel"] == true && !pcJSTruthy(review["second_provider"]) {
			return 1
		}
		return 0
	}},
	{"multimodel-effective-l2-collision", func(cfg map[string]any) int {
		if jsObject(cfg["review"])["multimodel"] != true {
			return 0
		}
		primary, second := proofEffectiveLevel(cfg, "L2"), proofSecondL2Provider(cfg)
		if primary != nil && pcJSTruthy(second) && second == any(primary.provider) {
			return 1
		}
		return 0
	}},
	{"judge-family-collision", func(cfg map[string]any) int {
		if jsObject(cfg["review"])["multimodel"] != true {
			return 0
		}
		judge := proofEffectiveLevel(cfg, "L3")
		if judge == nil {
			return 0
		}
		first, second := proofEffectiveLevel(cfg, "L2"), proofSecondL2Provider(cfg)
		if (first != nil && first.provider == judge.provider) || (pcJSTruthy(second) && second == any(judge.provider)) {
			return 1
		}
		return 0
	}},
	{"gates-not-argv-array", proofGateIssues},
	{"secret-looking-value", func(cfg map[string]any) int { return proofSecretIssues(cfg) }},
	{"shadow-rate-range", func(cfg map[string]any) int {
		if rate, ok := jsObject(cfg["calibration"])["shadow_rate"].(float64); ok && !(rate >= 0 && rate <= 0.5) {
			return 1
		}
		return 0
	}},
	{"effort-not-valid-for-provider", func(cfg map[string]any) int {
		n := 0
		for _, name := range proofLevelNames {
			if level := proofEffectiveLevel(cfg, name); level != nil && proofBadEffort(level.provider, level.effort) {
				n++
			}
			fallback, _ := jsObject(jsObject(cfg["levels"])[name])["fallback"].([]any)
			for _, fb := range fallback {
				if entry := jsObject(fb); entry != nil && proofBadEffort(entry["provider"], entry["effort"]) {
					n++
				}
			}
		}
		return n
	}},
	{"review-routes-invalid", func(cfg map[string]any) int {
		routes, present := jsObject(cfg["review"])["routes"]
		if !present || routes == nil {
			return 0
		}
		list, ok := routes.([]any)
		if !ok {
			return 1
		}
		n, seen := 0, map[string]bool{}
		for _, value := range list {
			rule := jsObject(value)
			if rule == nil {
				n++
				continue
			}
			level, present := rule["level"]
			if !present || level == nil {
				level = "L2"
			}
			if level != "L2" && level != "L3" {
				n++
				continue
			}
			if lines, ok := pcJSInteger(rule["min_lines"]); ok {
				key := fmt.Sprint(level, "\x00", lines)
				if seen[key] {
					n++
				}
				seen[key] = true
			}
		}
		return n
	}},
}

// proofBadEffort is checkEffort's refusals (invalid, unsupported, empty) for a
// string effort; any other effort is not checked.
func proofBadEffort(provider, effort any) bool {
	text, ok := effort.(string)
	if !ok {
		return false
	}
	switch provider {
	case "anthropic":
		return !proofIn(text, "low", "medium", "high", "xhigh", "max")
	case "openai":
		return !proofIn(text, "minimal", "low", "medium", "high")
	case "xai":
		return text == ""
	}
	return false
}

func proofIn(value string, list ...string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func proofSecretIssues(node any) int {
	switch v := node.(type) {
	case string:
		if config.LooksLikeSecret(v) {
			return 1
		}
	case []any:
		n := 0
		for _, item := range v {
			n += proofSecretIssues(item)
		}
		return n
	case map[string]any:
		n := 0
		for key, value := range v {
			if config.LooksLikeSecret(key) {
				n++
			}
			n += proofSecretIssues(value)
		}
		return n
	}
	return 0
}

var proofGateKeys = []string{"test", "lint", "types", "format"}
var proofEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var proofGateName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

func proofIsArgv(value any) ([]string, bool) {
	list, ok := value.([]any)
	if !ok || len(list) == 0 {
		return nil, false
	}
	argv := make([]string, len(list))
	for i, item := range list {
		text, ok := item.(string)
		if !ok {
			return nil, false
		}
		argv[i] = text
	}
	return argv, argv[0] != ""
}

func proofIsGateToken(s string) bool { return s == "{test_files}" || s == "{changed_files}" }

// proofGateMessages counts gateMessages of validate.mjs.
func proofGateMessages(value any, present bool) int {
	if !present || value == nil {
		return 0
	}
	tokens := func(argv []string) int {
		if proofIsGateToken(argv[0]) {
			return 1
		}
		return 0
	}
	gate, isObject := value.(map[string]any)
	if !isObject {
		argv, ok := proofIsArgv(value)
		if !ok {
			return 1
		}
		return tokens(argv)
	}
	n := 0
	for key := range gate {
		if key != "argv" && key != "env" {
			n++
		}
	}
	if argv, ok := proofIsArgv(gate["argv"]); !ok {
		n++
	} else {
		n += tokens(argv)
	}
	if env, present := gate["env"]; present {
		if object, ok := env.(map[string]any); !ok {
			n++
		} else {
			for name, envValue := range object {
				if _, ok := envValue.(string); !proofEnvName.MatchString(name) || !ok {
					n++
				}
			}
		}
	}
	return n
}

// proofJSKey stands for a JS value in a Set: strings, numbers, booleans,
// null and undefined compare by value; objects never equal another.
func proofJSKey(value any, present bool, i int) string {
	if !present {
		return "undefined"
	}
	switch v := value.(type) {
	case nil:
		return "null"
	case string:
		return "s:" + v
	case float64:
		return fmt.Sprint("n:", v)
	case bool:
		return fmt.Sprint("b:", v)
	}
	return fmt.Sprint("o:", i)
}

func proofGateIssues(cfg map[string]any) int {
	gates := jsObject(cfg["gates"])
	n := 0
	for _, key := range proofGateKeys {
		value, present := gates[key]
		n += proofGateMessages(value, present)
	}
	if custom, present := gates["custom"]; present {
		list, ok := custom.([]any)
		if !ok {
			n++
		} else {
			if len(list) > 20 {
				n++
			}
			names := map[string]bool{}
			for i, value := range list {
				entry := jsObject(value)
				if entry == nil {
					n++
					continue
				}
				name, hasName := entry["name"]
				text, isText := name.(string)
				if !isText || !proofGateName.MatchString(text) {
					n++
				}
				if isText && proofIn(text, proofGateKeys...) {
					n++
				}
				key := proofJSKey(name, hasName, i)
				if names[key] {
					n++
				}
				names[key] = true
				command := map[string]any{}
				for k, v := range entry {
					if k != "name" && k != "evidence" {
						command[k] = v
					}
				}
				n += proofGateMessages(command, true)
				if argv, ok := command["argv"].([]any); ok {
					for _, arg := range argv {
						if s, ok := arg.(string); ok && (strings.Contains(s, "{test_files}") || strings.Contains(s, "{changed_files}")) {
							n++
							break
						}
					}
				}
				if evidence, present := entry["evidence"]; present {
					patterns, ok := evidence.([]any)
					if !ok {
						n++
						continue
					}
					for _, pattern := range patterns {
						if !proofSafeEvidence(pattern) {
							n++
						}
					}
				}
			}
		}
	}
	custom := map[string]bool{}
	if list, ok := gates["custom"].([]any); ok {
		for _, value := range list {
			if name, ok := jsObject(value)["name"].(string); ok {
				custom[name] = true
			}
		}
	}
	kinds := jsObject(gates["kinds"])
	for _, kind := range []string{"code", "docs", "contract"} {
		names, ok := kinds[kind].([]any)
		if !ok {
			continue
		}
		for _, value := range names {
			name, ok := value.(string)
			if !ok {
				continue
			}
			fixed := proofIn(name, proofGateKeys...)
			if !fixed && !custom[name] {
				n++
			} else if gate, present := gates[name]; fixed && present && gate == nil {
				n++
			}
		}
	}
	if value, present := gates["repeat_new_tests"]; present {
		if v, ok := pcJSInteger(value); !ok || v < 2 || v > 10 {
			n++
		}
	}
	if value, present := gates["timeout_s"]; present {
		if v, ok := pcJSInteger(value); !ok || v < 30 || v > 7200 {
			n++
		}
	}
	return n
}

var proofDriveSegment = regexp.MustCompile(`^[A-Za-z]:`)

func proofSafeEvidence(value any) bool {
	pattern, ok := value.(string)
	if !ok || pattern == "" || strings.HasPrefix(pattern, "/") || strings.ContainsAny(pattern, "\\\x00") {
		return false
	}
	for _, segment := range strings.Split(pattern, "/") {
		if segment == ".." || proofDriveSegment.MatchString(segment) {
			return false
		}
	}
	return true
}
