package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// B482 step 0: `code-forge spawn --run <id>` runs in Go, through the
// run-pinned budget.usd and autopilot gate. The expected lines are what Node
// printed for the same fixture (CODE_FORGE_NATIVE=0 ./bin/code-forge spawn
// --level L1 --role reviewer --brief brief.md --run r1 --block B1, fake claude
// on PATH, temp HOME), duration_ms, ts and mac aside.

const b482Config = "version: 1\nproject:\n  slug: unit\nlevels:\n  L1:\n    provider: anthropic\n    model: claude-x\n    effort: high\n"

const b482Grant = `,"autopilot":{"grant_id":"g1","status":"active","until":"2099-01-01T00:00:00.000Z","started_at":"2020-01-01T00:00:00.000Z","caps":{"review":0.05},"stop_at":0.5}`

const b482OK = `{"status":"ok","answer":{"passed":true,"summary":"fine","reviewed_hunks":["h1"],"findings":[]},"usage":{"tokens_in":105,"tokens_out":20,"tokens_source":"reported"},"duration_ms":N,"exit_code":0,"stderr":"","provider":"anthropic","model":"claude-x","effort":"high","fallback_step":0,"row":{"event":"session","role":"reviewer","level":"L1","provider":"anthropic","model":"claude-x","effort":"high","fallback_step":0,"run":"r1","block":"B1","status":"ok","tokens_in":105,"tokens_out":20,"tokens_source":"reported","usd":0.0004,"cost_source":"estimated","duration_ms":N},"attempts":[{"fallback_step":0,"provider":"anthropic","model":"claude-x","status":"ok","reason":null}]}` + "\n"

const b482SessionRow = `{"event":"session","role":"reviewer","level":"L1","provider":"anthropic","model":"claude-x","effort":"high","fallback_step":0,"run":"r1","block":"B1","status":"ok","tokens_in":105,"tokens_out":20,"tokens_source":"reported","usd":0.0004,"cost_source":"estimated","duration_ms":N}`

const b482Started = "level=L1 provider=anthropic model=claude-x effort=high fallback_step=0\n"

var b482Mac = regexp.MustCompile(`,"mac":"[^"]*"`)

func b482Fixture(t *testing.T, budget, ledgerRows, record string) runFixture {
	t.Helper()
	f, repo := newB477Fixture(t)
	t.Setenv("FAKE_MODE", "ok")
	config := b482Config
	if budget != "" {
		config += "budget:\n  usd: " + budget + "\n"
	}
	b460Write(t, filepath.Join(repo, ".code-forge.yml"), config)
	b460Write(t, filepath.Join(repo, "brief.md"), "review this please\n")
	if ledgerRows != "" {
		dir := filepath.Join(f.home, ".code-forge", "ledger")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		b460Write(t, filepath.Join(dir, "unit.jsonl"), ledgerRows)
	}
	switch record {
	case "":
	case "unreadable":
		b460Write(t, filepath.Join(f.home, ".code-forge", "runs", "r1.json"), "{not json")
	default:
		f.record(t, "r1", "active", record)
	}
	return f
}

func b482Spawn(t *testing.T) (int, string, string) {
	t.Helper()
	return b460Call(t, "spawn", "--level", "L1", "--role", "reviewer", "--brief", "brief.md", "--run", "r1", "--block", "B1")
}

func b482Rows(t *testing.T, home string) []string {
	t.Helper()
	rows := b477Ledger(t, home)
	for i := range rows {
		rows[i] = b482Mac.ReplaceAllString(rows[i], "")
	}
	return rows
}

