package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B694: the typed `keys set <name>` on any stdin and stdout, drawn as Node
// draws it. Goldens are Node's own output (bin/code-forge with
// CODE_FORGE_NATIVE=0, temp HOME, file store, env PATH/HOME/TERM plus the
// case's variables) recorded on 2026-11-15: on a pty of the given size
// (stdout and stderr on the one terminal), with stdin a pipe ("pipe" set,
// written at once and closed) and with stdout and stderr pipes (cols 0).

// b694Prompt runs `keys set foo` with keys typed on a fake terminal, or the
// pipe text on a real pipe; cols 0 opens stdout as keysOpen does for a pipe.
func b694Prompt(t *testing.T, project string, keys []string, pipe string, cols, rows int) (int, string, string) {
	t.Helper()
	out, err := os.CreateTemp(t.TempDir(), "out-")
	if err != nil {
		t.Fatal(err)
	}
	errFile := out
	if cols == 0 {
		if errFile, err = os.CreateTemp(t.TempDir(), "err-"); err != nil {
			t.Fatal(err)
		}
	}
	var in io.Reader = &b528Keys{keys: keys}
	var inFile *os.File
	if keys == nil {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = r.Close() })
		if _, err := io.WriteString(w, pipe); err != nil {
			t.Fatal(err)
		}
		_ = w.Close()
		in, inFile = r, r
	}
	saved := keysPromptOpen
	keysPromptOpen = func() (keysTTY, bool) {
		if cols == 0 {
			return keysOpen(inFile, out)
		}
		return keysTTY{initWizEnv{in: in, out: out, cols: cols, rows: rows}, true}, true
	}
	defer func() { keysPromptOpen = saved }()
	keysEnded = false
	plan := keysDecide([]string{"set", "foo"}, project)
	if plan == nil {
		t.Fatal("keys set foo handed over to Node; want Go")
	}
	code := plan(out, errFile)
	_ = out.Close()
	_ = errFile.Close()
	stdout, _ := os.ReadFile(out.Name())
	stderr := ""
	if errFile != out {
		data, _ := os.ReadFile(errFile.Name())
		stderr = string(data)
	}
	return code, string(stdout), stderr
}

