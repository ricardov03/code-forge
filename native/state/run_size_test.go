package state

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// b829Record is a run whose blocks.B3 carries a tree_at_open like the
// customer's: thousands of .code-forge/export/B1/vendor stamps (B829).
func b829Record(t *testing.T, files int, caps ledger.Object) ledger.Object {
	t.Helper()
	tree := ledger.Object{}
	for i := 0; i < files; i++ {
		tree = append(tree, ledger.Field{Key: ".code-forge/export/B1/vendor/laravel/framework/src/Illuminate/File" + strings.Repeat("x", 40) + string(rune('a'+i%26)) + strconv.Itoa(i) + ".php", Value: "file:" + strings.Repeat("0", 64)})
	}
	blocks := ledger.Object{
		{Key: "B2", Value: ledger.Object{{Key: "status", Value: "open"}}},
		{Key: "B3", Value: ledger.Object{{Key: "status", Value: "open"}, {Key: "tree_at_open", Value: tree}}},
	}
	record := ledger.Object{{Key: "run_id", Value: "r-big"}, {Key: "workspace", Value: t.TempDir()}, {Key: "project", Value: "unit"}, {Key: "status", Value: "active"}, {Key: "blocks", Value: blocks}}
	if caps != nil {
		record = append(record, ledger.Field{Key: "config", Value: ledger.Object{{Key: "hash", Value: "h1"}, {Key: "snapshots", Value: ledger.Object{{Key: "h1", Value: ledger.Object{{Key: "caps", Value: caps}}}}}}})
	}
	return record
}

func b829Save(t *testing.T, record ledger.Object) (home, warned string) {
	t.Helper()
	var out bytes.Buffer
	before := RecordWarnOut
	RecordWarnOut = &out
	t.Cleanup(func() { RecordWarnOut = before })
	home = t.TempDir()
	if err := SaveRun(home, record); err != nil {
		t.Fatal(err)
	}
	return home, out.String()
}

func TestB829SaveWarnsOverTheBound(t *testing.T) {
	// ~15,000 stamps is the customer's size class: well over 2 MB
	home, warned := b829Save(t, b829Record(t, 15000, nil))
	if strings.Count(warned, "\n") != 1 || !strings.HasPrefix(warned, "WARN run record r-big is ") ||
		!strings.Contains(warned, "over caps.run_record_warn_mb (2.0 MB); largest keys: blocks ") ||
		!strings.Contains(warned, ", blocks.B3 ") {
		t.Fatalf("warning = %q", warned)
	}
	// the save happened and the large record still reads
	got, err := ReadRun(home, "r-big")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(got.Get("blocks").(ledger.Object).Get("B3").(ledger.Object).Get("tree_at_open").(ledger.Object)); n != 15000 {
		t.Fatalf("read back %d stamps", n)
	}
}

func TestB829SaveIsQuietWithinTheBound(t *testing.T) {
	if _, warned := b829Save(t, b829Record(t, 20, nil)); warned != "" {
		t.Fatalf("small record warned: %q", warned)
	}
	// the run's config sets the bound: 0 turns it off, a lower bound fires sooner
	if _, warned := b829Save(t, b829Record(t, 15000, ledger.Object{{Key: "run_record_warn_mb", Value: 0.0}})); warned != "" {
		t.Fatalf("bound 0 warned: %q", warned)
	}
	if _, warned := b829Save(t, b829Record(t, 200, ledger.Object{{Key: "run_record_warn_mb", Value: 0.01}})); !strings.Contains(warned, "over caps.run_record_warn_mb (10 KB)") {
		t.Fatalf("bound 0.01 MB: %q", warned)
	}
}
