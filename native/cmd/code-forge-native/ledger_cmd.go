package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/ledger"
)

const ledgerUsage = "usage: code-forge ledger calibration|outcome|tail --slug <slug> [...]  (tail: [--n|-n <count>]) | add coder --run <r> --block <b> --usd <n> [--note \"...\"] | outside-review --run <r> --pr <n> --rounds <n> --findings <n> [--link <url>] [--block <id>]...\n"
const ledgerTailUsage = "usage: code-forge ledger tail --slug <slug> [--n <count> | -n <count>]\n"

var ledgerSurrogatePattern = regexp.MustCompile(`(?i)\\ud[89a-f][0-9a-f]{2}`)
var ledgerBreadcrumbPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}(?: [a-z][a-z0-9-]{0,39})?$`)

func init() { routes["ledger"] = runLedger }

func ledgerHandover(args []string) int {
	code, err := handover(append([]string{"ledger"}, args...))
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

func runLedger(args []string) int {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	// Keep unported subcommands intact, including their help and argument errors.
	switch sub {
	case "calibration", "outcome", "add", "outside-review":
		return ledgerHandover(args)
	}
	help := func(s string) bool { return s == "--help" || s == "-h" }
	if help(sub) {
		fmt.Fprint(os.Stdout, ledgerUsage)
		return 0
	}
	if len(args) > 1 && help(args[1]) && sub == "tail" {
		fmt.Fprint(os.Stdout, ledgerTailUsage)
		return 0
	}
	code, output, message := 2, "", ledgerUsage
	slug, _, validSlug := ledgerFlag(args, "--slug")
	if validSlug && slug != "" && sub == "tail" {
		long, hasLong, longOK := ledgerFlag(args, "--n")
		short, hasShort, shortOK := ledgerFlag(args, "-n")
		switch {
		case !longOK:
			message = "ledger tail: --n requires a value\n"
		case !shortOK:
			message = "ledger tail: -n requires a value\n"
		case hasLong && hasShort:
			message = "ledger tail: give the count once, as --n <count> or -n <count>\n"
		default:
			count := "20"
			if hasLong {
				count = long
			} else if hasShort {
				count = short
			}
			if !regexp.MustCompile(`^[0-9]+$`).MatchString(count) {
				message = "ledger tail: --n/-n must be a non-negative integer, got \"" + count + "\"\n"
			} else {
				n, _ := strconv.ParseFloat(count, 64) // JS parseInt also permits +Infinity.
				var supported bool
				code, output, message, supported = ledgerTail(slug, n)
				if !supported {
					return ledgerHandover(args)
				}
			}
		}
	}
	// Resolve all fallback decisions before emitting output or modifying state.
	if !ledgerLoggingOff() {
		if !ledgerLog(args, code, message) {
			return ledgerHandover(args)
		}
	}
	fmt.Fprint(os.Stdout, output)
	fmt.Fprint(os.Stderr, message)
	return code
}

func ledgerTail(slug string, n float64) (int, string, string, bool) {
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`).MatchString(slug) {
		quoted, _ := ledger.Stringify(slug, false)
		return 1, "", fmt.Sprintf("ledger tail: ledgerPath: slug must match /^[a-z0-9][a-z0-9-]*$/ (got %s)\n", quoted), true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return 0, "", "", false
	}
	dir := filepath.Join(home, ".code-forge", "ledger")
	_, err = os.Stat(filepath.Join(dir, slug+".jsonl"))
	if os.IsNotExist(err) {
		entries, readErr := os.ReadDir(dir)
		if readErr != nil && !os.IsNotExist(readErr) {
			return 0, "", "", false
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
		return 1, "", fmt.Sprintf("ledger tail: no ledger for slug \"%s\"; known slugs: %s\n", slug, names), true
	}
	if err != nil {
		return 0, "", "", false
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

func ledgerLoggingOff() bool {
	flag := os.Getenv("CODE_FORGE_NO_ERROR_LOG")
	return flag != "" && flag != "0"
}

// Node's error entry includes its runtime version. Read installed headers rather
// than starting Node; installations without matching metadata retain Node's logger.
func ledgerNodeVersion() string {
	node := os.Getenv("CODE_FORGE_NODE")
	if node == "" {
		node = "node"
	}
	file, err := exec.LookPath(node)
	if err != nil {
		file, err = exec.LookPath("node")
	}
	if err != nil {
		return ""
	}
	file, err = filepath.EvalSymlinks(file)
	if err != nil {
		return ""
	}
	header, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "include", "node", "node_version.h"))
	if err != nil {
		return ""
	}
	parts := []string{}
	for _, part := range []string{"MAJOR", "MINOR", "PATCH"} {
		match := regexp.MustCompile(`(?m)^#define NODE_` + part + `_VERSION ([0-9]+)`).FindSubmatch(header)
		if match == nil {
			return ""
		}
		parts = append(parts, string(match[1]))
	}
	return "v" + strings.Join(parts, ".")
}

