package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestB1014ConfigFixKeepsRebaseSources(t *testing.T) {
	keys := "rebase:\n  approved_binary_sources: [design/approved/**] # owner approved\n"
	old := b791Old + keys
	_, repo := smallProject(t, old)
	file := filepath.Join(repo, ".code-forge.yml")
	checkSmallCases(t, []smallCase{{[]string{"config", "fix"}, 0, b791Fix + file + ": fixed (1 rule); run code-forge validate to check it\n", ""}, {[]string{"config", "fix"}, 0, file + ": config ok\n", ""}})
	b791Unchanged(t, file, strings.Replace(old, "    invariants: scripts/inv.sh # old\n", "", 1))
}

func TestB1014InitRebaseSources(t *testing.T) {
	t.Setenv("CODE_FORGE_RUN_ID", "")
	for _, existing := range []string{"", "version: 1\nrebase:\n  approved_binary_sources: [design/approved/**]\n"} {
		var setup func(string)
		if existing != "" {
			setup = b527Files(map[string]string{".code-forge.yml": existing})
		}
		side := b527Go(t, []string{"--no-interaction", "--skip-doctor", "--no-jev"}, setup)
		want := "approved_binary_sources: []"
		if existing != "" {
			want = "- design/approved/**"
		}
		if side.code != 0 || !strings.Contains(b939Config(t, side), want) {
			t.Fatalf("init exit=%d err=%s config=%s", side.code, side.stderr, b939Config(t, side))
		}
	}
}
