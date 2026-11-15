package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/humanask"
)

// Node goldens (B698): CODE_FORGE_NATIVE=0 code-forge tools install claude
// run under expect with stdin a terminal and stdout redirected to a file,
// one key sent after the prompt drew, FORCE_COLOR unset, fake npm on PATH.
const (
	b698ToolsPrompt = "\u001b[?25l│\n◆  Run 1 install command(s)?\n│  ○ Yes / ● No\n└\n\u001b[1A\n\u001b[?25h\u001b[999D\u001b[4A\u001b[1B\u001b[J◇  Run 1 install command(s)?\n"
	b698ToolsNo     = "plan:\n  claude: npm install -g @anthropic-ai/claude-code\n" + b698ToolsPrompt + "│  No\ncancelled — nothing installed\n"
	b698ToolsYes    = "plan:\n  claude: npm install -g @anthropic-ai/claude-code\n" + b698ToolsPrompt + "│  Yes\n\n$ npm install -g @anthropic-ai/claude-code\nadded 1 package\n\nclaude  installed (not on PATH yet — open a new shell)\n"
)

// b698Piped makes stdin a terminal and stdout a pipe, and answers the piped
// prompt by driving the real clack frames with keys at ConfirmPiped's width
// and colour; the both-terminals prompt must not be used.
func b698Piped(t *testing.T, keys string) *[]string {
	t.Helper()
	savedTTY, savedAsk, savedConfirm, savedPiped := toolsStdinTTY, toolsAskTTY, toolsConfirm, toolsConfirmPiped
	t.Cleanup(func() {
		toolsStdinTTY, toolsAskTTY, toolsConfirm, toolsConfirmPiped = savedTTY, savedAsk, savedConfirm, savedPiped
	})
	toolsStdinTTY = func() bool { return true }
	toolsAskTTY = func() bool { return false }
	toolsConfirm = func(string) (humanask.Result, error) {
		t.Fatal("the terminal prompt ran with stdout piped")
		return humanask.NoTTY, nil
	}
	asked := []string{}
	toolsConfirmPiped = func(message string) (humanask.Result, error) {
		asked = append(asked, message)
		return humanask.Ask(strings.NewReader(keys), os.Stdout, message, humanask.Options{Width: 80, Color: humanask.PipedColor(os.LookupEnv)})
	}
	return &asked
}

// B698: stdin at a terminal with stdout piped asks Node's install question in
// Go (it handed over as tools-confirm before); output is byte-equal to Node.
func TestB698ToolsInstallPipedStdoutAsksInGo(t *testing.T) {
	for _, c := range []struct {
		key, want string
		code      int
		ran       bool
	}{{"n", b698ToolsNo, 1, false}, {"y", b698ToolsYes, 0, true}} {
		t.Run(c.key, func(t *testing.T) {
			home, _ := smallProject(t, "")
			t.Setenv("FORCE_COLOR", "")
			os.Unsetenv("FORCE_COLOR")
			t.Setenv("CODE_FORGE_HANDOVER_LOG", "")
			dir := toolsPath(t, map[string]string{"npm": "#!/bin/sh\necho \"$3\" >> \"${0%/*}/ran\"\nprintf 'added 1 package\\n'\n"})
			asked := b698Piped(t, c.key)
			checkSmallCases(t, []smallCase{{[]string{"tools", "install", "claude"}, c.code, c.want, ""}})
			if len(*asked) != 1 || (*asked)[0] != "Run 1 install command(s)?" {
				t.Fatalf("asked %q, want one question", *asked)
			}
			ran, err := os.ReadFile(filepath.Join(dir, "ran"))
			if c.ran && (err != nil || string(ran) != "@anthropic-ai/claude-code\n") {
				t.Fatalf("installs run = %q (%v)", ran, err)
			}
			if !c.ran && err == nil {
				t.Fatalf("a cancelled install ran npm: %q", ran)
			}
			if data, _ := os.ReadFile(filepath.Join(home, ".code-forge", "native", "handovers.jsonl")); len(data) != 0 {
				t.Fatalf("handed over: %s", data)
			}
		})
	}
}
