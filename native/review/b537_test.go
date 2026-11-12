package review

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// testdata/b537_golden.json is Node's reviewFile (the engine cases) and
// runRound kind recheck (the recheck cases) on the b537_fixture.json
// repository: related files, definitions, the import-header context, the
// deletion note, the test-quality hint, moved-code hints, cross-file
// evidence (fits, cut line, nothing, cap and trim) and recheck_scope file.
// Each case ran in its own Node process with a temp HOME and a frozen clock;
// the base commit id reads $BASE.

// b537Repo builds the generator's repository: the commit is the base, the
// after files and the deletions are the working tree. It returns the root
// and the base commit id.
func b537Repo(t *testing.T, fx ledger.Object) (string, string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return string(out)
	}
	put := func(files ledger.Object) {
		for _, f := range files {
			full := filepath.Join(dir, f.Key)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(f.Value.(string)), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	git("init", "-q")
	put(fx.Get("commit").(ledger.Object))
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	sha := strings.TrimSpace(git("rev-parse", "HEAD"))
	put(fx.Get("after").(ledger.Object))
	for _, f := range fx.Get("deleted").([]any) {
		if err := os.Remove(filepath.Join(dir, f.(string))); err != nil {
			t.Fatal(err)
		}
	}
	return dir, sha
}

func b537Strings(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := []string{}
	for _, s := range list {
		out = append(out, s.(string))
	}
	return out
}

// b537Left is the work dir's names, random hex as X, sorted (nil: no dir).
func b537Left(t *testing.T, work string) any {
	t.Helper()
	entries, err := os.ReadDir(work)
	if err != nil {
		return nil
	}
	out := []any{}
	for _, e := range entries {
		out = append(out, hex12.ReplaceAllString(e.Name(), "X"))
	}
	return out
}

// TestB537PacketPartsMatchNode: on every case the Go review (File) and the Go
// recheck round (RunRound) give Node's result, ledger rows, session calls and
// packet bytes; none of them is refused as unsupported any more.
func TestB537PacketPartsMatchNode(t *testing.T) {
	useRepoLenses(t)
	fixtureFile := readOrdered(t, "b537_fixture.json")
	repo, sha := b537Repo(t, fixtureFile.Get("fixture").(ledger.Object))
	golden := readOrdered(t, "b537_golden.json").Get("cases").(ledger.Object)
	cases := fixtureFile.Get("cases").([]any)
	if len(cases) != len(golden) || len(cases) < 20 {
		t.Fatalf("cases %d, golden %d", len(cases), len(golden))
	}
	base := baseCfg()
	for _, raw := range cases {
		c := raw.(ledger.Object)
		name := c.Get("name").(string)
		t.Run(name, func(t *testing.T) {
			want := golden.Get(name).(ledger.Object)
			unbase := func(s string) string { return strings.ReplaceAll(s, sha, "$BASE") }
			cfg := base
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
			var result string
			var rows []string
			var spawns []ledger.Object
			if c.Get("kind") == "engine" {
				risk := 1.0
				in.Risk = &risk
				if err := Check(in); err != nil {
					t.Fatalf("Check refused a case Node reviews: %v", err)
				}
				run := runEngine(t, in, []any{okSession()}, true)
				if run.err != nil {
					t.Fatalf("File: %v", run.err)
				}
				result, rows = mustJSON(t, run.result), run.rows
				for _, s := range run.spawns {
					spawns = append(spawns, ledger.Object{{Key: "level", Value: s.Get("level")}, {Key: "role", Value: s.Get("role")},
						{Key: "text", Value: s.Get("text")}, {Key: "rowExtra", Value: s.Get("rowExtra")}})
				}
			} else {
				t.Setenv("HOME", t.TempDir())
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
					state.Set("reviewed_content", fixtureFile.Get("fixture").(ledger.Object).Get("commit").(ledger.Object).Get(file))
				}
				spawned := 0
				deps := LoopDeps{RepoRoot: repo, Cfg: cfg, Input: in,
					WriteRow: func(r ledger.Object) error { rows = append(rows, mustJSON(t, r)); return nil },
					Review:   func() (ledger.Object, error) { t.Fatal("full review ran"); return nil, nil },
					Session: Deps{Rand: bytes.NewReader(bytes.Repeat([]byte{0xab}, 600)), Spawn: func(o SpawnOpts) (ledger.Object, error) {
						text, err := os.ReadFile(o.PromptPath)
						if err != nil {
							t.Fatal(err)
						}
						spawned++
						spawns = append(spawns, ledger.Object{{Key: "level", Value: o.Level}, {Key: "role", Value: o.Role},
							{Key: "text", Value: string(text)}, {Key: "rowExtra", Value: o.RowExtra}})
						return withHunks(answer, hunksOf(string(text))).(ledger.Object), nil
					}}}
				got, err := RunRound(state, deps, "recheck")
				if err != nil {
					t.Fatalf("RunRound: %v", err)
				}
				result = js(t, got)
			}
			if got := unbase(result); got != want.Get("result").(string) {
				t.Errorf("result\ngot  %s\nNode %s", got, want.Get("result"))
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
				if got := unbase(s.Get("text").(string)); got != w.Get("text") {
					t.Errorf("spawn %d packet\ngot  %q\nNode %q", k, got, w.Get("text"))
				}
				s.Set("text", w.Get("text"))
				if got, ws := mustJSON(t, s), mustJSON(t, w); got != ws {
					t.Errorf("spawn %d\ngot  %s\nNode %s", k, got, ws)
				}
			}
			if got, ws := mustJSON(t, b537Left(t, work)), mustJSON(t, want.Get("left")); got != ws {
				t.Errorf("work dir left %s, Node %s", got, ws)
			}
		})
	}
}
