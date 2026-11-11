package gates

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// fakeGate logs its name to log when it starts, then exits with code.
func fakeGate(log, name, code string) *Command {
	return &Command{Argv: []string{"/bin/sh", "-c", "echo " + name + " >> '" + log + "'; exit " + code}}
}

func startLog(t *testing.T, log string) []string {
	t.Helper()
	b, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return []string{}
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(b))
}

func gateEntries(t *testing.T, results []any) map[string]ledger.Object {
	t.Helper()
	out := map[string]ledger.Object{}
	for _, v := range results {
		r := v.(ledger.Object)
		out[r.Get("gate").(string)] = r
	}
	return out
}

func gateNames(results []any) []string {
	names := []string{}
	for _, v := range results {
		names = append(names, v.(ledger.Object).Get("gate").(string))
	}
	return names
}

// B418: lint, types and format start before the test gate.
func TestRunStartsPreGatesBeforeTest(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "starts.log")
	p := Plan{Commands: map[string]*Command{"test": fakeGate(log, "test", "0"), "lint": fakeGate(log, "lint", "0"), "types": fakeGate(log, "types", "0"), "format": fakeGate(log, "format", "0")}}
	results, all, err := Run(context.Background(), p, dir, "fixture", 30000, Scope{Kind: "full", Reason: "explicit"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := startLog(t, log), []string{"lint", "types", "format", "test"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("start order = %v, want %v", got, want)
	}
	if !all {
		t.Fatalf("allOk = false, want true: %v", results)
	}
	if len(results) != 4 {
		t.Fatalf("results = %d, want 4", len(results))
	}
	for name, r := range gateEntries(t, results) {
		if r.Get("skipped") != false || r.Get("ok") != true {
			t.Fatalf("%s: skipped=%v ok=%v, want ran and ok", name, r.Get("skipped"), r.Get("ok"))
		}
	}
}

// B418: a red types gate stops the test gate from starting.
func TestRunSkipsTestWhenAPreGateIsRed(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "starts.log")
	p := Plan{Commands: map[string]*Command{"test": fakeGate(log, "test", "0"), "lint": fakeGate(log, "lint", "0"), "types": fakeGate(log, "types", "2"), "format": fakeGate(log, "format", "0")}}
	results, all, err := Run(context.Background(), p, dir, "fixture", 30000, Scope{Kind: "full", Reason: "explicit"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := startLog(t, log), []string{"lint", "types", "format"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("start log = %v, want %v (test must never start)", got, want)
	}
	if all {
		t.Fatal("allOk = true, want false")
	}
	if got, want := gateNames(results), []string{"test", "lint", "types", "format"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("gate entries = %v, want %v", got, want)
	}
	test := gateEntries(t, results)["test"]
	if test.Get("skipped") != true || test.Get("ok") != false || test.Get("code") != nil || test.Get("reason") != "pre_gate_failed" {
		t.Fatalf("test entry = %v, want skipped, ok false, code null, reason pre_gate_failed", test)
	}
	if got := test.Get("pre_gates_failed"); !reflect.DeepEqual(got, []string{"types"}) {
		t.Fatalf("pre_gates_failed = %v, want [types]", got)
	}
	if types := gateEntries(t, results)["types"]; types.Get("ok") != false || types.Get("code") != 2 {
		t.Fatalf("types entry = %v, want ok false code 2", types)
	}
}

// B418: with no lint, types or format the test gate runs as before.
func TestRunTestAloneStillRuns(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "starts.log")
	p := Plan{Commands: map[string]*Command{"test": fakeGate(log, "test", "0")}}
	results, all, err := Run(context.Background(), p, dir, "fixture", 30000, Scope{Kind: "full", Reason: "explicit"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := startLog(t, log), []string{"test"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("start log = %v, want %v", got, want)
	}
	test := gateEntries(t, results)["test"]
	if !all || test.Get("skipped") != false || test.Get("ok") != true || test.Get("reason") != nil {
		t.Fatalf("allOk=%v test=%v, want test ran ok with no reason", all, test)
	}
}
