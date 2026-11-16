package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestB760NativeRepoNeedsNoNodeScript: a code-forge tree is known by native/go.mod and
// scripts/cf-release.sh, so a tree without scripts/build-native.mjs (deleted by B760) still
// rebuilds its binary, and a tree without cf-release.sh is not a code-forge tree.
func TestB760NativeRepoNeedsNoNodeScript(t *testing.T) {
	for _, c := range []struct {
		name  string
		files []string
		want  bool
	}{
		{"go.mod and cf-release.sh", []string{"native/go.mod", "scripts/cf-release.sh"}, true},
		{"old tree with both scripts", []string{"native/go.mod", "scripts/cf-release.sh", "scripts/build-native.mjs"}, true},
		{"only the Node script", []string{"native/go.mod", "scripts/build-native.mjs"}, false},
		{"no go.mod", []string{"scripts/cf-release.sh"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			for _, f := range c.files {
				path := filepath.Join(root, f)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := nativeRepo(root); got != c.want {
				t.Fatalf("nativeRepo = %v, want %v", got, c.want)
			}
		})
	}
}
