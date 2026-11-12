package session

// B551 (B551r: the coder build too): the openai (Codex) provider of spawn.mjs: the closed-book build of
// src/engines/builders/codex.mjs (run only with the
// review.allow_open_book_codex opt-in, never for the delegate), the strict
// schema compilation of src/config/schema-compile.mjs for openai, the -o
// answer file, the JSONL event read of readAnswer('codex', …) and the strict
// validation and null-dropping of runForeground. The coder build (CODEX_HOME
// with the execpolicy rules) serves the doctor's probes.

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/review"
)

// codexEfforts is efforts.mjs PROVIDER_EFFORTS.openai.
var codexEfforts = []string{"minimal", "low", "medium", "high"}

// codexEffortOK is assertEffortForProvider('buildCodexArgv', 'openai', effort)
// after spawn's `if (step.effort)`: "" (none) or one of codexEfforts.
func codexEffortOK(effort string) bool { return effort == "" || contains(codexEfforts, effort) }

// codexDeepClone is structuredClone for decoded JSON values.
func codexDeepClone(v any) any {
	switch x := v.(type) {
	case ledger.Object:
		out := make(ledger.Object, len(x))
		for i, f := range x {
			out[i] = ledger.Field{Key: f.Key, Value: codexDeepClone(f.Value)}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = codexDeepClone(e)
		}
		return out
	}
	return v
}

func codexHas(o ledger.Object, key string) bool {
	for _, f := range o {
		if f.Key == key {
			return true
		}
	}
	return false
}

// codexAcceptsNull is schema-compile.mjs schemaAcceptsNull.
func codexAcceptsNull(schema any) bool {
	if schema == true {
		return true
	}
	node, ok := schema.(ledger.Object)
	if !ok {
		return false
	}
	if codexHas(node, "type") {
		types, isList := node.Get("type").([]any)
		if !isList {
			types = []any{node.Get("type")}
		}
		hasNull := false
		for _, t := range types {
			hasNull = hasNull || t == "null"
		}
		if !hasNull {
			return false
		}
	}
	if codexHas(node, "const") && node.Get("const") != nil {
		return false
	}
	if list, isList := node.Get("enum").([]any); isList {
		hasNull := false
		for _, e := range list {
			hasNull = hasNull || e == nil
		}
		if !hasNull {
			return false
		}
	}
	for _, k := range []string{"$ref", "not", "if", "then", "else"} {
		if codexHas(node, k) {
			return false
		}
	}
	if list, isList := node.Get("anyOf").([]any); isList {
		some := false
		for _, s := range list {
			some = some || codexAcceptsNull(s)
		}
		if !some {
			return false
		}
	}
	if list, isList := node.Get("oneOf").([]any); isList {
		n := 0
		for _, s := range list {
			if codexAcceptsNull(s) {
				n++
			}
		}
		if n != 1 {
			return false
		}
	}
	if list, isList := node.Get("allOf").([]any); isList {
		for _, s := range list {
			if !codexAcceptsNull(s) {
				return false
			}
		}
	}
	return true
}

func codexObjectTyped(node ledger.Object) bool {
	if node.Get("type") == "object" {
		return true
	}
	if list, ok := node.Get("type").([]any); ok {
		for _, t := range list {
			if t == "object" {
				return true
			}
		}
	}
	return false
}

// codexChildren is schema-compile.mjs childSchemas: (pointer suffix, node).
type codexChild struct {
	suffix string
	node   ledger.Object
}

func codexChildren(node ledger.Object) []codexChild {
	var out []codexChild
	push := func(suffix string, v any) {
		if o, ok := v.(ledger.Object); ok {
			out = append(out, codexChild{suffix, o})
		}
	}
	if props, ok := node.Get("properties").(ledger.Object); ok {
		for _, f := range props {
			push("/properties/"+f.Key, f.Value)
		}
	}
	for _, k := range []string{"anyOf", "oneOf", "allOf", "prefixItems"} {
		if list, ok := node.Get(k).([]any); ok {
			for i, c := range list {
				push("/"+k+"/"+jsNum(float64(i)), c)
			}
		}
	}
	for _, k := range []string{"items", "additionalProperties", "not", "if", "then", "else"} {
		push("/"+k, node.Get(k))
	}
	for _, k := range []string{"patternProperties", "$defs", "definitions"} {
		if m, ok := node.Get(k).(ledger.Object); ok {
			for _, f := range m {
				push("/"+k+"/"+f.Key, f.Value)
			}
		}
	}
	return out
}

