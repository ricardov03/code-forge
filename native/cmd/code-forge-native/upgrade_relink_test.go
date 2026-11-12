package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// upgradeFixture is a temp HOME and repo plus a fake package root (version
// 0.23.0, a skill directory and release notes); the clock is fixed and the
// docs sync answers without the network.
func upgradeFixture(t *testing.T) (home, repo, root string) {
	t.Helper()
	home, repo = smallProject(t, "")
	root = filepath.Join(filepath.Dir(home), "pkg")
	writeHomeFile(t, root, "package.json", `{"name":"x","version":"0.23.0"}`)
	writeHomeFile(t, root, "skill/SKILL.md", "skill\n")
	writeHomeFile(t, root, "CHANGELOG.md", "# Changelog\n\n## [Unreleased]\n- not this\n\n## [0.23.0] - 2026-11-11\n- first note\n- second note\n\n## [0.22.1]\n- old\n")
	oldRoot, oldNow, oldSync := upgradePackageRoot, upgradeNow, upgradeDocsSync
	upgradePackageRoot = func() (string, bool) { return root, true }
	upgradeNow = func() time.Time { return time.Date(2026, 11, 11, 21, 30, 0, 123e6, time.UTC) }
	upgradeDocsSync = func(_, _, version string) string { return "docs: kept packed " + version }
	t.Cleanup(func() { upgradePackageRoot, upgradeNow, upgradeDocsSync = oldRoot, oldNow, oldSync })
	return home, repo, root
}

func upgradeBanner(change string) string {
	return upgradeLogo + "code-forge 0.23.0\n" + change + "\n"
}

func upgradeNotes(root string) string {
	return "What's new in 0.23.0:\nfirst note\nsecond note\nFull notes: " + filepath.Join(root, "CHANGELOG.md") + "\n"
}

func readText(t *testing.T, file string) string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

const upgradeStamp = "2026-11-11T21:30:00.123Z"

