package landmerge

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/gitx"
)

const lmUnreleased = "# Changelog\n\n## [Unreleased]\n\n## [0.1.0]\n\n- first release\n"

func TestChangelogBothAddFixedKeepsOneHeading(t *testing.T) {
	ours := "# Changelog\n\n## [Unreleased]\n\n### Fixed\n\n- main entry (B1)\n\n## [0.1.0]\n\n- first release\n"
	theirs := "# Changelog\n\n## [Unreleased]\n\n### Fixed\n\n- block entry (B2)\n\n## [0.1.0]\n\n- first release\n"
	merged, ok, err := Changelog(lmUnreleased, ours, theirs)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	want := "# Changelog\n\n## [Unreleased]\n\n### Fixed\n\n- main entry (B1)\n- block entry (B2)\n\n## [0.1.0]\n\n- first release\n"
	if merged != want {
		t.Fatalf("merged:\n%q\nwant:\n%q", merged, want)
	}
}

func TestChangelogAddsUnderExistingHeadingsKeepsOneOfEachKind(t *testing.T) {
	base := "# Changelog\n\n## [Unreleased]\n\n### Added\n\n- old add (B0)\n\n### Fixed\n\n- old fix (B0)\n\n## [0.1.0]\n"
	ours := "# Changelog\n\n## [Unreleased]\n\n### Added\n\n- old add (B0)\n\n### Fixed\n\n- old fix (B0)\n- main fix (B1)\n\n## [0.1.0]\n"
	// The block adds a second Fixed heading and an Added entry (a block
	// written before main's change landed).
	theirs := "# Changelog\n\n## [Unreleased]\n\n### Fixed\n\n- block fix (B2)\n\n### Added\n\n- old add (B0)\n- block add (B2)\n\n### Fixed\n\n- old fix (B0)\n\n## [0.1.0]\n"
	merged, ok, err := Changelog(base, ours, theirs)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if n := strings.Count(merged, "### Fixed"); n != 1 {
		t.Fatalf("%d Fixed headings:\n%s", n, merged)
	}
	if n := strings.Count(merged, "### Added"); n != 1 {
		t.Fatalf("%d Added headings:\n%s", n, merged)
	}
	for _, entry := range []string{"old add (B0)", "block add (B2)", "old fix (B0)", "main fix (B1)", "block fix (B2)"} {
		if strings.Count(merged, entry) != 1 {
			t.Fatalf("entry %q not kept once:\n%s", entry, merged)
		}
	}
	if strings.Index(merged, "main fix (B1)") > strings.Index(merged, "block fix (B2)") {
		t.Fatalf("main's entry must come before the block's:\n%s", merged)
	}
}

func TestChangelogSameEntryBothSidesKeptOnce(t *testing.T) {
	side := "# Changelog\n\n## [Unreleased]\n\n### Fixed\n\n- shared fix (B3)\n\n## [0.1.0]\n\n- first release\n"
	ours := strings.Replace(side, "- shared fix (B3)\n", "- shared fix (B3)\n- main fix (B1)\n", 1)
	merged, ok, err := Changelog(lmUnreleased, ours, side)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if strings.Count(merged, "- shared fix (B3)") != 1 || strings.Count(merged, "### Fixed") != 1 {
		t.Fatalf("duplicate kept:\n%s", merged)
	}
}

func TestChangelogRemovedLineIsNoAppend(t *testing.T) {
	theirs := "# Changelog\n\n## [Unreleased]\n\n## [0.1.0]\n"
	if _, ok, err := Changelog(lmUnreleased, lmUnreleased+"- x\n", theirs); ok || err != nil {
		t.Fatalf("removal merged: ok=%v err=%v", ok, err)
	}
}

const lmTable = "# Engine\n\n| Command | Notes |\n|---|---|\n| a | one |\n| b | two |\n| c | three |\n\nText after.\n"

func TestTableRowsUnion(t *testing.T) {
	ours := strings.Replace(lmTable, "| a | one |\n", "| a | one |\n| a2 | main row |\n", 1)
	theirs := strings.Replace(lmTable, "| a | one |\n", "| a | one |\n| a3 | block row |\n", 1)
	merged, ok := Table(lmTable, ours, theirs)
	want := strings.Replace(lmTable, "| a | one |\n", "| a | one |\n| a2 | main row |\n| a3 | block row |\n", 1)
	if !ok || merged != want {
		t.Fatalf("ok=%v merged:\n%s\nwant:\n%s", ok, merged, want)
	}
	// Each side changed a different (adjacent) row.
	ours = strings.Replace(lmTable, "| a | one |", "| a | one (B1) |", 1)
	theirs = strings.Replace(lmTable, "| b | two |", "| b | two (B2) |", 1)
	merged, ok = Table(lmTable, ours, theirs)
	want = strings.Replace(ours, "| b | two |", "| b | two (B2) |", 1)
	if !ok || merged != want {
		t.Fatalf("ok=%v merged:\n%s\nwant:\n%s", ok, merged, want)
	}
	// Rows added at the end of the table by both sides.
	ours = strings.Replace(lmTable, "| c | three |\n", "| c | three |\n| d | main |\n", 1)
	theirs = strings.Replace(lmTable, "| c | three |\n", "| c | three |\n| e | block |\n| f | block 2 |\n", 1)
	merged, ok = Table(lmTable, ours, theirs)
	want = strings.Replace(lmTable, "| c | three |\n", "| c | three |\n| d | main |\n| e | block |\n| f | block 2 |\n", 1)
	if !ok || merged != want {
		t.Fatalf("ok=%v merged:\n%s\nwant:\n%s", ok, merged, want)
	}
}

