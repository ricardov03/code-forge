package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/humanask"
	"github.com/ricardov03/code-forge/native/ledger"
)

const ledgerUsage = "usage: code-forge ledger calibration|outcome|tail --slug <slug> [...]  (tail: [--n|-n <count>]) | add coder --run <r> --block <b> --usd <n> [--note \"...\"] | outside-review --run <r> --pr <n> --rounds <n> --findings <n> [--link <url>] [--block <id>]...\n"
const ledgerTailUsage = "usage: code-forge ledger tail --slug <slug> [--n <count> | -n <count>]\n"

var ledgerSurrogatePattern = regexp.MustCompile(`(?i)\\ud[89a-f][0-9a-f]{2}`)

func init() { routes["ledger"] = runLedger }

var ledgerSubUsage = map[string]string{
	"calibration":    "usage: code-forge ledger calibration --slug <slug> [--question <id>]\n",
	"outcome":        "usage: code-forge ledger outcome --scan-git --slug <slug> [--cwd <dir>] [--days <n>]\nusage: code-forge ledger outcome --pr <n> --ci red|green|reverted --slug <slug>\n",
	"tail":           ledgerTailUsage,
	"add":            "usage: code-forge ledger add coder --run <r> --block <b> --usd <n> [--note \"...\"]\n",
	"outside-review": ledgerOutsideReviewUsage + "\n",
}

const ledgerOutsideReviewUsage = "usage: code-forge ledger outside-review --run <r> --pr <n> --rounds <n> --findings <n> [--link <url>] [--block <id>]..."

func ledgerHandover(args []string, reason handoverReason) int {
	code, err := handover(append([]string{"ledger"}, args...), reason)
	if err != nil {
		fmt.Fprintln(os.Stderr, "code-forge-native:", err)
	}
	return code
}

// The Node parser uses the first exact flag occurrence, and only a double dash
// disqualifies the following token as a value (so -n -1 is a bad count).
func ledgerFlag(args []string, flag string) (string, bool, bool) {
	for i, arg := range args {
		if arg == flag {
			if i+1 == len(args) || strings.HasPrefix(args[i+1], "--") {
				return "", true, false
			}
			return args[i+1], true, true
		}
	}
	return "", false, true
}

// ledgerValue is Node's flagValue: a usable value, or "" with false.
func ledgerValue(args []string, flag string) (string, bool) {
	value, given, ok := ledgerFlag(args, flag)
	return value, given && ok
}

// ledgerResult is a finished ledger command: what Node would print, or the
// reason this case still runs in Node. Nothing is written before a hand-over.
type ledgerResult struct {
	code     int
	out, err string
	handover handoverReason
	secret   string // a run key registered for redaction, as Node's loadKey does
}

func ledgerHandedOver(reason handoverReason) ledgerResult {
	return ledgerResult{handover: reason}
}

func ledgerHelpFlag(s string) bool { return s == "--help" || s == "-h" }

// runLedger follows the Node router: help is metadata (no breadcrumb, no
// log); every other run notes a breadcrumb and logs a non-zero exit.
func runLedger(args []string) int {
	if len(args) > 0 && ledgerHelpFlag(args[0]) {
		fmt.Fprint(os.Stdout, ledgerUsage)
		return 0
	}
	if len(args) > 1 && ledgerHelpFlag(args[1]) {
		if usage, ok := ledgerSubUsage[args[0]]; ok {
			fmt.Fprint(os.Stdout, usage)
			return 0
		}
	}
	result := ledgerRun(args)
	if result.handover == 0 && result.code != 0 && result.code != 2 && ledgerHintShown() {
		// Node prints a once-a-day hint on a terminal; that memory stays in Node.
		result = ledgerHandedOver(hLedgerErrorHint)
	}
	if result.handover != 0 {
		return ledgerHandover(args, result.handover)
	}
	sub := ""
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			if arg == "tail" || arg == "calibration" || arg == "outcome" {
				sub = arg
			}
			break
		}
	}
	crumb := "ledger"
	if sub != "" {
		crumb += " " + sub
	}
	before := gatesBreadcrumbName(crumb)
	out, errText := ledgerRedact(result.out, result.secret), ledgerRedact(result.err, result.secret)
	fmt.Fprint(os.Stdout, out)
	fmt.Fprint(os.Stderr, errText)
	if result.code != 0 {
		var message any
		if trimmed := strings.TrimFunc(errText, errorLogSpace); trimmed != "" {
			message = trimmed
		}
		nativeVerbFailure("ledger", sub, args, result.code, message, before)
	}
	return result.code
}

