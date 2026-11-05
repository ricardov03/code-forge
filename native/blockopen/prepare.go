// Package blockopen owns the explicit-level block-open transaction.
package blockopen

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/ricardov03/code-forge/native/blockops"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
	"github.com/ricardov03/code-forge/native/state"
	"gopkg.in/yaml.v3"
)

func object(v any) ledger.Object         { o, _ := v.(ledger.Object); return o }
func text(v any) string                  { s, _ := v.(string); return s }
func quote(s string) string              { b, _ := ledger.Stringify(s, false); return string(b) }
func refusal(code, message string) error { return &state.Error{Code: code, Message: message} }

// ECMAScript's whitespace set (not Go's Unicode White_Space set).
func Space(r rune) bool {
	return r == '\t' || r == '\n' || r == '\v' || r == '\f' || r == '\r' || r == 0xfeff || r == 0x2028 || r == 0x2029 || unicode.Is(unicode.Zs, r)
}
func WhitespacePaths(root string, entries []string, label string) error {
	for _, entry := range entries {
		depth, separator := 0, strings.ContainsFunc(entry, Space)
		for _, r := range entry {
			switch r {
			case '{':
				depth++
			case '}':
				if depth > 0 {
					depth--
				}
			case ',':
				if depth == 0 {
					separator = true
				}
			}
		}
		if !separator {
			continue
		}
		st, err := os.Stat(filepath.Join(root, entry))
		if err != nil || !st.Mode().IsRegular() && !st.IsDir() {
			return refusal("usage", label+" entry "+quote(entry)+" matches no file; use spaces, not commas")
		}
	}
	return nil
}

