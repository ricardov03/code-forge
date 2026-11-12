package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// B567: every child the Go doctor runs (the CLI --version/--help probes, the
// provider pings and the isolation/path-deny sessions, the temp repos' and
// the worker probe's git init, the repo-top git of the indexing check, the
// 1Password op read) is recorded in the pid registry of this process's run
// root like Node's exec (src/util/exec.mjs + reaper.mjs registerPid): a full
// run adopts its doctor-<pid>-<hex> root, a quick run uses the lazy
// proc-<pid>-<ms> root, released at exit. testdata/b567/fake.sh is a fake
// claude/grok/op/git that waits for its own entry and records what it sees;
// testdata/b567/*.txt are the records of Node's run on the same setup
// (CODE_FORGE_NATIVE=0 ./bin/code-forge doctor ..., a temp HOME, a temp
// CODE_FORGE_TMP_ROOT, the fakes and node on a bare PATH), masked (every
// number -> <PID>, start_time -> <START>) and sorted (Node runs the probes
// in another order). A git child of Node's real worker is not recorded.

var (
	b567TestData, _ = filepath.Abs("testdata") // before a test's chdir
	b567Record      = regexp.MustCompile(`(?m)^(\S+) (.*) (\d+) SEEN \S+ \S+ \S+\n\{"pid":(\d+),"start_time":"([^"]*)","argv0":"([^"]*)"\}$`)
	b567Header      = regexp.MustCompile(`^\S+ .* <PID> (SEEN|NOENTRY)`)
)

