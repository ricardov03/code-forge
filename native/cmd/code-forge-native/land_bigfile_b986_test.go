package main

import (
	"bytes"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/gitx"
)

// B986 tests: built binaries are ignored, and land refuses to push or merge
// commits that add a file over land.max_file_mb. Remotes are local bare repos.

func TestB986BuiltBinariesIgnored(t *testing.T) {
	top, err := gitx.RepoTop(".")
	if err != nil {
		t.Skip("not inside the code-forge git checkout")
	}
	for _, path := range []string{"native/code-forge-native", "native/cf-gotest", "native/cf-release", "native/cmd/code-forge-native/code-forge-native", "native/cmd/cf-release/cf-release"} {
		cmd := exec.Command("git", "check-ignore", "-q", "--no-index", path)
		cmd.Dir, cmd.Env = top, gitx.ChildEnv()
		if err := cmd.Run(); err != nil {
			t.Errorf("%s is not ignored: a go build in native/ would leave it for a commit (%v)", path, err)
		}
	}
	// Source next to the binaries stays visible.
	cmd := exec.Command("git", "check-ignore", "-q", "--no-index", "native/cmd/code-forge-native/main.go")
	cmd.Dir, cmd.Env = top, gitx.ChildEnv()
	if cmd.Run() == nil {
		t.Errorf("native/cmd/code-forge-native/main.go must not be ignored")
	}
}

// b986Repo is a repo with one pushed commit on main and a bare origin.
func b986Repo(t *testing.T) (repo, bare string, git func(dir string, args ...string) string) {
	t.Helper()
	repo, bare = t.TempDir(), t.TempDir()
	git = func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, gitx.ChildEnv()
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(bare, "init", "-q", "--bare")
	git(repo, "init", "-qb", "main")
	git(repo, "config", "user.name", "Fixture")
	git(repo, "config", "user.email", "f@example.invalid")
	git(repo, "config", "commit.gpgsign", "false")
	b986Commit(t, git, repo, "README.md", 10, "base")
	git(repo, "remote", "add", "origin", bare)
	git(repo, "push", "-q", "origin", "main")
	return repo, bare, git
}

func b986Commit(t *testing.T, git func(string, ...string) string, repo, name string, size int, message string) {
	t.Helper()
	f := &landBatchFixture{t: t}
	f.write(repo, name, strings.Repeat("x", size))
	git(repo, "add", name)
	git(repo, "commit", "-qm", message)
}

func b986RemoteHas(git func(string, ...string) string, bare, ref string) bool {
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", ref)
	cmd.Dir, cmd.Env = bare, gitx.ChildEnv()
	return cmd.Run() == nil
}

func TestB986PushRefusesBigFile(t *testing.T) {
	repo, bare, git := b986Repo(t)
	b986Commit(t, git, repo, "native/code-forge-native", 6_000_001, "built binary")
	b986Commit(t, git, repo, "notes.md", 100, "later small change")
	var out bytes.Buffer
	_, _, err := landGitPush(repo, "origin", "HEAD:refs/heads/ci-b1", 5_000_000, &out)
	var big *landBigFileError
	if !errors.As(err, &big) {
		t.Fatalf("push err=%v, want a size refusal", err)
	}
	for _, want := range []string{"native/code-forge-native (6.0 MB)", "land.max_file_mb (5 MB)", ".gitignore"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q must name %q", err.Error(), want)
		}
	}
	if strings.Contains(err.Error(), "notes.md") || strings.Contains(err.Error(), "README.md") {
		t.Errorf("refusal names only the big file: %q", err.Error())
	}
	if b986RemoteHas(git, bare, "refs/heads/ci-b1") {
		t.Errorf("the remote got refs/heads/ci-b1; nothing may be pushed")
	}
	// The main push of land: refused with the merge kept local, no heal item.
	home := t.TempDir()
	t.Setenv("HOME", home)
	_, err = landPush(home, "r-b986", []string{"B1"}, repo, "main", false, 5_000_000, &out)
	if err == nil || !strings.Contains(err.Error(), "merged into local main but did not push") || strings.Contains(err.Error(), "heal: queued") {
		t.Errorf("landPush err=%v, want the local-merge size refusal without a heal item", err)
	}
	if items := queuedHeal(t, home, "r-b986"); len(items) != 0 {
		t.Errorf("heal items=%v, want none: a retry would fail the same way", items)
	}
	if got, want := git(bare, "rev-parse", "refs/heads/main"), git(repo, "rev-parse", "HEAD~2"); got != want {
		t.Errorf("remote main=%s, want it unchanged at %s", got, want)
	}
}

