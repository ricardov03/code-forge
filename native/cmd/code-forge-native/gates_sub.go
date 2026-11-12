package main

// B459: the gates subcommands other than run (detect, secret-scan, safe-edit,
// scope, acceptance, transcript-grep), the launcher's gates help and the
// gates usage error, ported from src/cli/gates.mjs and src/gates/*.mjs.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"os/user"
	"path"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/childreg"
	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/contextpack"
	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
)

const gatesEnvRule = "code-forge never prints or records an env value. Gate env is not a secret store. Child output is masked only for values of 8 or more characters."

const gatesUsage = "usage: code-forge gates detect|run|secret-scan|safe-edit|scope|acceptance|transcript-grep --cwd <dir> [...]\n" +
	"  gates run: an argv item exactly {test_files} runs the block's related tests (needs --run and --block); without --changed, over gates.full_suite_threshold_files owned files, or with no test file known, the full suite runs.\n" +
	"  gates run: a gate may be {argv, env}. " + gatesEnvRule + "\n" +
	"  gates run --run --block: when the block's base branch moved past its recorded base, no gate runs (exit 1) and the fix is printed: code-forge block rebase <id> --run <r> --accept-landed. --auto-rebase runs that rebase first; --allow-stale-base gates on the old base with a WARN line.\n" +
	"  gates run --run --block --changed: the Go test selection (the block's tests, tests that import a changed file); never the full suite; an empty selection skips the test gate.\n" +
	"  gates run --run --block --full: the only local full suite of a block (scope full, reason explicit); cannot go with --changed.\n" +
	"  gates run --run --block: the close-blocker preflight runs first by default; --no-preflight skips it, --preflight asks for it.\n"

var gatesSubUsage = map[string]string{
	"detect":          "usage: code-forge gates detect [--cwd <dir>]\n",
	"run":             "usage: code-forge gates run [--cwd <dir>] [--timeout-ms <n>] [--slug <slug>] [--run <id>] [--block <id>] [--changed | --full] [--auto-rebase | --allow-stale-base] [--preflight | --no-preflight]\n",
	"secret-scan":     "usage: code-forge gates secret-scan [--cwd <dir>] --file <path> [--file <path>...]\n",
	"safe-edit":       "usage: code-forge gates safe-edit [--cwd <dir>] --base <sha>\n",
	"scope":           "usage: code-forge gates scope [--cwd <dir>] --base <sha>\n",
	"acceptance":      "usage: code-forge gates acceptance --clauses <file.json> --tap <file>\n",
	"transcript-grep": "usage: code-forge gates transcript-grep --file <path> [--cwd <dir>]\n",
}

// gatesKnownSubs is breadcrumbs.mjs SUBCOMMANDS.gates.
var gatesKnownSubs = []string{"detect", "run", "secret-scan", "safe-edit", "scope", "acceptance", "transcript-grep"}

// gatesHelp is the launcher's help: it answers before any breadcrumb or log.
func gatesHelp(args []string, stdout io.Writer) (int, bool) {
	help := func(i int) bool { return len(args) > i && (args[i] == "--help" || args[i] == "-h") }
	if help(0) {
		fmt.Fprint(stdout, gatesUsage)
		return 0, true
	}
	if help(1) {
		if usage, ok := gatesSubUsage[args[0]]; ok {
			fmt.Fprint(stdout, usage)
			return 0, true
		}
	}
	return 0, false
}

// gatesSubOf is error-log.mjs subcommandOf for gates.
func gatesSubOf(args []string) string {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			for _, known := range gatesKnownSubs {
				if a == known {
					return a
				}
			}
			return ""
		}
	}
	return ""
}

// gatesHand asks for the Node hand-over with a named reason; nothing was
// printed or written before it.
type gatesHand struct{ reason handoverReason }

func (h *gatesHand) Error() string { return "hand over: " + handoverReasons[h.reason] }

// gatesThrown is an error Node throws inside runGatesVerb: "gates <sub>: <message>", exit 1.
type gatesThrown struct{ message string }

func (t *gatesThrown) Error() string { return t.message }

func gatesThrow(format string, args ...any) error {
	return &gatesThrown{fmt.Sprintf(format, args...)}
}