// ledgerHintShown: Node's maybeHint could print on this run (stderr is a
// terminal, the log is on, not an agent session).
func ledgerHintShown() bool {
	if !reviewWaitLogging() || os.Getenv("HOME") == "" || !humanask.IsTTY(os.Stderr) {
		return false
	}
	for _, name := range []string{"CLAUDECODE", "CLAUDE_CODE", "CURSOR_AGENT", "AI_AGENT"} {
		if os.Getenv(name) != "" {
			return false
		}
	}
	return true
}

// ledgerRedact is Node's redact for one registered secret: every occurrence,
// overlapping ones merged, becomes one [REDACTED].
func ledgerRedact(text, secret string) string {
	if secret == "" {
		return text
	}
	type span struct{ a, b int }
	spans := []span{}
	for at := 0; at < len(text); {
		next := strings.Index(text[at:], secret)
		if next < 0 {
			break
		}
		spans = append(spans, span{at + next, at + next + len(secret)})
		at += next + 1
	}
	if len(spans) == 0 {
		return text
	}
	var out strings.Builder
	cursor, start, end := 0, spans[0].a, spans[0].b
	for _, s := range spans[1:] {
		if s.a <= end {
			if s.b > end {
				end = s.b
			}
			continue
		}
		out.WriteString(text[cursor:start] + "[REDACTED]")
		cursor, start, end = end, s.a, s.b
	}
	out.WriteString(text[cursor:start] + "[REDACTED]" + text[end:])
	return out.String()
}

func ledgerRun(args []string) ledgerResult {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	if sub == "add" {
		return ledgerAdd(args[1:])
	}
	if sub == "outside-review" {
		return ledgerOutsideReview(args[1:])
	}
	usage := ledgerResult{code: 2, err: ledgerUsage}
	slug, ok := ledgerValue(args, "--slug")
	if !ok || slug == "" {
		return usage
	}
	failed := func(message string) ledgerResult {
		return ledgerResult{code: 1, err: "ledger " + sub + ": " + message + "\n"}
	}
	switch {
	case sub == "calibration":
		return ledgerCalibration(args, slug, failed)
	case sub == "outcome" && ledgerHas(args, "--scan-git"):
		return ledgerScanGit(args, slug, failed)
	case sub == "outcome" && ledgerHasValue(args, "--pr") && ledgerHasValue(args, "--ci"):
		return ledgerCIOutcome(args, slug, failed)
	case sub == "tail":
		return ledgerTailCommand(args, slug)
	}
	return usage
}

func ledgerHas(args []string, flag string) bool {
	for _, arg := range args {
		if arg == flag {
			return true
		}
	}
	return false
}

func ledgerHasValue(args []string, flag string) bool {
	value, ok := ledgerValue(args, flag)
	return ok && value != ""
}

func ledgerTailCommand(args []string, slug string) ledgerResult {
	long, hasLong, longOK := ledgerFlag(args, "--n")
	short, hasShort, shortOK := ledgerFlag(args, "-n")
	switch {
	case !longOK:
		return ledgerResult{code: 2, err: "ledger tail: --n requires a value\n"}
	case !shortOK:
		return ledgerResult{code: 2, err: "ledger tail: -n requires a value\n"}
	case hasLong && hasShort:
		return ledgerResult{code: 2, err: "ledger tail: give the count once, as --n <count> or -n <count>\n"}
	}
	count := "20"
	if hasLong {
		count = long
	} else if hasShort {
		count = short
	}
	if !regexp.MustCompile(`^[0-9]+$`).MatchString(count) {
		return ledgerResult{code: 2, err: "ledger tail: --n/-n must be a non-negative integer, got \"" + count + "\"\n"}
	}
	n, _ := strconv.ParseFloat(count, 64) // JS parseInt also permits +Infinity.
	code, output, message, supported := ledgerTail(slug, n)
	if !supported {
		return ledgerHandedOver(hLedgerTailFormat)
	}
	return ledgerResult{code: code, out: output, err: message}
}

