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

	"github.com/ricardov03/code-forge/native/humanask"
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

const logsUsage = "usage: code-forge logs [--last N] [--json] | logs summary [--days N] [--json] | logs clear [--yes] | logs path\n" +
	"       code-forge logs report [--last N] [--kind K] [--verb V] [--note \"text\"] [--include-warnings] [--with-doctor] [--no-ai] [--allow-old] [--force] [--dry-run] [--yes]\n"

var logsSubUsage = map[string]string{
	"summary": "usage: code-forge logs summary [--days N] [--json]\n",
	"clear":   "usage: code-forge logs clear [--yes]\n",
	"path":    "usage: code-forge logs path\n",
	"report":  "usage: code-forge logs report [--last N] [--kind K] [--verb V] [--note \"text\"] [--include-warnings] [--with-doctor] [--no-ai] [--allow-old] [--force] [--dry-run] [--yes]\n",
}

// logsSpecs is Node's parseFlags spec per subcommand: value flags, then booleans.
var logsSpecs = map[string][2][]string{
	"list":    {{"last"}, {"json"}},
	"summary": {{"days"}, {"json"}},
	"clear":   {nil, {"yes"}},
	"path":    {nil, nil},
	"report":  {{"last", "kind", "verb", "note"}, {"dry-run", "yes", "no-ai", "allow-old", "include-warnings", "with-doctor", "force"}},
}

func logsHelpFlag(s string) bool { return s == "--help" || s == "-h" }

func runLogs(args []string) int {
	if len(args) > 0 && args[0] == "handovers" {
		return runHandoverLogs(args[1:])
	}
	// The Node router answers help before parsing or touching any state.
	if len(args) > 0 && logsHelpFlag(args[0]) {
		fmt.Fprint(os.Stdout, logsUsage)
		return 0
	}
	if len(args) > 1 && logsHelpFlag(args[1]) {
		if usage, ok := logsSubUsage[args[0]]; ok {
			fmt.Fprint(os.Stdout, usage)
			return 0
		}
	}
	for _, arg := range args {
		if !utf8.ValidString(arg) {
			return logsHandover(args, hLogsFlagsOrHome) // Node sees U+FFFD in argv
		}
	}
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub = args[0]
	}
	crumb, logSub := "logs", ""
	if _, known := logsSubUsage[sub]; known {
		crumb, logSub = "logs "+sub, sub
	}
	fail := func(code int, text string) int {
		before := gatesBreadcrumbName(crumb)
		fmt.Fprint(os.Stderr, text)
		var message any
		if trimmed := strings.TrimFunc(text, errorLogSpace); trimmed != "" {
			message = trimmed
		}
		nativeVerbFailure("logs", logSub, args, code, message, before)
		return code
	}
	name, rest := "list", args
	if sub != "" {
		name, rest = sub, args[1:]
	}
	spec, known := logsSpecs[name]
	if !known || name == "list" && sub != "" {
		quoted, _ := ledger.Stringify(sub, false)
		return fail(2, "logs: unknown subcommand "+string(quoted)+"\n"+logsUsage)
	}
	flags, message := logsFlags(rest, spec)
	if message != "" {
		return fail(2, "logs: "+message+"\n"+logsUsage)
	}
	if os.Getenv("HOME") == "" {
		return fail(1, "logs: HOME is not set; there is no error log\n")
	}
	n := int64(0)
	if value, ok := flags["days"]; ok {
		n, _ = strconv.ParseInt(value, 10, 64)
	} else if value, ok := flags["last"]; ok {
		n, _ = strconv.ParseInt(value, 10, 64)
	} else if name == "summary" {
		n = 30
	} else if name == "report" {
		n = 5
	} else {
		n = 10
	}
	_, asJSON := flags["json"]
	file := filepath.Join(os.Getenv("HOME"), ".code-forge", "logs", "errors.jsonl")
	if name == "clear" {
		return logsClear(args, file, flags, crumb, fail)
	}
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
			ok := true
			switch name {
			case "summary":
				output, ok = logsSummary(entries, n, asJSON)
			case "report":
				if logsReportPicks(entries, flags, n) {
					return logsReport(args, flags, entries, n, crumb) // B554
				}
				output = "no matching errors to report\n"
			default:
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
	gatesBreadcrumbName(crumb)
	fmt.Fprint(os.Stdout, output)
	return 0
}

