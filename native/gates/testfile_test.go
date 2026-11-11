package gates

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
)

// B394: a file under test/ that is not a test file (a helper, fixture or
// script) is never picked as a test to run.

func TestTestScopeOwnedFallbackPicksOnlyTestFiles(t *testing.T) {
	block := ownedBlock("test/helpers/x.mjs", "test/a.test.mjs")
	s := TestScope(block, []string{"test/a.test.mjs", "test/helpers/x.mjs"}, 50)
	if s.Kind != "related" || !reflect.DeepEqual(s.Files, []string{"test/a.test.mjs"}) {
		t.Fatalf("scope=%+v, want related with exactly [test/a.test.mjs]", s)
	}
}

func TestChangedEmptyGraphNeverListsATestFolderHelper(t *testing.T) {
	old := map[string]any{"tests/Support/Helper.php": "old"}
	current := ledger.Object{{Key: "tests/Support/Helper.php", Value: "new"}}
	listed := []string{"tests/Support/Helper.php", "tests/Unit/FooTest.php"}
	fallback := Scope{Kind: "related", Files: []string{"tests/Unit/FooTest.php"}}
	s := ChangedEmptyGraph(old, current, fallback, listed, 50)
	if s.Reason != "unpaired" || !reflect.DeepEqual(s.Files, []string{"tests/Unit/FooTest.php"}) {
		t.Fatalf("scope=%+v, want reason unpaired with exactly [tests/Unit/FooTest.php]", s)
	}
}

func TestRepeatFilesSkipsTestFolderHelpers(t *testing.T) {
	cwd := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = cwd
		cmd.Env = gitx.ChildEnv()
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(cwd, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	write("test/helpers/x.mjs", "export const x = 1;\n")
	write("test/a.test.mjs", "import { x } from './helpers/x.mjs';\n")
	write("src/a.mjs", "export const a = 1;\n")
	git("add", ".")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "base")
	base := git("rev-parse", "HEAD")
	write("test/helpers/x.mjs", "export const x = 2;\n")
	write("test/a.test.mjs", "import { x } from './helpers/x.mjs'; // changed\n")
	block := ownedBlock("test/helpers/x.mjs", "test/a.test.mjs", "src/a.mjs")
	files, safe := RepeatFiles(cwd, &Repeat{N: 2, Base: base, Block: block})
	if !safe || !reflect.DeepEqual(files, []string{"test/a.test.mjs"}) {
		t.Fatalf("RepeatFiles=%q safe=%v, want exactly [test/a.test.mjs] and safe", files, safe)
	}
	write("src/a.mjs", "export const a = 2;\n")
	if files, safe := RepeatFiles(cwd, &Repeat{N: 2, Base: base, Block: block}); safe {
		t.Fatalf("RepeatFiles=%q safe=true with a changed src/a.mjs, want unsafe", files)
	}
}
