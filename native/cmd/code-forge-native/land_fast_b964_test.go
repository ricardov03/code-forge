package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B964 tests. The unit tests run landFastChecks on a throwaway repository:
// main holds the base, branch "block" the block's commit. The land tests use
// the batch fixture with land.require_ci on and a fake gh, to show the checks
// refuse before any CI read.

const landFastChangelog = "# Changelog\n\n## [Unreleased]\n\n### Added\n\n- older entry (B0)\n\n## [0.1.0] - 2026-01-01\n\n### Added\n\n- first (B00)\n"

func landFastRepo(t *testing.T, base map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	landFastGit(t, dir, "init", "-qb", "main")
	landFastFiles(t, dir, base)
	landFastGit(t, dir, "add", "-A")
	landFastGit(t, dir, "commit", "-qm", "base")
	landFastGit(t, dir, "checkout", "-qb", "block")
	return dir
}

func landFastGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func landFastFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		landWrite(t, filepath.Join(dir, name), body)
	}
}

// landFastBlock commits files on the block branch and runs the checks.
func landFastBlock(t *testing.T, dir string, files map[string]string) []string {
	t.Helper()
	landFastFiles(t, dir, files)
	landFastGit(t, dir, "add", "-A")
	landFastGit(t, dir, "commit", "-qm", "block")
	return landFastChecks(dir, "main", "HEAD")
}

func landFastHas(t *testing.T, problems []string, want string) {
	t.Helper()
	for _, p := range problems {
		if strings.Contains(p, want) {
			return
		}
	}
	t.Errorf("problems %q, want one containing %q", problems, want)
}

func TestB964ConflictMarkerRefused(t *testing.T) {
	dir := landFastRepo(t, map[string]string{"a.txt": "one\n"})
	problems := landFastBlock(t, dir, map[string]string{"a.txt": "one\n<<<<<<< HEAD\ntwo\n=======\nthree\n>>>>>>> other\n"})
	landFastHas(t, problems, "conflict marker added in a.txt: <<<<<<< HEAD")
	landFastHas(t, problems, "conflict marker added in a.txt: =======")
	landFastHas(t, problems, "conflict marker added in a.txt: >>>>>>> other")
}

func TestB964IgnoredTestdataRefused(t *testing.T) {
	dir := landFastRepo(t, map[string]string{".gitignore": "*.golden\n", "pkg/a.go": "package pkg\n"})
	problems := landFastBlock(t, dir, map[string]string{"pkg/a_test.go": "package pkg\n"})
	if problems != nil {
		t.Fatalf("problems before the ignored file: %q", problems)
	}
	landWrite(t, filepath.Join(dir, "pkg/testdata/want.golden"), "x\n")
	landFastHas(t, landFastChecks(dir, "main", "HEAD"), "test file ignored by git, never committed: pkg/testdata/ (git add -f it)")
}

func TestB964DuplicateDocsRowRefused(t *testing.T) {
	doc := "| Command | What |\n|---|---|\n| `run start` | starts |\n"
	dir := landFastRepo(t, map[string]string{"docs/ref.md": doc})
	problems := landFastBlock(t, dir, map[string]string{"docs/ref.md": doc + "| `run start`, `run stop` | both |\n"})
	landFastHas(t, problems, "duplicate docs row in docs/ref.md: `run start` <-> `run start`, `run stop`")
}

func TestB964ChangelogShapeRefused(t *testing.T) {
	dir := landFastRepo(t, map[string]string{"CHANGELOG.md": landFastChangelog})
	changed := strings.Replace(landFastChangelog, "- older entry (B0)\n", "- older entry (B0)\n\n### Added\n\n- new entry (B1)\n", 1)
	problems := landFastBlock(t, dir, map[string]string{"CHANGELOG.md": changed})
	landFastHas(t, problems, `"### Added" repeats in "## [Unreleased]"`)
}

func TestB964CleanBlockPasses(t *testing.T) {
	// A duplicate row the base already had is not the block's to fix; a
	// CHANGELOG whose entries never had ### headings is not refused either.
	doc := "| `a` | x |\n| `a` | y |\n"
	plain := "# Changelog\n\n## [Unreleased]\n\n- older entry (B0)\n"
	dir := landFastRepo(t, map[string]string{"docs/ref.md": doc, "CHANGELOG.md": plain, "a.txt": "one\n"})
	problems := landFastBlock(t, dir, map[string]string{
		"docs/ref.md":  doc + "| `b` | z |\n",
		"CHANGELOG.md": plain + "- new entry (B1)\n",
		"a.txt":        "one\n=== not a marker\n",
	})
	if problems != nil {
		t.Fatalf("problems %q, want none", problems)
	}
}