// codexCompileNode is compileForOpenAI on one node, in place; the node is
// returned because a nullable wrap rewrites a property's value.
func codexCompileNode(node ledger.Object) ledger.Object {
	if props, ok := node.Get("properties").(ledger.Object); ok {
		was := map[string]bool{}
		if list, isList := node.Get("required").([]any); isList {
			for _, k := range list {
				if s, isString := k.(string); isString {
					was[s] = true
				}
			}
		}
		keys := make([]any, len(props))
		for i, f := range props {
			keys[i] = f.Key
		}
		node.Set("required", keys)
		if !codexHas(node, "additionalProperties") {
			node.Set("additionalProperties", false)
		}
		for i, f := range props {
			if was[f.Key] {
				continue
			}
			if p, isObj := f.Value.(ledger.Object); isObj && !codexAcceptsNull(p) {
				props[i].Value = ledger.Object{{Key: "anyOf", Value: []any{p, ledger.Object{{Key: "type", Value: "null"}}}}}
			}
		}
		node.Set("properties", props)
	} else if codexObjectTyped(node) && !codexHas(node, "additionalProperties") && !codexHas(node, "patternProperties") {
		node.Set("properties", ledger.Object{})
		node.Set("required", []any{})
		node.Set("additionalProperties", false)
	}
	codexEachChild(node, codexCompileNode)
	return node
}

// codexEachChild rewrites every child node of node through fn, in place.
func codexEachChild(node ledger.Object, fn func(ledger.Object) ledger.Object) {
	if props, ok := node.Get("properties").(ledger.Object); ok {
		for i, f := range props {
			if o, isObj := f.Value.(ledger.Object); isObj {
				props[i].Value = fn(o)
			}
		}
	}
	for _, k := range []string{"anyOf", "oneOf", "allOf", "prefixItems"} {
		if list, ok := node.Get(k).([]any); ok {
			for i, c := range list {
				if o, isObj := c.(ledger.Object); isObj {
					list[i] = fn(o)
				}
			}
		}
	}
	for _, k := range []string{"items", "additionalProperties", "not", "if", "then", "else"} {
		if o, ok := node.Get(k).(ledger.Object); ok {
			node.Set(k, fn(o))
		}
	}
	for _, k := range []string{"patternProperties", "$defs", "definitions"} {
		if m, ok := node.Get(k).(ledger.Object); ok {
			for i, f := range m {
				if o, isObj := f.Value.(ledger.Object); isObj {
					m[i].Value = fn(o)
				}
			}
		}
	}
}

// codexStrictViolations is openAIStrictViolations.
func codexStrictViolations(node ledger.Object, pointer string, out *[]string) {
	_, hasProps := node.Get("properties").(ledger.Object)
	if (codexObjectTyped(node) || hasProps) && node.Get("additionalProperties") != false {
		*out = append(*out, pointer)
	}
	for _, c := range codexChildren(node) {
		codexStrictViolations(c.node, pointer+c.suffix, out)
	}
}

// CodexStrictSchema is compileSchema(schema, 'openai', {strict: true}): the
// compiled copy, or an error where Node throws (an open-ended object node).
func CodexStrictSchema(schema ledger.Object) (ledger.Object, error) {
	compiled := codexCompileNode(codexDeepClone(schema).(ledger.Object))
	var bad []string
	codexStrictViolations(compiled, "#", &bad)
	if len(bad) > 0 {
		return nil, errors.New("schema cannot be made OpenAI-strict at: " + strings.Join(bad, ", ") + " (open-ended object: additionalProperties is not false)")
	}
	return compiled, nil
}

