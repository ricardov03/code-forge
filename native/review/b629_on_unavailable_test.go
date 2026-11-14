package review

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
)

// b629Cfg is baseCfg with review.on_unavailable set to routes (and the
// backoff list when backoff is not nil).
func b629Cfg(routes []any, backoff []any) map[string]any {
	c := baseCfg()
	r := map[string]any{"on_unavailable": routes}
	if backoff != nil {
		r["on_unavailable_backoff_s"] = backoff
	}
	c["review"] = r
	return c
}

func b629Session(status, model string) ledger.Object {
	if status == "ok" {
		s := okSession()
		s.Set("model", model)
		return s
	}
	out := ledger.Object{{Key: "status", Value: status}, {Key: "provider", Value: "claude"}, {Key: "model", Value: model}}
	if status == "unavailable" {
		out.Set("reason", "provider-down")
	}
	if status == "timeout" {
		out.Set("duration_ms", 600000.0)
	}
	return out
}

type b629Run struct {
	engineRun
	sleeps []time.Duration
	steps  []map[string]any
}

func b629Engine(t *testing.T, cfg map[string]any, script []any, more ...func(*Deps)) b629Run {
	t.Helper()
	useRepoLenses(t)
	repo := buildRepo(t, readFixture(t, "packet_fixture.json"))
	risk := 1.0
	in := Input{RepoRoot: repo, File: "notes.txt", Cfg: cfg, Risk: &risk, WorkDir: filepath.Join(t.TempDir(), "w"), Ticket: "T-629"}
	var out b629Run
	seams := append([]func(*Deps){func(d *Deps) {
		d.TimeoutFallback = func(TimeoutFallbackInput) (*Fallback, error) { return nil, nil }
		d.Sleep = func(d time.Duration) { out.sleeps = append(out.sleeps, d) }
	}}, more...)
	out.engineRun = runEngine(t, in, script, true, seams...)
	if out.err != nil {
		t.Fatalf("File: %v", out.err)
	}
	for _, r := range out.rows {
		var m map[string]any
		if err := json.Unmarshal([]byte(r), &m); err != nil {
			t.Fatal(err)
		}
		if m["event"] == "review.unavailable_fallback" {
			out.steps = append(out.steps, m)
		}
	}
	return out
}

func b629Status(t *testing.T, res ledger.Object) string {
	t.Helper()
	s, _ := res.Get("status").(string)
	return s
}

func b629Want(t *testing.T, step map[string]any, want map[string]any) {
	t.Helper()
	for k, v := range want {
		got, _ := json.Marshal(step[k])
		exp, _ := json.Marshal(v)
		if string(got) != string(exp) {
			t.Errorf("row %v: %s = %s, want %s", step["attempt"], k, got, exp)
		}
	}
}

// The chain runs only after the built-in recovery (here the timeout retry)
// fails, one signed-row step per route with the configured backoff, and the
// first route that answers ends the walk with an approved review.
func TestB629ChainAfterBuiltInRecovery(t *testing.T) {
	cfg := b629Cfg([]any{map[string]any{"model": "m3"}, map[string]any{"provider": "claude", "model": "m4", "effort": "high"}}, nil)
	run := b629Engine(t, cfg, []any{b629Session("timeout", "m2"), b629Session("timeout", "m2"), b629Session("timeout", "m3"), b629Session("ok", "m4")})
	if len(run.spawns) != 4 {
		t.Fatalf("spawns %d, want 4 (2 built-in, 2 chain)", len(run.spawns))
	}
	if run.result.Get("approved") != true {
		t.Errorf("want the review approved; result %s", mustJSON(t, run.result))
	}
	if len(run.steps) != 2 {
		t.Fatalf("unavailable_fallback rows %d, want 2: %v", len(run.steps), run.rows)
	}
	b629Want(t, run.steps[0], map[string]any{"ticket": "T-629", "file": "notes.txt", "level": "L2", "role": "reviewer",
		"from_provider": "claude", "from_model": "m2", "to_provider": "claude", "to_model": "m3", "reason": "timeout", "attempt": 1, "waited_ms": 0, "skipped": nil})
	b629Want(t, run.steps[1], map[string]any{"from_model": "m3", "to_model": "m4", "reason": "timeout", "attempt": 2, "waited_ms": 30000})
	if len(run.sleeps) != 1 || run.sleeps[0] != 30*time.Second {
		t.Errorf("sleeps %v, want [30s] (the first step waits 0)", run.sleeps)
	}
	// each chain spawn runs the route pinned, with no ladder of its own
	l2, _ := json.Marshal(run.cfgL2[3])
	if string(l2) != `{"effort":"high","fallback":[],"model":"m4","provider":"claude"}` {
		t.Errorf("chain spawn levels.L2 %s", l2)
	}
	if !strings.Contains(mustJSON(t, run.result), `"schema_fallback":{"level":"review.on_unavailable","provider":"claude","model":"m4"}`) {
		t.Errorf("result does not name the chain route: %s", mustJSON(t, run.result))
	}
}

