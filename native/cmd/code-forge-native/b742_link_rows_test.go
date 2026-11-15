package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestB742TreeLinkRowHasNoMode: the tree the init goldens compare prints a
// symlink as "<rel> L -> <target>" on every OS, while a file keeps its mode.
func TestB742TreeLinkRowHasNoMode(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "f"), []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "f"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/some/target", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	got := b527Tree(t, root, filepath.Join(root, "no-home"), filepath.Join(root, "no-cwd"))
	want := "f -rw-r-----\nx\nlink L -> /some/target"
	if got != want {
		t.Fatalf("tree:\n%q\nwant:\n%q", got, want)
	}
}

// TestB742GoldenLinkModeIgnored: a golden link row recorded with macOS
// (Lrwxr-xr-x) or Linux (Lrwxrwxrwx) mode bits reads the same; the target
// and the other rows' modes still count.
func TestB742GoldenLinkModeIgnored(t *testing.T) {
	mac := b552SkillRows(t, ".agents drwxr-xr-x\n.agents/skills/code-forge Lrwxr-xr-x -> <REPO>/skill\nx.yml -rw-r--r--\na: 1", "")
	linux := b552SkillRows(t, ".agents drwxr-xr-x\n.agents/skills/code-forge Lrwxrwxrwx -> <REPO>/skill\nx.yml -rw-r--r--\na: 1", "")
	want := ".agents drwxr-xr-x\n.agents/skills/code-forge L -> <REPO>/skill\nx.yml -rw-r--r--\na: 1"
	if mac != want || linux != want {
		t.Fatalf("mac:\n%q\nlinux:\n%q\nwant:\n%q", mac, linux, want)
	}
	if other := b552SkillRows(t, ".agents/skills/code-forge Lrwxr-xr-x -> <REPO>/other", ""); other == want {
		t.Fatal("a different link target must not match")
	}
	if mode := b552SkillRows(t, "x.yml -rw-------\na: 1", ""); mode != "x.yml -rw-------\na: 1" {
		t.Fatalf("a file mode changed: %q", mode)
	}
}