// b567Setup is B525's doctor setup (fake worker, sealed process table, Jev
// on a closed port) with the B567 fakes on PATH under each name.
func b567Setup(t *testing.T, config string, fakes []string) (*b525Env, string) {
	t.Helper()
	td := b567TestData
	e := b525Setup(t, b525Case{mode: "ok", config: config}, "")
	fake, err := os.ReadFile(filepath.Join(td, "b567", "fake.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.bin, "fake"), fake, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range fakes {
		if err := os.Symlink("fake", filepath.Join(e.bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(e.base, "fake.log")
	t.Setenv("FAKELOG", log)
	t.Setenv("B567_TMP", e.tmp)
	t.Setenv("B567_TESTDATA", td)
	return e, log
}

// b567Records checks each recorded entry is the child's own and names its
// command with a ps start time, then returns the records masked and sorted.
func b567Records(t *testing.T, log string) string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	got := string(data)
	for _, m := range b567Record.FindAllStringSubmatch(got, -1) {
		if m[3] != m[4] {
			t.Errorf("entry is not the child's own: %q", m[0])
		}
		if m[1] != m[6] {
			t.Errorf("argv0 %q for %s", m[6], m[1])
		}
		if !lrRegLstart.MatchString(m[5]) {
			t.Errorf("start_time %q is not ps lstart", m[5])
		}
	}
	masked := lrRegPid.ReplaceAllString(lrRegStart.ReplaceAllString(got, `"start_time":"<START>"`), "<PID>")
	return b567Sort(masked)
}

// b567Sort sorts the records (a header line and, when SEEN, its entry).
func b567Sort(text string) string {
	var records []string
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		switch {
		case line == "":
		case b567Header.MatchString(line) || len(records) == 0:
			records = append(records, line+"\n")
		default:
			records[len(records)-1] += line + "\n"
		}
	}
	sort.Strings(records)
	return strings.Join(records, "")
}

func b567Want(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(b567TestData, "b567", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

const b567Plain = "provider: anthropic\n" + b474Levels

// A full run: every probe child sees its own entry in the adopted doctor
// root, with Node's fields; the root is gone after the run.
func TestB567DoctorFullRunRegistersEachChild(t *testing.T) {
	e, log := b567Setup(t, b567Plain, []string{"claude", "grok", "git"})
	code, stdout, stderr := runCaptured(t, "doctor")
	if code != 0 || !strings.Contains(stdout, "OK   claude: present, 9.9.9 (fake claude)\n") || !strings.HasSuffix(stderr, "0 fail\n") {
		t.Fatalf("doctor: exit %d\n%s\nstderr: %s", code, stdout, stderr)
	}
	if got, want := b567Records(t, log), b567Want(t, "full.txt"); got != want {
		t.Errorf("registry seen by the children:\n%s\nwant (Node):\n%s", got, want)
	}
	e.assertClean(t)
	if lrRoot != "" {
		t.Errorf("the doctor root is still adopted: %s", lrRoot)
	}
}

// A quick run adopts no root: the op child is recorded in the lazy root,
// which is released at exit.
func TestB567DoctorQuickOpRegistersInTheLazyRoot(t *testing.T) {
	e, log := b567Setup(t, b483OpCfg, []string{"op", "git"})
	code, stdout, stderr := runCaptured(t, "doctor", "--quick")
	if code != 0 || !strings.Contains(stdout, "OK   keys: jev key from op") || stderr != b474Summary(2, 1, 0) {
		t.Fatalf("doctor --quick: exit %d\n%s\nstderr: %s", code, stdout, stderr)
	}
	if got, want := b567Records(t, log), b567Want(t, "quick-op.txt"); got != want {
		t.Errorf("registry seen by the children:\n%s\nwant (Node):\n%s", got, want)
	}
	e.assertClean(t)
}

// An unwritable temp root: no doctor root, no probes, and the children that
// still run (git) find no entry, as in Node; nothing is written there.
func TestB567DoctorUnwritableRootStillRuns(t *testing.T) {
	e, log := b567Setup(t, b567Plain, []string{"claude", "grok", "git"})
	if err := os.Chmod(e.tmp, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(e.tmp, 0o700) })
	code, stdout, _ := runCaptured(t, "doctor")
	if code != 1 || !strings.Contains(stdout, "FAIL probes: no temp root (EACCES: permission denied, mkdir ") {
		t.Fatalf("doctor: exit %d\n%s", code, stdout)
	}
	if got, want := b567Records(t, log), b567Want(t, "ro.txt"); got != want {
		t.Errorf("registry seen by the children:\n%s\nwant (Node):\n%s", got, want)
	}
	if err := os.Chmod(e.tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	e.assertClean(t)
}

// Init's installer is recorded under the tmp.root of the config at the
// process start (bin/code-forge.mjs applyTmpRoot), not the one init's own
// .code-forge.yml gives: from a subfolder the parent's tmp.root holds even
// after init writes the subfolder's config, which has none. The want is
// Node's (CODE_FORGE_NATIVE=0 ./bin/code-forge init ... on the same setup):
// the npm child sees its entry under tmp.root in both cases.
func TestB567InitInstallUsesTheStartTmpRoot(t *testing.T) {
	for _, sub := range []string{"", "sub"} {
		name := "own config"
		if sub != "" {
			name = "parent config"
		}
		t.Run(name, func(t *testing.T) {
			base, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			repo, tmpRoot, home, bin := filepath.Join(base, "repo"), filepath.Join(base, "A"), filepath.Join(base, "home"), filepath.Join(base, "bin")
			for _, dir := range []string{filepath.Join(repo, ".git"), filepath.Join(repo, sub), tmpRoot, home, bin, filepath.Join(base, "tmpdir")} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(repo, ".code-forge.yml"), []byte("tmp:\n  root: "+tmpRoot+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			npm, err := os.ReadFile(filepath.Join(b567TestData, "b567", "npm.sh"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bin, "npm"), npm, 0o755); err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(base, "fake.log")
			t.Setenv("HOME", home)
			t.Setenv("PATH", bin+":/usr/bin:/bin")
			t.Setenv("TMPDIR", filepath.Join(base, "tmpdir"))
			t.Setenv("CODE_FORGE_TMP_ROOT", "")
			t.Setenv("CODE_FORGE_NODE", "/nonexistent-b567-node")
			t.Setenv("CODE_FORGE_HANDOVER_LOG", "0")
			t.Setenv("CODE_FORGE_NATIVE", "1")
			t.Setenv("CODE_FORGE_KEY_BACKEND", "file")
			t.Setenv("B567_TMP", tmpRoot)
			t.Setenv("FAKELOG", log)
			for _, name := range []string{"CLAUDECODE", "CLAUDE_CODE", "CURSOR_AGENT", "AI_AGENT", "CODEX_SANDBOX", "CODEX_SANDBOX_NETWORK_DISABLED", "SOLO_MCP_PATH", "JEV_KEY", "CODE_FORGE_KEY_JEV"} {
				t.Setenv(name, "")
			}
			savedTTY, savedNow := initStdinTTY, initNow
			initStdinTTY = func() bool { return false }
			initNow = func() time.Time { return time.Now().UTC() }
			defer func() { initStdinTTY, initNow = savedTTY, savedNow }()
			pkg := b458Repo(t)
			savedRoot, savedUpgrade := metaPackageRoot, upgradePackageRoot
			metaPackageRoot = func() (string, error) { return pkg, nil }
			upgradePackageRoot = func() (string, bool) { return pkg, true }
			defer func() { metaPackageRoot, upgradePackageRoot = savedRoot, savedUpgrade }()
			old := toolSystemApps
			toolSystemApps = filepath.Join(base, "no-apps")
			defer func() { toolSystemApps = old }()
			here, _ := os.Getwd()
			if err := os.Chdir(filepath.Join(repo, sub)); err != nil {
				t.Fatal(err)
			}
			defer os.Chdir(here)
			code, stdout, stderr := runCaptured(t, "init", "--no-interaction", "--no-jev", "--skip-doctor", "--tools", "recommended", "--yes-tool", "codex")
			if code != 0 || !strings.Contains(stdout, "tool codex: installed (npm install -g @openai/codex)") {
				t.Fatalf("init: exit %d\n%s\nstderr: %s", code, stdout, stderr)
			}
			data, _ := os.ReadFile(log)
			want := `npm install SEEN {"pid":<PID>,"start_time":"<START>","argv0":"npm"}` + "\n"
			if string(data) != want {
				t.Errorf("the installer saw:\n%s\nwant (Node):\n%s", data, want)
			}
			for _, dir := range []string{tmpRoot, filepath.Join(base, "tmpdir")} {
				if left, _ := os.ReadDir(dir); len(left) != 0 {
					t.Errorf("%s holds %d entries after init", dir, len(left))
				}
			}
		})
	}
}
