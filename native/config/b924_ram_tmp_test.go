package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B924: gates.ram_tmp loads as auto or off and refuses other values.
func TestB924RamTmpConfigValidation(t *testing.T) {
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
	for _, ok := range []string{"  ram_tmp: auto\n", "  ram_tmp: off\n"} {
		if err := load(t, ok); err != nil {
			t.Errorf("%q must load, got %v", ok, err)
		}
	}
	for _, bad := range []string{"  ram_tmp: on\n", "  ram_tmp: true\n"} {
		if err := load(t, bad); err == nil || !strings.Contains(err.Error(), "ram_tmp") {
			t.Errorf("%q must be refused naming the key, got %v", bad, err)
		}
	}
}
