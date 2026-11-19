package config

import (
	"strings"
	"testing"
)

// B800: autopilot.max_found_blocks is an integer from 0 to 100.
func TestB800SchemaMaxFoundBlocks(t *testing.T) {
	cfg := func(v any) map[string]any {
		return map[string]any{
			"version":  float64(1),
			"provider": "anthropic",
			"levels": map[string]any{
				"L0": map[string]any{"model": "claude-haiku-4-5"},
				"L1": map[string]any{"model": "claude-sonnet-4-5"},
				"L2": map[string]any{"model": "claude-opus-4-1"},
				"L3": map[string]any{"model": "claude-opus-4-1"},
			},
			"autopilot": map[string]any{"max_found_blocks": v},
		}
	}
	for _, ok := range []any{float64(0), float64(3), float64(100)} {
		if err := ValidateSchema(cfg(ok), "0.0.0"); err != nil {
			t.Fatalf("%v refused: %v", ok, err)
		}
	}
	for _, bad := range []any{float64(101), float64(-1), float64(2.5), "10"} {
		if err := ValidateSchema(cfg(bad), "0.0.0"); err == nil || !strings.Contains(err.Error(), "max_found_blocks") {
			t.Fatalf("%v accepted or unnamed: %v", bad, err)
		}
	}
}
