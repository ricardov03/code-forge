package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B667: gates.go_changed_packages loads as a boolean and refuses other values.
func TestB667GoChangedPackagesConfigValidation(t *testing.T) {
	base := `version: 1
provider: anthropic
levels:
  L0: {model: claude-haiku-4-5}
  L1: {model: claude-sonnet-4-5}
  L2: {model: claude-opus-4-1}
  L3: {model: claude-opus-4-1}
gates:
`
	load := func(t *testing.T, gates string) error {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".code-forge.yml"), []byte(base+gates), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := LoadValidated(dir, "0.0.0")
		return err
	}
	for _, ok := range []string{"  go_changed_packages: true\n", "  go_changed_packages: false\n"} {
		if err := load(t, ok); err != nil {
			t.Errorf("%q must load, got %v", ok, err)
		}
	}
	err := load(t, "  go_changed_packages: \"yes\"\n")
	if err == nil || !strings.Contains(err.Error(), "go_changed_packages") {
		t.Errorf("a string value must be refused naming the key, got %v", err)
	}
}
