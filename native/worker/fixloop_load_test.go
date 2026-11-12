package worker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// Every golden step's state files and ledger rows, loaded by Go: the same
// {status, state, seq} as Node's loadState (migrate false) for a.go —
// ok, none, lost, restored (rebuilt from the anchor's body, the reviewed
// text from the current file), tampered (junk file, file with no anchor,
// an anchor of another block that does not verify) and the legacy path.
func TestLoadStateMatchesNode(t *testing.T) {
	for _, sc := range b496Scenarios(t) {
		name := sc.Get("name").(string)
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			b496Repo(t, repo)
			ledgerRows := []ledger.Object{}
			for i, s := range sc.Get("steps").([]any) {
				st := s.(ledger.Object)
				ledgerRows = append(ledgerRows, objects(st.Get("rows"))...)
				home := t.TempDir()
				for _, f := range st.Get("files").(ledger.Object) {
					p := filepath.Join(home, f.Key)
					if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(p, []byte(f.Value.(string)), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				got, err := LoadState(StateWhere{Home: home, RunRootDir: filepath.Join(home, "rr"), RunID: "r1", Block: "B1", File: "a.go",
					Key: b496Key, Rows: ledgerRows, ContentRoot: repo}, false)
				if err != nil {
					t.Fatalf("step %d: %v", i, err)
				}
				sameJSON(t, "step load", got.Object(), st.Get("load"))
			}
		})
	}
}

// B69: a valid state at the legacy path (<runRootDir>/review-state/<block>/
// <name>) moves to the new one when migrate is set: same bytes, mode 0600,
// the old copy removed; a file already at the new path is loaded and the
// legacy copy is not read.
func TestLoadStateMovesLegacyFile(t *testing.T) {
	var step ledger.Object
	var rows []ledger.Object
	for _, sc := range b496Scenarios(t) {
		if sc.Get("name") == "legacy-move" {
			steps := sc.Get("steps").([]any)
			rows = objects(steps[0].(ledger.Object).Get("rows"))
			step = steps[1].(ledger.Object)
		}
	}
	home := t.TempDir()
	repo := t.TempDir()
	b496Repo(t, repo)
	var legacyRel, text string
	for _, f := range step.Get("files").(ledger.Object) {
		legacyRel, text = f.Key, f.Value.(string)
	}
	legacy := filepath.Join(home, legacyRel)
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	where := StateWhere{Home: home, RunRootDir: filepath.Join(home, "rr"), RunID: "r1", Block: "B1", File: "a.go", Key: b496Key, Rows: rows, ContentRoot: repo}
	got, err := LoadState(where, true)
	if err != nil || got.Status != "ok" || got.Seq != 1 {
		t.Fatalf("load %+v %v", got, err)
	}
	target := StatePath(home, "r1", "B1", "a.go")
	data, err := os.ReadFile(target)
	if err != nil || string(data) != text {
		t.Fatalf("moved file %q %v", data, err)
	}
	if info, _ := os.Stat(target); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode())
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy copy kept: %v", err)
	}
	// the new path wins: a junk legacy copy is never read
	if err := os.WriteFile(legacy, []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadState(where, true); err != nil || got.Status != "ok" {
		t.Fatalf("second load %+v %v", got, err)
	}
	if data, _ := os.ReadFile(legacy); string(data) != "junk" {
		t.Fatalf("legacy touched: %q", data)
	}
}

// A state file Node would decode with replacement characters is left to
// Node, and a block id statePath refuses is Node's tampered.
func TestLoadStateEdges(t *testing.T) {
	home := t.TempDir()
	target := StatePath(home, "r1", "B1", "a.go")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("{\xff}"), 0o600); err != nil {
		t.Fatal(err)
	}
	where := StateWhere{Home: home, RunID: "r1", Block: "B1", File: "a.go", Key: b496Key}
	if _, err := LoadState(where, false); !IsUnsupported(err) {
		t.Fatalf("invalid UTF-8: %v", err)
	}
	where.Block = "-bad"
	if got, err := LoadState(where, false); err != nil || got.Status != "tampered" || got.Seq != 0 {
		t.Fatalf("bad block %+v %v", got, err)
	}
}
