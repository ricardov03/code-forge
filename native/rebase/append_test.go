package rebase

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B431 CHANGELOG.md append-only rule, through Run on real repositories.

func (f *openFixture) config() string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.repo, ".git", "config"))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(data)
}

func (f *openFixture) requireNoAttributes() {
	f.t.Helper()
	for _, file := range []string{filepath.Join(f.repo, ".gitattributes"), filepath.Join(f.tree, ".gitattributes"), filepath.Join(f.repo, ".git", "info", "attributes")} {
		if _, err := os.Stat(file); err == nil {
			f.t.Fatalf("%s was created", file)
		}
	}
}

// Two block commits each add a line where main added two: every step of the
// replay merges, main's lines come first, and no repo file or config changes.
func TestChangelogAppendKeepsBothSidesAcrossCommits(t *testing.T) {
	f := newOpenFixture(t, []string{"CHANGELOG.md"}, map[string]string{"CHANGELOG.md": changelogBase})
	f.write(f.repo, "CHANGELOG.md", withLine(changelogBase, "## [Unreleased]\n\n", "- main one (B2)\n- main two (B3)\n"))
	f.commit(f.repo, "B2 and B3 land")
	f.write(f.tree, "CHANGELOG.md", withLine(changelogBase, "## [Unreleased]\n\n", "- block one (A)\n"))
	f.commit(f.tree, "A one")
	f.write(f.tree, "CHANGELOG.md", withLine(changelogBase, "## [Unreleased]\n\n", "- block one (A)\n- block two (A)\n"))
	f.commit(f.tree, "A two")
	f.save()
	config := f.config()

	_, err := Run(f.home, "unit", "A")
	f.requireRebased(err, []string{"CHANGELOG.md"})
	want := withLine(changelogBase, "## [Unreleased]\n\n", "- main one (B2)\n- main two (B3)\n- block one (A)\n- block two (A)\n")
	if got := f.read(f.tree, "CHANGELOG.md"); got != want {
		t.Fatalf("CHANGELOG.md =\n%s\nwant:\n%s", got, want)
	}
	if got := f.git(f.tree, "rev-list", "--count", "main..HEAD"); got != "2" {
		t.Fatalf("replayed commits = %s, want 2", got)
	}
	if f.config() != config {
		t.Fatalf(".git/config changed:\n%s", f.config())
	}
	f.requireNoAttributes()
}

// When the block changes an existing line next to main's addition, the
// CHANGELOG.md conflict is real.
func TestChangelogChangedLineStaysConflict(t *testing.T) {
	f := newOpenFixture(t, []string{"CHANGELOG.md"}, map[string]string{"CHANGELOG.md": changelogBase})
	f.write(f.repo, "CHANGELOG.md", withLine(changelogBase, "## [Unreleased]\n\n", "- main entry (B2)\n"))
	f.commit(f.repo, "B2 lands")
	f.write(f.tree, "CHANGELOG.md", strings.Replace(changelogBase, "- older entry (B1)\n", "- block entry (A)\n- older entry, reworded (B1)\n", 1))
	head := f.commit(f.tree, "A rewords")
	f.save()
	config := f.config()

	_, err := Run(f.home, "unit", "A")
	f.requireConflict(err, head, "CHANGELOG.md")
	if f.config() != config {
		t.Fatalf(".git/config changed:\n%s", f.config())
	}
	f.requireNoAttributes()
}

// Main removes a line the block's addition sits next to: a real conflict.
func TestChangelogMainRemovedLineStaysConflict(t *testing.T) {
	f := newOpenFixture(t, []string{"CHANGELOG.md"}, map[string]string{"CHANGELOG.md": changelogBase})
	f.write(f.repo, "CHANGELOG.md", strings.Replace(changelogBase, "- older entry (B1)\n", "- main entry (B2)\n", 1))
	f.commit(f.repo, "B2 replaces B1")
	f.write(f.tree, "CHANGELOG.md", withLine(changelogBase, "## [Unreleased]\n\n", "- block entry (A)\n"))
	head := f.commit(f.tree, "A")
	f.save()

	_, err := Run(f.home, "unit", "A")
	f.requireConflict(err, head, "CHANGELOG.md")
}