func TestB694KeysPromptVariantsMatchNode(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		env                    map[string]string
		keys                   []string
		pipe                   string
		cols, rows, code       int
		stored, stdout, stderr string
	}{
		{"nocolor", map[string]string{"NO_COLOR": "1"}, []string{"a", "b", "\u001b[D", "c", "\r"}, "", 80, 24, 0, "acb", "WARN keys: no OS keychain available; secrets are stored in 0600 files under ~/.code-forge/store\n\u001b[?25l│\n◆  Value for key \"foo\" (input hidden)\n│  _\n└\n\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G│  ▪_\u001b[2B\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G│  ▪▪_\u001b[2B\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G│  ▪▪\u001b[2B\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G│  ▪▪▪\u001b[2B\u001b[999D\u001b[4A\u001b[1B\u001b[J◇  Value for key \"foo\" (input hidden)\n│  ▪▪▪\n\u001b[?25hstored foo in file (source=user, expires=never)\n", ""},
		{"linux", map[string]string{"TERM": "linux"}, []string{"a", "b", "\u001b[D", "c", "\r"}, "", 80, 24, 0, "acb", "WARN keys: no OS keychain available; secrets are stored in 0600 files under ~/.code-forge/store\n\u001b[?25l\u001b[90m|\u001b[39m\n\u001b[36m*\u001b[39m  Value for key \"foo\" (input hidden)\n\u001b[36m|\u001b[39m  \u001b[7m\u001b[8m_\u001b[28m\u001b[27m\n\u001b[36m—\u001b[39m\n\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G\u001b[36m|\u001b[39m  •\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G\u001b[36m|\u001b[39m  ••\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G\u001b[36m|\u001b[39m  •\u001b[7m•\u001b[27m\u001b[2B\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G\u001b[36m|\u001b[39m  ••\u001b[7m•\u001b[27m\u001b[2B\u001b[999D\u001b[4A\u001b[1B\u001b[J\u001b[32mo\u001b[39m  Value for key \"foo\" (input hidden)\n\u001b[90m|\u001b[39m  \u001b[2m•••\u001b[22m\n\u001b[?25hstored foo in file (source=user, expires=never)\n", ""},
		{"cursor-moves-nocolor", map[string]string{"NO_COLOR": "1"}, []string{"a", "b", "c", "\u001b[D", "\u001b[D", "\u001b[D", "\u001b[C", "\r"}, "", 80, 24, 0, "abc", "WARN keys: no OS keychain available; secrets are stored in 0600 files under ~/.code-forge/store\n\u001b[?25l│\n◆  Value for key \"foo\" (input hidden)\n│  _\n└\n\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G│  ▪_\u001b[2B\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G│  ▪▪_\u001b[2B\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G│  ▪▪▪_\u001b[2B\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G│  ▪▪▪\u001b[2B\u001b[999D\u001b[4A\u001b[1B\u001b[J◇  Value for key \"foo\" (input hidden)\n│  ▪▪▪\n\u001b[?25hstored foo in file (source=user, expires=never)\n", ""},
		{"constructor", map[string]string{"TERM": "constructor", "COLORTERM": "x"}, []string{"x", "\r"}, "", 80, 24, 0, "x", "WARN keys: no OS keychain available; secrets are stored in 0600 files under ~/.code-forge/store\n\u001b[?25l│\n◆  Value for key \"foo\" (input hidden)\n│  _\n└\n\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G│  ▪_\u001b[2B\u001b[999D\u001b[4A\u001b[1B\u001b[J◇  Value for key \"foo\" (input hidden)\n│  ▪\n\u001b[?25hstored foo in file (source=user, e[REDACTED]pires=never)\n", ""},
		{"20x5", map[string]string{}, []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o", "p", "q", "r", "s", "t", "u", "\r"}, "", 20, 5, 0, "abcdefghijklmnopqrstu", "WARN keys: no OS keychain available; secrets are stored in 0600 files under ~/.code-forge/store\n\u001b[?25l\u001b[90m│\u001b[39m\n\u001b[36m◆\u001b[39m  Value for key \n\"foo\" (input hidden)\n\u001b[36m│\u001b[39m  \u001b[7m\u001b[8m_\u001b[28m\u001b[27m\n\u001b[36m└\u001b[39m\n\u001b[999D\u001b[5A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[5A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[5A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[5A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪▪▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[5A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪▪▪▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[5A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪▪▪▪▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[5A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪▪▪▪▪▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[5A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪▪▪▪▪▪▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[5A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪▪▪▪▪▪▪▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[5A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪▪▪▪▪▪▪▪▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[5A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪▪▪▪▪▪▪▪▪▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[5A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪▪▪▪▪▪▪▪▪▪▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[5A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪▪▪▪▪▪▪▪▪▪▪▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[5A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪▪▪▪▪▪▪▪▪▪▪▪▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[5A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪▪▪▪▪▪▪▪▪▪▪▪▪▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[5A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪▪▪▪▪▪▪▪▪▪▪▪▪▪▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[5A\u001b[2B\u001b[J\u001b[36m│\u001b[39m  \n▪▪▪▪▪▪▪▪▪▪▪▪▪▪▪▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\n\u001b[36m└\u001b[39m\n\u001b[999D\u001b[6A\u001b[2B\u001b[2K\u001b[G▪▪▪▪▪▪▪▪▪▪▪▪▪▪▪▪▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[6A\u001b[2B\u001b[2K\u001b[G▪▪▪▪▪▪▪▪▪▪▪▪▪▪▪▪▪▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[6A\u001b[1B\u001b[J\u001b[36m│\u001b[39m  ▪▪▪▪▪▪▪▪▪▪▪▪▪▪▪▪▪\n▪▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\n\u001b[36m└\u001b[39m\n\u001b[999D\u001b[6A\u001b[2B\u001b[2K\u001b[G▪▪▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[6A\u001b[J\u001b[90m│\u001b[39m\n\u001b[32m◇\u001b[39m  Value for key \n\"foo\" (input hidden)\n\u001b[90m│\u001b[39m  \u001b[2m▪▪▪▪▪▪▪▪▪▪▪▪▪▪▪▪▪\u001b[22m\n\u001b[2m▪▪▪▪\u001b[22m\n\u001b[?25hstored foo in file (source=user, expires=never)\n", ""},
		{"10x3-ctrlc", map[string]string{}, []string{"a", "b", "\u0003"}, "", 10, 3, 1, "", "WARN keys: no OS keychain available; secrets are stored in 0600 files under ~/.code-forge/store\n\u001b[?25l\u001b[90m│\u001b[39m\n\u001b[36m◆\u001b[39m  Value \nfor key \n\"foo\" \n(input \nhidden)\n\u001b[36m│\u001b[39m  \u001b[7m\u001b[8m_\u001b[28m\u001b[27m\n\u001b[36m└\u001b[39m\n\u001b[999D\u001b[8A\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[8A\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[8A\u001b[Jhidden)\n\u001b[90m│\u001b[39m  \u001b[9m\u001b[2m▪▪\u001b[22m\u001b[29m\n\u001b[90m│\u001b[39m\n\u001b[?25hfoo: nothing stored\n", ""},
		{"pipein", map[string]string{}, nil, "s3cret\r", 80, 24, 0, "s3cret", "WARN keys: no OS keychain available; secrets are stored in 0600 files under ~/.code-forge/store\n\u001b[?25l\u001b[90m│\u001b[39m\n\u001b[36m◆\u001b[39m  Value for key \"foo\" (input hidden)\n\u001b[36m│\u001b[39m  \u001b[7m\u001b[8m_\u001b[28m\u001b[27m\n\u001b[36m└\u001b[39m\n\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪▪▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪▪▪▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪▪▪▪▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[4A\u001b[1B\u001b[J\u001b[32m◇\u001b[39m  Value for key \"foo\" (input hidden)\n\u001b[90m│\u001b[39m  \u001b[2m▪▪▪▪▪▪\u001b[22m\n\u001b[?25hstored foo in file (source=user, expires=never)\n", ""},
		{"pipein-lf", map[string]string{}, nil, "ab\ncd\r", 80, 24, 0, "cd", "WARN keys: no OS keychain available; secrets are stored in 0600 files under ~/.code-forge/store\n\u001b[?25l\u001b[90m│\u001b[39m\n\u001b[36m◆\u001b[39m  Value for key \"foo\" (input hidden)\n\u001b[36m│\u001b[39m  \u001b[7m\u001b[8m_\u001b[28m\u001b[27m\n\u001b[36m└\u001b[39m\n\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  \u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[4A\u001b[1B\u001b[J\u001b[32m◇\u001b[39m  Value for key \"foo\" (input hidden)\n\u001b[90m│\u001b[39m  \u001b[2m▪▪\u001b[22m\n\u001b[?25hstored foo in file (source=user, expires=never)\n", ""},
		{"pipein-ctrlc", map[string]string{}, nil, "ab\u0003", 80, 24, 1, "", "WARN keys: no OS keychain available; secrets are stored in 0600 files under ~/.code-forge/store\n\u001b[?25l\u001b[90m│\u001b[39m\n\u001b[36m◆\u001b[39m  Value for key \"foo\" (input hidden)\n\u001b[36m│\u001b[39m  \u001b[7m\u001b[8m_\u001b[28m\u001b[27m\n\u001b[36m└\u001b[39m\n\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[4A\u001b[1B\u001b[J\u001b[31m■\u001b[39m  Value for key \"foo\" (input hidden)\n\u001b[90m│\u001b[39m  \u001b[9m\u001b[2m▪▪\u001b[22m\u001b[29m\n\u001b[90m│\u001b[39m\n\u001b[?25hfoo: nothing stored\n", ""},
		{"pipein-eof", map[string]string{}, nil, "ab", 80, 24, 13, "", "WARN keys: no OS keychain available; secrets are stored in 0600 files under ~/.code-forge/store\n\u001b[?25l\u001b[90m│\u001b[39m\n\u001b[36m◆\u001b[39m  Value for key \"foo\" (input hidden)\n\u001b[36m│\u001b[39m  \u001b[7m\u001b[8m_\u001b[28m\u001b[27m\n\u001b[36m└\u001b[39m\n\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2BWarning: Detected unsettled top-level await at file://@MJS@:212\n  const code = await run(process.argv.slice(2), { configureTmpRoot: true });\n               ^\n\n\n\n", ""},
		{"pipeout", map[string]string{}, nil, "ab\r", 0, 0, 0, "ab", "\u001b[?25l│\n◆  Value for key \"foo\" (input hidden)\n│  _\n└\n\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G│  ▪_\u001b[2B\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G│  ▪▪_\u001b[2B\u001b[999D\u001b[4A\u001b[1B\u001b[J◇  Value for key \"foo\" (input hidden)\n│  ▪▪\n\u001b[?25hstored foo in file (source=user, expires=never)\n", "WARN keys: no OS keychain available; secrets are stored in 0600 files under ~/.code-forge/store\n"},
		{"pipeout-force", map[string]string{"FORCE_COLOR": "1"}, nil, "a\r", 0, 0, 0, "a", "\u001b[?25l\u001b[90m│\u001b[39m\n\u001b[36m◆\u001b[39m  Value for key \"foo\" (input hidden)\n\u001b[36m│\u001b[39m  \u001b[7m\u001b[8m_\u001b[28m\u001b[27m\n\u001b[36m└\u001b[39m\n\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[4A\u001b[1B\u001b[J\u001b[32m◇\u001b[39m  Value for key \"foo\" (input hidden)\n\u001b[90m│\u001b[39m  \u001b[2m▪\u001b[22m\n\u001b[?25hstored foo in file (source=user, expires=never)\n", "WARN keys: no OS keychain available; secrets are stored in 0600 files under ~/.code-forge/store\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, project, mjs := b678PromptHome(t)
			b528Unset(t, "TMUX", "TF_BUILD", "AGENT_NAME", "CI_NAME", "TERM_PROGRAM", "COLORTERM", "GITHUB_ACTIONS")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			code, stdout, stderr := b694Prompt(t, project, tc.keys, tc.pipe, tc.cols, tc.rows)
			want := strings.ReplaceAll(tc.stdout, "@MJS@", mjs)
			if code != tc.code || stdout != want || stderr != tc.stderr {
				t.Fatalf("exit %d (want %d) stderr %q\n%s", code, tc.code, stderr, b528Diff(want, stdout))
			}
			key := b678Read(t, filepath.Join(dir, "foo.key"))
			if tc.stored == "" && key != "<none>" || tc.stored != "" && key != `{"v":"`+tc.stored+`","exp":null}` {
				t.Fatalf("foo.key = %q", key)
			}
		})
	}
}

// FORCE_COLOR with NO_COLOR or NODE_DISABLE_COLORS set: Node warns with its
// pid ("(node:<pid>) Warning: The 'NO_COLOR' env is ignored ..."), so Node
// runs it; FORCE_COLOR=0 with NO_COLOR draws without colors in Go.
func TestB694KeysPromptForceColorWarning(t *testing.T) {
	_, project, _ := b678PromptHome(t)
	saved := keysPromptOpen
	t.Cleanup(func() { keysPromptOpen = saved })
	keysPromptOpen = func() (keysTTY, bool) {
		return keysTTY{initWizEnv{in: &b528Keys{}, out: io.Discard, cols: 80, rows: 24}, true}, true
	}
	for _, tc := range []struct {
		force, noColor, disable string
		node                    bool
	}{
		{"1", "1", "", true},
		{"", "", "1", true},
		{"3", "x", "", true},
		{"0", "1", "", false},
		{"1", "", "", false},
	} {
		t.Setenv("FORCE_COLOR", tc.force)
		t.Setenv("NO_COLOR", tc.noColor)
		t.Setenv("NODE_DISABLE_COLORS", tc.disable)
		if got := keysDecide([]string{"set", "foo"}, project) == nil; got != tc.node {
			t.Errorf("FORCE_COLOR=%q NO_COLOR=%q NODE_DISABLE_COLORS=%q: handed over %v", tc.force, tc.noColor, tc.disable, got)
		}
	}
}

// keysColors on a terminal is Node's getColorDepth(env) > 2 (recorded from
// require('tty').WriteStream.prototype.getColorDepth on Node 24); a stdout
// that is not a terminal never gets colors without FORCE_COLOR.
func TestB694KeysColorsMatchNode(t *testing.T) {
	vars := []string{"TERM", "NO_COLOR", "NODE_DISABLE_COLORS", "FORCE_COLOR", "TMUX", "CI", "CI_NAME", "TRAVIS", "TF_BUILD", "AGENT_NAME", "TEAMCITY_VERSION", "TERM_PROGRAM", "TERM_PROGRAM_VERSION", "COLORTERM",
		"APPVEYOR", "BUILDKITE", "CIRCLECI", "DRONE", "GITEA_ACTIONS", "GITHUB_ACTIONS", "GITLAB_CI"}
	for _, tc := range []struct {
		env  map[string]string
		node bool
	}{
		{map[string]string{}, false},
		{map[string]string{"TERM": "xterm"}, true},
		{map[string]string{"TERM": "xterm-256color", "NO_COLOR": ""}, true},
		{map[string]string{"TERM": "xterm", "NO_COLOR": "1"}, false},
		{map[string]string{"TERM": "xterm", "NODE_DISABLE_COLORS": "1"}, false},
		{map[string]string{"TERM": "dumb", "COLORTERM": "truecolor"}, false},
		{map[string]string{"TERM": "foo", "TMUX": "1"}, true},
		{map[string]string{"TERM": "foo", "TMUX": ""}, false},
		{map[string]string{"TERM": "xterm", "CI": ""}, false},
		{map[string]string{"TERM": "xterm", "CI": "true"}, false},
		{map[string]string{"TERM": "foo", "CI": "", "TRAVIS": ""}, true},
		{map[string]string{"TERM": "foo", "CI": "1", "CI_NAME": "codeship"}, true},
		{map[string]string{"TERM": "foo", "TF_BUILD": "", "AGENT_NAME": ""}, true},
		{map[string]string{"TERM": "foo", "TF_BUILD": "1"}, false},
		{map[string]string{"TERM": "xterm", "TEAMCITY_VERSION": "9.1.2"}, true},
		{map[string]string{"TERM": "xterm", "TEAMCITY_VERSION": "9.0.1"}, false},
		{map[string]string{"TERM": "foo", "TEAMCITY_VERSION": "10.0"}, true},
		{map[string]string{"TERM": "foo", "TERM_PROGRAM": "Apple_Terminal"}, true},
		{map[string]string{"TERM": "foo", "TERM_PROGRAM": "vscode"}, false},
		{map[string]string{"TERM": "foo", "COLORTERM": "24bit"}, true},
		{map[string]string{"TERM": "foo", "COLORTERM": "x"}, true},
		{map[string]string{"TERM": "my-truecolor"}, true},
		{map[string]string{"TERM": "Konsole"}, true},
		{map[string]string{"TERM": "vt100"}, true},
		{map[string]string{"TERM": "VT220"}, true},
		{map[string]string{"TERM": "linux"}, true},
		{map[string]string{"TERM": "con80x25"}, true},
		{map[string]string{"TERM": "cons25"}, true},
		{map[string]string{"TERM": "ansi"}, true},
		{map[string]string{"TERM": "screen.xterm"}, true},
		{map[string]string{"TERM": "rxvt"}, true},
		{map[string]string{"TERM": "Eterm"}, true},
		{map[string]string{"TERM": "constructor", "COLORTERM": "x"}, false},
		{map[string]string{"TERM": "__proto__"}, false},
		{map[string]string{"TERM": "hasownproperty", "COLORTERM": "1"}, true},
		{map[string]string{"TERM": "xterm-mono"}, true},
		{map[string]string{"TERM": "foo"}, false},
		{map[string]string{"TERM": ""}, false},
	} {
		b528Unset(t, vars...)
		for k, v := range tc.env {
			t.Setenv(k, v)
		}
		colors, ok := keysColors(true)
		piped, _ := keysColors(false)
		if !ok || colors != tc.node || piped {
			t.Errorf("%v: colors %v (Node %v) ok %v piped %v", tc.env, colors, tc.node, ok, piped)
		}
	}
}
