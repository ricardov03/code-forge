package session

import (
	"errors"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B482 step 0: the run-pinned gate in Go (spawn.mjs budgetGate).

func gateSetup(t *testing.T, budget float64) (Opts, *strings.Builder, *[]ledger.Object) {
	t.Helper()
	ResetReservations()
	t.Cleanup(ResetReservations)
	home := t.TempDir()
	t.Setenv("HOME", home)
	o, _ := testSetup(t)
	run, block := "r1", "B1"
	o.Run, o.Block = &run, &block
	o.Cfg["budget"] = map[string]any{"usd": budget}
	return o, &strings.Builder{}, &[]ledger.Object{}
}

func gateDeps(stderr *strings.Builder, rows []ledger.Row) Deps {
	return Deps{Stderr: stderr, Home: "", NowMs: func() float64 { return 1.8e12 }, ReadRows: func() ([]ledger.Row, error) { return rows, nil }}
}

// A second session while the first one's estimate is reserved: the
// "reserved by running sessions" refusal; the reservation goes on release.
func TestBudgetGateCountsRunningSessions(t *testing.T) {
	o, stderr, written := gateSetup(t, 0.03)
	deps := gateDeps(stderr, []ledger.Row{})
	write := func(row ledger.Object) error { *written = append(*written, row); return nil }
	s := step{provider: "anthropic", model: "claude-x", effort: "high"}
	refused, release, err := budgetGate(o, deps, write, s)
	if err != nil || refused != nil {
		t.Fatalf("first gate = %v, %v", refused, err)
	}
	refused, _, err = budgetGate(o, deps, write, s)
	if err != nil || refused == nil {
		t.Fatalf("second gate = %v, %v", refused, err)
	}
	want := "budget.usd 0.03 would be exceeded: spent 0.00, reserved by running sessions 0.02, this session about 0.02; it starts when running sessions finish or when budget.usd is raised"
	if refused.Get("message") != want || stderr.String() != "code-forge: "+want+"\n" {
		t.Fatalf("refusal = %v, stderr %q", refused, stderr.String())
	}
	if len(*written) != 1 || (*written)[0].Get("reserved_usd") != 0.018 || (*written)[0].Get("estimate_usd") != 0.018 {
		t.Fatalf("rows = %v", *written)
	}
	release()
	release() // idempotent
	if refused, rel, err := budgetGate(o, deps, write, s); err != nil || refused != nil {
		t.Fatalf("after release = %v, %v", refused, err)
	} else {
		rel()
	}
}

// No budget.usd and no autopilot grant: no gate, nothing read or written.
func TestBudgetGateNoBudgetNoRecordIsOpen(t *testing.T) {
	o, stderr, _ := gateSetup(t, 0)
	delete(o.Cfg, "budget")
	deps := gateDeps(stderr, nil)
	deps.ReadRows = func() ([]ledger.Row, error) { t.Fatal("rows read"); return nil, nil }
	refused, release, err := budgetGate(o, deps, nil, step{provider: "anthropic", model: "claude-x"})
	if err != nil || refused != nil || release == nil {
		t.Fatalf("gate = %v, %v", refused, err)
	}
}

// Unreadable spend: the spend-unreadable refusal and row.
func TestBudgetGateSpendUnreadable(t *testing.T) {
	o, stderr, written := gateSetup(t, 1)
	deps := gateDeps(stderr, nil)
	deps.ReadRows = func() ([]ledger.Row, error) { return nil, errors.New("EACCES") }
	write := func(row ledger.Object) error { *written = append(*written, row); return nil }
	refused, _, err := budgetGate(o, deps, write, step{provider: "anthropic", model: "claude-x"})
	if err != nil || refused == nil || refused.Get("message") != SpendUnreadableMessage || refused.Get("spent_usd") != nil {
		t.Fatalf("gate = %v, %v", refused, err)
	}
	row, _ := ledger.Stringify((*written)[0], false)
	if string(row) != `{"event":"budget.refused","run":"r1","block":"B1","role":"reviewer","budget_usd":1,"spent_usd":null,"reason":"spend-unreadable"}` {
		t.Fatalf("row = %s", row)
	}
}

// The 80 % warning needs the error log: with no Warn seam it is a case only
// Node serves, found before anything is written.
func TestBudgetGateWarningWithoutWarnSeamIsUnsupported(t *testing.T) {
	o, stderr, written := gateSetup(t, 1)
	deps := gateDeps(stderr, []ledger.Row{map[string]any{"event": "session", "run": "r1", "usd": 0.85}})
	write := func(row ledger.Object) error { *written = append(*written, row); return nil }
	_, _, err := budgetGate(o, deps, write, step{provider: "anthropic", model: "claude-x"})
	if !IsUnsupported(err) || len(*written) != 0 || stderr.Len() != 0 {
		t.Fatalf("err = %v, rows %v, stderr %q", err, *written, stderr.String())
	}
	var warned []string
	deps.Warn = func(kind, message string) { warned = append(warned, kind+": "+message) }
	if refused, release, err := budgetGate(o, deps, write, step{provider: "anthropic", model: "claude-x"}); err != nil || refused != nil {
		t.Fatalf("gate = %v, %v", refused, err)
	} else {
		release()
	}
	if len(warned) != 1 || warned[0] != BudgetWarningKind+": "+BudgetWarningMessage || len(*written) != 1 || (*written)[0].Get("event") != "budget.warning" {
		t.Fatalf("warned %v rows %v", warned, *written)
	}
}

// Spawn with a run: a refused gate ends the ladder with the step's fields and
// never runs the CLI.
func TestSpawnRunRefusedByBudgetRunsNoCLI(t *testing.T) {
	o, stderr, _ := gateSetup(t, 0.01)
	deps := gateDeps(stderr, []ledger.Row{})
	deps.WriteRow = func(ledger.Object) error { return nil }
	deps.Exec = func(execOpts) execResult { t.Fatal("CLI ran"); return execResult{} }
	res, err := Spawn(o, deps)
	if err != nil {
		t.Fatal(err)
	}
	line, _ := ledger.Stringify(res, false)
	if string(line) != `{"status":"unavailable","reason":"budget","message":"this session's estimated cost (about 0.02) is above budget.usd 0.01; raise budget.usd","answer":null,"spent_usd":0,"budget_usd":0.01,"provider":"anthropic","model":"claude-x","effort":"high","fallback_step":0,"attempts":[]}` {
		t.Fatalf("result = %s", line)
	}
}
