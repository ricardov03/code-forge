package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/humanask"
)

// toolsPath is a PATH holding only fake executables named by files.
func toolsPath(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	return dir
}

// soloLine is the solo row: on macOS an installed Solo.app makes it present.
func soloLine(home string) (text, json string) {
	if runtime.GOOS == "darwin" {
		for _, p := range []string{"/Applications/Solo.app", filepath.Join(home, "Applications", "Solo.app")} {
			if _, err := os.Stat(p); err == nil {
				return "solo    installed  version unknown\n", `{"id":"solo","status":"installed","version":null,"install":null,"hint":null,"note":null}`
			}
		}
	}
	return "solo    missing    manual: https://soloterm.com (desktop app, then add its MCP entry)\n",
		`{"id":"solo","status":"missing","version":null,"install":null,"hint":"https://soloterm.com (desktop app, then add its MCP entry)","note":null}`
}

func TestB455ToolsListMatchesNode(t *testing.T) {
	home, _ := smallProject(t, "")
	toolsPath(t, map[string]string{
		"claude": "#!/bin/sh\necho 'claude 1.2.3 (Claude Code)'\n",
		"codex":  "#!/bin/sh\necho >&2 '  '\necho >&2 '  codex-cli 0.9  '\n",
		"gemini": "#!/bin/sh\nexit 3\n",
		"op":     "#!/bin/sh\nprintf 'op \\377\\342\\202 v2\\n'\n",
	})
	brew := "grok    missing    manual: https://x.ai/build\n"
	brewJSON := `{"id":"grok","status":"missing","version":null,"install":null,"hint":"https://x.ai/build","note":null}`
	if runtime.GOOS == "darwin" {
		brew = "grok    missing    manual: install Homebrew first: https://brew.sh\n"
		brewJSON = `{"id":"grok","status":"missing","version":null,"install":null,"hint":"install Homebrew first: https://brew.sh","note":null}`
	}
	solo, soloJSON := soloLine(home)
	note := `turn on 1Password app → Settings → Developer → "Integrate with 1Password CLI"`
	checkSmallCases(t, []smallCase{
		{[]string{"tools"}, 0, "claude  installed  claude 1.2.3 (Claude Code)\ncodex   installed  codex-cli 0.9\ngemini  installed  version unknown\n" + brew +
			"op      installed  op �� v2 — " + note + "\n" + solo, ""},
		{[]string{"tools", "--json"}, 0, `{"tools":[{"id":"claude","status":"installed","version":"claude 1.2.3 (Claude Code)","install":null,"hint":null,"note":null},` +
			`{"id":"codex","status":"installed","version":"codex-cli 0.9","install":null,"hint":null,"note":null},` +
			`{"id":"gemini","status":"installed","version":null,"install":null,"hint":null,"note":null},` + brewJSON + "," +
			`{"id":"op","status":"installed","version":"op �� v2","install":null,"hint":null,"note":` + jsQuote(note) + `},` + soloJSON + "]}\n", ""},
	})
	usage := func(message string) string { return "tools: " + message + "\n" + toolsUsage }
	checkSmallCases(t, []smallCase{
		{[]string{"tools", "x"}, 2, "", usage(`unknown subcommand "x"`)},
		{[]string{"tools", "--json", "--json"}, 2, "", usage("--json given more than once")},
		{[]string{"tools", "--json=1"}, 2, "", usage("--json takes no value")},
		{[]string{"tools", "--nope"}, 2, "", usage(`unknown flag "--nope"`)},
		{[]string{"tools", "install", "nope", "zz", "nope"}, 2, "", usage("unknown tool id(s): nope, zz — valid ids: claude, codex, gemini, grok, op, solo")},
		{[]string{"tools", "install", "--yes", "--yes"}, 2, "", usage("--yes given more than once")},
		{[]string{"tools", "install", "--dry-run=no"}, 2, "", usage("--dry-run takes no value")},
		{[]string{"tools", "-h"}, 0, toolsUsage, ""},
		{[]string{"tools", "install", "--help"}, 0, "usage: code-forge tools install [<id>...] [--yes] [--dry-run]\n" + strings.SplitN(toolsUsage, "\n", 2)[1], ""},
		{[]string{"tools", "install", "claude", "codex"}, 0, "claude  already installed\ncodex   already installed\n", ""},
	})
}

