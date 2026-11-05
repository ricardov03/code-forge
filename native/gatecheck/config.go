package gatecheck

import (
	"encoding/json"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/gitx"
	"gopkg.in/yaml.v3"
)

// HighPaths reads both sides, with the base checked as a commit even if neither
// side has configuration. Unsupported configuration dialects request handover.
func HighPaths(workspace, base, version string) ([]any, bool) {
	if base == "" || strings.HasPrefix(base, "-") {
		return nil, false
	}
	paths := []any{}
	current, err := config.Load(workspace)
	if err == nil {
		if !validConfig(current, version) {
			return nil, false
		}
		paths = append(paths, highPaths(current)...)
	} else if e, ok := err.(*config.Error); !ok || e.Kind != "not-found" {
		return nil, false
	}
	if _, err := gitx.Run(workspace, "rev-parse", "--verify", "--quiet", base+"^{commit}"); err != nil {
		return nil, false
	}
	listed, err := gitx.Run(workspace, "ls-tree", base, "--", ".code-forge.yml")
	if err != nil {
		return nil, false
	}
	if strings.TrimSpace(listed) != "" {
		shown, err := gitx.Run(workspace, "show", base+":.code-forge.yml")
		if err != nil {
			return nil, false
		}
		baseConfig, ok := plainYAML(shown)
		if !ok || !validConfig(baseConfig, version) {
			return nil, false
		}
		for _, p := range highPaths(baseConfig) {
			if !slices.Contains(paths, p) {
				paths = append(paths, p)
			}
		}
	}
	return paths, true
}
func highPaths(cfg map[string]any) []any {
	return array(mapping(mapping(mapping(cfg["proof"])["tiers"])["high"])["paths"])
}
func mapping(v any) map[string]any { m, _ := v.(map[string]any); return m }

// Domain validation is deliberately bounded to the ordinary single-reviewer
// configuration. Shape validation uses the shared schema. Configuration with
// consensus, routes, runners, sandbox directories or custom gates stays in Node.
func validConfig(cfg map[string]any, version string) bool {
	if config.ValidateSchema(cfg, version) != nil {
		return false
	}
	encoded, _ := json.Marshal(cfg)
	if regexp.MustCompile(`sk-|xai-|xox[baprs]-|gh[opsu]_|github_pat_|AKIA|AIza|eyJ`).Match(encoded) {
		return false
	}
	for _, key := range []string{"coder", "system1", "shadow", "keys"} {
		if cfg[key] != nil {
			return false
		}
	}
	if value := mapping(cfg["escalation"])["stop_at"]; value != nil && value != "L3" {
		return false
	}
	review := mapping(cfg["review"])
	for _, key := range []string{"routes", "second_levels", "second_provider", "judge"} {
		if review[key] != nil {
			return false
		}
	}
	if review["multimodel"] == true || review["mode"] == "consensus" {
		return false
	}
	if mapping(cfg["proof"])["runners"] != nil {
		return false
	}
	seen := map[string]bool{}
	for _, name := range []string{"L0", "L1", "L2", "L3"} {
		level := mapping(mapping(cfg["levels"])[name])
		model := Text(level["model"])
		provider := level["provider"]
		if provider == nil {
			provider = cfg["provider"]
		}
		if model == "" || !slices.Contains([]string{"anthropic", "openai", "xai"}, Text(provider)) {
			return false
		}
		if level["fallback"] != nil {
			return false
		}
		effort := Text(level["effort"])
		if level["effort"] != nil {
			if provider == "anthropic" && !slices.Contains([]string{"low", "medium", "high", "xhigh", "max"}, effort) || provider == "openai" && !slices.Contains([]string{"minimal", "low", "medium", "high"}, effort) || effort == "" {
				return false
			}
		}
		// Closed-book policy has provider-dependent errors; keep that setting in Node.
		for _, key := range []string{"no_tools", "closed_book"} {
			if level[key] != nil {
				return false
			}
		}
		tuple := Text(provider) + "\x00" + model + "\x00" + effort
		if seen[tuple] {
			return false
		}
		seen[tuple] = true
	}
	for key, value := range mapping(cfg["gates"]) {
		if value == nil {
			continue
		}
		if !slices.Contains([]string{"test", "lint", "typecheck", "build"}, key) {
			return false
		}
		argv, ok := value.([]any)
		if !ok || len(argv) == 0 || Text(argv[0]) == "" || argv[0] == "{test_files}" || argv[0] == "{changed_files}" {
			return false
		}
		for _, v := range argv {
			if _, ok := v.(string); !ok {
				return false
			}
		}
	}
	return true
}

var yamlNumber = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

// Decode only YAML nodes whose core-schema value is unambiguous in both YAML
// libraries. Advanced tags, aliases and multiple documents hand over read-only.
func plainYAML(source string) (map[string]any, bool) {
	var doc yaml.Node
	decoder := yaml.NewDecoder(strings.NewReader(source))
	if decoder.Decode(&doc) != nil || len(doc.Content) != 1 {
		return nil, false
	}
	var extra yaml.Node
	if decoder.Decode(&extra) != io.EOF {
		return nil, false
	}
	var decode func(*yaml.Node) (any, bool)
	decode = func(n *yaml.Node) (any, bool) {
		if n.Anchor != "" || n.Style&yaml.TaggedStyle != 0 {
			return nil, false
		}
		switch n.Kind {
		case yaml.MappingNode:
			out := map[string]any{}
			for i := 0; i < len(n.Content); i += 2 {
				k := n.Content[i]
				if k.Kind != yaml.ScalarNode || k.Tag != "!!str" || k.Value == "<<" {
					return nil, false
				}
				if _, had := out[k.Value]; had {
					return nil, false
				}
				value, ok := decode(n.Content[i+1])
				if !ok {
					return nil, false
				}
				out[k.Value] = value
			}
			return out, true
		case yaml.SequenceNode:
			out := []any{}
			for _, child := range n.Content {
				value, ok := decode(child)
				if !ok {
					return nil, false
				}
				out = append(out, value)
			}
			return out, true
		case yaml.ScalarNode:
			switch n.Tag {
			case "!!str":
				return n.Value, true
			case "!!null":
				return nil, true
			case "!!bool":
				return strings.EqualFold(n.Value, "true"), true
			case "!!int", "!!float":
				if !yamlNumber.MatchString(n.Value) {
					return nil, false
				}
				number, err := strconv.ParseFloat(n.Value, 64)
				return number, err == nil
			}
		}
		return nil, false
	}
	value, ok := decode(doc.Content[0])
	cfg, mapping := value.(map[string]any)
	if !ok || !mapping {
		return nil, false
	}
	if cfg["version"] == nil {
		cfg["version"] = float64(1)
	}
	if cfg["version"] != float64(1) {
		return nil, false
	}
	return cfg, true
}
