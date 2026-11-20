package releasecheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// sizeRepo is a temp git repo with a v1.0.0 tag and n block commits after it.
func sizeRepo(t *testing.T, n int, config string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(sizeEnv(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	if config != "" {
		if err := os.WriteFile(filepath.Join(dir, ".code-forge.yml"), []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", "-A")
	}
	git("commit", "-q", "--allow-empty", "-m", "B900: before the tag")
	git("tag", "-a", "v1.0.0", "-m", "Release v1.0.0")
	for i := 1; i <= n; i++ {
		git("commit", "-q", "--allow-empty", "-m", "B"+strconv.Itoa(950+i)+": block "+strconv.Itoa(i))
	}
	git("commit", "-q", "--allow-empty", "-m", "release prep: docs stamp")            // not a block
	git("commit", "-q", "--allow-empty", "-m", "B951: a fix round of the same block") // counted once
	return dir
}

// sizeEnv is the environment without repoEnvKeys, as DefaultRunner builds
// it: a GIT_DIR from a hook never points the fixture at the outer repo.
func sizeEnv() []string {
	env := []string{}
	for _, e := range os.Environ() {
		if k, _, _ := strings.Cut(e, "="); !repoEnvKeys[k] {
			env = append(env, e)
		}
	}
	return env
}

// sizeRunner runs git for real and fakes gh and npm.
func sizeRunner(dir string, timeout time.Duration, argv ...string) Exec {
	if argv[0] == "git" {
		return DefaultRunner(dir, timeout, argv...)
	}
	return Exec{Code: -1, Err: argv[0] + " is not on PATH"}
}

// A tag and 8 block commits: release check prints the suggestion; 4: none.
func TestB951ReleaseCheckSuggestsAtMaxBlocks(t *testing.T) {
	r := Run(sizeRepo(t, 8, ""), sizeRunner)
	if r.Size.Blocks != 8 || r.Size.LastTag != "v1.0.0" || r.Size.Max != 8 || r.Size.Level != SizeNow {
		t.Fatalf("size: %+v", r.Size)
	}
	want := "RELEASE NOW: 8 blocks since v1.0.0 (release.max_blocks 8): make a release before more blocks land\n"
	if !strings.Contains(r.Text(), want) || !strings.Contains(r.JSON(), `"release_size":{"last_tag":"v1.0.0","blocks":8,`) {
		t.Fatalf("text:\n%s\njson: %s", r.Text(), r.JSON())
	}
	r = Run(sizeRepo(t, 4, ""), sizeRunner)
	if r.Size.Blocks != 4 || r.Size.Level != SizeNone || strings.Contains(r.Text(), "RELEASE NOW") || strings.Contains(r.Text(), "release size:") {
		t.Fatalf("4 blocks: %+v\n%s", r.Size, r.Text())
	}
	r = Run(sizeRepo(t, 6, ""), sizeRunner)
	if !strings.Contains(r.Text(), "release size: 6 blocks since v1.0.0 (release.max_blocks 8): a release is near\n") {
		t.Fatalf("6 blocks:\n%s", r.Text())
	}
}

// release.max_blocks in .code-forge.yml moves the limit; a bad value keeps 8.
func TestB951MaxBlocksFromConfig(t *testing.T) {
	r := Run(sizeRepo(t, 4, "version: 1\nrelease:\n  max_blocks: 4\n"), sizeRunner)
	if r.Size.Max != 4 || r.Size.Level != SizeNow || !strings.Contains(r.Text(), "RELEASE NOW: 4 blocks since v1.0.0 (release.max_blocks 4)") {
		t.Fatalf("max 4: %+v\n%s", r.Size, r.Text())
	}
	for _, bad := range []string{"0", "2.5", "\"eight\""} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".code-forge.yml"), []byte("version: 1\nrelease:\n  max_blocks: "+bad+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := MaxBlocks(dir); got != DefaultMaxBlocks {
			t.Errorf("max_blocks %s: got %d, want %d", bad, got, DefaultMaxBlocks)
		}
	}
}

func TestB951BlockIDsAndLevels(t *testing.T) {
	got := BlockIDs([]string{"B950: x", "B877+B878: y", "B828fix: z", "B12, B13: w", "B950: again", "release prep: B999 named", "Release v0.26.0", "Bump B1", "B7 B8 words"})
	if want := []string{"B950", "B877", "B878", "B828", "B12", "B13", "B7"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ids = %q, want %q", got, want)
	}
	for _, c := range []struct {
		n, max int
		want   string
	}{{0, 8, SizeNone}, {4, 8, SizeNone}, {5, 8, SizeInfo}, {7, 8, SizeInfo}, {8, 8, SizeNow}, {12, 8, SizeNow}, {1, 2, SizeInfo}, {0, 2, SizeNone}} {
		if got := SizeLevel(c.n, c.max); got != c.want {
			t.Errorf("SizeLevel(%d, %d) = %q, want %q", c.n, c.max, got, c.want)
		}
	}
	if line := (Size{Blocks: 9, Max: 8, Level: SizeNow}).Line(); line != "RELEASE NOW: 9 blocks and no v* tag yet (release.max_blocks 8): make a release before more blocks land" {
		t.Fatalf("no tag line: %q", line)
	}
}
