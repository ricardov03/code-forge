package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// b951Commits gives the fixture's workspace a CHANGELOG.md, a v1.0.0 tag and
// n block commits after it.
func b951Commits(t *testing.T, ws string, n int) {
	t.Helper()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", ws}, args...)...)
		cmd.Env = append(envWithoutGit(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(ws, "CHANGELOG.md"), []byte("# Changelog\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "CHANGELOG.md")
	git("commit", "-q", "-m", "release 1.0.0")
	git("tag", "-a", "v1.0.0", "-m", "Release v1.0.0")
	for i := 1; i <= n; i++ {
		git("commit", "-q", "--allow-empty", "-m", "B"+strconv.Itoa(100+i)+": block "+strconv.Itoa(i))
	}
}

func b951Binnacle(t *testing.T) string {
	t.Helper()
	code, out, errText := autopilotCall(t, "binnacle", "--run", "r-test", "--markdown")
	if data, err := os.ReadFile(strings.TrimSpace(out)); err == nil {
		out = string(data)
	}
	if code != 0 {
		t.Fatalf("binnacle = %d %q %q", code, out, errText)
	}
	return out
}

// The binnacle shows a Release size item at 8 blocks since the tag, and none at 4.
func TestB951BinnacleReleaseSize(t *testing.T) {
	f := b950Run(t, map[string]any{})
	b951Commits(t, f.workspace, 8)
	if out := b951Binnacle(t); !strings.Contains(out, "| Release size | RELEASE NOW: 8 blocks since v1.0.0 (release.max_blocks 8): make a release before more blocks land |") {
		t.Fatalf("8 blocks: %s", out)
	}
	g := b950Run(t, map[string]any{})
	b951Commits(t, g.workspace, 4)
	if out := b951Binnacle(t); strings.Contains(out, "Release size") {
		t.Fatalf("4 blocks: %s", out)
	}
}