// codexSchemaPlan decides, before anything runs, how an openai step checks
// its answer: "finding" (finding.schema.json: strict presence + the review
// validator on the source shape), "same" (a Validate schema the strict
// compilation leaves unchanged, so the source check is the strict check) or
// an error for every other schema (not ported).
func codexSchemaPlan(o Opts) (string, error) {
	if o.Schema == nil {
		return "", nil
	}
	compiled, err := CodexStrictSchema(o.Schema)
	if err != nil {
		return "", unsupported("a schema Node cannot make OpenAI-strict")
	}
	if o.Validate == nil && o.Schema.Get("$id") == FindingSchemaID {
		return "finding", nil
	}
	a, err1 := ledger.Stringify(o.Schema, false)
	b, err2 := ledger.Stringify(compiled, false)
	if err1 != nil || err2 != nil || !bytes.Equal(a, b) {
		return "", unsupported("a Codex session whose schema strict mode changes")
	}
	return "same", nil
}

// codexDropNulls is spawn.mjs dropStrictNulls(structuredClone(value), schema).
func codexDropNulls(value any, schema ledger.Object) any {
	return codexDropNullsRec(codexDeepClone(value), schema)
}

// codexDropNullsRec rebuilds rather than mutates, so nested objects that lose
// a field are replaced in their parent.
func codexDropNullsRec(value any, node any) any {
	schema, ok := node.(ledger.Object)
	if !ok || value == nil {
		return value
	}
	if list, isList := value.([]any); isList {
		if items, isObj := schema.Get("items").(ledger.Object); isObj {
			for i, item := range list {
				list[i] = codexDropNullsRec(item, items)
			}
		}
		return list
	}
	obj, isObj := value.(ledger.Object)
	if !isObj {
		return value
	}
	props, ok := schema.Get("properties").(ledger.Object)
	if !ok {
		return obj
	}
	required := map[string]bool{}
	if list, isList := schema.Get("required").([]any); isList {
		for _, k := range list {
			if s, isString := k.(string); isString {
				required[s] = true
			}
		}
	}
	out := ledger.Object{}
	for _, f := range obj {
		if !codexHas(props, f.Key) {
			out = append(out, f)
			continue
		}
		if f.Value == nil && !required[f.Key] {
			continue
		}
		out = append(out, ledger.Field{Key: f.Key, Value: codexDropNullsRec(f.Value, props.Get(f.Key))})
	}
	return out
}

// codexAllKeys is the part of the strict schema the source schema does not
// say: every property of every object node is present (optional ones may
// be null). Walked along properties and items, as dropStrictNulls is.
func codexAllKeys(value any, node ledger.Object) bool {
	if list, isList := value.([]any); isList {
		if items, ok := node.Get("items").(ledger.Object); ok {
			for _, item := range list {
				if !codexAllKeys(item, items) {
					return false
				}
			}
		}
		return true
	}
	obj, isObj := value.(ledger.Object)
	props, hasProps := node.Get("properties").(ledger.Object)
	if !isObj || !hasProps {
		return true
	}
	for _, p := range props {
		if !codexHas(obj, p.Key) {
			return false
		}
		v := obj.Get(p.Key)
		if v == nil {
			continue
		}
		if child, ok := p.Value.(ledger.Object); ok && !codexAllKeys(v, child) {
			return false
		}
	}
	return true
}

// codexFindingValid is matchesSchema(compiled finding schema, value): every
// node of finding.schema.json already says additionalProperties false and
// none uses anyOf/oneOf/allOf, so the strict schema accepts exactly the
// values with every key present whose null-dropped source shape the review
// validator accepts.
func codexFindingValid(value any, schema ledger.Object) bool {
	if _, ok := value.(ledger.Object); !ok {
		return false
	}
	return codexAllKeys(value, schema) && review.ValidAnswer(codexDropNulls(value, schema))
}

// codexClosedBook is buildStep + buildClosedBookArgv for an openai step: the
// strict schema in <session>/schema.json (0600), the answer in
// <session>/out.json, the packet on stdin.
func codexClosedBook(o Opts, model, effort, cwd, sessionDir string, schema ledger.Object) (built, error) {
	argv := []string{"codex", "exec", "-m", model}
	if effort != "" {
		argv = append(argv, "-c", "model_reasoning_effort="+effort)
	}
	argv = append(argv, "-s", "read-only", "--ephemeral", "--ignore-rules", "--ignore-user-config", "-C", cwd, "--skip-git-repo-check")
	if schema != nil {
		compiled, err := CodexStrictSchema(schema)
		if err != nil {
			return built{}, err
		}
		text, err := ledger.Stringify(compiled, false)
		if err != nil {
			return built{}, err
		}
		schemaPath := filepath.Join(sessionDir, "schema.json")
		if err := os.WriteFile(schemaPath, text, 0o600); err != nil {
			return built{}, err
		}
		argv = append(argv, "--output-schema", schemaPath)
	}
	outPath := filepath.Join(sessionDir, "out.json")
	argv = append(argv, "-o", outPath, "--json", "-")
	return built{cli: "codex", argv: argv, cwd: cwd, stdin: true, stdinFile: o.PromptPath, outPath: outPath}, nil
}

