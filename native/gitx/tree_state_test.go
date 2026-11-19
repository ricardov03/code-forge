package gitx

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// B829: the block tree scans never list a .code-forge/ folder (top-level or
// nested), whatever .gitignore says; .code-forge.yml and look-alike names
// are still real changes.
func TestB829TreeScansSkipTheStateFolder(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/nonexistent")
	root := t.TempDir()
	g := func(args ...string) string {
		t.Helper()
		out, err := Run(root, args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	write := func(rel, body string) {
		t.Helper()
		file := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	g("init", "-qb", "main")
	write(".gitignore", "/vendor\n.env\n")
	write("app/User.php", "<?php\n")
	g("add", ".")
	g("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "base")
	base := g("rev-parse", "HEAD")[:40]
	for _, rel := range []string{
		".code-forge/export/B1/vendor/a.php", ".code-forge/export/B1/.env.testing", ".code-forge/queue/t-01.json",
		"packages/api/.code-forge/runs/r.json",
		".code-forge.yml", ".code-forge-notes/x.md", "app/User.php", "app/New.php",
	} {
		write(rel, "x\n")
	}
	want := []string{".code-forge-notes/x.md", ".code-forge.yml", "app/New.php", "app/User.php"}
	files, err := ChangedFiles(root, base)
	if err != nil || !reflect.DeepEqual(files, want) {
		t.Fatalf("ChangedFiles = %v %v, want %v", files, err, want)
	}
	atOpen, err := TreeAtOpen(root, base)
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, f := range atOpen {
		keys = append(keys, f.Key)
	}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("tree_at_open keys = %v, want %v", keys, want)
	}
}