// Expected bytes are the Node CLI's output on the same fixture
// (CODE_FORGE_NATIVE=0, temp HOME).
func TestB475UpgradeRelinksInGo(t *testing.T) {
	t.Run("records re-pointed, rows rewritten", func(t *testing.T) {
		home, _, root := upgradeFixture(t)
		writeHomeFile(t, root, "skill/sub/run.sh", "#!/bin/sh\n")
		if err := os.Chmod(filepath.Join(root, "skill", "sub", "run.sh"), 0o751); err != nil {
			t.Fatal(err)
		}
		old := filepath.Join(home, "old-skill")
		writeHomeFile(t, old, "SKILL.md", "old\n")
		claude := filepath.Join(home, ".claude", "skills", "code-forge")
		codex := filepath.Join(home, "proj", ".agents", "skills", "code-forge")
		grok := filepath.Join(home, ".grok", "skills", "code-forge")
		if err := os.MkdirAll(filepath.Dir(claude), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(old, claude); err != nil {
			t.Fatal(err)
		}
		writeHomeFile(t, codex, "stale.txt", "stale\n")
		writeHomeFile(t, home, ".code-forge/installs.json", fmt.Sprintf(`{"version":1,"installs":[
{"harness":"claude","scope":"global","method":"symlink","source":%q,"target":%q,"linked_at":"x","version":"0.22.1","note":1.50},
{"extra":true,"harness":"codex","scope":"project","method":"copy","source":%q,"target":%q,"linked_at":"y"},
{"harness":"bad","scope":"nowhere","method":"symlink","source":"/a","target":"/b","linked_at":"z"},
{"harness":"grok","scope":"global","method":"symlink","source":"/gone","target":%q,"linked_at":"z","version":"0.21.0-rc.2"}]}`, old, claude, old, codex, grok))
		code, out, errText := runCaptured(t, "upgrade")
		skill := filepath.Join(root, "skill")
		want := upgradeBanner("0.21.0-rc.2 -> 0.23.0") +
			"upgraded claude (global) -> " + skill + "\nupgraded codex (project) -> " + skill + "\nupgraded grok (global) -> " + skill + "\n" +
			upgradeNotes(root) + "docs: kept packed 0.23.0\n"
		if code != 0 || out != want || errText != "" {
			t.Fatalf("code=%d stdout=%q stderr=%q\nwant %q", code, out, errText, want)
		}
		wantRows := fmt.Sprintf(`{
  "version": 1,
  "installs": [
    {
      "harness": "claude",
      "scope": "global",
      "method": "symlink",
      "source": %q,
      "target": %q,
      "linked_at": %q,
      "version": "0.23.0",
      "note": 1.5
    },
    {
      "extra": true,
      "harness": "codex",
      "scope": "project",
      "method": "copy",
      "source": %q,
      "target": %q,
      "linked_at": %q,
      "version": "0.23.0"
    },
    {
      "harness": "grok",
      "scope": "global",
      "method": "symlink",
      "source": %q,
      "target": %q,
      "linked_at": %q,
      "version": "0.23.0"
    }
  ]
}
`, skill, claude, upgradeStamp, skill, codex, upgradeStamp, skill, grok, upgradeStamp)
		if got := readText(t, filepath.Join(home, ".code-forge", "installs.json")); got != wantRows {
			t.Fatalf("installs.json:\n%s\nwant:\n%s", got, wantRows)
		}
		for _, link := range []string{claude, grok} {
			if dest, err := os.Readlink(link); err != nil || dest != skill {
				t.Fatalf("%s -> %q (%v), want %s", link, dest, err, skill)
			}
		}
		if _, err := os.Stat(filepath.Join(codex, "stale.txt")); !os.IsNotExist(err) {
			t.Fatalf("copy kept a stale file: %v", err)
		}
		if got := readText(t, filepath.Join(codex, "SKILL.md")); got != "skill\n" {
			t.Fatalf("copied SKILL.md = %q", got)
		}
		if info, err := os.Stat(filepath.Join(codex, "sub", "run.sh")); err != nil || info.Mode().Perm() != 0o751 {
			t.Fatalf("copied run.sh mode: %v %v", info, err)
		}
		if got := readText(t, filepath.Join(old, "SKILL.md")); got != "old\n" {
			t.Fatalf("old source changed: %q", got)
		}
	})
	t.Run("plain, already on, explicit relative source", func(t *testing.T) {
		home, repo, root := upgradeFixture(t)
		src := filepath.Join(repo, "src")
		if err := os.MkdirAll(src, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(home, ".claude", "skills", "code-forge")
		writeHomeFile(t, home, ".code-forge/installs.json", fmt.Sprintf(`[{"harness":"claude","scope":"global","method":"symlink","source":"/x","target":%q,"linked_at":"x","version":"0.23.0"}]`, target))
		checkSmallCases(t, []smallCase{
			{[]string{"upgrade", "--plain", "--source", "src"}, 0, "upgraded claude (global) -> " + filepath.Join(repo, "src") + "\ndocs: kept packed 0.23.0\n", ""},
			{[]string{"upgrade", "--source", "src"}, 0, upgradeBanner("already on 0.23.0") + "upgraded claude (global) -> " + filepath.Join(repo, "src") + "\n" + upgradeNotes(root) + "docs: kept packed 0.23.0\n", ""},
		})
		if dest, _ := os.Readlink(target); dest != src {
			t.Fatalf("link -> %q, want %q", dest, src)
		}
		if !strings.Contains(readText(t, filepath.Join(home, ".code-forge", "installs.json")), `"source": "`+src+`"`) {
			t.Fatal("row source not the real path")
		}
	})
	t.Run("release notes: five bullets, cut, CRLF", func(t *testing.T) {
		_, _, root := upgradeFixture(t)
		long := strings.Repeat("é", 101)
		writeHomeFile(t, root, "CHANGELOG.md", "## [0.23.0]\r\n- bold\r\n- "+long+"\r\n- c\r\n-  d\r\n- e\r\n- f\r\n")
		code, out, _ := runCaptured(t, "upgrade")
		want := upgradeBanner("first versioned upgrade") + "nothing to upgrade\nWhat's new in 0.23.0:\nbold\n" + strings.Repeat("é", 97) + "...\nc\n d\ne\nFull notes: " + filepath.Join(root, "CHANGELOG.md") + "\ndocs: kept packed 0.23.0\n"
		if code != 0 || out != want {
			t.Fatalf("code=%d stdout=%q\nwant %q", code, out, want)
		}
	})
	t.Run("no notes for the version, empty ledger written", func(t *testing.T) {
		home, _, root := upgradeFixture(t)
		writeHomeFile(t, root, "CHANGELOG.md", "## [0.23.0-x]\n- no\n##\tnot a heading\n")
		code, out, _ := runCaptured(t, "upgrade")
		if code != 0 || out != upgradeBanner("first versioned upgrade")+"nothing to upgrade\ndocs: kept packed 0.23.0\n" {
			t.Fatalf("code=%d stdout=%q", code, out)
		}
		if got := readText(t, filepath.Join(home, ".code-forge", "installs.json")); got != "{\n  \"version\": 1,\n  \"installs\": []\n}\n" {
			t.Fatalf("installs.json = %q", got)
		}
	})
	t.Run("real docs sync offline", func(t *testing.T) {
		_, _, root := upgradeFixture(t)
		upgradeDocsSync = func(home, root, version string) string {
			return upgradeRealDocsSync(home, root, version)
		}
		t.Setenv("CODE_FORGE_DOCS_URL", "http://127.0.0.1:1/llms-full.txt")
		writeHomeFile(t, root, "docs/reference/llms-full.txt", "# code-forge\nversion: 0.22.9\n")
		code, out, _ := runCaptured(t, "upgrade", "--plain")
		if code != 0 || out != "nothing to upgrade\ndocs: kept packed 0.22.9\n" {
			t.Fatalf("code=%d stdout=%q", code, out)
		}
	})
}

// Cases Node would not finish the same way stay with Node, each under its
// own reason, before anything is touched.
func TestB475UpgradeHandsOverOddCases(t *testing.T) {
	record := func(target, method, version string) string {
		v := ""
		if version != "" {
			v = fmt.Sprintf(`,"version":%q`, version)
		}
		return fmt.Sprintf(`{"harness":"claude","scope":"global","method":%q,"source":"/x","target":%q,"linked_at":"x"%s}`, method, target, v)
	}
	ledgerOf := func(t *testing.T, home string, rows ...string) {
		writeHomeFile(t, home, ".code-forge/installs.json", "["+strings.Join(rows, ",")+"]")
	}
	t.Run("upgrade-unsafe-target: home", func(t *testing.T) {
		home, _, _ := upgradeFixture(t)
		ledgerOf(t, home, record(home, "symlink", ""))
		expectHandover(t, "upgrade-unsafe-target", "upgrade")
	})
	t.Run("upgrade-unsafe-target: inside source", func(t *testing.T) {
		home, _, root := upgradeFixture(t)
		ledgerOf(t, home, record(strings.ToUpper(filepath.Join(root, "skill", "in")), "symlink", ""))
		expectHandover(t, "upgrade-unsafe-target", "upgrade")
	})
	t.Run("upgrade-unsafe-target: nested targets", func(t *testing.T) {
		home, _, _ := upgradeFixture(t)
		ledgerOf(t, home, record(filepath.Join(home, "a"), "symlink", ""), record(filepath.Join(home, "a", "b"), "symlink", ""))
		expectHandover(t, "upgrade-unsafe-target", "upgrade")
	})
	t.Run("upgrade-copy-entry", func(t *testing.T) {
		home, _, root := upgradeFixture(t)
		if err := os.Symlink("SKILL.md", filepath.Join(root, "skill", "link")); err != nil {
			t.Fatal(err)
		}
		ledgerOf(t, home, record(filepath.Join(home, "t"), "copy", ""))
		expectHandover(t, "upgrade-copy-entry", "upgrade")
	})
	t.Run("upgrade-version-order", func(t *testing.T) {
		home, _, _ := upgradeFixture(t)
		ledgerOf(t, home, record(filepath.Join(home, "t"), "symlink", "1.02.0"))
		expectHandover(t, "upgrade-version-order", "upgrade")
	})
	t.Run("upgrade-ledger-encoding", func(t *testing.T) {
		home, _, _ := upgradeFixture(t)
		writeHomeFile(t, home, ".code-forge/installs.json", "[\xff]")
		expectHandover(t, "upgrade-ledger-encoding", "upgrade")
	})
	t.Run("upgrade-unfinishable", func(t *testing.T) {
		home, _, _ := upgradeFixture(t)
		if err := os.MkdirAll(filepath.Join(home, ".code-forge", "installs.json"), 0o700); err != nil {
			t.Fatal(err)
		}
		expectHandover(t, "upgrade-unfinishable", "upgrade")
	})
	t.Run("upgrade-no-home", func(t *testing.T) {
		upgradeFixture(t)
		t.Setenv("HOME", "rel")
		expectHandover(t, "upgrade-no-home", "upgrade")
	})
	t.Run("upgrade-path-encoding", func(t *testing.T) {
		upgradeFixture(t)
		expectHandover(t, "upgrade-path-encoding", "upgrade", "--source", "a\xffb")
	})
	t.Run("upgrade-control-chars: printed source", func(t *testing.T) {
		home, repo, _ := upgradeFixture(t)
		if err := os.MkdirAll(filepath.Join(repo, "s\x1b[31mrc"), 0o700); err != nil {
			t.Fatal(err)
		}
		ledgerOf(t, home, record(filepath.Join(home, "t"), "symlink", ""))
		expectHandover(t, "upgrade-control-chars", "upgrade", "--plain", "--source", "s\x1b[31mrc")
	})
	t.Run("upgrade-control-chars: missing source", func(t *testing.T) {
		upgradeFixture(t)
		expectHandover(t, "upgrade-control-chars", "upgrade", "--source", "a\u009bb")
	})
	t.Run("upgrade-control-chars: release note", func(t *testing.T) {
		_, _, root := upgradeFixture(t)
		writeHomeFile(t, root, "CHANGELOG.md", "## [0.23.0]\n- \x1b[1mbold\x1b[0m\n")
		expectHandover(t, "upgrade-control-chars", "upgrade")
	})
	t.Run("upgrade-control-chars: docs line", func(t *testing.T) {
		upgradeFixture(t)
		upgradeDocsSync = func(_, _, _ string) string { return "docs: none (remote version \x1b[2J, installed 0.23.0)" }
		expectHandover(t, "upgrade-control-chars", "upgrade", "--plain")
	})
	t.Run("upgrade-package-root", func(t *testing.T) {
		upgradeFixture(t)
		upgradePackageRoot = upgradeRealPackageRoot
		expectHandover(t, "upgrade-package-root", "upgrade")
	})
	t.Run("ledger untouched by a hand-over", func(t *testing.T) {
		home, _, _ := upgradeFixture(t)
		ledgerOf(t, home, record(home, "symlink", ""))
		before := readText(t, filepath.Join(home, ".code-forge", "installs.json"))
		expectHandover(t, "upgrade-unsafe-target", "upgrade")
		if after := readText(t, filepath.Join(home, ".code-forge", "installs.json")); after != before {
			t.Fatalf("ledger changed: %q", after)
		}
	})
}

func TestB475UpgradeOrdersVersionsLikeNode(t *testing.T) {
	// Signs of Node's compareVersions on the same pairs.
	pairs := [][2]string{{"1.2", "1.2.0"}, {"1.10.0", "1.9.0"}, {"0.22.1", "0.22.1-rc.1"}, {"1.0.0-rc.2", "1.0.0-rc.10"}, {"1.0.0-a", "1.0.0-1"}, {"1.0.0-a.b", "1.0.0-a"}, {"10.0.0", "9.99.99"}, {"1.0.0+b", "1.0.0+a"}, {"1.0.0-", "1.0.0"}, {"1.0.0-x-y", "1.0.0-x"}, {"2", "10"}, {"1.0.0-99999999999999999999", "1.0.0-99999999999999999998"}}
	signs := []int{-1, 1, 1, -1, 1, 1, 1, 0, -1, 1, -1, 0}
	for i, p := range pairs {
		got := compareVersions(p[0], p[1])
		if got > 0 {
			got = 1
		} else if got < 0 {
			got = -1
		}
		if got != signs[i] {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", p[0], p[1], got, signs[i])
		}
	}
}