// gatesOther runs every gates form but run and classes, as Node's launcher and runGatesVerb.
func gatesOther(args []string) int {
	childEnv := os.Environ()
	if os.Getenv("HOME") == "" {
		// Node's os.homedir() reads the user database when HOME is unset.
		if u, err := user.Current(); err == nil && u.HomeDir != "" {
			_ = os.Setenv("HOME", u.HomeDir)
		}
	}
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	var value ledger.Object
	code := 0
	var err error
	switch sub {
	case "detect":
		value, code, err = gatesDetectSub(args)
	case "secret-scan":
		value, code, err = gatesSecretScanSub(args)
	case "safe-edit":
		value, code, err = gatesSafeEditSub(args, childEnv)
	case "scope":
		value, code, err = gatesScopeSub(args, childEnv)
	case "acceptance":
		value, code, err = gatesAcceptanceSub(args)
	case "transcript-grep":
		value, code, err = gatesTranscriptSub(args)
	default:
		gatesErrf("%s", gatesUsage)
		code = 2
	}
	var hand *gatesHand
	if errors.As(err, &hand) {
		code, e := handover(append([]string{"gates"}, args...), hand.reason)
		if e != nil {
			fmt.Fprintln(os.Stderr, "code-forge-native:", e)
		}
		return code
	}
	if err != nil {
		gatesErrf("gates %s: %s\n", sub, err.Error())
		code = 1
	} else if value != nil {
		data, _ := ledger.Stringify(value, false)
		os.Stdout.Write(append(data, '\n'))
	}
	name := "gates"
	known := gatesSubOf(args)
	if known != "" {
		name += " " + known
	}
	before := gatesBreadcrumbName(name)
	if code != 0 {
		nativeVerbFailure("gates", known, args, code, gatesLastMessage, before)
	}
	return code
}

// gatesCwdArg is resolveCwd: a bare --cwd is a usage error; no --cwd is the process directory.
func gatesCwdArg(args []string, label string) (string, bool) {
	v, given, ok := gatesFlag(args, "--cwd")
	if !ok {
		gatesErrf("%s: --cwd requires a value\n", label)
		return "", false
	}
	if !given {
		v, _ = os.Getwd()
	}
	return v, true
}

// gatesFlagValue is flagValue: the value, or "" when absent or malformed.
func gatesFlagValue(args []string, key string) string {
	v, _, ok := gatesFlag(args, key)
	if !ok {
		return ""
	}
	return v
}

// gatesNodeJoin is Node's path.join of two parts (posix): a trailing slash stays.
func gatesNodeJoin(a, b string) string {
	parts := []string{}
	for _, p := range []string{a, b} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	joined := strings.Join(parts, "/")
	if joined == "" {
		return "."
	}
	clean := path.Clean(joined)
	if strings.HasSuffix(joined, "/") && clean != "/" {
		clean += "/"
	}
	return clean
}

// gatesReadFile reads a file as Node's readFile; an fs error Go cannot word as
// Node does hands over.
func gatesReadFile(name string) ([]byte, string, error) {
	data, err := os.ReadFile(name)
	if err == nil {
		return data, "", nil
	}
	code := contextpack.Errno(err)
	var pe *os.PathError
	if code == "error" || !errors.As(err, &pe) {
		return nil, "", &gatesHand{hGatesFsError}
	}
	return nil, code, gatesThrow("%s", contextpack.NodeError(err))
}

// gatesJSSpace is JavaScript's \s and String.prototype.trim white space.
func gatesJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

func gatesJSTrim(s string) string { return strings.TrimFunc(s, gatesJSSpace) }

const gatesJSSpaceClass = `[\t\n\x{0B}\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]`

// --- detect ---------------------------------------------------------------

func gatesDetectSub(args []string) (ledger.Object, int, error) {
	cwd, ok := gatesCwdArg(args, "gates detect")
	if !ok {
		return nil, 2, nil
	}
	configured, err := gatesConfigured(cwd)
	if err != nil {
		return nil, 0, err
	}
	if cwd == "" {
		return nil, 0, gatesThrow("detectGates: root must be a non-empty string")
	}
	detected := gates.Detect(cwd)
	out := ledger.Object{{Key: "stack", Value: detected.Stack}}
	for _, name := range gates.Order {
		out = append(out, ledger.Field{Key: name, Value: gatesDetectedCommand(detected.Commands[name])})
	}
	if configured != nil {
		plain := gatesPlain(configured).(map[string]any)
		messages := append(gates.CustomMessages(plain), gates.KindMessages(plain)...)
		if v, present := plain["rerun_failed"]; present && v != float64(0) && v != float64(1) {
			messages = append(messages, "gates.rerun_failed must be 0 or 1")
		}
		if len(messages) > 0 {
			return nil, 0, gatesThrow("%s", strings.Join(messages, "; "))
		}
		for _, name := range gates.Order {
			if v, present := gatesField(configured, name); present {
				out.Set(name, v)
			}
		}
		if v, present := gatesField(configured, "custom"); present {
			out = append(out, ledger.Field{Key: "custom", Value: v})
		}
		if v, present := gatesField(configured, "rerun_failed"); present {
			out = append(out, ledger.Field{Key: "rerun_failed", Value: v})
		}
	}
	for i, f := range out {
		if f.Key != "custom" {
			out[i].Value = gatesPrintable(f.Value)
			continue
		}
		switch v := f.Value.(type) {
		case nil, bool, string, float64:
			if gatesTruthy(v) {
				return nil, 0, &gatesHand{hGatesDetectConfig}
			}
		case []any:
			mapped := make([]any, len(v))
			for j, gate := range v {
				mapped[j] = gatesPrintable(gate)
			}
			out[i].Value = mapped
		default:
			return nil, 0, &gatesHand{hGatesDetectConfig}
		}
	}
	return out, 0, nil
}

func gatesTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0 && !math.IsNaN(x)
	}
	return true
}

func gatesField(o ledger.Object, key string) (any, bool) {
	for _, f := range o {
		if f.Key == key {
			return f.Value, true
		}
	}
	return nil, false
}

func gatesDetectedCommand(c *gates.Command) any {
	if c == nil {
		return nil
	}
	argv := make([]any, len(c.Argv))
	for i, a := range c.Argv {
		argv[i] = a
	}
	switch {
	case c.Formatter != "":
		return ledger.Object{{Key: "argv", Value: argv}, {Key: "formatter", Value: c.Formatter}}
	case c.FailOnStdout:
		return ledger.Object{{Key: "argv", Value: argv}, {Key: "failOnStdout", Value: true}}
	}
	return argv
}

// gatesPrintable is printableGates for one value: a gate object's env becomes its sorted names.
func gatesPrintable(v any) any {
	o, ok := v.(ledger.Object)
	if !ok {
		return v
	}
	env, has := gatesField(o, "env")
	if !has {
		return v
	}
	rest := o.Clone()
	rest.Delete("env")
	if e, ok := env.(ledger.Object); ok && len(e) > 0 {
		keys := []string{}
		for k := range e.Map() {
			keys = append(keys, k)
		}
		gitx.SortStrings(keys)
		names := make([]any, len(keys))
		for i, k := range keys {
			names[i] = k
		}
		rest.Set("env_keys", names)
	}
	return rest
}

// gatesConfigured is configuredGates with the file's key order: nil when the
// config does not load or has no gates mapping.
func gatesConfigured(cwd string) (ledger.Object, error) {
	loaded, err := config.Load(cwd)
	if err != nil {
		return nil, nil
	}
	raw, present := loaded["gates"]
	if !present {
		return nil, nil
	}
	switch raw.(type) {
	case map[string]any, []any:
	default:
		return nil, nil
	}
	source, err := os.ReadFile(gatesNodeJoin(config.ProjectRootFor(cwd), config.Filename))
	if err != nil {
		return nil, &gatesHand{hGatesDetectConfig}
	}
	tree, err := config.ParseOrdered(string(source))
	if err != nil {
		return nil, &gatesHand{hGatesDetectConfig}
	}
	top, _ := tree.([]config.Pair)
	var value any
	for _, p := range top {
		if p.Key == "gates" {
			value = gatesOrdered(p.Value)
		}
	}
	// The ordered read must be the same value Load gave, or the order is not known.
	if value == nil || !reflect.DeepEqual(gatesPlain(value), raw) {
		return nil, &gatesHand{hGatesDetectConfig}
	}
	ordered, _ := value.(ledger.Object)
	if list, ok := value.([]any); ok {
		// Node spreads a gates list into index keys ({...[a, b]} is {"0": a, "1": b}).
		ordered = ledger.Object{}
		for i, item := range list {
			ordered = append(ordered, ledger.Field{Key: strconv.Itoa(i), Value: item})
		}
	}
	if _, has := gatesField(ordered, "rerun_failed"); !has {
		ordered = append(ordered, ledger.Field{Key: "rerun_failed", Value: float64(1)})
	}
	return ordered, nil
}

func gatesOrdered(v any) any {
	switch x := v.(type) {
	case []config.Pair:
		o := ledger.Object{}
		for _, p := range x {
			o = append(o, ledger.Field{Key: p.Key, Value: gatesOrdered(p.Value)})
		}
		return o
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = gatesOrdered(item)
		}
		return out
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil
		}
	}
	return v
}

