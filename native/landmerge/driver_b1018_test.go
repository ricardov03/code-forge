package landmerge

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestB1018MergeDriverDuplicateBlocks(t *testing.T) {
	for _, tc := range []struct {
		name, ours, theirs string
		bad                bool
	}{
		{"prefix", "- B998: first wording\n", "- B998: near duplicate\n", true},
		{"suffix", "- first wording (B998).\n", "- near duplicate (B998)\n", true},
		{"different", "- B998: first wording\n", "- B999: another block\n", false},
		{"identical", "- B998: same wording\n", "- B998: same wording\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CODE_FORGE_RUN_ID", "")
			dir := t.TempDir()
			base := filepath.Join(dir, "base")
			ours := filepath.Join(dir, "ours")
			theirs := filepath.Join(dir, "theirs")
			head := "# Changelog\n\n## [Unreleased]\n\n### Fixed\n\n"
			for path, body := range map[string]string{base: head, ours: head + tc.ours, theirs: head + tc.theirs} {
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := MergeDriver(base, ours, theirs, "CHANGELOG.md")
			got, e := os.ReadFile(ours)
			if e != nil {
				t.Fatal(e)
			}
			if tc.bad {
				if err == nil || !strings.Contains(err.Error(), "duplicate block B998") {
					t.Fatalf("merge err=%v", err)
				}
				if string(got) != head+tc.ours {
					t.Fatalf("failed merge overwrote ours: %s", got)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(got), tc.ours) || !strings.Contains(string(got), tc.theirs) {
					t.Fatalf("lost entry: %s", got)
				}
			}
		})
	}
}

func TestB1018DriverKeepsRefusal(t *testing.T) {
	t.Setenv("CODE_FORGE_RUN_ID", "")
	dir := t.TempDir()
	base := filepath.Join(dir, "base")
	ours := filepath.Join(dir, "ours")
	theirs := filepath.Join(dir, "theirs")
	exe := filepath.Join(dir, "code-forge")
	for path, body := range map[string]string{base: "base\n", ours: "ours\n", theirs: "theirs\n", exe: "#!/bin/sh\necho 'duplicate block B998' >&2\nexit 1\n"} {
		if err := os.WriteFile(path, []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	line := strings.NewReplacer("%O", shellQuote(base), "%A", shellQuote(ours), "%B", shellQuote(theirs), "%P", "CHANGELOG.md").Replace(UnionDriverLine(exe))
	out, err := exec.Command("sh", "-c", line).CombinedOutput()
	if e, ok := err.(*exec.ExitError); !ok || e.ExitCode() != 1 || !strings.Contains(string(out), "duplicate block B998") {
		t.Fatalf("status=%v out=%s", err, out)
	}
	got, err := os.ReadFile(ours)
	if err != nil || string(got) != "ours\n" {
		t.Fatalf("refusal overwritten: %s %v", got, err)
	}
}
