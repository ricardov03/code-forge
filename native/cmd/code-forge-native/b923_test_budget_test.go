package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B923: every coder context pack and every spawned coder prompt carries the
// CPU budget rule, with the -p of gates.coder_test_parallel (default 2).

const b923Rule = "run only the test names you add or change: go test <pkg> -run '^(TestA|TestB)$' -count=1 -p %P (or your stack's single-test form). No package-wide or full-suite runs, and no stress runs (-count=N) unless the owner asks. GitHub CI is the full gate."

func b923RuleFor(p string) string { return strings.ReplaceAll(b923Rule, "%P", p) }

func TestB923ContextPackCarriesTestBudget(t *testing.T) {
	for _, tc := range []struct{ name, gates, p string }{
		{"default", "", "2"},
		{"configured", "gates:\n  coder_test_parallel: 3\n", "3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := "version: 1\n" + tc.gates + "context:\n  commands:\n    test: [go, test]\n"
			newContextFixture(t, map[string]any{"B1": map[string]any{"status": "open", "level": "L1", "base_branch": "main", "worktree": map[string]any{"branch": "forge/r-ctx/B1"}}}, config)
			code, out, errText := runCaptured(t, "context", "pack", "--run", "r-ctx", "--block", "B1")
			if code != 0 || errText != "" {
				t.Fatalf("code=%d stderr=%q", code, errText)
			}
			want := "## test budget (code-forge)\n- " + b923RuleFor(tc.p) + "\n"
			if !strings.Contains(out, want) {
				t.Fatalf("pack lacks %q:\n%s", want, out)
			}
		})
	}
}

func TestB923SpawnedCoderPromptCarriesTestBudget(t *testing.T) {
	for _, tc := range []struct{ name, gates, p string }{
		{"default", "", "2"},
		{"configured", "gates:\n  coder_test_parallel: 4\n", "4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := b549Fixture(t, "ok", "")
			b460Write(t, filepath.Join(w, "repo", ".code-forge.yml"), b549Config+tc.gates)
			code, out, errText := b460Call(t, "spawn", "--level", "L1", "--role", "coder", "--brief", "brief.md", "--block", "B1")
			if code != 0 {
				t.Fatalf("code=%d out=%q err=%q", code, out, errText)
			}
			prompt, err := os.ReadFile(filepath.Join(w, "log", "prompt.txt"))
			if err != nil {
				t.Fatal(err)
			}
			want := "\n\nTest budget (code-forge): " + b923RuleFor(tc.p) + "\n\nReport the changed files"
			if !strings.Contains(string(prompt), want) {
				t.Fatalf("prompt lacks %q:\n%s", want, prompt)
			}
		})
	}
}
