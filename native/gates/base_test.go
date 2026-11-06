package gates

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

func TestBaseDeltaRetainsOwnedSideOfRename(t *testing.T) {
	cwd := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = cwd
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("config", "diff.renames", "true")
	if err := os.WriteFile(filepath.Join(cwd, "owned.txt"), []byte("owned source input\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "base")
	base := git("rev-parse", "HEAD")
	git("mv", "owned.txt", "unowned.txt")
	block := ledger.Object{{Key: "owned_files", Value: []any{"owned.txt"}}}
	files, err := BaseDelta(cwd, base, block)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(files, []string{"owned.txt"}) {
		t.Fatalf("owned deletion must remain in delta after rename to unowned path: %v", files)
	}
}
