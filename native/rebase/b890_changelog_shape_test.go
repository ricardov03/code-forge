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

func TestB890ChangelogShapeRefusesNewRepeat(t *testing.T) {
	f := newOpenFixture(t, []string{"CHANGELOG.md"}, map[string]string{"CHANGELOG.md": b890Changelog})
	f.write(f.tree, "CHANGELOG.md", withLine(b890Changelog, "- old fix (B1)\n\n", "### Added\n\n- a (A)\n\n### Added\n\n- b (A)\n\n"))
	f.commit(f.tree, "A")
	err := ChangelogShape(f.tree, f.base)
	if err == nil || !strings.Contains(err.Error(), "### added") {
		t.Fatalf("err = %v, want a refusal naming ### added", err)
	}
}

// B813: the clean replay of b890Fixture folds the block's `### Added` into
// main's, keeps every line and still records the rebase.
func TestB813RebaseFoldsRepeatedChangelogHeading(t *testing.T) {
	f, _ := b890Fixture(t)
	_, err := RunPlain(f.home, "unit", "A")
	f.requireRebased(err, []string{"CHANGELOG.md"})
	got := f.read(f.tree, "CHANGELOG.md")
	want := "# Changelog\n\n## [Unreleased]\n\n### Added\n\n- main add (B2)\n- block add (A)\n\n### Fixed\n\n- old fix (B1)\n\n## [0.1.0]\n\n- first release\n"
	if got != want {
		t.Fatalf("CHANGELOG.md =\n%s\nwant:\n%s", got, want)
	}
	if status := f.git(f.tree, "status", "--porcelain"); status != "" {
		t.Fatalf("worktree not clean: %q", status)
	}
	if n := f.git(f.tree, "rev-list", "--count", "main..HEAD"); n != "1" {
		t.Fatalf("replayed commits = %s, want 1 (the fold is in the block's commit)", n)
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
