package main

// B678: `keys set <name>` without --op in Go: askHidden in src/cli/keys.mjs,
// @clack/prompts password({message: `Value for key "<name>" (input hidden)`})
// drawn by the wizard's clack port, the input read through
// humanask.ReadSecret (echo off, the terminal restored on every path). An
// empty value or a cancel stores nothing (exit 1); input that ends at the
// prompt is Node's unsettled top-level await (exit 13).
//
// B694: the prompt is drawn as Node draws it on any stdin and stdout: a
// piped stdin is read as it comes (readline keypresses), a stdout that is
// not a terminal gets no wrapping and 20 rows, util.styleText's colors follow
// shouldColorize(process.stdout) and clack's symbols are ASCII on TERM=linux.
// Only FORCE_COLOR with NO_COLOR or NODE_DISABLE_COLORS (Node's warning
// carries its pid) and a terminal of size 0 go to Node.

import (
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/ricardov03/code-forge/native/humanask"
)

// keysTTY is where the prompt draws: outTTY says stdout is a terminal.
type keysTTY struct {
	initWizEnv
	outTTY bool
}

// keysNoWrap is wrapAnsi(text, undefined): a stdout without columns.
const keysNoWrap = 1 << 30

var (
	// keysPromptOpen is the terminal the prompt draws on; tests stand in.
	keysPromptOpen = func() (keysTTY, bool) { return keysOpen(os.Stdin, os.Stdout) }
	// keysEnded is set when the input ended at the prompt: Node exits 13
	// with no error-log row.
	keysEnded bool
)

// keysOpen is process.stdin and process.stdout as clack sees them.
func keysOpen(in, out *os.File) (keysTTY, bool) {
	env := initWizEnv{in: in, out: out, cols: keysNoWrap, rows: 20}
	cols, rows, ok := initWizSize(out.Fd())
	if !ok {
		return keysTTY{initWizEnv: env}, true
	}
	if cols <= 0 || rows <= 0 {
		return keysTTY{}, false
	}
	env.cols, env.rows = cols, rows
	return keysTTY{initWizEnv: env, outTTY: true}, true
}

// keysColors is util.styleText's shouldColorize(process.stdout); ok false is
// the case where Node prints a warning with its pid.
func keysColors(outTTY bool) (colors, ok bool) {
	if force, set := os.LookupEnv("FORCE_COLOR"); set {
		switch force {
		case "", "1", "true", "2", "3":
			if os.Getenv("NO_COLOR") != "" || os.Getenv("NODE_DISABLE_COLORS") != "" {
				return false, false
			}
			return true, true
		}
		return false, true
	}
	return outTTY && keysColorDepth(), true
}

var (
	keysCISigns  = []string{"APPVEYOR", "BUILDKITE", "CIRCLECI", "DRONE", "GITEA_ACTIONS", "GITHUB_ACTIONS", "GITLAB_CI", "TRAVIS"}
	keysTermEnvs = []string{"eterm", "cons25", "console", "cygwin", "dtterm", "gnome", "hurd", "jfbterm", "konsole", "kterm", "mlterm", "mosh", "putty", "st", "rxvt-unicode-24bit", "terminator", "xterm-kitty"}
	keysTermRes  = regexp.MustCompile(`ansi|color|linux|direct|^con[0-9]*x[0-9]|^rxvt|^screen|^xterm|^vt100|^vt220`)
	keysTeamCity = regexp.MustCompile(`^(9\.(0*[1-9]\d*)\.|\d{2,}\.)`)
)

// keysColorDepth is tty getColorDepth(process.env) > 2 without FORCE_COLOR
// (lib/internal/tty.js, not Windows).
func keysColorDepth() bool {
	if os.Getenv("NODE_DISABLE_COLORS") != "" || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	if os.Getenv("TMUX") != "" {
		return true
	}
	_, tf := os.LookupEnv("TF_BUILD")
	_, agent := os.LookupEnv("AGENT_NAME")
	if tf && agent {
		return true
	}
	if _, ci := os.LookupEnv("CI"); ci {
		for _, sign := range keysCISigns {
			if _, set := os.LookupEnv(sign); set {
				return true
			}
		}
		return os.Getenv("CI_NAME") == "codeship"
	}
	if v, set := os.LookupEnv("TEAMCITY_VERSION"); set {
		return keysTeamCity.MatchString(v)
	}
	switch os.Getenv("TERM_PROGRAM") {
	case "iTerm.app", "HyperTerm", "MacTerm", "Apple_Terminal":
		return true
	}
	colorterm := os.Getenv("COLORTERM")
	if colorterm == "truecolor" || colorterm == "24bit" {
		return true
	}
	if term := os.Getenv("TERM"); term != "" {
		if strings.Contains(term, "truecolor") || strings.HasPrefix(term, "xterm-256") {
			return true
		}
		lower := strings.ToLower(term)
		for _, t := range keysTermEnvs {
			if lower == t {
				return true
			}
		}
		if lower == "constructor" || lower == "__proto__" {
			// TERM_ENVS[termEnv] is an Object.prototype member: not a depth
			return false
		}
		if keysTermRes.MatchString(lower) {
			return true
		}
	}
	return colorterm != ""
}

var (
	keysSGR   = regexp.MustCompile("\x1b\\[[0-9;]*m")
	keysASCII = strings.NewReplacer("◆", "*", "■", "x", "▲", "x", "◇", "o", "│", "|", "└", "—", "▪", "•")
)

// keysLook is the frame without styleText's colors and with clack's
// non-unicode symbols (unicode is TERM !== 'linux'), or nil when it is
// drawn as is.
func keysLook(colors, unicode bool) func(string) string {
	if colors && unicode {
		return nil
	}
	return func(frame string) string {
		if !colors {
			frame = keysSGR.ReplaceAllString(frame, "")
		}
		if !unicode {
			frame = keysASCII.Replace(frame)
		}
		return frame
	}
}

// keysPromptRead runs the line editor with the input hidden: through
// humanask.ReadSecret on a real terminal, directly on a test's reader.
func keysPromptRead(env initWizEnv, read func(io.Reader) (string, humanask.Result)) (string, humanask.Result) {
	file, ok := env.in.(*os.File)
	if !ok {
		return read(env.in)
	}
	value, res, err := humanask.ReadSecret(file, read)
	if res == humanask.NoTTY {
		// a pipe or a file: Node's readline reads its keypresses as is
		return read(file)
	}
	if err != nil {
		return "", humanask.Cancelled
	}
	return value, res
}

// keysPromptDecide is the hidden-prompt `keys set <name>`, or nil to hand
// over before anything is written.
func keysPromptDecide(name string, w keysWriter, storeWarning string, r *keysRedactor) keysRun {
	tty, ok := keysPromptOpen()
	if !ok {
		return nil
	}
	colors, ok := keysColors(tty.outTTY)
	if !ok {
		return nil
	}
	env := tty.initWizEnv
	eofText := initWizEOFFile()
	if eofText == "" {
		return nil
	}
	return func(stdout, stderr io.Writer) int {
		r.write(stderr, storeWarning)
		term := &initWizTerm{in: env.in, out: env.out, cols: env.cols, rows: env.rows, look: keysLook(colors, os.Getenv("TERM") != "linux")}
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
		if reason, thrown := w.put(name, typed, "user", nil, ""); reason != "" {
			// Node's put throws: the verb fails (Node adds the stack)
			r.write(stderr, keysCrash(thrown))
			return 1
		}
		r.write(stdout, "stored "+name+" in "+w.name()+" (source=user, expires=never)\n")
		return 0
	}
}
