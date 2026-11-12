package review

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// TestB514ReviewFileMatchesNode: the needs_file round, the schema-miss row
// with its diagnosis and the second-timeout fallback give Node's result,
// ledger rows, session calls (packet text, config), warnings and seam calls,
// byte for byte (goldens: Node's reviewFile on the same repository).
func TestB514ReviewFileMatchesNode(t *testing.T) {
	matchEngineGolden(t, "b514_engine_fixture.json", "b514_engine_golden.json")
}

// matchEngineGolden runs each fixture case through File with a fake session
// and compares it with Node's golden (B514, B538).
func matchEngineGolden(t *testing.T, fixture, goldenFile string) {
	useRepoLenses(t)
	repo := buildRepo(t, readFixture(t, "packet_fixture.json"))
	cases := readOrdered(t, fixture).Get("cases").([]any)
	golden := readOrdered(t, goldenFile).Get("cases").(ledger.Object)
	if len(cases) != len(golden) {
		t.Fatalf("cases %d, golden %d", len(cases), len(golden))
	}
	for _, raw := range cases {
		c := raw.(ledger.Object)
		name := c.Get("name").(string)
		t.Run(name, func(t *testing.T) {
			want := golden.Get(name).(ledger.Object)
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
			cfg := baseCfg()
			if v := c.Get("cfg"); v != nil {
				cfg = plainCfg(t, v)
			}
			risk := 1.0
			if r, ok := c.Get("risk").(float64); ok {
				risk = r
			}
			work := filepath.Join(t.TempDir(), name)
			in := Input{RepoRoot: repo, File: "notes.txt", Cfg: cfg, Risk: &risk, RulesDigest: "rule one\nrule two", FactsExcerpt: "facts here", WorkDir: work,
				OwnedFiles: strs("owned"), ChangedFiles: strs("changed"), Peers: strs("peers"), Secrets: strs("secrets")}
			if err := Check(in); err != nil {
				t.Fatalf("Check refused a case Node reviews: %v", err)
			}
			histCalls := []any{}
			readCalls := 0.0
			fbWant, _ := want.Get("fallback").(ledger.Object)
			fbCalled := false
			seams := func(d *Deps) {
				if failEvent, ok := c.Get("failEvent").(string); ok && d.WriteRow != nil {
					inner := d.WriteRow
					d.WriteRow = func(r ledger.Object) error {
						if r.Get("event") == failEvent {
							return errors.New("disk")
						}
						return inner(r)
					}
				}
				if h := c.Get("history"); h != nil {
					d.SchemaHistory = func(hash, level, role string) (int, bool, error) {
						histCalls = append(histCalls, []any{hash, level, role})
						if h == "throw" {
							return 0, false, errors.New("read")
						}
						o := h.(ledger.Object)
						return int(o.Get("failures").(float64)), o.Get("fellBack") == true, nil
					}
				}
				if l := c.Get("ledger"); l != nil {
					d.ReadRows = func() ([]any, error) {
						readCalls++
						if l == "throw" {
							return nil, errors.New("read")
						}
						var rows []any
						if err := json.Unmarshal([]byte(mustJSON(t, l)), &rows); err != nil {
							t.Fatal(err)
						}
						return rows, nil
					}
				}
				d.TimeoutFallback = func(ti TimeoutFallbackInput) (*Fallback, error) {
					if fbWant == nil {
						if strings.HasPrefix(name, "tf-") {
							t.Errorf("timeoutFallbackFor called; Node did not reach it")
						}
						return nil, nil // Node's call with no ledger rows: no candidate
					}
					fbCalled = true
					wi := fbWant.Get("input").(ledger.Object)
					from := wi.Get("from").(ledger.Object)
					if ti.FromProvider != from.Get("provider") || ti.FromModel != from.Get("model") || ti.TicketID != wi.Get("ticketId") || ti.Level != "L2" || ti.Role != "reviewer" {
						t.Errorf("timeoutFallbackFor input %+v, Node %s", ti, mustJSON(t, wi))
					}
					if got, w := sortedJSON(t, ti.Rows), sortedJSON(t, wi.Get("rows")); got != w {
						t.Errorf("rows %s, Node %s", got, w)
					}
					out, ok := fbWant.Get("output").(ledger.Object)
					if !ok {
						return nil, nil
					}
					return &Fallback{Cfg: plainCfg(t, out.Get("cfg")), Provider: out.Get("provider").(string), Model: out.Get("model").(string), Reason: out.Get("reason").(string)}, nil
				}
			}
			run := runEngine(t, in, c.Get("script").([]any), c.Get("writer") != false, seams)
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
				w := wantSpawns[k].(ledger.Object).Clone()
				cfgL2 := w.Get("cfgL2")
				w.Delete("cfgL2")
				if got, ws := mustJSON(t, s), mustJSON(t, w); got != ws {
					t.Errorf("spawn %d\ngot  %s\nNode %s", k, got, ws)
				}
				if got, ws := sortedJSON(t, run.cfgL2[k]), sortedJSON(t, cfgL2); got != ws {
					t.Errorf("spawn %d levels.L2 %s, Node %s", k, got, ws)
				}
			}
			if got, ws := mustJSON(t, toAny(run.warnings)), mustJSON(t, want.Get("warnings")); got != ws {
				t.Errorf("warnings %s, Node %s", got, ws)
			}
			if got, ws := mustJSON(t, histCalls), mustJSON(t, want.Get("histCalls")); got != ws {
				t.Errorf("schemaHistory calls %s, Node %s", got, ws)
			}
			if readCalls != want.Get("readRowsCalls").(float64) {
				t.Errorf("readRows calls %v, Node %v", readCalls, want.Get("readRowsCalls"))
			}
			if fbWant != nil && !fbCalled {
				t.Errorf("Node called timeoutFallbackFor, Go did not")
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

// sortedJSON is v's JSON with object keys sorted (a Go map has no order).
func sortedJSON(t *testing.T, v any) string {
	t.Helper()
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

func toAny(list []string) []any {
	out := []any{}
	for _, s := range list {
		out = append(out, s)
	}
	return out
}

// TestB514DecodeUTF8MatchesNode: attached file text decodes invalid UTF-8 as
// Buffer.toString('utf8') does (one U+FFFD per maximal invalid subpart).
func TestB514DecodeUTF8MatchesNode(t *testing.T) {
	for _, raw := range readOrdered(t, "b514_engine_golden.json").Get("utf8").([]any) {
		pair := raw.([]any)
		b, err := hex.DecodeString(pair[0].(string))
		if err != nil {
			t.Fatal(err)
		}
		if got := decodeUTF8(b); got != pair[1].(string) {
			t.Errorf("%s: got %q, Node %q", pair[0], got, pair[1])
		}
	}
}
