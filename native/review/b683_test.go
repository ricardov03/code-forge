package review

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// b683OpenIDs is the Node generator's openIds: the ids listed under the
// packet's `## open findings`.
var b683OpenIDs = regexp.MustCompile(`(?m)^- (\S+) \(`)

// b683Fill is the Node generator's fill: "$HUNKS" is the packet's reviewed
// hunks, "$RESOLVE" resolves every finding the packet lists.
func b683Fill(v any, text string) any {
	switch x := v.(type) {
	case string:
		if x == "$HUNKS" {
			return hunksOf(text)
		}
		if x == "$RESOLVE" {
			out := []any{}
			at := strings.Index(text, "\n## open findings\n")
			if at < 0 {
				return out
			}
			for _, m := range b683OpenIDs.FindAllStringSubmatch(text[at:], -1) {
				out = append(out, ledger.Object{{Key: "id", Value: m[1]}, {Key: "resolved", Value: true}, {Key: "why", Value: "fixed"}})
			}
			return out
		}
		return x
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = b683Fill(e, text)
		}
		return out
	case ledger.Object:
		out := ledger.Object{}
		for _, f := range x {
			out = append(out, ledger.Field{Key: f.Key, Value: b683Fill(f.Value, text)})
		}
		return out
	}
	return v
}

func b683Files(o any) map[string]string {
	out := map[string]string{}
	for _, f := range o.(ledger.Object) {
		out[f.Key] = f.Value.(string)
	}
	return out
}