// logsReportPicks reports whether `logs report` selects any entry (newest
// first): warnings only with --include-warnings, then --kind and --verb.
func logsReportPicks(newest []ledger.Object, flags map[string]string, last int64) bool {
	_, warnings := flags["include-warnings"]
	kind, byKind := flags["kind"]
	verb, byVerb := flags["verb"]
	for _, entry := range newest {
		entryKind, kindString := entry.Get("kind").(string)
		if !warnings && kindString && entryKind == "warning" {
			continue
		}
		if byKind && (!kindString || entryKind != kind) {
			continue
		}
		if byVerb && logsString(entry, "verb") != verb {
			continue
		}
		return last > 0
	}
	return false
}

// Seams: Node asks only when stdin is a terminal (process.stdin.isTTY);
// clack draws on stdout, so the Go prompt needs both ends (humanask).
var (
	logsStdinTTY = func() bool { return humanask.IsTTY(os.Stdin) }
	logsAskTTY   = humanask.Available
	logsConfirm  = humanask.Confirm
)

// logsClear is Node's clear: a missing log is nothing to do, --yes deletes it,
// no terminal refuses, a terminal asks (clack confirm, No first). A prompt
// Go cannot draw like clack (stdout not a terminal) stays in Node.
func logsClear(args []string, file string, flags map[string]string, crumb string, fail func(int, string) int) int {
	if _, err := os.ReadFile(file); err != nil {
		gatesBreadcrumbName(crumb)
		fmt.Fprint(os.Stdout, "no errors logged\n")
		return 0
	}
	if _, yes := flags["yes"]; !yes {
		if !logsStdinTTY() {
			return fail(2, "logs: no terminal to confirm; nothing deleted — pass --yes to delete the error log\n")
		}
		entries, safe := logsRead(file)
		if !logsAskTTY() || !safe || !routerHintReadable() {
			return logsHandover(args, hLogsClearPrompt)
		}
		before := gatesBreadcrumbName(crumb)
		answer, err := logsConfirm(fmt.Sprintf("Delete the error log (%d entries)?", len(entries)))
		if err != nil || answer == humanask.NoTTY {
			// Nothing was answered; Node asks again from the start.
			return logsHandover(args, hLogsClearPrompt)
		}
		if answer != humanask.Confirmed {
			fmt.Fprint(os.Stdout, "cancelled — nothing deleted\n")
			routerHint(nativeVerbFailure("logs", "clear", args, 1, nil, before), 1)
			return 1
		}
	} else {
		gatesBreadcrumbName(crumb)
	}
	if err := os.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return logsHandover(args, hLogsClearPrompt)
	}
	fmt.Fprint(os.Stdout, "deleted "+file+"\n")
	return 0
}

// logsFlags is Node's parseFlags with intFlag for --last/--days; a non-empty
// message is Node's usage error.
func logsFlags(args []string, spec [2][]string) (map[string]string, string) {
	has := func(list []string, name string) bool {
		for _, item := range list {
			if item == name {
				return true
			}
		}
		return false
	}
	flags := map[string]string{}
	positionals := []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			positionals = append(positionals, arg)
			continue
		}
		name, inline, hasInline := strings.Cut(arg[2:], "=")
		if !has(spec[0], name) && !has(spec[1], name) {
			quoted, _ := ledger.Stringify(arg, false)
			return nil, "unknown flag " + string(quoted)
		}
		if _, seen := flags[name]; seen {
			return nil, "--" + name + " given more than once"
		}
		switch {
		case has(spec[1], name):
			if hasInline {
				return nil, "--" + name + " takes no value"
			}
			flags[name] = ""
		case hasInline:
			if inline == "" {
				return nil, "--" + name + " needs a value"
			}
			flags[name] = inline
		default:
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return nil, "--" + name + " needs a value"
			}
			flags[name] = args[i+1]
			i++
		}
	}
	if len(positionals) > 0 {
		quoted, _ := ledger.Stringify(positionals[0], false)
		return nil, "unexpected argument " + string(quoted)
	}
	for _, name := range []string{"last", "days"} {
		_, isLast := flags["last"]
		if name == "days" && isLast {
			break // Node reads flags.last ?? flags.days
		}
		if value, given := flags[name]; given {
			n, err := strconv.ParseInt(value, 10, 64)
			if !logsPositive.MatchString(value) || err != nil || n > 9007199254740991 {
				label := "last"
				if _, days := flags["days"]; days {
					label = "days"
				}
				return nil, "--" + label + " must be a positive integer"
			}
			break
		}
	}
	return flags, ""
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
