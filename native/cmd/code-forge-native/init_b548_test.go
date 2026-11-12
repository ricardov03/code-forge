package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// B548: the init steps after the write in Go (the quick doctor, the tools
// step, the models step, the Jev key from the environment or the file key
// store), compared with Node's init on the same temp HOME, project, fake
// provider CLIs and temp CODE_FORGE_TMP_ROOT. Masked: epoch-millisecond
// numbers (13 digits, the 24 h key expiry) and, as in B527, ISO times and the
// init log's file-name stamp. No real keychain is read: every compared run
// sets CODE_FORGE_KEY_JEV or CODE_FORGE_KEY_BACKEND=file.

var b548Millis = regexp.MustCompile(`\b\d{13}\b`)

type b548Case struct {
	name  string
	args  []string
	env   map[string]string
	bins  map[string]string // fake commands on PATH: name → shell body
	home  func(home string)
	setup func(cwd string)
}

// b548Bin is b527Env plus the fake provider and installer commands.
func b548Bin(t *testing.T, bins map[string]string) string {
	t.Helper()
	dir := b527Env(t)
	for name, body := range bins {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func b548Mask(side b527Side) b527Side {
	for _, p := range []*string{&side.stdout, &side.stderr, &side.log, &side.files} {
		*p = b527ISO.ReplaceAllString(b548Millis.ReplaceAllString(*p, "<MS>"), "<ISO>")
	}
	return side
}

func b548Home(t *testing.T, c b548Case) (string, string, string) {
	t.Helper()
	home := t.TempDir()
	if c.home != nil {
		c.home(home)
	}
	cwd := b527Project(t, c.setup)
	tmpRoot := filepath.Join(filepath.Dir(cwd), "tmproot")
	if err := os.Mkdir(tmpRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	return home, cwd, tmpRoot
}

func b548Go(t *testing.T, c b548Case) b527Side {
	t.Helper()
	home, cwd, tmpRoot := b548Home(t, c)
	t.Setenv("HOME", home)
	t.Setenv("PATH", b548Bin(t, c.bins))
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-b548-node")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "")
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "0")
	t.Setenv("CODE_FORGE_TMP_ROOT", tmpRoot)
	t.Setenv("CODE_FORGE_NATIVE", "1")
	for _, name := range []string{"CLAUDECODE", "CLAUDE_CODE", "CURSOR_AGENT", "AI_AGENT", "CODEX_SANDBOX", "CODEX_SANDBOX_NETWORK_DISABLED", "SOLO_MCP_PATH", "JEV_KEY", "CODE_FORGE_KEY_JEV", "CODE_FORGE_KEY_BACKEND"} {
		t.Setenv(name, "")
	}
	for k, v := range c.env {
		t.Setenv(k, v)
	}
	savedTTY, savedNow := initStdinTTY, initNow
	initStdinTTY = func() bool { return false }
	initNow = func() time.Time { return time.Now().UTC() }
	defer func() { initStdinTTY, initNow = savedTTY, savedNow }()
	repo := b458Repo(t)
	savedRoot, savedUpgrade := metaPackageRoot, upgradePackageRoot
	metaPackageRoot = func() (string, error) { return repo, nil }
	upgradePackageRoot = func() (string, bool) { return repo, true }
	defer func() { metaPackageRoot, upgradePackageRoot = savedRoot, savedUpgrade }()
	here, _ := os.Getwd()
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(here)
	code, stdout, stderr := runCaptured(t, append([]string{"init"}, c.args...)...)
	side := b527Side{code: code, stdout: stdout, stderr: stderr}
	b527Finish(t, &side, home, cwd)
	return b548Mask(side)
}

func b548Node(t *testing.T, c b548Case) b527Side {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH")
	}
	if c.env["CODE_FORGE_KEY_JEV"] == "" && c.env["CODE_FORGE_KEY_BACKEND"] != "file" {
		t.Fatal("a Node golden must not read the real keychain")
	}
	home, cwd, tmpRoot := b548Home(t, c)
	cmd := exec.Command(node, append([]string{filepath.Join(b458Repo(t), "bin", "code-forge.mjs"), "init"}, c.args...)...)
	cmd.Dir = cwd
	cmd.Env = []string{"PATH=" + b548Bin(t, c.bins), "HOME=" + home, "TMPDIR=" + os.TempDir(), "CODE_FORGE_TMP_ROOT=" + tmpRoot, "CODE_FORGE_NATIVE=0", "CODE_FORGE_HANDOVER_LOG=0", "NO_COLOR=1"}
	for k, v := range c.env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		code = exit.ExitCode()
	}
	side := b527Side{code: code, stdout: stdout.String(), stderr: stderr.String()}
	b527Finish(t, &side, home, cwd)
	return b548Mask(side)
}