func TestB455ToolsInstallMatchesNode(t *testing.T) {
	smallProject(t, "")
	dir := toolsPath(t, map[string]string{
		"npm": "#!/bin/sh\ncase \"$3\" in\n" +
			"@openai/codex) printf 'added 1 package'; printf 'warn: \\377x' >&2; exit 0;;\n" +
			"@anthropic-ai/claude-code) echo 'npm ERR! nope' >&2; exit 4;;\n" +
			"@google/gemini-cli) kill -TERM $$;;\nesac\n",
	})
	checkSmallCases(t, []smallCase{
		{[]string{"tools", "install", "codex", "claude", "--dry-run"}, 0, "plan:\n  claude: npm install -g @anthropic-ai/claude-code\n  codex: npm install -g @openai/codex\ndry run: nothing installed\n", ""},
		{[]string{"tools", "install", "codex", "claude"}, 2, "plan:\n  claude: npm install -g @anthropic-ai/claude-code\n  codex: npm install -g @openai/codex\n",
			"tools: no terminal to confirm; nothing installed — pass --yes to install the plan above\n"},
		{[]string{"tools", "install", "gemini", "claude", "codex", "--yes"}, 1, "plan:\n  claude: npm install -g @anthropic-ai/claude-code\n  codex: npm install -g @openai/codex\n  gemini: npm install -g @google/gemini-cli\n" +
			"\n$ npm install -g @anthropic-ai/claude-code\n\n$ npm install -g @openai/codex\nadded 1 package\n\n$ npm install -g @google/gemini-cli\n\n" +
			"claude  failed (exit 4)\ncodex   installed (not on PATH yet — open a new shell)\ngemini  failed (signal SIGTERM)\n", "npm ERR! nope\nwarn: �x\n"},
	})
	// an install that puts the tool on PATH reads "installed"
	writeHomeFile(t, dir, "npm", "#!/bin/sh\nprintf '#!/bin/sh\\n' > '"+dir+"/codex'; /bin/chmod +x '"+dir+"/codex'\n")
	checkSmallCases(t, []smallCase{{[]string{"tools", "install", "codex", "--yes"}, 0, "plan:\n  codex: npm install -g @openai/codex\n\n$ npm install -g @openai/codex\n\ncodex   installed\n", ""}})
	// an npm that cannot start reads "did not start"
	writeHomeFile(t, dir, "npm", "#!/nonexistent-b455/sh\n")
	if err := os.Chmod(filepath.Join(dir, "npm"), 0o755); err != nil {
		t.Fatal(err)
	}
	checkSmallCases(t, []smallCase{{[]string{"tools", "install", "claude", "--yes"}, 1, "plan:\n  claude: npm install -g @anthropic-ai/claude-code\n\n$ npm install -g @anthropic-ai/claude-code\n\nclaude  failed (did not start)\n", ""}})
}

// A --version cut that splits a surrogate pair (a lone surrogate) stays with
// Node. (A terminal stdin with a piped stdout asks in Go since B698.)
func TestB455ToolsHandsOverConfirmAndSplitSurrogate(t *testing.T) {
	smallProject(t, "")
	toolsPath(t, map[string]string{"claude": "#!/bin/sh\necho '" + strings.Repeat("a", 119) + "😀'\n"})
	expectHandover(t, "tools-unsupported", "tools", "--json")
}

// toolsTerminal makes the run look like a terminal on both ends and answers
// the install question by driving the real clack prompt with keys. It
// returns the questions asked and the prompt bytes each key drew.
func toolsTerminal(t *testing.T, keys string, fail error) (asked *[]string, drawn func(string) string) {
	t.Helper()
	savedTTY, savedAsk, savedConfirm := toolsStdinTTY, toolsAskTTY, toolsConfirm
	t.Cleanup(func() { toolsStdinTTY, toolsAskTTY, toolsConfirm = savedTTY, savedAsk, savedConfirm })
	toolsStdinTTY = func() bool { return true }
	toolsAskTTY = func() bool { return true }
	list := []string{}
	asked = &list
	toolsConfirm = func(message string) (humanask.Result, error) {
		list = append(list, message)
		if fail != nil {
			return humanask.NoTTY, fail
		}
		return humanask.Ask(strings.NewReader(keys), os.Stdout, message, humanask.Options{Width: 80})
	}
	drawn = func(message string) string {
		var b bytes.Buffer
		_, _ = humanask.Ask(strings.NewReader(keys), &b, message, humanask.Options{Width: 80})
		return b.String()
	}
	return asked, drawn
}

