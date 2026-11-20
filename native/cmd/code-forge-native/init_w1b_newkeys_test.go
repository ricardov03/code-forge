package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// w1b combine (owner rule: a new config key goes into the default config):
// init writes land.require_main_ci (B962), autopilot.step_budgets margin and
// min_samples (B950) and release.max_blocks (B951) with their defaults, and
// keeps values a config already sets.
func TestW1bInitWritesNewLaneKeys(t *testing.T) {
	side := b527Go(t, []string{"--no-interaction", "--skip-doctor", "--no-jev"}, nil)
	want := "\nland:\n  require_main_ci: false\nautopilot:\n  step_budgets:\n    margin: 0.5\n    min_samples: 5\nrelease:\n  max_blocks: 8\n"
	if cfg := b939Config(t, side); !strings.Contains(cfg, want) {
		t.Fatalf("the new config lacks the land, autopilot and release defaults:\n%s", cfg)
	}
	side = b527Go(t, []string{"--no-interaction", "--skip-doctor", "--no-jev"}, b527Files(map[string]string{
		".code-forge.yml": "version: 1\nland:\n  require_ci: github\n  require_main_ci: true\nautopilot:\n  step_budgets:\n    margin: 1.5\n    coder: 30\nrelease:\n  max_blocks: 3\n",
	}))
	cfg := b939Config(t, side)
	for _, kept := range []string{"  require_ci: github\n", "  require_main_ci: true\n", "    margin: 1.5\n", "    coder: 30\n", "    min_samples: 5\n", "  max_blocks: 3\n"} {
		if !strings.Contains(cfg, kept) {
			t.Fatalf("init lost %q:\n%s", kept, cfg)
		}
	}
	if strings.Count(cfg, "require_main_ci:") != 1 || strings.Count(cfg, "margin:") != 1 || strings.Count(cfg, "max_blocks:") != 1 {
		t.Fatalf("init wrote a key twice:\n%s", cfg)
	}
}

// config fix leaves the new lane keys as they are (they are current keys, so
// no fixer rule rewrites them) and reports nothing unknown.
func TestW1bConfigFixKeepsNewLaneKeys(t *testing.T) {
	keys := "land:\n  require_main_ci: true # base must be green\nautopilot:\n  step_budgets:\n    margin: 0.25\n    min_samples: 3\n    ci_wait: 20\nrelease:\n  max_blocks: 5\n"
	old := b791Old + keys
	_, repo := smallProject(t, old)
	file := filepath.Join(repo, ".code-forge.yml")
	checkSmallCases(t, []smallCase{
		{[]string{"config", "fix"}, 0, b791Fix + file + ": fixed (1 rule); run code-forge validate to check it\n", ""},
		{[]string{"config", "fix"}, 0, file + ": config ok\n", ""},
	})
	b791Unchanged(t, file, strings.Replace(old, "    invariants: scripts/inv.sh # old\n", "", 1))
}
