package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ricardov03/code-forge/native/jsonx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/pkgroot"
	"github.com/ricardov03/code-forge/native/runstatus"
)

// B501, B687: `code-forge native-status` serves src/cli/native-status.mjs in
// Go: one status JSON line per stdin line (statusOf with gateDetails false),
// then the router's "no default export" line, exit 1, breadcrumb, error log
// and (on a terminal) the router's hint. Lines are read as Node reads them
// (WHATWG UTF-8, JSON.parse with lone surrogates and Infinity) and written as
// JSON.stringify writes them. Without HOME, Node's os.homedir() is the
// password file's home. Only a line whose listed block ids need ICU's
// localeCompare (an id outside printable ASCII among two or more) still hands
// the rest of stdin to Node's companion.
func init() { routes["native-status"] = runNativeStatus }

const nativeStatusUsageText = "usage: code-forge native-status\n  Internal daemon companion: reads JSON records and rows from standard input.\n"

// nativeStatusUnreadable is serve()'s catch line.
const nativeStatusUnreadable = `{"ok":false,"error":"status-unreadable"}`

// nativeStatusAgentVars is AGENT_ENV_VARS in src/util/error-hint.mjs.
var nativeStatusAgentVars = []string{"CLAUDECODE", "CLAUDE_CODE", "CURSOR_AGENT", "AI_AGENT"}

// nativeStatusCompanion runs Node's companion script (no router) over the
// rest of stdin; a test seam.
var nativeStatusCompanion = nativeStatusNode

// nsUserHome is the password file's home of this user, the one Node's
// os.homedir() returns when HOME is unset (a test seam).
var nsUserHome = func() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	if u.HomeDir == "" {
		return "", errors.New("no home directory")
	}
	return u.HomeDir, nil
}

// nsSlug is SLUG_PATTERN in src/ledger/paths.mjs.
var nsSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// nsHome is os.homedir() for the serve loop. ok is false when it throws.
type nsHome struct {
	dir string
	ok  bool
}

func runNativeStatus(args []string) int {
	// The router prints USAGE for `native-status --help` before anything else;
	// native-status has no SUB_USAGE, so `native-status x --help` serves.
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprint(safeStdout, nativeStatusUsageText)
		return 0
	}
	if !routerHintReadable() {
		// The router's maybeHint would judge a time only Date.parse reads.
		return refuseUnported(append([]string{"native-status"}, args...), hRouterHintState)
	}
	before := gatesBreadcrumbName("native-status")
	home := nsHome{dir: os.Getenv("HOME"), ok: true}
	_, homeSet := os.LookupEnv("HOME")
	if !homeSet {
		// Node's os.homedir() reads the password file; the router's logs,
		// keyed on process.env.HOME, stay off.
		home.dir, _ = nsUserHome()
		home.ok = home.dir != ""
		if home.ok {
			_ = os.Setenv("HOME", home.dir) // config.Load's home, while serving
		}
	}
	in := bufio.NewReaderSize(os.Stdin, 64*1024)
	code, done := nativeStatusServe(in, safeStdout, home, args)
	if !homeSet {
		_ = os.Unsetenv("HOME")
	}
	if done {
		return code
	}
	stderr := &contextErrWriter{}
	fmt.Fprint(stderr, "code-forge: verb \"native-status\" has no default export function\n")
	routerHint(nativeVerbFailure("native-status", "", args, 1, stderr.last, before), 1)
	return 1
}

// nativeStatusServe answers every line until EOF. done is true only when the
// Node companion could not finish (its code is then the exit code).
func nativeStatusServe(in *bufio.Reader, out io.Writer, home nsHome, args []string) (int, bool) {
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
				fmt.Fprintln(safeStderr, "code-forge-native:", err)
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
// \n, \r\n, a lone \r (a \n right after that \r is skipped), U+2028 or
// U+2029; a last line without an ending is emitted when it is not empty. raw
// holds the bytes read for this line (a skipped leading \n excluded): handed
// to Node, they split into the same lines. The bytes E2 80 A8/A9 always decode
// to U+2028/U+2029, whatever comes before them.
func nativeStatusLine(in *bufio.Reader, skipLF *bool) (content, raw []byte, ok bool) {
	ended := func() ([]byte, []byte, bool) {
		if content == nil {
			content = []byte{}
		}
		return content, raw, true
	}
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
			return ended()
		case '\r':
			*skipLF = true
			return ended()
		case 0xE2:
			if next, _ := in.Peek(2); len(next) == 2 && next[0] == 0x80 && (next[1] == 0xA8 || next[1] == 0xA9) {
				_, _ = in.Discard(2)
				raw = append(raw, next[0], next[1])
				return ended()
			}
		}
		content = append(content, b)
	}
}

