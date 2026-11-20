package worker

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

const b1027Cfg = `{"provider":"anthropic","levels":{"L2":{"provider":"openai","model":"codex","fallback":[{"provider":"anthropic","model":"claude"}]}},"review":{"adaptive":{"explore":0},"routes":[{"min_lines":0,"provider":"anthropic","model":"claude"}]}}`

func TestB1027AdaptiveClosedBookCandidates(t *testing.T) {
	for _, scenario := range []string{"cold", "previous", "performance", "explore", "pin", "exhausted"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := b492Cfg(t, b1027Cfg)
			rev := cfg["review"].(map[string]any)
			var rows []any
			switch scenario {
			case "cold":
				delete(rev, "routes")
			case "previous":
				rows = []any{map[string]any{"event": "review.route", "run": "r1", "ticket": "t1", "level": "L2", "provider": "openai", "model": "codex", "reason": "explore"}}
			case "performance":
				for i := 0; i < 10; i++ {
					for _, model := range []string{"codex", "claude"} {
						duration := 1000.0
						if model == "claude" {
							duration = 100000
						}
						rows = append(rows, map[string]any{"event": "session", "role": "reviewer", "model": model, "status": "ok", "duration_ms": duration, "ts": "2026-11-11T09:00:00.000Z"})
					}
				}
			case "explore":
				rev["adaptive"].(map[string]any)["explore"] = 1.0
			case "pin":
				rev["routes"] = []any{map[string]any{"min_lines": 0.0, "provider": "openai", "model": "codex", "pin": true}}
			case "exhausted":
				delete(rev, "routes")
				cfg["levels"].(map[string]any)["L2"].(map[string]any)["fallback"] = []any{}
			}
			for i := 0; i < 32; i++ {
				ticket := "t1"
				if scenario == "explore" {
					ticket = fmt.Sprintf("t%d", i)
				}
				got, err := adaptiveRoutes(cfg, 20, false, 300, rows, ticket, "r1", fixedNow)
				if scenario == "exhausted" {
					if !errors.Is(err, errRouteInvalid) {
						t.Fatalf("no compatible reviewer: route=%v error=%v", got.rows, err)
					}
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				l2 := got.cfg["levels"].(map[string]any)["L2"].(map[string]any)
				if l2["provider"] != "anthropic" || l2["model"] != "claude" {
					t.Fatalf("selected unsupported reviewer: %v", l2)
				}
				if got.rows[0].Get("provider") != "anthropic" {
					t.Fatalf("route row disagrees: %v", got.rows)
				}
				for _, raw := range l2["fallback"].([]any) {
					if closedBookRefused(raw.(map[string]any)["provider"], "reviewer", false) {
						t.Fatalf("unsupported fallback: %v", raw)
					}
				}
			}
		})
	}
	// The existing explicit opt-in remains authoritative.
	cfg := b492Cfg(t, b1027Cfg)
	rev := cfg["review"].(map[string]any)
	delete(rev, "routes")
	rev["allow_open_book_codex"] = true
	got, err := adaptiveRoutes(cfg, 20, false, 300, nil, "t1", "r1", fixedNow)
	if err != nil || got.rows[0].Get("provider") != "openai" {
		t.Fatalf("open-book opt-in lost: %v, %v", got.rows, err)
	}
}

func TestB1027AdaptiveClosedBookWorker(t *testing.T) {
	for _, previous := range []bool{false, true} {
		t.Run(fmt.Sprint(previous), func(t *testing.T) {
			t.Setenv("CODE_FORGE_RUN_ID", "")
			e := b629Setup(t, b1027Cfg)
			const content = "hello\nworld\nmore\n"
			if err := os.WriteFile(filepath.Join(e.fx.root, "a.txt"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			id := e.fx.ticket(t, "r1", "B1", "a.txt", sha(content))
			if previous {
				row, err := e.w.signObject(ledger.Object{{Key: "event", Value: "review.route"}, {Key: "run", Value: "r1"}, {Key: "ticket", Value: id}, {Key: "level", Value: "L2"}, {Key: "provider", Value: "openai"}, {Key: "model", Value: "codex"}, {Key: "reason", Value: "explore"}})
				if err != nil {
					t.Fatal(err)
				}
				if err := e.w.writeRow(row); err != nil {
					t.Fatal(err)
				}
			} else {
				// No matching route: the configured Codex L2 must also fall back immediately.
				delete(e.w.Cfg["review"].(map[string]any), "routes")
			}
			if n, poison, err := e.w.RunTicket(id); n != 1 || poison || err != nil {
				t.Fatalf("ticket: %d %v %v", n, poison, err)
			}
			res := readFile(t, filepath.Join(e.fx.root, ".code-forge", "reviews", "r1", id+".json"))
			if strings.Join(e.calls, ",") != "claude" || !strings.Contains(res, `"approved":true`) {
				t.Fatalf("calls %v result %s", e.calls, res)
			}
			routes := e.rows(t, "review.route")
			last := routes[len(routes)-1]
			if last["provider"] != "anthropic" || last["model"] != "claude" {
				t.Fatalf("no signed replacement route: %v", routes)
			}
			// Rewriting route rows for the same compatible choice stays idempotent.
			row := ledger.Object{{Key: "event", Value: "review.route"}, {Key: "level", Value: "L2"}, {Key: "provider", Value: "anthropic"}, {Key: "model", Value: "claude"}}
			if ok, err := e.w.writeRouteRows(id, ledger.Object{}, []ledger.Object{row}); !ok || err != nil {
				t.Fatalf("route retry: %v %v", ok, err)
			}
			if again := e.rows(t, "review.route"); len(again) != len(routes) {
				t.Fatalf("duplicate compatible route: %v", again)
			}
		})
	}
}