// The rule is for CHANGELOG.md only: another file where both sides add lines
// at the same place stays a conflict.
func TestAppendRuleOnlyForChangelog(t *testing.T) {
	f := newOpenFixture(t, []string{"NOTES.md"}, map[string]string{"NOTES.md": changelogBase})
	f.write(f.repo, "NOTES.md", withLine(changelogBase, "## [Unreleased]\n\n", "- main entry (B2)\n"))
	f.commit(f.repo, "B2 lands")
	f.write(f.tree, "NOTES.md", withLine(changelogBase, "## [Unreleased]\n\n", "- block entry (A)\n"))
	head := f.commit(f.tree, "A")
	f.save()

	_, err := Run(f.home, "unit", "A")
	f.requireConflict(err, head, "NOTES.md")
}

// A repository may mark CHANGELOG.md merge=union (code-forge's own repo does,
// B396). git's union driver keeps both sides of a conflicted region, so a line
// the block removed would come back. The replay keeps every removal: next to
// main's addition it is a real conflict with nothing changed; away from it the
// replay is clean and the line stays removed. Appends still keep both sides,
// and another merge=union file is still merged by union.
func TestUnionAttributeKeepsBlockRemoval(t *testing.T) {
	attrs := "CHANGELOG.md merge=union\nNOTES.md merge=union\n"
	removed := strings.Replace(changelogBase, "- older entry (B1)\n", "", 1)
	t.Run("removal next to main's line", func(t *testing.T) {
		f := newOpenFixture(t, []string{"CHANGELOG.md"}, map[string]string{"CHANGELOG.md": changelogBase, ".gitattributes": attrs})
		f.write(f.repo, "CHANGELOG.md", withLine(changelogBase, "## [Unreleased]\n\n", "- main entry (B2)\n"))
		f.commit(f.repo, "B2 lands")
		f.write(f.tree, "CHANGELOG.md", removed)
		head := f.commit(f.tree, "A removes B1")
		f.save()

		_, err := Run(f.home, "unit", "A")
		f.requireConflict(err, head, "CHANGELOG.md")
		if got := f.read(f.tree, "CHANGELOG.md"); got != removed {
			t.Fatalf("CHANGELOG.md =\n%s\nwant the block's removal kept:\n%s", got, removed)
		}
	})
	t.Run("removal away from main's line", func(t *testing.T) {
		f := newOpenFixture(t, []string{"CHANGELOG.md"}, map[string]string{"CHANGELOG.md": changelogBase, ".gitattributes": attrs})
		f.write(f.repo, "CHANGELOG.md", withLine(changelogBase, "## [0.1.0]\n\n", "- main fix (B2)\n"))
		f.commit(f.repo, "B2 lands")
		f.write(f.tree, "CHANGELOG.md", removed)
		f.commit(f.tree, "A removes B1")
		f.save()

		_, err := Run(f.home, "unit", "A")
		f.requireRebased(err, []string{"CHANGELOG.md"})
		if got, want := f.read(f.tree, "CHANGELOG.md"), withLine(removed, "## [0.1.0]\n\n", "- main fix (B2)\n"); got != want {
			t.Fatalf("CHANGELOG.md =\n%s\nwant:\n%s", got, want)
		}
	})
	t.Run("both append", func(t *testing.T) {
		f := newOpenFixture(t, []string{"CHANGELOG.md", "NOTES.md"}, map[string]string{"CHANGELOG.md": changelogBase, "NOTES.md": changelogBase, ".gitattributes": attrs})
		for _, name := range []string{"CHANGELOG.md", "NOTES.md"} {
			f.write(f.repo, name, withLine(changelogBase, "## [Unreleased]\n\n", "- main entry (B2)\n"))
			f.write(f.tree, name, withLine(changelogBase, "## [Unreleased]\n\n", "- block entry (A)\n"))
		}
		f.commit(f.repo, "B2 lands")
		f.commit(f.tree, "A")
		f.save()

		_, err := Run(f.home, "unit", "A")
		f.requireRebased(err, []string{"CHANGELOG.md", "NOTES.md"})
		want := withLine(changelogBase, "## [Unreleased]\n\n", "- main entry (B2)\n- block entry (A)\n")
		for _, name := range []string{"CHANGELOG.md", "NOTES.md"} {
			if got := f.read(f.tree, name); got != want {
				t.Fatalf("%s =\n%s\nwant:\n%s", name, got, want)
			}
		}
	})
}