func gatesPlain(v any) any {
	switch x := v.(type) {
	case ledger.Object:
		m := map[string]any{}
		for _, f := range x {
			m[f.Key] = gatesPlain(f.Value)
		}
		return m
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = gatesPlain(item)
		}
		return out
	}
	return v
}

// --- secret-scan ----------------------------------------------------------

var gatesSecretPatterns = func() []*regexp.Regexp {
	out := []*regexp.Regexp{}
	for _, p := range []string{`sk-[A-Za-z0-9_-]{10,}`, `xai-[A-Za-z0-9]{20,}`, `xox[baprs]-[A-Za-z0-9-]{10,}`, `gh[opsu]_[A-Za-z0-9]{20,}`, `github_pat_[A-Za-z0-9_]{20,}`, `AKIA[0-9A-Z]{12,}`, `AIza[0-9A-Za-z_-]{20,}`, `eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{5,}`} {
		out = append(out, regexp.MustCompile(p))
	}
	return out
}()

func gatesSecretWord(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

// gatesSecretTokens is secretTokensIn: each pattern's global matches behind
// (?<![A-Za-z0-9_-]), pattern by pattern.
func gatesSecretTokens(line string) []string {
	tokens := []string{}
	for _, re := range gatesSecretPatterns {
		for at := 0; at <= len(line); {
			loc := re.FindStringIndex(line[at:])
			if loc == nil {
				break
			}
			start, end := at+loc[0], at+loc[1]
			if start > 0 && gatesSecretWord(line[start-1]) {
				at = start + 1
				continue
			}
			tokens = append(tokens, line[start:end])
			at = end
		}
	}
	return tokens
}

func gatesSecretScanSub(args []string) (ledger.Object, int, error) {
	cwd, ok := gatesCwdArg(args, "gates secret-scan")
	if !ok {
		return nil, 2, nil
	}
	files := []string{}
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--file" {
			files = append(files, args[i+1])
		}
	}
	if len(files) == 0 {
		gatesErrf("gates secret-scan: at least one --file <path> is required\n")
		return nil, 2, nil
	}
	hits := []any{}
	for _, rel := range files {
		data, code, err := gatesReadFile(gatesNodeJoin(cwd, rel))
		if code == "ENOENT" || code == "ENOTDIR" {
			continue
		}
		if err != nil {
			return nil, 0, err
		}
		for i, line := range strings.Split(string(data), "\n") {
			count := 0
			for _, token := range gatesSecretTokens(line) {
				if !strings.Contains(strings.ToLower(token), "fake") {
					count++
				}
			}
			if count > 0 {
				hits = append(hits, ledger.Object{{Key: "path", Value: rel}, {Key: "line", Value: float64(i + 1)}, {Key: "count", Value: float64(count)}})
			}
		}
	}
	code := 0
	if len(hits) > 0 {
		code = 1
	}
	return ledger.Object{{Key: "hits", Value: hits}, {Key: "count", Value: float64(len(hits))}}, code, nil
}

// --- git (safe-edit, scope) -----------------------------------------------

type gatesGitResult struct {
	stdout, stderr string
	// code is the exit code as Node prints it: a number, or "null" when git did not start.
	code    string
	ok      bool
	spawned bool
}

const gatesGitMaxBuffer = 10 * 1024 * 1024

// gatesGitTimeout is the per-command git timeout (Node's GIT_TIMEOUT_MS).
var gatesGitTimeout = 30 * time.Second

// gatesGit runs git as src/gates/{safe-edit,scope}.mjs exec it: Node's gitEnv
// (no inherited GIT_*, no system or global config). A timeout, a signal, an
// output over Node's buffer or one Node would decode with U+FFFD hands over.
func gatesGit(cwd string, environ []string, args ...string) (gatesGitResult, error) {
	env := []string{}
	for _, kv := range environ {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	env = append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	ctx, cancel := context.WithTimeout(context.Background(), gatesGitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = cwd
	cmd.Env = childreg.ChildEnv(env)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		return gatesGitResult{code: "null"}, nil
	}
	settled := childreg.Begin(nil, cmd.Process.Pid, "git")
	err := cmd.Wait()
	settled()
	if ctx.Err() != nil || stdout.Len() > gatesGitMaxBuffer || stderr.Len() > gatesGitMaxBuffer ||
		!utf8.Valid(stdout.Bytes()) || !utf8.Valid(stderr.Bytes()) {
		return gatesGitResult{}, &gatesHand{hGatesGit}
	}
	r := gatesGitResult{stdout: stdout.String(), stderr: stderr.String(), spawned: true, code: "0", ok: err == nil}
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() < 0 {
			return gatesGitResult{}, &gatesHand{hGatesGit}
		}
		r.code = strconv.Itoa(exit.ExitCode())
	}
	return r, nil
}

