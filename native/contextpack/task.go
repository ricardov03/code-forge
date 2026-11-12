package contextpack

import (
	"regexp"
	"strings"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B549: pack.mjs readBlockBrief + blockTaskLines for spawn's coderPromptFor.

var taskSpaces = regexp.MustCompile(`[\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]+`)

func taskTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		return r >= 9 && r <= 13 || r == 32 || r == 160 || r == 0x1680 || r >= 0x2000 && r <= 0x200a || r == 0x2028 || r == 0x2029 || r == 0x202f || r == 0x205f || r == 0x3000 || r == 0xfeff
	})
}

// TrimEnd is String.prototype.trimEnd.
func TrimEnd(s string) string { return trimEnd(s) }

// BlockTaskLines is blockTaskLines({brief: readBlockBrief(root, entry),
// acceptance: entry.acceptance}, prompt). A brief that cannot be read is an
// *Error (Node's ContextError); ok false is a shape Node would throw a
// TypeError on, or print through String() (a non-string clause or test).
func BlockTaskLines(root string, entry ledger.Object, prompt string) ([]string, bool, error) {
	normalized := taskTrim(taskSpaces.ReplaceAllString(prompt, " "))
	contains := func(text string) bool {
		return strings.Contains(normalized, taskTrim(taskSpaces.ReplaceAllString(text, " ")))
	}
	lines := []string{}
	if path, isString := object(entry.Get("brief")).Get("path").(string); isString {
		briefLines, err := readLines(root, path, "block.brief")
		if err != nil {
			return nil, true, err
		}
		text := strings.Join(briefLines, "\n")
		if prompt == "" || !contains(text) {
			lines = append(lines, "## brief: "+path)
			lines = append(lines, split(text)...)
		}
	}
	var clauses []any
	if raw := entry.Get("acceptance"); raw != nil {
		list, isList := raw.([]any)
		if !isList {
			return nil, false, nil
		}
		clauses = list
	}
	type clause struct {
		text  string
		human bool
		tests []string
	}
	var missing []clause
	for _, raw := range clauses {
		c, isObject := raw.(ledger.Object)
		if !isObject {
			return nil, false, nil
		}
		text, isString := c.Get("clause").(string)
		if !isString {
			return nil, false, nil
		}
		human := c.Get("kind") == "human_check"
		var tests []string
		if !human {
			list, isList := c.Get("tests").([]any)
			if !isList {
				return nil, false, nil
			}
			for _, t := range list {
				name, isString := t.(string)
				if !isString {
					return nil, false, nil
				}
				tests = append(tests, name)
			}
		}
		gone := prompt == "" || !contains(text)
		if !gone {
			if human {
				gone = !contains("human_check")
			} else {
				for _, name := range tests {
					if !contains(name) {
						gone = true
						break
					}
				}
			}
		}
		if gone {
			missing = append(missing, clause{text, human, tests})
		}
	}
	if len(missing) > 0 {
		lines = append(lines, "## acceptance")
		for _, c := range missing {
			lines = append(lines, "- "+c.text)
			if c.human {
				lines = append(lines, "  kind: human_check")
			} else {
				lines = append(lines, "  tests: "+strings.Join(c.tests, ", "))
			}
		}
	}
	if len(lines) == 0 {
		return []string{}, true, nil
	}
	return split(strings.Join(lines, "\n")), true, nil
}
