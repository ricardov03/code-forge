package worker

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
)

// testdata/b496_fixloop_golden.json is Node's real fixLoopRound
// (src/worker/engine.mjs, reviewFile replaced by a fixed result) over 23
// scenarios: key 32 bytes of 7, run r1, block B1, level L2, frozen clock
// 2026-11-11T09:00:00.000Z, ledger rows signed as the worker's writeRow does
// ({run, ...row, block, file, content_hash}). Each step records the outcome,
// the new rows, the state files under HOME and Node's loadState for a.go.

var b496Key = bytes.Repeat([]byte{7}, 32)

func b496Clock() time.Time { return time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC) }

func b496Scenarios(t *testing.T) []ledger.Object {
	t.Helper()
	out := fixLoopGoldenScenarios(t, "testdata/b496_fixloop_golden.json")
	if len(out) != 23 {
		t.Fatalf("golden has %d scenarios", len(out))
	}
	return out
}

// fixLoopGoldenScenarios reads one fix-loop golden file's scenarios.
func fixLoopGoldenScenarios(t *testing.T, path string) []ledger.Object {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := ledger.DecodeObject(append(append([]byte(`{"s":`), data...), '}'))
	if err != nil {
		t.Fatal(err)
	}
	out := []ledger.Object{}
	for _, s := range wrapped.Get("s").([]any) {
		out = append(out, s.(ledger.Object))
	}
	return out
}

func b496Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=g", "GIT_AUTHOR_EMAIL=g@x", "GIT_COMMITTER_NAME=g", "GIT_COMMITTER_EMAIL=g@x",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return string(bytes.TrimSpace(out))
}

// b496Repo is golden.mjs makeRepo: a.go, c.go, db/migration.go committed,
// then a.go and db/migration.go changed and b.go added. It returns the base.
func b496Repo(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	b496Git(t, dir, "init", "-q")
	write := func(name, text string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.go", "package a\n")
	write("c.go", "package c\n")
	write("db/migration.go", "package db\n")
	b496Git(t, dir, "add", "-A")
	b496Git(t, dir, "commit", "-q", "-m", "base")
	base := b496Git(t, dir, "rev-parse", "HEAD")
	write("a.go", "package a\n\nfunc A() {}\n")
	write("b.go", "package b\n")
	write("db/migration.go", "package db\n// m\n")
	return base
}

func objects(v any) []ledger.Object {
	out := []ledger.Object{}
	list, _ := v.([]any)
	for _, r := range list {
		out = append(out, r.(ledger.Object))
	}
	return out
}

// b496Files is golden.mjs walk: every file under HOME/.code-forge/runs and
// HOME/rr, relative to HOME, with its text.
func b496Files(t *testing.T, home string) ledger.Object {
	t.Helper()
	files := ledger.Object{}
	for _, top := range []string{filepath.Join(home, ".code-forge", "runs"), filepath.Join(home, "rr")} {
		_ = filepath.WalkDir(top, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			data, rerr := os.ReadFile(p)
			if rerr != nil {
				t.Fatal(rerr)
			}
			rel, _ := filepath.Rel(home, p)
			files = append(files, ledger.Field{Key: rel, Value: string(data)})
			return nil
		})
	}
	return files
}

func sameJSON(t *testing.T, what string, got, want any) {
	t.Helper()
	if a, b := mustJSON(t, got), mustJSON(t, want); a != b {
		t.Fatalf("%s differs\n  go: %s\nnode: %s", what, a, b)
	}
}
