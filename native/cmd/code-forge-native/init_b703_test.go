package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/config"
)

// B703: the init wizard cases Go used to hand to Node, recorded from Node
// on a real pseudo-terminal (b703RunPTY, 80x24) and replayed in Go with the
// B528 harness. Masks are B528's, plus <BASE> for the folder that holds a
// copied package (its own path stays as Node printed it).

type b703Case struct {
	name string
	args []string
	keys []string
	env  map[string]string
	// pkg runs the launcher from a copied package whose path has an "@"
	// and a space, like a real npm install under @codedology
	pkg bool
	// setup adds files before the run (HOME, the project, the PATH folder)
	setup func(t *testing.T, home, cwd, bin string)
}

var b703Enter14 = []string{"\r", "\r", "\r", "\r", "\r", "\r", "\r", "\r", "\r", "\r", "\r", "\r", "\r", "\r"}

var b703Cases = []b703Case{
	// terminals Node draws without colours or with ASCII symbols
	{name: "vt100", args: []string{"--skip-doctor", "--no-jev"}, keys: b703Enter14, env: map[string]string{"TERM": "vt100"}},
	{name: "no-color", args: []string{"--skip-doctor", "--no-jev"}, keys: b703Enter14, env: map[string]string{"NO_COLOR": "1"}},
	{name: "linux", args: []string{"--skip-doctor", "--no-jev"}, keys: b703Enter14, env: map[string]string{"TERM": "linux"}},
	{name: "tmux", args: []string{"--skip-doctor", "--no-jev"}, keys: b703Enter14, env: map[string]string{"TERM": "tmux"}},
	// a broken models cache is skipped
	{name: "models-json", args: []string{"--skip-doctor", "--no-jev"}, keys: b703Enter14, setup: func(t *testing.T, home, _, _ string) {
		b703Write(t, filepath.Join(home, ".codex", "models_cache.json"), "{bad", 0o644)
	}},
	// recommended tools with a yes for codex: npm installs it
	{name: "tools", args: []string{"--skip-doctor", "--no-jev", "--tools", "recommended", "--yes-tool", "codex"}, keys: b703Enter14, setup: b703FakeNpm},
	// the input ends at a prompt, the launcher under an npm-like path
	{name: "eof-path", args: []string{"--skip-doctor", "--no-jev"}, keys: []string{"\r", "\x04", "\r", "\r"}, pkg: true},
}

func b703Write(t *testing.T, file, text string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
}

// b703FakeNpm puts an npm on PATH that says what it was asked and
// installs a codex that prints a version.
func b703FakeNpm(t *testing.T, _, _, bin string) {
	b703Write(t, filepath.Join(bin, "npm"), "#!/bin/sh\necho \"fake npm $*\"\nprintf '#!/bin/sh\\necho codex-cli 9.9.9\\n' > \""+bin+"/codex\"\n/bin/chmod 755 \""+bin+"/codex\"\n", 0o755)
}

