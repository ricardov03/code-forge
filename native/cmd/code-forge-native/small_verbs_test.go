package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/gitx"
)

// smallProject is a temp HOME and a git repository holding config, entered
// as the working directory. CODE_FORGE_NODE names no executable, so a run
// that is handed over fails runCaptured instead of starting Node.
func smallProject(t *testing.T, config string) (home, repo string) {
	t.Helper()
	base, _ := filepath.EvalSymlinks(t.TempDir())
	home, repo = filepath.Join(base, "home"), filepath.Join(base, "repo")
	for _, dir := range []string{home, repo} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	git := exec.Command("git", "init", "-q", repo)
	git.Env = gitx.ChildEnv()
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	if config != "" {
		if err := os.WriteFile(filepath.Join(repo, ".code-forge.yml"), []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	here, _ := os.Getwd()
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(here) })
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-b455-node")
	t.Setenv("CODE_FORGE_TMP_ROOT", filepath.Join(base, "tmp"))
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "")
	return home, repo
}

type smallCase struct {
	args               []string
	code               int
	stdout, stderrText string
}

func checkSmallCases(t *testing.T, cases []smallCase) {
	t.Helper()
	for _, c := range cases {
		code, out, errText := runCaptured(t, c.args...)
		if code != c.code || out != c.stdout || errText != c.stderrText {
			t.Errorf("%q: code=%d stdout=%q stderr=%q\nwant code=%d stdout=%q stderr=%q", c.args, code, out, errText, c.code, c.stdout, c.stderrText)
		}
	}
}

// expectHandover checks the run is left to Node with reason: with no Node
// executable the handover exits 127 after logging its row.
func expectHandover(t *testing.T, reason string, args ...string) {
	t.Helper()
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "")
	t.Setenv("CODE_FORGE_NATIVE", "")
	code, _ := run(args)
	data, _ := os.ReadFile(handoverLogPath())
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if code != 127 || !strings.Contains(lines[len(lines)-1], `"reason":"`+reason+`"`) {
		t.Fatalf("%q: want a Node handover (%s), got code=%d log=%s", args, reason, code, data)
	}
}
