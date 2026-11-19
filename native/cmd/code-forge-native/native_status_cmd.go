package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ricardov03/code-forge/native/humanask"
	"github.com/ricardov03/code-forge/native/jsonx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/runstatus"
)

// B501, B687: `code-forge native-status` serves src/cli/native-status.mjs in
// Go: one status JSON line per stdin line (statusOf with gateDetails false),
// then the router's "no default export" line, exit 1, breadcrumb, error log
// and (on a terminal) the router's hint. Lines are read as Node reads them
// (WHATWG UTF-8, JSON.parse with lone surrogates and Infinity) and written as
// JSON.stringify writes them. Without HOME, Node's os.homedir() is the
// password file's home. B690b: every line is answered in Go; an id outside
// printable ASCII sorts after the printable ASCII ids (no Node companion).
// B879 (B801 audit P07): the Node router's "no default export" line is gone.
// Records piped in are answered and the verb exits 0; an argument or a
// terminal on stdin is a usage error: exit 2, the usage, the refusal line and
// `next: code-forge status` (the error log keeps the refusal line).
func init() { routes["native-status"] = runNativeStatus }

// nsStdinTTY reports a terminal on stdin (a test seam).
var nsStdinTTY = func() bool { return humanask.IsTTY(os.Stdin) }

const nativeStatusUsageText = "usage: code-forge native-status\n  Internal daemon companion: reads JSON records and rows from standard input.\n"

// nativeStatusUnreadable is serve()'s catch line.
const nativeStatusUnreadable = `{"ok":false,"error":"status-unreadable"}`

// nativeStatusAgentVars is AGENT_ENV_VARS in src/util/error-hint.mjs.
var nativeStatusAgentVars = []string{"CLAUDECODE", "CLAUDE_CODE", "CURSOR_AGENT", "AI_AGENT"}

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
	if len(args) > 0 || nsStdinTTY() {
		return nativeStatusRefuse(args, before)
	}
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
	_ = done
	return code
}

// nativeStatusRefuse is the usage error for an argument or a terminal stdin.
func nativeStatusRefuse(args []string, before []any) int {
	stderr := &contextErrWriter{}
	fmt.Fprint(stderr, nativeStatusUsageText)
	line := "native-status: reads status records piped on stdin; it takes no arguments"
	if len(args) > 0 {
		line = "native-status: takes no arguments (got " + args[0] + "); it reads status records piped on stdin"
	}
	fmt.Fprint(stderr, line+"\n"+refusalNext("code-forge status"))
	routerHint(nativeVerbFailure("native-status", "", args, 2, stderr.last, before), 2)
	return 2
}

// nativeStatusServe answers every line until EOF. done is never true since
// B690b removed the Node companion; the pair keeps the caller's shape.
func nativeStatusServe(in *bufio.Reader, out io.Writer, home nsHome, args []string) (int, bool) {
	skipLF := false
	for {
		content, _, ok := nativeStatusLine(in, &skipLF)
		if !ok {
			return 0, false
		}
		answer := nativeStatusAnswer(content, home)
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

// nativeStatusAnswer is serve()'s body for one line.
func nativeStatusAnswer(line []byte, home nsHome) string {
	// readline decodes stdin as WHATWG UTF-8; an ASCII line break always ends
	// a byte sequence, so decoding each line alone gives the same text.
	parsed, err := jsonx.Parse(jsonx.DecodeUTF8(line))
	if err != nil {
		return nativeStatusUnreadable
	}
	// const { record, rows } = JSON.parse(line): anything but an object has no
	// record, and statusOf(undefined) throws.
	top, isObject := parsed.(map[string]any)
	if !isObject {
		return nativeStatusUnreadable
	}
	rawRecord, present := top["record"]
	if !present || rawRecord == nil {
		return nativeStatusUnreadable
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
		return nativeStatusUnreadable
	}
	keyHome := home.dir
	if !home.ok {
		keyHome = "\x00" // findingsKey's loadKey throws: the zero key
	}
	summary, err := runstatus.ComputeBriefErr(record, rows, keyHome)
	if err != nil {
		return nativeStatusUnreadable
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
		return nativeStatusUnreadable
	}
	return data
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