// codexCoder is buildStep + buildCoderArgv for a coder step on openai (no
// coder.sandbox: plan leaves that to Node): the default sandbox profile plus
// a linked worktree's git dirs, the -o answer in <session>/out.json, the
// brief as the last argv token, and a fresh per-session CODEX_HOME under
// <runRoot>/codex-homes with the execpolicy rules (and the auth copy) as
// the child's CODEX_HOME. A foreground session removes the home after the
// run; a background one keeps it until the run root is swept.
func codexCoder(o Opts, model, effort, cwd, promptPath, sessionDir string) (built, error) {
	outPath := filepath.Join(sessionDir, "out.json")
	if _, count := codexRulesFile(forbiddenMerged()); count == 0 {
		return built{}, errors.New("buildCodexArgv: coder role requires a non-empty forbidden-list render (got 0 usable patterns)")
	}
	runRoot := o.RunRoot
	home := filepath.Join(runRoot, "codex-homes", "coder-"+randomHexN(6))
	if !filepath.IsAbs(home) {
		return built{}, errors.New("buildCodexArgv: codexHome must be an absolute path")
	}
	if inside, err := codexIsInside(home, cwd); err != nil {
		return built{}, err
	} else if inside {
		return built{}, errors.New("buildCodexArgv: the session CODEX_HOME must not be inside the coder cwd (the sandbox can write there)")
	}
	environ := os.Environ() // the builder reads process.env, never the session's env
	argv := []string{"codex", "exec", "-m", model}
	if effort != "" {
		argv = append(argv, "-c", "model_reasoning_effort="+effort)
	}
	argv = append(argv, "-s", "workspace-write")
	seen := map[string]bool{}
	for _, d := range codexWorktreeGitDirs(cwd, environ) {
		if !seen[d] {
			seen[d] = true
			argv = append(argv, "--add-dir", d)
		}
	}
	argv = append(argv, codexSandboxTmpExclusions...)
	argv = append(argv, "-c", `approval_policy="never"`, "-C", cwd, "--json", "-o", outPath, promptPath)
	if err := codexPrepareHome(home, runRoot, environ); err != nil {
		return built{}, err
	}
	return built{cli: "codex", argv: argv, cwd: cwd, outPath: outPath, env: []string{"CODEX_HOME=" + home}, codexHome: home, codexRunRoot: runRoot}, nil
}

// codexReadOut is runForeground's `-o` read: the text (nil = no file), the
// file removed after.
func codexReadOut(outPath string) *string {
	var out *string
	if data, err := os.ReadFile(outPath); err == nil {
		t := utf8Text(data)
		out = &t
	}
	_ = os.Remove(outPath)
	return out
}

// codexReadAnswer is readAnswer('codex', stdout, outText): the last error or
// turn.failed event as parsed (nil = none), the -o text, its JSON, and the
// usage of the last event that carries a usage object (nil = estimated).
func codexReadAnswer(stdout string, outText *string) (parsed any, text string, answer any, hasAnswer bool, usage ledger.Object) {
	var usageEvent any
	for _, line := range strings.Split(stdout, "\n") {
		v, ok := tryJSON(line)
		e, isObj := v.(ledger.Object)
		if !ok || !isObj {
			continue
		}
		switch e.Get("usage").(type) {
		case ledger.Object, []any: // a JS object; parseUsage reads only a plain one
			usageEvent = e
		}
		if t := e.Get("type"); t == "error" || t == "turn.failed" {
			parsed = e
		}
	}
	if outText != nil {
		text = *outText
	}
	answer, hasAnswer = tryJSON(text)
	return parsed, text, answer, hasAnswer, parseUsage("codex", usageEvent)
}
