package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/ledger"
)

func init() { routes["logs"] = runLogs }

// Keep fallbacks before both output and breadcrumbs: Node must observe the
// original state and argv, especially when a usage error itself gets logged.
func logsHandover(args []string, reason handoverReason) int {
	code, err := handover(append([]string{"logs"}, args...), reason)
	if err != nil {
		fmt.Fprintln(os.Stderr, "code-forge-native:", err)
	}
	return code
}

var logsPositive = regexp.MustCompile(`^[1-9][0-9]*$`)
var logsFP = regexp.MustCompile(`^[0-9a-f]{12}$`)
var logsWord = regexp.MustCompile(`^[a-z]+$`)
var logsCrumb = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}( [a-z][a-z0-9-]{0,39})?$`)
var logsSurrogate = regexp.MustCompile(`(?i)\\ud[89ab][0-9a-f]{2}|\\ud[cdef][0-9a-f]{2}`)

func runLogs(args []string) int {
	if len(args) > 0 && args[0] == "handovers" {
		return runHandoverLogs(args[1:])
	}
	name, rest := "list", args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, rest = args[0], args[1:]
		if name != "path" && name != "summary" {
			return logsHandover(args, hLogsUnportedSubcommand)
		}
	}
	// The Node router handles help before parsing or touching any state.
	for i := 0; i < len(args) && i < 2; i++ {
		if args[i] == "--help" || args[i] == "-h" {
			return logsHandover(args, hLogsHelp)
		}
	}
	n, asJSON, ok := logsFlags(name, rest)
	if !ok || os.Getenv("HOME") == "" {
		return logsHandover(args, hLogsFlagsOrHome)
	}
	file := filepath.Join(os.Getenv("HOME"), ".code-forge", "logs", "errors.jsonl")
	var output string
	if name == "path" {
		output = file + "\n"
	} else {
		entries, safe := logsRead(file)
		if !safe {
			return logsHandover(args, hLogsDecode)
		}
		if len(entries) == 0 {
			output = "no errors logged\n"
			if asJSON {
				output = "{\"errors\":[]}\n"
				if name == "summary" {
					output = fmt.Sprintf("{\"days\":%d,\"counts\":[],\"warnings\":[]}\n", n)
				}
			}
		} else {
			for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
				entries[i], entries[j] = entries[j], entries[i]
			}
			if name == "summary" {
				output, ok = logsSummary(entries, n, asJSON)
			} else {
				if n < int64(len(entries)) {
					entries = entries[:int(n)]
				}
				output, ok = logsList(entries, asJSON)
			}
			if !ok {
				return logsHandover(args, hLogsFormat)
			}
		}
	}
	logsBreadcrumb(name)
	fmt.Fprint(os.Stdout, output)
	return 0
}

func logsFlags(name string, args []string) (int64, bool, bool) {
	n := int64(10)
	valueFlag := "last"
	if name == "summary" {
		n, valueFlag = 30, "days"
	}
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		key, value, inline := strings.Cut(args[i], "=")
		if seen[key] || name == "path" {
			return 0, false, false
		}
		seen[key] = true
		switch key {
		case "--json":
			if inline {
				return 0, false, false
			}
		case "--" + valueFlag:
			if !inline {
				i++
				if i >= len(args) {
					return 0, false, false
				}
				value = args[i]
			}
			var err error
			n, err = strconv.ParseInt(value, 10, 64)
			if err != nil || !logsPositive.MatchString(value) || n > 9007199254740991 {
				return 0, false, false
			}
		default:
			return 0, false, false
		}
	}
	return n, seen["--json"], true
}

func logsRead(file string) ([]ledger.Object, bool) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, true // Node treats every read failure as a missing log.
	}
	if !utf8.Valid(data) || logsSurrogate.Match(data) {
		return nil, false // JSON.parse retains lone UTF-16 surrogates.
	}
	entries := []ledger.Object{}
	for _, line := range strings.Split(string(data), "\n") {
		entry, err := ledger.DecodeObject([]byte(line))
		if err != nil {
			if strings.HasPrefix(strings.TrimSpace(line), "{") && json.Valid([]byte(line)) {
				return nil, false // e.g. JSON.parse accepts overflowing numbers.
			}
			continue
		}
		_, ts := entry.Get("ts").(string)
		_, verb := entry.Get("verb").(string)
		if ts && verb {
			entries = append(entries, entry)
		}
	}
	return entries, true
}

func logsJSON(v any) (string, bool) {
	data, err := ledger.Stringify(v, false)
	return string(data) + "\n", err == nil
}

func logsArray(entries []ledger.Object) []any {
	array := make([]any, len(entries))
	for i, entry := range entries {
		array[i] = entry
	}
	return array
}

// ECMAScript trim's whitespace set differs from unicode.IsSpace (notably U+0085).
func logsTrim(s string) string {
	return strings.Trim(s, "\t\n\v\f\r \u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000\ufeff")
}

func logsString(entry ledger.Object, key string) string {
	value, ok := entry.Get(key).(string)
	if ok {
		return value
	}
	if entry.Get(key) == nil {
		for _, field := range entry {
			if field.Key == key {
				return "null"
			}
		}
		return "undefined"
	}
	// Exotic objects/arrays are handed over before formatted output.
	value, _ = logsJSON(entry.Get(key))
	return strings.TrimSuffix(value, "\n")
}

func logsCommand(entry ledger.Object) (string, bool) {
	verb := logsString(entry, "verb")
	sub := entry.Get("sub")
	if sub == nil || sub == "" || sub == false || sub == float64(0) {
		return verb, true
	}
	text, ok := sub.(string)
	return verb + " " + text, ok
}

func logsList(entries []ledger.Object, asJSON bool) (string, bool) {
	if asJSON {
		return logsJSON(ledger.Object{{Key: "errors", Value: logsArray(entries)}})
	}
	if !logsEmptyFixes() {
		return "", false
	}
	var out strings.Builder
	for _, entry := range entries {
		fp, _ := entry.Get("fp").(string)
		command, safe := logsCommand(entry)
		if !logsFP.MatchString(fp) || !safe {
			return "", false // Legacy fingerprint normalization stays in Node.
		}
		message, _ := entry.Get("message").(string)
		first := ""
		for _, line := range strings.Split(message, "\n") {
			if first = logsTrim(line); first != "" {
				break
			}
		}
		if entry.Get("kind") == "warning" {
			warning, ok := entry.Get("warning").(string)
			if !ok {
				warning = "warning"
			}
			fmt.Fprintf(&out, "%s  [warning] %s  %s  %s  %s\n", logsString(entry, "ts"), command, warning, fp, first)
		} else {
			for _, key := range []string{"exit", "kind"} {
				switch entry.Get(key).(type) {
				case ledger.Object, []any:
					return "", false
				}
			}
			fmt.Fprintf(&out, "%s  %s  %s  %s  %s  %s\n", logsString(entry, "ts"), command, logsString(entry, "exit"), logsString(entry, "kind"), fp, first)
		}
	}
	return out.String(), true
}

// Resolve from the installed executable, just as handover resolves the Node
// entry. A nonempty table is delegated so its full validation stays authoritative.
func logsEmptyFixes() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return false
	}
	file := filepath.Join(filepath.Dir(exe), "..", "..", "..", "src", "util", "known-fixes.json")
	data, err := os.ReadFile(file)
	if err != nil {
		return errors.Is(err, fs.ErrNotExist) // Node treats a missing table as empty.
	}
	var table any
	if json.Unmarshal(data, &table) != nil {
		return false
	}
	rows, ok := table.([]any)
	return !ok || len(rows) == 0
}

var logsISO = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,3})?(Z|[+-](0[0-9]|1[0-9]|2[0-3]):[0-5][0-9])$`)

