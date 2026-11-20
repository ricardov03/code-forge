package main

import (
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ramtmp"
)

// b939Config is the .code-forge.yml init wrote, cut from the file tree.
func b939Config(t *testing.T, side b527Side) string {
	t.Helper()
	_, rest, ok := strings.Cut(side.files, ".code-forge.yml -rw-r--r--\n")
	if !ok {
		t.Fatalf("init wrote no .code-forge.yml (exit %d):\n%s\n%s", side.code, side.files, side.stderr)
	}
	return rest
}

// TestB939InitWritesRamTmp: a new config from init carries gates.ram_tmp
// with the default mode.
func TestB939InitWritesRamTmp(t *testing.T) {
	side := b527Go(t, []string{"--no-interaction", "--skip-doctor", "--no-jev"}, nil)
	if side.code != 0 {
		t.Fatalf("init exit %d: %s", side.code, side.stderr)
	}
	cfg := b939Config(t, side)
	if !strings.Contains(cfg, "\n  format: null\n  ram_tmp: "+ramtmp.DefaultMode+"\nproof:\n") {
		t.Fatalf("gates.ram_tmp: %s is not in the new config:\n%s", ramtmp.DefaultMode, cfg)
	}
}

// TestB939InitKeepsRamTmpOff: re-running init keeps a ram_tmp the team set.
func TestB939InitKeepsRamTmpOff(t *testing.T) {
	side := b527Go(t, []string{"--no-interaction", "--skip-doctor", "--no-jev"}, b527Files(map[string]string{
		".code-forge.yml": "version: 1\ngates:\n  ram_tmp: off\n",
	}))
	if side.code != 0 {
		t.Fatalf("init exit %d: %s", side.code, side.stderr)
	}
	cfg := b939Config(t, side)
	if !strings.Contains(cfg, "  ram_tmp: off\n") || strings.Count(cfg, "ram_tmp:") != 1 {
		t.Fatalf("init changed gates.ram_tmp: off:\n%s", cfg)
	}
}
