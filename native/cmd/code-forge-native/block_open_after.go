package main

import (
	"slices"
	"strings"
)

// Open's multi flags have the same parsing semantics as owned and tests in the
// shared parser. Keep them local so the other block verbs retain their grammar.
func blockOpenFlags(args []string) (map[string]any, []string, string) {
	flags, pos := map[string]any{}, []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			pos = append(pos, arg)
			continue
		}
		name, value, inline := strings.Cut(arg[2:], "=")
		multi := slices.Contains([]string{"owned", "tests", "after", "after-outcome"}, name)
		boolean := name == "json" || name == "remove-worktree"
		if _, exists := flags[name]; exists && !multi {
			return flags, pos, "--" + name + " given more than once"
		}
		if boolean {
			if inline {
				return flags, pos, "--" + name + " takes no value"
			}
			flags[name] = true
		} else if multi {
			values, _ := flags[name].([]string)
			if inline && value != "" {
				values = append(values, value)
			}
			for i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				i++
				values = append(values, args[i])
			}
			if len(values) == 0 {
				return flags, pos, "--" + name + " needs at least one value"
			}
			flags[name] = values
		} else {
			if !inline {
				if i+1 == len(args) || strings.HasPrefix(args[i+1], "--") {
					return flags, pos, "--" + name + " needs a value"
				}
				i++
				value = args[i]
			} else if value == "" {
				return flags, pos, "--" + name + " needs a value"
			}
			flags[name] = value
		}
	}
	return flags, pos, ""
}
