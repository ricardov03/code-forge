package gates

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/ledger"
)

// B450: the gate refusals of gates run, in the words of Node's
// customGateMessages and kindGateMessages (src/config/validate.mjs). gates run
// prints them joined by "; " and runs no gate. Map keys are listed in sorted
// order (a parsed config keeps no key order).

var (
	customName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	envName    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	driveName  = regexp.MustCompile(`^[A-Za-z]:`)
)

// blockKinds is Node's BLOCK_KINDS.
var blockKinds = []string{"code", "docs", "contract"}

func quoted(s string) string {
	b, err := ledger.Stringify(s, false)
	if err != nil {
		return fmt.Sprintf("%q", s)
	}
	return string(b)
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// argvArray is Node's isArgvArray: a non-empty array of strings whose first
// item is not empty.
func argvArray(v any) ([]string, bool) {
	a, ok := v.([]any)
	if !ok || len(a) == 0 {
		return nil, false
	}
	s := Strings(a)
	if len(s) != len(a) || s[0] == "" {
		return nil, false
	}
	return s, true
}

func tokenMessages(key string, argv []string) []string {
	if len(argv) == 1 && (argv[0] == "{test_files}" || argv[0] == "{changed_files}") {
		return []string{fmt.Sprintf("gates.%s: '%s' cannot be the only item (the full suite would run an empty command)", key, argv[0])}
	}
	if argv[0] == "{test_files}" || argv[0] == "{changed_files}" {
		return []string{fmt.Sprintf("gates.%s: '%s' cannot be the command (argv[0])", key, argv[0])}
	}
	return nil
}

// gateMessages is Node's gateMessages for one {argv, env} object gate.
func gateMessages(key string, gate map[string]any) []string {
	messages := []string{}
	for _, k := range sortedKeys(gate) {
		if k != "argv" && k != "env" {
			messages = append(messages, fmt.Sprintf("gates.%s: unknown key %s (an object gate has only argv and env)", key, quoted(k)))
		}
	}
	if argv, ok := argvArray(gate["argv"]); !ok {
		messages = append(messages, fmt.Sprintf("gates.%s.argv must be a non-empty argv array of strings", key))
	} else {
		messages = append(messages, tokenMessages(key, argv)...)
	}
	if v, present := gate["env"]; present {
		env, ok := v.(map[string]any)
		if !ok {
			messages = append(messages, fmt.Sprintf("gates.%s.env must be a map of variable names to string values", key))
		} else {
			for _, name := range sortedKeys(env) {
				if !envName.MatchString(name) {
					messages = append(messages, fmt.Sprintf("gates.%s.env: %s is not a variable name (letters, digits and _, not starting with a digit)", key, quoted(name)))
				} else if _, ok := env[name].(string); !ok {
					messages = append(messages, fmt.Sprintf("gates.%s.env.%s must be a string", key, name))
				}
			}
		}
	}
	return messages
}

// nameKey is a set key for any custom gate name, as JS Set compares them.
func nameKey(v any, present bool) string {
	if !present {
		return "undefined"
	}
	if n, ok := v.(json.Number); ok {
		return "number:" + n.String()
	}
	return fmt.Sprintf("%T:%v", v, v)
}

// CustomMessages is Node's customGateMessages for gates.custom.
func CustomMessages(configured map[string]any) []string {
	v, present := configured["custom"]
	if !present {
		return nil
	}
	items, ok := v.([]any)
	if !ok {
		return []string{"gates.custom must be an array of at most 20 named object gates"}
	}
	messages := []string{}
	if len(items) > 20 {
		messages = append(messages, "gates.custom must contain at most 20 entries")
	}
	names := map[string]bool{}
	for i, item := range items {
		key := fmt.Sprintf("custom[%d]", i)
		m, ok := item.(map[string]any)
		if !ok {
			messages = append(messages, fmt.Sprintf("gates.%s must be a named object gate", key))
			continue
		}
		rawName, hasName := m["name"]
		name, isString := rawName.(string)
		label := key
		if isString {
			label = fmt.Sprintf("%s (%s)", key, config.SafeSegment(name))
		}
		if !isString || !customName.MatchString(name) {
			messages = append(messages, fmt.Sprintf("gates.%s.name must match ^[a-z][a-z0-9_-]{0,31}$", label))
		}
		if isString && Has(Order, name) {
			messages = append(messages, fmt.Sprintf("gates.%s.name must not be a fixed gate name", label))
		}
		id := nameKey(rawName, hasName)
		if names[id] {
			messages = append(messages, fmt.Sprintf("gates.%s.name must be unique", label))
		}
		names[id] = true
		command := map[string]any{}
		for k, v := range m {
			if k != "name" && k != "evidence" {
				command[k] = v
			}
		}
		messages = append(messages, gateMessages(label, command)...)
		if a, ok := command["argv"].([]any); ok {
			for _, arg := range a {
				if s, ok := arg.(string); ok && (strings.Contains(s, "{test_files}") || strings.Contains(s, "{changed_files}")) {
					messages = append(messages, fmt.Sprintf("gates.%s.argv: {test_files} and {changed_files} are refused in custom gates", label))
					break
				}
			}
		}
		if evidence, present := m["evidence"]; present {
			patterns, ok := evidence.([]any)
			if !ok {
				messages = append(messages, fmt.Sprintf("gates.%s.evidence must be an array of safe relative paths or simple globs", label))
				continue
			}
			for j, value := range patterns {
				if !safeEvidence(value) {
					messages = append(messages, fmt.Sprintf("gates.%s.evidence[%d] must be a non-empty relative path or glob without absolute paths or a .. segment", label, j))
				}
			}
		}
	}
	return messages
}

func safeEvidence(v any) bool {
	pattern, ok := v.(string)
	if !ok || pattern == "" || strings.HasPrefix(pattern, "/") || strings.ContainsAny(pattern, "\\\x00") {
		return false
	}
	for _, segment := range strings.Split(pattern, "/") {
		if segment == ".." || driveName.MatchString(segment) {
			return false
		}
	}
	return true
}

// KindMessages is Node's kindGateMessages: every string name in a
// gates.kinds.<code|docs|contract> list must be a configured or detected gate.
// Other shapes are left alone, as in Node.
func KindMessages(configured map[string]any) []string {
	custom := map[string]bool{}
	if items, ok := configured["custom"].([]any); ok {
		for _, item := range items {
			if m, ok := item.(map[string]any); ok {
				if name, ok := m["name"].(string); ok {
					custom[name] = true
				}
			}
		}
	}
	kinds, _ := configured["kinds"].(map[string]any)
	messages := []string{}
	for _, kind := range blockKinds {
		names, ok := kinds[kind].([]any)
		if !ok {
			continue
		}
		for _, value := range names {
			name, ok := value.(string)
			if !ok {
				continue
			}
			label := fmt.Sprintf("gates.kinds.%s: %s", kind, config.SafeSegment(name))
			fixed := Has(Order, name)
			gate, present := configured[name]
			if !fixed && !custom[name] {
				messages = append(messages, label+" is an unknown gate name")
			} else if fixed && present && gate == nil {
				messages = append(messages, label+" is a null fixed gate")
			}
		}
	}
	return messages
}