func TestTableSameRowChangedTwiceIsConflict(t *testing.T) {
	ours := strings.Replace(lmTable, "| b | two |", "| b | two (B1) |", 1)
	theirs := strings.Replace(lmTable, "| b | two |", "| b | two (B2) |", 1)
	if _, ok := Table(lmTable, ours, theirs); ok {
		t.Fatal("one row changed differently by both sides merged")
	}
	theirs = strings.Replace(lmTable, "Text after.", "Other text.", 1)
	ours = strings.Replace(lmTable, "Text after.", "Main text.", 1)
	if _, ok := Table(lmTable, ours, theirs); ok {
		t.Fatal("prose changed by both sides merged")
	}
}

func TestFromConfig(t *testing.T) {
	if s := FromConfig(nil); !s.Enabled || len(s.Tables) != 1 || s.Tables[0] != "docs/native-engine.md" {
		t.Fatalf("default: %+v", s)
	}
	if s := FromConfig(map[string]any{"land": map[string]any{"auto_resolve": false}}); s.Enabled {
		t.Fatalf("false: %+v", s)
	}
	s := FromConfig(map[string]any{"land": map[string]any{"auto_resolve": map[string]any{
		"tables":    []any{"docs/a.md"},
		"generated": []any{map[string]any{"path": "docs/gen.md", "argv": []any{"sh", "gen.sh"}}},
	}}})
	if !s.Enabled || len(s.Tables) != 1 || s.Tables[0] != "docs/a.md" || len(s.Generated) != 1 || s.Generated[0].Argv[1] != "gen.sh" {
		t.Fatalf("object: %+v", s)
	}
	if s.Kind("docs/a.md", false) != KindTable || s.Kind("docs/gen.md", false) != KindGenerated || s.Kind("x/CHANGELOG.md", false) != KindChangelog || s.Kind("main.go", false) != KindCode || s.Kind("main.go", true) != KindUnion {
		t.Fatal("kinds")
	}
	if Legacy().Kind("docs/native-engine.md", false) != KindCode {
		t.Fatal("legacy table kind")
	}
}

func lmGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitx.Run(dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

func lmRepo(t *testing.T, files map[string][3]string) string {
	t.Helper()
	repo := t.TempDir()
	lmGit(t, repo, "init", "-qb", "main")
	lmGit(t, repo, "config", "user.name", "F")
	lmGit(t, repo, "config", "user.email", "f@example.invalid")
	lmGit(t, repo, "config", "commit.gpgsign", "false")
	write := func(i int) {
		for name, texts := range files {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, name)), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repo, name), []byte(texts[i]), 0600); err != nil {
				t.Fatal(err)
			}
		}
		lmGit(t, repo, "add", ".")
		lmGit(t, repo, "commit", "-qm", "c")
	}
	write(0)
	lmGit(t, repo, "checkout", "-qb", "block")
	write(2)
	lmGit(t, repo, "checkout", "-q", "main")
	write(1)
	if _, err := gitx.Run(repo, "merge", "--no-ff", "--no-edit", "block"); err == nil {
		t.Fatal("fixture merged without a conflict")
	}
	return repo
}

func TestResolveCodeConflictWritesNothing(t *testing.T) {
	ours := strings.Replace(lmTable, "| a | one |\n", "| a | one |\n| a2 | x |\n", 1)
	theirs := strings.Replace(lmTable, "| a | one |\n", "| a | one |\n| a3 | y |\n", 1)
	repo := lmRepo(t, map[string][3]string{
		"docs/native-engine.md": {lmTable, ours, theirs},
		"main.go":               {"package main\n", "package main // a\n", "package main // b\n"},
	})
	before, _ := os.ReadFile(filepath.Join(repo, "docs/native-engine.md"))
	_, err := Resolve(repo, gitx.ChildEnv(), Defaults())
	var conflict *Conflict
	if !errors.As(err, &conflict) || Describe(conflict.Files) != "docs/native-engine.md (table), main.go (code)" {
		t.Fatalf("err=%v", err)
	}
	after, _ := os.ReadFile(filepath.Join(repo, "docs/native-engine.md"))
	if string(before) != string(after) {
		t.Fatal("a refused resolve wrote a file")
	}
}

func TestResolveTableAndGenerated(t *testing.T) {
	ours := strings.Replace(lmTable, "| a | one |\n", "| a | one |\n| a2 | x |\n", 1)
	theirs := strings.Replace(lmTable, "| a | one |\n", "| a | one |\n| a3 | y |\n", 1)
	repo := lmRepo(t, map[string][3]string{
		"docs/native-engine.md": {lmTable, ours, theirs},
		"docs/gen.md":           {"gen 0\n", "gen main\n", "gen block\n"},
	})
	settings := Defaults()
	settings.Generated = []Generated{{Path: "docs/gen.md", Argv: []string{"/bin/sh", "-c", "echo regenerated > docs/gen.md"}}}
	files, err := Resolve(repo, gitx.ChildEnv(), settings)
	if err != nil || Describe(files) != "docs/gen.md (generated), docs/native-engine.md (table)" {
		t.Fatalf("files=%v err=%v", files, err)
	}
	if left := lmGit(t, repo, "diff", "--name-only", "--diff-filter=U"); left != "" {
		t.Fatalf("still unmerged: %s", left)
	}
	gen := lmGit(t, repo, "show", ":docs/gen.md")
	table := lmGit(t, repo, "show", ":docs/native-engine.md")
	if gen != "regenerated\n" || !strings.Contains(table, "| a2 | x |\n| a3 | y |\n") {
		t.Fatalf("gen=%q table=%q", gen, table)
	}
}
