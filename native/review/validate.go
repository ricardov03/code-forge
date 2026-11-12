package review

import (
	"math"
	"strconv"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/ledger"
)

// Port of src/review/validate-review.mjs (the stub guard) and
// truncateReviewFields from src/review/schema-fallback.mjs. Session results and
// answers are ordered JSON objects (ledger.Object) so every row and result
// keeps Node's property order.

// DefaultMinTokensOut is review.min_tokens_out when unset (plan §4.2).
const DefaultMinTokensOut = 120

// CleanPassMinTokensOut is the floor for a structured clean pass (B53).
const CleanPassMinTokensOut = 40

// Verdict is the stub guard's answer. OK carries Review and TokensOut; a
// failure carries Reason (exit, schema, hunks_mismatch, too_short, timeout,
// budget) and Detail.
type Verdict struct {
	OK            bool
	Review        ledger.Object
	TokensOut     float64
	PassedCoerced bool
	Reason        string
	Detail        string
}

// lookup tells an absent property from a JSON null, as JS `undefined` does.
func lookup(o ledger.Object, key string) (any, bool) {
	for _, f := range o {
		if f.Key == key {
			return f.Value, true
		}
	}
	return nil, false
}

// jsString is JS String(value) for JSON scalars.
func jsString(v any, present bool) string {
	if !present {
		return "undefined"
	}
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return jsNumber(x)
	case []any:
		return "[array]"
	default:
		return "[object Object]"
	}
}

func jsNumber(x float64) string {
	switch {
	case math.IsNaN(x):
		return "NaN"
	case math.IsInf(x, 1):
		return "Infinity"
	case math.IsInf(x, -1):
		return "-Infinity"
	}
	b, _ := canon.Marshal(x)
	return string(b)
}

