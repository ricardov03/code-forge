package worker

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
)

// testdata/review_state_golden.json "saves" is Node's real saveState (key
// 32 bytes of 7, run r1, block B1, frozen clock) over the fix-loop states of
// the same golden, content root a git repo.
func TestSaveStateMatchesNode(t *testing.T) {
	data, err := os.ReadFile("testdata/review_state_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	g, err := ledger.DecodeObject(data)
	if err != nil {
		t.Fatal(err)
	}
	rounds := g.Get("rounds").([]any)
	saves := g.Get("saves").([]any)
	if len(saves) != 2 {
		t.Fatalf("golden has %d saves", len(saves))
	}
	clock := func() time.Time { return time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC) }
	for _, s := range saves {
		sc := s.(ledger.Object)
		t.Run(sc.Get("name").(string), func(t *testing.T) {
			home := t.TempDir()
			repo := t.TempDir()
			cmd := exec.Command("git", "init", "-q", repo)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git init: %v %s", err, out)
			}
			state := rounds[int(sc.Get("idx").(float64))].(ledger.Object).Get("state").(ledger.Object)
			rows := []any{}
			err := SaveState(SaveRequest{Home: home, RunID: "r1", Block: "B1", File: "src/app.go", ContentRoot: repo,
				Key: bytes.Repeat([]byte{7}, 32), State: state, Seq: int(sc.Get("seq").(float64)), Now: clock,
				WriteRow: func(r ledger.Object) error { rows = append(rows, r); return nil }})
			if err != nil {
				t.Fatal(err)
			}
			if a, b := mustJSON(t, rows), mustJSON(t, sc.Get("rows")); a != b {
				t.Fatalf("rows differ\n go: %s\nnode: %s", a, b)
			}
			dir := filepath.Join(home, ".code-forge", "runs", "r1", "review-state", "B1")
			names, err := os.ReadDir(dir)
			if err != nil || len(names) != 1 || names[0].Name() != sc.Get("files").([]any)[0] {
				t.Fatalf("files %v %v", names, err)
			}
			text, _ := os.ReadFile(filepath.Join(dir, names[0].Name()))
			if string(text) != sc.Get("text") {
				t.Fatalf("file differs\n go: %s\nnode: %s", text, sc.Get("text"))
			}
			if info, _ := os.Stat(filepath.Join(dir, names[0].Name())); info.Mode().Perm() != 0o600 {
				t.Fatalf("mode %v", info.Mode())
			}
			if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
				t.Fatalf("dir mode %v", info.Mode())
			}
		})
	}
}

func TestSaveStateAnchorFailureWritesNoFile(t *testing.T) {
	home := t.TempDir()
	state := ledger.Object{{Key: "file", Value: "a.go"}, {Key: "reviewed_content", Value: "x\n"}, {Key: "l3_rung_used", Value: true}}
	err := SaveState(SaveRequest{Home: home, RunID: "r1", Block: "B1", File: "a.go", ContentRoot: t.TempDir(),
		Key: bytes.Repeat([]byte{7}, 32), State: state,
		WriteRow: func(r ledger.Object) error {
			// not a git repo under the ceiling: the blob is null, the save goes on
			if body := r.Get("state_body").(ledger.Object); body.Get("reviewed_content_blob") != nil || r.Get("l3_rung_used") != true {
				t.Fatalf("row %s", mustJSON(t, r))
			}
			return errors.New("ledger down")
		}})
	if err == nil {
		t.Fatal("anchor failure was swallowed")
	}
	if _, err := os.Stat(StatePath(home, "r1", "B1", "a.go")); !os.IsNotExist(err) {
		t.Fatalf("state file written after a failed anchor: %v", err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := ledger.Stringify(v, false)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
