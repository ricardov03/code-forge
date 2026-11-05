package owned

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

func TestKindsAndMatches(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "dir"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	kinds := Kinds(root, []any{"file", "dir", "link", "missing", "file/child", "*.go"})
	want := map[string]string{"file": "file", "dir": "dir", "link": "other", "missing": "absent", "file/child": "absent"}
	if len(kinds) != 5 {
		t.Fatalf("want exactly five exact-entry kinds, got %v", kinds)
	}
	for entry, kind := range want {
		if kinds.Get(entry) != kind {
			t.Errorf("%s kind = %v, want %s", entry, kinds.Get(entry), kind)
		}
	}
	for _, c := range []struct {
		entry, file string
		kind        string
		want        bool
	}{
		{"file", "file", "file", true}, {"file", "file/child", "file", false}, {"dir", "dir/a", "dir", true}, {"dir", "dirX/a", "dir", false},
		{"src/**/{a,b{1,2}}.go", "src/.dot/b2.go", "", true}, {"src/{a,b}", "src/a/child", "", false},
		{"?.go", "😀.go", "", false}, {"??.go", "😀.go", "", true}, {"pages/[id].vue", "pages/[id].vue", "file", true},
	} {
		k := ledger.Object{{Key: c.entry, Value: c.kind}}
		if got := OwnsFile([]any{c.entry}, k, c.file); got != c.want {
			t.Errorf("%s owns %s = %v, want %v", c.entry, c.file, got, c.want)
		}
	}
}
