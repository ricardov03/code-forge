package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B796: land.ci_wait, land.ci_branch_prefix and land.ci_remote load through
// the config validator; a bad value is refused naming the key.
func TestB796LandCIWaitConfigValidation(t *testing.T) {
	base := `version: 1
provider: anthropic
levels:
  L0: {model: claude-haiku-4-5}
  L1: {model: claude-sonnet-4-5}
  L2: {model: claude-opus-4-1}
  L3: {model: claude-opus-4-1}
land:
  require_ci: github
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
	for _, ok := range []string{"  ci_wait: 30m\n  ci_branch_prefix: ci-\n  ci_remote: origin\n", "  ci_wait: off\n", "  ci_wait: 1h30m\n", "  ci_branch_prefix: ci/blocks-\n"} {
		if err := load(t, ok); err != nil {
			t.Errorf("%q must load, got %v", ok, err)
		}
	}
	for _, bad := range []struct{ yaml, want string }{
		{"  ci_wait: soon\n", "ci_wait"},
		{"  ci_wait: 30\n", "ci_wait"},
		{"  ci_branch_prefix: \"-x\"\n", "ci_branch_prefix"},
		{"  ci_remote: \"\"\n", "ci_remote"},
	} {
		err := load(t, bad.yaml)
		if err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%q: error %v, want one naming %q", bad.yaml, err, bad.want)
		}
	}
}
