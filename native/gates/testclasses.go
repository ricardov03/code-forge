package gates

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
)

// ClassesToken (B335) is replaced inside a fixed gate's argv item by the
// block's test classes, e.g. -Pandroid.testInstrumentationRunnerArguments.class={test_classes}.
// With no class the whole item is dropped, so the gate runs the full suite.
const ClassesToken = "{test_classes}"

// ClassRule is one gates.test_class_map entry: a path glob and how a matching
// path becomes a class name (strip the prefix and suffix, then each "/" turns
// into Separator).
type ClassRule struct {
	Glob, StripPrefix, StripSuffix, Separator string
	match                                     *regexp.Regexp
}

// ParseClassMap reads gates.test_class_map. Absent (nil) means no rules. The
// glob uses the evidence syntax: * within a segment, ** across segments.
func ParseClassMap(v any) ([]ClassRule, error) {
	if v == nil {
		return nil, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("gates.test_class_map must be a list of rules")
	}
	rules := []ClassRule{}
	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("gates.test_class_map[%d] must be a mapping", i)
		}
		for key := range m {
			if key != "glob" && key != "strip_prefix" && key != "strip_suffix" && key != "separator" {
				return nil, fmt.Errorf("gates.test_class_map[%d] has an unknown key %q", i, key)
			}
		}
		text := func(key string) (string, error) {
			value, present := m[key]
			if !present {
				return "", nil
			}
			s, ok := value.(string)
			if !ok {
				return "", fmt.Errorf("gates.test_class_map[%d].%s must be a string", i, key)
			}
			return s, nil
		}
		r := ClassRule{Separator: "."}
		var err error
		if r.Glob, err = text("glob"); err != nil || r.Glob == "" {
			return nil, fmt.Errorf("gates.test_class_map[%d].glob must be a non-empty string", i)
		}
		if r.StripPrefix, err = text("strip_prefix"); err != nil {
			return nil, err
		}
		if r.StripSuffix, err = text("strip_suffix"); err != nil {
			return nil, err
		}
		if _, present := m["separator"]; present {
			if r.Separator, err = text("separator"); err != nil {
				return nil, err
			}
		}
		r.match = evidenceMatcher(r.Glob)
		rules = append(rules, r)
	}
	return rules, nil
}

// BlockTestPaths are the block's own test candidates: its block open --tests
// paths, else its exact owned entries (globs and directories are skipped).
func BlockTestPaths(block ledger.Object) []string {
	files := Strings(block.Get("test_files"))
	if len(files) > 0 {
		return files
	}
	for _, f := range Strings(block.Get("owned_files")) {
		if owned.IsExactEntry(f) && !strings.HasSuffix(f, "/") {
			files = append(files, f)
		}
	}
	return files
}

// TestClasses maps each path through the first rule whose glob matches it. A
// path outside every rule is ignored. Order follows the paths; duplicates and
// names holding "," (the filter's join) are dropped.
func TestClasses(rules []ClassRule, files []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, f := range files {
		if !SafeFile(f) {
			continue
		}
		for _, r := range rules {
			match := r.match
			if match == nil {
				match = evidenceMatcher(r.Glob)
			}
			if !match.MatchString(f) {
				continue
			}
			name := strings.TrimSuffix(strings.TrimPrefix(f, r.StripPrefix), r.StripSuffix)
			name = strings.ReplaceAll(name, "/", r.Separator)
			if name != "" && !strings.Contains(name, ",") && !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
			break
		}
	}
	return out
}

// BlockClassFilter is the block's test classes joined with ",", or "" when
// none match (the gate then runs the full suite).
func BlockClassFilter(rules []ClassRule, block ledger.Object) string {
	return strings.Join(TestClasses(rules, BlockTestPaths(block)), ",")
}

// HasClasses reports whether any fixed gate argv uses ClassesToken.
func HasClasses(p Plan) bool {
	for _, c := range p.Commands {
		if c == nil {
			continue
		}
		for _, a := range c.Argv {
			if strings.Contains(a, ClassesToken) {
				return true
			}
		}
	}
	return false
}

// WithClasses returns a copy of p whose fixed gate argv items holding
// ClassesToken carry filter, or are dropped when filter is "". The commands
// of p are not changed.
func WithClasses(p Plan, filter string) (Plan, error) {
	if !HasClasses(p) {
		return p, nil
	}
	commands := make(map[string]*Command, len(p.Commands))
	for _, name := range Order {
		c, present := p.Commands[name]
		if !present {
			continue
		}
		commands[name] = c
		if c == nil {
			continue
		}
		uses := false
		for _, a := range c.Argv {
			uses = uses || strings.Contains(a, ClassesToken)
		}
		if !uses {
			continue
		}
		if strings.Contains(c.Argv[0], ClassesToken) {
			return p, fmt.Errorf("gates.%s: '%s' cannot be in the command (argv[0])", name, ClassesToken)
		}
		copied := *c
		copied.Argv = []string{}
		for _, a := range c.Argv {
			if !strings.Contains(a, ClassesToken) {
				copied.Argv = append(copied.Argv, a)
			} else if filter != "" {
				copied.Argv = append(copied.Argv, strings.ReplaceAll(a, ClassesToken, filter))
			}
		}
		commands[name] = &copied
	}
	for name, c := range p.Commands {
		if _, done := commands[name]; !done {
			commands[name] = c
		}
	}
	p.Commands = commands
	return p, nil
}
