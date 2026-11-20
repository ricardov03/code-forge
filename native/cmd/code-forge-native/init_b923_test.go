package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// B923 (owner rule B791: a config key change updates init, the examples and
// the fixer): init writes gates.coder_test_parallel with the default -p after
// gates.ram_tmp_mb, and keeps a value already set.
func TestB923InitWritesCoderTestParallel(t *testing.T) {
	side := b527Go(t, []string{"--no-interaction", "--skip-doctor", "--no-jev"}, nil)
	if cfg := b939Config(t, side); !strings.Contains(cfg, "\n  ram_tmp_mb: 2048\n  coder_test_parallel: 2\nproof:\n") {
		t.Fatalf("gates.coder_test_parallel: 2 is not in the new config:\n%s", cfg)
	}
	side = b527Go(t, []string{"--no-interaction", "--skip-doctor", "--no-jev"}, b527Files(map[string]string{
		".code-forge.yml": "version: 1\ngates:\n  coder_test_parallel: 3\n",
	}))
	if cfg := b939Config(t, side); !strings.Contains(cfg, "  coder_test_parallel: 3\n") || strings.Count(cfg, "coder_test_parallel:") != 1 {
		t.Fatalf("init changed gates.coder_test_parallel: 3:\n%s", cfg)
	}
}

// B923 under the B791 rule: config fix knows gates.coder_test_parallel and
// keeps it (and its comment) while it fixes an old shape.
func TestB923ConfigFixKeepsCoderTestParallel(t *testing.T) {
	old := strings.Replace(b791Old, "  format: null\n", "  format: null\n  coder_test_parallel: 4 # big Mac\n", 1)
	_, repo := smallProject(t, old)
	file := filepath.Join(repo, ".code-forge.yml")
	checkSmallCases(t, []smallCase{
		{[]string{"config", "fix"}, 0, b791Fix + file + ": fixed (1 rule); run code-forge validate to check it\n", ""},
		{[]string{"config", "fix"}, 0, file + ": config ok\n", ""},
	})
	b791Unchanged(t, file, strings.Replace(old, "    invariants: scripts/inv.sh # old\n", "", 1))
}