func b548Write(rel, text string) func(string) {
	return func(root string) {
		file := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(file), 0o755)
		_ = os.WriteFile(file, []byte(text), 0o600)
	}
}

func b548Both(fns ...func(string)) func(string) {
	return func(root string) {
		for _, fn := range fns {
			fn(root)
		}
	}
}

var b548File = map[string]string{"CODE_FORGE_KEY_BACKEND": "file"}

// b548Shapes are the init runs with the steps after the write that Go now
// serves; each must match Node byte for byte, and wantDoctor is the doctor
// summary Go's JSON carries.
var b548Shapes = []struct {
	b548Case
	code   int
	expect []string
}{
	{b548Case{name: "all defaults, jev from env, doctor", args: []string{"--no-interaction"}, env: map[string]string{"CODE_FORGE_KEY_JEV": "sk-b548-env-secret"}}, 0, []string{`"doctor":{"ok":true`, "doctor OK"}},
	{b548Case{name: "all defaults, file store empty", args: []string{"--no-interaction"}, env: b548File}, 2, []string{"missing required answers"}},
	{b548Case{name: "no jev, doctor", args: []string{"--no-interaction", "--no-jev"}, env: b548File}, 0, []string{`"doctor":{"ok":true`}},
	{b548Case{name: "jev from env, skip doctor", args: []string{"--no-interaction", "--skip-doctor"}, env: map[string]string{"CODE_FORGE_KEY_JEV": "sk-b548-env-secret"}}, 0, []string{"env:CODE_FORGE_KEY_JEV"}},
	{b548Case{name: "jev from file store", args: []string{"--no-interaction"}, env: b548File, home: b548Write(".code-forge/store/jev.key", `{"v":"sk-b548-file-secret","exp":1999999999999}`+"\n")}, 0, []string{`"doctor":{"ok":true`}},
	{b548Case{name: "jev from file store, long expiry cut to 24 h", args: []string{"--no-interaction"}, env: b548File, home: b548Both(b548Write(".code-forge/store/jev.key", `{"v":"sk-b548-file-secret","exp":4102444800000}`), b548Write(".code-forge/store/index.json", `{"jev":{"backend":"file","exp":4102444800000,"note":"x"}}`))}, 0, []string{"jev.key"}},
	{b548Case{name: "jev from file store, plain value", args: []string{"--no-interaction", "--skip-doctor"}, env: b548File, home: b548Write(".code-forge/store/jev.key", "sk-b548-plain\n")}, 0, []string{`"ok":true`}},
	{b548Case{name: "jev from file store, expired", args: []string{"--no-interaction", "--skip-doctor"}, env: b548File, home: b548Both(b548Write(".code-forge/store/jev.key", `{"v":"sk-b548-old","exp":1000}`), b548Write(".code-forge/store/index.json", `{"jev":{"backend":"file","exp":1000},"other":{"backend":"file"}}`))}, 2, []string{"missing required answers"}},
	{b548Case{name: "doctor fails on a broken link record", args: []string{"--no-interaction", "--no-jev"}, env: b548File, home: b548Write(".code-forge/installs.json", `[{"harness":"claude","scope":"global","method":"symlink","source":"/nowhere/b548-src","target":"/nowhere/b548-target","linked_at":"2026-11-01T00:00:00.000Z"}]`)}, 1, []string{`"ok":false`, "doctor FAIL"}},
	{b548Case{name: "doctor with a provider CLI present", args: []string{"--no-interaction", "--no-jev"}, env: b548File, bins: map[string]string{"claude": "echo 1.0.0", "codex": "echo 0.1"}}, 0, []string{"doctor"}},
	{b548Case{name: "tools step, npm present", args: []string{"--no-interaction", "--no-jev", "--skip-doctor", "--tools", "recommended"}, env: b548File, bins: map[string]string{"npm": "exit 0", "claude": "exit 0"}}, 0, []string{"tool claude: present", "install with: npm install -g @openai/codex"}},
	{b548Case{name: "tools step, no npm", args: []string{"--no-interaction", "--no-jev", "--skip-doctor", "--tools", "recommended"}, env: b548File}, 0, []string{"install Node.js/npm first"}},
	{b548Case{name: "tools step, yes-tool installs", args: []string{"--no-interaction", "--no-jev", "--skip-doctor", "--tools", "recommended", "--yes-tool", "codex", "--yes-tool", "gemini"}, env: b548File, bins: map[string]string{"npm": `test "$3" = "@openai/codex" && exit 0; exit 3`}}, 0, []string{"tool codex: installed (npm install -g @openai/codex)", "tool gemini: install failed (exit 3)"}},
	{b548Case{name: "models step", args: []string{"--no-interaction", "--no-jev", "--skip-doctor", "--refresh-models"}, env: b548File, home: b548Both(b548Write(".codex/models_cache.json", `{"models":[{"slug":"gpt-b548-codex"},{"slug":"gpt-b548-mini"}]}`), b548Write(".claude/models.json", "not json"))}, 0, []string{"CLI cache(s) read"}},
	{b548Case{name: "models step, no caches", args: []string{"--no-interaction", "--no-jev", "--skip-doctor", "--refresh-models"}, env: b548File}, 0, []string{"models: 0 CLI cache(s) read"}},
	{b548Case{name: "all steps with doctor", args: []string{"--no-interaction", "--tools", "recommended", "--refresh-models"}, env: map[string]string{"CODE_FORGE_KEY_JEV": "sk-b548-env-secret", "CODE_FORGE_KEY_BACKEND": "file"}, bins: map[string]string{"npm": "exit 0"}, home: b548Write(".codex/models_cache.json", `{"models":[{"slug":"gpt-b548-codex"}]}`)}, 0, []string{"models: 1 CLI cache(s) read", "doctor"}},
}

