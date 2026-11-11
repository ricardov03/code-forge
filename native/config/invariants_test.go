package config

import (
	"strings"
	"testing"
)

// B402: no engine ever ran gates.extra.invariants, so the schema refuses it
// instead of accepting a script that would be skipped silently.
func TestSchemaRefusesGatesExtraInvariants(t *testing.T) {
	base := func(extra map[string]any) map[string]any {
		return map[string]any{
			"version":  float64(1),
			"provider": "anthropic",
			"levels": map[string]any{
				"L0": map[string]any{"model": "claude-haiku-4-5"},
				"L1": map[string]any{"model": "claude-sonnet-4-5"},
				"L2": map[string]any{"model": "claude-opus-4-1"},
				"L3": map[string]any{"model": "claude-opus-4-1"},
			},
			"gates": map[string]any{"extra": extra},
		}
	}
	if err := ValidateSchema(base(map[string]any{"secret_scan": true, "safe_edit": false}), "0.0.0"); err != nil {
		t.Fatalf("gates.extra with secret_scan and safe_edit must stay valid, got %v", err)
	}
	err := ValidateSchema(base(map[string]any{"secret_scan": true, "invariants": "scripts/check.sh"}), "0.0.0")
	if err == nil {
		t.Fatal("gates.extra.invariants must be refused, got no error")
	}
	lines := strings.Split(strings.TrimSpace(err.Error()), "\n")
	if len(lines) != 1 {
		t.Fatalf("want exactly one schema error, got %d: %q", len(lines), err.Error())
	}
	if !strings.Contains(lines[0], "/gates/extra") || !strings.Contains(lines[0], `"invariants"`) {
		t.Fatalf("the error must name the property invariants under /gates/extra, got %q", lines[0])
	}
}
