package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/ledger"
)

// `code-forge init` (src/install/wizard/run.mjs) in Go, B502. Go serves the
// paths that end before the wizard writes the project: every usage error,
// no HOME, a .code-forge.yml that cannot be loaded and the draft-PR refusal
// in non-interactive mode. Every other path (the prompts, the 1Password and
// key-store reads, the config write, the links, the tools step, doctor) hands
// over to Node with its own reason, before Go writes anything.

// initNow is the wizard's clock (the log file name); a test seam.
var initNow = time.Now

// initStdinTTY is process.stdin.isTTY; a test seam.
var initStdinTTY = stdinIsTerminal

var (
	initAnswerFlags = map[string]bool{
		"--tools": true, "--yes-tool": true, "--harness": true, "-g": true, "-p": true, "--copy": true,
		"--provider": true, "--level": true, "--refresh-models": true, "--multimodel": true,
		"--second-provider": true, "--jev-ref": true, "--jev-env": true, "--no-jev": true, "--engine": true,
		"--solo-project": true, "--gate": true, "--proof": true, "--skip-doctor": true,
		"--coder-may-push": true, "--coder-may-open-draft-pr": true,
	}
	initBooleanFlags = map[string]bool{"-g": true, "-p": true, "--copy": true, "--refresh-models": true, "--no-jev": true, "--skip-doctor": true, "--no-interaction": true}
	initRepeatable   = map[string]bool{"--yes-tool": true, "--level": true, "--gate": true, "--proof": true}
	initProviders    = []string{"anthropic", "openai", "xai"}
	initToolIDs      = []string{"claude", "codex", "gemini", "grok", "op", "solo"}
	initHarnessIDs   = []string{"claude", "codex", "grok", "gemini", "cursor", "copilot"}
	initEnvName      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	initSoloProject  = regexp.MustCompile(`^[1-9][0-9]{0,9}$`)
	initOpRef        = regexp.MustCompile(`^op://[^/\r\n]+/[^/\r\n]+/[^\r\n]+$`)
	initOpItemID     = regexp.MustCompile(`^[a-z0-9]{26}$`)
	initHTTPS        = regexp.MustCompile(`^(?i)https://`)
)

const initDraftRefusal = "--coder-may-open-draft-pr yes needs autonomy.coder_may_push yes — a draft PR needs a push; pass --coder-may-push yes or drop the flag. Nothing written."

// initGiven is one parsed answer flag; only the values Go acts on are kept.
type initGiven struct {
	flag  string
	value any
}

type initParsed struct {
	noInteraction bool
	given         []initGiven
}

// initLast is lastFlag in resolveAnswers for one flag name.
func (p initParsed) initLast(flag string) (any, bool) {
	for i := len(p.given) - 1; i >= 0; i-- {
		if p.given[i].flag == flag {
			return p.given[i].value, true
		}
	}
	return nil, false
}

