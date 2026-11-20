package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/landmerge"
)

// B990b: code-forge sets merge.union.driver itself (run start, block land),
// with a line that falls back to git's union merge when the binary is gone or
// older than `land merge-driver`.

func b990bDriver(t *testing.T, dir string) string {
	t.Helper()
	out, _ := exec.Command("git", "-C", dir, "config", "--local", "--get", "merge.union.driver").Output()
	return strings.TrimRight(string(out), "\n")
}

// b990bScript writes an executable shell script named name in a new dir.
func b990bScript(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func b990bMergeLane(t *testing.T, dir string) string {
	t.Helper()
	b990Git(t, dir, "merge", "--no-ff", "--no-edit", "lane")
	if status := b990Git(t, dir, "status", "--porcelain"); status != "" {
		t.Fatalf("tree not clean after the merge: %q", status)
	}
	data, err := os.ReadFile(filepath.Join(dir, "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestB990bEnsuredDriverFoldsOnGitMerge(t *testing.T) {
	test, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b613Env(t)
	t.Setenv(b990ChildEnv, "1")
	// stands in for an installed code-forge: `land merge-driver O A B P`
	exe := b990bScript(t, "code-forge", `[ "$1 $2" = "land merge-driver" ] || exit 9
exec '`+test+`' -test.run='^TestB990DriverChild$' -- "$3" "$4" "$5" "$6"
`)
	dir := b990Repo(t)
	changed, err := landmerge.EnsureUnionDriver(dir, exe)
	if err != nil || !changed || b990bDriver(t, dir) != landmerge.UnionDriverLine(exe) {
		t.Fatalf("ensure = %v %v, driver %q", changed, err, b990bDriver(t, dir))
	}
	text := b990bMergeLane(t, dir)
	unreleased := text[:strings.Index(text, "## [0.1.0]")]
	if n := strings.Count(unreleased, "### Changed\n"); n != 1 {
		t.Fatalf("%d ### Changed headings in [Unreleased], want 1:\n%s", n, text)
	}
	if again, err := landmerge.EnsureUnionDriver(dir, exe); again || err != nil {
		t.Fatalf("second ensure = %v %v, want no write", again, err)
	}
}

func TestB990bDriverFallsBackToUnion(t *testing.T) {
	for name, exe := range map[string]string{
		"gone": filepath.Join(t.TempDir(), "code-forge"),
		"old":  b990bScript(t, "code-forge", "echo 'unknown command: land' >&2\nexit 2\n"),
	} {
		dir := b990Repo(t)
		if changed, err := landmerge.EnsureUnionDriver(dir, exe); !changed || err != nil {
			t.Fatalf("%s: ensure = %v %v", name, changed, err)
		}
		text := b990bMergeLane(t, dir)
		for _, line := range []string{"- main change (B2)\n", "- lane change (B3)\n", "- f0 (B1)\n"} {
			if strings.Count(text, line) != 1 {
				t.Errorf("%s: line %q not kept once (git union fallback):\n%s", name, line, text)
			}
		}
		list, _ := os.ReadFile(filepath.Join(dir, "list.txt"))
		if string(list) != "a\nb\nmain\nlane\n" {
			t.Errorf("%s: list.txt %q, want git's union", name, list)
		}
	}
}

func TestB990bEnsureRules(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "code-forge")
	manual := "code-forge land merge-driver %O %A %B %P"
	cases := []struct {
		name, exe, before, want string
		attr, changed           bool
	}{
		{"unset", exe, "", landmerge.UnionDriverLine(exe), true, true},
		{"manual line replaced", exe, manual, landmerge.UnionDriverLine(exe), true, true},
		{"other binary replaced", exe, landmerge.UnionDriverLine("/old/code-forge"), landmerge.UnionDriverLine(exe), true, true},
		{"owner driver kept", exe, "my-merge %A %O %B", "my-merge %A %O %B", true, false},
		{"no union attribute", exe, "", "", false, false},
		{"go test binary", filepath.Join(t.TempDir(), "code-forge-native.test"), "", "", true, false},
		{"not code-forge", filepath.Join(t.TempDir(), "git"), "", "", true, false},
		{"relative path", "code-forge", "", "", true, false},
	}
	for _, c := range cases {
		dir := t.TempDir()
		b990Git(t, dir, "init", "-q")
		if c.attr {
			b990Write(t, dir, ".gitattributes", "CHANGELOG.md merge=union\n")
		}
		if c.before != "" {
			b990Git(t, dir, "config", "merge.union.driver", c.before)
		}
		changed, err := landmerge.EnsureUnionDriver(dir, c.exe)
		if err != nil || changed != c.changed || b990bDriver(t, dir) != c.want {
			t.Errorf("%s: ensure = %v %v, driver %q, want %v %q", c.name, changed, err, b990bDriver(t, dir), c.changed, c.want)
		}
	}
	// a linked worktree writes the repository's shared config
	dir := b990Repo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	b990Git(t, dir, "worktree", "add", "-q", wt, "lane")
	if changed, err := landmerge.EnsureUnionDriver(wt, exe); !changed || err != nil {
		t.Fatalf("worktree ensure = %v %v", changed, err)
	}
	if b990bDriver(t, dir) != landmerge.UnionDriverLine(exe) {
		t.Fatalf("main checkout driver %q after a worktree ensure", b990bDriver(t, dir))
	}
}

func TestB990bRunStartSetsUnionDriver(t *testing.T) {
	f := newStartFixture(t, "project:\n  slug: unit\nengine: claude\ntmp:\n  root: TMPROOT\n")
	b990Write(t, f.repo, ".gitattributes", "CHANGELOG.md merge=union\n")
	exe := filepath.Join(t.TempDir(), "code-forge")
	saved := unionDriverBinary
	unionDriverBinary = func() (string, error) { return exe, nil }
	t.Cleanup(func() { unionDriverBinary = saved })
	t.Setenv("CODE_FORGE_RUN_ID", "")
	code, out, errs := runVerbCall(t, "start", "--cwd", f.repo, "--run", "r-b990b", "--mode", "brief")
	if code != 0 {
		t.Fatalf("start = %d %q %q", code, out, errs)
	}
	if got := b990bDriver(t, f.repo); got != landmerge.UnionDriverLine(exe) {
		t.Fatalf("driver after run start %q, want %q", got, landmerge.UnionDriverLine(exe))
	}
}

func TestB990bBlockLandSetsUnionDriver(t *testing.T) {
	f := newLandBatchFixture(t, 0)
	// the attribute in .git/info/attributes keeps the tree clean for the land
	b990Write(t, filepath.Join(f.repo, ".git", "info"), "attributes", "CHANGELOG.md merge=union\n")
	exe := filepath.Join(t.TempDir(), "code-forge")
	saved := unionDriverBinary
	unionDriverBinary = func() (string, error) { return exe, nil }
	t.Cleanup(func() { unionDriverBinary = saved })
	if code, output := f.land("B1", "B2"); code != 0 {
		t.Fatalf("land exit=%d: %s", code, output)
	}
	if got := b990bDriver(t, f.repo); got != landmerge.UnionDriverLine(exe) {
		t.Fatalf("driver after block land %q, want %q", got, landmerge.UnionDriverLine(exe))
	}
}
