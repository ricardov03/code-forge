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
var integer = regexp.MustCompile(`^[+-]?[0-9]+$`)
var timestamp = regexp.MustCompile(`^[0-9]{4}-[0-9]{1,2}-[0-9]{1,2}(?:(?:t|T|[ \t]+)[0-9]{1,2}:[0-9]{1,2}:[0-9]{1,2}(\.[0-9]+)?(?:[ \t]*(Z|[-+][012]?[0-9](?::[0-9]{2})?))?)?$`)
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
	return (&yamlReader{active: map[*yaml.Node]bool{}, anchors: map[*yaml.Node]*yamlAnchor{}, source: source}).value(doc.Content[0])
}

type yamlAnchor struct {
	value  any
	count  int
	weight int
}

type yamlReader struct {
	active  map[*yaml.Node]bool
	anchors map[*yaml.Node]*yamlAnchor
	source  string
}

func (r *yamlReader) value(node *yaml.Node) (any, error) {
	if node.Kind == yaml.AliasNode {
		anchor := r.anchors[node.Alias]
		if anchor == nil {
			return nil, fmt.Errorf("YAML_PARSE_ERROR: Excessive alias count indicates a resource exhaustion attack")
		}
		anchor.count++
		if anchor.weight == 0 {
			anchor.weight = r.aliasWeight(node.Alias)
		}
		if anchor.count*anchor.weight > 100 {
			return nil, fmt.Errorf("YAML_PARSE_ERROR: Excessive alias count indicates a resource exhaustion attack")
		}
		return anchor.value, nil
	}
	if r.active[node] {
		return nil, fmt.Errorf("YAML_PARSE_ERROR: Excessive alias count indicates a resource exhaustion attack")
	}
	r.active[node] = true
	defer delete(r.active, node)
	value, err := r.readValue(node)
	if err == nil && node.Anchor != "" {
		r.anchors[node] = &yamlAnchor{value: value, count: 1}
	}
	return value, err
}

func (r *yamlReader) aliasWeight(node *yaml.Node) int {
	if node.Kind == yaml.AliasNode {
		if anchor := r.anchors[node.Alias]; anchor != nil {
			return anchor.count * anchor.weight
		}
		return 0
	}
	if node.Kind == yaml.MappingNode || node.Kind == yaml.SequenceNode {
		maximum := 0
		for _, child := range node.Content {
			if count := r.aliasWeight(child); count > maximum {
				maximum = count
			}
		}
		return maximum
	}
	return 1
}

func (r *yamlReader) readValue(node *yaml.Node) (any, error) {
	switch node.Kind {
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
				column := keyNode.Column
				lines := strings.Split(r.source, "\n")
				if keyNode.Line <= len(lines) {
					runes := []rune(lines[keyNode.Line-1])
					if column-1 <= len(runes) {
						column = 1 + len(utf16.Encode(runes[:column-1]))
					}
				}
				return nil, yamlError("DUPLICATE_KEY", "Map keys must be unique", keyNode.Line, column)
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
		if node.Style&yaml.TaggedStyle != 0 && node.Tag == "!!timestamp" && !timestamp.MatchString(node.Value) {
			return nil, yamlError("TAG_RESOLVE_FAILED", "!!timestamp expects a date, starting with yyyy-mm-dd", node.Line, node.Column)
		}
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
			var encoded strings.Builder
			for _, c := range text {
				if c == '=' {
					break
				}
				if c == '-' {
					c = '+'
				}
				if c == '_' {
					c = '/'
				}
				if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '+' || c == '/' {
					encoded.WriteRune(c)
				}
			}
			base64Text := encoded.String()
			if len(base64Text)%4 == 1 {
				base64Text = base64Text[:len(base64Text)-1]
			}
			data, _ := base64.RawStdEncoding.DecodeString(base64Text)
			out := map[string]any{}
			for i, b := range data {
				out[strconv.Itoa(i)] = float64(b)
			}
			return out
		case "!!str":
			return text
		case "!!null":
			if text != "" && text != "~" && text != "null" && text != "Null" && text != "NULL" {
				return text
			}
		case "!!bool":
			if text != "true" && text != "True" && text != "TRUE" && text != "false" && text != "False" && text != "FALSE" {
				return text
			}
		case "!!int":
			if !integer.MatchString(text) && !radix.MatchString(text) {
				return text
			}
		case "!!float":
			if !decimal.MatchString(text) && !strings.Contains(strings.ToLower(text), ".inf") && strings.ToLower(text) != ".nan" || integer.MatchString(text) {
				return text
			}
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
	escapeLine, escapeColumn := 0, 0
	line, column := 1, 1
	for offset, c := range source {
		if quote != 0 {
			if escaped {
				if !strings.ContainsRune("0abtnvfre \"/\\N_LPuxU\n\r", c) {
					return yamlError("BAD_DQ_ESCAPE", "Invalid escape sequence \\"+string(c), escapeLine, escapeColumn)
				}
				escaped = false
			} else if c == '\\' && quote == '"' {
				escaped = true
				escapeLine, escapeColumn = line, column
			} else if c == quote {
				quote = 0
			}
		} else if !comment {
			switch c {
			case '#':
				comment = true
			case '\'', '"':
				quote = c
			case '@', '`':
				if offset == 0 || source[offset-1] == ' ' || source[offset-1] == ':' {
					return yamlError("BAD_SCALAR_START", "Plain value cannot start with reserved character "+string(c), line, column)
				}
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
	if quote != 0 {
		return yamlError("MISSING_CHAR", "Missing closing "+string(quote)+"quote", line, column)
	}
	if len(stack) > 0 {
		return flowError(stack[len(stack)-1], line, column)
	}
	if strings.Contains(message, "expected ':'") {
		for i, line := range lines {
			trim := strings.TrimSpace(line)
			if trim != "" && !strings.Contains(trim, ":") && !strings.HasPrefix(trim, "#") && trim != "---" && trim != "..." {
				return yamlError("MISSING_CHAR", "Implicit map keys need to be followed by map values", i+1, len(line)-len(strings.TrimLeft(line, " \t"))+1)
			}
		}
	}
	if strings.Contains(message, "mapping values are not allowed") {
		match := regexp.MustCompile(`line ([0-9]+):`).FindStringSubmatch(message)
		if len(match) > 1 {
			n, _ := strconv.Atoi(match[1])
			index := n - 1
			if index > 0 {
				index--
			}
			if index < len(lines) {
				lineText := lines[index]
				if colon := strings.Index(lineText, ":"); colon >= 0 {
					value := colon + 1
					for value < len(lineText) && lineText[value] == ' ' {
						value++
					}
					return yamlError("BLOCK_AS_IMPLICIT_KEY", "Nested mappings are not allowed in compact mappings", index+1, 1+len(utf16.Encode([]rune(lineText[:value]))))
				}
			}
		}
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