func TestB482SpawnRunMatchesNode(t *testing.T) {
	refusedTail := `,"provider":"anthropic","model":"claude-x","effort":"high","fallback_step":0,"attempts":[]}` + "\n"
	cases := []struct {
		name, budget, rows, record string
		code                       int
		out, err                   string
		ledger                     []string
	}{
		{"no gate: a session pinned to the run", "", "", "", 0, b482OK, b482Started, []string{b482SessionRow}},
		{"budget reached", "1", `{"event":"session","run":"r1","role":"reviewer","usd":1.2,"cost_source":"estimated"}` + "\n", "", 3,
			`{"status":"unavailable","reason":"budget","message":"budget.usd 1.00 reached (spent 1.20); raise budget.usd or end the run","answer":null,"spent_usd":1.2,"budget_usd":1` + refusedTail,
			"code-forge: budget.usd 1.00 reached (spent 1.20); raise budget.usd or end the run\n",
			[]string{`{"event":"session","run":"r1","role":"reviewer","usd":1.2,"cost_source":"estimated"}`,
				`{"event":"budget.refused","run":"r1","block":"B1","role":"reviewer","budget_usd":1,"spent_usd":1.2,"unknown_usd_sessions":0,"reserved_usd":0,"estimate_usd":0.018,"tokens_source":null,"cost_source":null}`}},
		{"estimate above the budget", "0.01", "", "", 3,
			`{"status":"unavailable","reason":"budget","message":"this session's estimated cost (about 0.02) is above budget.usd 0.01; raise budget.usd","answer":null,"spent_usd":0,"budget_usd":0.01` + refusedTail,
			"code-forge: this session's estimated cost (about 0.02) is above budget.usd 0.01; raise budget.usd\n",
			[]string{`{"event":"budget.refused","run":"r1","block":"B1","role":"reviewer","budget_usd":0.01,"spent_usd":0,"unknown_usd_sessions":0,"reserved_usd":0,"estimate_usd":0.018,"tokens_source":null,"cost_source":null}`}},
		{"would exceed", "1", `{"event":"session","run":"r1","role":"reviewer","usd":0.99,"cost_source":"estimated"}` + "\n", "", 3,
			`{"status":"unavailable","reason":"budget","message":"budget.usd 1.00 would be exceeded: spent 0.99, this session about 0.02; raise budget.usd or end the run","answer":null,"spent_usd":0.99,"budget_usd":1` + refusedTail,
			"code-forge: budget.usd 1.00 would be exceeded: spent 0.99, this session about 0.02; raise budget.usd or end the run\n",
			[]string{`{"event":"session","run":"r1","role":"reviewer","usd":0.99,"cost_source":"estimated"}`,
				`{"event":"budget.refused","run":"r1","block":"B1","role":"reviewer","budget_usd":1,"spent_usd":0.99,"unknown_usd_sessions":0,"reserved_usd":0,"estimate_usd":0.018,"tokens_source":null,"cost_source":null}`}},
		{"autopilot record unreadable", "1", "", "unreadable", 3,
			`{"status":"unavailable","reason":"autopilot-unknown","message":"autopilot: the run record cannot be read, so the autopilot budget is unknown; not starting a session","answer":null` + refusedTail,
			"code-forge: autopilot: the run record cannot be read, so the autopilot budget is unknown; not starting a session\n", []string{}},
		{"autopilot under its cap", "", "", b482Grant, 0, b482OK, b482Started, []string{b482SessionRow}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := b482Fixture(t, c.budget, c.rows, c.record)
			code, out, errText := b482Spawn(t)
			if code != c.code || b477Norm(out) != c.out || errText != c.err {
				t.Fatalf("spawn = %d\n%s\n%q", code, out, errText)
			}
			if got := b482Rows(t, f.home); strings.Join(got, "\n") != strings.Join(c.ledger, "\n") {
				t.Fatalf("ledger rows =\n%s", strings.Join(got, "\n"))
			}
			if got := b460Handovers(t, f.home); len(got) != 0 {
				t.Fatalf("handed over: %v", got)
			}
			// the run's own root stays; the lazy proc root holding the pid entry goes
			if _, err := os.Stat(filepath.Join(f.workspace, "tmp", "code-forge", "r1", "owner.json")); err != nil {
				t.Fatalf("run root: %v", err)
			}
			if left, _ := filepath.Glob(filepath.Join(f.workspace, "tmp", "code-forge", "proc-*")); len(left) != 0 {
				t.Fatalf("proc root left behind: %v", left)
			}
		})
	}
}