func logsSummary(entries []ledger.Object, days int64, asJSON bool) (string, bool) {
	counts, warnings := []ledger.Object{}, []ledger.Object{}
	indices := []map[string]int{{}, {}}
	since := float64(time.Now().UnixMilli()) - float64(days)*86400000
	for _, entry := range entries {
		ts := logsString(entry, "ts")
		when, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil || !logsISO.MatchString(ts) {
			return "", false // Date.parse's non-ISO grammar belongs to Node.
		}
		if float64(when.UnixMilli()) < since {
			continue
		}
		fp, _ := entry.Get("fp").(string)
		if !logsFP.MatchString(fp) {
			return "", false
		}
		if _, ok := logsCommand(entry); !ok {
			return "", false
		}
		rows, group, kindKey := &counts, 0, "kind"
		if entry.Get("kind") == "warning" {
			rows, group, kindKey = &warnings, 1, "warning"
		}
		index, exists := indices[group][fp]
		if !exists {
			label, ok := entry.Get(kindKey).(string)
			if !ok {
				if group == 0 {
					return "", false
				}
				label = "warning"
			}
			row := ledger.Object{{Key: "fp", Value: fp}, {Key: "verb", Value: entry.Get("verb")}, {Key: "sub", Value: entry.Get("sub")}, {Key: kindKey, Value: label}, {Key: "count", Value: float64(0)}}
			index = len(*rows)
			indices[group][fp] = index
			*rows = append(*rows, row)
		}
		row := &(*rows)[index]
		row.Set("count", row.Get("count").(float64)+1)
	}
	for i, rows := range [][]ledger.Object{counts, warnings} {
		key := "kind"
		if i == 1 {
			key = "warning"
		}
		safe := true
		sort.SliceStable(rows, func(i, j int) bool {
			a, b := rows[i], rows[j]
			if a.Get("count") != b.Get("count") {
				return a.Get("count").(float64) > b.Get("count").(float64)
			}
			for _, field := range []string{"verb", key, "fp"} {
				x, y := logsString(a, field), logsString(b, field)
				if x != y {
					// ASCII lowercase letters have the same order in Node's ICU.
					// Other locale-dependent ties are delegated, never approximated.
					if field != "fp" && (!logsWord.MatchString(x) || !logsWord.MatchString(y)) {
						safe = false
					}
					return x < y
				}
			}
			return false
		})
		if !safe {
			return "", false
		}
	}
	if asJSON {
		return logsJSON(ledger.Object{{Key: "days", Value: days}, {Key: "counts", Value: logsArray(counts)}, {Key: "warnings", Value: logsArray(warnings)}})
	}
	var out strings.Builder
	if len(counts) == 0 {
		fmt.Fprintf(&out, "no errors in the last %d days\n", days)
	}
	for i, rows := range [][]ledger.Object{counts, warnings} {
		if len(rows) == 0 {
			continue
		}
		label, key := "errors", "kind"
		if i == 1 {
			label, key = "warnings", "warning"
		}
		fmt.Fprintf(&out, "%s in the last %d days:\n", label, days)
		for _, row := range rows {
			command, _ := logsCommand(row)
			count := row.Get("count").(float64)
			times := "times"
			if count == 1 {
				times = "time"
			}
			fmt.Fprintf(&out, "%s  %s  %s  seen %.0f %s\n", logsString(row, "fp"), command, logsString(row, key), count, times)
		}
	}
	return out.String(), true
}

func logsBreadcrumb(name string) {
	flag := os.Getenv("CODE_FORGE_NO_ERROR_LOG")
	if flag != "" && flag != "0" {
		return
	}
	file := filepath.Join(os.Getenv("HOME"), ".code-forge", "logs", "breadcrumbs.json")
	var before []json.RawMessage
	data, _ := os.ReadFile(file)
	if json.Unmarshal(data, &before) != nil {
		before = nil
	}
	next := []any{}
	for _, value := range before {
		var crumb string
		if json.Unmarshal(value, &crumb) == nil && logsCrumb.MatchString(crumb) {
			next = append(next, crumb)
		}
	}
	crumb := "logs"
	if name != "list" {
		crumb += " " + name
	}
	next = append(next, crumb)
	if len(next) > 5 {
		next = next[len(next)-5:]
	}
	dir := filepath.Dir(file)
	if os.MkdirAll(dir, 0700) != nil {
		return
	}
	_ = os.Chmod(dir, 0700)
	text, _ := logsJSON(next)
	tmp := fmt.Sprintf("%s.tmp-%d", file, os.Getpid())
	defer os.Remove(tmp)
	if os.WriteFile(tmp, []byte(text), 0600) == nil {
		_ = os.Rename(tmp, file)
	}
}