// YAML syntax, aliases, unusual tags and non-string map keys are delegated
// before any mutation. Ordinary core YAML preserves mapping insertion order.
func ParseAcceptance(data []byte) (any, error) {
	if regexp.MustCompile(`(?m)^%`).Match(data) {
		return nil, blockops.ErrHandover
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if err == io.EOF {
			return nil, nil
		}
		return nil, blockops.ErrHandover
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, blockops.ErrHandover
	}
	var read func(*yaml.Node) (any, error)
	read = func(n *yaml.Node) (any, error) {
		if n.Anchor != "" || n.Kind == yaml.AliasNode || n.Style&yaml.TaggedStyle != 0 {
			return nil, blockops.ErrHandover
		}
		switch n.Kind {
		case yaml.DocumentNode:
			if len(n.Content) == 0 {
				return nil, nil
			}
			return read(n.Content[0])
		case yaml.SequenceNode:
			out := []any{}
			for _, c := range n.Content {
				v, err := read(c)
				if err != nil {
					return nil, err
				}
				out = append(out, v)
			}
			return out, nil
		case yaml.MappingNode:
			out := ledger.Object{}
			seen := map[string]bool{}
			for i := 0; i < len(n.Content); i += 2 {
				key := n.Content[i]
				if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || seen[key.Value] {
					return nil, blockops.ErrHandover
				}
				seen[key.Value] = true
				v, err := read(n.Content[i+1])
				if err != nil {
					return nil, err
				}
				out.Set(key.Value, v)
			}
			return out, nil
		case yaml.ScalarNode:
			switch n.Tag {
			case "!!str", "!!timestamp":
				return n.Value, nil // core schema treats dates as strings
			case "!!null":
				return nil, nil
			case "!!bool":
				return strings.EqualFold(n.Value, "true"), nil
			case "!!int", "!!float":
				// Delegate YAML's non-JSON numeric spellings rather than silently
				// inheriting yaml.v3's YAML 1.1 octal semantics.
				if !regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`).MatchString(n.Value) {
					return nil, blockops.ErrHandover
				}
				v, err := strconv.ParseFloat(n.Value, 64)
				if err != nil {
					return nil, blockops.ErrHandover
				}
				return v, nil
			}
		}
		return nil, blockops.ErrHandover
	}
	return read(&doc)
}
func Acceptance(value any) ([]any, error) {
	clauses, ok := value.([]any)
	valid := ok && len(clauses) > 0
	for _, c := range clauses {
		o := object(c)
		if text(o.Get("clause")) == "" {
			valid = false
		}
		if o.Get("kind") == "human_check" {
			for _, f := range o {
				if f.Key == "tests" {
					valid = false
				}
			}
		} else {
			tests, ok := o.Get("tests").([]any)
			if !ok || len(tests) == 0 {
				valid = false
			}
			for _, t := range tests {
				if text(t) == "" {
					valid = false
				}
			}
		}
	}
	if !valid {
		return nil, refusal("bad-acceptance", "acceptance: a non-empty list of {clause, tests: [test ids…]} or {clause, kind: human_check} without tests is required")
	}
	return clauses, nil
}
func BriefPointer(path string, data []byte) (string, error) {
	if path == "" || strings.ContainsFunc(path, Space) {
		return "", refusal("bad-brief-path", "brief path must be non-empty and contain no whitespace")
	}
	lines := bytes.Count(data, []byte{'\n'})
	if len(data) > 0 && data[len(data)-1] != '\n' {
		lines++
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	pointer := fmt.Sprintf("BRIEF %s lines=%d sha=%s <<<EOM>>>", path, lines, hash[:8])
	if len(pointer) > 200 {
		return "", refusal("pointer-too-long", fmt.Sprintf("brief pointer is %d bytes (max 200); use a shorter brief path", len(pointer)))
	}
	return pointer, nil
}
func expand(pattern string) []string {
	open := strings.IndexByte(pattern, '{')
	if open < 0 {
		return []string{pattern}
	}
	depth, close := 0, -1
	bounds := []int{open}
	for i := open; i < len(pattern); i++ {
		switch pattern[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				close = i
			}
		case ',':
			if depth == 1 {
				bounds = append(bounds, i)
			}
		}
		if close >= 0 {
			break
		}
	}
	if close < 0 {
		return []string{pattern}
	}
	bounds = append(bounds, close)
	out := []string{}
	for i := 0; i < len(bounds)-1; i++ {
		out = append(out, expand(pattern[:open]+pattern[bounds[i]+1:bounds[i+1]]+pattern[close+1:])...)
	}
	return out
}
func AssertOwned(entries []string, label, code string) error {
	if len(entries) == 0 {
		return refusal(code, label+": at least one path is required")
	}
	for _, entry := range entries {
		why := ""
		exact := owned.IsExactEntry(entry)
		if entry == "" {
			why = "must be a non-empty string"
		} else if strings.Contains(entry, `\`) {
			why = "backslashes are not allowed (POSIX paths only)"
		} else if !exact && strings.ContainsAny(entry, "[]()!+@") {
			why = "a glob may not contain [ ] ( ) ! + @ — character classes and extglobs are not supported; name the files exactly or use only *, ?, ** and {a,b}"
		} else {
			for _, alt := range expand(entry) {
				if strings.HasPrefix(alt, "/") {
					why = "must be repo-relative, not absolute"
				} else if !exact && strings.ContainsAny(alt, "{}") {
					why = "unbalanced braces"
				} else {
					for _, part := range strings.Split(alt, "/") {
						if part == "" || part == "." || part == ".." {
							why = `no ".", ".." or empty segments`
							break
						}
					}
				}
				if why != "" {
					break
				}
			}
		}
		if why != "" {
			return refusal(code, label+": invalid path "+quote(entry)+": "+why)
		}
	}
	return nil
}
func Kind(entries []string, declared string) string {
	if declared != "" {
		return declared
	}
	kind := "docs"
	for _, entry := range entries {
		lower := strings.ToLower(entry)
		one := "code"
		for _, suffix := range []string{".md", ".mdx", ".markdown", ".rst", ".adoc"} {
			if strings.HasSuffix(lower, suffix) {
				one = "docs"
			}
		}
		for _, suffix := range []string{".schema.json", ".schema.yaml", ".schema.yml", ".proto", ".graphql", ".gql", ".avsc", ".openapi.json", ".openapi.yaml", ".openapi.yml"} {
			if strings.HasSuffix(lower, suffix) {
				one = "contract"
			}
		}
		if strings.HasSuffix(lower, ".json") || strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml") {
			parts := strings.Split(lower, "/")
			for _, dir := range parts[:len(parts)-1] {
				if dir == "schema" || dir == "schemas" || dir == "contract" || dir == "contracts" {
					one = "contract"
				}
			}
		}
		if one == "code" {
			return "code"
		}
		if one == "contract" {
			kind = "contract"
		}
	}
	return kind
}

type alternative struct {
	p    string
	tree bool
}

func alternatives(entry string, kinds ledger.Object) []alternative {
	if owned.IsExactEntry(entry) {
		return []alternative{{strings.TrimRight(entry, "/"), kinds.Get(entry) != "file"}}
	}
	out := []alternative{}
	for _, p := range expand(entry) {
		out = append(out, alternative{p, false})
	}
	return out
}
func matches(file, pattern string) bool {
	return owned.OwnsFile([]any{pattern}, ledger.Object{{Key: pattern, Value: "file"}}, file)
}
func below(dir, pattern string) bool {
	f, p := strings.Split(dir, "/"), strings.Split(pattern, "/")
	for i := 0; ; i++ {
		if i < len(p) && p[i] == "**" {
			return true
		}
		if i == len(f) {
			return i < len(p)
		}
		if i == len(p) || !matches(f[i], p[i]) {
			return false
		}
	}
}
func prefix(a, b []string) bool {
	if len(a) > len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func static(p string) []string {
	out := []string{}
	for _, s := range strings.Split(p, "/") {
		if strings.ContainsAny(s, "*?") {
			break
		}
		out = append(out, s)
	}
	return out
}
func overlaps(a, b alternative) bool {
	if b.tree && !a.tree {
		return overlaps(b, a)
	}
	if a.tree {
		if b.tree {
			return a.p == b.p || strings.HasPrefix(a.p, b.p+"/") || strings.HasPrefix(b.p, a.p+"/")
		}
		if !strings.ContainsAny(b.p, "*?") {
			return a.p == b.p || strings.HasPrefix(b.p, a.p+"/")
		}
		return matches(a.p, b.p) || below(a.p, b.p)
	}
	ag, bg := strings.ContainsAny(a.p, "*?"), strings.ContainsAny(b.p, "*?")
	if !ag && !bg {
		return a.p == b.p
	}
	if !ag {
		return matches(a.p, b.p)
	}
	if !bg {
		return matches(b.p, a.p)
	}
	return prefix(static(a.p), static(b.p)) || prefix(static(b.p), static(a.p))
}
func overlap(files []any, kinds ledger.Object, tree string, other ledger.Object) (string, string) {
	if t := object(other.Get("worktree")); tree != "" && t != nil && t.Get("path") != tree {
		return "", ""
	}
	otherFiles, _ := other.Get("owned_files").([]any)
	for _, a := range files {
		for _, b := range otherFiles {
			for _, x := range alternatives(text(a), kinds) {
				for _, y := range alternatives(text(b), object(other.Get("owned_kinds"))) {
					if overlaps(x, y) {
						return text(a), text(b)
					}
				}
			}
		}
	}
	return "", ""
}