// gatesFirstFailure is Promise.all's rejection of two parallel git steps:
// when both failed by exit code the winner is a race, so Node keeps it.
func gatesFirstFailure(a, b gatesGitResult, aFails, bFails bool) (first bool, err error) {
	if aFails && bFails && (a.spawned || b.spawned) {
		return false, &gatesHand{hGatesGitRace}
	}
	return aFails, nil
}

func gatesSplitNul(text string) []string {
	out := []string{}
	for _, s := range strings.Split(text, "\x00") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func gatesJSONString(s string) string {
	data, _ := ledger.Stringify(s, false)
	return string(data)
}

// --- safe-edit ------------------------------------------------------------

var gatesTestKinds = []struct {
	stack string
	re    *regexp.Regexp
}{
	{"node", regexp.MustCompile(`\.test\.[cm]?[jt]sx?$`)},
	{"node", regexp.MustCompile(`\.spec\.[cm]?[jt]sx?$`)},
	{"php", regexp.MustCompile(`Test\.php$`)},
	{"python", regexp.MustCompile(`^test_[^\n\r\x{2028}\x{2029}]*\.py$`)},
	{"go", regexp.MustCompile(`_test\.go$`)},
}

var gatesTestDecls = map[string]*regexp.Regexp{
	"node":   regexp.MustCompile(`\b(?:test|it)` + gatesJSSpaceClass + `*\(`),
	"php":    regexp.MustCompile(`(?:public` + gatesJSSpaceClass + `+function` + gatesJSSpaceClass + `+test[A-Z_]\w*` + gatesJSSpaceClass + `*\(|\bit` + gatesJSSpaceClass + `*\(|\btest` + gatesJSSpaceClass + `*\()`),
	"python": regexp.MustCompile(`(?m)^` + gatesJSSpaceClass + `*def test_\w+`),
	"go":     regexp.MustCompile(`(?m)^func Test[A-Z]\w*`),
}

var gatesLineEnds = strings.NewReplacer("\r", "\n", " ", "\n", " ", "\n")

func gatesTestStack(file string) string {
	base := file
	if i := strings.LastIndex(file, "/"); i >= 0 {
		base = file[i+1:]
	}
	for _, k := range gatesTestKinds {
		if k.re.MatchString(base) {
			return k.stack
		}
	}
	return ""
}

func gatesCountDecls(content, stack string) int {
	if stack == "python" || stack == "go" {
		// JavaScript's ^ in multiline mode also starts a line after \r, U+2028 and U+2029.
		content = gatesLineEnds.Replace(content)
	}
	return len(gatesTestDecls[stack].FindAllStringIndex(content, -1))
}

func gatesSafeEditSub(args []string, environ []string) (ledger.Object, int, error) {
	cwd, ok := gatesCwdArg(args, "gates safe-edit")
	if !ok {
		return nil, 2, nil
	}
	base := gatesFlagValue(args, "--base")
	if base == "" {
		gatesErrf("gates safe-edit: --base <sha> is required\n")
		return nil, 2, nil
	}
	if strings.HasPrefix(base, "-") {
		return nil, 0, gatesThrow("safe-edit: base must be a non-empty string not starting with \"-\" (got %s)", gatesJSONString(base))
	}
	tree, err := gatesGit(cwd, environ, "ls-tree", "-r", "-z", "--name-only", base)
	if err != nil {
		return nil, 0, err
	}
	tracked, err := gatesGit(cwd, environ, "ls-files", "-z")
	if err != nil {
		return nil, 0, err
	}
	others, err := gatesGit(cwd, environ, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return nil, 0, err
	}
	current := tracked
	if tracked.ok {
		current = others
	}
	treeFirst, err := gatesFirstFailure(tree, current, !tree.ok, !tracked.ok || !others.ok)
	if err != nil {
		return nil, 0, err
	}
	if treeFirst {
		return nil, 0, gatesThrow("safe-edit: git ls-tree -r -z --name-only %s failed (exit %s): %s", base, tree.code, tree.stderr)
	}
	if !tracked.ok {
		return nil, 0, gatesThrow("safe-edit: git ls-files -z failed (exit %s): %s", tracked.code, tracked.stderr)
	}
	if !others.ok {
		return nil, 0, gatesThrow("safe-edit: git ls-files -z --others --exclude-standard failed (exit %s): %s", others.code, others.stderr)
	}
	baseCounts := map[string]int{}
	baseFiles := []string{}
	for _, file := range gatesSplitNul(tree.stdout) {
		stack := gatesTestStack(file)
		if stack == "" {
			continue
		}
		shown, err := gatesGit(cwd, environ, "show", base+":"+file)
		if err != nil {
			return nil, 0, err
		}
		if !shown.ok {
			return nil, 0, gatesThrow("safe-edit: git show %s:%s failed (exit %s): %s", base, file, shown.code, shown.stderr)
		}
		if _, seen := baseCounts[file]; !seen {
			baseFiles = append(baseFiles, file)
		}
		baseCounts[file] = gatesCountDecls(shown.stdout, stack)
	}
	currentCounts := map[string]int{}
	seen := map[string]bool{}
	for _, file := range append(gatesSplitNul(tracked.stdout), gatesSplitNul(others.stdout)...) {
		if seen[file] {
			continue
		}
		seen[file] = true
		stack := gatesTestStack(file)
		if stack == "" {
			continue
		}
		data, code, err := gatesReadFile(gatesNodeJoin(cwd, file))
		if code == "ENOENT" {
			continue
		}
		if err != nil {
			return nil, 0, err
		}
		if !utf8.Valid(data) {
			// Node counts the U+FFFD-decoded text; byte matching counts the same tokens.
			data = []byte(strings.ToValidUTF8(string(data), "�"))
		}
		currentCounts[file] = gatesCountDecls(string(data), stack)
	}
	deleted := []string{}
	totalBase, totalCurrent := 0, 0
	for _, file := range baseFiles {
		if _, ok := currentCounts[file]; !ok {
			deleted = append(deleted, file)
		}
		totalBase += baseCounts[file]
	}
	for _, n := range currentCounts {
		totalCurrent += n
	}
	if len(deleted) > 0 {
		gitx.SortStrings(deleted)
		return ledger.Object{{Key: "ok", Value: false}, {Key: "reason", Value: "deleted test file(s): " + strings.Join(deleted, ", ")}}, 1, nil
	}
	if totalBase > 0 && float64(totalCurrent) < float64(totalBase)*0.8 {
		return ledger.Object{{Key: "ok", Value: false}, {Key: "reason", Value: fmt.Sprintf("test count shrank from %d to %d (more than 20%%)", totalBase, totalCurrent)}}, 1, nil
	}
	return ledger.Object{{Key: "ok", Value: true}}, 0, nil
}

// --- scope ----------------------------------------------------------------

func gatesScopeSub(args []string, environ []string) (ledger.Object, int, error) {
	cwd, ok := gatesCwdArg(args, "gates scope")
	if !ok {
		return nil, 2, nil
	}
	base := gatesFlagValue(args, "--base")
	if base == "" {
		gatesErrf("gates scope: --base <sha> is required\n")
		return nil, 2, nil
	}
	if strings.HasPrefix(base, "-") {
		return nil, 0, gatesThrow("base must be a non-empty string not starting with \"-\" (got %s) — option injection", gatesJSONString(base))
	}
	diff, err := gatesGit(cwd, environ, "diff", "--name-only", "-z", base)
	if err != nil {
		return nil, 0, err
	}
	status, err := gatesGit(cwd, environ, "status", "--porcelain", "-z", "--untracked-files=all")
	if err != nil {
		return nil, 0, err
	}
	diffFirst, err := gatesFirstFailure(diff, status, !diff.ok, !status.ok)
	if err != nil {
		return nil, 0, err
	}
	if diffFirst {
		return nil, 0, gatesThrow("trackedChangedFiles: git diff --name-only -z %s failed (exit %s): %s", base, diff.code, diff.stderr)
	}
	if !status.ok {
		return nil, 0, gatesThrow("untrackedFiles: git status failed (exit %s): %s", status.code, status.stderr)
	}
	tracked := gatesSplitNul(diff.stdout)
	untracked := []string{}
	for _, entry := range gatesSplitNul(status.stdout) {
		if strings.HasPrefix(entry, "?? ") {
			untracked = append(untracked, entry[3:])
		}
	}
	all := []string{}
	seen := map[string]bool{}
	for _, f := range append(append([]string{}, tracked...), untracked...) {
		if !seen[f] {
			seen[f] = true
			all = append(all, f)
		}
	}
	gitx.SortStrings(all)
	list := func(items []string) []any {
		out := make([]any, len(items))
		for i, s := range items {
			out[i] = s
		}
		return out
	}
	return ledger.Object{{Key: "tracked", Value: list(tracked)}, {Key: "untracked", Value: list(untracked)}, {Key: "all", Value: list(all)}}, 0, nil
}

// --- acceptance -----------------------------------------------------------

var (
	gatesTapOK        = regexp.MustCompile(`^ok` + gatesJSSpaceClass + `+[0-9]+` + gatesJSSpaceClass + `+-` + gatesJSSpaceClass + `+([^\n\r\x{2028}\x{2029}]+)$`)
	gatesTapNotOK     = regexp.MustCompile(`^not ok` + gatesJSSpaceClass + `+[0-9]+` + gatesJSSpaceClass + `+-` + gatesJSSpaceClass + `+([^\n\r\x{2028}\x{2029}]+)$`)
	gatesTapDirective = regexp.MustCompile(`^([^\n\r\x{2028}\x{2029}]*?)` + gatesJSSpaceClass + `*#` + gatesJSSpaceClass + `*(?:[Ss][Kk][Ii][Pp]|[Tt][Oo][Dd][Oo])\b`)
	gatesSurrogate    = regexp.MustCompile(`\\u[dD][89a-fA-F]`)
)

// gatesTapName is parseNameAndDirective: the trimmed name and whether a SKIP/TODO directive follows.
func gatesTapName(text string) (string, bool) {
	if m := gatesTapDirective.FindStringSubmatch(text); m != nil {
		return gatesJSTrim(m[1]), true
	}
	return gatesJSTrim(text), false
}

func gatesParseTap(text string) (passed, failed map[string]bool) {
	passed, failed = map[string]bool{}, map[string]bool{}
	for _, raw := range strings.Split(text, "\n") {
		line := gatesJSTrim(raw)
		if m := gatesTapOK.FindStringSubmatch(line); m != nil {
			if name, directive := gatesTapName(m[1]); !directive {
				passed[name] = true
			}
			continue
		}
		if m := gatesTapNotOK.FindStringSubmatch(line); m != nil {
			name, _ := gatesTapName(m[1])
			failed[name] = true
		}
	}
	return passed, failed
}

// gatesJSString is JavaScript's String(value) for a JSON value.
func gatesJSValueString(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		data, _ := ledger.Stringify(x, false)
		return string(data)
	case []any:
		parts := make([]string, len(x))
		for i, item := range x {
			if item != nil {
				parts[i] = gatesJSValueString(item)
			}
		}
		return strings.Join(parts, ",")
	}
	return "[object Object]"
}