func initHas(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func initCSV(value string) []string {
	out := []string{}
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimFunc(part, isJSSpace); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func initKeyValue(flag, value string) (string, string, string) {
	eq := strings.Index(value, "=")
	if eq <= 0 {
		return "", "", flag + " expects <key>=<value>"
	}
	return value[:eq], value[eq+1:], ""
}

// initShellWords is shellWords in flags.mjs: the words, or its error.
func initShellWords(text string) ([]string, string) {
	words, has := []string{}, false
	var current strings.Builder
	quote := rune(0)
	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		switch {
		case quote == '\'':
			if ch == '\'' {
				quote = 0
			} else {
				current.WriteRune(ch)
			}
		case ch == '\\' && i+1 < len(runes):
			i++
			current.WriteRune(runes[i])
			has = true
		case quote == '"':
			if ch == '"' {
				quote = 0
			} else {
				current.WriteRune(ch)
			}
		case ch == '"' || ch == '\'':
			quote, has = ch, true
		case isJSSpace(ch):
			if has {
				words = append(words, current.String())
			}
			current.Reset()
			has = false
		default:
			current.WriteRune(ch)
			has = true
		}
	}
	if quote != 0 {
		return nil, "--gate: unterminated quote"
	}
	if has {
		words = append(words, current.String())
	}
	return words, ""
}

// initLevelValue is parseLevel's {level, spec}; spec keeps model, effort,
// provider in that order.
type initLevelValue struct {
	level string
	spec  ledger.Object
}

// initGateValue is the --gate value {name, argv}; argv is nil for none/null.
type initGateValue struct {
	name string
	argv any
}

// initProofValue is the --proof value {key, value}.
type initProofValue struct {
	key   string
	value any
}

// initConvert is convert in flags.mjs. It returns the value, a usage error,
// or hand=true for an input Go cannot judge like Node (a 1Password link,
// which Node reads with the WHATWG URL parser).
func initConvert(flag, value string) (any, string, bool) {
	switch flag {
	case "--tools":
		if value != "recommended" && value != "current" {
			return nil, "--tools must be recommended or current", false
		}
	case "--yes-tool":
		if !initHas(initToolIDs, value) {
			return nil, "--yes-tool must be one of " + strings.Join(initToolIDs, ", "), false
		}
	case "--harness":
		ids := initCSV(value)
		bad := len(ids) == 0
		for _, id := range ids {
			bad = bad || !initHas(initHarnessIDs, id)
		}
		if bad {
			return nil, "--harness takes a comma list of " + strings.Join(initHarnessIDs, ", "), false
		}
		unique := []string{}
		for _, id := range ids {
			if !initHas(unique, id) {
				unique = append(unique, id)
			}
		}
		return unique, "", false
	case "--provider", "--second-provider":
		if !initHas(initProviders, value) {
			return nil, flag + " must be one of " + strings.Join(initProviders, ", "), false
		}
	case "--level":
		level, rest, msg := initKeyValue("--level", value)
		if msg != "" {
			return nil, msg, false
		}
		if !initHas([]string{"L0", "L1", "L2", "L3"}, level) {
			return nil, "--level: level must be L0, L1, L2 or L3", false
		}
		body, effort, provider := rest, "", ""
		if at := strings.LastIndex(body, "@"); at >= 0 {
			if !initHas(initProviders, body[at+1:]) {
				return nil, "--level: unknown provider", false
			}
			provider, body = body[at+1:], body[:at]
		}
		if colon := strings.LastIndex(body, ":"); colon >= 0 {
			if colon == len(body)-1 {
				return nil, "--level: empty effort", false
			}
			effort, body = body[colon+1:], body[:colon]
		}
		if body == "" {
			return nil, "--level: empty model", false
		}
		spec := ledger.Object{{Key: "model", Value: body}}
		if effort != "" {
			spec = append(spec, ledger.Field{Key: "effort", Value: effort})
		}
		if provider != "" {
			spec = append(spec, ledger.Field{Key: "provider", Value: provider})
		}
		return initLevelValue{level, spec}, "", false
	case "--multimodel":
		if value != "on" && value != "off" {
			return nil, "--multimodel must be on or off", false
		}
		return value == "on", "", false
	case "--jev-ref":
		text := strings.TrimFunc(value, isJSSpace)
		if initOpRef.MatchString(text) || initOpItemID.MatchString(text) {
			return text, "", false
		}
		if initHTTPS.MatchString(text) {
			return nil, "", true
		}
		return nil, "--jev-ref must be a 1Password item ID, item link or op://vault/item/field reference", false
	case "--jev-env":
		if !initEnvName.MatchString(value) {
			return nil, "--jev-env must be an environment variable name", false
		}
	case "--engine":
		if value == "subprocess" {
			return nil, `--engine: init never selects subprocess (R2); type "engine: subprocess" into .code-forge.yml yourself`, false
		}
		if !initHas([]string{"auto", "solo", "harness"}, value) {
			return nil, "--engine must be one of auto, solo, harness", false
		}
	case "--solo-project":
		if !initSoloProject.MatchString(value) {
			return nil, "--solo-project must be a positive integer", false
		}
		n, _ := strconv.ParseFloat(value, 64)
		return n, "", false
	case "--gate":
		name, cmd, msg := initKeyValue("--gate", value)
		if msg != "" {
			return nil, msg, false
		}
		if !initHas([]string{"test", "lint", "types", "format"}, name) {
			return nil, "--gate: gate must be one of test, lint, types, format", false
		}
		if cmd == "none" || cmd == "null" {
			return initGateValue{name, nil}, "", false
		}
		words, msg := initShellWords(cmd)
		if msg != "" {
			return nil, msg, false
		}
		if len(words) == 0 {
			return nil, "--gate: empty command", false
		}
		argv := []any{}
		for _, w := range words {
			argv = append(argv, w)
		}
		return initGateValue{name, argv}, "", false
	case "--coder-may-push", "--coder-may-open-draft-pr":
		if value != "yes" && value != "no" {
			return nil, flag + " must be yes or no", false
		}
		return value == "yes", "", false
	case "--proof":
		key, raw, msg := initKeyValue("--proof", value)
		if msg != "" {
			return nil, msg, false
		}
		if !initHas([]string{"isolation", "high", "link_dirs", "copy_untracked"}, key) {
			return nil, "--proof: key must be one of isolation, high, link_dirs, copy_untracked", false
		}
		if key == "isolation" {
			if raw != "export" && raw != "lock" {
				return nil, "--proof isolation must be export or lock", false
			}
			return initProofValue{key, raw}, "", false
		}
		list := []any{}
		for _, item := range initCSV(raw) {
			list = append(list, item)
		}
		return initProofValue{key, list}, "", false
	}
	return value, "", false
}

// initParseArgs is parseInitArgs in flags.mjs: the parsed flags, a usage
// error message, or hand=true when Node must judge an argument.
func initParseArgs(args []string) (initParsed, string, bool) {
	out := initParsed{}
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		eq := -1
		if strings.HasPrefix(arg, "--") {
			eq = strings.Index(arg, "=")
		}
		flag := arg
		if eq > 0 {
			flag = arg[:eq]
		}
		if flag != "--no-interaction" && !initAnswerFlags[flag] {
			if strings.HasPrefix(arg, "-") {
				return out, "unknown flag " + flag, false
			}
			return out, "init takes no positional argument", false
		}
		if seen[flag] && !initRepeatable[flag] {
			return out, flag + " given more than once", false
		}
		seen[flag] = true
		if initBooleanFlags[flag] {
			if eq > 0 {
				return out, flag + " takes no value", false
			}
			if flag == "--no-interaction" {
				out.noInteraction = true
			} else {
				value := any(true)
				switch flag {
				case "--copy":
					value = "copy"
				case "-g":
					value = "global"
				case "-p":
					value = "project"
				}
				out.given = append(out.given, initGiven{flag, value})
			}
			continue
		}
		value, ok := "", false
		if eq > 0 {
			value, ok = arg[eq+1:], true
		} else {
			i++
			if i < len(args) {
				value, ok = args[i], true
			}
		}
		if !ok || value == "" || eq < 0 && strings.HasPrefix(value, "-") {
			return out, flag + " needs a value", false
		}
		converted, msg, hand := initConvert(flag, value)
		if hand || msg != "" {
			return out, msg, hand
		}
		out.given = append(out.given, initGiven{flag, converted})
	}
	if seen["-g"] && seen["-p"] {
		return out, "-g and -p are exclusive", false
	}
	jev := []string{}
	for _, f := range []string{"--jev-ref", "--jev-env", "--no-jev"} {
		if seen[f] {
			jev = append(jev, f)
		}
	}
	if len(jev) > 1 {
		return out, strings.Join(jev, " and ") + " are exclusive", false
	}
	return out, "", false
}

