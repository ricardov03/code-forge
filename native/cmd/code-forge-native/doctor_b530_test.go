package main

import (
	"reflect"
	"testing"

	"github.com/ricardov03/code-forge/native/config"
)

// B530: configuredProviders skips a level whose value is falsy in JavaScript;
// a YAML .nan is NaN there, and NaN is falsy (Node printed [] for
// {provider: anthropic, levels: {L0: NaN}} and ["xai","anthropic"] for the
// mixed case below).
func TestB530DoctorConfiguredProvidersNaNLevel(t *testing.T) {
	for _, c := range []struct {
		name, yaml string
		want       []string
	}{
		{"nan", "provider: anthropic\nlevels:\n  L0: .nan\n", nil},
		{"zero", "provider: anthropic\nlevels:\n  L0: 0\n", nil},
		{"one", "provider: anthropic\nlevels:\n  L0: 1\n", []string{"anthropic"}},
		{"mixed", "provider: anthropic\nlevels:\n  L0: .NaN\n  L1:\n    provider: xai\n  L2: -0\n  L3: x\n", []string{"xai", "anthropic"}},
	} {
		parsed, err := config.ParseYAML(c.yaml)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		cfg, _ := parsed.(map[string]any)
		if got := doctorConfiguredProviders(cfg); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %#v want %#v", c.name, got, c.want)
		}
	}
}
