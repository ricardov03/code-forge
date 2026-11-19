package worker

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/gitx"
)

// B829: the budget's untracked list never holds the .code-forge/ folder (the
// customer's proof export and queue tickets), top-level or nested.
func TestB829BudgetFileSetSkipsTheStateFolder(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/nonexistent")
	repo := t.TempDir()
	g := func(args ...string) string {
		t.Helper()
		out, err := gitx.Run(repo, args...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out)
	}
	write := func(rel string) {
		t.Helper()
		file := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(rel+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	g("init", "-qb", "main")
	write("app/User.php")
	g("add", ".")
	g("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "base")
	base := g("rev-parse", "HEAD")
	for _, rel := range []string{".code-forge/export/B1/vendor/a.php", ".code-forge/queue/t-01.json", "api/.code-forge/runs/r.json", ".code-forge.yml", "app/Card.php"} {
		write(rel)
	}
	all, ok, err := computeFileSet(repo, base)
	if want := []string{".code-forge.yml", "app/Card.php"}; !ok || err != nil || !reflect.DeepEqual(all, want) {
		t.Fatalf("file set = %v ok=%v err=%v, want %v", all, ok, err, want)
	}
}
