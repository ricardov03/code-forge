package config

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The shipped schema is embedded so installed native binaries do not depend on
// a checkout. The Node golden oracle exercises the schema's types and keywords.
//
//go:embed schema.json
var schemaJSON string

type schemaEntry struct {
	key   string
	value any
}
type schemaObject []schemaEntry

func (s schemaObject) get(key string) any {
	for _, field := range s {
		if field.key == key {
			return field.value
		}
	}
	return nil
}
func object(value any) schemaObject { out, _ := value.(schemaObject); return out }
func list(value any) []any          { out, _ := value.([]any); return out }
func text(value any) string         { out, _ := value.(string); return out }

func readSchema(d *json.Decoder) any {
	token, err := d.Token()
	if err != nil {
		panic(err)
	}
	switch token {
	case json.Delim('{'):
		out := schemaObject{}
		for d.More() {
			key, _ := d.Token()
			value := readSchema(d)
			found := false
			for i := range out {
				if out[i].key == key.(string) {
					out[i].value = value
					found = true
					break
				}
			}
			if !found {
				out = append(out, schemaEntry{key.(string), value})
			}
		}
		_, _ = d.Token()
		sort.SliceStable(out, func(i, j int) bool {
			a, aOK := jsIndex(out[i].key)
			b, bOK := jsIndex(out[j].key)
			if aOK && bOK {
				return a < b
			}
			return aOK && !bOK
		})
		return out
	case json.Delim('['):
		out := []any{}
		for d.More() {
			out = append(out, readSchema(d))
		}
		_, _ = d.Token()
		return out
	default:
		if number, ok := token.(json.Number); ok {
			value, _ := strconv.ParseFloat(string(number), 64)
			return value
		}
		return token
	}
}

func jsIndex(key string) (uint64, bool) {
	index, err := strconv.ParseUint(key, 10, 32)
	return index, err == nil && index < 4294967295 && strconv.FormatUint(index, 10) == key
}

var projectSchema = object(readSchema(json.NewDecoder(strings.NewReader(schemaJSON))))

func init() { checkSchemaPatterns(projectSchema) }

// Check every embedded rule at startup, including branches no config uses yet.
func checkSchemaPatterns(value any) {
	switch node := value.(type) {
	case schemaObject:
		if pattern := text(node.get("pattern")); pattern != "" {
			patternMatches(pattern, "")
		}
		for _, entry := range object(node.get("patternProperties")) {
			patternMatches(entry.key, "")
		}
		for _, entry := range node {
			checkSchemaPatterns(entry.value)
		}
	case []any:
		for _, child := range node {
			checkSchemaPatterns(child)
		}
	}
}

// ValidateSchema returns the schema layer's refusals, in Ajv order. It does not
// run machine-dependent domain rules or mutate the config by applying defaults.
// version is the CLI package version used in Node's unknown-key diagnostic.
func ValidateSchema(config map[string]any, version string) error {
	issues := validate(projectSchema, config, "", version)
	if len(issues) == 0 {
		return nil
	}
	texts := make([]string, len(issues))
	for i, issue := range issues {
		texts[i] = issue.Text
	}
	return failure("schema", strings.Join(texts, "\n"), "")
}

// SchemaIssues is runSchemaValidation in src/config/validate.mjs: every
// schema refusal as "<pointer> <detail>", in Ajv order.
func SchemaIssues(config map[string]any, version string) []string {
	return validate(projectSchema, config, "", version)
}

// LoadValidated is the load-then-schema-check pipeline used by verbs that need
// a complete configuration. Load itself preserves loadProjectConfig semantics.
func LoadValidated(cwd, version string) (map[string]any, error) {
	config, err := Load(cwd)
	if err != nil {
		return nil, err
	}
	if err := ValidateSchema(config, version); err != nil {
		return nil, err
	}
	return config, nil
}

