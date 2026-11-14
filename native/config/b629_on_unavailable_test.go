package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B629: review.on_unavailable and review.on_unavailable_backoff_s load
// through the config validator; unknown keys and bad values are refused.
func TestB629OnUnavailableConfigValidation(t *testing.T) {
	base := `version: 1
provider: anthropic
levels:
  L0: {model: claude-haiku-4-5}
  L1: {model: claude-sonnet-4-5}
  L2: {model: claude-opus-4-1}
  L3: {model: claude-opus-4-1}
review:
`
	load := func(t *testing.T, review string) error {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".code-forge.yml"), []byte(base+review), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := LoadValidated(dir, "0.0.0")
		return err
	}
	ok := `  on_unavailable:
    - {provider: openai, model: gpt-5, effort: high}
    - {model: claude-sonnet-4-5, level: L3}
  on_unavailable_backoff_s: [0, 30, 120]
`
	if err := load(t, ok); err != nil {
		t.Fatalf("a valid chain must load, got %v", err)
	}
	if err := load(t, "  on_unavailable_backoff_s: []\n"); err != nil {
		t.Fatalf("an empty backoff list must load, got %v", err)
	}
	refused := map[string]struct{ yaml, want string }{
		"unknown route key": {"  on_unavailable:\n    - {model: gpt-5, min_lines: 10}\n", "min_lines"},
		"missing model":     {"  on_unavailable:\n    - {provider: openai}\n", "model"},
		"bad level":         {"  on_unavailable:\n    - {model: gpt-5, level: L1}\n", "level"},
		"not a list":        {"  on_unavailable: gpt-5\n", "on_unavailable"},
		"negative backoff":  {"  on_unavailable_backoff_s: [-1]\n", "on_unavailable_backoff_s"},
		"backoff too long":  {"  on_unavailable_backoff_s: [3601]\n", "on_unavailable_backoff_s"},
		"unknown sibling":   {"  on_unavailable_chain: []\n", "on_unavailable_chain"},
	}
	for name, c := range refused {
		err := load(t, c.yaml)
		if err == nil {
			t.Errorf("%s: want a schema error, got none", name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %q does not name %q", name, err.Error(), c.want)
		}
	}
}
