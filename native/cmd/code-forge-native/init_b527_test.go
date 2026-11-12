package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// B527: the non-interactive setup of `init` in Go, compared with Node's
// wizard on the same temp HOME and project: exit code, stdout (the agent
// JSON line), stderr, the init log and every file written (bytes, mode and
// symlink target) in the project and in HOME.

type b527Side struct {
	code                int
	stdout, stderr, log string
	files               string
}

var b527ISO = regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z`)

// b527Tree lists every entry under root (logs and the hand-over log
// aside): path, mode, link target, and the bytes (a hash for big files).
func b527Tree(t *testing.T, root, home, cwd string) string {
	t.Helper()
	var rows []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if rel == filepath.Join(".code-forge", "logs") || rel == filepath.Join(".code-forge", "native") {
			return filepath.SkipDir
		}
		info, _ := os.Lstat(p)
		row := rel + " " + info.Mode().String()
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			row += " -> " + target
		case info.Mode().IsRegular():
			data, _ := os.ReadFile(p)
			if len(data) > 4096 {
				sum := sha256.Sum256(data)
				row += " sha256:" + hex.EncodeToString(sum[:])
			} else {
				row += "\n" + b527ISO.ReplaceAllString(string(data), "<ISO>")
			}
		}
		rows = append(rows, row)
		return nil
	})
	sort.Strings(rows)
	return b502Normalize(strings.Join(rows, "\n"), home, cwd)
}

// b527Env is a PATH with node and git only, so no real harness CLI is found.
func b527Env(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"node", "git"} {
		if p, err := exec.LookPath(name); err == nil {
			_ = os.Symlink(p, filepath.Join(dir, name))
		}
	}
	return dir
}

// b527Project makes <tmp>/myproj (the same basename on both sides).
func b527Project(t *testing.T, setup func(cwd string)) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(base, "myproj")
	if err := os.Mkdir(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if setup != nil {
		setup(cwd)
	}
	return cwd
}

func b527Finish(t *testing.T, side *b527Side, home, cwd string) {
	t.Helper()
	logs, _ := filepath.Glob(filepath.Join(home, ".code-forge", "logs", "init-*.log"))
	for _, file := range logs {
		data, _ := os.ReadFile(file)
		side.log += filepath.Base(b502Normalize(file, home, cwd)) + ":" + b502Normalize(string(data), home, cwd)
	}
	side.stdout = b502Normalize(side.stdout, home, cwd)
	side.stderr = b502Normalize(side.stderr, home, cwd)
	side.files = "project:\n" + b527Tree(t, cwd, home, cwd) + "\nhome:\n" + b527Tree(t, home, home, cwd)
}

func b527Go(t *testing.T, args []string, setup func(cwd string)) b527Side {
	t.Helper()
	home, cwd, bin := t.TempDir(), b527Project(t, setup), b527Env(t)
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin)
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-b527-node")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "")
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "0")
	t.Setenv("CODE_FORGE_TMP_ROOT", "")
	t.Setenv("CODE_FORGE_NATIVE", "1")
	for _, name := range []string{"CLAUDECODE", "CLAUDE_CODE", "CURSOR_AGENT", "AI_AGENT", "CODEX_SANDBOX", "CODEX_SANDBOX_NETWORK_DISABLED", "SOLO_MCP_PATH", "JEV_KEY"} {
		t.Setenv(name, "")
	}
	savedTTY, savedNow := initStdinTTY, initNow
	initStdinTTY = func() bool { return false }
	initNow = func() time.Time { return time.Date(2026, 11, 12, 9, 8, 7, 6e6, time.UTC) }
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
	code, stdout, stderr := runCaptured(t, append([]string{"init"}, args...)...)
	side := b527Side{code: code, stdout: stdout, stderr: stderr}
	b527Finish(t, &side, home, cwd)
	return side
}

func b527Node(t *testing.T, args []string, setup func(cwd string)) b527Side {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH")
	}
	home, cwd, bin := t.TempDir(), b527Project(t, setup), b527Env(t)
	cmd := exec.Command(node, append([]string{filepath.Join(b458Repo(t), "bin", "code-forge.mjs"), "init"}, args...)...)
	cmd.Dir = cwd
	cmd.Env = []string{"PATH=" + bin, "HOME=" + home, "TMPDIR=" + os.TempDir(), "CODE_FORGE_NATIVE=0", "CODE_FORGE_HANDOVER_LOG=0", "NO_COLOR=1"}
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
	return side
}

func b527Files(files map[string]string) func(string) {
	return func(cwd string) {
		for rel, text := range files {
			file := filepath.Join(cwd, rel)
			_ = os.MkdirAll(filepath.Dir(file), 0o755)
			_ = os.WriteFile(file, []byte(text), 0o644)
		}
	}
}

// b527Shapes are the setup runs Go serves; every one must match Node.
var b527Shapes = []struct {
	name  string
	args  []string
	setup func(string)
}{
	{"empty project", []string{"--no-jev"}, nil},
	{"git node project", []string{"--no-jev"}, b527Files(map[string]string{".git/HEAD": "ref: refs/heads/main\n", "package.json": `{"name":"@acme/Web App","scripts":{"test":"vitest","lint":"eslint ."}}`, ".env": "A=1\n", "tsconfig.json": "{}"})},
	{"laravel project", []string{"--no-jev"}, b527Files(map[string]string{"composer.json": `{"name":"acme/shop"}`, "artisan": "", ".env": "", ".env.testing": ""})},
	{"go project", []string{"--no-jev"}, b527Files(map[string]string{"go.mod": "module x\n\ngo 1.22\n"})},
	{"existing config with comments, changed", []string{"--no-jev"}, b527Files(map[string]string{".code-forge.yml": "# team settings\nversion: 1\nengine: solo # pinned\nreview:\n  workers: 2\n  exclude: [a, b]\nkeys:\n  jev: env:OLD\n"})},
	{"existing config keeps its jev ref", nil, b527Files(map[string]string{".code-forge.yml": "version: 1\nkeys:\n  jev: env:TEAM_JEV\nsystem1:\n  fallback: rules\n"})},
	{"existing gates and proof", []string{"--no-jev"}, b527Files(map[string]string{"package.json": `{"name":"app"}`, ".code-forge.yml": "version: 1\ngates:\n  test: [npm, test]\n  lint:\n    argv: [npx, eslint, \"src dir\"]\n    env:\n      Z: \"1\"\n      A: \"2\"\nproof:\n  isolation: lock\n  tiers:\n    high:\n      paths: [src/auth]\n"})},
	{"jev env", []string{"--jev-env", "JEV_KEY"}, nil},
	{"jev op ref", []string{"--jev-ref", "op://Vault/Jev/credential"}, nil},
	{"provider and levels", []string{"--no-jev", "--provider", "openai", "--level", "L2=gpt-5.1-codex:high", "--level", "L0=small@xai"}, nil},
	{"gates and proof flags", []string{"--no-jev", "--gate", `test=make "unit tests"`, "--gate", "format=gofmt -l .", "--proof", "high=src/auth,db", "--proof", "isolation=lock", "--proof", "copy_untracked=.env.local"}, b527Files(map[string]string{"go.mod": "module x\n"})},
	{"multimodel", []string{"--no-jev", "--multimodel", "on", "--second-provider", "openai"}, nil},
	{"multimodel same provider", []string{"--no-jev", "--multimodel", "on", "--second-provider", "anthropic"}, nil},
	{"multimodel judge pinned", []string{"--no-jev", "--multimodel", "on", "--second-provider", "openai", "--level", "L3=grok@openai"}, nil},
	{"multimodel without second", []string{"--no-jev", "--multimodel", "on"}, nil},
	{"push and draft", []string{"--no-jev", "--coder-may-push", "yes", "--coder-may-open-draft-pr", "yes"}, nil},
	{"engine solo", []string{"--no-jev", "--engine", "solo"}, nil},
	{"solo project", []string{"--no-jev", "--solo-project", "42"}, nil},
	{"harness project symlink", []string{"--no-jev", "--harness", "claude,codex"}, b527Files(map[string]string{".git/HEAD": "x\n"})},
	{"harness global copy", []string{"--no-jev", "-g", "--copy", "--harness", "cursor"}, b527Files(map[string]string{".git/HEAD": "x\n"})},
	{"invalid result", []string{"--no-jev", "--level", "L1=same-model", "--level", "L2=same-model"}, b527Files(map[string]string{".code-forge.yml": "version: 1\n"})},
}

func TestB527InitSetupMatchesNode(t *testing.T) {
	for _, c := range b527Shapes {
		t.Run(c.name, func(t *testing.T) {
			args := append([]string{"--no-interaction", "--skip-doctor"}, c.args...)
			goSide := b527Go(t, args, c.setup)
			nodeSide := b527Node(t, args, c.setup)
			if goSide != nodeSide {
				t.Fatalf("%q:\n go:   %#v\n node: %#v", args, goSide, nodeSide)
			}
			want := map[string]int{"invalid result": 1, "multimodel same provider": 2, "multimodel judge pinned": 2, "multimodel without second": 2}[c.name]
			if goSide.code != want || goSide.log == "" {
				t.Fatalf("exit %d, want %d: %#v", goSide.code, want, goSide)
			}
			if strings.HasPrefix(c.name, "harness") && !strings.Contains(goSide.stdout, `"harnesses":["`) {
				t.Fatalf("no harness linked: %s", goSide.stdout)
			}
		})
	}
}

