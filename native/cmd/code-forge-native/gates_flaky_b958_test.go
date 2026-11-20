package main

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B958 tests: the B798 fake go gate, run several times in one run
// ("fixture"); B798_FAIL names the tests that fail once and pass on rerun.

func TestB958ThreeFlakyInRunPass(t *testing.T) {
	f := newB798Fix(t, nil)
	for _, names := range []string{"TestA TestB", "TestC"} {
		t.Setenv("B798_FAIL", names)
		if code, out, errText := f.gates(t); code != 0 || out["allOk"] != true || out["flaky_limit"] != nil {
			t.Fatalf("gates (%s) = %d %v: %s", names, code, out, errText)
		}
	}
	if rows := b798Rows(t, f.home, "test.flaky_limit"); len(rows) != 0 {
		t.Fatalf("test.flaky_limit rows with 3 flaky tests: %v", rows)
	}
}

func TestB958FourthFlakyInRunIsRed(t *testing.T) {
	f := newB798Fix(t, nil)
	t.Setenv("B798_FAIL", "TestA TestB TestC")
	if code, _, errText := f.gates(t); code != 0 {
		t.Fatalf("first gates = %d: %s", code, errText)
	}
	t.Setenv("B798_FAIL", "TestD")
	code, out, errText := f.gates(t)
	if code != 1 || out["allOk"] != false {
		t.Fatalf("gates with a 4th flaky test = %d %v, want 1 and allOk false: %s", code, out, errText)
	}
	want := []any{"example.invalid/m/a.TestA", "example.invalid/m/a.TestB", "example.invalid/m/a.TestC", "example.invalid/m/a.TestD"}
	if !reflect.DeepEqual(out["flaky_limit"], want) {
		t.Errorf("flaky_limit=%v, want %v", out["flaky_limit"], want)
	}
	if !strings.Contains(errText, "gates: 4 flaky tests in run fixture (example.invalid/m/a.TestA, ") || !strings.Contains(errText, "more than 3: the gate is red") {
		t.Errorf("stderr must name the limit: %s", errText)
	}
	rows := b798Rows(t, f.home, "test.flaky_limit")
	if len(rows) != 1 || rows[0]["run"] != "fixture" || rows[0]["block"] != "B1" || rows[0]["max"] != float64(3) || !reflect.DeepEqual(rows[0]["tests"], want) {
		t.Fatalf("test.flaky_limit rows %v", rows)
	}
}

func TestB958CleanGateAfterLimitPasses(t *testing.T) {
	f := newB798Fix(t, nil)
	t.Setenv("B798_FAIL", "TestA TestB TestC")
	f.gates(t)
	t.Setenv("B798_FAIL", "TestD")
	if code, _, _ := f.gates(t); code != 1 {
		t.Fatalf("4th flaky gate = %d, want 1", code)
	}
	t.Setenv("B798_FAIL", "")
	if code, out, errText := f.gates(t); code != 0 || out["allOk"] != true {
		t.Fatalf("clean gate after the limit = %d %v, want green: %s", code, out, errText)
	}
}

// Block land (batch): the run already lists 3 flaky tests; a block gate with a
// 4th fails the land and writes a signed test.flaky_limit row.
func TestB958LandBatchFourthFlakyRefused(t *testing.T) {
	f, goLog := newB667BatchFixture(t)
	b667WriteFile(t, filepath.Join(filepath.Dir(goLog), "fakebin", "go"), b798FakeGo)
	cfg := landTestConfig([]string{filepath.Join(filepath.Dir(goLog), "fakebin", "go"), "test", "./...", "-count=1"})
	data, _ := json.Marshal(cfg)
	f.write(f.repo, ".code-forge.yml", string(data))
	f.git(f.repo, "commit", "-qam", "gate config")
	f.base = f.git(f.repo, "rev-parse", "HEAD")
	for _, name := range []string{"TestA", "TestB", "TestC"} {
		finding := ledger.Object{{Key: "source", Value: "flaky-test"}, {Key: "fingerprint", Value: "flaky-test:example.invalid/m/a." + name}, {Key: "run", Value: "r-batch-unit"}}
		if err := gatesAddFinding(f.home, "r-batch-unit", finding); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("B798_LOG", filepath.Join(f.home, "b798-reruns.log"))
	t.Setenv("B798_FAIL", "TestFlaky")
	t.Setenv("B798_ONCE", "1")
	code, output := f.landWith(nil, "B1", "B2")
	if code == 0 || !strings.Contains(output, "gates: 4 flaky tests in run r-batch-unit (") {
		t.Fatalf("exit=%d, want a refused land naming the flaky limit: %s", code, output)
	}
	rows := f.rows("test.flaky_limit")
	if len(rows) != 1 || rows[0]["block"] != "B1" || rows[0]["mac"] == nil || len(rows[0]["tests"].([]any)) != 4 {
		t.Fatalf("test.flaky_limit rows %v, want one signed row for B1 with 4 tests", rows)
	}
}
