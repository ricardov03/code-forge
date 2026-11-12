package session

import "github.com/ricardov03/code-forge/native/ledger"

// B550: the answer schemas of the author and s2 sessions, in their source key
// order (JSON.stringify of src/session/author.mjs AUTHOR_SCHEMA and
// src/session/s2.mjs S2_SCHEMA), and matchesSchema for each.

const authorSchemaJSON = `{"type":"object","properties":{"draft":{"type":"string"},"questions":{"type":"array","items":{"type":"object","properties":{"id":{"type":"string"},"question":{"type":"string"},"options":{"type":"array","items":{"type":"string"}},"why":{"type":"string"},"blocking":{"type":"boolean"}},"required":["id","question","options","why","blocking"],"additionalProperties":false}}},"required":["draft","questions"],"additionalProperties":false}`

// AuthorSchema is AUTHOR_SCHEMA.
func AuthorSchema() ledger.Object {
	schema, err := ledger.DecodeObject([]byte(authorSchemaJSON))
	if err != nil {
		panic(err)
	}
	return schema
}

// S2Schema is S2_SCHEMA.
func S2Schema() ledger.Object {
	schema, err := ledger.DecodeObject([]byte(probeSchemaJSON[ProbeSchemaS2]))
	if err != nil {
		panic(err)
	}
	return schema
}

// MatchesS2Schema is matchesSchema(S2_SCHEMA, value).
func MatchesS2Schema(value any) bool { return probeMatchesSchema(ProbeSchemaS2, value) }

// MatchesAuthorSchema is matchesSchema(AUTHOR_SCHEMA, value): an object with
// exactly draft (a string) and questions (a list of objects with exactly id,
// question, why (strings), options (a list of strings) and blocking).
func MatchesAuthorSchema(value any) bool {
	obj, ok := value.(ledger.Object)
	if !ok || !exactKeys(obj, "draft", "questions") {
		return false
	}
	if _, ok := obj.Get("draft").(string); !ok {
		return false
	}
	questions, ok := obj.Get("questions").([]any)
	if !ok {
		return false
	}
	for _, item := range questions {
		q, ok := item.(ledger.Object)
		if !ok || !exactKeys(q, "id", "question", "options", "why", "blocking") {
			return false
		}
		for _, key := range []string{"id", "question", "why"} {
			if _, ok := q.Get(key).(string); !ok {
				return false
			}
		}
		if _, ok := q.Get("blocking").(bool); !ok {
			return false
		}
		options, ok := q.Get("options").([]any)
		if !ok {
			return false
		}
		for _, o := range options {
			if _, ok := o.(string); !ok {
				return false
			}
		}
	}
	return true
}

// exactKeys: every listed key present, no other key.
func exactKeys(obj ledger.Object, keys ...string) bool {
	seen := map[string]bool{}
	for _, f := range obj {
		if !contains(keys, f.Key) {
			return false
		}
		seen[f.Key] = true
	}
	return len(seen) == len(keys)
}
