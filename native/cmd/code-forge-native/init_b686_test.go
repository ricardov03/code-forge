package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// B686: `init` reads and rewrites a .code-forge.yml and a user config that
// hold YAML anchors, aliases, << keys and standard tags in Go, byte-equal to
// Node's runInit on the same temp HOME and project (exit, stdout, stderr,
// the init log and every file written).

// b686Setup writes the project config and the user config (empty: none).
func b686Setup(config, user string) (func(cwd string), func(home string)) {
	project := func(cwd string) {
		if config != "" {
			_ = os.WriteFile(filepath.Join(cwd, ".code-forge.yml"), []byte(config), 0o644)
		}
	}
	home := func(home string) {
		if user != "" {
			_ = os.MkdirAll(filepath.Join(home, ".code-forge"), 0o755)
			_ = os.WriteFile(filepath.Join(home, ".code-forge", "config.yml"), []byte(user), 0o644)
		}
	}
	return project, home
}

func b686Go(t *testing.T, args []string, config, user string) b527Side {
	t.Helper()
	project, homeSetup := b686Setup(config, user)
	home, cwd, bin := t.TempDir(), b527Project(t, project), b527Env(t)
	homeSetup(home)
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin)
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-b686-node")
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

func b686Node(t *testing.T, args []string, config, user string) b527Side {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH")
	}
	project, homeSetup := b686Setup(config, user)
	home, cwd, bin := t.TempDir(), b527Project(t, project), b527Env(t)
	homeSetup(home)
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

// b686Shapes are the configs B686 serves; each must match Node. want is a
// line the written files must hold (the anchors yaml writes).
var b686Shapes = []struct {
	name, config, user string
	want               []string
}{
	{"shared threshold band", "version: 1\nthresholds:\n  default: &band\n    act: 0.8\n    check: 0.6\n  review: *band\n", "",
		[]string{"  default: &a1\n    act: 0.8\n    check: 0.6\n  review: *a1\n"}},
	{"shared list and anchor order", "version: 1\nknown_extra:\n  claude: &m [a, b]\n  codex: &n [c]\n  grok: *n\n  cursor: *m\n", "",
		[]string{"  claude: &a2\n    - a\n    - b\n  codex: &a1\n    - c\n  grok: *a1\n  cursor: *a2\n"}},
	{"merge key kept as a key", "version: 1\nknown_extra:\n  claude: &m [a]\n  <<: *m\n", "",
		[]string{"  claude: &a1\n    - a\n  <<: *a1\n"}},
	{"scalar alias", "version: 1\nthresholds:\n  default: {act: &v 0.75, check: *v}\n", "", []string{"  default:\n    act: 0.75\n    check: 0.75\n"}},
	{"merged object loses its anchor", "version: 1\nproof:\n  tiers:\n    high:\n      paths: &p [src/auth]\nknown_extra:\n  claude: *p\n", "",
		[]string{"known_extra:\n  claude:\n    - src/auth\n"}},
	{"standard tags", "version: !!int 1\nengine: !!str auto\nreview:\n  workers: !!int 2\n  exclude: !!seq [!!str 12, !!str true, a]\nthresholds: !!map\n  default: {act: !!float 0.75}\nknown_extra:\n  claude: !!seq [!!str x]\ngates:\n  test: !!null\n  lint: !!null ~\n", "", nil},
	{"user config anchors", "", "tools: none\nprofiles:\n  base: &b\n    model: x\n  fast: *b\n", []string{"  base: &a1\n    model: x\n  fast: *a1\n"}},
	{"user config tags and merge key", "", "tools: !!str none\nbase: &b {model: x}\nmine:\n  <<: *b\n  more: !!int 3\n", []string{"base: &a1\n  model: x\nmine:\n  <<: *a1\n  more: 3\n"}},
	{"user config repeated key", "", "tools: none\ntools: recommended\nkeep: 1\n", nil},
	{"user config too many aliases", "", "a: &a {k: v}\nb: &b {a1: *a, a2: *a, a3: *a, a4: *a, a5: *a, a6: *a, a7: *a, a8: *a, a9: *a, a10: *a, a11: *a}\nc: {b1: *b, b2: *b, b3: *b, b4: *b, b5: *b, b6: *b, b7: *b, b8: *b, b9: *b, b10: *b}\n", []string{".code-forge/config.yml -rw-r--r--\ntools: current\n"}},
}

