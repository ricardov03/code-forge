package landmerge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/gitx"
)

// B890: the `block land` row of docs/native-engine.md as it was at v0.25.1
// (base), after B796 and B797 (main) and with B795's sentence (the block).
// Each side only inserted sentences into the row; land said NO ANCHOR.

const b890Table = "# Engine\n\n| Command | Runs in Go | Hands over to Node |\n|---|---|---|\n| `block claim` | Claim. | Nothing. |\n%ROW%\n| `block open` | Open. | Nothing. |\n"

func b890Row(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "b890_row_"+name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(string(data), "\n")
}

func b890Doc(row string) string { return strings.Replace(b890Table, "%ROW%", row, 1) }

func TestB890RowBothSidesAppendIsUnion(t *testing.T) {
	base, main, block := b890Row(t, "base"), b890Row(t, "main"), b890Row(t, "block")
	merged, ok, err := Merge(KindTable, b890Doc(base), b890Doc(main), b890Doc(block))
	if err != nil || !ok {
		t.Fatalf("row merge refused: ok=%v err=%v", ok, err)
	}
	b795 := "is resolved in Go in the trial merge, the merge into main and a batch's precheck and rebases"
	b796 := "and the block's CI PRs are closed after the land (B796). "
	b797 := "Since B797 land reads the base branch's reflog before it merges"
	b789 := "so the setting no longer stops land (B789). "
	for _, s := range []string{b795, b796, b797, b789, "| `block claim` | Claim. | Nothing. |\n", "| `block open` | Open. | Nothing. |\n"} {
		if strings.Count(merged, s) != 1 {
			t.Fatalf("%q kept %d times:\n%s", s, strings.Count(merged, s), merged)
		}
	}
	// Main's sentence first, then the block's, where both inserted.
	if !(strings.Index(merged, b789) < strings.Index(merged, b797) && strings.Index(merged, b797) < strings.Index(merged, b795)) {
		t.Fatalf("order: want B789, then main's B797, then the block's B795:\n%s", merged)
	}
	// Every base sentence is still there, in its order.
	at := 0
	for _, token := range rowTokens(base) {
		i := strings.Index(merged[at:], token)
		if i < 0 {
			t.Fatalf("base text %q lost or moved", token)
		}
		at += i + len(token)
	}
	if strings.Count(merged, "\n") != strings.Count(b890Doc(base), "\n") {
		t.Fatalf("row count changed:\n%s", merged)
	}
}

// A row a side reworded (not only appended) stays a conflict.
func TestB890RowRewordIsStillConflict(t *testing.T) {
	base, main := b890Row(t, "base"), b890Row(t, "main")
	block := strings.Replace(base, "(B765). ", "(B765, reworded). ", 1)
	if _, ok, _ := Merge(KindTable, b890Doc(base), b890Doc(main), b890Doc(block)); ok {
		t.Fatal("a reworded row merged")
	}
}

// Through Resolve on a real conflicted merge of docs/native-engine.md.
func TestB890ResolveRowAppendConflict(t *testing.T) {
	base, main, block := b890Row(t, "base"), b890Row(t, "main"), b890Row(t, "block")
	repo := lmRepo(t, map[string][3]string{"docs/native-engine.md": {b890Doc(base), b890Doc(main), b890Doc(block)}})
	files, err := Resolve(repo, gitx.ChildEnv(), Defaults())
	if err != nil || Describe(files) != "docs/native-engine.md (table)" {
		t.Fatalf("files=%v err=%v", files, err)
	}
	staged := lmGit(t, repo, "show", ":docs/native-engine.md")
	if !strings.Contains(staged, "`code-forge land <ids>`. A merge conflict whose") || !strings.Contains(staged, "(B796). With `native.daemon") {
		t.Fatalf("staged row is not the union:\n%s", staged)
	}
}
