package review

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// testdata/fixloop_golden.json is Node's real runRound (kind full, no Jev),
// planAndRecord (blockOnly) and rulesRisk over the same inputs (scratch tool
// b495tools/fixloop_golden.mjs, frozen clock, temp HOME).
func fixloopGolden(t *testing.T) ledger.Object {
	t.Helper()
	data, err := os.ReadFile("testdata/fixloop_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	g, err := ledger.DecodeObject(data)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func js(t *testing.T, v any) string {
	t.Helper()
	b, err := ledger.Stringify(v, false)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func plain(t *testing.T, v any) map[string]any {
	t.Helper()
	o, _ := v.(ledger.Object)
	b, err := ledger.Stringify(o, false)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRunRoundFullMatchesNode(t *testing.T) {
	g := fixloopGolden(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "app.go"), []byte("package app\n\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases, _ := g.Get("rounds").([]any)
	if len(cases) < 20 {
		t.Fatalf("golden has %d round cases", len(cases))
	}
	for _, c := range cases {
		tc := c.(ledger.Object)
		name := tc.Get("name").(string)
		t.Run(name, func(t *testing.T) {
			failOn, _ := tc.Get("failOn").(string)
			rows := []any{}
			cfg := plain(t, tc.Get("cfg"))
			state := NewFileState("src/app.go", tc.Get("level").(string), tc.Get("rung") == true)
			result := tc.Get("result").(ledger.Object)
			calls := 0
			got, err := RunRound(state, LoopDeps{RepoRoot: root, Cfg: cfg,
				WriteRow: func(row ledger.Object) error {
					if row.Get("event") == failOn {
						return errors.New("boom")
					}
					rows = append(rows, row)
					return nil
				},
				Review: func() (ledger.Object, error) { calls++; return result, nil },
			}, "full")
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("review ran %d times", calls)
			}
			if a, b := js(t, rows), js(t, tc.Get("rows")); a != b {
				t.Fatalf("rows differ\n go: %s\nnode: %s", a, b)
			}
			if a, b := js(t, got), js(t, tc.Get("state")); a != b {
				t.Fatalf("state differs\n go: %s\nnode: %s", a, b)
			}
		})
	}
}

func TestRunRoundRefusesBeforeReview(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte{0xff, 0xfe}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.go"), []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		file, level, kind string
		cfg               map[string]any
	}{
		"recheck":      {"b.go", "L2", "recheck", nil},
		"patch_check":  {"b.go", "L2", "patch_check", nil},
		"bad level":    {"b.go", "L9", "full", nil},
		"bad stop_at":  {"b.go", "L2", "full", map[string]any{"escalation": map[string]any{"stop_at": "top"}}},
		"not utf-8":    {"a.go", "L2", "full", nil},
		"glob in name": {"b*.go", "L2", "full", nil},
	} {
		t.Run(name, func(t *testing.T) {
			ran := false
			_, err := RunRound(NewFileState(tc.file, tc.level, false), LoopDeps{RepoRoot: root, Cfg: tc.cfg,
				Review: func() (ledger.Object, error) { ran = true; return nil, nil }}, tc.kind)
			if !IsUnsupported(err) || ran {
				t.Fatalf("err=%v ran=%v", err, ran)
			}
		})
	}
}

func TestRunRoundReviewThrowLeavesState(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.WriteFile(filepath.Join(root, "b.go"), []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state := NewFileState("b.go", "L2", false)
	before := js(t, state)
	_, err := RunRound(state, LoopDeps{RepoRoot: root, Review: func() (ledger.Object, error) { return nil, errors.New("spawn threw") }}, "full")
	if err == nil || IsUnsupported(err) || js(t, state) != before {
		t.Fatalf("err=%v state=%s", err, js(t, state))
	}
}

func TestBudgetRowsAndRulesRiskMatchNode(t *testing.T) {
	g := fixloopGolden(t)
	budgets, _ := g.Get("budgets").([]any)
	if len(budgets) < 7 {
		t.Fatalf("golden has %d budget cases", len(budgets))
	}
	for _, c := range budgets {
		tc := c.(ledger.Object)
		t.Run(tc.Get("name").(string), func(t *testing.T) {
			cfg := plain(t, tc.Get("cfg"))
			files := []BudgetFile{}
			for _, f := range tc.Get("files").([]any) {
				files = append(files, BudgetFile{File: f.(string), Tier: TierOf(RulesRisk(f.(string), 0, 0, cfg))})
			}
			rows := []any{}
			for _, r := range BudgetRows(files, cfg, "B1") {
				rows = append(rows, r)
			}
			if a, b := js(t, rows), js(t, tc.Get("rows")); a != b {
				t.Fatalf("rows differ\n go: %s\nnode: %s", a, b)
			}
		})
	}
	risks, _ := g.Get("risks").([]any)
	for _, c := range risks {
		tc := c.(ledger.Object)
		got := RulesRisk(tc.Get("file").(string), int(tc.Get("plus").(float64)), int(tc.Get("minus").(float64)), plain(t, tc.Get("cfg")))
		if got != tc.Get("risk").(float64) {
			t.Fatalf("%s: risk %v, Node %v", js(t, tc), got, tc.Get("risk"))
		}
	}
}