// Logging is best effort, like the Node router. Complex scrub contexts stay with
// Node until its general-purpose telemetry is ported; no state is touched first.
func ledgerLog(args []string, code int, message string) bool {
	home := os.Getenv("HOME")
	if home == "" {
		return true
	}
	nodeVersion := ""
	cwd, _ := os.Getwd()
	slug := ""
	if code != 0 {
		nodeVersion = ledgerNodeVersion()
		if nodeVersion == "" || !regexp.MustCompile(`^[A-Za-z0-9 \t\n.,:;|()/<>?\[\]"_=*^$-]*$`).MatchString(message) {
			return false
		}
		// Preserve Node's path/reference/id scrubbing for contexts outside this
		// port, rather than publishing an incompletely scrubbed error entry.
		if strings.Contains(message, "op://") || regexp.MustCompile(`[a-z0-9]{26}`).MatchString(message) ||
			len(home) >= 2 && strings.Contains(message, home) || len(cwd) >= 2 && strings.Contains(message, cwd) {
			return false
		}
		if cfg, err := config.LoadFile(filepath.Join(cwd, config.Filename)); err == nil {
			if project, ok := cfg["project"].(map[string]any); ok {
				slug, _ = project["slug"].(string)
			}
		} else if failure, ok := err.(*config.Error); ok && failure.Kind == "migrate-error" {
			return false
		}
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
	dir := filepath.Join(home, ".code-forge", "logs")
	crumbFile := filepath.Join(dir, "breadcrumbs.json")
	before := []string{}
	var old []any
	if data, err := os.ReadFile(crumbFile); err == nil && json.Unmarshal(data, &old) == nil {
		for _, v := range old {
			if s, ok := v.(string); ok && ledgerBreadcrumbPattern.MatchString(s) {
				before = append(before, s)
			}
		}
	}
	if len(before) > 5 {
		before = before[len(before)-5:]
	}
	crumb := "ledger"
	if sub != "" {
		crumb += " " + sub
	}
	next := append(append([]string{}, before...), crumb)
	if len(next) > 5 {
		next = next[len(next)-5:]
	}
	if os.MkdirAll(dir, 0700) == nil {
		_ = os.Chmod(dir, 0700)
		data, _ := json.Marshal(next)
		ledgerLogReplace(crumbFile, append(data, '\n'))
	}
	if code == 0 {
		return true
	}
	// For the supported ASCII messages only project names can need scrubbing.
	cleaned := []any{}
	count := 0
	clean := func(s string) string {
		if slug == "" {
			return s
		}
		re := regexp.MustCompile(regexp.QuoteMeta(slug))
		indices := re.FindAllStringIndex(s, -1)
		var out strings.Builder
		cursor := 0
		word := func(c byte) bool {
			return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
		}
		for _, span := range indices {
			a, b := span[0], span[1]
			if a > 0 && (word(s[a-1]) || s[a-1] == '<') || b < len(s) && (word(s[b]) || s[b] == '>') || strings.HasPrefix(s[b:], "://") {
				continue
			}
			out.WriteString(s[cursor:a])
			out.WriteString("<slug>")
			cursor = b
			count++
		}
		out.WriteString(s[cursor:])
		return out.String()
	}
	text := clean(strings.TrimSpace(message))
	if len(text) > 2048 {
		text = text[:2048]
	}
	flags := []string{}
	afterLong := false
	longFlag := regexp.MustCompile(`^--([A-Za-z][A-Za-z0-9-]{0,39})(=|$)`)
	shortFlag := regexp.MustCompile(`^-([A-Za-z])`)
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if m := longFlag.FindStringSubmatch(arg); m != nil {
			flags = append(flags, clean("--"+m[1]))
			afterLong = m[2] == ""
			continue
		}
		if m := shortFlag.FindStringSubmatch(arg); m != nil && !afterLong {
			flags = append(flags, clean("-"+m[1]))
		}
		afterLong = false
	}
	verb := clean("ledger")
	if count > 0 {
		cleaned = append(cleaned, ledger.Object{{Key: "rule", Value: "project_name"}, {Key: "count", Value: count}})
	}
	kind := "error"
	if code == 2 {
		kind = "usage"
	}
	normal := regexp.MustCompile(`"[^"\n]*"|<[^<>\n]*>|\S*/\S*`).ReplaceAllString(text, " ")
	normal = strings.Join(strings.Fields(normal), " ")
	fp := fmt.Sprintf("%x", sha256.Sum256([]byte(verb+"\n"+sub+"\n"+kind+"\n"+normal)))[:12]
	var subValue any
	if sub != "" {
		subValue = sub
	}
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	entry := ledger.Object{
		{Key: "ts", Value: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")},
		{Key: "version", Value: version}, {Key: "node", Value: nodeVersion},
		{Key: "platform", Value: runtime.GOOS}, {Key: "arch", Value: arch},
		{Key: "verb", Value: verb}, {Key: "sub", Value: subValue}, {Key: "flags", Value: flags},
		{Key: "exit", Value: code}, {Key: "kind", Value: kind}, {Key: "message", Value: text},
		{Key: "stack", Value: nil}, {Key: "cleaned", Value: cleaned}, {Key: "fp", Value: fp}, {Key: "before", Value: before},
	}
	data, err := ledger.Stringify(entry, false)
	if err != nil {
		return true
	}
	file := filepath.Join(dir, "errors.jsonl")
	f, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return true
	}
	_, err = f.Write(append(data, '\n'))
	_ = f.Close()
	if err == nil {
		if data, err := os.ReadFile(file); err == nil && len(data) > 1024*1024 {
			lines := strings.FieldsFunc(string(data), func(r rune) bool { return r == '\n' })
			used, first := 0, len(lines)
			for first > 0 {
				size := len(lines[first-1]) + 1
				if first < len(lines) && used+size > len(data)/2 {
					break
				}
				first--
				used += size
			}
			ledgerLogReplace(file, []byte(strings.Join(lines[first:], "\n")+"\n"))
		}
	}
	return true
}

func ledgerLogReplace(file string, data []byte) {
	tmp := fmt.Sprintf("%s.tmp-%d", file, os.Getpid())
	if os.WriteFile(tmp, data, 0600) == nil {
		_ = os.Rename(tmp, file)
	}
	_ = os.Remove(tmp)
}
