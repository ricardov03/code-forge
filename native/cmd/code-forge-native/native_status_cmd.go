package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/humanask"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/runstatus"
)

// B501: `code-forge native-status` serves src/cli/native-status.mjs in Go: one
// status JSON line per stdin line (statusOf with gateDetails false), then the
// router's "no default export" line, exit 1, breadcrumb and error log. A line
// Go cannot answer as Node does hands the rest of stdin to Node's companion.
func init() { routes["native-status"] = runNativeStatus }

const nativeStatusUsageText = "usage: code-forge native-status\n  Internal daemon companion: reads JSON records and rows from standard input.\n"

// nativeStatusUnreadable is serve()'s catch line.
const nativeStatusUnreadable = `{"ok":false,"error":"status-unreadable"}`

// A \u escape of a UTF-16 surrogate: JSON.parse keeps a lone one, Go cannot.
var nativeStatusSurrogate = regexp.MustCompile(`\\u[dD][89a-fA-F]`)

// nativeStatusAgentVars is AGENT_ENV_VARS in src/util/error-hint.mjs.
var nativeStatusAgentVars = []string{"CLAUDECODE", "CLAUDE_CODE", "CURSOR_AGENT", "AI_AGENT"}

// nativeStatusCompanion runs Node's companion script (no router) over the
// rest of stdin; a test seam.
var nativeStatusCompanion = nativeStatusNode

func runNativeStatus(args []string) int {
	// The router prints USAGE for `native-status --help` before anything else;
	// native-status has no SUB_USAGE, so `native-status x --help` serves.
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprint(os.Stdout, nativeStatusUsageText)
		return 0
	}
	home := os.Getenv("HOME")
	if home == "" {
		// Node's os.homedir() falls back to the password file.
		return nativeStatusHandover(args, hNativeStatusNoHome)
	}
	if reviewWaitLogging() && humanask.IsTTY(os.Stderr) && !nativeStatusAgent() {
		// The router's maybeHint may print a hint line on a terminal.
		return nativeStatusHandover(args, hNativeStatusTTY)
	}
	before := gatesBreadcrumbName("native-status")
	in := bufio.NewReaderSize(os.Stdin, 64*1024)
	if code, done := nativeStatusServe(in, os.Stdout, home, args); done {
		return code
	}
	stderr := &contextErrWriter{}
	fmt.Fprint(stderr, "code-forge: verb \"native-status\" has no default export function\n")
	nativeVerbFailure("native-status", "", args, 1, stderr.last, before)
	return 1
}

func nativeStatusAgent() bool {
	for _, name := range nativeStatusAgentVars {
		if os.Getenv(name) != "" {
			return true
		}
	}
	return false
}

func nativeStatusHandover(args []string, reason handoverReason) int {
	code, err := handover(append([]string{"native-status"}, args...), reason)
	if err != nil {
		fmt.Fprintln(os.Stderr, "code-forge-native:", err)
	}
	return code
}

// nativeStatusServe answers every line until EOF. done is true only when the
// Node companion could not finish (its code is then the exit code).
func nativeStatusServe(in *bufio.Reader, out io.Writer, home string, args []string) (int, bool) {
	skipLF := false
	for {
		content, raw, ok := nativeStatusLine(in, &skipLF)
		if !ok {
			return 0, false
		}
		answer, served := nativeStatusAnswer(content, home)
		if !served {
			recordHandover(append([]string{"native-status"}, args...), handoverReasons[hNativeStatusLine])
			code, err := nativeStatusCompanion(io.MultiReader(bytes.NewReader(raw), in), out)
			if err != nil {
				fmt.Fprintln(os.Stderr, "code-forge-native:", err)
				return code, true
			}
			if code != 0 {
				return code, true
			}
			return 0, false
		}
		if _, err := io.WriteString(out, answer+"\n"); err != nil {
			// A closed stdout ends the companion's output, not its exit code.
			_, _ = io.Copy(io.Discard, in)
			return 0, false
		}
	}
}

