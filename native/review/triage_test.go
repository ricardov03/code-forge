package review

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/jev"
	"github.com/ricardov03/code-forge/native/ledger"
)

// testdata/triage_golden.json is Node's real runRound (kind full) and
// resolvedBand with askJev bound to a fake key, against a local fake Jev
// (scratch tool b533tools/triage_golden.mjs: one process per scenario, temp
// HOME, frozen clock). Each scenario holds the replies the fake served, the
// requests it saw, the backoff sleeps, the error-log warnings and the rows.
type triageFake struct {
	mu       sync.Mutex
	replies  []ledger.Object
	requests []any
	srv      *httptest.Server
}

// seen waits for every request the fake still serves (a client that gave up
// on a timeout leaves its handler running), then reads them under the lock
// (B560: the race detector caught the bare read on CI).
func (f *triageFake) seen() []any {
	if f.srv != nil {
		f.srv.Close()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]any(nil), f.requests...)
}

func (f *triageFake) serve(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, ledger.Object{{Key: "auth", Value: r.Header.Get("Authorization")},
			{Key: "type", Value: r.Header.Get("Content-Type")}, {Key: "body", Value: string(body)}})
		rep := f.replies[0]
		if len(f.replies) > 1 {
			f.replies = f.replies[1:]
		}
		f.mu.Unlock()
		if d, ok := rep.Get("delay").(float64); ok {
			select {
			case <-time.After(time.Duration(d) * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(int(rep.Get("status").(float64)))
		_, _ = io.WriteString(w, rep.Get("body").(string))
	}))
	f.srv = srv
	t.Cleanup(srv.Close)
	return srv.URL + "/v1/systemone"
}

// closedURL is a local port nothing listens on (Node's "Jev down").
func closedURL(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return "http://" + addr + "/v1/systemone"
}

func TestB533TriageWithJevMatchesNode(t *testing.T) {
	data, err := os.ReadFile("testdata/triage_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	g, err := ledger.DecodeObject(data)
	if err != nil {
		t.Fatal(err)
	}
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
	scenarios, _ := g.Get("scenarios").([]any)
	if len(scenarios) < 18 {
		t.Fatalf("golden has %d scenarios", len(scenarios))
	}
	for _, s := range scenarios {
		sc := s.(ledger.Object)
		t.Run(sc.Get("name").(string), func(t *testing.T) {
			gold := sc.Get("golden").(ledger.Object)
			replies := []ledger.Object{}
			for _, r := range sc.Get("replies").([]any) {
				replies = append(replies, r.(ledger.Object))
			}
			fake := &triageFake{replies: replies}
			url := fake.serve(t)
			if sc.Get("down") == true {
				url = closedURL(t)
			}
			slept := []any{}
			opts := jev.Opts{URL: url, Sleep: func(ms float64) { slept = append(slept, ms) }}
			if ms, ok := sc.Get("timeoutMs").(float64); ok {
				opts.Timeout = time.Duration(ms) * time.Millisecond
			}
			var ask JevAsk
			if sc.Get("noJev") != true {
				ask = func(state, questions any) (jev.Result, error) {
					o := opts
					o.State, o.Questions, o.Key = state, questions, "fake-jev-key-0001"
					return jev.Ask(o)
				}
			}
			warnings := []any{}
			seen := map[string]bool{}
			rows := []any{}
			deps := LoopDeps{RepoRoot: root, Cfg: plain(t, sc.Get("cfg")), Jev: ask,
				WriteRow: func(row ledger.Object) error { rows = append(rows, row); return nil },
				Session: Deps{Warn: func(w, m string) {
					// logWarning writes one line per message per process
					if !seen[w+"|"+m] {
						seen[w+"|"+m] = true
						warnings = append(warnings, w+"|"+m)
					}
				}},
			}
			if sc.Get("resolved") == true {
				closed := resolvedClosed(sc.Get("finding"), sc.Get("fixHunk").(string), deps)
				if want := gold.Get("band") == "closed"; closed != want {
					t.Fatalf("closed=%v, node band %v", closed, gold.Get("band"))
				}
			} else {
				result := sc.Get("result").(ledger.Object)
				got, err := RunRound(NewFileState("src/app.go", "L2", false), LoopDeps{RepoRoot: deps.RepoRoot, Cfg: deps.Cfg, Jev: deps.Jev,
					WriteRow: deps.WriteRow, Session: deps.Session,
					Review: func() (ledger.Object, error) { return result, nil }}, "full")
				if err != nil {
					t.Fatal(err)
				}
				if a, b := js(t, got), js(t, gold.Get("state")); a != b {
					t.Fatalf("state differs\n go: %s\nnode: %s", a, b)
				}
				if a, b := js(t, rows), js(t, gold.Get("rows")); a != b {
					t.Fatalf("rows differ\n go: %s\nnode: %s", a, b)
				}
			}
			if a, b := js(t, fake.seen()), js(t, gold.Get("requests")); a != b && sc.Get("down") != true {
				t.Fatalf("requests differ\n go: %s\nnode: %s", a, b)
			}
			if a, b := js(t, slept), js(t, gold.Get("slept")); a != b {
				t.Fatalf("sleeps differ: go %s node %s", a, b)
			}
			if a, b := js(t, warnings), js(t, gold.Get("warnings")); a != b {
				t.Fatalf("warnings differ\n go: %s\nnode: %s", a, b)
			}
		})
	}
}

// CheckJev refuses, before a round, the configs Go cannot render like Node;
// a plain or an ordered criteria_extra passes.
func TestB533CheckJev(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg map[string]any
		ok  bool
	}{
		"none":            {nil, true},
		"one extra key":   {map[string]any{"system1": map[string]any{"criteria_extra": map[string]any{"defect": map[string]any{"criteria": map[string]any{"extra": "x", "7": "y"}}}}}, true},
		"two extra keys":  {map[string]any{"system1": map[string]any{"criteria_extra": map[string]any{"defect": map[string]any{"criteria": map[string]any{"a": "x", "b": "y"}}}}}, false},
		"disabled":        {map[string]any{"system1": map[string]any{"disable": []any{"defect", "resolved"}, "criteria_extra": map[string]any{"defect": map[string]any{"criteria": "str"}}}}, true},
		"string criteria": {map[string]any{"system1": map[string]any{"criteria_extra": map[string]any{"resolved": map[string]any{"criteria": "str"}}}}, false},
		"text threshold":  {map[string]any{"thresholds": map[string]any{"findings": map[string]any{"fix": "0.5"}}}, false},
	} {
		t.Run(name, func(t *testing.T) {
			err := CheckJev(tc.cfg)
			if (err == nil) != tc.ok || (err != nil && !IsUnsupported(err)) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