func gatesDecodeJSON(data []byte) (any, bool) {
	if !utf8.Valid(data) || gatesSurrogate.Match(data) {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	var decode func() (any, error)
	decode = func() (any, error) {
		token, err := dec.Token()
		if err != nil {
			return nil, err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return token, nil
		}
		switch delim {
		case '{':
			o := ledger.Object{}
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return nil, err
				}
				value, err := decode()
				if err != nil {
					return nil, err
				}
				o.Set(key.(string), value)
			}
			_, err := dec.Token()
			return o, err
		case '[':
			a := []any{}
			for dec.More() {
				value, err := decode()
				if err != nil {
					return nil, err
				}
				a = append(a, value)
			}
			_, err := dec.Token()
			return a, err
		}
		return nil, fmt.Errorf("unexpected %q", delim)
	}
	v, err := decode()
	if err != nil {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false
	}
	return v, true
}

func gatesCheckAcceptance(clauses any, passed, failed map[string]bool) ledger.Object {
	list, _ := clauses.([]any)
	if len(list) == 0 {
		return ledger.Object{{Key: "ok", Value: false}, {Key: "reason", Value: "no acceptance clauses given"}, {Key: "clause", Value: ""}, {Key: "test", Value: ""}}
	}
	for _, item := range list {
		entry, _ := item.(ledger.Object)
		clause, hasClause := gatesField(entry, "clause")
		tests, hasTests := gatesField(entry, "tests")
		text, isText := clause.(string)
		if kind, _ := gatesField(entry, "kind"); kind == "human_check" && isText && text != "" {
			if hasTests {
				return ledger.Object{{Key: "ok", Value: false}, {Key: "reason", Value: "human_check clause must not name tests"}, {Key: "clause", Value: text}, {Key: "test", Value: ""}}
			}
			continue
		}
		testList, isList := tests.([]any)
		if !isText || text == "" || !isList || len(testList) == 0 {
			shown := ""
			if hasClause && clause != nil {
				shown = gatesJSValueString(clause)
			}
			return ledger.Object{{Key: "ok", Value: false}, {Key: "reason", Value: "clause names no test id"}, {Key: "clause", Value: shown}, {Key: "test", Value: ""}}
		}
		for _, test := range testList {
			name, isName := test.(string)
			if !isName || failed[name] || !passed[name] {
				return ledger.Object{{Key: "ok", Value: false},
					{Key: "reason", Value: fmt.Sprintf("clause \"%s\" names test \"%s\", which did not exist or did not pass in this gate run", text, gatesJSValueString(test))},
					{Key: "clause", Value: text}, {Key: "test", Value: test}}
			}
		}
	}
	return ledger.Object{{Key: "ok", Value: true}}
}

