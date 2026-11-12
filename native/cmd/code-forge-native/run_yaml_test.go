package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRunYAMLKeyAndTagErrors: a duplicate key and a bad !!timestamp are
// worded by Go exactly as Node's yaml package words them (same line and
// column), so run start and run reload report them without Node.
// Expected texts come from Node's loadProjectConfig on the same files.
func TestRunYAMLKeyAndTagErrors(t *testing.T) {
	cases := []struct {
		src, message, line string
	}{
		{"a: 1\na: 2\n", "DUPLICATE_KEY: Map keys must be unique (line 2, column 1)", "2"},
		{"a:\n  b: 1\n  b: 2\n", "DUPLICATE_KEY: Map keys must be unique (line 3, column 3)", "3"},
		{"a: {x: 1, x: 2}\n", "DUPLICATE_KEY: Map keys must be unique (line 1, column 11)", "1"},
		{"x:\n  ñandú: {k: 1, k: 2}\n", "DUPLICATE_KEY: Map keys must be unique (line 2, column 17)", "2"},
		{"a: 1\nb:\n  - c: 1\n    c: 2\n", "DUPLICATE_KEY: Map keys must be unique (line 4, column 5)", "4"},
		{"\"a\": 1\na: 2\n", "DUPLICATE_KEY: Map keys must be unique (line 2, column 1)", "2"},
		{"x: !!timestamp foo\n", "TAG_RESOLVE_FAILED: !!timestamp expects a date, starting with yyyy-mm-dd (line 1, column 4)", "1"},
	}
	t.Run("start", func(t *testing.T) {
		f := newStartFixture(t, "")
		for _, tc := range cases {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, ".code-forge.yml"), []byte(tc.src), 0644); err != nil {
				t.Fatal(err)
			}
			code, out, errs := runVerbCall(t, "start", "--cwd", dir)
			if code != 2 || out != "" || errs != "run start: "+tc.message+"\n" {
				t.Fatalf("%q = %d %q %q", tc.src, code, out, errs)
			}
		}
		if entries, _ := os.ReadDir(filepath.Join(f.home, ".code-forge", "runs")); len(entries) != 0 {
			t.Fatalf("a refusal wrote a run: %v", entries)
		}
	})
	t.Run("reload", func(t *testing.T) {
		f := newRunFixture(t)
		f.record(t, "r-test", "active", `,"blocks":{}`)
		file := filepath.Join(f.workspace, ".code-forge.yml")
		for _, tc := range cases {
			if err := os.WriteFile(file, []byte(tc.src), 0644); err != nil {
				t.Fatal(err)
			}
			code, out, errs := runVerbCall(t, "reload", "--run", "r-test")
			want := "run reload: the config could not be parsed (.code-forge.yml:" + tc.line + ") — nothing changed\n"
			if code != 1 || out != "" || errs != want {
				t.Fatalf("%q = %d %q %q", tc.src, code, out, errs)
			}
		}
		if len(f.rows(t)) != 0 {
			t.Fatal("a refusal wrote a row")
		}
	})
}
