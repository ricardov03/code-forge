package review

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

func TestB1019OversizedMarkdownPartsPreserveDiff(t *testing.T) {
	for _, mode := range []string{"paragraphs", "lines", "deleted", "replacement", "multiple-hunks", "no-newline", "context"} {
		t.Run(mode, func(t *testing.T) {
			var content, body strings.Builder
			content.WriteString("# Guide\n")
			body.WriteString("+# Guide\n")
			for i := 0; i < 80; i++ {
				line := fmt.Sprintf("paragraph %02d has complete reviewable words", i)
				content.WriteString(line + "\n")
				body.WriteString("+" + line + "\n")
				if mode == "paragraphs" {
					content.WriteString("\n")
					body.WriteString("+\n")
				}
			}
			text := content.String()
			n := strings.Count(text, "\n")
			diff := fmt.Sprintf("--- /dev/null\n+++ b/doc.md\n@@ -0,0 +1,%d @@\n%s", n, body.String())
			var ptr *string = &text
			if mode == "deleted" {
				ptr = nil
				diff = fmt.Sprintf("--- a/doc.md\n+++ /dev/null\n@@ -1,%d +0,0 @@\n-%s", n, strings.ReplaceAll(strings.TrimSuffix(text, "\n"), "\n", "\n-")) + "\n"
			}
			if mode == "replacement" {
				diff = fmt.Sprintf("--- a/doc.md\n+++ b/doc.md\n@@ -1,2 +1,%d @@\n-old heading\n-old body\n%s", n, body.String())
			}
			if mode == "multiple-hunks" {
				text = "# Guide\n" + strings.Repeat("first changed line\n", 40) + strings.Repeat("unchanged gap\n", 9) + strings.Repeat("second changed line\n", 40)
				diff = "--- a/doc.md\n+++ b/doc.md\n@@ -1,0 +2,40 @@\n" + strings.Repeat("+first changed line\n", 40) + "@@ -10,0 +51,40 @@\n" + strings.Repeat("+second changed line\n", 40)
			}
			if mode == "context" {
				text = "shared heading\n" + strings.Repeat("unchanged context\nchanged paragraph\n", 40)
				diff = "--- a/doc.md\n+++ b/doc.md\n@@ -1,41 +1,81 @@\n shared heading\n" + strings.Repeat(" unchanged context\n+changed paragraph\n", 40)
			}
			if mode == "no-newline" {
				text = strings.TrimSuffix(text, "\n")
				diff += "\\ No newline at end of file\n"
			}
			d := reparsed(&FileDiff{File: "doc.md", Content: ptr}, diff)
			if ptr != nil {
				current, at := SplitLines(ptr), 0
				for _, line := range strings.Split(diff, "\n") {
					if strings.HasPrefix(line, "@@") {
						hs, _, _ := ParseDiff(line)
						at = hs[0].NewStart - 1
						continue
					}
					if onNewSide(line) && !strings.HasPrefix(line, "+++") {
						if at < 0 || at >= len(current) || line[1:] != current[at] {
							t.Fatalf("fixture new side at %d disagrees with content: %q", at+1, line)
						}
						at++
					}
				}
			}
			measure := func(part *FileDiff, _ []int) int { return len(part.DiffText) }
			packed := packSections(d, 500, measure)
			if !packed.ok || len(packed.sections) < 2 {
				t.Fatalf("oversized section did not split: %+v", packed)
			}
			// Compare every body line with both cursors, including context and
			// no-newline markers, to catch correct text at incorrect positions.
			coordinates := func(d *FileDiff) string {
				var out strings.Builder
				h, old, new := -1, 0, 0
				for _, line := range strings.Split(strings.TrimSuffix(d.DiffText, "\n"), "\n") {
					if strings.HasPrefix(line, "@@") {
						h++
						old, new = d.Hunks[h].OldStart, d.Hunks[h].NewStart
						if d.Hunks[h].OldLines == 0 {
							old++
						}
						if d.Hunks[h].NewLines == 0 {
							new++
						}
						continue
					}
					if h < 0 {
						continue
					}
					fmt.Fprintf(&out, "%d:%d:%s\n", old, new, line)
					if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "-") {
						old++
					}
					if onNewSide(line) {
						new++
					}
				}
				return out.String()
			}
			var positioned strings.Builder
			var got strings.Builder
			var plus, minus int
			for _, s := range packed.sections {
				positioned.WriteString(coordinates(s.diff))
				if measure(s.diff, s.keys) > 500 {
					t.Fatal("part exceeds budget")
				}
				plus += s.diff.PlusCount
				minus += s.diff.MinusCount
				for _, line := range strings.Split(s.diff.DiffText, "\n") {
					if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
						got.WriteString(line + "\n")
					}
				}
				for _, h := range s.diff.Hunks {
					if h.OldStart < 0 || h.NewStart < 0 {
						t.Fatalf("invalid coordinates: %+v", h)
					}
				}
			}
			var want strings.Builder
			for _, line := range strings.Split(diff, "\n") {
				if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
					want.WriteString(line + "\n")
				}
			}
			if positioned.String() != coordinates(d) {
				t.Fatal("split changed body coordinates, context or markers")
			}
			if got.String() != want.String() || plus != d.PlusCount || minus != d.MinusCount {
				t.Fatal("split omitted, duplicated or reordered changed lines")
			}
		})
	}
}

