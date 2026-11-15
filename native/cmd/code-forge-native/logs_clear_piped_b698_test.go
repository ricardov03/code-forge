package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/humanask"
)

// Node goldens (B698): CODE_FORGE_NATIVE=0 code-forge logs clear run under
// expect with stdin a terminal and stdout redirected to a file, one key sent
// after the prompt drew, on b500ErrorLog (2 entries; the torn line skipped).
const (
	b698LogsFrame = "\u001b[?25l│\n◆  Delete the error log (2 entries)?\n│  ○ Yes / ● No\n└\n\u001b[1A\n\u001b[?25h\u001b[999D\u001b[4A\u001b[1B\u001b[J◇  Delete the error log (2 entries)?\n"
	b698LogsNo    = b698LogsFrame + "│  No\ncancelled — nothing deleted\n"
	b698LogsYes   = b698LogsFrame + "│  Yes\ndeleted %HOME%/.code-forge/logs/errors.jsonl\n"
	// FORCE_COLOR=1: styleText colors the pipe.
	b698LogsNoColor = "\u001b[?25l\u001b[90m│\u001b[39m\n\u001b[36m◆\u001b[39m  Delete the error log (2 entries)?\n\u001b[36m│\u001b[39m  \u001b[2m○\u001b[22m \u001b[2mYes\u001b[22m \u001b[2m/\u001b[22m \u001b[32m●\u001b[39m No\n\u001b[36m└\u001b[39m\n\u001b[1A\n\u001b[?25h\u001b[999D\u001b[4A\u001b[1B\u001b[J\u001b[32m◇\u001b[39m  Delete the error log (2 entries)?\n\u001b[90m│\u001b[39m  \u001b[2mNo\u001b[22m\ncancelled — nothing deleted\n"
)

// b698LogsPiped: stdin a terminal, stdout a pipe; the piped prompt draws the
// real clack frames from keys at ConfirmPiped's width and colour.
func b698LogsPiped(t *testing.T, keys string) *[]string {
	t.Helper()
	oldStdin, oldAsk, oldConfirm, oldPiped := logsStdinTTY, logsAskTTY, logsConfirm, logsConfirmPiped
	t.Cleanup(func() {
		logsStdinTTY, logsAskTTY, logsConfirm, logsConfirmPiped = oldStdin, oldAsk, oldConfirm, oldPiped
	})
	logsStdinTTY = func() bool { return true }
	logsAskTTY = func() bool { return false }
	logsConfirm = func(string) (humanask.Result, error) {
		t.Fatal("the terminal prompt ran with stdout piped")
		return humanask.NoTTY, nil
	}
	asked := []string{}
	logsConfirmPiped = func(message string) (humanask.Result, error) {
		asked = append(asked, message)
		return humanask.Ask(strings.NewReader(keys), os.Stdout, message, humanask.Options{Width: 80, Color: humanask.PipedColor(os.LookupEnv)})
	}
	return &asked
}

// B698: logs clear with stdin at a terminal and stdout piped asks in Go (it
// handed over as logs-clear-prompt before), byte-equal to Node: no keeps
// the log, adds Node's exit-1 row and exits 1; yes deletes it.
func TestB698LogsClearPipedStdoutAsksInGo(t *testing.T) {
	for _, c := range []struct {
		name, key, color, want string
		code                   int
	}{
		{"no", "n", "", b698LogsNo, 1},
		{"no-force-color", "n", "1", b698LogsNoColor, 1},
		{"yes", "y", "", b698LogsYes, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			home, _ := b500Env(t)
			if c.color == "" {
				t.Setenv("FORCE_COLOR", "")
				os.Unsetenv("FORCE_COLOR")
			} else {
				t.Setenv("FORCE_COLOR", c.color)
			}
			file := filepath.Join(home, ".code-forge", "logs", "errors.jsonl")
			b500Write(t, file, b500ErrorLog)
			asked := b698LogsPiped(t, c.key)
			code, out, errText := b500Run(t, "logs", "clear")
			want := strings.ReplaceAll(c.want, "%HOME%", home)
			if code != c.code || out != want || errText != "" {
				t.Fatalf("got (%d, %q, %q)\nwant (%d, %q, \"\")", code, out, errText, c.code, want)
			}
			if len(*asked) != 1 || (*asked)[0] != "Delete the error log (2 entries)?" {
				t.Fatalf("asked %q", *asked)
			}
			if reason := b500HandedOver(t, home); reason != "" {
				t.Fatalf("handed over: %s", reason)
			}
			if c.code == 0 {
				if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("log still there: %v", err)
				}
				return
			}
			log := b500Read(t, file)
			if !strings.HasPrefix(log, b500ErrorLog) || !strings.Contains(log, `"verb":"logs","sub":"clear","flags":[],"exit":1,"kind":"error","message":null,"stack":null,"cleaned":[],"fp":"`) {
				t.Fatalf("error log after the cancel:\n%s", log)
			}
		})
	}
}
