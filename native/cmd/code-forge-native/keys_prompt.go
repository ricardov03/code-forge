package main

// B678: `keys set <name>` without --op in Go: askHidden in src/cli/keys.mjs,
// @clack/prompts password({message: `Value for key "<name>" (input hidden)`})
// drawn by the wizard's clack port, the input read through
// humanask.ReadSecret (echo off, the terminal restored on every path). An
// empty value or a cancel stores nothing (exit 1); input that ends at the
// prompt is Node's unsettled top-level await (exit 13). Without a terminal
// on stdin and stdout, or one Go does not draw like Node, Node runs it.

import (
	"io"
	"os"

	"github.com/ricardov03/code-forge/native/humanask"
)

var (
	// keysPromptOpen is the terminal the prompt draws on; tests stand in.
	keysPromptOpen = func() (initWizEnv, bool) { return initWizOpen() }
	// keysEnded is set when the input ended at the prompt: Node exits 13
	// with no error-log row.
	keysEnded bool
)

// keysPromptRead runs the line editor with the input hidden: through
// humanask.ReadSecret on a real terminal, directly on a test's reader.
func keysPromptRead(env initWizEnv, read func(io.Reader) (string, humanask.Result)) (string, humanask.Result) {
	file, ok := env.in.(*os.File)
	if !ok {
		return read(env.in)
	}
	value, res, err := humanask.ReadSecret(file, read)
	if err != nil || res == humanask.NoTTY {
		// the terminal went away after the check: nothing was typed
		return "", humanask.Cancelled
	}
	return value, res
}

// keysPromptDecide is the hidden-prompt `keys set <name>`, or nil to hand
// over before anything is written.
func keysPromptDecide(name string, w keysWriter, storeWarning string, r *keysRedactor) keysRun {
	env, ok := keysPromptOpen()
	if !ok || !initWizTerminalOK(env) {
		return nil
	}
	eofText := initWizEOFFile()
	if eofText == "" {
		return nil
	}
	return func(stdout, stderr io.Writer) int {
		r.write(stderr, storeWarning)
		term := &initWizTerm{in: env.in, out: env.out, cols: env.cols, rows: env.rows}
		p := &initWizPrompt{kind: "password", message: `Value for key "` + name + `" (input hidden)`}
		typed, res := keysPromptRead(env, func(in io.Reader) (string, humanask.Result) {
			term.in = in
			switch term.run(p) {
			case initWizSubmitted:
				return p.input, humanask.Confirmed
			case initWizEOF:
				return "", humanask.Ended
			}
			return "", humanask.Cancelled
		})
		if res == humanask.Ended {
			keysEnded = true
			_, _ = io.WriteString(stderr, eofText)
			return 13
		}
		if res != humanask.Confirmed || typed == "" {
			r.write(stderr, name+": nothing stored\n")
			return 1
		}
		r.register(typed)
		if reason := w.put(name, typed, "user", nil, ""); reason != "" {
			// Node's put throws: the verb fails (Node adds the stack)
			r.write(stderr, "code-forge: verb \"keys\" failed: Error: "+reason+"\n")
			return 1
		}
		r.write(stdout, "stored "+name+" in "+w.name()+" (source=user, expires=never)\n")
		return 0
	}
}
