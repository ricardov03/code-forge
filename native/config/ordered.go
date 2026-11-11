package config

import (
	"reflect"
	"sort"

	"github.com/ricardov03/code-forge/native/ledger"
)

// Ordered remembers the insertion order of each YAML mapping in a loaded
// config. Keys added after parsing (migrateConfig's version) come last.
type Ordered struct{ order map[uintptr][]string }

// Keys returns m's keys in insertion order, then any later keys sorted.
func (o Ordered) Keys(m map[string]any) []string {
	keys := []string{}
	seen := map[string]bool{}
	if o.order != nil && m != nil {
		for _, key := range o.order[reflect.ValueOf(m).Pointer()] {
			if _, ok := m[key]; ok && !seen[key] {
				keys = append(keys, key)
				seen[key] = true
			}
		}
	}
	rest := []string{}
	for key := range m {
		if !seen[key] {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	return append(keys, rest...)
}

// Object converts value so every mapping becomes a ledger.Object in YAML
// order; ledger.Stringify then matches JSON.stringify byte for byte.
func (o Ordered) Object(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := ledger.Object{}
		for _, key := range o.Keys(v) {
			out = append(out, ledger.Field{Key: key, Value: o.Object(v[key])})
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = o.Object(child)
		}
		return out
	default:
		return value
	}
}

// MaskSecrets is maskSecretTokens in src/config/secret-patterns.mjs.
func MaskSecrets(text string) string { return maskSecrets(text) }
