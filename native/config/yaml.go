package config

import (
	"encoding/base64"
	"fmt"
	"io"
	"math"
	"math/big"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/ricardov03/code-forge/native/canon"

	"gopkg.in/yaml.v3"
)

var decimal = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)
var radix = regexp.MustCompile(`^0(?:o[0-7]+|x[0-9a-fA-F]+)$`)
var integer = regexp.MustCompile(`^[+-]?[0-9]+$`)
var timestamp = regexp.MustCompile(`^([0-9]{4})-([0-9]{1,2})-([0-9]{1,2})(?:(?:t|T|[ \t]+)([0-9]{1,2}):([0-9]{1,2}):([0-9]{1,2})(\.[0-9]+)?(?:[ \t]*(Z|[-+][012]?[0-9](?::[0-9]{2})?))?)?$`)
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
	value, _, err := parseYAMLOrdered(source)
	return value, err
}

// parseYAMLOrdered also records each mapping's key insertion order, keyed by
// the map's identity, so callers can rebuild JavaScript property order.
func parseYAMLOrdered(source string) (any, map[uintptr][]string, error) {
	order := map[uintptr][]string{}
	var doc yaml.Node
	decoder := yaml.NewDecoder(strings.NewReader(source))
	if err := decoder.Decode(&doc); err != nil {
		if err == io.EOF {
			return nil, order, nil
		}
		return nil, order, describeSyntax(source, err)
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
		return nil, order, yamlError("MULTIPLE_DOCS", "Source contains multiple documents; please use YAML.parseAllDocuments()", line, 1)
	}
	if len(doc.Content) == 0 {
		return nil, order, nil
	}
	value, err := (&yamlReader{active: map[*yaml.Node]bool{}, anchors: map[*yaml.Node]*yamlAnchor{}, source: source, order: order}).value(doc.Content[0])
	return value, order, err
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
	order   map[uintptr][]string
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
			name := jsKey(key)
			if _, exists := out[name]; !exists && r.order != nil {
				r.order[reflect.ValueOf(out).Pointer()] = append(r.order[reflect.ValueOf(out).Pointer()], name)
			}
			out[name] = value
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
			return timestampISO(text)
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

func timestampISO(text string) string {
	parts := timestamp.FindStringSubmatch(text)
	number := func(index int) int { n, _ := strconv.Atoi(parts[index]); return n }
	year := number(1)
	// Date.UTC interprets years 0..99 as 1900..1999 and normalizes overflow.
	if year < 100 {
		year += 1900
	}
	millis := 0
	if parts[7] != "" {
		millis, _ = strconv.Atoi((parts[7][1:] + "00")[:3])
	}
	date := time.Date(year, time.Month(number(2)), number(3), number(4), number(5), number(6), millis*int(time.Millisecond), time.UTC)
	if zone := parts[8]; zone != "" && zone != "Z" {
		minutes := 0
		for _, part := range strings.Split(zone[1:], ":") {
			n, _ := strconv.Atoi(part)
			minutes = minutes*60 + n
		}
		// Match yaml's sexagesimal offset resolver, including small offsets.
		if minutes < 30 {
			minutes *= 60
		}
		if zone[0] == '-' {
			minutes = -minutes
		}
		date = date.Add(-time.Duration(minutes) * time.Minute)
	}
	isoYear := fmt.Sprintf("%04d", date.Year())
	if date.Year() > 9999 || date.Year() < 0 {
		isoYear = fmt.Sprintf("%+07d", date.Year())
	}
	return isoYear + date.Format("-01-02T15:04:05.000Z")
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
	reportedLine := 1
	if match := syntaxLine.FindStringSubmatch(message); len(match) > 1 {
		reportedLine, _ = strconv.Atoi(match[1])
	}
	// yaml.v3 may report a collection's context mark using a zero-based line.
	// Read the whole prefix for lexical context, but diagnose only the reported
	// line (or the collection starting on the following line), never a file-wide
	// first match. Errors without a line originate on the first line.
	near := func(line int) bool { return line == reportedLine || line == reportedLine+1 }
	flowFailure := strings.Contains(message, "expected ',' or")
	tokenFailure := strings.Contains(message, "cannot start any token")
	escapeFailure := strings.Contains(message, "unknown escape character")
	// Track flow delimiters outside comments and quoted strings to report the
	// same missing terminator and UTF-16 location as yaml's core-schema parser.
	type delimiter struct {
		char rune
		line int
	}
	var stack []delimiter
	quote, escaped, doubledQuote := rune(0), false, false
	quoteLine := 0
	escapeLine, escapeColumn := 0, 0
	line, column := 1, 1
	blockIndent := -1
	for index, lineText := range lines {
		line, column = index+1, 1
		indent := len(lineText) - len(strings.TrimLeft(lineText, " "))
		if blockIndent >= 0 {
			if strings.TrimSpace(lineText) == "" || indent > blockIndent {
				column += len(utf16.Encode([]rune(lineText)))
				continue
			}
			blockIndent = -1
		}
		// A newline does not open a new scalar inside a quoted value.
		scalarStart, comment := quote == 0, false
		for offset, c := range lineText {
			next := byte(0)
			if offset+1 < len(lineText) {
				next = lineText[offset+1]
			}
			if quote != 0 {
				if doubledQuote {
					doubledQuote = false
				} else if escaped {
					if escapeFailure && (near(line) || near(quoteLine)) && !strings.ContainsRune("0abtnvfre \"/\\N_LPuxU\n\r", c) {
						return yamlError("BAD_DQ_ESCAPE", "Invalid escape sequence \\"+string(c), escapeLine, escapeColumn)
					}
					escaped = false
				} else if c == '\\' && quote == '"' {
					escaped = true
					escapeLine, escapeColumn = line, column
				} else if c == quote {
					if quote == '\'' && next == '\'' {
						doubledQuote = true
					} else {
						quote = 0
					}
				}
			} else if !comment {
				switch c {
				case ' ', '\t', '\r':
					if c == '\t' && line == reportedLine && (tokenFailure || strings.Contains(message, "tab character")) && offset <= len(lineText)-len(strings.TrimLeft(lineText, " \t")) {
						return yamlError("TAB_AS_INDENT", "Tabs are not allowed as indentation", line, column)
					}
				case '#':
					comment = offset == 0 || lineText[offset-1] == ' ' || lineText[offset-1] == '\t'
					if !comment {
						scalarStart = false
					}
				case '\'', '"':
					if scalarStart {
						quote, quoteLine = c, line
					}
					scalarStart = false
				case '@', '`':
					if scalarStart && tokenFailure && near(line) {
						return yamlError("BAD_SCALAR_START", "Plain value cannot start with reserved character "+string(c), line, column)
					}
					scalarStart = false
				case '[', '{':
					if scalarStart || len(stack) > 0 {
						stack = append(stack, delimiter{c, line})
						scalarStart = true
					}
				case ']', '}':
					if len(stack) > 0 {
						open := stack[len(stack)-1]
						if open.char == '[' && c != ']' || open.char == '{' && c != '}' {
							if flowFailure && near(open.line) {
								return flowError(open.char, line, column)
							}
						}
						stack = stack[:len(stack)-1]
					}
					scalarStart = false
				case ':':
					if next == 0 || next == ' ' || next == '\t' || len(stack) > 0 && strings.ContainsRune("[]{},", rune(next)) {
						scalarStart = true
					}
				case ',':
					scalarStart = len(stack) > 0
				case '-':
					scalarStart = scalarStart && (next == ' ' || next == '\t')
				case '|', '>':
					if scalarStart && len(stack) == 0 {
						blockIndent, comment = indent, true
					}
					scalarStart = false
				default:
					scalarStart = false
				}
			}
			column += len(utf16.Encode([]rune{c}))
		}
		// A backslash followed by a newline is a valid double-quote escape.
		escaped = false
	}
	if quote != 0 && strings.Contains(message, "unexpected end of stream") && (near(quoteLine) || near(line)) {
		return yamlError("MISSING_CHAR", "Missing closing "+string(quote)+"quote", line, column)
	}
	if len(stack) > 0 && flowFailure && near(stack[len(stack)-1].line) {
		return flowError(stack[len(stack)-1].char, line, column)
	}
	if strings.Contains(message, "expected ':'") {
		if reportedLine <= len(lines) {
			text := lines[reportedLine-1]
			column := 1 + len(text) - len(strings.TrimLeft(text, " \t"))
			for _, following := range lines[reportedLine:] {
				if trim := strings.TrimSpace(following); trim != "" && !strings.HasPrefix(trim, "#") {
					return yamlError("MULTILINE_IMPLICIT_KEY", "Implicit keys need to be on a single line", reportedLine, column)
				}
			}
			return yamlError("MISSING_CHAR", "Implicit map keys need to be followed by map values", reportedLine, column)
		}
	}
	if strings.Contains(message, "mapping values are not allowed") {
		if reportedLine <= len(lines) {
			index := reportedLine - 1
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
	return yamlError("YAML_PARSE_ERROR", "Invalid YAML", reportedLine, 1)
}

var syntaxLine = regexp.MustCompile(`line ([0-9]+):`)

func flowError(open rune, line, column int) error {
	kind, close := "sequence", "]"
	if open == '{' {
		kind, close = "map", "}"
	}
	return yamlError("BAD_INDENT", "Flow "+kind+" in block collection must be sufficiently indented and end with a "+close, line, column)
}

// ParseYAML is the yaml package's parse() (Node's loadProjectConfig read):
// the document's JavaScript value, non-finite numbers kept as they are. A
// nil value is an empty document.
func ParseYAML(source string) (any, error) {
	return parseYAML(source)
}
