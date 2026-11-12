package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// B528: the interactive init wizard in Go. Each case feeds the keystrokes
// of a recorded Node run (CODE_FORGE_NATIVE=0 ./bin/code-forge init
// --skip-doctor --no-jev in a pseudo-terminal of 80x24, TERM=xterm-256color,
// temp HOME and a temp git project "p" with a package.json) and compares the
// terminal transcript, the exit code, the init log and every file written in
// the project and HOME with what Node did.
//
// Masks (both sides): the terminal's "\r\n" is "\n" (the pty's ONLCR; the
// program writes no other "\r"); HOME, the project and the package root are
// <HOME>, <CWD> and <REPO>; the init log's time stamp; ISO times inside
// files; the launcher line number in Node's EOF warning. The project's .git,
// HOME's logs dir and macOS's HOME/Library cache are not compared.

type b528Golden struct {
	Code       int    `json:"code"`
	Transcript string `json:"transcript"`
	Log        string `json:"log"`
	Files      string `json:"files"`
}

var b528Cases = []struct {
	name string
	keys []string
}{
	// accept every default
	{"defaults", []string{"\r", "\r", "\r", "\r", "\r", "\r", "\r", "\r", "\r", "\r", "\r", "\r", "\r", "\r"}},
	// another provider, "n" to a confirm, a bad level re-asked, levels typed
	{"provider", []string{"\r", "\r", "\r", "\r", "\x1b[B", "\r", "n", "\r", "\r", "\r", "\x15", "bad:", "\r", "\x15", "gpt-5.5", "\r", "\x7f\x7f\x7f\x7f\x7f\x7f\x7fgpt-5.4-mini:low", "\r", "\r", "\r", "\r", "\r", "\r", "\r"}},
	// harness choice (two harnesses, symlinked) and multimodel yes
	{"multi", []string{"\x1b[B", "\r", " ", "\x1b[B", " ", "\r", "\r", "\x1b[B", "\r", "\r", "\r", "y", "\r", "\r", "\x1b[B", "\r", "y", "y", "\r"}},
	// Ctrl-C at the harness prompt: exit 1, nothing written
	{"ctrlc", []string{"\r", "\x03"}},
	// Esc at a later prompt: exit 1, nothing written
	{"esc", []string{"\r", "\r", "\r", "\r", "\r", "\x1b"}},
	// the input ends (Ctrl-D) at a prompt: Node's exit 13 and warning
	{"eof", []string{"\r", "\x04", "\r", "\r"}},
}

var b528Row = regexp.MustCompile(`^(\S+) [-dlrwxLst]{10}`)

var b528MjsLine = regexp.MustCompile(`code-forge\.mjs:\d+`)

func b528Mask(text, home, cwd, repo string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = b502Normalize(text, home, cwd)
	text = strings.ReplaceAll(text, repo, "<REPO>")
	return b528MjsLine.ReplaceAllString(text, "code-forge.mjs:<N>")
}

