package runstatus

import (
	"sort"
	"strings"
	"testing"
)

// The expected order is Node's output for
// ids.sort((a,b)=>a.localeCompare(b,'en',{numeric:true})||(a<b?-1:a>b?1:0)).
func TestB452BlockIDOrderMatchesNode(t *testing.T) {
	want := []string{"01", "1", "2", "10", "A9", "a10", "B", "B 1", "B_1", "B-1", "B.1", "b1", "B001", "B01", "B1", "B1-5", "B1.5", "B1a", "B1A", "B02", "B2", "B9_", "B9-", "B9.", "B9/", "B9z", "B10", "B10x", "B12345678901234567890", "B12345678901234567891", "bA", "Ba", "BA", "x", "X"}
	ids := []string{"B10", "B2", "B02", "B1", "b1", "B1a", "B1A", "B-1", "B_1", "B.1", "B1.5", "B1-5", "B01", "B001", "A9", "a10", "B", "B10x", "x", "X", "1", "01", "10", "2", "B 1", "Ba", "BA", "bA", "B12345678901234567890", "B12345678901234567891", "B9z", "B9-", "B9_", "B9.", "B9/"}
	sort.SliceStable(ids, func(i, j int) bool { c, _ := blockIDCompare(ids[i], ids[j]); return c < 0 })
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("order\n got %v\nwant %v", ids, want)
	}
	if _, ok := blockIDCompare("B1", "Bé"); ok {
		t.Fatal("a non-ASCII id must not claim Node's order")
	}
}

// B690b: every id has a place. Printable ASCII ids keep Node's order; any
// other id sorts after them, by UTF-16 code units (U+1F600 is D83D DE00, so it
// sorts before U+FF5A although its code point is larger).
func TestB690bBlockIDLessOrdersEveryID(t *testing.T) {
	want := []string{"B1", "B2", "B10", "b20", "é", "\U0001F600", "ｚ"}
	ids := []string{"ｚ", "B10", "é", "\U0001F600", "b20", "B2", "B1"}
	sort.SliceStable(ids, func(i, j int) bool { return b690bBlockIDLess(ids[i], ids[j]) })
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("order\n got %q\nwant %q", ids, want)
	}
	for _, a := range want {
		if b690bBlockIDLess(a, a) {
			t.Fatalf("%q < %q: the order is not strict", a, a)
		}
	}
}
