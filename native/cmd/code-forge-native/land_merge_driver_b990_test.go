package main

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// B990: `code-forge land merge-driver` as git's merge.union.driver folds the
// repeated CHANGELOG ### headings a plain `git merge --no-ff` (the wave
// combine) used to keep. The tests run real git merges; git calls this test
// binary as the driver (TestB990DriverChild), which runs the land route.

const b990ChildEnv = "CODE_FORGE_B990_DRIVER_CHILD"

// TestB990DriverChild is the driver git starts; it does nothing in a normal run.
func TestB990DriverChild(t *testing.T) {
	if os.Getenv(b990ChildEnv) != "1" {
		t.Skip("only as the git merge driver of the B990 tests")
	}
	code, _ := run(append([]string{"land", "merge-driver"}, flag.Args()...))
	os.Exit(code)
}

func b990Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func b990Write(t *testing.T, dir, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

const b990Base = "# Changelog\n\n## [Unreleased]\n\n### Fixed\n\n- f0 (B1)\n\n## [0.1.0]\n\n### Added\n\n- old (B0)\n"

// b990Repo makes a repo whose main and lane branches each add a ### Changed
// group to [Unreleased] (main above ### Fixed, the lane below it: a clean
// merge with two headings, as in the w1a combine) and a line to a second
// merge=union file.
func b990Repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	b990Git(t, dir, "init", "-q", "-b", "main")
	b990Git(t, dir, "config", "user.email", "t@example.com")
	b990Git(t, dir, "config", "user.name", "t")
	b990Git(t, dir, "config", "commit.gpgsign", "false")
	b990Write(t, dir, ".gitattributes", "CHANGELOG.md merge=union\nlist.txt merge=union\n")
	b990Write(t, dir, "CHANGELOG.md", b990Base)
	b990Write(t, dir, "list.txt", "a\nb\n")
	b990Git(t, dir, "add", "-A")
	b990Git(t, dir, "commit", "-q", "-m", "base")
	b990Git(t, dir, "checkout", "-q", "-b", "lane")
	b990Write(t, dir, "CHANGELOG.md", strings.Replace(b990Base, "- f0 (B1)\n", "- f0 (B1)\n\n### Changed\n\n- lane change (B3)\n", 1))
	b990Write(t, dir, "list.txt", "a\nb\nlane\n")
	b990Git(t, dir, "commit", "-q", "-am", "lane")
	b990Git(t, dir, "checkout", "-q", "main")
	b990Write(t, dir, "CHANGELOG.md", strings.Replace(b990Base, "### Fixed", "### Changed\n\n- main change (B2)\n\n### Fixed", 1))
	b990Write(t, dir, "list.txt", "a\nb\nmain\n")
	b990Git(t, dir, "commit", "-q", "-am", "main")
	return dir
}

// b990UseDriver points merge.union.driver at this test binary.
func b990UseDriver(t *testing.T, dir string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b613Env(t)
	t.Setenv(b990ChildEnv, "1")
	b990Git(t, dir, "config", "merge.union.driver", "'"+exe+"' -test.run='^TestB990DriverChild$' -- %O %A %B %P")
}

func TestB990MergeDriverFoldsChangelogOnGitMerge(t *testing.T) {
	// without the driver: git's union keeps two ### Changed headings
	plain := b990Repo(t)
	b990Git(t, plain, "merge", "--no-ff", "--no-edit", "lane")
	data, _ := os.ReadFile(filepath.Join(plain, "CHANGELOG.md"))
	if n := strings.Count(string(data), "### Changed\n"); n != 2 {
		t.Fatalf("plain union merge has %d ### Changed headings, want 2 (the B990 problem):\n%s", n, data)
	}

	dir := b990Repo(t)
	b990UseDriver(t, dir)
	b990Git(t, dir, "merge", "--no-ff", "--no-edit", "lane")
	if parents := strings.Fields(b990Git(t, dir, "log", "-1", "--format=%P")); len(parents) != 2 {
		t.Fatalf("no merge commit: parents %v", parents)
	}
	if status := b990Git(t, dir, "status", "--porcelain"); status != "" {
		t.Fatalf("tree not clean after the merge: %q", status)
	}
	data, _ = os.ReadFile(filepath.Join(dir, "CHANGELOG.md"))
	text := string(data)
	unreleased := text[:strings.Index(text, "## [0.1.0]")]
	if n := strings.Count(unreleased, "### Changed\n"); n != 1 {
		t.Fatalf("%d ### Changed headings in [Unreleased], want 1:\n%s", n, text)
	}
	for _, line := range []string{"- main change (B2)\n", "- lane change (B3)\n", "- f0 (B1)\n", "- old (B0)\n"} {
		if strings.Count(text, line) != 1 {
			t.Errorf("line %q not kept once:\n%s", line, text)
		}
	}
	if !(strings.Index(text, "main change") < strings.Index(text, "lane change") && strings.Index(text, "lane change") < strings.Index(text, "### Fixed")) {
		t.Errorf("lines out of order (main first, both under ### Changed):\n%s", text)
	}
}

// A merge=union file that is not a CHANGELOG gets git's own union result.
func TestB990MergeDriverUnionsOtherFiles(t *testing.T) {
	plain := b990Repo(t)
	b990Git(t, plain, "merge", "--no-ff", "--no-edit", "lane")
	want, _ := os.ReadFile(filepath.Join(plain, "list.txt"))

	dir := b990Repo(t)
	b990UseDriver(t, dir)
	b990Git(t, dir, "merge", "--no-ff", "--no-edit", "lane")
	got, _ := os.ReadFile(filepath.Join(dir, "list.txt"))
	if string(got) != string(want) || string(got) != "a\nb\nmain\nlane\n" {
		t.Fatalf("list.txt %q, git union %q", got, want)
	}
}

func TestB990MergeDriverUsage(t *testing.T) {
	b613Env(t)
	for _, args := range [][]string{{"merge-driver"}, {"merge-driver", "a", "b"}, {"merge-driver", "a", "b", "c", "d", "e"}, {"merge-driver", "--x", "a", "b"}} {
		code, stdout, stderr := runCapturedRoute(t, routes["land"], args...)
		if code != 2 || stdout != "" || stderr != landMergeDriverUsage {
			t.Errorf("%v: code=%d stdout=%q stderr=%q", args, code, stdout, stderr)
		}
	}
	code, stdout, _ := runCapturedRoute(t, routes["land"], "merge-driver", "--help")
	if code != 0 || stdout != landMergeDriverUsage {
		t.Errorf("merge-driver --help: code=%d stdout=%q", code, stdout)
	}
}