// b548Diff names the first differing line of each field.
func b548Diff(t *testing.T, a, b b527Side) {
	t.Helper()
	for _, f := range [][3]string{{"stdout", a.stdout, b.stdout}, {"stderr", a.stderr, b.stderr}, {"log", a.log, b.log}, {"files", a.files, b.files}} {
		x, y := strings.Split(f[1], "\n"), strings.Split(f[2], "\n")
		for i := 0; i < len(x) || i < len(y); i++ {
			var l, r string
			if i < len(x) {
				l = x[i]
			}
			if i < len(y) {
				r = y[i]
			}
			if l != r {
				t.Logf("%s line %d:\n go:   %q\n node: %q", f[0], i, l, r)
				break
			}
		}
	}
}

func TestB548InitStepsMatchNode(t *testing.T) {
	for _, c := range b548Shapes {
		t.Run(c.name, func(t *testing.T) {
			goSide := b548Go(t, c.b548Case)
			nodeSide := b548Node(t, c.b548Case)
			if goSide != nodeSide {
				b548Diff(t, goSide, nodeSide)
				t.Fatalf("%q:\n go:   %#v\n node: %#v", c.args, goSide, nodeSide)
			}
			if goSide.code != c.code {
				t.Fatalf("exit %d, want %d: %#v", goSide.code, c.code, goSide)
			}
			all := goSide.stdout + goSide.stderr + goSide.log + goSide.files
			for _, want := range c.expect {
				if !strings.Contains(all, want) {
					t.Fatalf("no %q in %#v", want, goSide)
				}
			}
			for _, secret := range []string{"sk-b548-env-secret"} {
				if strings.Contains(goSide.stdout+goSide.stderr+goSide.log, secret) {
					t.Fatalf("secret printed: %#v", goSide)
				}
			}
		})
	}
}

