package rebase

import (
	"strings"
	"testing"
)

// B890: a replay whose CHANGELOG.md ends with two `### Added` headings in
// ## [Unreleased] is refused before its result can be pushed (the B795
// rebase that turned CI red on TestB785RepoChangelogShape).

const b890Changelog = "# Changelog\n\n## [Unreleased]\n\n### Fixed\n\n- old fix (B1)\n\n## [0.1.0]\n\n- first release\n"

// b890Fixture: main and the block each add a `### Added` group in places git
// merges without a conflict.
func b890Fixture(t *testing.T) (*openFixture, string) {
	f := newOpenFixture(t, []string{"CHANGELOG.md"}, map[string]string{"CHANGELOG.md": b890Changelog})
	f.write(f.repo, "CHANGELOG.md", withLine(b890Changelog, "## [Unreleased]\n\n", "### Added\n\n- main add (B2)\n\n"))
	f.commit(f.repo, "B2 lands")
	f.write(f.tree, "CHANGELOG.md", withLine(b890Changelog, "- old fix (B1)\n\n", "### Added\n\n- block add (A)\n\n"))
	head := f.commit(f.tree, "A")
	f.save()
	return f, head
}

func TestB890RebaseRefusesRepeatedChangelogHeading(t *testing.T) {
	f, head := b890Fixture(t)
	_, err := RunPlain(f.home, "unit", "A")
	if err == nil || !strings.Contains(err.Error(), "rebase-changelog-shape") || !strings.Contains(err.Error(), "### added") {
		t.Fatalf("err = %v, want a rebase-changelog-shape refusal naming ### added", err)
	}
	if got := f.git(f.tree, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD = %s, want the entry HEAD %s", got, head)
	}
	if got := f.blockBase(); got != f.base {
		t.Fatalf("base_sha = %v after the refusal, want %s", got, f.base)
	}
	if rows := f.rows(); len(rows) != 0 {
		t.Fatalf("refusal wrote rows %v", rows)
	}
}

func TestB890ChangelogShapeKeepsAnOldRepeat(t *testing.T) {
	// A base that already repeats a kind is not the replay's fault.
	twice := "# Changelog\n\n## [Unreleased]\n\n### Added\n\n- a (B1)\n\n### Added\n\n- b (B1)\n\n## [0.1.0]\n"
	f := newOpenFixture(t, []string{"CHANGELOG.md"}, map[string]string{"CHANGELOG.md": twice})
	f.write(f.tree, "CHANGELOG.md", withLine(twice, "- b (B1)\n", "- c (A)\n"))
	f.commit(f.tree, "A")
	if err := ChangelogShape(f.tree, f.base); err != nil {
		t.Fatalf("an old repeat refused: %v", err)
	}
}
