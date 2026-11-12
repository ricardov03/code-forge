package review

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := ledger.Stringify(v, false)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func plainCfg(t *testing.T, v any) map[string]any {
	t.Helper()
	var cfg map[string]any
	if err := json.Unmarshal([]byte(mustJSON(t, v)), &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// hunksOf reads the hunk headers the packet lists, as the fake reviewer copies them.
func hunksOf(text string) []any {
	out := []any{}
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "- @@") {
			out = append(out, l[2:])
		}
	}
	return out
}

func withHunks(v any, hunks []any) any {
	switch x := v.(type) {
	case string:
		if x == "$HUNKS" {
			return hunks
		}
		return x
	case ledger.Object:
		out := ledger.Object{}
		for _, f := range x {
			out = append(out, ledger.Field{Key: f.Key, Value: withHunks(f.Value, hunks)})
		}
		return out
	case []any:
		out := []any{}
		for _, e := range x {
			out = append(out, withHunks(e, hunks))
		}
		return out
	}
	return v
}

var hex12 = regexp.MustCompile(`[0-9a-f]{12}`)

type engineRun struct {
	result   ledger.Object
	err      error
	rows     []string
	spawns   []ledger.Object
	warnings []string
	cfgL2    []any // each spawn's levels.L2 (B514)
}

// runEngine runs File with a fake session that plays the script.
func runEngine(t *testing.T, in Input, script []any, writer bool, more ...func(*Deps)) engineRun {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // the schema-miss mark's lock lives under $HOME
	var run engineRun
	i := 0
	deps := Deps{
		Spawn: func(o SpawnOpts) (ledger.Object, error) {
			text, err := os.ReadFile(o.PromptPath)
			if err != nil {
				t.Fatal(err)
			}
			info, _ := os.Stat(o.PromptPath)
			if info.Mode().Perm() != 0o600 {
				t.Errorf("packet file mode %v", info.Mode().Perm())
			}
			run.spawns = append(run.spawns, ledger.Object{{Key: "level", Value: o.Level}, {Key: "role", Value: o.Role},
				{Key: "name", Value: hex12.ReplaceAllString(filepath.Base(o.PromptPath), "X")},
				{Key: "inWorkDir", Value: filepath.Dir(o.PromptPath) == in.WorkDir}, {Key: "text", Value: string(text)},
				{Key: "rowExtra", Value: o.RowExtra}, {Key: "cfgSame", Value: fmt.Sprintf("%p", o.Cfg) == fmt.Sprintf("%p", in.Cfg)}})
			levels, _ := o.Cfg["levels"].(map[string]any)
			run.cfgL2 = append(run.cfgL2, levels["L2"])
			if i >= len(script) {
				t.Fatalf("spawn %d: script has %d items", i+1, len(script))
			}
			item := script[i]
			i++
			if item == "throw" {
				return nil, errors.New("boom")
			}
			if item == nil {
				return nil, nil
			}
			return withHunks(item, hunksOf(string(text))).(ledger.Object), nil
		},
		Warn: func(w, m string) { run.warnings = append(run.warnings, w+": "+m) },
		Rand: bytes.NewReader(bytes.Repeat([]byte{0xab}, 600)),
	}
	if writer {
		deps.WriteRow = func(r ledger.Object) error {
			run.rows = append(run.rows, mustJSON(t, r))
			return nil
		}
	}
	for _, f := range more {
		f(&deps)
	}
	run.result, run.err = File(in, deps)
	return run
}

// gapCases are the cases Node finishes after the session with code Go does not
// port; Go stops there with a GapError and the rows written so far.
var gapCases = map[string]string{}

