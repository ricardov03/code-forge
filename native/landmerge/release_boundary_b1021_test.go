package landmerge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestB1021ReleaseBoundary(t *testing.T) {
	base := "# Changelog\n\n## [Unreleased]\n\n### Fixed\n\n- shipped (B1)\n  shipped detail\n\n## [0.1.0]\n\n- old (B0)\n"
	released := strings.Replace(base, "### Fixed", "## [0.2.0]\n\n### Fixed", 1)
	released = strings.Replace(released, "## [Unreleased]\n\n", "## [Unreleased]\n\n### Added\n\n- main next (B2)\n\n", 1)
	lane := strings.Replace(base, "  shipped detail\n", "  shipped detail\n- lane next (B3)\n  lane detail\n", 1)
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "release on ours", true: "release on theirs"}[reverse], func(t *testing.T) {
			dir := t.TempDir()
			ours, theirs := released, lane
			if reverse {
				ours, theirs = theirs, ours
			}
			for name, body := range map[string]string{"base": base, "ours": ours, "theirs": theirs} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := MergeDriver(filepath.Join(dir, "base"), filepath.Join(dir, "ours"), filepath.Join(dir, "theirs"), "CHANGELOG.md"); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "ours"))
			if err != nil {
				t.Fatal(err)
			}
			got := string(data)
			u, rest, ok := strings.Cut(got, "## [0.2.0]")
			if !ok || !strings.Contains(u, "- lane next (B3)\n  lane detail") || !strings.Contains(u, "- main next (B2)") || strings.Contains(u, "shipped") || strings.Contains(rest, "lane next") || strings.Contains(rest, "main next") {
				t.Fatalf("entries crossed release boundary:\n%s", got)
			}
			if rest != strings.SplitN(released, "## [0.2.0]", 2)[1] {
				t.Fatalf("released history changed:\n%s", got)
			}
		})
	}
}