// initAgent is currentHarness(env).agent in answers.mjs.
func initAgent() bool {
	for _, name := range []string{"CLAUDECODE", "CLAUDE_CODE", "CURSOR_AGENT", "AI_AGENT", "CODEX_SANDBOX", "CODEX_SANDBOX_NETWORK_DISABLED"} {
		if os.Getenv(name) != "" {
			return true
		}
	}
	return false
}

// initOutputSafe reports whether Go's JSON line and file names for s are the
// ones Node writes: valid UTF-8 with no U+2028 or U+2029.
func initOutputSafe(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsAny(s, "\u2028\u2029")
}

// initRefusal is the wizard's failure before anything is written: the code,
// the error text, or a hand-over reason.
type initRefusal struct {
	code    int
	message string
	hand    bool
	reason  handoverReason
	// existing: a .code-forge.yml was loaded (hInitSetup only)
	existing bool
}

// initPlan decides, without side effects, what `init` does after the flags
// parsed: a refusal Go serves, or a hand-over.
func initPlan(parsed initParsed, home, cwd string, interactive bool) initRefusal {
	if home == "" {
		return initRefusal{code: 1, message: "HOME is not set"}
	}
	if !initOutputSafe(home) || !initOutputSafe(cwd) {
		return initRefusal{hand: true, reason: hInitOutput}
	}
	configFile := filepath.Join(cwd, config.Filename)
	var existing map[string]any
	if _, err := os.Stat(configFile); err == nil {
		if raw, err := os.ReadFile(configFile); err == nil && (!utf8.Valid(raw) || strings.HasPrefix(string(raw), "\ufeff")) {
			return initRefusal{hand: true, reason: hInitConfigSyntax}
		}
		loaded, _, err := config.LoadFileOrdered(configFile)
		if err != nil {
			var cerr *config.Error
			if !errors.As(err, &cerr) {
				return initRefusal{hand: true, reason: hInitConfigSyntax}
			}
			switch {
			case cerr.Kind == "read-error", cerr.Kind == "migrate-error",
				cerr.Kind == "parse-error" && strings.HasPrefix(cerr.Message, "config root must be a mapping, got "):
				return initRefusal{code: 1, message: config.Filename + " exists but cannot be loaded (" + cerr.Kind + "); fix or move it, then re-run"}
			default:
				// A YAML syntax error: Node's `yaml` package judges it.
				return initRefusal{hand: true, reason: hInitConfigSyntax}
			}
		}
		existing = loaded
	}
	if interactive {
		return initRefusal{hand: true, reason: hInitInteractive}
	}
	if existing != nil && initAnswersThrow(parsed, existing) {
		return initRefusal{hand: true, reason: hInitAnswers}
	}
	draft, _ := parsed.initLast("--coder-may-open-draft-pr")
	if draft == true && !initCoderMayPush(parsed, existing) {
		return initRefusal{code: 2, message: initDraftRefusal}
	}
	return initRefusal{hand: true, reason: hInitSetup, existing: existing != nil}
}