// A second run on the config the first run wrote: unchanged, not written.
func TestB527InitRerunUnchanged(t *testing.T) {
	args := []string{"--no-interaction", "--skip-doctor", "--no-jev"}
	setup := func(cwd string) {
		home := filepath.Join(filepath.Dir(cwd), "first-home")
		_ = os.Mkdir(home, 0o755)
		saved := os.Getenv("HOME")
		os.Setenv("HOME", home)
		t.Setenv("PATH", b527Env(t))
		for _, name := range []string{"CLAUDECODE", "CLAUDE_CODE", "CURSOR_AGENT", "AI_AGENT", "CODEX_SANDBOX", "CODEX_SANDBOX_NETWORK_DISABLED", "SOLO_MCP_PATH"} {
			t.Setenv(name, "")
		}
		savedTTY := initStdinTTY
		initStdinTTY = func() bool { return false }
		defer func() { initStdinTTY = savedTTY }()
		here, _ := os.Getwd()
		_ = os.Chdir(cwd)
		_, _, _ = runCaptured(t, append([]string{"init"}, args...)...)
		_ = os.Chdir(here)
		os.Setenv("HOME", saved)
		data, _ := os.ReadFile(filepath.Join(cwd, ".code-forge.yml"))
		_ = os.WriteFile(filepath.Join(cwd, ".code-forge.yml"), append([]byte("# reviewed by the team\n"), data...), 0o600)
	}
	goSide := b527Go(t, args, setup)
	nodeSide := b527Node(t, args, setup)
	if goSide != nodeSide {
		t.Fatalf("go:   %#v\n node: %#v", goSide, nodeSide)
	}
	if !strings.Contains(goSide.log, ".code-forge.yml: unchanged") || !strings.Contains(goSide.files, "# reviewed by the team") {
		t.Fatalf("rerun: %#v", goSide)
	}
}

