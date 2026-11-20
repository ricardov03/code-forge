package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/landmerge"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/releasecheck"
	"github.com/ricardov03/code-forge/native/state"
)

// B813: a CHANGELOG.md merge git finishes without a conflict can leave two
// `### Added` headings in [Unreleased]; land folds them into one, every line
// kept.

const b813Changelog = "# Changelog\n\n## [Unreleased]\n\n### Fixed\n\n- old fix (B1)\n\n## [0.1.0]\n\n- first release\n"

func b813Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = dir, gitx.ChildEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func b813Write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestB813LandMergeFoldsCleanChangelogHeadings(t *testing.T) {
	repo := t.TempDir()
	b813Git(t, repo, "init", "-qb", "main")
	b813Git(t, repo, "config", "user.name", "T")
	b813Git(t, repo, "config", "user.email", "t@example.com")
	b813Git(t, repo, "config", "commit.gpgsign", "false")
	b813Write(t, repo, "CHANGELOG.md", b813Changelog)
	b813Git(t, repo, "add", ".")
	b813Git(t, repo, "commit", "-qm", "base")
	b813Git(t, repo, "checkout", "-qb", "block")
	b813Write(t, repo, "CHANGELOG.md", strings.Replace(b813Changelog, "- old fix (B1)\n\n", "- old fix (B1)\n\n### Added\n\n- block add (A)\n\n", 1))
	b813Git(t, repo, "commit", "-qam", "A")
	b813Git(t, repo, "checkout", "-q", "main")
	b813Write(t, repo, "CHANGELOG.md", strings.Replace(b813Changelog, "## [Unreleased]\n\n", "## [Unreleased]\n\n### Added\n\n- main add (B2)\n\n", 1))
	b813Git(t, repo, "commit", "-qam", "B2")

	resolved, conflicts, output, err := lcfMerge(repo, "block", landmerge.Defaults(), "", nil)
	if err != nil || len(conflicts) != 0 || len(resolved) != 0 {
		t.Fatalf("merge: resolved=%v conflicts=%v err=%v output=%s", resolved, conflicts, err, output)
	}
	got := b813Git(t, repo, "show", "HEAD:CHANGELOG.md") + "\n"
	want := "# Changelog\n\n## [Unreleased]\n\n### Added\n\n- main add (B2)\n- block add (A)\n\n### Fixed\n\n- old fix (B1)\n\n## [0.1.0]\n\n- first release\n"
	if got != want {
		t.Fatalf("CHANGELOG.md =\n%s\nwant:\n%s", got, want)
	}
	if repeats := releasecheck.ChangelogRepeats(got); len(repeats) != 0 {
		t.Fatalf("shape check: repeats %v", repeats)
	}
	if parents := strings.Fields(b813Git(t, repo, "show", "-s", "--format=%P", "HEAD")); len(parents) != 2 {
		t.Fatalf("HEAD parents = %v, want a merge commit", parents)
	}
	if status := b813Git(t, repo, "status", "--porcelain"); status != "" {
		t.Fatalf("checkout not clean: %q", status)
	}
}

// A clean merge with nothing to fold stays the plain merge commit.
func TestB813LandMergeWithoutRepeatIsUnchanged(t *testing.T) {
	repo := t.TempDir()
	b813Git(t, repo, "init", "-qb", "main")
	b813Git(t, repo, "config", "user.name", "T")
	b813Git(t, repo, "config", "user.email", "t@example.com")
	b813Write(t, repo, "CHANGELOG.md", b813Changelog)
	b813Write(t, repo, "a.txt", "a\n")
	b813Git(t, repo, "add", ".")
	b813Git(t, repo, "commit", "-qm", "base")
	b813Git(t, repo, "checkout", "-qb", "block")
	b813Write(t, repo, "a.txt", "a block\n")
	b813Git(t, repo, "commit", "-qam", "A")
	b813Git(t, repo, "checkout", "-q", "main")
	if _, conflicts, _, err := lcfMerge(repo, "block", landmerge.Defaults(), "", nil); err != nil || len(conflicts) != 0 {
		t.Fatalf("merge: conflicts=%v err=%v", conflicts, err)
	}
	if got := b813Git(t, repo, "show", "-s", "--format=%s", "HEAD"); got != "Merge branch 'block'" {
		t.Fatalf("merge subject = %q", got)
	}
}

// A batch: B1 and B2 each add a `### Added` group in a different place of
// [Unreleased]; B2's rebase onto B1 is clean. Main ends with one heading.
func TestB813BatchLandFoldsChangelogHeadings(t *testing.T) {
	f := newLandBatchFixture(t, 0)
	edits := map[string]string{
		"B1": strings.Replace(landChangelog, "## [Unreleased]\n\n", "## [Unreleased]\n\n### Added\n\n- entry B1\n\n### Fixed\n\n", 1),
		"B2": landChangelog + "\n### Added\n\n- entry B2\n",
	}
	for _, id := range []string{"B1", "B2"} {
		tree := filepath.Join(f.repo, ".code-forge/worktrees", id)
		f.write(tree, "CHANGELOG.md", edits[id])
		f.git(tree, "commit", "-qam", id+" changelog")
		if err := state.UpdateRun(f.home, "r-batch-unit", func(record *ledger.Object) error {
			blocks := blockObject(record.Get("blocks"))
			entry := blockObject(blocks.Get(id))
			entry.Set("owned_files", []any{"src/" + id + ".mjs", "CHANGELOG.md"})
			blocks.Set(id, entry)
			record.Set("blocks", blocks)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	code, output := f.land("B1", "B2")
	if code != 0 {
		t.Fatalf("batch exit=%d, want 0: %s", code, output)
	}
	f.checkMain([]string{"B1", "B2"})
	main := f.git(f.repo, "show", "main:CHANGELOG.md")
	if n := strings.Count(main, "### Added"); n != 1 {
		t.Fatalf("main CHANGELOG.md has %d ### Added headings:\n%s", n, main)
	}
	for _, entry := range []string{"- entry B1", "- entry B2", "- older entry (B0)"} {
		if strings.Count(main, entry) != 1 {
			t.Fatalf("main CHANGELOG.md lost %q:\n%s", entry, main)
		}
	}
}
