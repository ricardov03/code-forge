package reviewq

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeSymlinkedRoot(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "repo")
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "sample.mjs"), []byte("export {};\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "linked-repo")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	for _, cwd := range []string{root, alias} {
		for _, file := range []string{"sample.mjs", "deleted.mjs"} {
			t.Run(filepath.Base(cwd)+"/"+file, func(t *testing.T) {
				got, err := Normalize(file, filepath.Join(cwd, "src"), alias)
				if err != nil || got != "src/"+file {
					t.Fatalf("Normalize = %q, %v; want %q, nil", got, err, "src/"+file)
				}
			})
		}
	}
	outside := filepath.Join(dir, "outside")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := Normalize("escape/sample.mjs", alias, alias); err == nil {
		t.Fatal("Normalize accepted a path through a symlink outside the repository")
	}
}