// ledgerMissing is Node's readExistingRows check before reading: a bad slug or
// an absent ledger gives an error message; ok false means hand over.
func ledgerMissing(slug string) (dir, message string, ok bool) {
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`).MatchString(slug) {
		return "", ledgerSlugError(slug), true
	}
	home := os.Getenv("HOME")
	if home == "" {
		return "", "", false
	}
	dir = filepath.Join(home, ".code-forge", "ledger")
	_, err := os.Stat(filepath.Join(dir, slug+".jsonl"))
	if err == nil {
		return dir, "", true
	}
	if !os.IsNotExist(err) {
		return "", "", false
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil && !os.IsNotExist(readErr) {
		return "", "", false
	}
	known := map[string]bool{}
	pattern := regexp.MustCompile(`^([a-z0-9][a-z0-9-]*)(?:\.[0-9]+)?\.jsonl$`)
	for _, entry := range entries {
		if match := pattern.FindStringSubmatch(entry.Name()); entry.Type().IsRegular() && match != nil {
			known[match[1]] = true
		}
	}
	slugs := []string{}
	for name := range known {
		slugs = append(slugs, name)
	}
	sort.Strings(slugs)
	names := strings.Join(slugs, ", ")
	if names == "" {
		names = "none"
	}
	return "", fmt.Sprintf("no ledger for slug \"%s\"; known slugs: %s", slug, names), true
}

func ledgerSlugError(slug string) string {
	quoted, _ := ledger.Stringify(slug, false)
	return fmt.Sprintf("ledgerPath: slug must match /^[a-z0-9][a-z0-9-]*$/ (got %s)", quoted)
}

func ledgerTail(slug string, n float64) (int, string, string, bool) {
	dir, message, ok := ledgerMissing(slug)
	if !ok {
		return 0, "", "", false
	}
	if message != "" {
		return 1, "", "ledger tail: " + message + "\n", true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, "", "", false
	}
	pattern := regexp.MustCompile(`^` + regexp.QuoteMeta(slug) + `\.([0-9]+)\.jsonl$`)
	names := []string{}
	for _, entry := range entries {
		if pattern.MatchString(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	sort.SliceStable(names, func(i, j int) bool {
		a, _ := strconv.ParseFloat(pattern.FindStringSubmatch(names[i])[1], 64)
		b, _ := strconv.ParseFloat(pattern.FindStringSubmatch(names[j])[1], 64)
		return a < b
	})
	names = append(names, slug+".jsonl")
	rows := []string{}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !utf8.Valid(data) {
			return 0, "", "", false
		}
		lines := []string{}
		for _, line := range strings.Split(string(data), "\n") {
			if line != "" {
				lines = append(lines, line)
			}
		}
		for i, line := range lines {
			if !json.Valid([]byte(line)) {
				if i == len(lines)-1 {
					continue
				}
				return 0, "", "", false // Node owns its precise JSON syntax errors.
			}
			// The existing ordered ledger decoder also supports scalar/array rows
			// through this envelope, without losing nested property order.
			if ledgerSurrogatePattern.MatchString(line) {
				return 0, "", "", false
			}
			obj, err := ledger.DecodeObject([]byte(`{"row":` + line + `}`))
			if err != nil {
				return 0, "", "", false
			}
			encoded, err := ledger.Stringify(obj.Get("row"), false)
			if err != nil {
				return 0, "", "", false
			}
			rows = append(rows, string(encoded)+"\n")
		}
	}
	start := 0
	if n < float64(len(rows)) {
		start = len(rows) - int(n)
	}
	return 0, strings.Join(rows[start:], ""), "", true
}