// At 80 % of budget.usd: the budget.warning row, the stderr line and the error
// log's warning line (Node's logWarning), then the session runs.
func TestB482SpawnRunBudgetWarning(t *testing.T) {
	f := b482Fixture(t, "1", `{"event":"session","run":"r1","role":"reviewer","usd":0.85,"cost_source":"estimated"}`+"\n", "")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "")
	spawnWarnMu.Lock()
	spawnWarned = map[string]bool{}
	spawnWarnMu.Unlock()
	code, out, errText := b482Spawn(t)
	if code != 0 || b477Norm(out) != b482OK || errText != "code-forge: budget.usd: spent 0.85 of 1.00 (≥ 80%); new sessions stop at 100%\n"+b482Started {
		t.Fatalf("spawn = %d\n%s\n%q", code, out, errText)
	}
	want := []string{`{"event":"session","run":"r1","role":"reviewer","usd":0.85,"cost_source":"estimated"}`,
		`{"event":"budget.warning","run":"r1","block":"B1","role":"reviewer","budget_usd":1,"spent_usd":0.85,"unknown_usd_sessions":0,"tokens_source":null,"cost_source":null}`,
		b482SessionRow}
	if got := b482Rows(t, f.home); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("ledger rows =\n%s", strings.Join(got, "\n"))
	}
	data, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "logs", "errors.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	line := regexp.MustCompile(`"ts":"[^"]*","version":"[^"]*","node":"[^"]*","platform":"[^"]*","arch":"[^"]*",`).ReplaceAllString(strings.TrimSpace(string(data)), "")
	if line != `{"verb":"spawn","sub":null,"flags":[],"exit":0,"kind":"warning","warning":"budget_warning","message":"budget.usd: 80% of the run budget is spent; new sessions stop at 100%","stack":null,"cleaned":[],"fp":"731f737b8512","before":[]}` {
		t.Fatalf("error log = %s", data)
	}
	if got := b460Handovers(t, f.home); len(got) != 0 {
		t.Fatalf("handed over: %v", got)
	}
}

// Over the autopilot stop: the pause saved in the run record, one signed
// autopilot.pause row, the autopilot-paused refusal.
func TestB482SpawnRunAutopilotPause(t *testing.T) {
	f := b482Fixture(t, "", `{"event":"session","run":"r1","role":"reviewer","usd":0.03,"cost_source":"estimated","ts":"2026-01-01T00:00:00.000Z"}`+"\n", b482Grant)
	code, out, errText := b482Spawn(t)
	if code != 3 || out != `{"status":"unavailable","reason":"autopilot-paused","message":"autopilot paused: review spend 0.03 of 0.05 reached the stop at 0.5; waiting for the owner","answer":null,"category":"review","cap_usd":0.05,"spent_usd":0.03,"reserved_usd":0,"provider":"anthropic","model":"claude-x","effort":"high","fallback_step":0,"attempts":[]}`+"\n" ||
		errText != "code-forge: autopilot paused: review spend 0.03 of 0.05 reached the stop at 0.5; waiting for the owner\n" {
		t.Fatalf("spawn = %d\n%s\n%q", code, out, errText)
	}
	rows := b482Rows(t, f.home)
	if len(rows) != 2 || rows[1] != `{"run":"r1","event":"autopilot.pause","grant_id":"g1","category":"review","spent_usd":0.03,"reserved_usd":0,"cap_usd":0.05,"stop_at":0.5,"role":"reviewer","block":"B1","tokens_source":null,"cost_source":null}` {
		t.Fatalf("ledger rows =\n%s", strings.Join(rows, "\n"))
	}
	data, _ := os.ReadFile(filepath.Join(f.home, ".code-forge", "ledger", "unit.jsonl"))
	if !regexp.MustCompile(`"event":"autopilot.pause".*"mac":"[0-9a-f]{64}"`).Match(data) {
		t.Fatalf("pause row is not signed: %s", data)
	}
	record := f.read(t, "r1")
	pause, _ := record["autopilot"].(map[string]any)["paused"].(map[string]any)["review"].(map[string]any)
	if pause == nil || pause["row_pending"] != false || pause["spent_usd"] != 0.03 || pause["block"] != "B1" || pause["role"] != "reviewer" {
		t.Fatalf("record = %v", record)
	}
	// a second spawn stays paused and writes no second row
	code, _, _ = b482Spawn(t)
	if got := b482Rows(t, f.home); code != 3 || len(got) != 2 {
		t.Fatalf("second spawn = %d, rows %v", code, got)
	}
}
