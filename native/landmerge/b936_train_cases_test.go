package landmerge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/releasecheck"
)

// B936: the two real train-1 cases (2026-11-19 15:08). The CHANGELOG.md of
// B800 (bf3692cc..36c6a330) merged onto the train (f24d468e) left a second
// `### Added` in [Unreleased]; the `gates run` row of docs/native-engine.md
// of B877 (14724f8d) and B870 (ac9aaef1), each appending a sentence where the
// train (B798) appended one, said NO ANCHOR. Land keeps both sides of each,
// with one heading per kind.

func b936File(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// b936Bullets lists the `- ` lines of [Unreleased].
func b936Bullets(text string) []string {
	bullets, in := []string{}, false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "## ") {
			in = strings.HasPrefix(line, "## [Unreleased]")
			continue
		}
		if in && strings.HasPrefix(line, "- ") {
			bullets = append(bullets, line)
		}
	}
	return bullets
}

func b936CheckChangelog(t *testing.T, merged, main, block string) {
	t.Helper()
	if repeats := releasecheck.ChangelogRepeats(merged); len(repeats) != 0 {
		t.Fatalf("[Unreleased] repeats %v:\n%s", repeats, merged)
	}
	for _, bullet := range append(b936Bullets(main), b936Bullets(block)...) {
		if strings.Count(merged, bullet+"\n") != 1 {
			t.Fatalf("bullet kept %d times: %.80s\n%s", strings.Count(merged, bullet+"\n"), bullet, merged)
		}
	}
	if got, want := len(b936Bullets(merged)), len(b936Bullets(main))+len(b936Bullets(block)); got != want {
		t.Fatalf("[Unreleased] has %d bullets, want %d", got, want)
	}
	if !strings.Contains(merged, "## [0.25.1] — 2026-11-19\n\n### Added\n") {
		t.Fatalf("the released section changed:\n%s", merged)
	}
}

func TestB936ChangelogTrainKeepsBothWithOneHeading(t *testing.T) {
	base, main, block := b936File(t, "b936_changelog_base.md"), b936File(t, "b936_changelog_main.md"), b936File(t, "b936_changelog_block.md")
	// Land's own merge (union off): a conflict, merged by Changelog.
	merged, ok, err := Merge(KindChangelog, base, main, block)
	if err != nil || !ok {
		t.Fatalf("changelog merge refused: ok=%v err=%v", ok, err)
	}
	b936CheckChangelog(t, merged, main, block)
	// The repository's merge=union driver (the train's rebase) finishes
	// cleanly with two `### Added`; the fold makes it one.
	union, clean, err := threeWay(base, main, block, true)
	if err != nil || !clean {
		t.Fatalf("union merge: clean=%v err=%v", clean, err)
	}
	b936CheckChangelog(t, FoldChangelog(union, main), main, block)
}

func b936Sentences(row string) []string {
	out := []string{}
	for _, token := range rowTokens(strings.TrimSuffix(row, "\n")) {
		if s := strings.TrimSpace(token); len(s) > 1 {
			out = append(out, s)
		}
	}
	return out
}

func TestB936GatesRunRowKeepsEverySide(t *testing.T) {
	base, main := b936File(t, "b936_row_base.txt"), b936File(t, "b936_row_main.txt")
	b877, b870 := b936File(t, "b936_row_b877.txt"), b936File(t, "b936_row_b870.txt")
	doc := func(row string) string { return strings.Replace(b890Table, "%ROW%", strings.TrimSuffix(row, "\n"), 1) }
	// B877 onto the train, then B870 onto that result (both from the same base row).
	first, ok, err := Merge(KindTable, doc(base), doc(main), doc(b877))
	if err != nil || !ok {
		t.Fatalf("B877 row merge refused: ok=%v err=%v", ok, err)
	}
	second, ok, err := Merge(KindTable, doc(base), first, doc(b870))
	if err != nil || !ok {
		t.Fatalf("B870 row merge refused: ok=%v err=%v", ok, err)
	}
	for _, side := range []string{base, main, b877, b870} {
		for _, sentence := range b936Sentences(side) {
			if !strings.Contains(second, sentence) {
				t.Fatalf("sentence lost: %.100s\n%s", sentence, second)
			}
		}
	}
	for _, s := range []string{"Since B798 a red `go test` gate", "Since B878 `gates run`, `gates detect`", "(B870)."} {
		if strings.Count(second, s) != 1 {
			t.Fatalf("%q kept %d times", s, strings.Count(second, s))
		}
	}
	if strings.Count(second, "\n") != strings.Count(doc(base), "\n") {
		t.Fatalf("row count changed:\n%s", second)
	}
}