func safeSegment(s string) string {
	if maskSecrets(s) != s && !strings.HasPrefix(s, "env:") && !strings.HasPrefix(s, "op://") {
		return "<redacted-key>"
	}
	return s
}
func pointer(path string) string {
	if path == "" {
		return "(root)"
	}
	parts := strings.Split(path, "/")
	for i := 1; i < len(parts); i++ {
		parts[i] = safeSegment(strings.ReplaceAll(strings.ReplaceAll(parts[i], "~1", "/"), "~0", "~"))
	}
	return strings.Join(parts, "/")
}
func childPath(path, key string) string {
	return path + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
}
func same(a, b any) bool { return reflect.DeepEqual(a, b) }
func schemaValue(value any) any {
	switch v := value.(type) {
	case schemaObject:
		out := map[string]any{}
		for _, field := range v {
			out[field.key] = schemaValue(field.value)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = schemaValue(child)
		}
		return out
	default:
		return v
	}
}
func typeMatches(value any, kind string) bool {
	switch kind {
	case "null":
		return value == nil
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "number":
		n, ok := value.(float64)
		return ok && !math.IsNaN(n) && !math.IsInf(n, 0)
	case "integer":
		n, ok := value.(float64)
		return ok && !math.IsNaN(n) && !math.IsInf(n, 0) && math.Trunc(n) == n
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	}
	return true
}

func patternMatches(pattern, value string) bool {
	// The evidence path rule uses ECMAScript lookahead, which RE2 does not
	// implement. These predicates implement that rule's excluded path segments.
	const evidencePathPattern = `^(?!/)(?!.*(?:^|/)(?:[A-Za-z]:|\.\.(?:/|$)))[^\\\u0000]+$`
	if pattern == evidencePathPattern {
		if value == "" || strings.HasPrefix(value, "/") || strings.ContainsAny(value, "\\\x00") {
			return false
		}
		for _, part := range strings.Split(value, "/") {
			if part == ".." || len(part) >= 2 && part[1] == ':' && (part[0] >= 'a' && part[0] <= 'z' || part[0] >= 'A' && part[0] <= 'Z') {
				return false
			}
		}
		return true
	}
	return regexp.MustCompile(strings.ReplaceAll(pattern, `\u0000`, `\x00`)).MatchString(value)
}

// SchemaIssue is one schema refusal: Node's safe instance pointer (the
// issue's path) and its scrubbed message.
type SchemaIssue struct{ Path, Text string }

// SchemaIssues returns the schema layer's refusals with their paths, in the
// order ValidateSchema prints them (B462: proof tier names the paths).
func SchemaIssues(config map[string]any, version string) []SchemaIssue {
	return validate(projectSchema, config, "", version)
}