func gatesAcceptanceSub(args []string) (ledger.Object, int, error) {
	clausesPath, tapPath := gatesFlagValue(args, "--clauses"), gatesFlagValue(args, "--tap")
	if clausesPath == "" || tapPath == "" {
		gatesErrf("gates acceptance: --clauses <file.json> and --tap <file> are required\n")
		return nil, 2, nil
	}
	data, _, err := gatesReadFile(clausesPath)
	if err != nil {
		return nil, 0, err
	}
	clauses, ok := gatesDecodeJSON(data)
	if !ok {
		// JSON.parse's own error text (and U+FFFD or lone-surrogate decoding) stays with Node.
		return nil, 0, &gatesHand{hGatesAcceptanceJSON}
	}
	tap, _, err := gatesReadFile(tapPath)
	if err != nil {
		return nil, 0, err
	}
	if !utf8.Valid(tap) {
		return nil, 0, &gatesHand{hGatesTapEncoding}
	}
	passed, failed := gatesParseTap(string(tap))
	result := gatesCheckAcceptance(clauses, passed, failed)
	if ok, _ := gatesField(result, "ok"); ok == true {
		return result, 0, nil
	}
	return result, 1, nil
}

// --- transcript-grep ------------------------------------------------------

// gatesForbiddenIDs is the FORBIDDEN list of src/util/forbidden.mjs (no coder-only entries).
var gatesForbiddenIDs = map[string]bool{"gh-pr-ready": true, "gh-pr-merge": true, "gh-pr-close": true, "gh-pr-base-retarget": true,
	"git-push-force": true, "git-reset-hard": true, "git-checkout-discard": true, "git-restore": true, "git-clean": true, "git-stash": true,
	"git-branch-force-delete": true, "git-branch-force-delete-long": true, "git-rm": true, "rm-rf": true, "production-marker": true,
	"code-forge-runs-access": true, "code-forge-worker-from-coder": true, "code-forge-reviews-write": true}

