package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B795: land.auto_resolve is a boolean or an object of enabled, tables and
// generated; a bad shape is refused.
func TestB795LandAutoResolveConfigValidation(t *testing.T) {
	base := `version: 1
provider: anthropic
levels:
  L0: {model: claude-haiku-4-5}
  L1: {model: claude-sonnet-4-5}
  L2: {model: claude-opus-4-1}
  L3: {model: claude-opus-4-1}
land:
`
	load := func(t *testing.T, land string) error {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".code-forge.yml"), []byte(base+land), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := LoadValidated(dir, "0.0.0")
		return err
	}
	for _, ok := range []string{
		"  auto_resolve: false\n",
		"  auto_resolve: true\n",
		"  auto_resolve:\n    enabled: true\n    tables: [docs/native-engine.md, docs/other.md]\n    generated:\n      - {path: docs/reference/commands.md, argv: [sh, scripts/gen-commands-doc.sh]}\n",
	} {
		if err := load(t, ok); err != nil {
			t.Errorf("%q must load, got %v", ok, err)
		}
	}
	for _, bad := range []string{
		"  auto_resolve: yes-please\n",
		"  auto_resolve:\n    tables: docs/native-engine.md\n",
		"  auto_resolve:\n    generated: [{path: docs/a.md}]\n",
		"  auto_resolve:\n    union: [docs/a.md]\n",
	} {
		if err := load(t, bad); err == nil || !strings.Contains(err.Error(), "auto_resolve") {
			t.Errorf("%q: error %v, want one naming auto_resolve", bad, err)
		}
	}
}
