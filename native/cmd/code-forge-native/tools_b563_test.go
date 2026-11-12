package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// B563: Go `tools` (version probes and installs) and init's step 1 installs
// record each child in the pid registry of this process's run root like
// Node's exec (src/util/exec.mjs + reaper.mjs registerPid): <run-root>/pids/
// <pid>.json while the child runs, removed when it settles, the lazy root
// removed at the end. testdata/b563/fake.sh is a fake tool (or npm) that
// waits for its own entry and writes what it sees; registry-*.txt and
// *.stdout.txt are Node's (CODE_FORGE_NATIVE=0 ./bin/code-forge tools ...)
// output for the same setup. Masks: every pid -> <PID>, start_time -> <START>.

var tlRegSeen = regexp.MustCompile(`(?m)^(\S+) (\S+) (\d+) SEEN \S+ \S+ (\d+)\.json \n\{"pid":(\d+),"start_time":"([^"]*)","argv0":"([^"]*)"\}\n`)

type tlRegCase struct {
	home, root, log string
}

// tlRegEnv puts the fake on a bare PATH under each name, with a temp HOME,
// a temp CODE_FORGE_TMP_ROOT and the fake's log.
func tlRegEnv(t *testing.T, names ...string) tlRegCase {
	t.Helper()
	home, _ := b500Env(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := tlRegCase{home: home, root: root, log: filepath.Join(t.TempDir(), "fake.log")}
	fake, err := os.ReadFile(filepath.Join(lrTestData, "..", "b563", "fake.sh"))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	b500Write(t, filepath.Join(bin, "fake"), string(fake))
	if err := os.Chmod(filepath.Join(bin, "fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if err := os.Symlink("fake", filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("FAKELOG", c.log)
	t.Setenv("CODE_FORGE_TMP_ROOT", c.root)
	old := toolSystemApps
	toolSystemApps = filepath.Join(t.TempDir(), "no-apps")
	t.Cleanup(func() { toolSystemApps = old })
	return c
}

// check compares the fake's log with Node's after the masks, and checks each
// entry is the child's own, names its command, with a real ps start time.
func (c tlRegCase) check(t *testing.T, golden string) {
	t.Helper()
	got := b500Read(t, c.log)
	seen := tlRegSeen.FindAllStringSubmatch(got, -1)
	if len(seen) != strings.Count(got, " SEEN ") {
		t.Errorf("%d of %d entries parsed:\n%s", len(seen), strings.Count(got, " SEEN "), got)
	}
	for _, m := range seen {
		if m[3] != m[4] || m[3] != m[5] {
			t.Errorf("entry is not the child's own: %q", m[0])
		}
		if m[1] != m[7] {
			t.Errorf("argv0 %q for %s", m[7], m[1])
		}
		if !lrRegLstart.MatchString(m[6]) {
			t.Errorf("start_time %q is not ps lstart", m[6])
		}
	}
	masked := lrRegPid.ReplaceAllString(lrRegStart.ReplaceAllString(got, `"start_time":"<START>"`), "<PID>")
	if want := b500Read(t, filepath.Join(lrTestData, "..", "b563", golden)); masked != want {
		t.Errorf("registry seen by the children:\n%s\nwant (Node):\n%s", masked, want)
	}
}

// left lists what is under the temp root after the run (Node leaves nothing).
func (c tlRegCase) left(t *testing.T) []string {
	t.Helper()
	left := []string{}
	_ = filepath.WalkDir(c.root, func(path string, _ os.DirEntry, err error) error {
		if err == nil && path != c.root {
			left = append(left, strings.TrimPrefix(path, c.root+"/"))
		}
		return nil
	})
	return left
}

var tlAllTools = []string{"claude", "codex", "gemini", "grok", "op", "solo", "npm"}

func TestB563ToolsVersionProbesRegisterEachChild(t *testing.T) {
	c := tlRegEnv(t, tlAllTools...)
	code, out, errText := b500Run(t, "tools")
	want := b500Read(t, filepath.Join(lrTestData, "..", "b563", "status.stdout.txt"))
	if code != 0 || out != want || errText != "" {
		t.Errorf("tools: got %d\n%s\nstderr: %q\nwant (Node):\n%s", code, out, errText, want)
	}
	c.check(t, "registry-status.txt")
	if left := c.left(t); len(left) != 0 {
		t.Errorf("run root or entry left behind: %v", left)
	}
	if h := b500HandedOver(t, c.home); h != "" {
		t.Errorf("handed over: %s", h)
	}
}

func TestB563ToolsInstallRegistersTheInstaller(t *testing.T) {
	c := tlRegEnv(t, "npm")
	code, out, errText := b500Run(t, "tools", "install", "codex", "--yes")
	want := b500Read(t, filepath.Join(lrTestData, "..", "b563", "install.stdout.txt"))
	if code != 0 || out != want || errText != "" {
		t.Errorf("tools install: got %d\n%s\nstderr: %q\nwant (Node):\n%s", code, out, errText, want)
	}
	c.check(t, "registry-install.txt")
	if left := c.left(t); len(left) != 0 {
		t.Errorf("run root or entry left behind: %v", left)
	}
	if h := b500HandedOver(t, c.home); h != "" {
		t.Errorf("handed over: %s", h)
	}
}

func TestB563ToolsUnwritableRegistryStillProbes(t *testing.T) {
	c := tlRegEnv(t, tlAllTools...)
	if err := os.Chmod(c.root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(c.root, 0o700) })
	code, out, errText := b500Run(t, "tools")
	want := b500Read(t, filepath.Join(lrTestData, "..", "b563", "status.stdout.txt"))
	if code != 0 || out != want || errText != "" {
		t.Errorf("unwritable registry: got %d\n%s\nstderr: %q", code, out, errText)
	}
	c.check(t, "registry-ro.txt")
	if left := c.left(t); len(left) != 0 {
		t.Errorf("unwritable root changed: %v", left)
	}
}

// Init's step 1 installs through the same exec: the npm child sees its own
// entry as Node's `tools install` npm child does.
func TestB563InitInstallRegistersTheInstaller(t *testing.T) {
	c := tlRegEnv(t, "npm")
	var lines []string
	for _, line := range initToolLines(false, []string{"codex"}) {
		if strings.HasPrefix(line, "tool codex:") {
			lines = append(lines, line)
		}
	}
	toolsReleaseRoot() // init's exit (B567: runInitVerb releases the root)
	if want := "tool codex: installed (npm install -g @openai/codex)"; len(lines) != 1 || lines[0] != want {
		t.Errorf("init step 1: got %q, want %q", lines, want)
	}
	c.check(t, "registry-install.txt")
	if left := c.left(t); len(left) != 0 {
		t.Errorf("run root or entry left behind: %v", left)
	}
}