// initCoderMayPush is the coder_may_push answer of resolveAnswers.
func initCoderMayPush(parsed initParsed, existing map[string]any) bool {
	if v, ok := parsed.initLast("--coder-may-push"); ok {
		return v == true
	}
	if autonomy, ok := existing["autonomy"].(map[string]any); ok {
		if b, ok := autonomy["coder_may_push"].(bool); ok {
			return b
		}
	}
	return false
}

// initAnswersThrow reports an existing config on which resolveAnswers may
// throw before the draft-PR check: an unknown provider (no provider
// defaults) or --level over levels that are not a mapping.
func initAnswersThrow(parsed initParsed, existing map[string]any) bool {
	_, providerFlag := parsed.initLast("--provider")
	exProvider, hasProvider := existing["provider"]
	if !providerFlag && hasProvider && exProvider != nil {
		if s, ok := exProvider.(string); !ok || !initHas(initProviders, s) {
			return true
		}
	}
	if _, levelFlag := parsed.initLast("--level"); levelFlag {
		switch existing["levels"].(type) {
		case nil, map[string]any, []any:
		default:
			return true
		}
	}
	return false
}

// initLogName is init-<toISOString with : and . as ->.log.
func initLogName(now time.Time) string {
	stamp := now.UTC().Format("2006-01-02T15:04:05.000Z")
	stamp = strings.NewReplacer(":", "-", ".", "-").Replace(stamp)
	return "init-" + stamp + ".log"
}

// initFinish is finish(code, error) in run.mjs for a refusal before any
// line was said: the log file, stderr, and the JSON line in agent mode.
func initFinish(stdout, stderr io.Writer, home string, jsonMode bool, code int, message string) int {
	line := "error: " + message
	_, _ = io.WriteString(stderr, "init: "+message+"\n")
	var log any
	if home != "" {
		file := filepath.Join(home, ".code-forge", "logs", initLogName(initNow()))
		if os.MkdirAll(filepath.Dir(file), 0o777) == nil && os.WriteFile(file, []byte(line+"\n"), 0o666) == nil {
			log = file
		}
	}
	if jsonMode {
		body := ledger.Object{
			{Key: "ok", Value: false},
			{Key: "error", Value: message},
			{Key: "wrote", Value: []any{}},
			{Key: "log", Value: log},
			{Key: "log_tail", Value: []any{line}},
		}
		data, _ := ledger.Stringify(body, false)
		_, _ = stdout.Write(append(data, '\n'))
	}
	return code
}

func runInitVerb(args []string) int {
	if metaHelp(args, initUsage, nil) {
		return 0
	}
	for _, arg := range args {
		if !utf8.ValidString(arg) {
			return metaHandover("init", args, hInitArgEncoding)
		}
	}
	parsed, usage, hand := initParseArgs(args)
	if hand {
		return metaHandover("init", args, hInitJevLink)
	}
	if usage != "" {
		return metaServe("init", "", args, func(_, stderr io.Writer) int {
			_, _ = io.WriteString(stderr, "init: "+usage+"\n"+initUsage)
			return 2
		})
	}
	jsonMode := initAgent() || !initStdinTTY()
	interactive := !parsed.noInteraction && !jsonMode
	home := os.Getenv("HOME")
	plan := initPlan(parsed, home, physicalCwd(), interactive)
	if plan.hand && plan.reason == hInitSetup {
		serve, reason := initSetupPlan(parsed, home, physicalCwd(), jsonMode, plan.existing)
		if serve == nil {
			return metaHandover("init", args, reason)
		}
		return metaServe("init", "", args, serve)
	}
	if plan.hand {
		return metaHandover("init", args, plan.reason)
	}
	return metaServe("init", "", args, func(stdout, stderr io.Writer) int {
		return initFinish(stdout, stderr, home, jsonMode, plan.code, plan.message)
	})
}