// nativeStatusLine is node:readline with crlfDelay Infinity: a line ends at
// \n, \r\n or a lone \r (a \n right after that \r is skipped); a last line
// without an ending is emitted when it is not empty. raw holds the bytes read
// for this line (a skipped leading \n excluded): handed to Node, they split
// into the same lines.
func nativeStatusLine(in *bufio.Reader, skipLF *bool) (content, raw []byte, ok bool) {
	for {
		b, err := in.ReadByte()
		if err != nil {
			*skipLF = false
			if len(content) > 0 {
				return content, raw, true
			}
			return nil, nil, false
		}
		if *skipLF {
			*skipLF = false
			if b == '\n' {
				continue
			}
		}
		raw = append(raw, b)
		switch b {
		case '\n':
			if content == nil {
				content = []byte{}
			}
			return content, raw, true
		case '\r':
			*skipLF = true
			if content == nil {
				content = []byte{}
			}
			return content, raw, true
		}
		content = append(content, b)
	}
}

// nativeStatusAnswer is serve()'s body for one line. served is false where Go
// cannot give Node's answer.
func nativeStatusAnswer(line []byte, home string) (string, bool) {
	if !utf8.Valid(line) || nativeStatusSurrogate.Match(line) {
		return "", false
	}
	if !json.Valid(line) {
		return nativeStatusUnreadable, true
	}
	var parsed any
	if err := json.Unmarshal(line, &parsed); err != nil {
		// A number JSON.parse reads as Infinity.
		return "", false
	}
	// const { record, rows } = JSON.parse(line): anything but an object has no
	// record, and statusOf(undefined) throws.
	top, isObject := parsed.(map[string]any)
	if !isObject {
		return nativeStatusUnreadable, true
	}
	rawRecord, present := top["record"]
	if !present || rawRecord == nil {
		return nativeStatusUnreadable, true
	}
	record, isMap := rawRecord.(map[string]any)
	if !isMap {
		// A list or a primitive record: every field statusOf reads is undefined.
		record = map[string]any{}
	}
	var rows []ledger.Row
	rawRows, given := top["rows"]
	switch value := rawRows.(type) {
	case nil:
		if !given {
			// rows undefined: statusOf reads readAllRows(record.project).
			if slug, ok := record["project"].(string); ok {
				read, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge", "ledger"), slug)
				var numeric *json.UnmarshalTypeError
				if errors.As(err, &numeric) {
					return "", false
				}
				if err == nil {
					rows = read
				}
			}
		}
	case []any:
		rows = make([]ledger.Row, len(value))
		copy(rows, value)
	default:
		// for (const r of rows) throws on any other value.
		return nativeStatusUnreadable, true
	}
	summary, err := runstatus.ComputeBriefErr(record, rows, home)
	if errors.Is(err, runstatus.ErrUnorderedBlockID) {
		return "", false
	}
	if err != nil {
		return nativeStatusUnreadable, true
	}
	// JSON.stringify writes a non-finite number as null.
	if n, ok := summary.Get("spent_usd").(float64); ok && (math.IsInf(n, 0) || math.IsNaN(n)) {
		summary.Set("spent_usd", nil)
	}
	data, err := ledger.Stringify(ledger.Object{{Key: "runs", Value: []any{summary}}}, false)
	if err != nil {
		return "", false
	}
	return string(data), true
}

// nativeStatusNode runs `node <package>/src/cli/native-status.mjs` (the
// companion's main path, no router) with the given stdin and this stdout.
func nativeStatusNode(stdin io.Reader, stdout io.Writer) (int, error) {
	node := os.Getenv("CODE_FORGE_NODE")
	if node == "" {
		node = "node"
	}
	nodePath, err := exec.LookPath(node)
	if err != nil {
		return 127, fmt.Errorf("node executable %q not found: %w", node, err)
	}
	executable, err := os.Executable()
	if err != nil {
		return 1, fmt.Errorf("locate package executable: %w", err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return 1, fmt.Errorf("resolve package executable: %w", err)
	}
	script := filepath.Join(filepath.Dir(executable), "..", "..", "..", "src", "cli", "native-status.mjs")
	cmd := exec.Command(nodePath, script)
	cmd.Stdout = stdout
	cmd.Stderr = os.Stderr
	pipe, err := cmd.StdinPipe()
	if err != nil {
		return 1, fmt.Errorf("start node: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return 126, fmt.Errorf("start node: %w", err)
	}
	// The copy ends at stdin's EOF; Wait does not wait for it, so a companion
	// that exits early never hangs on an open stdin.
	go func() {
		_, _ = io.Copy(pipe, stdin)
		_ = pipe.Close()
	}()
	err = cmd.Wait()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if code := exit.ExitCode(); code >= 0 {
			return code, nil
		}
		return 1, nil
	}
	if err != nil {
		return 1, fmt.Errorf("node: %w", err)
	}
	return 0, nil
}
