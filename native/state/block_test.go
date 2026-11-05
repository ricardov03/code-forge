package state

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

func TestBlockPublicationAndRefusals(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	record := ledger.Object{{Key: "run_id", Value: "unit"}, {Key: "workspace", Value: workspace}, {Key: "project", Value: "unit"}, {Key: "status", Value: "active"}, {Key: "blocks", Value: ledger.Object{{Key: "A", Value: ledger.Object{{Key: "status", Value: "open"}}}}}}
	if err := SaveRun(home, record); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(home, ".code-forge", "runs", "unit.json")
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	mutate := func(record, block *ledger.Object) ([]ledger.Object, error) {
		block.Set("status", "stopped")
		block.Set("stop_reason", "done")
		record.Set("extra", true)
		return []ledger.Object{{{Key: "event", Value: "block.stop"}, {Key: "block", Value: "A"}, {Key: "ts", Value: "2026-10-31T00:00:00.000Z"}}}, nil
	}
	if err := UpdateBlock(home, "unit", "A", mutate); err == nil || err.Error() != "run unit has no signer key" {
		t.Fatalf("missing key: %v", err)
	}
	after, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("failed signing changed registry: %s %v", after, err)
	}
	if err := os.WriteFile(filepath.Join(home, ".code-forge", "runs", "unit.key"), bytes.Repeat([]byte{7}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	if err := UpdateBlock(home, "unit", "A", mutate); err != nil {
		t.Fatal(err)
	}
	record, err = ReadRun(home, "unit")
	if err != nil {
		t.Fatal(err)
	}
	blocks := record.Get("blocks").(ledger.Object)
	block := blocks.Get("A").(ledger.Object)
	if block.Get("status") != "stopped" || block.Get("stop_reason") != "done" || record.Get("extra") != true {
		t.Fatalf("mutation lost appended fields: %v", record)
	}
	called := false
	if err := UpdateBlock(home, "unit", "A", func(record, block *ledger.Object) ([]ledger.Object, error) { called = true; return nil, nil }); err == nil || err.Error() != "block A is not open in run unit" || called {
		t.Fatalf("closed block must refuse before callback: %v, called=%v", err, called)
	}
	rows, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge", "ledger"), "unit")
	if err != nil || len(rows) != 1 {
		t.Fatalf("exactly one signed row: %v %v", rows, err)
	}
	if err := UpdateBlock(home, "unit", "", func(record, block *ledger.Object) ([]ledger.Object, error) {
		if block != nil {
			t.Fatal("unchecked mutation must receive nil block")
		}
		record.Set("status", "ended")
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	called = false
	if err := UpdateBlock(home, "unit", "", func(record, block *ledger.Object) ([]ledger.Object, error) { called = true; return nil, nil }); err == nil || err.Error() != "run unit has ended" || called {
		t.Fatalf("ended run must refuse before callback: %v, called=%v", err, called)
	}
}
