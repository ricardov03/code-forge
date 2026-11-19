package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B785: a release refuses a new section with an entry before its first ### heading or a ### kind
// that appears twice, naming the line; a well-formed section releases.
func TestB785ReleaseChangelogShape(t *testing.T) {
	const tail = "## [0.1.0] — 2026-01-01\n\n### Added\n\n- First.\n"
	cases := []struct {
		name, unreleased, want string
	}{
		{"entry before first heading", "- Bare.\n\n### Fixed\n\n- A fix.\n",
			`CHANGELOG.md line 5: an entry of "## [Unreleased]" comes before its first ### heading: put each entry under ### Added, Changed, Deprecated, Removed, Fixed, Security`},
		{"only bare entries", "- Bare one.\n- Bare two.\n",
			`CHANGELOG.md line 5: an entry of "## [Unreleased]" comes before its first ### heading: put each entry under ### Added, Changed, Deprecated, Removed, Fixed, Security`},
		{"repeated heading", "### Changed\n\n- One.\n\n### Fixed\n\n- Two.\n\n### Changed\n\n- Three.\n",
			`CHANGELOG.md line 13: "### Changed" repeats in "## [Unreleased]" (first at line 5): merge them into one ### Changed heading`},
		{"repeated heading other case", "### Fixed\n\n- One.\n\n### fixed\n\n- Two.\n",
			`CHANGELOG.md line 9: "### fixed" repeats in "## [Unreleased]" (first at line 5): merge them into one ### fixed heading`},
		{"well formed", "### Added\n\n- New.\n\n### Changed\n\n- Other.\n\n### Fixed\n\n- A fix.\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text := "# Changelog\n\n## [Unreleased]\n\n" + tc.unreleased + "\n" + tail
			out, notes, err := releaseChangelog(text, "0.2.0", "2026-02-02")
			if tc.want == "" {
				if err != nil {
					t.Fatalf("refused a good section: %v", err)
				}
				if !strings.Contains(out, "## [0.2.0] — 2026-02-02\n\n"+tc.unreleased) || notes != strings.TrimSpace(tc.unreleased) {
					t.Fatalf("out=%q notes=%q", out, notes)
				}
				return
			}
			var r *refusal
			if !errors.As(err, &r) || r.msg != tc.want {
				t.Fatalf("err=%v\nwant %s", err, tc.want)
			}
		})
	}
}

// The repo's own CHANGELOG.md: every released section and [Unreleased] keep the shape the release
// now requires (B785 regrouped 0.25.0 and merged 0.24.0's two ### Fixed).
func TestB785RepoChangelogShape(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	for _, name := range []string{"Unreleased", "0.25.0", "0.24.0"} {
		sec := findSection(lines, name)
		if sec == nil {
			t.Fatalf("no ## [%s] section", name)
		}
		if msg := sectionShapeError(lines, sec); msg != "" {
			t.Errorf("## [%s]: %s", name, msg)
		}
	}
}