// The common setup runs in Go: exit 0, the config written, no hand-over.
func TestB527InitSetupRunsInGo(t *testing.T) {
	side := b527Go(t, []string{"--no-interaction", "--skip-doctor", "--no-jev"}, nil)
	if side.code != 0 || !strings.Contains(side.stdout, `"ok":true`) || !strings.Contains(side.files, "myproj") && !strings.Contains(side.files, ".code-forge.yml -rw") {
		t.Fatalf("setup: %#v", side)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(side.stdout), &body); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(body["wrote"]) != "[<CWD>/.code-forge.yml <HOME>/.code-forge/config.yml]" {
		t.Fatalf("wrote: %v", body["wrote"])
	}
}

// The steps Go does not serve hand over with a narrow reason, before any
// write.
func TestB527InitHandOvers(t *testing.T) {
	repo := b458Repo(t)
	cases := []struct {
		name   string
		args   []string
		config string
		user   string
		noRoot bool
		reason string
	}{
		{"jev key", []string{"--skip-doctor"}, "", "", false, "init-jev-key"},
		{"jev item id", []string{"--skip-doctor", "--jev-ref", "abcdefghijklmnopqrstuvwxyz"}, "", "", false, "init-jev-op"},
		{"tools", []string{"--skip-doctor", "--no-jev", "--tools", "recommended"}, "", "", false, "init-tools"},
		{"models", []string{"--skip-doctor", "--no-jev", "--refresh-models"}, "", "", false, "init-models"},
		{"doctor", []string{"--no-jev"}, "", "", false, "init-doctor"},
		{"links", []string{"--skip-doctor", "--no-jev", "--harness", "claude"}, "", "", true, "init-links"},
		{"config anchor", []string{"--skip-doctor", "--no-jev"}, "version: 1\nx: &a [1]\ny: *a\n", "", false, "init-config-shape"},
		{"config gate shape", []string{"--skip-doctor", "--no-jev"}, "version: 1\ngates:\n  test: 5\n", "", false, "init-config-shape"},
		{"config sandbox", []string{"--skip-doctor", "--no-jev"}, "version: 1\ncoder:\n  sandbox:\n    add_dirs: [/x]\n", "", false, "init-config-check"},
		{"config unknown key", []string{"--skip-doctor", "--no-jev"}, "version: 1\ncustom: 1\n", "", false, "init-config-check"},
		{"user config", []string{"--skip-doctor", "--no-jev"}, "", "a: [1\n", false, "init-user-config"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			cwd := b527Project(t, nil)
			if c.config != "" {
				b502WriteConfig(c.config)(cwd)
			}
			if c.user != "" {
				_ = os.MkdirAll(filepath.Join(home, ".code-forge"), 0o755)
				_ = os.WriteFile(filepath.Join(home, ".code-forge", "config.yml"), []byte(c.user), 0o644)
			}
			t.Setenv("HOME", home)
			t.Setenv("PATH", b527Env(t))
			t.Setenv("CODE_FORGE_NODE", "/nonexistent-b527-node")
			t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
			t.Setenv("CODE_FORGE_NATIVE", "1")
			for _, name := range []string{"CLAUDECODE", "CLAUDE_CODE", "CURSOR_AGENT", "AI_AGENT", "CODEX_SANDBOX", "CODEX_SANDBOX_NETWORK_DISABLED", "SOLO_MCP_PATH"} {
				t.Setenv(name, "")
			}
			savedTTY, savedUpgrade, savedMeta := initStdinTTY, upgradePackageRoot, metaPackageRoot
			initStdinTTY = func() bool { return false }
			upgradePackageRoot = func() (string, bool) { return repo, !c.noRoot }
			metaPackageRoot = func() (string, error) { return repo, nil }
			defer func() { initStdinTTY, upgradePackageRoot, metaPackageRoot = savedTTY, savedUpgrade, savedMeta }()
			before := b527Tree(t, cwd, home, cwd)
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
			if after := b527Tree(t, cwd, home, cwd); after != before {
				t.Fatalf("Go wrote into the project before the hand-over:\n%s", after)
			}
		})
	}
}