func validate(s schemaObject, value any, path, version string) []SchemaIssue {
	var out []SchemaIssue
	add := func(detail string) {
		out = append(out, SchemaIssue{pointer(path), maskSecrets(pointer(path) + " " + detail)})
	}
	if ref := text(s.get("$ref")); ref != "" {
		node := projectSchema
		for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			node = object(node.get(part))
		}
		out = append(out, validate(node, value, path, version)...)
	}
	if kind := text(s.get("type")); kind != "" && !typeMatches(value, kind) {
		add("must be " + kind)
	}
	if not := object(s.get("not")); not != nil && len(validate(not, value, path, version)) == 0 {
		add("must NOT be valid")
	}
	if alternatives := list(s.get("anyOf")); alternatives != nil {
		var failures []SchemaIssue
		valid := false
		for _, alternative := range alternatives {
			errors := validate(object(alternative), value, path, version)
			if len(errors) == 0 {
				valid = true
				break
			}
			failures = append(failures, errors...)
		}
		if !valid {
			out = append(out, failures...)
			add("must match a schema in anyOf")
		}
	}
	if condition := object(s.get("if")); condition != nil {
		branch, label := object(s.get("else")), "else"
		if len(validate(condition, value, path, version)) == 0 {
			branch, label = object(s.get("then")), "then"
		}
		if errors := validate(branch, value, path, version); len(errors) > 0 {
			out = append(out, errors...)
			add("must match \"" + label + "\" schema")
		}
	}
	for _, field := range s {
		if field.key == "const" && !same(value, schemaValue(field.value)) {
			add("must be equal to constant")
		}
	}
	if enums := list(s.get("enum")); enums != nil {
		found := false
		for _, candidate := range enums {
			if same(value, schemaValue(candidate)) {
				found = true
			}
		}
		if !found {
			add("must be equal to one of the allowed values")
		}
	}
	if n, ok := value.(float64); ok {
		for _, item := range []struct {
			key, operator string
			fails         func(float64, float64) bool
		}{
			{"maximum", "<=", func(a, b float64) bool { return a > b }}, {"minimum", ">=", func(a, b float64) bool { return a < b }},
			{"exclusiveMaximum", "<", func(a, b float64) bool { return a >= b }}, {"exclusiveMinimum", ">", func(a, b float64) bool { return a <= b }},
		} {
			if limit, ok := s.get(item.key).(float64); ok && item.fails(n, limit) {
				add("must be " + item.operator + " " + strconv.FormatFloat(limit, 'f', -1, 64))
			}
		}
	}
	if str, ok := value.(string); ok {
		if limit, ok := s.get("minLength").(float64); ok && utf8.RuneCountInString(str) < int(limit) {
			add(fmt.Sprintf("must NOT have fewer than %g characters", limit))
		}
		if pattern := text(s.get("pattern")); pattern != "" && !patternMatches(pattern, str) {
			add("must match pattern \"" + pattern + "\"")
		}
	}
	if array, ok := value.([]any); ok {
		if limit, ok := s.get("maxItems").(float64); ok && len(array) > int(limit) {
			add(fmt.Sprintf("must NOT have more than %g items", limit))
		}
		if limit, ok := s.get("minItems").(float64); ok && len(array) < int(limit) {
			add(fmt.Sprintf("must NOT have fewer than %g items", limit))
		}
		if s.get("uniqueItems") == true {
			duplicate := false
			for i := len(array) - 1; i >= 0 && !duplicate; i-- {
				for j := i - 1; j >= 0; j-- {
					if same(array[i], array[j]) {
						add(fmt.Sprintf("must NOT have duplicate items (items ## %d and %d are identical)", i, j))
						duplicate = true
						break
					}
				}
			}
		}
		if items := object(s.get("items")); items != nil {
			for i, item := range array {
				out = append(out, validate(items, item, childPath(path, strconv.Itoa(i)), version)...)
			}
		}
		if contains := object(s.get("contains")); contains != nil {
			found := false
			for _, item := range array {
				if len(validate(contains, item, path, version)) == 0 {
					found = true
					break
				}
			}
			if !found {
				add("must contain at least 1 valid item(s)")
			}
		}
	}
	if mapping, ok := value.(map[string]any); ok {
		keys := make([]string, 0, len(mapping))
		for key := range mapping {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, required := range list(s.get("required")) {
			if _, present := mapping[text(required)]; !present {
				add("must have required property '" + text(required) + "'")
			}
		}
		properties, patterns := object(s.get("properties")), object(s.get("patternProperties"))
		if names := object(s.get("propertyNames")); names != nil {
			for _, key := range keys {
				if len(validate(names, key, path, version)) > 0 {
					add("property name \"" + safeSegment(key) + "\" is not allowed (propertyNames)")
				}
			}
		}
		for _, key := range keys {
			known := properties.get(key) != nil
			for _, pattern := range patterns {
				if regexp.MustCompile(pattern.key).MatchString(key) {
					known = true
				}
			}
			if !known {
				if s.get("additionalProperties") == false {
					add("must NOT have additional property \"" + safeSegment(key) + "\" (unknown key; if it comes from a newer code-forge, upgrade: this is code-forge " + version + ")")
				} else if additional := object(s.get("additionalProperties")); additional != nil {
					out = append(out, validate(additional, mapping[key], childPath(path, key), version)...)
				}
			}
		}
		for _, property := range properties {
			if child, present := mapping[property.key]; present {
				out = append(out, validate(object(property.value), child, childPath(path, property.key), version)...)
			}
		}
		for _, pattern := range patterns {
			for _, key := range keys {
				if regexp.MustCompile(pattern.key).MatchString(key) {
					out = append(out, validate(object(pattern.value), mapping[key], childPath(path, key), version)...)
				}
			}
		}
		for _, dependent := range object(s.get("dependentSchemas")) {
			if _, present := mapping[dependent.key]; present {
				out = append(out, validate(object(dependent.value), value, path, version)...)
			}
		}
	}
	return out
}

// LooksLikeSecret is Node's looksLikeSecret (src/config/secret-patterns.mjs):
// a key reference (env:, op://, user) never is one.
func LooksLikeSecret(value string) bool {
	if strings.HasPrefix(value, "env:") || strings.HasPrefix(value, "op://") || value == "user" {
		return false
	}
	return maskSecrets(value) != value
}