// jsToNumber is JS Number(value) for JSON values.
func jsToNumber(v any) float64 {
	switch x := v.(type) {
	case nil:
		return 0
	case bool:
		if x {
			return 1
		}
		return 0
	case float64:
		return x
	case string:
		s := trimJS(x)
		if s == "" {
			return 0
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil && s != "+Inf" && s != "Inf" && s != "-Inf" && s != "inf" && s != "+inf" && s != "-inf" {
			return f
		}
		return math.NaN()
	}
	return math.NaN()
}

// schema is the finding schema (src/review/finding.schema.json) in the order
// Node walks it.
type schemaNode struct {
	typ       string
	maxLength int // -1 = none
	minLength int
	minimum   *float64
	enum      []string
	required  []string
	props     []schemaProp
	closed    bool
	items     *schemaNode
}

type schemaProp struct {
	name string
	node *schemaNode
}

func str(max int) *schemaNode { return &schemaNode{typ: "string", maxLength: max} }

var zero = 0.0

var findingSchema = func() *schemaNode {
	finding := &schemaNode{typ: "object", maxLength: -1, closed: true,
		required: []string{"id", "file", "line_start", "line_end", "severity", "category", "claim", "evidence", "fix"},
		props: []schemaProp{
			{"id", &schemaNode{typ: "string", maxLength: 64, minLength: 1}},
			{"file", str(-1)},
			{"line_start", &schemaNode{typ: "integer", maxLength: -1, minimum: &zero}},
			{"line_end", &schemaNode{typ: "integer", maxLength: -1, minimum: &zero}},
			{"severity", &schemaNode{typ: "string", maxLength: -1, enum: []string{"critical", "warning", "nit"}}},
			{"category", str(64)},
			{"claim", str(300)},
			{"evidence", str(300)},
			{"fix", str(300)},
			{"agreement", &schemaNode{typ: "string", maxLength: -1, enum: []string{"both", "A-only", "B-only"}}},
		}}
	resolved := &schemaNode{typ: "object", maxLength: -1, closed: true,
		required: []string{"id", "resolved", "why"},
		props: []schemaProp{
			{"id", str(-1)},
			{"resolved", &schemaNode{typ: "boolean", maxLength: -1}},
			{"why", str(200)},
			{"current_lines", str(300)},
		}}
	return &schemaNode{typ: "object", maxLength: -1, closed: true,
		required: []string{"passed", "summary", "reviewed_hunks", "findings"},
		props: []schemaProp{
			{"passed", &schemaNode{typ: "boolean", maxLength: -1}},
			{"summary", str(300)},
			{"reviewed_hunks", &schemaNode{typ: "array", maxLength: -1, items: str(-1)}},
			{"findings", &schemaNode{typ: "array", maxLength: -1, items: finding}},
			{"resolved", &schemaNode{typ: "array", maxLength: -1, items: resolved}},
			{"needs_file", &schemaNode{typ: "array", maxLength: -1, items: str(-1)}},
		}}
}()

// codePoints counts like Ajv's ucs2length (a surrogate pair is one).
func codePoints(s string) int { return utf8.RuneCountInString(s) }

func (s *schemaNode) valid(v any) bool {
	switch s.typ {
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "integer":
		n, ok := v.(float64)
		if !ok || n != math.Trunc(n) || math.IsInf(n, 0) {
			return false
		}
		return s.minimum == nil || n >= *s.minimum
	case "string":
		x, ok := v.(string)
		if !ok {
			return false
		}
		n := codePoints(x)
		if s.maxLength >= 0 && n > s.maxLength || n < s.minLength {
			return false
		}
		if len(s.enum) > 0 {
			for _, e := range s.enum {
				if e == x {
					return true
				}
			}
			return false
		}
		return true
	case "array":
		arr, ok := v.([]any)
		if !ok {
			return false
		}
		for _, item := range arr {
			if !s.items.valid(item) {
				return false
			}
		}
		return true
	case "object":
		o, ok := v.(ledger.Object)
		if !ok {
			return false
		}
		for _, r := range s.required {
			if _, has := lookup(o, r); !has {
				return false
			}
		}
		for _, f := range o {
			child := s.prop(f.Key)
			if child == nil {
				if s.closed {
					return false
				}
				continue
			}
			if !child.valid(f.Value) {
				return false
			}
		}
		return true
	}
	return false
}

func (s *schemaNode) prop(name string) *schemaNode {
	for _, p := range s.props {
		if p.name == name {
			return p.node
		}
	}
	return nil
}

// ValidAnswer reports whether v validates against finding.schema.json.
func ValidAnswer(v any) bool { return findingSchema.valid(v) }

// CoerceReviewPassed recovers a null verdict as failure only when an otherwise
// valid review has findings; the second result tells whether it did.
func CoerceReviewPassed(answer any) (any, bool) {
	o, ok := answer.(ledger.Object)
	if !ok {
		return answer, false
	}
	passed, has := lookup(o, "passed")
	if !has || passed != nil {
		return answer, false
	}
	findings, ok := o.Get("findings").([]any)
	if !ok || len(findings) == 0 {
		return answer, false
	}
	candidate := o.Clone()
	candidate.Set("passed", false)
	if ValidAnswer(candidate) {
		return candidate, true
	}
	return answer, false
}

// MinTokensOut is review.min_tokens_out when a positive integer, else 120.
func MinTokensOut(cfg map[string]any) int {
	if n, ok := intOf(section(cfg, "review")["min_tokens_out"]); ok && n > 0 {
		return n
	}
	return DefaultMinTokensOut
}

// ValidateReview is the stub guard on one session result (nil = no session).
func ValidateReview(session ledger.Object, hunkHeaders []string, min int) Verdict {
	if session == nil {
		return Verdict{Reason: "exit", Detail: "no session result"}
	}
	status, hasStatus := lookup(session, "status")
	if status == "timeout" {
		return Verdict{Reason: "timeout", Detail: "killed at the session timeout"}
	}
	if status == "unavailable" && session.Get("reason") == "budget" {
		msg, has := lookup(session, "message")
		if !has || msg == nil {
			return Verdict{Reason: "budget", Detail: "budget.usd reached"}
		}
		return Verdict{Reason: "budget", Detail: jsString(msg, true)}
	}
	exitCode, hasExit := lookup(session, "exit_code")
	if status == "failed" || status == "unavailable" || (hasExit && exitCode != 0.0) {
		detail := "session " + jsString(status, hasStatus)
		if hasExit {
			detail += " (exit " + jsString(exitCode, true) + ")"
		}
		return Verdict{Reason: "exit", Detail: detail}
	}
	raw, _ := lookup(session, "answer")
	answerAny, coerced := CoerceReviewPassed(raw)
	answer, isObj := answerAny.(ledger.Object)
	if status != "ok" || !isObj || answer == nil || !ValidAnswer(answer) {
		return Verdict{Reason: "schema", Detail: "the answer does not validate against the finding schema"}
	}
	got, _ := answer.Get("reviewed_hunks").([]any)
	mismatch := len(got) != len(hunkHeaders)
	for i := 0; !mismatch && i < len(got); i++ {
		mismatch = got[i] != hunkHeaders[i]
	}
	if mismatch {
		return Verdict{Reason: "hunks_mismatch", Detail: "reviewed_hunks has " + strconv.Itoa(len(got)) + " entries; the packet has " + strconv.Itoa(len(hunkHeaders)) + " hunks"}
	}
	tokensOut := jsToNumber(tokensOutValue(session))
	findings, _ := answer.Get("findings").([]any)
	summary, _ := answer.Get("summary").(string)
	clean := answer.Get("passed") == true && len(findings) == 0 && trimJS(summary) != ""
	floor := min
	if clean && CleanPassMinTokensOut < floor {
		floor = CleanPassMinTokensOut
	}
	if !(tokensOut >= float64(floor)) {
		return Verdict{Reason: "too_short", Detail: "tokens_out " + jsNumber(tokensOut) + " < " + strconv.Itoa(floor)}
	}
	return Verdict{OK: true, Review: answer, TokensOut: tokensOut, PassedCoerced: coerced || session.Get("passed_coerced") == true}
}

// tokensOutValue is session.usage?.tokens_out ?? session.row?.tokens_out ?? 0.
func tokensOutValue(session ledger.Object) any {
	for _, key := range []string{"usage", "row"} {
		if o, ok := session.Get(key).(ledger.Object); ok {
			if v := o.Get("tokens_out"); v != nil {
				return v
			}
		}
	}
	return 0.0
}

// TruncatedField is one entry of truncated_fields.
type TruncatedField struct {
	Path           string
	OriginalLength int
	MaxLength      int
}

// TruncateReviewFields shortens only schema-bounded strings of the answer
// (restoring a JSON text the spawner rejected), like Node's helper. It returns
// the new session result and the cuts made (nil when res is returned as is).
func TruncateReviewFields(res ledger.Object) (ledger.Object, []TruncatedField) {
	if res == nil {
		return res, nil
	}
	status := res.Get("status")
	if status != "ok" && status != "invalid-output" {
		return res, nil
	}
	answer := res.Get("answer")
	if answer == nil {
		text, ok := res.Get("text").(string)
		if !ok {
			return res, nil
		}
		parsed, err := ledger.DecodeObject([]byte(text))
		if err != nil {
			return res, nil
		}
		answer = parsed
	}
	obj, ok := answer.(ledger.Object)
	if !ok {
		return res, nil
	}
	cuts := []TruncatedField{}
	shortened := shorten(deepClone(obj), findingSchema, "", &cuts)
	out := res.Clone()
	out.Set("answer", shortened)
	list := make([]any, 0, len(cuts))
	for _, c := range cuts {
		list = append(list, ledger.Object{{Key: "path", Value: c.Path}, {Key: "original_length", Value: float64(c.OriginalLength)}, {Key: "max_length", Value: float64(c.MaxLength)}})
	}
	out.Set("truncated_fields", list)
	if len(cuts) > 0 && status == "invalid-output" {
		out.Set("status", "ok")
	}
	return out, cuts
}

func shorten(v any, s *schemaNode, at string, cuts *[]TruncatedField) any {
	if x, ok := v.(string); ok && s.maxLength >= 0 {
		runes := []rune(x)
		if len(runes) > s.maxLength {
			*cuts = append(*cuts, TruncatedField{at, len(runes), s.maxLength})
			keep := s.maxLength - 1
			if keep < 0 {
				keep = 0
			}
			return string(runes[:keep]) + "…"
		}
		return v
	}
	if arr, ok := v.([]any); ok {
		if s.items == nil {
			return v
		}
		out := make([]any, len(arr))
		for i, item := range arr {
			out[i] = shorten(item, s.items, at+"/"+strconv.Itoa(i), cuts)
		}
		return out
	}
	if o, ok := v.(ledger.Object); ok {
		for _, p := range s.props {
			if child, has := lookup(o, p.name); has {
				o.Set(p.name, shorten(child, p.node, at+"/"+p.name, cuts))
			}
		}
	}
	return v
}

func deepClone(v any) ledger.Object {
	return cloneValue(v).(ledger.Object)
}

func cloneValue(v any) any {
	switch x := v.(type) {
	case ledger.Object:
		out := make(ledger.Object, len(x))
		for i, f := range x {
			out[i] = ledger.Field{Key: f.Key, Value: cloneValue(f.Value)}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = cloneValue(item)
		}
		return out
	}
	return v
}
