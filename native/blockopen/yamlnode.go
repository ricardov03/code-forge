package blockopen

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/ledger"
	"gopkg.in/yaml.v3"
)

// ErrYAMLForm: the file uses a YAML form whose Node output Go does not
// reproduce byte-equal: a directive, an explicit tag or a collection as a
// mapping key (the yaml package may print a process warning with Node's
// pid), a tab, a quoted scalar over several lines, two mapping keys with the
// same value, bytes that are not UTF-8 (Node decodes them to U+FFFD before
// parsing), or a syntax error the port has no exact text for (B581).
var ErrYAMLForm = errors.New("block open: a YAML form Go does not serve")

var yamlErrorText = regexp.MustCompile(`(?s)^[A-Z_]+: (.*?)(?: \(line [0-9]+, column [0-9]+\))?$`)

// ParseYAMLJS is Node's parseYAML(text, {prettyErrors: false}) as block open
// reads the acceptance file and the pipeline: mappings keep their property
// order, numbers are JavaScript's, a non-finite number is the null JSON
// stores. A parse error is the yaml package's message alone (B581).
func ParseYAMLJS(data []byte) (any, error) {
	if !utf8.Valid(data) || bytes.HasPrefix(data, []byte("\xef\xbb\xbf")) || bytes.IndexByte(data, '\t') >= 0 || regexp.MustCompile(`(?m)^%`).Match(data) {
		return nil, ErrYAMLForm
	}
	forms, duplicate := yamlCoreForms(data)
	if !forms {
		return nil, ErrYAMLForm
	}
	value, err := config.ParseOrdered(string(data))
	if duplicate && (err == nil || !strings.HasPrefix(err.Error(), "DUPLICATE_KEY: Map keys must be unique")) {
		return nil, ErrYAMLForm
	}
	if err != nil {
		message := err.Error()
		match := yamlErrorText.FindStringSubmatch(message)
		if match == nil || strings.HasPrefix(message, "YAML_PARSE_ERROR: Invalid YAML") || strings.Contains(message, "Excessive alias") || strings.Contains(message, "[REDACTED]") {
			return nil, ErrYAMLForm
		}
		return nil, errors.New(match[1])
	}
	return yamlJSValue(value), nil
}

// yamlCoreForms reports whether no node has an explicit tag, every mapping
// key is a scalar and unique by value, and every quoted scalar ends on its
// first line. duplicate reports two keys of one mapping with the same value.
// A file yaml.v3 cannot read is left to the parser.
func yamlCoreForms(data []byte) (ok, duplicate bool) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	lines := strings.Split(string(data), "\n")
	ok = true
	for {
		var doc yaml.Node
		if err := dec.Decode(&doc); err != nil {
			return ok, duplicate
		}
		var walk func(n *yaml.Node)
		walk = func(n *yaml.Node) {
			if n == nil || !ok {
				return
			}
			if n.Style&yaml.TaggedStyle != 0 || (n.Kind == yaml.ScalarNode && n.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle) != 0 && !yamlQuoteOneLine(lines, n)) {
				ok = false
				return
			}
			if n.Kind == yaml.MappingNode {
				seen := map[string]bool{}
				for i := 0; i < len(n.Content); i += 2 {
					key := n.Content[i]
					for key.Kind == yaml.AliasNode && key.Alias != nil {
						key = key.Alias
					}
					if key.Kind != yaml.ScalarNode {
						ok = false
						return
					}
					var value any
					if key.Decode(&value) != nil {
						ok = false
						return
					}
					switch number := value.(type) {
					case int:
						value = float64(number)
					case uint64:
						value = float64(number)
					}
					identity := fmt.Sprintf("%T:%v", value, value)
					if seen[identity] {
						duplicate = true
					}
					seen[identity] = true
				}
			}
			if n.Kind != yaml.AliasNode {
				for _, c := range n.Content {
					walk(c)
				}
			}
		}
		walk(&doc)
	}
}

// yamlQuoteOneLine reports whether the quoted scalar n closes on the line
// it opens on.
func yamlQuoteOneLine(lines []string, n *yaml.Node) bool {
	if n.Line < 1 || n.Line > len(lines) {
		return false
	}
	line := []rune(lines[n.Line-1])
	start := n.Column - 1
	if start < 0 || start >= len(line) {
		return false
	}
	quote := line[start]
	if quote != '"' && quote != '\'' {
		return false
	}
	for i := start + 1; i < len(line); i++ {
		switch {
		case quote == '"' && line[i] == '\\':
			i++
		case line[i] == quote && quote == '\'' && i+1 < len(line) && line[i+1] == '\'':
			i++
		case line[i] == quote:
			return true
		}
	}
	return false
}

func yamlJSValue(value any) any {
	switch v := value.(type) {
	case []config.Pair:
		out := ledger.Object{}
		for _, p := range v {
			out.Set(p.Key, yamlJSValue(p.Value))
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, c := range v {
			out[i] = yamlJSValue(c)
		}
		return out
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil
		}
	}
	return value
}