// B682: at a terminal, plain `tools install` asks Node's one question
// (default no) after the plan; yes runs the same installs --yes runs.
func TestB682ToolsInstallAsksAtTerminalYesInstalls(t *testing.T) {
	smallProject(t, "")
	dir := toolsPath(t, map[string]string{
		"npm": "#!/bin/sh\necho \"$3\" >> \"${0%/*}/ran\"\nprintf 'added 1 package\\n'\n",
	})
	asked, drawn := toolsTerminal(t, "y", nil)
	plan := "plan:\n  claude: npm install -g @anthropic-ai/claude-code\n  codex: npm install -g @openai/codex\n"
	prompt := drawn("Run 2 install command(s)?")
	if !strings.Contains(prompt, "Run 2 install command(s)?") || !strings.Contains(prompt, "Yes") {
		t.Fatalf("prompt not drawn: %q", prompt)
	}
	checkSmallCases(t, []smallCase{{[]string{"tools", "install", "codex", "claude"}, 0, plan + prompt +
		"\n$ npm install -g @anthropic-ai/claude-code\nadded 1 package\n\n$ npm install -g @openai/codex\nadded 1 package\n\n" +
		"claude  installed (not on PATH yet — open a new shell)\ncodex   installed (not on PATH yet — open a new shell)\n", ""}})
	if len(*asked) != 1 || (*asked)[0] != "Run 2 install command(s)?" {
		t.Fatalf("asked %q, want one question", *asked)
	}
	ran, err := os.ReadFile(filepath.Join(dir, "ran"))
	if err != nil || string(ran) != "@anthropic-ai/claude-code\n@openai/codex\n" {
		t.Fatalf("installs run = %q (%v)", ran, err)
	}
}

// B682: every answer but yes (n, Enter on the default no, Escape, Ctrl-C, a
// closed input, a terminal that cannot be set up) prints Node's "cancelled —
// nothing installed", exits 1 and installs nothing.
func TestB682ToolsInstallAnswerNoCancels(t *testing.T) {
	plan := "plan:\n  claude: npm install -g @anthropic-ai/claude-code\n"
	for _, c := range []struct {
		name, keys string
		fail       error
	}{
		{"no", "n", nil}, {"enter-default-no", "\r", nil}, {"escape", "\x1b", nil},
		{"ctrl-c", "\x03", nil}, {"closed-input", "", nil}, {"toggle-then-no", " n", nil},
		{"setup-error", "", errors.New("ioctl failed")},
	} {
		t.Run(c.name, func(t *testing.T) {
			smallProject(t, "")
			dir := toolsPath(t, map[string]string{"npm": "#!/bin/sh\n: > \"${0%/*}/ran\"\n"})
			asked, drawn := toolsTerminal(t, c.keys, c.fail)
			prompt := ""
			if c.fail == nil {
				prompt = drawn("Run 1 install command(s)?")
			}
			checkSmallCases(t, []smallCase{{[]string{"tools", "install", "claude"}, 1, plan + prompt + "cancelled — nothing installed\n", ""}})
			if len(*asked) != 1 {
				t.Fatalf("asked %q, want one question", *asked)
			}
			if _, err := os.Stat(filepath.Join(dir, "ran")); err == nil {
				t.Fatal("a cancelled install ran npm")
			}
		})
	}
}

// B682: Enter after moving to Yes accepts, as clack does.
func TestB682ToolsInstallToggleEnterInstalls(t *testing.T) {
	smallProject(t, "")
	dir := toolsPath(t, map[string]string{"npm": "#!/bin/sh\n: > \"${0%/*}/ran\"\n"})
	_, drawn := toolsTerminal(t, "\x1b[D\r", nil)
	prompt := drawn("Run 1 install command(s)?")
	checkSmallCases(t, []smallCase{{[]string{"tools", "install", "claude"}, 0, "plan:\n  claude: npm install -g @anthropic-ai/claude-code\n" + prompt +
		"\n$ npm install -g @anthropic-ai/claude-code\n\nclaude  installed (not on PATH yet — open a new shell)\n", ""}})
	if _, err := os.Stat(filepath.Join(dir, "ran")); err != nil {
		t.Fatal("yes did not run npm")
	}
}

// B682: no question when --yes or --dry-run skips it, or nothing is left to
// run (all installed, or manual tools only).
func TestB682ToolsInstallNoQuestionWhenNothingToAsk(t *testing.T) {
	smallProject(t, "")
	toolsPath(t, map[string]string{"npm": "#!/bin/sh\nexit 0\n", "claude": "#!/bin/sh\nexit 0\n"})
	asked, _ := toolsTerminal(t, "y", nil)
	saved := toolOS
	toolOS = "linux"
	t.Cleanup(func() { toolOS = saved })
	checkSmallCases(t, []smallCase{
		{[]string{"tools", "install", "codex", "--dry-run"}, 0, "plan:\n  codex: npm install -g @openai/codex\ndry run: nothing installed\n", ""},
		{[]string{"tools", "install", "codex", "--yes"}, 0, "plan:\n  codex: npm install -g @openai/codex\n\n$ npm install -g @openai/codex\n\ncodex   installed (not on PATH yet — open a new shell)\n", ""},
		{[]string{"tools", "install", "claude"}, 0, "claude  already installed\n", ""},
		{[]string{"tools", "install", "grok"}, 0, "plan:\n  grok: manual — https://x.ai/build\n\ngrok    skipped: manual — https://x.ai/build\n", ""},
	})
	if len(*asked) != 0 {
		t.Fatalf("asked %q, want no question", *asked)
	}
}
