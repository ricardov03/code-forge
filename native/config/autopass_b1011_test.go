package config

import (
	"encoding/json"
	"testing"
)

func TestB1011Config(t *testing.T) {
	var cfg map[string]any
	if err := json.Unmarshal([]byte(`{"version":1,"provider":"anthropic","levels":{"L0":{"model":"a"},"L1":{"model":"b"},"L2":{"model":"c"},"L3":{"model":"d"}}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	cfg["human_check"] = map[string]any{"autopilot": map[string]any{"enabled": true, "require_proof": true, "proof_dirs": []any{"/proof"}, "exclude_waits_for": []any{"board approval"}, "require_separate_prover": true}}
	if err := ValidateSchema(cfg, ""); err != nil {
		t.Fatal(err)
	}
	if err := CheckInvariants(cfg); err != nil {
		t.Fatal(err)
	}
	ap := cfg["human_check"].(map[string]any)["autopilot"].(map[string]any)
	ap["require_proof"] = false
	if err := CheckInvariants(cfg); err == nil {
		t.Fatal("require_proof false accepted")
	}
	if err := ValidateSchema(cfg, ""); err == nil {
		t.Fatal("schema accepted false require_proof")
	}
	ap["require_proof"] = true
	ap["proof_dirs"] = []any{}
	if err := CheckInvariants(cfg); err == nil {
		t.Fatal("enabled without proof_dirs accepted")
	}
	if err := ValidateSchema(cfg, ""); err == nil {
		t.Fatal("schema accepted enabled without proof_dirs")
	}
	delete(cfg, "human_check")
	if err := CheckInvariants(cfg); err != nil {
		t.Fatal(err)
	}
}