func TestB986PushUnderLimit(t *testing.T) {
	repo, bare, git := b986Repo(t)
	b986Commit(t, git, repo, "assets/logo.png", 1_000_000, "small asset")
	var out bytes.Buffer
	if _, _, err := landGitPush(repo, "origin", "HEAD:refs/heads/ci-b1", 5_000_000, &out); err != nil {
		t.Fatalf("push under the limit: %v", err)
	}
	if !b986RemoteHas(git, bare, "refs/heads/ci-b1") {
		t.Errorf("the remote has no refs/heads/ci-b1 after a push under the limit")
	}
	// 0 turns the check off: a big file pushes.
	b986Commit(t, git, repo, "big.bin", 6_000_001, "big")
	if _, _, err := landGitPush(repo, "origin", "HEAD:refs/heads/ci-b2", 0, &out); err != nil {
		t.Fatalf("push with the check off: %v", err)
	}
	if !b986RemoteHas(git, bare, "refs/heads/ci-b2") {
		t.Errorf("the remote has no refs/heads/ci-b2 with land.max_file_mb 0")
	}
}

func TestB986LimitConfig(t *testing.T) {
	cases := []struct {
		cfg  map[string]any
		want int64
	}{
		{nil, 5_000_000},
		{map[string]any{"land": map[string]any{"push": true}}, 5_000_000},
		{map[string]any{"land": map[string]any{"max_file_mb": float64(0)}}, 0},
		{map[string]any{"land": map[string]any{"max_file_mb": float64(12)}}, 12_000_000},
		{map[string]any{"land": map[string]any{"max_file_mb": 0.5}}, 500_000},
	}
	for _, c := range cases {
		if got := landBigFileLimit(c.cfg); got != c.want {
			t.Errorf("landBigFileLimit(%v)=%d, want %d", c.cfg, got, c.want)
		}
	}
	// The schema takes the key; a negative size is a schema issue.
	landIssues := func(mb float64) []config.SchemaIssue {
		out := []config.SchemaIssue{}
		for _, issue := range config.SchemaIssues(map[string]any{"land": map[string]any{"max_file_mb": mb}}, "1") {
			if strings.HasPrefix(issue.Path, "/land") {
				out = append(out, issue)
			}
		}
		return out
	}
	if issues := landIssues(12); len(issues) != 0 {
		t.Errorf("land.max_file_mb 12: schema issues %v, want none", issues)
	}
	if issues := landIssues(-1); len(issues) == 0 {
		t.Errorf("land.max_file_mb -1: no schema issue, want one")
	}
}

func TestB986LandRefusesBigFile(t *testing.T) {
	f := newLandBatchFixture(t, 0)
	tree := filepath.Join(f.repo, ".code-forge/worktrees", "B1")
	f.write(tree, "src/B1.mjs", strings.Repeat("green B1\n", 700_000))
	f.git(tree, "commit", "-qam", "B1 big")
	code, output := f.land("B1", "B2")
	if code == 0 {
		t.Fatalf("exit=0, want a refusal: %s", output)
	}
	for _, want := range []string{"block B1 land stopped at size:", "src/B1.mjs (6.3 MB)", "land.max_file_mb"} {
		if !strings.Contains(output, want) {
			t.Errorf("output must contain %q: %s", want, output)
		}
	}
	if got := f.git(f.repo, "rev-parse", "main"); got != f.base {
		t.Errorf("main=%s, want it unchanged at %s", got, f.base)
	}
}