var gatesTranscriptDetail = regexp.MustCompile(`^transcript line ([0-9]+): (.+)$`)

func gatesTranscriptSub(args []string) (ledger.Object, int, error) {
	file := gatesFlagValue(args, "--file")
	if file == "" {
		gatesErrf("gates transcript-grep: --file <path> is required\n")
		return nil, 2, nil
	}
	cwd, _, ok := gatesFlag(args, "--cwd")
	if !ok {
		gatesErrf("gates transcript-grep: --cwd requires a value\n")
		return nil, 2, nil
	}
	data, _, err := gatesReadFile(file)
	if err != nil {
		return nil, 0, err
	}
	if cwd != "" {
		if loaded, err := config.Load(cwd); err == nil {
			if production, ok := loaded["production"].(map[string]any); ok {
				markers, _ := production["markers"].([]any)
				names, _ := production["names"].([]any)
				if len(markers)+len(names) > 0 {
					// production markers and names extend Node's list; that merge stays in Node.
					return nil, 0, &gatesHand{hGatesTranscriptExtras}
				}
			}
		}
	}
	found, supported := gatecheck.Transcript(string(data))
	if !supported {
		return nil, 0, &gatesHand{hGatesTranscriptUnsup}
	}
	hits := []any{}
	for _, b := range found {
		m := gatesTranscriptDetail.FindStringSubmatch(b.Detail)
		if m == nil {
			return nil, 0, &gatesHand{hGatesTranscriptUnsup}
		}
		if !gatesForbiddenIDs[m[2]] {
			continue
		}
		line, _ := strconv.Atoi(m[1])
		hits = append(hits, ledger.Object{{Key: "id", Value: m[2]}, {Key: "line", Value: float64(line)}})
	}
	code := 0
	if len(hits) > 0 {
		code = 1
	}
	return ledger.Object{{Key: "ok", Value: len(hits) == 0}, {Key: "hits", Value: hits}}, code, nil
}