func TestB964BatchRefusedBeforeCI(t *testing.T) {
	f := landCIBatch(t, landCIOn, "", "")
	gh := landCIMainFakeGh(t, "[]", nil)
	tree := filepath.Join(f.repo, ".code-forge/worktrees", "B1")
	landWrite(t, filepath.Join(tree, "src/B1.mjs"), "<<<<<<< HEAD\ngreen B1\n=======\nother\n>>>>>>> x\n")
	f.git(tree, "commit", "-qam", "conflict left in")
	code, output := f.land("B1", "B2")
	if code == 0 || !strings.Contains(output, "block B1 fails the land fast checks:") || !strings.Contains(output, "conflict marker added in src/B1.mjs: <<<<<<< HEAD") {
		t.Fatalf("exit=%d, want a refusal naming the conflict marker: %s", code, output)
	}
	if calls := landCICalls(t, gh); len(calls) != 0 {
		t.Errorf("gh calls %q, want none: the fast checks run before CI", calls)
	}
	if head := f.git(f.repo, "rev-parse", "main"); head != f.base {
		t.Errorf("main moved to %s", head)
	}
	if lines := f.lines("merge-calls"); len(lines) != 0 {
		t.Errorf("merge gate ran before the fast checks: %q", lines)
	}
}

// The single-block path (a closed-block retry with --merge) refuses the same
// way, before its CI read.
func TestB964SingleRefusedBeforeCI(t *testing.T) {
	f := androidLand(t)
	const run = "r-fast-single"
	landWrite(t, filepath.Join(f.tree, "sample.mjs"), "new\n<<<<<<< HEAD\nmine\n=======\ntheirs\n>>>>>>> x\n")
	landGit(t, f.tree, "commit", "-qam", "conflict left in")
	entry := ledger.Object{{Key: "status", Value: "closed"}, {Key: "owned_files", Value: []any{"sample.mjs"}}, {Key: "base_branch", Value: "main"}, {Key: "worktree", Value: ledger.Object{{Key: "path", Value: f.tree}, {Key: "branch", Value: "block-B1"}, {Key: "base_sha", Value: f.base}}}}
	record := ledger.Object{{Key: "run_id", Value: run}, {Key: "project", Value: "fixture"}, {Key: "workspace", Value: f.repo}, {Key: "status", Value: "active"}, {Key: "blocks", Value: ledger.Object{{Key: "B1", Value: entry}}}, {Key: "orphans", Value: []any{}}}
	if err := state.SaveRun(f.home, record); err != nil {
		t.Fatal(err)
	}
	landWrite(t, filepath.Join(f.home, ".code-forge/runs", run+".key"), strings.Repeat("0", 32))
	landDone(t, f.home, run, landGit(t, f.tree, "rev-parse", "HEAD"))
	landCISnapshot(t, f.home, run, f.cfg, landCIOn)
	t.Setenv(landCIRedBlocksEnv, "")
	gh := landCIFakeGh(t, landCIGreen, nil)
	saved, err := state.ReadRun(f.home, run)
	if err != nil {
		t.Fatal(err)
	}
	main := landGit(t, f.repo, "rev-parse", "main")
	capture, err := os.CreateTemp(t.TempDir(), "output-")
	if err != nil {
		t.Fatal(err)
	}
	out, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = capture, capture
	flags := map[string]any{"report": "unused", "transcript": "unused", "merge": true, "no-merge-gate": true}
	code := landExecuteWithReason(nil, flags, f.home, run, "B1", saved, blockObject(blockObject(saved.Get("blocks")).Get("B1")), func(reason handoverReason) int {
		t.Errorf("handed over to Node: %v", reason)
		return 127
	})
	os.Stdout, os.Stderr = out, stderr
	capture.Close()
	data, _ := os.ReadFile(capture.Name())
	if code != 1 || !strings.Contains(string(data), "block B1 land stopped at fast-checks: the land fast checks fail:") || !strings.Contains(string(data), "conflict marker added in sample.mjs: <<<<<<< HEAD") {
		t.Fatalf("exit=%d output=%s, want a fast-checks refusal naming the marker", code, data)
	}
	if calls := landCICalls(t, gh); len(calls) != 0 {
		t.Errorf("gh calls %q, want none: the fast checks run before CI", calls)
	}
	if landGit(t, f.repo, "rev-parse", "main") != main {
		t.Errorf("main moved")
	}
}
