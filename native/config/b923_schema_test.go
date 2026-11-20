package config

import (
	"strings"
	"testing"
)

// B923: gates.coder_test_parallel is the -p of the coder test budget rule.
func TestB923SchemaTakesCoderTestParallel(t *testing.T) {
	cfg := func(v any) map[string]any {
		levels := map[string]any{}
		for _, l := range []string{"L0", "L1", "L2", "L3"} {
			levels[l] = map[string]any{"model": "claude-sonnet-4-5"}
		}
		return map[string]any{"version": float64(1), "provider": "anthropic", "levels": levels, "gates": map[string]any{"coder_test_parallel": v}}
	}
	if err := ValidateSchema(cfg(float64(3)), "0.0.0"); err != nil {
		t.Fatalf("coder_test_parallel: 3 refused: %v", err)
	}
	for _, bad := range []any{float64(0), 1.5, "2"} {
		if err := ValidateSchema(cfg(bad), "0.0.0"); err == nil || !strings.Contains(err.Error(), "coder_test_parallel") {
			t.Errorf("coder_test_parallel: %v accepted or not named: %v", bad, err)
		}
	}
}