// A CHANGELOG.md whose last line has no newline, both sides adding a line
// after it (and one side also one before it): the last line stays once and
// every added line is kept.
func TestAppendMergeUnterminatedLastLine(t *testing.T) {
	base := "# Changelog\n\n- older entry (B1)"
	cases := []struct{ name, ours, theirs, want string }{
		{"both add after", base + "\n- main entry (B2)\n", base + "\n- block entry (A)\n",
			"# Changelog\n\n- older entry (B1)\n- main entry (B2)\n- block entry (A)\n"},
		{"theirs also adds before", base + "\n- main entry (B2)\n", "# Changelog\n\n- block top (A)\n- older entry (B1)\n- block entry (A)\n",
			"# Changelog\n\n- block top (A)\n- older entry (B1)\n- main entry (B2)\n- block entry (A)\n"},
		{"ours also adds before", "# Changelog\n\n- main top (B2)\n- older entry (B1)\n- main entry (B2)\n", base + "\n- block entry (A)\n",
			"# Changelog\n\n- main top (B2)\n- older entry (B1)\n- main entry (B2)\n- block entry (A)\n"},
		{"no side ends with a newline", base + "\n- main entry (B2)", "# Changelog\n\n- block top (A)\n" + base[len("# Changelog\n\n"):] + "\n- block entry (A)",
			"# Changelog\n\n- block top (A)\n- older entry (B1)\n- main entry (B2)\n- block entry (A)"},
	}
	for _, c := range cases {
		got, ok, err := AppendMerge(base, c.ours, c.theirs)
		if err != nil || !ok {
			t.Fatalf("%s: AppendMerge ok=%v err=%v, want a merge", c.name, ok, err)
		}
		if got != c.want {
			t.Fatalf("%s: merged =\n%q\nwant:\n%q", c.name, got, c.want)
		}
	}
}

// The same case through a real replay: main appends after the unterminated
// last line, the block adds one line before it and one after it.
func TestChangelogAppendAfterUnterminatedLastLine(t *testing.T) {
	base := "# Changelog\n\n- older entry (B1)"
	f := newOpenFixture(t, []string{"CHANGELOG.md"}, map[string]string{"CHANGELOG.md": base})
	f.write(f.repo, "CHANGELOG.md", base+"\n- main entry (B2)\n")
	f.commit(f.repo, "B2 lands")
	f.write(f.tree, "CHANGELOG.md", "# Changelog\n\n- block top (A)\n- older entry (B1)\n- block entry (A)\n")
	f.commit(f.tree, "A")
	f.save()

	_, err := Run(f.home, "unit", "A")
	f.requireRebased(err, []string{"CHANGELOG.md"})
	if got, want := f.read(f.tree, "CHANGELOG.md"), "# Changelog\n\n- block top (A)\n- older entry (B1)\n- main entry (B2)\n- block entry (A)\n"; got != want {
		t.Fatalf("CHANGELOG.md =\n%q\nwant:\n%q", got, want)
	}
}

// Each replayed commit keeps its own committer when an add-only CHANGELOG.md
// stop is merged and the replay goes on.
func TestChangelogAppendKeepsCommitters(t *testing.T) {
	f := newOpenFixture(t, []string{"CHANGELOG.md"}, map[string]string{"CHANGELOG.md": changelogBase})
	f.write(f.repo, "CHANGELOG.md", withLine(changelogBase, "## [Unreleased]\n\n", "- main one (B2)\n"))
	f.commit(f.repo, "B2 lands")
	for i, who := range []string{"First Committer <first@example.invalid>", "Second Committer <second@example.invalid>"} {
		lines := []string{"- block one (A)\n", "- block one (A)\n- block two (A)\n"}
		f.write(f.tree, "CHANGELOG.md", withLine(changelogBase, "## [Unreleased]\n\n", lines[i]))
		name, email, _ := strings.Cut(strings.TrimSuffix(who, ">"), " <")
		f.git(f.tree, "add", "-A")
		env := append(gitEnv(), "GIT_COMMITTER_NAME="+name, "GIT_COMMITTER_EMAIL="+email)
		if _, err := gitWithEnv(f.tree, env, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "A"); err != nil {
			t.Fatal(err)
		}
	}
	f.save()

	_, err := Run(f.home, "unit", "A")
	f.requireRebased(err, []string{"CHANGELOG.md"})
	if got, want := f.git(f.tree, "log", "--reverse", "--format=%cn <%ce>", "main..HEAD"), "First Committer <first@example.invalid>\nSecond Committer <second@example.invalid>"; got != want {
		t.Fatalf("committers =\n%s\nwant:\n%s", got, want)
	}
}
