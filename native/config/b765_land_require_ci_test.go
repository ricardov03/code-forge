package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B765: land.require_ci, land.ci_workflow and land.ci_red_list load through
// the config validator; a bad value or unknown land key is refused.
func TestB765LandRequireCIConfigValidation(t *testing.T) {
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
	for _, ok := range []string{"  require_ci: github\n  ci_workflow: ci.yml\n  ci_red_list: ci-red.map\n", "  require_ci: off\n"} {
		if err := load(t, ok); err != nil {
			t.Errorf("%q must load, got %v", ok, err)
		}
	}
	for _, bad := range []struct{ yaml, want string }{
		{"  require_ci: gitlab\n", "require_ci"},
		{"  require_ci: true\n", "require_ci"},
		{"  ci_workflow: \"\"\n", "ci_workflow"},
		{"  require_ci_red: x\n", "require_ci_red"},
	} {
		err := load(t, bad.yaml)
		if err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%q: error %v, want one naming %q", bad.yaml, err, bad.want)
		}
	}
}
