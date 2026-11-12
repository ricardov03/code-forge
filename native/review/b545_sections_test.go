package review

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// TestB545MarkdownSectionsMatchNode: a Markdown file whose diff alone is over
// the packet budget is reviewed section by section like Node's reviewFile
// (B54): headings outside front matter and fences, hunks cut at headings,
// groups packed within the budget, one session per section with its
// review.plan row, S<i>.<id> findings, S<i>: summaries, the first unavailable
// section ending the file, a group alone over the budget split_required with
// its heading, a CRLF file, a new file and a quick step-up. Result, ledger
// rows, packet text and the work dir match Node byte for byte (goldens:
// Node's reviewFile on the same repository).
func TestB545MarkdownSectionsMatchNode(t *testing.T) {
	b545MatchNode(t, "b545_md_fixture.json", "b545_md_golden.json")
}

// TestB545DualConsensusMatchNode: risk 2+ runs two blind lenses (A, B) and an
// L3 judge, and review.multimodel runs two full reviewers on two providers
// (review.second_levels / second_provider, the second ladder without the
// first provider) and the judge, like Node's reviewFile: the judge packet
// (both reports, the hunk list, the diff with review.judge_sees_diff), a
// failed lens ending the file before the judge, a failed judge, the four
// consensus refusals before any session, single_reviewer_max_risk and the
// docs-kind rule. Result, rows, packet text and work dir match Node (goldens:
// Node's reviewFile on the same repository).
func TestB545DualConsensusMatchNode(t *testing.T) {
	b545MatchNode(t, "b545_dual_fixture.json", "b545_dual_golden.json")
}

func b545MatchNode(t *testing.T, fixtureName, goldenName string) {
	useRepoLenses(t)
	fxRaw := readOrdered(t, fixtureName)
	fx := fixture{Commit: map[string]string{}, After: map[string]string{}}
	for key, dst := range map[string]map[string]string{"commit": fx.Commit, "after": fx.After} {
		for _, f := range fxRaw.Get(key).(ledger.Object) {
			dst[f.Key] = f.Value.(string)
		}
	}
	repo := buildRepo(t, fx)
	golden := readOrdered(t, goldenName).Get("cases").(ledger.Object)
	cases := fxRaw.Get("cases").([]any)
	if len(cases) != len(golden) {
		t.Fatalf("cases %d, golden %d", len(cases), len(golden))
	}
	for _, raw := range cases {
		c := raw.(ledger.Object)
		name := c.Get("name").(string)
		t.Run(name, func(t *testing.T) {
			want := golden.Get(name).(ledger.Object)
			risk := 1.0
			if r, ok := c.Get("risk").(float64); ok {
				risk = r
			}
			work := filepath.Join(t.TempDir(), name)
			in := Input{RepoRoot: repo, File: c.Get("file").(string), Cfg: plainCfg(t, c.Get("cfg")), Risk: &risk,
				RulesDigest: "rule one\nrule two", FactsExcerpt: "facts here", WorkDir: work}
			if k, ok := c.Get("kind").(string); ok {
				in.Kind = k
			}
			// B537: a case with peers diffs against HEAD like the Node generator
			if peers, ok := c.Get("peers").([]any); ok {
				in.Base = "HEAD"
				for _, p := range peers {
					in.Peers = append(in.Peers, p.(string))
				}
			}
			if err := Check(in); err != nil {
				t.Fatalf("Check refused a case Node reviews: %v", err)
			}
			run := runEngine(t, in, c.Get("script").([]any), true)
			if run.err != nil {
				t.Fatalf("File: %v", run.err)
			}
			if got := mustJSON(t, run.result); got != want.Get("result").(string) {
				t.Errorf("result\ngot  %s\nNode %s", got, want.Get("result"))
			}
			wantRows := want.Get("rows").([]any)
			if len(run.rows) != len(wantRows) {
				t.Fatalf("rows: got %d %v, Node %d %v", len(run.rows), run.rows, len(wantRows), wantRows)
			}
			for k, r := range run.rows {
				if r != wantRows[k].(string) {
					t.Errorf("row %d\ngot  %s\nNode %s", k, r, wantRows[k])
				}
			}
			wantSpawns := want.Get("spawns").([]any)
			if len(run.spawns) != len(wantSpawns) {
				t.Fatalf("spawns: got %d, Node %d", len(run.spawns), len(wantSpawns))
			}
			for k, s := range run.spawns {
				w := wantSpawns[k].(ledger.Object)
				if s.Get("text") != w.Get("text") {
					t.Errorf("spawn %d packet\ngot  %s\nNode %s", k, s.Get("text"), w.Get("text"))
				}
				if got, ws := mustJSON(t, s.Get("rowExtra")), mustJSON(t, w.Get("rowExtra")); got != ws {
					t.Errorf("spawn %d rowExtra %s, Node %s", k, got, ws)
				}
			}
			var left any
			if entries, err := os.ReadDir(work); err == nil {
				names := []any{}
				for _, e := range entries {
					names = append(names, e.Name())
				}
				left = names
			}
			if got, ws := mustJSON(t, left), mustJSON(t, want.Get("left")); got != ws {
				t.Errorf("work dir left %s, Node %s", got, ws)
			}
		})
	}
}
