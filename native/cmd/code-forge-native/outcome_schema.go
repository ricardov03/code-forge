package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B873 (B801 audit P02): an outcome file that is valid JSON with the wrong
// shape, or not UTF-8 text, used to stop with the outcome-unsupported
// refusal. outcome seal|check|record now name what is wrong, the way
// schema/outcome.schema.json states it, and exit 1.

// outcomeSchemaFields is the schema's top-level keys, in its order.
var outcomeSchemaFields = []string{"skill", "version", "request", "status", "approved_by", "artifact", "files", "decisions", "checks", "cost_usd", "minutes"}

// outcomeSchemaProblem is the one problem line for a decoded outcome that
// does not match the schema ("" when it does).
func outcomeSchemaProblem(file string, v any) string {
	problems := outcomeSchemaProblems(v)
	if len(problems) == 0 {
		return ""
	}
	return "Outcome " + jsQuote(file) + " does not match the outcome schema: " + strings.Join(problems, "; ")
}

// outcomeSchemaProblems lists every way v breaks schema/outcome.schema.json.
func outcomeSchemaProblems(v any) []string {
	o, ok := v.(ledger.Object)
	if !ok {
		return []string{"must be a JSON object"}
	}
	var problems []string
	add := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }
	for _, f := range o {
		if !slices.Contains(outcomeSchemaFields, f.Key) {
			add("unknown field %s", jsQuote(f.Key))
		}
	}
	for _, key := range []string{"skill", "status", "files"} {
		if !outcomeHas(o, key) {
			add("missing field %s", jsQuote(key))
		}
	}
	for _, f := range o {
		name := jsQuote(f.Key)
		switch f.Key {
		case "skill", "version", "request":
			if _, ok := f.Value.(string); !ok {
				add("%s must be a string", name)
			}
		case "status":
			if !slices.Contains([]any{"draft", "approved", "blocked", "failed"}, f.Value) {
				add("%s must be one of draft, approved, blocked, failed", name)
			}
		case "approved_by", "artifact":
			if _, ok := f.Value.(string); f.Value != nil && !ok {
				add("%s must be a string or null", name)
			}
		case "cost_usd", "minutes":
			if _, ok := f.Value.(float64); f.Value != nil && !ok {
				add("%s must be a number or null", name)
			}
		case "decisions":
			outcomeStringList(f.Value, "decisions", add)
		case "files":
			outcomeObjectList(f.Value, "files", []string{"path", "sha256"}, []string{"path", "sha256"}, add, func(at string, entry ledger.Object) {
				for _, key := range []string{"path", "sha256"} {
					if val, ok := outcomeGet(entry, key); ok {
						if _, isText := val.(string); !isText {
							add("%s must be a string", jsQuote(at+"."+key))
						}
					}
				}
			})
		case "checks":
			outcomeObjectList(f.Value, "checks", []string{"name", "result", "detail", "evidence"}, []string{"name", "result"}, add, func(at string, check ledger.Object) {
				if val, ok := outcomeGet(check, "name"); ok {
					if _, isText := val.(string); !isText {
						add("%s must be a string", jsQuote(at+".name"))
					}
				}
				if val, ok := outcomeGet(check, "result"); ok && !slices.Contains([]any{"pass", "warn", "fail"}, val) {
					add("%s must be one of pass, warn, fail", jsQuote(at+".result"))
				}
				if val, ok := outcomeGet(check, "detail"); ok {
					outcomeStringList(val, at+".detail", add)
				}
				if val, ok := outcomeGet(check, "evidence"); ok {
					if _, isText := val.(string); !isText {
						add("%s must be a string", jsQuote(at+".evidence"))
					}
				}
			})
		}
	}
	return problems
}

func outcomeHas(o ledger.Object, key string) bool {
	_, ok := outcomeGet(o, key)
	return ok
}

func outcomeGet(o ledger.Object, key string) (any, bool) {
	for _, f := range o {
		if f.Key == key {
			return f.Value, true
		}
	}
	return nil, false
}

func outcomeStringList(v any, at string, add func(string, ...any)) {
	list, ok := v.([]any)
	if !ok {
		add("%s must be an array of strings", jsQuote(at))
		return
	}
	for i, item := range list {
		if _, ok := item.(string); !ok {
			add("%s must be a string", jsQuote(fmt.Sprintf("%s[%d]", at, i)))
		}
	}
}

// outcomeObjectList checks an array of objects with only the allowed keys
// and every required one, then hands each entry to each.
func outcomeObjectList(v any, at string, allowed, required []string, add func(string, ...any), each func(string, ledger.Object)) {
	list, ok := v.([]any)
	if !ok {
		add("%s must be an array", jsQuote(at))
		return
	}
	for i, item := range list {
		path := fmt.Sprintf("%s[%d]", at, i)
		entry, ok := item.(ledger.Object)
		if !ok {
			add("%s must be an object", jsQuote(path))
			continue
		}
		for _, f := range entry {
			if !slices.Contains(allowed, f.Key) {
				add("%s has unknown field %s", jsQuote(path), jsQuote(f.Key))
			}
		}
		for _, key := range required {
			if !outcomeHas(entry, key) {
				add("%s is missing field %s", jsQuote(path), jsQuote(key))
			}
		}
		each(path, entry)
	}
}
