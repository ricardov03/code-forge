package review

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// TestB545RecheckRecoveryMatchesNode: a recheck round with
// review.second_levels (or review.second_provider), a levels.L2 fallback
// ladder or review.routes runs in Go like Node's runRound(kind recheck)
// (fixloop.mjs recheckSession): a schema miss's signed row and, at the
// packet's second broken result, the schema_fallback mark and one
// second-level try (its own ladder on a timeout), the second level's miss
// row, a timed-out first attempt trying the second level, a same-provider
// second level, B30's same-model retry with a configured L2 ladder (the
// first attempt pins nothing) and a routes config. State, rows, each
// session's options and packet, and the work dir match Node (goldens:
// Node's runRound on the same repository and scripted sessions).
func TestB545RecheckRecoveryMatchesNode(t *testing.T) {
	useRepoLenses(t)
	fxRaw := readOrdered(t, "b545_recheck_fixture.json")
	fx := fixture{Commit: map[string]string{}, After: map[string]string{}}
	for key, dst := range map[string]map[string]string{"commit": fx.Commit, "after": fx.After} {
		for _, f := range fxRaw.Get(key).(ledger.Object) {
			dst[f.Key] = f.Value.(string)
		}
	}
	repo := buildRepo(t, fx)
	golden := readOrdered(t, "b545_recheck_golden.json").Get("cases").(ledger.Object)
	cases := fxRaw.Get("cases").([]any)
	if len(cases) != len(golden) {
		t.Fatalf("cases %d, golden %d", len(cases), len(golden))
	}
	sortedJSON := func(v any) string {
		var plain any
		if err := json.Unmarshal([]byte(mustJSON(t, v)), &plain); err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(plain)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	for _, raw := range cases {
		c := raw.(ledger.Object)
		name := c.Get("name").(string)
		t.Run(name, func(t *testing.T) {
			want := golden.Get(name).(ledger.Object)
			cfg := plainCfg(t, c.Get("cfg"))
			seed := int(c.Get("seed").(float64))
			script := c.Get("script").([]any)
			rows := []ledger.Object{}
			type seen struct {
				opts SpawnOpts
				text string
			}
			spawns := []seen{}
			used := 0
			history := func(hash, level, role string) (int, bool, error) {
				n, fellBack := seed, false
				for _, r := range rows {
					if r.Get("event") == "review.schema_invalid" && r.Get("packet_hash") == hash && r.Get("level") == level && r.Get("role") == role && r.Get("second_level") == false {
						n++
					}
					if r.Get("event") == "review.schema_fallback" && r.Get("packet_hash") == hash {
						fellBack = true
					}
				}
				return n, fellBack, nil
			}
			spawn := func(o SpawnOpts) (ledger.Object, error) {
				data, err := os.ReadFile(o.PromptPath)
				if err != nil {
					t.Fatal(err)
				}
				spawns = append(spawns, seen{o, string(data)})
				used++ // counted like Node's harness: i++ before the short-script throw
				if used > len(script) {
					return nil, errors.New("script short")
				}
				item := script[used-1]
				res, err := ledger.DecodeObject([]byte(mustJSON(t, withHunks(item, hunksOf(string(data))))))
				if err != nil {
					t.Fatal(err)
				}
				return res, nil
			}
			work := filepath.Join(t.TempDir(), name)
			state, err := ledger.DecodeObject([]byte(mustJSON(t, fxRaw.Get("state"))))
			if err != nil {
				t.Fatal(err)
			}
			deps := LoopDeps{RepoRoot: repo, Cfg: cfg,
				WriteRow: func(r ledger.Object) error { rows = append(rows, r.Clone()); return nil },
				Review:   func() (ledger.Object, error) { return nil, errors.New("no full review") },
				Input:    Input{RepoRoot: repo, File: "a.go", Cfg: cfg, Kind: "code", WorkDir: work},
				Session: Deps{Spawn: spawn, SchemaHistory: history, Warn: func(string, string) {},
					Lock: func(_ string, fn func() error) error { return fn() }}}
			if _, err := CheckLoop(state.Clone(), "recheck", deps); err != nil {
				t.Fatalf("CheckLoop refused a round Node runs: %v", err)
			}
			got, err := RunRound(state, deps, "recheck")
			if err != nil {
				t.Fatalf("RunRound: %v", err)
			}
			if g, w := mustJSON(t, got), want.Get("state").(string); g != w {
				t.Errorf("state\ngot  %s\nNode %s", g, w)
			}
			wantRows := want.Get("rows").([]any)
			if len(rows) != len(wantRows) {
				t.Fatalf("rows: got %d %v, Node %d %v", len(rows), rows, len(wantRows), wantRows)
			}
			for k, r := range rows {
				if g := mustJSON(t, r); g != wantRows[k].(string) {
					t.Errorf("row %d\ngot  %s\nNode %s", k, g, wantRows[k])
				}
			}
			if float64(used) != want.Get("used").(float64) {
				t.Errorf("answers used %d, Node %v", used, want.Get("used"))
			}
			wantSpawns := want.Get("spawns").([]any)
			if len(spawns) != len(wantSpawns) {
				t.Fatalf("spawns: got %d, Node %d", len(spawns), len(wantSpawns))
			}
			for k, s := range spawns {
				w := wantSpawns[k].(ledger.Object)
				if s.text != w.Get("text") {
					t.Errorf("spawn %d packet\ngot  %s\nNode %s", k, s.text, w.Get("text"))
				}
				if s.opts.Level != w.Get("level") || s.opts.Role != w.Get("role") {
					t.Errorf("spawn %d level/role %s/%s, Node %v/%v", k, s.opts.Level, s.opts.Role, w.Get("level"), w.Get("role"))
				}
				if g, ws := mustJSON(t, s.opts.RowExtra), mustJSON(t, w.Get("rowExtra")); g != ws {
					t.Errorf("spawn %d rowExtra %s, Node %s", k, g, ws)
				}
				var gotCfg any
				if s.opts.Cfg != nil {
					gotCfg = s.opts.Cfg
				}
				if g, ws := sortedJSON(gotCfg), sortedJSON(w.Get("cfg")); g != ws {
					t.Errorf("spawn %d cfg %s, Node %s", k, g, ws)
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
			if g, ws := mustJSON(t, left), mustJSON(t, want.Get("left")); g != ws {
				t.Errorf("work dir left %s, Node %s", g, ws)
			}
		})
	}
}