// The shapes the old code handed over now run in Go and match Node.
func TestB686InitYAMLConfigsMatchNode(t *testing.T) {
	args := []string{"--no-interaction", "--skip-doctor", "--no-jev"}
	for _, c := range b686Shapes {
		t.Run(c.name, func(t *testing.T) {
			goSide := b686Go(t, args, c.config, c.user)
			nodeSide := b686Node(t, args, c.config, c.user)
			if goSide != nodeSide {
				t.Fatalf("go:   %#v\nnode: %#v", goSide, nodeSide)
			}
			if goSide.code == 127 || goSide.log == "" {
				t.Fatalf("not served in Go: %#v", goSide)
			}
			for _, want := range c.want {
				if !strings.Contains(goSide.files, want) {
					t.Fatalf("files lack %q:\n%s", want, goSide.files)
				}
			}
		})
	}
}

// A config whose anchors survive and whose values the setup leaves as they
// are: unchanged, not rewritten (its anchors, comments and tags stay).
func TestB686InitAnchoredConfigUnchanged(t *testing.T) {
	args := []string{"--no-interaction", "--skip-doctor", "--no-jev"}
	first := b686Go(t, args, "version: 1\nthresholds:\n  default: &band {act: 0.8}\n  review: *band\n", "")
	if first.code != 0 {
		t.Fatalf("first run: %#v", first)
	}
	start := strings.Index(first.files, ".code-forge.yml -rw-r--r--\n")
	end := strings.Index(first.files[start:], "\nhome:")
	written := strings.TrimPrefix(first.files[start:start+end], ".code-forge.yml -rw-r--r--\n")
	again := "# reviewed\n" + written + "\n"
	goSide := b686Go(t, args, again, "")
	nodeSide := b686Node(t, args, again, "")
	if goSide != nodeSide {
		t.Fatalf("go:   %#v\nnode: %#v", goSide, nodeSide)
	}
	if !strings.Contains(goSide.log, ".code-forge.yml: unchanged") || !strings.Contains(goSide.files, "# reviewed\n") || !strings.Contains(goSide.files, "*a1") {
		t.Fatalf("rerun: %#v", goSide)
	}
}

// The YAML Go still cannot read like Node keeps its hand-over, before any
// write.
func TestB686InitYAMLHandOvers(t *testing.T) {
	repo := b458Repo(t)
	cases := []struct {
		name, config, user, reason string
	}{
		{"custom tag", "version: 1\nengine: !custom auto\n", "", "init-config-shape"},
		{"tag that does not fit", "version: 1\nreview:\n  workers: !!int two\n", "", "init-config-shape"},
		{"binary tag", "version: 1\nknown_extra:\n  a: !!binary aGk=\n", "", "init-config-shape"},
		{"alias to an empty map", "version: 1\nthresholds:\n  default: &e {}\n  review: *e\n", "", "init-config-shape"},
		{"user directive", "", "%YAML 1.2\n---\ntools: none\n", "init-user-config"},
		{"anchor on a key", "version: 1\n&k engine: auto\n", "", "init-config-shape"},
		{"user custom tag", "", "tools: !custom none\n", "init-user-config"},
		{"user syntax error", "", "a: [1\n", "init-user-config"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			project, homeSetup := b686Setup(c.config, c.user)
			cwd := b527Project(t, project)
			homeSetup(home)
			t.Setenv("HOME", home)
			t.Setenv("PATH", b527Env(t))
			t.Setenv("CODE_FORGE_NODE", "/nonexistent-b686-node")
			t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
			t.Setenv("CODE_FORGE_NATIVE", "1")
			for _, name := range []string{"CLAUDECODE", "CLAUDE_CODE", "CURSOR_AGENT", "AI_AGENT", "CODEX_SANDBOX", "CODEX_SANDBOX_NETWORK_DISABLED", "SOLO_MCP_PATH"} {
				t.Setenv(name, "")
			}
			savedTTY, savedUpgrade, savedMeta := initStdinTTY, upgradePackageRoot, metaPackageRoot
			initStdinTTY = func() bool { return false }
			upgradePackageRoot = func() (string, bool) { return repo, true }
			metaPackageRoot = func() (string, error) { return repo, nil }
			defer func() { initStdinTTY, upgradePackageRoot, metaPackageRoot = savedTTY, savedUpgrade, savedMeta }()
			before := b527Tree(t, cwd, home, cwd)
			here, _ := os.Getwd()
			_ = os.Chdir(cwd)
			code, _ := run([]string{"init", "--no-interaction", "--skip-doctor", "--no-jev"})
			_ = os.Chdir(here)
			if code != 127 {
				t.Fatalf("code=%d; want the Node hand-over", code)
			}
			data, _ := os.ReadFile(filepath.Join(home, ".code-forge", "native", "handovers.jsonl"))
			var row handoverRow
			if json.Unmarshal(bytes.TrimSpace(data), &row) != nil || row.Reason != c.reason || row.Verb != "init" {
				t.Fatalf("handover row %q; want reason %q", data, c.reason)
			}
			if after := b527Tree(t, cwd, home, cwd); after != before {
				t.Fatalf("Go wrote before the hand-over:\n%s", after)
			}
		})
	}
}
