package config

import (
	"encoding/base64"
	"fmt"
	"io"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/ricardov03/code-forge/native/canon"

	"gopkg.in/yaml.v3"
)

var decimal = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)
var radix = regexp.MustCompile(`^0(?:o[0-7]+|x[0-9a-fA-F]+)$`)
var secret = regexp.MustCompile(`sk-[A-Za-z0-9_-]{10,}|xai-[A-Za-z0-9]{20,}|xox[baprs]-[A-Za-z0-9-]{10,}|gh[opsu]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|AKIA[0-9A-Z]{12,}|AIza[0-9A-Za-z_-]{20,}|eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{5,}`)

func maskSecrets(text string) string {
	var out strings.Builder
	end := 0
	for _, span := range secret.FindAllStringIndex(text, -1) {
		if span[0] > 0 {
			before := text[span[0]-1]
			if before >= 'a' && before <= 'z' || before >= 'A' && before <= 'Z' || before >= '0' && before <= '9' || before == '_' || before == '-' {
				continue
			}
		}
		out.WriteString(text[end:span[0]])
		out.WriteString("[REDACTED]")
		end = span[1]
	}
	out.WriteString(text[end:])
	return out.String()
}

func yamlError(code, message string, line, column int) error {
	return fmt.Errorf("%s: %s (line %d, column %d)", code, maskSecrets(message), line, column)
}

func parseYAML(source string) (any, error) {
	var doc yaml.Node
	decoder := yaml.NewDecoder(strings.NewReader(source))
	if err := decoder.Decode(&doc); err != nil {
		if err == io.EOF {
			return nil, nil
		}
		return nil, describeSyntax(source, err)
	}
	var next yaml.Node
	if err := decoder.Decode(&next); err != io.EOF {
		line := 1
		for i, text := range strings.Split(source, "\n") {
			if i > 0 && (text == "---" || strings.HasPrefix(text, "--- ")) {
				line = i + 1
				break
			}
		}
		return nil, yamlError("MULTIPLE_DOCS", "Source contains multiple documents; please use YAML.parseAllDocuments()", line, 1)
	}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	return (&yamlReader{active: map[*yaml.Node]bool{}}).value(doc.Content[0])
}

type yamlReader struct {
	active map[*yaml.Node]bool
	count  int
}

func (r *yamlReader) value(node *yaml.Node) (any, error) {
	r.count++
	if r.count > 100000 || r.active[node] {
		return nil, fmt.Errorf("YAML_PARSE_ERROR: Excessive alias count indicates a resource exhaustion attack")
	}
	r.active[node] = true
	defer delete(r.active, node)
	switch node.Kind {
	case yaml.AliasNode:
		return r.value(node.Alias)
	case yaml.MappingNode:
		out := map[string]any{}
		seen := map[string]bool{}
		for i := 0; i < len(node.Content); i += 2 {
			keyNode := node.Content[i]
			key, err := r.value(keyNode)
			if err != nil {
				return nil, err
			}
			identity := fmt.Sprintf("%T:%v", key, key)
			if seen[identity] {
				return nil, yamlError("DUPLICATE_KEY", "Map keys must be unique", keyNode.Line, keyNode.Column)
			}
			seen[identity] = true
			value, err := r.value(node.Content[i+1])
			if err != nil {
				return nil, err
			}
			out[jsKey(key)] = value
		}
		return out, nil
	case yaml.SequenceNode:
		out := make([]any, len(node.Content))
		for i, child := range node.Content {
			value, err := r.value(child)
			if err != nil {
				return nil, err
			}
			out[i] = value
		}
		return out, nil
	default:
		return scalar(node), nil
	}
}

