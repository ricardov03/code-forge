package gates

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/gitx"
)

// gitFixtureTests build a fixture repo with git (B411).
var gitFixtureTests = []string{
	"TestBaseDeltaRetainsOwnedSideOfRename",
	"TestStaleBaseOnlyWhenTheBranchMovedPastTheRecordedBase",
}

// TestGitFixturesIgnoreInheritedGitEnv reruns the fixture-repo tests in a
// child with GIT_DIR, GIT_INDEX_FILE and GIT_WORK_TREE pointing nowhere; each
// must still pass exactly once.
func TestGitFixturesIgnoreInheritedGitEnv(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.count=1", "-test.v", "-test.run=^("+strings.Join(gitFixtureTests, "|")+")$")
	cmd.Env = append(gitx.ChildEnv(), "GIT_DIR=/nonexistent-git-dir", "GIT_INDEX_FILE=/nonexistent-index", "GIT_WORK_TREE=/nonexistent-wt")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture tests fail with inherited GIT_* env: %v\n%s", err, out)
	}
	for _, name := range gitFixtureTests {
		if n := strings.Count(string(out), "--- PASS: "+name+" "); n != 1 {
			t.Fatalf("%s passed %d times in the child, want 1\n%s", name, n, out)
		}
	}
}
