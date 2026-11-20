package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B941: gates.ram_tmp_mb loads as a whole number of MB in 64..65536.
func TestB941RamTmpMBConfigValidation(t *testing.T) {
	base := "version: 1\nprovider: anthropic\nlevels:\n  L0: {model: claude-haiku-4-5}\n  L1: {model: claude-sonnet-4-5}\n  L2: {model: claude-opus-4-1}\n  L3: {model: claude-opus-4-1}\ngates:\n"
	load := func(t *testing.T, gates string) error {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".code-forge.yml"), []byte(base+gates), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := LoadValidated(dir, "0.0.0")
		return err
	}
	for _, ok := range []string{"  ram_tmp_mb: 512\n", "  ram_tmp_mb: 2048\n  ram_tmp: auto\n"} {
		if err := load(t, ok); err != nil {
			t.Errorf("%q must load, got %v", ok, err)
		}
	}
	for _, bad := range []string{"  ram_tmp_mb: 0\n", "  ram_tmp_mb: 100000\n", "  ram_tmp_mb: 1.5\n", "  ram_tmp_mb: big\n"} {
		if err := load(t, bad); err == nil || !strings.Contains(err.Error(), "ram_tmp_mb") {
			t.Errorf("%q must be refused naming the key, got %v", bad, err)
		}
	}
}
