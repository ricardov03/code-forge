package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// B726: the init wizard with wide text in each field Go used to hand to
// Node (init-wizard-shape): levels, gates, proof lists and the Solo
// project, with CJK, emoji and their ZWJ, flag, keycap and skin tone
// sequences, combining marks (not in NFC too), full-width forms and zero
// width joiners. Recorded from Node on a pseudo-terminal (80x24), replayed
// in Go with the B703 harness.

const (
	// b726Wide mixes every kind of width clack measures
	b726Wide = "日本語 👩‍💻 café 🇯🇵 #️⃣ 👍🏽 한국어 é̂ ｱｲｳ ＡＢＣ x‍y"
	// b726Long wraps at 80 columns in the middle of wide characters
	b726Long = "長い名前の検査コマンド🧪テスト用の非常に長い行を折り返す👨‍👩‍👧‍👦家族と国旗🇯🇵🇰🇷と結合文字caféを含む"
)

var b726Cases = []b703Case{
	// an existing config's levels: the matrix in the confirm message, then
	// each level's text prompt pre-filled with it
	{name: "levels", args: []string{"--skip-doctor", "--no-jev"}, keys: b726Enter(20), setup: b726Levels},
	{name: "levels-edit", args: []string{"--skip-doctor", "--no-jev"}, keys: append(append(b726Enter(5), "\x1b[C"), b726Enter(20)...), setup: b726Levels},
	{name: "gates", args: []string{"--skip-doctor", "--no-jev",
		"--gate", "test=echo " + b726Wide,
		"--gate", "lint=printf '" + b726Long + "'",
		"--gate", "types=é̂ 한국어 👍🏽"}, keys: b703Enter14},
	{name: "proof", args: []string{"--skip-doctor", "--no-jev",
		"--proof", "link_dirs=データ,📁files,café,👩‍👩‍👧‍👦",
		"--proof", "copy_untracked=.env.日本," + b726Long}, keys: b703Enter14},
	// Customize now: each gate and proof value in a text prompt
	// Customize now: an existing config's gates and proof lists, each in a
	// text prompt pre-filled with it
	{name: "customize", args: []string{"--skip-doctor", "--no-jev"}, keys: append(append(b726Enter(10), "\x1b[B"), b726Enter(20)...), setup: func(t *testing.T, _, cwd, _ string) {
		b703Write(t, filepath.Join(cwd, ".code-forge.yml"), "version: 1\nprovider: anthropic\ngates:\n"+
			"  test: [echo, \""+b726Wide+"\"]\n  lint: [printf, \""+b726Long+"\"]\n  types: [\"e\u0301\u0302\", 한국어]\n  format: null\n"+
			"proof:\n  tiers:\n    high:\n      paths: [\"src/日本/**\", \"👩\u200d💻/*.go\"]\n  isolation: export\n"+
			"  export:\n    link_dirs: [データ, 📁files, \"cafe\u0301\"]\n    copy_untracked: [.env.日本, \""+b726Long+"\"]\n", 0o644)
	}},
	{name: "solo", args: []string{"--skip-doctor", "--no-jev"}, keys: b726Enter(20), env: map[string]string{"SOLO_MCP_PATH": "/opt/solo-mcp"}, setup: func(t *testing.T, home, _, _ string) {
		b703Write(t, filepath.Join(home, ".code-forge", "config.yml"), "solo:\n  project_id: \"プロジェクト 👍🏽 café #️⃣\"\n", 0o644)
	}},
}

func b726Enter(n int) []string {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = "\r"
	}
	return keys
}

// b726Levels writes a .code-forge.yml whose levels hold wide text.
func b726Levels(t *testing.T, _, cwd, _ string) {
	b703Write(t, filepath.Join(cwd, ".code-forge.yml"), "version: 1\nprovider: anthropic\nlevels:\n"+
		"  L0:\n    model: \"ｈａｉｋｕ e\u0301\u0302\"\n"+
		"  L1:\n    model: \"モデル👩\u200d💻-cafe\u0301\"\n    effort: high\n"+
		"  L2:\n    model: \"ｍｏｄｅｌ🇯🇵#\ufe0f\u20e3 👍🏽\"\n    effort: low\n"+
		"  L3:\n    model: \""+b726Long+"\"\n    effort: max\n", 0o644)
}