// b703Prep is b528Setup plus the case's package copy and files; it returns
// home, cwd, the repo, the package root and the <BASE> folder ("" without pkg).
func b703Prep(t *testing.T, c b703Case) (home, cwd, repo, root, base string) {
	t.Helper()
	home, cwd, repo = b528Setup(t)
	// the recorded Node runs saw none of these (getColorDepth reads them)
	b528Unset(t, "TERM_PROGRAM", "TERM_PROGRAM_VERSION", "COLORTERM", "TMUX", "TF_BUILD", "AGENT_NAME", "CI_NAME")
	for _, sign := range keysCISigns {
		b528Unset(t, sign)
	}
	root = repo
	if c.pkg {
		var err error
		if base, err = filepath.EvalSymlinks(t.TempDir()); err != nil {
			t.Fatal(err)
		}
		root = filepath.Join(base, "@codedology", "code forge")
		if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"package.json"} {
			data, err := os.ReadFile(filepath.Join(repo, name))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, name), data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(filepath.Join(repo, "src"), filepath.Join(root, "src")); err != nil {
			t.Fatal(err)
		}
		// the skill folder is a real copy: Go links skills only from one
		err = filepath.Walk(filepath.Join(repo, "skill"), func(file string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(repo, file)
			if info.IsDir() {
				return os.MkdirAll(filepath.Join(root, rel), 0o755)
			}
			data, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(root, rel), data, info.Mode().Perm())
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for k, v := range c.env {
		t.Setenv(k, v)
	}
	if c.setup != nil {
		c.setup(t, home, cwd, os.Getenv("PATH"))
	}
	return home, cwd, repo, root, base
}

func b703Mask(text, home, cwd, repo, base string) string {
	if base != "" {
		text = strings.ReplaceAll(text, base, "<BASE>")
	}
	return b528Mask(text, home, cwd, repo)
}

func b703GoldenFile(name string) string {
	return filepath.Join("testdata", "b703", name+".json")
}

// TestB703RecordNode records each case from Node on a pseudo-terminal into
// testdata/b703. It runs only with B703_RECORD=1.
func TestB703RecordNode(t *testing.T) {
	b760NoNodeRecorder(t, "B703_RECORD")
	if os.Getenv("B703_RECORD") != "1" {
		t.Skip("B703_RECORD is not 1")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH")
	}
	only := os.Getenv("B703_CASE")
	_ = os.MkdirAll(filepath.Join("testdata", "b703"), 0o755)
	for _, c := range b703Cases {
		if only != "" && c.name != only {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			home, cwd, repo, root, base := b703Prep(t, c)
			env := []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "TERM=" + os.Getenv("TERM"), "CODE_FORGE_NATIVE=0"}
			for k, v := range c.env {
				if k != "TERM" {
					env = append(env, k+"="+v)
				}
			}
			argv := append([]string{node, filepath.Join(root, "bin", "code-forge.mjs"), "init"}, c.args...)
			raw, code := b703RunPTY(t, b703PTYRun{argv: argv, env: env, dir: cwd, cols: 80, rows: 24, keys: c.keys, timeout: 3 * time.Minute})
			log, files := b528Collect(t, home, cwd, repo)
			g := b528Golden{Code: code, Transcript: b703Mask(string(raw), home, cwd, repo, base), Log: b703Mask(log, home, cwd, repo, base), Files: b703Mask(files, home, cwd, repo, base)}
			data, _ := json.MarshalIndent(g, "", "  ")
			if err := os.WriteFile(b703GoldenFile(c.name), append(data, '\n'), 0o644); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestB703InitWizardMatchesNode: each recorded Node run, replayed in Go:
// the screen, the exit code, the init log and every file written.
func TestB703InitWizardMatchesNode(t *testing.T) {
	for _, c := range b703Cases {
		t.Run(c.name, func(t *testing.T) {
			b552SealApps(t)
			if err := os.MkdirAll(filepath.Join(toolSystemApps, "Solo.app"), 0o755); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(b703GoldenFile(c.name))
			if err != nil {
				t.Fatal(err)
			}
			var want b528Golden
			if err := json.Unmarshal(data, &want); err != nil {
				t.Fatal(err)
			}
			home, cwd, repo, root, base := b703Prep(t, c)
			metaPackageRoot = func() (string, error) { return root, nil }
			upgradePackageRoot = func() (string, bool) { return root, true }
			code, transcript := b528Run(t, cwd, c.keys, c.args...)
			if code == 127 {
				handed, _ := os.ReadFile(filepath.Join(home, ".code-forge", "native", "handovers.jsonl"))
				t.Fatalf("handed over to Node: %s", handed)
			}
			log, files := b528Collect(t, home, cwd, repo)
			got := b528Golden{Code: code, Transcript: b703Mask(transcript, home, cwd, repo, base), Log: b703Mask(log, home, cwd, repo, base), Files: b703Mask(files, home, cwd, repo, base)}
			b989WizardCheck(t, b703GoldenFile(c.name), want, got)
		})
	}
}

// TestB703InitWizardHandOvers: what the wizard refuses in Go (it handed over
// to Node before B690a), decided
// before any prompt: a models cache Node cannot write (its refresh fails
// with a stack after the prompts) and a hint state Go cannot judge.
func TestB703InitWizardHandOvers(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(t *testing.T, home string)
		reason string
	}{
		{"models cache not writable", func(t *testing.T, home string) {
			if err := os.MkdirAll(config.UserModelsCachePath(home), 0o755); err != nil {
				t.Fatal(err)
			}
		}, "init-models"},
		{"hint state", func(t *testing.T, home string) {
			b703Write(t, filepath.Join(home, ".code-forge", "logs", "hints.json"), `{"0123456789ab":"yesterday"}`, 0o644)
		}, "router-hint-state"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home, cwd, _ := b528Setup(t)
			c.setup(t, home)
			before := b528Tree(t, cwd, home, cwd)
			code, transcript := b528Run(t, cwd, []string{"\r"}, "--skip-doctor", "--no-jev")
			// B690a (B756): the case is refused in Go, before any output.
			if code != 1 || !b690aIsRefusal(transcript) {
				t.Fatalf("code=%d transcript=%q; want the Go refusal before any output", code, transcript)
			}
			data, _ := os.ReadFile(filepath.Join(home, ".code-forge", "native", "handovers.jsonl"))
			var row handoverRow
			if json.Unmarshal(bytes.TrimSpace(data), &row) != nil || row.Reason != c.reason || row.Verb != "init" {
				t.Fatalf("handover row %q; want reason %q", data, c.reason)
			}
			if after := b528Tree(t, cwd, home, cwd); after != before {
				t.Fatalf("Go wrote into the project before the refusal:\n%s", after)
			}
		})
	}
}