// Every route fails: the session stays unavailable as before, with one row
// per step.
func TestB629ChainExhaustedIsUnavailable(t *testing.T) {
	cfg := b629Cfg([]any{map[string]any{"model": "m3"}, map[string]any{"model": "m4"}}, []any{5.0})
	run := b629Engine(t, cfg, []any{b629Session("timeout", "m2"), b629Session("timeout", "m2"), b629Session("unavailable", "m3"), b629Session("timeout", "m4")})
	if got := b629Status(t, run.result); got != "unavailable" {
		t.Fatalf("status %q, want unavailable: %s", got, mustJSON(t, run.result))
	}
	if len(run.steps) != 2 {
		t.Fatalf("rows %d, want 2", len(run.steps))
	}
	b629Want(t, run.steps[1], map[string]any{"from_model": "m3", "to_model": "m4", "reason": "exit", "attempt": 2, "waited_ms": 5000})
	if len(run.sleeps) != 2 || run.sleeps[1] != 5*time.Second {
		t.Errorf("sleeps %v, want the last backoff entry repeated", run.sleeps)
	}
}

// A route that answers ends the walk: later routes never run.
func TestB629ChainSuccessMidChainStops(t *testing.T) {
	cfg := b629Cfg([]any{map[string]any{"model": "m3"}, map[string]any{"model": "m4"}, map[string]any{"model": "m5"}}, []any{})
	run := b629Engine(t, cfg, []any{b629Session("timeout", "m2"), b629Session("timeout", "m2"), b629Session("ok", "m3")})
	if len(run.spawns) != 3 || len(run.steps) != 1 {
		t.Fatalf("spawns %d rows %d, want 3 and 1", len(run.spawns), len(run.steps))
	}
	if len(run.sleeps) != 0 {
		t.Errorf("an empty backoff list waited %v", run.sleeps)
	}
	if run.result.Get("approved") != true {
		t.Errorf("not approved after a route answered: %s", mustJSON(t, run.result))
	}
}

// A spawn that throws (exit) also walks the chain; a closed-book route and a
// route the session layer cannot run are skipped with a row and no wait.
func TestB629ChainAfterExitSkipsUnrunnable(t *testing.T) {
	cfg := b629Cfg([]any{map[string]any{"provider": "openai", "model": "gpt-5"}, map[string]any{"model": "nope"}, map[string]any{"model": "m3"}}, nil)
	run := b629Engine(t, cfg, []any{"throw", b629Session("ok", "m3")}, func(d *Deps) {
		d.CheckSpawn = func(c map[string]any, level, role string) error {
			l, _ := section(c, "levels")[level].(map[string]any)
			if l["model"] == "nope" {
				return errors.New("unknown model")
			}
			return nil
		}
	})
	if len(run.spawns) != 2 || len(run.steps) != 3 {
		t.Fatalf("spawns %d rows %d, want 2 and 3: %v", len(run.spawns), len(run.steps), run.rows)
	}
	b629Want(t, run.steps[0], map[string]any{"from_model": "m2", "to_provider": "openai", "reason": "exit", "skipped": "closed-book", "waited_ms": 0})
	b629Want(t, run.steps[1], map[string]any{"to_model": "nope", "skipped": "not-runnable", "waited_ms": 0})
	b629Want(t, run.steps[2], map[string]any{"from_model": "m2", "to_model": "m3", "attempt": 3, "skipped": nil, "waited_ms": 0})
	if len(run.sleeps) != 0 {
		t.Errorf("sleeps %v, want none (skipped steps never wait, the first run step waits 0)", run.sleeps)
	}
}

// Routes of the other level are not walked; no chain leaves today's result.
func TestB629ChainOtherLevelUnused(t *testing.T) {
	cfg := b629Cfg([]any{map[string]any{"model": "m3", "level": "L3"}}, nil)
	run := b629Engine(t, cfg, []any{b629Session("timeout", "m2"), b629Session("timeout", "m2")})
	if len(run.steps) != 0 || b629Status(t, run.result) != "unavailable" {
		t.Fatalf("rows %d status %q, want 0 and unavailable", len(run.steps), b629Status(t, run.result))
	}
}