// TestReviewFileMatchesNode: on 25 tickets the Go review gives Node's result
// object, ledger rows, session calls, packet text and retry warnings, byte for
// byte, and leaves no packet file behind.
func TestReviewFileMatchesNode(t *testing.T) {
	useRepoLenses(t)
	fx := readFixture(t, "packet_fixture.json")
	engineFx := readOrdered(t, "engine_fixture.json")
	if after, ok := engineFx.Get("after").(ledger.Object); ok {
		for _, f := range after {
			fx.After[f.Key] = f.Value.(string)
		}
	}
	repo := buildRepo(t, fx)
	golden := readOrdered(t, "engine_golden.json")
	cases := engineFx.Get("cases").([]any)
	if len(cases) != len(golden) {
		t.Fatalf("cases %d, golden %d", len(cases), len(golden))
	}
	for _, raw := range cases {
		c := raw.(ledger.Object)
		name := c.Get("name").(string)
		t.Run(name, func(t *testing.T) {
			want := golden.Get(name).(ledger.Object)
			work := filepath.Join(t.TempDir(), name)
			in := Input{RepoRoot: repo, File: c.Get("file").(string), Cfg: plainCfg(t, c.Get("cfg")),
				RulesDigest: c.Get("digest").(string), FactsExcerpt: c.Get("facts").(string), WorkDir: work}
			if risk, ok := c.Get("risk").(float64); ok {
				in.Risk = &risk
			}
			in.Kind, _ = c.Get("kind").(string)
			strs := func(key string) []string {
				list, ok := c.Get(key).([]any)
				if !ok {
					return nil
				}
				out := []string{}
				for _, v := range list {
					out = append(out, v.(string))
				}
				return out
			}
			in.OwnedFiles, in.ChangedFiles, in.TestFiles, in.Secrets = strs("owned"), strs("changed"), strs("tests"), strs("secrets")
			if err := Check(in); err != nil {
				t.Fatalf("Check refused a case Node reviews: %v", err)
			}
			run := runEngine(t, in, c.Get("script").([]any), c.Get("writer") == true)
			wantRows := want.Get("rows").([]any)
			if gap, ok := gapCases[name]; ok {
				var ge *GapError
				if !errors.As(run.err, &ge) || ge.Case != gap {
					t.Fatalf("got %v, want GapError %s", run.err, gap)
				}
				wantRows = wantRows[:1]
			} else {
				if run.err != nil {
					t.Fatalf("File: %v", run.err)
				}
				if got := mustJSON(t, run.result); got != want.Get("result").(string) {
					t.Errorf("result\ngot  %s\nNode %s", got, want.Get("result"))
				}
			}
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
				w.Delete("schemaSame")
				if got, ws := mustJSON(t, s), mustJSON(t, w); got != ws {
					t.Errorf("spawn %d\ngot  %s\nNode %s", k, got, ws)
				}
			}
			// Each extra session is a timeout retry; Node logs one review_retry warning per retry.
			retries := max(len(wantSpawns)-1, 0)
			if len(run.warnings) != retries {
				t.Errorf("warnings %v, want %d", run.warnings, retries)
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

func okSession(extra ...ledger.Field) ledger.Object {
	answer := ledger.Object{{Key: "passed", Value: true}, {Key: "summary", Value: "fine"}, {Key: "reviewed_hunks", Value: "$HUNKS"}, {Key: "findings", Value: []any{}}}
	answer = append(answer, extra...)
	return ledger.Object{{Key: "status", Value: "ok"}, {Key: "provider", Value: "claude"}, {Key: "model", Value: "m2"}, {Key: "fallback_step", Value: 0.0},
		{Key: "exit_code", Value: 0.0}, {Key: "answer", Value: answer},
		{Key: "usage", Value: ledger.Object{{Key: "tokens_in", Value: 900.0}, {Key: "tokens_out", Value: 150.0}}}}
}

func timeoutSession() ledger.Object {
	return ledger.Object{{Key: "status", Value: "timeout"}, {Key: "provider", Value: "claude"}, {Key: "model", Value: "m2"}, {Key: "duration_ms", Value: 600000.0}}
}

func baseCfg() map[string]any {
	return map[string]any{"provider": "claude", "levels": map[string]any{"L2": map[string]any{"provider": "claude", "model": "m2"}}}
}

// TestReviewFileGapAfterSpawn: the one case Node finishes with code Go may
// not model (timeoutFallbackFor on a config the seam cannot read, or no seam)
// stops with a GapError after the second timeout's rows, never a wrong result.
// B514: needs_file and the schema-miss diagnosis are no longer gaps
// (TestB514ReviewFileMatchesNode).
func TestReviewFileGapAfterSpawn(t *testing.T) {
	useRepoLenses(t)
	repo := buildRepo(t, readFixture(t, "packet_fixture.json"))
	risk := 1.0
	for _, c := range []struct {
		name string
		seam func(TimeoutFallbackInput) (*Fallback, error)
	}{
		{"no-seam", nil},
		{"seam-unsure", func(TimeoutFallbackInput) (*Fallback, error) { return nil, errors.New("route unsure") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := Input{RepoRoot: repo, File: "notes.txt", Cfg: baseCfg(), Risk: &risk, WorkDir: filepath.Join(t.TempDir(), "w")}
			run := runEngine(t, in, []any{timeoutSession(), timeoutSession()}, true, func(d *Deps) { d.TimeoutFallback = c.seam })
			var ge *GapError
			if !errors.As(run.err, &ge) || ge.Case != "timeout-fallback" {
				t.Fatalf("got %v, want GapError timeout-fallback", run.err)
			}
			if IsUnsupported(run.err) {
				t.Errorf("a gap after spawn must not read as a pre-spawn refusal")
			}
			if len(run.rows) != 3 {
				t.Errorf("rows %d %v, want 3", len(run.rows), run.rows)
			}
		})
	}
}

// TestReviewFileUnsupportedBeforeSpawn: every case this port does not cover is
// refused with ErrUnsupported before any session runs or any row is written,
// so the caller can still hand the ticket to Node.
func TestReviewFileUnsupportedBeforeSpawn(t *testing.T) {
	useRepoLenses(t)
	fx := readFixture(t, "packet_fixture.json")
	fx.After["pkg/thing_test.txt"] = "one\n"
	fx.After["README.md"] = strings.Repeat("# head\nsome words here\n", 200)
	fx.After["emoji.md"] = "# " + strings.Repeat("a", 117) + "\U0001F600 tail\n" + strings.Repeat("some words here\n", 200)
	repo := buildRepo(t, fx)
	if err := os.Remove(filepath.Join(repo, "same.txt")); err != nil {
		t.Fatal(err)
	}
	f := func(v float64) *float64 { return &v }
	withReview := func(r map[string]any) map[string]any { c := baseCfg(); c["review"] = r; return c }
	withL2 := func(l map[string]any) map[string]any { c := baseCfg(); c["levels"] = map[string]any{"L2": l}; return c }
	cases := []struct {
		name string
		in   Input
	}{
		{"dual-lens", Input{File: "notes.txt", Cfg: baseCfg(), Risk: f(2)}},
		{"risk-out-of-range", Input{File: "notes.txt", Cfg: baseCfg(), Risk: f(3.5)}},
		{"consensus", Input{File: "notes.txt", Cfg: withReview(map[string]any{"multimodel": true}), Risk: f(3)}},
		{"routes", Input{File: "notes.txt", Cfg: withReview(map[string]any{"routes": []any{"x"}}), Risk: f(1)}},
		{"second-levels", Input{File: "notes.txt", Cfg: withReview(map[string]any{"second_levels": []any{"L3"}}), Risk: f(1)}},
		{"fallback-ladder", Input{File: "notes.txt", Cfg: withL2(map[string]any{"provider": "claude", "model": "m2", "fallback": []any{"x"}}), Risk: f(1)}},
		{"no-model", Input{File: "notes.txt", Cfg: withL2(map[string]any{"provider": "claude"}), Risk: f(1)}},
		{"no-provider", Input{File: "notes.txt", Cfg: map[string]any{"levels": map[string]any{"L2": map[string]any{"model": "m2"}}}, Risk: f(1)}},
		{"moved-code", Input{File: "notes.txt", Cfg: baseCfg(), Risk: f(1), HasPeerDiffs: true}},
		{"peers-with-base", Input{File: "notes.txt", Cfg: baseCfg(), Risk: f(1), Base: "HEAD", Peers: []string{"big.txt"}}},
		{"consensus-code-kind", Input{File: "notes.txt", Cfg: withReview(map[string]any{"multimodel": true, "single_reviewer_max_risk": 0.0}), Risk: f(2), Kind: "code"}},
		{"deleted", Input{File: "same.txt", Cfg: baseCfg(), Risk: f(1)}},
		{"test-file", Input{File: "pkg/thing_test.txt", Cfg: baseCfg(), Risk: f(1)}},
		// B545: a Markdown file over the budget reviews by section (TestB545MarkdownSectionsMatchNode);
		// only a heading Node cuts inside a surrogate pair stays with Node
		{"markdown-heading-surrogate-cut", Input{File: "emoji.md", Cfg: withReview(map[string]any{"budgets": map[string]any{"full_in": 100.0}}), Risk: f(1)}},
		{"related-files", Input{File: "app.js", Cfg: baseCfg(), Risk: f(1)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.in.RepoRoot = repo
			c.in.WorkDir = filepath.Join(t.TempDir(), "w")
			if err := Check(c.in); !IsUnsupported(err) {
				t.Fatalf("Check: got %v, want ErrUnsupported", err)
			}
			if _, err := os.Stat(c.in.WorkDir); !os.IsNotExist(err) {
				t.Fatalf("Check wrote the work dir: %v", err)
			}
			rows := 0
			_, err := File(c.in, Deps{
				Spawn:    func(SpawnOpts) (ledger.Object, error) { t.Fatal("spawned"); return nil, nil },
				WriteRow: func(ledger.Object) error { rows++; return nil },
			})
			if !IsUnsupported(err) {
				t.Fatalf("got %v, want ErrUnsupported", err)
			}
			if rows != 0 {
				t.Errorf("%d rows written before the refusal", rows)
			}
		})
	}
}
