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

// Re-pointing the installs (with the release notes and docs sync) and a path
// holding control characters go to Node.
func TestB471UpgradeRelinkHandsOver(t *testing.T) {
	_, repo := smallProject(t, "")
	if err := os.MkdirAll(filepath.Join(repo, "skill"), 0o700); err != nil {
		t.Fatal(err)
	}
	expectHandover(t, "upgrade-relink", "upgrade")
	expectHandover(t, "upgrade-relink", "upgrade", "--source", "skill", "--plain")
	expectHandover(t, "upgrade-control-chars", "upgrade", "--source", "a\x1b[31mb")
}