func TestB1019SingleSectionCanBeApproved(t *testing.T) {
	useRepoLenses(t)
	text := "# Guide\n\n" + strings.Repeat("A paragraph with enough words to require several bounded packets.\n\n", 200)
	repo := buildRepo(t, fixture{Commit: map[string]string{"base.txt": "base\n"}, After: map[string]string{"doc.md": text}})
	cfg := map[string]any{"provider": "claude", "levels": map[string]any{"L2": map[string]any{"provider": "claude", "model": "m2"}}, "review": map[string]any{"budgets": map[string]any{"full_in": float64(1800)}}}
	risk := 1.0
	in := Input{RepoRoot: repo, File: "doc.md", Base: "HEAD", Cfg: cfg, Risk: &risk, WorkDir: filepath.Join(t.TempDir(), "review")}
	sessions := 0
	got, err := File(in, Deps{Spawn: func(o SpawnOpts) (ledger.Object, error) {
		sessions++
		data, err := os.ReadFile(o.PromptPath)
		if err != nil {
			t.Fatal(err)
		}
		return ledger.Object{{Key: "status", Value: "ok"}, {Key: "provider", Value: "claude"}, {Key: "model", Value: "m2"}, {Key: "exit_code", Value: float64(0)}, {Key: "usage", Value: ledger.Object{{Key: "tokens_out", Value: float64(120)}}}, {Key: "answer", Value: ledger.Object{{Key: "passed", Value: true}, {Key: "summary", Value: "All supplied paragraphs reviewed."}, {Key: "reviewed_hunks", Value: hunksOf(string(data))}, {Key: "findings", Value: []any{}}}}}, nil
	}})
	if err != nil || got.Get("approved") != true || sessions < 2 {
		t.Fatalf("review: %v %v sessions=%d", got, err, sessions)
	}
}

func TestB1019RecheckFindingsAssignedOnce(t *testing.T) {
	text := "# Guide\n" + strings.Repeat("a changed paragraph with words\n\n", 80)
	diff := fmt.Sprintf("@@ -0,0 +1,%d @@\n+%s\n", strings.Count(text, "\n"), strings.ReplaceAll(strings.TrimSuffix(text, "\n"), "\n", "\n+"))
	d := reparsed(&FileDiff{File: "doc.md", Content: &text}, diff)
	pending := []any{ledger.Object{{Key: "id", Value: "F1"}, {Key: "line_start", Value: float64(5)}}, ledger.Object{{Key: "id", Value: "F2"}, {Key: "line_start", Value: float64(150)}}}
	parts, stop := packRecheck("recheck lens", d, pending, 300, nil)
	if stop != nil || len(parts) < 2 {
		t.Fatalf("recheck did not split: %v %+v", parts, stop)
	}
	seen := map[int]int{}
	for _, p := range parts {
		for _, i := range p.open {
			seen[i]++
		}
	}
	if seen[0] != 1 || seen[1] != 1 {
		t.Fatalf("findings duplicated or lost: %v", seen)
	}
}