// b528Tree is b527Tree without the project's .git and HOME/Library.
func b528Tree(t *testing.T, root, home, cwd string) string {
	t.Helper()
	// rows start with "<rel> <mode>"; contents follow on later lines
	out := []string{}
	skip := false
	for _, line := range strings.Split(b527Tree(t, root, home, cwd), "\n") {
		if m := b528Row.FindStringSubmatch(line); m != nil {
			rel := m[1]
			skip = rel == ".git" || strings.HasPrefix(rel, ".git/") || rel == "Library" || strings.HasPrefix(rel, "Library/")
		}
		if !skip {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func b528Collect(t *testing.T, home, cwd, repo string) (log, files string) {
	t.Helper()
	logs, _ := filepath.Glob(filepath.Join(home, ".code-forge", "logs", "init-*.log"))
	for _, file := range logs {
		data, _ := os.ReadFile(file)
		log += filepath.Base(b502Normalize(file, home, cwd)) + ":" + b528Mask(string(data), home, cwd, repo)
	}
	files = "project:\n" + b528Tree(t, cwd, home, cwd) + "\nhome:\n" + b528Tree(t, home, home, cwd)
	return log, strings.ReplaceAll(files, repo, "<REPO>")
}

// b528Keys hands one recorded keystroke per read, then the end of input.
type b528Keys struct{ keys []string }

func (k *b528Keys) Read(p []byte) (int, error) {
	if len(k.keys) == 0 {
		return 0, io.EOF
	}
	n := copy(p, k.keys[0])
	k.keys = k.keys[1:]
	return n, nil
}

// b528Unset clears env vars for the test (t.Setenv("X", "") still sets X).
func b528Unset(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		if old, set := os.LookupEnv(name); set {
			t.Cleanup(func() { _ = os.Setenv(name, old) })
			_ = os.Unsetenv(name)
		}
	}
}

// b528Setup makes the temp HOME and project "p" (git, package.json) and
// the environment of the recorded runs; it returns home, cwd and the repo.
func b528Setup(t *testing.T) (string, string, string) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not on PATH")
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(base, "p")
	if err := os.Mkdir(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	init := exec.Command(git, "init", "-q")
	init.Dir = cwd
	init.Env = []string{"HOME=" + home, "PATH=" + filepath.Dir(git)}
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(cwd, "package.json"), []byte(`{"name":"demo","scripts":{"test":"node --test"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := b458Repo(t)
	t.Setenv("HOME", home)
	t.Setenv("PATH", b527Env(t))
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-b528-node")
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	t.Setenv("CODE_FORGE_NATIVE", "1")
	b528Unset(t, "NO_COLOR", "FORCE_COLOR", "NODE_DISABLE_COLORS", "CI", "TEAMCITY_VERSION", "ACCESSIBLE", "NO_UNICODE",
		"CODE_FORGE_NO_ERROR_LOG", "CODE_FORGE_TMP_ROOT", "CLAUDECODE", "CLAUDE_CODE", "CURSOR_AGENT", "AI_AGENT", "CODEX_SANDBOX",
		"CODEX_SANDBOX_NETWORK_DISABLED", "SOLO_MCP_PATH", "JEV_KEY", "GEMINI_CLI", "COPILOT_AGENT")
	for _, name := range nativeStatusAgentVars {
		b528Unset(t, name)
	}
	savedTTY, savedNow, savedRoot, savedUpgrade, savedStderr := initStdinTTY, initNow, metaPackageRoot, upgradePackageRoot, routerLogStderrTTY
	initStdinTTY = func() bool { return true }
	initNow = func() time.Time { return time.Date(2026, 11, 12, 9, 8, 7, 6e6, time.UTC) }
	metaPackageRoot = func() (string, error) { return repo, nil }
	upgradePackageRoot = func() (string, bool) { return repo, true }
	routerLogStderrTTY = func() bool { return true }
	t.Cleanup(func() {
		initStdinTTY, initNow, metaPackageRoot, upgradePackageRoot, routerLogStderrTTY = savedTTY, savedNow, savedRoot, savedUpgrade, savedStderr
	})
	return home, cwd, repo
}

// b528Run runs `init args...` in Go on a fake 80x24 terminal fed with keys;
// stdout, stderr and the drawing share one file, as on the terminal. It
// returns the exit code and the transcript; a hand-over gives code 127.
func b528Run(t *testing.T, cwd string, keys []string, args ...string) (int, string) {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "tty-")
	if err != nil {
		t.Fatal(err)
	}
	savedOpen := initWizOpen
	initWizOpen = func() (initWizEnv, bool) {
		return initWizEnv{in: &b528Keys{keys: keys}, out: file, cols: 80, rows: 24, raw: func() func() { return func() {} }}, true
	}
	defer func() { initWizOpen = savedOpen }()
	here, _ := os.Getwd()
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(here)
	out, errOut := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = file, file
	code, _ := run(append([]string{"init"}, args...))
	os.Stdout, os.Stderr = out, errOut
	_ = file.Close()
	data, _ := os.ReadFile(file.Name())
	return code, string(data)
}

func b528GoldenFile(name string) string {
	return filepath.Join("testdata", "b528", name+".json")
}

// TestB528InitWizardMatchesNode: each recorded Node run, replayed in Go.
func TestB528InitWizardMatchesNode(t *testing.T) {
	for _, c := range b528Cases {
		t.Run(c.name, func(t *testing.T) {
			data, err := os.ReadFile(b528GoldenFile(c.name))
			if err != nil {
				t.Fatal(err)
			}
			var want b528Golden
			if err := json.Unmarshal(data, &want); err != nil {
				t.Fatal(err)
			}
			home, cwd, repo := b528Setup(t)
			code, transcript := b528Run(t, cwd, c.keys, "--skip-doctor", "--no-jev")
			if code == 127 {
				handed, _ := os.ReadFile(filepath.Join(home, ".code-forge", "native", "handovers.jsonl"))
				t.Fatalf("handed over to Node: %s", handed)
			}
			log, files := b528Collect(t, home, cwd, repo)
			got := b528Golden{Code: code, Transcript: b528Mask(transcript, home, cwd, repo), Log: log, Files: files}
			if got.Code != want.Code {
				t.Errorf("exit %d, Node %d", got.Code, want.Code)
			}
			if got.Transcript != want.Transcript {
				t.Errorf("transcript differs from Node:\n%s", b528Diff(want.Transcript, got.Transcript))
			}
			if got.Log != want.Log {
				t.Errorf("init log differs from Node:\n%s", b528Diff(want.Log, got.Log))
			}
			if got.Files != want.Files {
				t.Errorf("files differ from Node:\n%s", b528Diff(want.Files, got.Files))
			}
		})
	}
}

func b528Diff(want, got string) string {
	i := 0
	for i < len(want) && i < len(got) && want[i] == got[i] {
		i++
	}
	from := i - 200
	if from < 0 {
		from = 0
	}
	cut := func(s string) string {
		end := i + 200
		if end > len(s) {
			end = len(s)
		}
		if from > len(s) {
			return ""
		}
		return s[from:end]
	}
	return "at byte " + strconv.Itoa(i) + "\nnode: " + strconv.Quote(cut(want)) + "\ngo:   " + strconv.Quote(cut(got))
}

// The cases that keep Node: a narrow reason, decided before any prompt or
// write.
func TestB528InitWizardHandOvers(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		env    map[string]string
		reason string
	}{
		{"jev key lookup", []string{"--skip-doctor"}, nil, "init-jev-key"},
		{"doctor", []string{"--no-jev"}, nil, "init-doctor"},
		{"no colour", []string{"--skip-doctor", "--no-jev"}, map[string]string{"NO_COLOR": "1"}, "init-interactive"},
		{"other terminal", []string{"--skip-doctor", "--no-jev"}, map[string]string{"TERM": "vt100"}, "init-interactive"},
		{"tool installs", []string{"--skip-doctor", "--no-jev", "--yes-tool", "solo"}, nil, "init-wizard-tools"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home, cwd, _ := b528Setup(t)
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			before := b528Tree(t, cwd, home, cwd)
			code, transcript := b528Run(t, cwd, []string{"\r"}, c.args...)
			// the only output is the launcher's own line: no Node here
			if code != 127 || !strings.HasPrefix(transcript, "code-forge-native: node executable") || strings.Count(transcript, "\n") != 1 {
				t.Fatalf("code=%d transcript=%q; want the Node hand-over before any output", code, transcript)
			}
			data, _ := os.ReadFile(filepath.Join(home, ".code-forge", "native", "handovers.jsonl"))
			var row handoverRow
			if json.Unmarshal(bytes.TrimSpace(data), &row) != nil || row.Reason != c.reason || row.Verb != "init" {
				t.Fatalf("handover row %q; want reason %q", data, c.reason)
			}
			if _, err := os.Stat(filepath.Join(home, ".code-forge", "logs")); err == nil {
				t.Fatal("Go wrote logs before the hand-over")
			}
			if after := b528Tree(t, cwd, home, cwd); after != before {
				t.Fatalf("Go wrote into the project before the hand-over:\n%s", after)
			}
			// HOME holds only the hand-over log (.code-forge/native is not listed)
			if after := b528Tree(t, home, home, cwd); after != ".code-forge drwx------" {
				t.Fatalf("Go wrote into HOME before the hand-over:\n%s", after)
			}
		})
	}
}

// TestB528WriteNodeGoldens turns recorded Node pty runs into the goldens
// above. It runs only with B528_NODE_RUNS=<dir> holding n-<case>/{h,p} and
// B528_NODE_OUT=<dir> holding n-<case>.out (drive.py output: the raw pty bytes, then "\nEXIT <code>\n").
func TestB528WriteNodeGoldens(t *testing.T) {
	dir := os.Getenv("B528_NODE_RUNS")
	if dir == "" {
		t.Skip("B528_NODE_RUNS is not set")
	}
	repo := b458Repo(t)
	exitLine := regexp.MustCompile(`\nEXIT (\d+)\n$`)
	_ = os.MkdirAll(filepath.Join("testdata", "b528"), 0o755)
	for _, c := range b528Cases {
		home, cwd := filepath.Join(dir, "n-"+c.name, "h"), filepath.Join(dir, "n-"+c.name, "p")
		raw, err := os.ReadFile(filepath.Join(os.Getenv("B528_NODE_OUT"), "n-"+c.name+".out"))
		if err != nil {
			t.Fatal(err)
		}
		m := exitLine.FindSubmatchIndex(raw)
		if m == nil {
			t.Fatalf("%s: no exit line", c.name)
		}
		code, _ := strconv.Atoi(string(raw[m[2]:m[3]]))
		log, files := b528Collect(t, home, cwd, repo)
		g := b528Golden{Code: code, Transcript: b528Mask(string(raw[:m[0]]), home, cwd, repo), Log: log, Files: files}
		data, _ := json.MarshalIndent(g, "", "  ")
		if err := os.WriteFile(b528GoldenFile(c.name), append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
