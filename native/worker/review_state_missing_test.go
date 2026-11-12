package worker

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B504: missingStateFiles lists the files whose latest anchor has no state
// file at the new or the legacy path; a bad anchor of the run gives !ok.
func TestB504MissingStateFiles(t *testing.T) {
	home := t.TempDir()
	runRoot := t.TempDir()
	withBody := b496Anchor(t, "B2", "c.go", false)
	withBody.Set("state_body", "{}")
	withBody, err := signAt(withBody, b496Key, b496Clock())
	if err != nil {
		t.Fatal(err)
	}
	rows := []ledger.Object{b496Anchor(t, "B1", "a.go", false), b496Anchor(t, "B1", "b.go", false), withBody, b496Anchor(t, "B1", "a.go", false)}
	got, ok := MissingStateFiles(home, "r1", b496Key, rows, runRoot)
	if !ok || len(got) != 3 {
		t.Fatalf("none on disk: ok=%v %+v", ok, got)
	}
	for _, p := range []string{StatePath(home, "r1", "B1", "a.go"), LegacyStatePath(runRoot, "B1", "b.go")} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, ok = MissingStateFiles(home, "r1", b496Key, rows, runRoot)
	if want := []MissingState{{Block: "B2", File: "c.go", Rebuildable: true}}; !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("a.go new path, b.go legacy path: ok=%v %+v", ok, got)
	}
	if got, ok := MissingStateFiles(home, "r1", b496Key, rows, ""); !ok || len(got) != 2 {
		t.Errorf("no legacy root: ok=%v %+v", ok, got)
	}
	if got, ok := MissingStateFiles(home, "r9", b496Key, rows, runRoot); !ok || len(got) != 0 {
		t.Errorf("other run: ok=%v %+v", ok, got)
	}
	bad := b496Anchor(t, "B9", "z.go", false)
	bad.Set("block", "B8")
	if _, ok := MissingStateFiles(home, "r1", b496Key, append(rows, bad), runRoot); ok {
		t.Error("a bad anchor of the run must give !ok")
	}
}
