package config

import "encoding/json"

const providerDefaults = `{
"anthropic":{"provider":"anthropic","levels":{"L0":{"model":"claude-haiku-4-5-20251001"},"L1":{"model":"claude-sonnet-5"},"L2":{"model":"claude-opus-5-5"},"L3":{"model":"claude-fable-5-2"}},"caps":{"coders":2}},
"openai":{"provider":"openai","levels":{"L0":{"model":"gpt-6-luna","effort":"low"},"L1":{"model":"gpt-6-luna","effort":"high"},"L2":{"model":"gpt-6-sol","effort":"high"},"L3":{"model":"gpt-6-astra","effort":"high"}},"caps":{"coders":2}},
"xai":{"provider":"xai","levels":{"L0":{"model":"grok-4.7","effort":"low"},"L1":{"model":"grok-4.7","effort":"medium"},"L2":{"model":"grok-4.7","effort":"high"},"L3":{"model":"grok-4.7","effort":"xhigh"}},"caps":{"coders":2}}
}`

// DefaultsForProvider returns a fresh copy of the wizard's shipped matrix, or
// nil for an unknown provider. Loading a config does not merge this matrix.
func DefaultsForProvider(provider string) map[string]any {
	var defaults map[string]map[string]any
	_ = json.Unmarshal([]byte(providerDefaults), &defaults)
	return defaults[provider]
}
