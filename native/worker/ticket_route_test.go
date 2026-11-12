package worker

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/sign"
)

// B492: testdata/b492_node_routes.golden is what Node's createWorker
// (src/worker/loop.mjs processTicket with the real engine, default ledger and
// tail reader) wrote for the same fixture: a route list that cannot be used,
// an explicit route then review.exclude, adaptive L2 routing in configured
// order, by measured performance and from the ticket's earlier review.route
// row, each ending in the engine's own refusal. Normalized like B488.

func b492Cfg(t *testing.T, text string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

const b492L2 = `{"levels":{"L2":{"model":"m-a","provider":"pa","fallback":[{"model":"m-b","provider":"pb"}]}}}`

func (fx *b488Fix) appendLedger(t *testing.T, text string) {
	t.Helper()
	dir := filepath.Join(fx.home, ".code-forge", "ledger")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "unit.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func (fx *b488Fix) blocksRecord(t *testing.T, blocks string) {
	t.Helper()
	fx.record(t, `{"run_id":"r1","project":"unit","workspace":"`+fx.root+`","status":"active"`+blocks+`}`)
}

func TestB492RoutesMatchNodeBytes(t *testing.T) {
	fx := newB488Fix(t)
	real := sha("hello\n")
	base := strings.Repeat("a", 40)
	clock := fixedNow
	w := fx.runner(&clock)
	type tc struct {
		block, cfg, blocks string
		extra              []ledger.Field
	}
	cases := []tc{
		{"B1", `{"review":{"routes":"bad"}}`, ``, nil},
		{"B2", `{"review":{"exclude":["a.txt"],"adaptive":{"enabled":false},"routes":[{"min_lines":0,"model":"m-x","provider":"px","session_timeout_s":60,"second":{"model":"m-y"}}]}}`, `,"blocks":{"B2":{"base_sha":"` + base + `"}}`, nil},
		{"B3", b492L2, `,"blocks":{"B9":{"base_sha":"` + base + `"}}`, nil},
		{"B4", b492L2, `,"blocks":{"B4":{"base_sha":"` + base + `","acceptance_revision":"rev2"}}`, []ledger.Field{{Key: "acceptance_revision", Value: "rev1"}}},
		{"B5", b492L2, `,"blocks":{"B5":{}}`, nil},
	}
	var ids []string
	for _, c := range cases {
		id := fx.ticket(t, "r1", c.block, "a.txt", real, c.extra...)
		ids = append(ids, id)
		fx.blocksRecord(t, c.blocks)
		switch c.block {
		case "B4": // ten measured sessions each: m-b is faster
			var b strings.Builder
			for i := 0; i < 10; i++ {
				b.WriteString(`{"event":"session","role":"reviewer","model":"m-a","provider":"pa","status":"ok","duration_ms":30000,"run":"r0","block":"B0","lens":"l","ts":"2026-11-10T09:00:00.000Z"}` + "\n")
				b.WriteString(`{"event":"session","role":"reviewer","model":"m-b","provider":"pb","status":"ok","duration_ms":5000,"run":"r0","block":"B0","lens":"l","ts":"2026-11-10T09:00:00.000Z"}` + "\n")
			}
			fx.appendLedger(t, b.String())
		case "B5": // the ticket's earlier route row pins the reviewer
			fx.appendLedger(t, `{"event":"review.route","run":"r1","ticket":"`+id+`","level":"L2","provider":"pb","model":"m-b","reason":"explore","ts":"2026-11-10T09:00:00.000Z"}`+"\n")
		}
		w.Cfg = b492Cfg(t, c.cfg)
		n, poison, err := w.RunTicket(id)
		if err != nil || n != 1 || poison {
			t.Fatalf("%s: RunTicket = %d, %v, %v; want 1, false, nil", c.block, n, poison, err)
		}
	}
	ledgerText := readFile(t, filepath.Join(fx.home, ".code-forge", "ledger", "unit.jsonl"))
	got := b488Normalize(fx.files(t, ids) + ledgerText)
	want := readFile(t, filepath.Join("testdata", "b492_node_routes.golden"))
	if got != want {
		gl, wl := strings.Split(got, "\n"), strings.Split(want, "\n")
		for i := range wl {
			if i >= len(gl) || gl[i] != wl[i] {
				g := "<missing>"
				if i < len(gl) {
					g = gl[i]
				}
				t.Fatalf("line %d differs from Node\n got: %s\nwant: %s", i+1, g, wl[i])
			}
		}
		t.Fatalf("Go wrote %d lines, Node %d", len(gl), len(wl))
	}
	for _, l := range strings.Split(strings.TrimSpace(ledgerText), "\n") {
		obj, err := ledger.DecodeObject([]byte(l))
		if err != nil {
			t.Fatal(err)
		}
		m := obj.Map()
		if _, signed := m["mac"]; !signed {
			continue // the seeded rows
		}
		delete(m, "tokens_source")
		delete(m, "cost_source")
		if res, err := sign.VerifyRow(m, fx.key); err != nil || !res.OK {
			t.Fatalf("ledger row does not verify: %s", l)
		}
	}
}

// A route row the ledger refuses: the ticket is unavailable (route-row), as in
// Node, where the row is written before the engine runs. (A ticket the engine
// would review stays unsupported with nothing written: see below.)
func TestB492RouteRowWriteFails(t *testing.T) {
	fx := newB488Fix(t)
	fx.blocksRecord(t, `,"blocks":{"B9":{"base_sha":"`+strings.Repeat("a", 40)+`"}}`)
	id := fx.ticket(t, "r1", "B1", "a.txt", sha("hello\n"))
	clock := fixedNow
	w := fx.runner(&clock)
	w.Cfg = b492Cfg(t, b492L2)
	var events []string
	w.WriteRow = func(row ledger.Object) error {
		ev := row.Get("event")
		events = append(events, ev.(string))
		if ev == "review.route" {
			return errors.New("disk full")
		}
		return nil
	}
	if n, poison, err := w.RunTicket(id); err != nil || n != 1 || poison {
		t.Fatalf("RunTicket = %d, %v, %v", n, poison, err)
	}
	res := readFile(t, filepath.Join(fx.root, ".code-forge", "reviews", "r1", id+".json"))
	if !strings.Contains(res, `"status":"unavailable"`) || !strings.Contains(res, `"reason":"route-row"`) {
		t.Fatalf("result = %s", res)
	}
	if strings.Join(events, ",") != "review.route,review.result" {
		t.Fatalf("rows = %v", events)
	}
}

// Every ticket Go cannot finish like Node stays queued with nothing written.
func TestB492UnsupportedWritesNothing(t *testing.T) {
	base := strings.Repeat("a", 40)
	for _, c := range []struct{ name, cfg, blocks string }{
		{"the review engine (block ready for sessions)", b492L2, `,"blocks":{"B1":{"base_sha":"` + base + `"}}`},
		{"explicit routes, review engine", `{"review":{"routes":[{"min_lines":0,"model":"m-x","provider":"px"}],"adaptive":{"enabled":false}}}`, `,"blocks":{"B1":{"base_sha":"` + base + `"}}`},
		{"adaptive min_sessions not a number", `{"levels":{"L2":{"model":"m-a","provider":"pa"}},"review":{"adaptive":{"min_sessions":"x"}}}`, `,"blocks":{"B1":{"base_sha":"` + base + `"}}`},
		{"blocks is a list", `{}`, `,"blocks":[]`},
	} {
		t.Run(c.name, func(t *testing.T) {
			fx := newB488Fix(t)
			fx.blocksRecord(t, c.blocks)
			id := fx.ticket(t, "r1", "B1", "a.txt", sha("hello\n"))
			clock := fixedNow
			w := fx.runner(&clock)
			w.Cfg = b492Cfg(t, c.cfg)
			w.WriteRow = func(ledger.Object) error { t.Fatal("a row was written"); return nil }
			_, _, err := w.RunTicket(id)
			if !IsUnsupported(err) {
				t.Fatalf("err = %v, want unsupported", err)
			}
			for _, p := range []string{
				filepath.Join(fx.root, ".code-forge", "reviews", "r1", id+".json"),
				filepath.Join(QueueDir(fx.root), id+".done"),
				filepath.Join(fx.home, ".code-forge", "ledger", "unit.jsonl"),
			} {
				if _, err := os.Stat(p); !os.IsNotExist(err) {
					t.Fatalf("%s was written", p)
				}
			}
		})
	}
}

func TestB492CountLines(t *testing.T) {
	for in, want := range map[string]int{"": 0, "a": 1, "a\n": 1, "a\nb": 2, "\n\n": 2} {
		if got := countLines([]byte(in)); got != want {
			t.Fatalf("countLines(%q) = %d, want %d", in, got, want)
		}
	}
	if countLines(nil) != 0 {
		t.Fatal("deleted file")
	}
}
