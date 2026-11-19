package yamledit

import (
	"strings"
	"testing"
)

// The splice keeps every other line as written (flow sequences, comments);
// the model only runs for a shape the splice refuses.
func TestB791RemoveSpliceFirstKeepsOtherLines(t *testing.T) {
	text := "# top\ngates:\n  test: [\"sh\", \"-c\", \"exit 0\"] # t\n  extra:\n    secret_scan: true # keep\n    invariants: x.sh # old\nreview: {close_wait_s: 3}\n"
	got, err := RemoveSpliceFirst(text, []string{"gates", "extra", "invariants"}, 1)
	want := strings.Replace(text, "    invariants: x.sh # old\n", "", 1)
	if err != nil || got != want {
		t.Fatalf("got %q, %v\nwant %q", got, err, want)
	}
	got, err = RemoveSpliceFirst("gates:\n  extra:\n    invariants: x.sh\n  lint: null\n", []string{"gates", "extra", "invariants"}, 1)
	if err != nil || got != "gates:\n  lint: null\n" {
		t.Fatalf("prune: got %q, %v", got, err)
	}
	// a flow mapping on the path: the splice refuses, the model edits
	got, err = RemoveSpliceFirst("gates: {extra: {invariants: x.sh, safe_edit: true}}\n", []string{"gates", "extra", "invariants"}, 1)
	if err != nil || strings.Contains(got, "invariants") || !strings.Contains(got, "safe_edit: true") {
		t.Fatalf("flow: got %q, %v", got, err)
	}
	// an anchor: both refuse, nothing is returned
	if got, err := RemoveSpliceFirst("a: &x 1\ngates:\n  extra:\n    invariants: *x\n", []string{"gates", "extra", "invariants"}, 1); err == nil && strings.Contains(got, "invariants") {
		t.Fatalf("anchor: got %q", got)
	}
}