// The default key chain reads the OS keychain: that stays with Node, decided
// before init writes anything. A models cache Go cannot write keeps its
// hand-over too.
func TestB548InitHandOvers(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		env    map[string]string
		home   func(string)
		reason string
	}{
		{"jev keychain", []string{"--skip-doctor"}, nil, nil, "init-jev-keychain"},
		{"jev keychain with doctor", nil, nil, nil, "init-jev-keychain"},
		{"jev store file not text", []string{"--skip-doctor"}, b548File, b548Write(".code-forge/store/jev.key", "\xff\xfe"), "init-jev-key"},
		{"jev store entry broken", []string{"--skip-doctor"}, b548File, b548Write(".code-forge/store/jev.key", `{"v":`), "init-jev-key"},
		{"models cache not writable", []string{"--skip-doctor", "--no-jev", "--refresh-models"}, b548File, b548Write(".code-forge/cache/models.json/x", "x"), "init-models"},
		{"doctor reads the keychain", []string{"--no-jev"}, nil, nil, "doctor-key-store"},
	}
	repo := b458Repo(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			// the dispatcher's own hand-over log lands under ~/.code-forge
			_ = os.Mkdir(filepath.Join(home, ".code-forge"), 0o700)
			if c.home != nil {
				c.home(home)
			}
			cwd := b527Project(t, nil)
			t.Setenv("HOME", home)
			t.Setenv("PATH", b527Env(t))
			t.Setenv("CODE_FORGE_NODE", "/nonexistent-b548-node")
			t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
			t.Setenv("CODE_FORGE_NATIVE", "1")
			t.Setenv("CODE_FORGE_TMP_ROOT", t.TempDir())
			for _, name := range []string{"CLAUDECODE", "CLAUDE_CODE", "CURSOR_AGENT", "AI_AGENT", "CODEX_SANDBOX", "CODEX_SANDBOX_NETWORK_DISABLED", "SOLO_MCP_PATH", "CODE_FORGE_KEY_JEV", "CODE_FORGE_KEY_BACKEND"} {
				t.Setenv(name, "")
			}
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			savedTTY, savedUpgrade, savedMeta := initStdinTTY, upgradePackageRoot, metaPackageRoot
			initStdinTTY = func() bool { return false }
			upgradePackageRoot = func() (string, bool) { return repo, true }
			metaPackageRoot = func() (string, error) { return repo, nil }
			defer func() { initStdinTTY, upgradePackageRoot, metaPackageRoot = savedTTY, savedUpgrade, savedMeta }()
			before := b527Tree(t, cwd, home, cwd) + b527Tree(t, home, home, cwd)
			here, _ := os.Getwd()
			_ = os.Chdir(cwd)
			code, _ := run(append([]string{"init", "--no-interaction"}, c.args...))
			_ = os.Chdir(here)
			if code != 127 {
				t.Fatalf("code=%d; want the Node hand-over", code)
			}
			data, _ := os.ReadFile(filepath.Join(home, ".code-forge", "native", "handovers.jsonl"))
			var row handoverRow
			if json.Unmarshal(bytes.TrimSpace(data), &row) != nil || row.Reason != c.reason || row.Verb != "init" {
				t.Fatalf("handover row %q; want reason %q", data, c.reason)
			}
			if _, err := os.Stat(filepath.Join(home, ".code-forge", "logs")); err == nil {
				t.Fatal("Go wrote logs before the hand-over")
			}
			if after := b527Tree(t, cwd, home, cwd) + b527Tree(t, home, home, cwd); after != before {
				t.Fatalf("Go wrote before the hand-over:\n%s", after)
			}
		})
	}
}
