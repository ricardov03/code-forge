package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestB471UpgradeUsageAndSourceMatchNode(t *testing.T) {
	_, repo := smallProject(t, "")
	writeHomeFile(t, repo, "plan.md", "# plan\n")
	checkSmallCases(t, []smallCase{
		{[]string{"upgrade", "--help"}, 0, upgradeUsage, ""},
		{[]string{"upgrade", "-h"}, 0, upgradeUsage, ""},
		{[]string{"upgrade", "--bogus"}, 2, "", upgradeUsage},
		{[]string{"upgrade", "--source"}, 2, "", upgradeUsage},
		{[]string{"upgrade", "--source", ""}, 2, "", upgradeUsage},
		{[]string{"upgrade", "--source", "--plain"}, 2, "", upgradeUsage},
		{[]string{"upgrade", "--plain", "--plain"}, 2, "", upgradeUsage},
		{[]string{"upgrade", "--source=x"}, 2, "", upgradeUsage},
		{[]string{"upgrade", "x", "--help"}, 2, "", upgradeUsage},
		{[]string{"upgrade", "--source", "/nonexistent/x"}, 1, "", "upgrade: source \"/nonexistent/x\" does not exist\n"},
		{[]string{"upgrade", "--source", "plan.md"}, 1, "", "upgrade: source \"" + filepath.Join(repo, "plan.md") + "\" is not a directory\n"},
		{[]string{"upgrade", "--plain", "--source", "rel/none"}, 1, "", "upgrade: source \"" + filepath.Join(repo, "rel", "none") + "\" does not exist\n"},
	})
}

// Re-pointing the installs (with the release notes and docs sync) runs in
// Go now (B475); the cases B471 handed over all finish here.
func TestB471UpgradeRelinkRunsInGo(t *testing.T) {
	_, repo, root := upgradeFixture(t)
	for _, dir := range []string{filepath.Join(repo, "skill"), filepath.Join(repo, "a\x1b[31mb")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	checkSmallCases(t, []smallCase{
		{[]string{"upgrade"}, 0, upgradeBanner("first versioned upgrade") + "nothing to upgrade\n" + upgradeNotes(root) + "docs: kept packed 0.23.0\n", ""},
		{[]string{"upgrade", "--source", "skill", "--plain"}, 0, "nothing to upgrade\ndocs: kept packed 0.23.0\n", ""},
		{[]string{"upgrade", "--source", "a\x1b[31mb", "--plain"}, 0, "nothing to upgrade\ndocs: kept packed 0.23.0\n", ""},
	})
}