func scalar(n *yaml.Node) any {
	text := n.Value
	explicit := n.Style&yaml.TaggedStyle != 0
	if explicit {
		switch n.Tag {
		case "!!timestamp":
			// canonicalJSON visits enumerable own properties; JS Dates have none.
			return map[string]any{}
		case "!!binary":
			data, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(text), ""))
			if err == nil {
				items := make([]any, len(data))
				for i, b := range data {
					items[i] = float64(b)
				}
				out := map[string]any{}
				for i, b := range items {
					out[strconv.Itoa(i)] = b
				}
				return out
			}
		case "!!str":
			return text
		case "!!null", "!!bool", "!!int", "!!float":
		default:
			return text
		}
	} else if n.Style&(yaml.SingleQuotedStyle|yaml.DoubleQuotedStyle|yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return text
	}
	switch text {
	case "", "~", "null", "Null", "NULL":
		return nil
	case "true", "True", "TRUE":
		return true
	case "false", "False", "FALSE":
		return false
	case ".nan", ".NaN", ".NAN":
		return math.NaN()
	case ".inf", ".Inf", ".INF", "+.inf", "+.Inf", "+.INF":
		return math.Inf(1)
	case "-.inf", "-.Inf", "-.INF":
		return math.Inf(-1)
	}
	if radix.MatchString(text) {
		base := 16
		if text[1] == 'o' {
			base = 8
		}
		// Parse through a big integer so YAML integers have JS's float64 rounding.
		integer, _ := new(big.Int).SetString(text[2:], base)
		value, _ := new(big.Float).SetInt(integer).Float64()
		return value
	}
	if decimal.MatchString(text) {
		value, _ := strconv.ParseFloat(text, 64)
		return value
	}
	return text
}

func jsKey(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case nil:
		return ""
	case float64:
		if v == 0 {
			return "0"
		}
		if math.IsNaN(v) {
			return "NaN"
		}
		if math.IsInf(v, 0) {
			if v < 0 {
				return "-Infinity"
			}
			return "Infinity"
		}
		data, _ := canon.Marshal(v)
		return string(data)
	default:
		return fmt.Sprint(value)
	}
}

// Non-finite YAML numbers stringify to null in Node's canonical JSON.
func jsonNumbers(value any) any {
	switch v := value.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil
		}
	case map[string]any:
		for key, child := range v {
			v[key] = jsonNumbers(child)
		}
	case []any:
		for i, child := range v {
			v[i] = jsonNumbers(child)
		}
	}
	return value
}

func describeSyntax(source string, err error) error {
	message := err.Error()
	if at := strings.Index(message, "unknown anchor '"); at >= 0 {
		name := strings.TrimPrefix(message[at:], "unknown anchor '")
		name = strings.TrimSuffix(name, "' referenced")
		return fmt.Errorf("YAML_PARSE_ERROR: Unresolved alias (the anchor must be set before the alias): %s", maskSecrets(name))
	}
	lines := strings.Split(source, "\n")
	for i, line := range lines {
		for j, c := range line {
			if c == '\t' {
				return yamlError("TAB_AS_INDENT", "Tabs are not allowed as indentation", i+1, j+1)
			}
			if c != ' ' {
				break
			}
		}
	}
	// Track flow delimiters outside comments and quoted strings to report the
	// same missing terminator and UTF-16 location as yaml's core-schema parser.
	var stack []rune
	quote, escaped, comment := rune(0), false, false
	line, column := 1, 1
	for _, c := range source {
		if quote != 0 {
			if escaped {
				escaped = false
			} else if c == '\\' && quote == '"' {
				escaped = true
			} else if c == quote {
				quote = 0
			}
		} else if !comment {
			switch c {
			case '#':
				comment = true
			case '\'', '"':
				quote = c
			case '[', '{':
				stack = append(stack, c)
			case ']', '}':
				if len(stack) > 0 {
					open := stack[len(stack)-1]
					if open == '[' && c != ']' || open == '{' && c != '}' {
						return flowError(open, line, column)
					}
					stack = stack[:len(stack)-1]
				}
			}
		}
		if c == '\n' {
			line, column, comment = line+1, 1, false
		} else {
			column += len(utf16.Encode([]rune{c}))
		}
	}
	if len(stack) > 0 {
		return flowError(stack[len(stack)-1], line, column)
	}
	return yamlError("YAML_PARSE_ERROR", "Invalid YAML", line, column)
}

func flowError(open rune, line, column int) error {
	kind, close := "sequence", "]"
	if open == '{' {
		kind, close = "map", "}"
	}
	return yamlError("BAD_INDENT", "Flow "+kind+" in block collection must be sufficiently indented and end with a "+close, line, column)
}