// TestB683MarkdownRecheckBySectionMatchesNode: a Markdown recheck packet
// over the budget runs in Go section by section like Node's runRound(kind
// recheck) (fixloop.mjs packRecheck): one session per heading section with
// its own open findings, section findings named S<i>.<id>, a section's
// unavailable session, late findings outside the fix hunks, a finding with
// no line_start, recheck_scope file with a base, emoji headings, a heading
// cut inside a surrogate pair in a section that runs, and a group alone
// over the budget (stop naming its heading). State, rows, each session's
// level, role and packet, and the work dir match Node (goldens: Node's
// runRound on the same repositories and scripted sessions).
func TestB683MarkdownRecheckBySectionMatchesNode(t *testing.T) {
	useRepoLenses(t)
	t.Setenv("HOME", t.TempDir())
	fxRaw := readOrdered(t, "b683_md_recheck_fixture.json")
	golden := readOrdered(t, "b683_md_recheck_golden.json").Get("cases").(ledger.Object)
	cases := fxRaw.Get("cases").([]any)
	if len(cases) != len(golden) {
		t.Fatalf("cases %d, golden %d", len(cases), len(golden))
	}
	ran := 0
	for _, raw := range cases {
		c := raw.(ledger.Object)
		name := c.Get("name").(string)
		if name == "emoji-split-heading-stop" {
			continue // TestB683SplitHeadingStopHandsOver
		}
		ran++
		t.Run(name, func(t *testing.T) {
			want := golden.Get(name).(ledger.Object)
			repo := buildRepo(t, fixture{Commit: b683Files(c.Get("commit")), After: b683Files(c.Get("after"))})
			cfg := plainCfg(t, c.Get("cfg"))
			script := c.Get("script").([]any)
			file := c.Get("file").(string)
			rows := []ledger.Object{}
			type seen struct {
				opts SpawnOpts
				text string
			}
			spawns := []seen{}
			used := 0
			spawn := func(o SpawnOpts) (ledger.Object, error) {
				data, err := os.ReadFile(o.PromptPath)
				if err != nil {
					t.Fatal(err)
				}
				spawns = append(spawns, seen{o, string(data)})
				used++
				if used > len(script) {
					return nil, errors.New("script short")
				}
				res, err := ledger.DecodeObject([]byte(mustJSON(t, b683Fill(script[used-1], string(data)))))
				if err != nil {
					t.Fatal(err)
				}
				return res, nil
			}
			work := filepath.Join(t.TempDir(), name)
			in := Input{RepoRoot: repo, File: file, Cfg: cfg, WorkDir: work,
				RulesDigest: "rule one\nrule two", FactsExcerpt: "facts here"}
			if c.Get("base") == true {
				out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
				if err != nil {
					t.Fatal(err)
				}
				in.Base = strings.TrimSpace(string(out))
			}
			state, err := ledger.DecodeObject([]byte(mustJSON(t, c.Get("state"))))
			if err != nil {
				t.Fatal(err)
			}
			deps := LoopDeps{RepoRoot: repo, Cfg: cfg,
				WriteRow: func(r ledger.Object) error { rows = append(rows, r.Clone()); return nil },
				Review:   func() (ledger.Object, error) { return nil, errors.New("no full review") },
				Input:    in,
				Session: Deps{Spawn: spawn, Warn: func(string, string) {},
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
	if ran == 0 {
		t.Fatal("no case ran")
	}
}

// TestB683SplitHeadingStopHandsOver: the one case kept on Node. When the
// section a recheck stop names has a heading cut inside a surrogate pair,
// Node writes that lone surrogate into the state's next.section (golden);
// Go's JSON writer cannot, so CheckLoop and RunRound refuse with
// ErrUnsupported before any row, session or state change.
func TestB683SplitHeadingStopHandsOver(t *testing.T) {
	useRepoLenses(t)
	t.Setenv("HOME", t.TempDir())
	fxRaw := readOrdered(t, "b683_md_recheck_fixture.json")
	golden := readOrdered(t, "b683_md_recheck_golden.json").Get("cases").(ledger.Object)
	var c ledger.Object
	for _, raw := range fxRaw.Get("cases").([]any) {
		if raw.(ledger.Object).Get("name") == "emoji-split-heading-stop" {
			c = raw.(ledger.Object)
		}
	}
	if c == nil {
		t.Fatal("no emoji-split-heading-stop case")
	}
	if w := golden.Get("emoji-split-heading-stop").(ledger.Object).Get("state").(string); !strings.Contains(w, `\ud83d"`) {
		t.Fatalf("Node's stop should name a heading ending in a lone surrogate: %s", w)
	}
	repo := buildRepo(t, fixture{Commit: b683Files(c.Get("commit")), After: b683Files(c.Get("after"))})
	cfg := plainCfg(t, c.Get("cfg"))
	rows := 0
	spawned := 0
	work := filepath.Join(t.TempDir(), "w")
	deps := LoopDeps{RepoRoot: repo, Cfg: cfg,
		WriteRow: func(ledger.Object) error { rows++; return nil },
		Review:   func() (ledger.Object, error) { return nil, errors.New("no full review") },
		Input:    Input{RepoRoot: repo, File: c.Get("file").(string), Cfg: cfg, WorkDir: work},
		Session: Deps{Spawn: func(SpawnOpts) (ledger.Object, error) { spawned++; return nil, errors.New("no") },
			Warn: func(string, string) {}, Lock: func(_ string, fn func() error) error { return fn() }}}
	state, err := ledger.DecodeObject([]byte(mustJSON(t, c.Get("state"))))
	if err != nil {
		t.Fatal(err)
	}
	before := mustJSON(t, state)
	if _, err := CheckLoop(state.Clone(), "recheck", deps); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("CheckLoop: %v, want ErrUnsupported", err)
	}
	if _, err := RunRound(state, deps, "recheck"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("RunRound: %v, want ErrUnsupported", err)
	}
	if rows != 0 || spawned != 0 || mustJSON(t, state) != before {
		t.Fatalf("rows %d, spawns %d, state changed %v", rows, spawned, mustJSON(t, state) != before)
	}
}

// TestB683MarkdownEngineSplitHeadingOutsideChangesMatchesNode: a Markdown
// full review over the budget whose file has a heading cut inside a
// surrogate pair in a section with no change now runs in Go section by
// section like Node's reviewFile (emoji headings, a literal `\ud83d` escape).
func TestB683MarkdownEngineSplitHeadingOutsideChangesMatchesNode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	b545MatchNode(t, "b683_md_engine_fixture.json", "b683_md_engine_golden.json")
}