func b726GoldenFile(name string) string { return b726TestdataFile(name + ".json") }

// b726RecordWizard records each wizard case from Node into testdata/b726
// (TestB726RecordNode with B726_RECORD=1; B726_CASE picks one).
func b726RecordWizard(t *testing.T, only string) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH")
	}
	_ = os.MkdirAll(filepath.Join("testdata", "b726"), 0o755)
	for _, c := range b726Cases {
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
			if err := os.WriteFile(b726GoldenFile(c.name), append(data, '\n'), 0o644); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestB726InitWizardMatchesNode: each wide-text run, replayed in Go with no
// hand-over: the screen byte for byte, the exit code, the init log and
// every file written.
func TestB726InitWizardMatchesNode(t *testing.T) {
	for _, c := range b726Cases {
		t.Run(c.name, func(t *testing.T) {
			b552SealApps(t)
			if err := os.MkdirAll(filepath.Join(toolSystemApps, "Solo.app"), 0o755); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(b726GoldenFile(c.name))
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
			if got.Code != want.Code {
				t.Errorf("exit %d, Node %d", got.Code, want.Code)
			}
			if got.Transcript != want.Transcript {
				t.Errorf("transcript differs from Node:\n%s", b528Diff(want.Transcript, got.Transcript))
			}
			if got.Log != want.Log {
				t.Errorf("init log differs from Node:\n%s", b528Diff(want.Log, got.Log))
			}
			want.Files = b607LinkVersion(t, b552SkillRows(t, want.Files, ""))
			if got.Files != want.Files {
				t.Errorf("files differ from Node:\n%s", b528Diff(want.Files, got.Files))
			}
		})
	}
}

// TestB726InitWizardShapeHandOver: what init-wizard-shape still hands to
// Node, before any prompt and with nothing written: a level, Solo project
// or tools value whose type Node prints as JavaScript would (a number
// effort, a list as a level, a map as the Solo project id).
func TestB726InitWizardShapeHandOver(t *testing.T) {
	cases := []struct{ name, project, user string }{
		{"number effort", "version: 1\nprovider: anthropic\nlevels:\n  L1:\n    model: m\n    effort: 5\n", ""},
		{"level list", "version: 1\nprovider: anthropic\nlevels:\n  L2: [a, b]\n", ""},
		{"solo map", "", "solo:\n  project_id: {a: 1}\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home, cwd, _ := b528Setup(t)
			if c.project != "" {
				b703Write(t, filepath.Join(cwd, ".code-forge.yml"), c.project, 0o644)
			}
			if c.user != "" {
				b703Write(t, filepath.Join(home, ".code-forge", "config.yml"), c.user, 0o644)
			}
			t.Setenv("SOLO_MCP_PATH", "/opt/solo-mcp")
			before := b528Tree(t, cwd, home, cwd)
			code, transcript := b528Run(t, cwd, []string{"\r"}, "--skip-doctor", "--no-jev")
			if code != 127 || !strings.HasPrefix(transcript, "code-forge-native: node executable") || strings.Count(transcript, "\n") != 1 {
				t.Fatalf("code=%d transcript=%q; want the Node hand-over before any output", code, transcript)
			}
			data, _ := os.ReadFile(filepath.Join(home, ".code-forge", "native", "handovers.jsonl"))
			var row handoverRow
			if json.Unmarshal([]byte(strings.TrimSpace(string(data))), &row) != nil || row.Reason != "init-wizard-shape" || row.Verb != "init" {
				t.Fatalf("handover row %q; want init-wizard-shape", data)
			}
			if after := b528Tree(t, cwd, home, cwd); after != before {
				t.Fatalf("Go wrote into the project before the hand-over:\n%s", after)
			}
		})
	}
}