// nativeStatusAnswer is serve()'s body for one line. served is false only
// where the block order needs ICU (see runstatus.ErrUnorderedBlockID).
func nativeStatusAnswer(line []byte, home nsHome) (string, bool) {
	// readline decodes stdin as WHATWG UTF-8; an ASCII line break always ends
	// a byte sequence, so decoding each line alone gives the same text.
	parsed, err := jsonx.Parse(jsonx.DecodeUTF8(line))
	if err != nil {
		return nativeStatusUnreadable, true
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
	source, isMap := rawRecord.(map[string]any)
	record := map[string]any{}
	if isMap {
		// A list or a primitive record: every field statusOf reads is undefined.
		for k, v := range source {
			record[k] = v
		}
	}
	if ws, ok := record["workspace"].(string); ok {
		// Node encodes a path's lone surrogate as U+FFFD.
		record["workspace"] = jsonx.WellFormed(ws)
	}
	var rows []ledger.Row
	rawRows, given := top["rows"]
	switch value := rawRows.(type) {
	case nil:
		if !given && home.ok {
			// rows undefined: statusOf reads readAllRows(record.project); a throw
			// leaves rows null.
			if slug, ok := record["project"].(string); ok {
				rows = nsReadAllRows(filepath.Join(home.dir, ".code-forge", "ledger"), slug)
			}
		}
	case []any:
		rows = make([]ledger.Row, len(value))
		copy(rows, value)
	default:
		// for (const r of rows) throws on any other value.
		return nativeStatusUnreadable, true
	}
	keyHome := home.dir
	if !home.ok {
		keyHome = "\x00" // findingsKey's loadKey throws: the zero key
	}
	summary, err := runstatus.ComputeBriefErr(record, rows, keyHome)
	if errors.Is(err, runstatus.ErrUnorderedBlockID) {
		return "", false
	}
	if err != nil {
		return nativeStatusUnreadable, true
	}
	if !home.ok {
		summary.Set("budget_usd", nil) // loadProjectConfig threw
	}
	if awaiting, ok := summary.Get("awaiting_human_check").([]string); ok {
		// Array#sort compares UTF-16 code units, lone surrogates included.
		sort.SliceStable(awaiting, func(i, j int) bool { return jsonx.CompareUTF16(awaiting[i], awaiting[j]) < 0 })
	}
	data, err := jsonx.Stringify(jsonx.Object{{Key: "runs", Value: []any{nsJSON(summary)}}})
	if err != nil {
		return nativeStatusUnreadable, true
	}
	return data, true
}

// nsJSON turns a summary built with ledger.Object into jsonx values.
func nsJSON(v any) any {
	switch x := v.(type) {
	case ledger.Object:
		out := make(jsonx.Object, len(x))
		for i, f := range x {
			out[i] = jsonx.Member{Key: f.Key, Value: nsJSON(f.Value)}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = nsJSON(item)
		}
		return out
	}
	return v
}

// nsReadAllRows is readAllRows in src/ledger/write.mjs; nil is a throw (the
// caller's rows are then null): a bad slug, an unreadable directory or file,
// or a broken line that is not a file's last.
func nsReadAllRows(dir, slug string) []ledger.Row {
	if !nsSlug.MatchString(slug) {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []ledger.Row{}
	}
	if err != nil {
		return nil
	}
	pattern := regexp.MustCompile(`^` + regexp.QuoteMeta(slug) + `\.(\d+)\.jsonl$`)
	type rotation struct {
		name string
		n    float64
	}
	rotated := []rotation{}
	for _, entry := range entries {
		if m := pattern.FindStringSubmatch(entry.Name()); m != nil {
			// Number() of the suffix; a huge one is Infinity.
			n, _ := strconv.ParseFloat(m[1], 64)
			rotated = append(rotated, rotation{entry.Name(), n})
		}
	}
	sort.SliceStable(rotated, func(i, j int) bool { return rotated[i].n < rotated[j].n })
	rotated = append(rotated, rotation{name: slug + ".jsonl"})
	rows := []ledger.Row{}
	for _, file := range rotated {
		data, err := os.ReadFile(filepath.Join(dir, file.name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil
		}
		lines := []string{}
		for _, line := range strings.Split(jsonx.DecodeUTF8(data), "\n") {
			if line != "" {
				lines = append(lines, line)
			}
		}
		for i, line := range lines {
			row, err := jsonx.Parse(line)
			if err != nil {
				if i == len(lines)-1 {
					continue // a torn final line
				}
				return nil
			}
			rows = append(rows, row)
		}
	}
	return rows
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
	script := filepath.Join(pkgroot.FromExecutable(executable), "src", "cli", "native-status.mjs")
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
