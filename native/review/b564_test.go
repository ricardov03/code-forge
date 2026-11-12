package review

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// testdata/b564_golden.json is Node's runRound kind recheck
// (src/review/fixloop.mjs, its own buildRecheckPacket, crossFileEvidence and
// moved-code reader) on the b564_fixture.json repository, one Node process
// per case with a temp HOME: a base with one peer, with several peers (named
// and imported ones kept, the rest dropped), a moved-code hint alone, a moved
// hint plus cross-file evidence (one and several peers), over the budget (the
// cut line, and nothing when even the cut line does not fit), a base with no
// peers, no base, and an unchanged file with and without peer evidence.
// Masks: the base commit id reads $BASE; random hex in the work dir reads X.

// TestB564RecheckEvidenceAndMovesMatchNode: each case's recheck round runs in
// Go (no ErrUnsupported) with Node's state, ledger rows, session options and
// packet bytes.
func TestB564RecheckEvidenceAndMovesMatchNode(t *testing.T) {
	useRepoLenses(t)
	fixtureFile := readOrdered(t, "b564_fixture.json")
	fixture := fixtureFile.Get("fixture").(ledger.Object)
	repo, sha := b537Repo(t, fixture)
	golden := readOrdered(t, "b564_golden.json").Get("cases").(ledger.Object)
	cases := fixtureFile.Get("cases").([]any)
	if len(cases) != len(golden) || len(cases) != 11 {
		t.Fatalf("cases %d, golden %d", len(cases), len(golden))
	}
	unbase := func(s string) string { return strings.ReplaceAll(s, sha, "$BASE") }
	for _, raw := range cases {
		c := raw.(ledger.Object)
		name := c.Get("name").(string)
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			want := golden.Get(name).(ledger.Object)
			cfg := baseCfg()
			if v := c.Get("cfg"); v != nil {
				cfg = plainCfg(t, v)
			}
			file := c.Get("file").(string)
			work := filepath.Join(t.TempDir(), name)
			in := Input{RepoRoot: repo, File: file, Cfg: cfg, RulesDigest: "rule one\nrule two", FactsExcerpt: "facts here", WorkDir: work,
				Peers: b537Strings(c.Get("peers"))}
			if c.Get("base") == true {
				in.Base = sha
			}
			open := c.Get("open").([]any)
			resolved := []any{}
			for _, f := range open {
				resolved = append(resolved, ledger.Object{{Key: "id", Value: f.(ledger.Object).Get("id")}, {Key: "resolved", Value: true}, {Key: "why", Value: "fixed"}})
			}
			answer := okSession(ledger.Field{Key: "resolved", Value: resolved})
			state := NewFileState(file, "L2", false)
			state.Set("round", 1.0)
			state.Set("rounds_at_level", 1.0)
			state.Set("open", open)
			if c.Get("reviewedIsCurrent") == true {
				data, err := os.ReadFile(filepath.Join(repo, file))
				if err != nil {
					t.Fatal(err)
				}
				state.Set("reviewed_content", string(data))
			} else {
				state.Set("reviewed_content", fixture.Get("commit").(ledger.Object).Get(file))
			}
			var rows []string
			var spawns []ledger.Object
			deps := LoopDeps{RepoRoot: repo, Cfg: cfg, Input: in,
				WriteRow: func(r ledger.Object) error { rows = append(rows, mustJSON(t, r)); return nil },
				Review:   func() (ledger.Object, error) { t.Fatal("full review ran"); return nil, nil },
				Session: Deps{Rand: bytes.NewReader(bytes.Repeat([]byte{0xab}, 600)), Spawn: func(o SpawnOpts) (ledger.Object, error) {
					text, err := os.ReadFile(o.PromptPath)
					if err != nil {
						t.Fatal(err)
					}
					spawns = append(spawns, ledger.Object{{Key: "level", Value: o.Level}, {Key: "role", Value: o.Role},
						{Key: "text", Value: string(text)}, {Key: "rowExtra", Value: o.RowExtra}})
					return withHunks(answer, hunksOf(string(text))).(ledger.Object), nil
				}}}
			got, err := RunRound(state, deps, "recheck")
			if err != nil {
				t.Fatalf("RunRound refused a recheck Node runs: %v", err)
			}
			if g := unbase(js(t, got)); g != want.Get("result").(string) {
				t.Errorf("result\ngot  %s\nNode %s", g, want.Get("result"))
			}
			wantRows := want.Get("rows").([]any)
			if len(rows) != len(wantRows) {
				t.Fatalf("rows: got %d %v, Node %d %v", len(rows), rows, len(wantRows), wantRows)
			}
			for k, r := range rows {
				if unbase(r) != wantRows[k].(string) {
					t.Errorf("row %d\ngot  %s\nNode %s", k, unbase(r), wantRows[k])
				}
			}
			wantSpawns := want.Get("spawns").([]any)
			if len(spawns) != len(wantSpawns) {
				t.Fatalf("spawns: got %d, Node %d", len(spawns), len(wantSpawns))
			}
			for k, s := range spawns {
				w := wantSpawns[k].(ledger.Object)
				if g := unbase(s.Get("text").(string)); g != w.Get("text") {
					t.Errorf("spawn %d packet\ngot  %q\nNode %q", k, g, w.Get("text"))
				}
				s.Set("text", w.Get("text"))
				if g, ws := mustJSON(t, s), mustJSON(t, w); g != ws {
					t.Errorf("spawn %d\ngot  %s\nNode %s", k, g, ws)
				}
			}
			if g, ws := mustJSON(t, b537Left(t, work)), mustJSON(t, want.Get("left")); g != ws {
				t.Errorf("work dir left %s, Node %s", g, ws)
			}
		})
	}
}
