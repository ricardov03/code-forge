package session

// B523: the one-call sessions of the doctor's model probes (src/doctor/
// probes.mjs session() and reviewerInProject()). Probe is spawnSession for a
// fallback-free level with no slug and no run: one step, no ledger row, no
// budget gate, the provider slot and the rate-limit backoff kept. It adds what
// Spawn refuses and the pings need: the coder and facts roles, the S2 and
// delegate schemas and the --max-budget-usd flag. A Codex (openai) session is
// still refused (IsUnsupported) before anything is written.

import (
	"fmt"
	"math"
	mrand "math/rand"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
)

// Probe schemas (src/session/s2.mjs S2_SCHEMA, src/autopilot/delegate.mjs DELEGATE_SCHEMA).
const (
	ProbeSchemaS2       = "s2"
	ProbeSchemaDelegate = "delegate"
)

type probeProp struct {
	name     string
	types    []string // JSON Schema type names
	min, max *float64
}

func probeF(v float64) *float64 { return &v }

var probeSchemas = map[string][]probeProp{
	ProbeSchemaS2: {
		{name: "decision", types: []string{"string"}},
		{name: "confidence", types: []string{"number"}, min: probeF(0), max: probeF(1)},
		{name: "reason", types: []string{"string"}},
		{name: "overrule", types: []string{"boolean"}},
		{name: "ask_human", types: []string{"boolean"}},
		{name: "human_question", types: []string{"string", "null"}},
	},
	ProbeSchemaDelegate: {
		{name: "decision", types: []string{"string"}},
		{name: "within_scope", types: []string{"boolean"}},
		{name: "confidence", types: []string{"number"}, min: probeF(0), max: probeF(1)},
		{name: "reason", types: []string{"string"}},
		{name: "escalate", types: []string{"boolean"}},
	},
}

// probeSchemaJSON is JSON.stringify of the schema object, as the CLIs get it.
var probeSchemaJSON = map[string]string{
	ProbeSchemaS2:       `{"type":"object","properties":{"decision":{"type":"string"},"confidence":{"type":"number","minimum":0,"maximum":1},"reason":{"type":"string"},"overrule":{"type":"boolean"},"ask_human":{"type":"boolean"},"human_question":{"type":["string","null"]}},"required":["decision","confidence","reason","overrule","ask_human","human_question"],"additionalProperties":false}`,
	ProbeSchemaDelegate: `{"type":"object","properties":{"decision":{"type":"string"},"within_scope":{"type":"boolean"},"confidence":{"type":"number","minimum":0,"maximum":1},"reason":{"type":"string"},"escalate":{"type":"boolean"}},"required":["decision","within_scope","confidence","reason","escalate"],"additionalProperties":false}`,
}

func probeTypeOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case ledger.Object:
		return "object"
	case []any:
		return "array"
	}
	return ""
}

// probeMatchesSchema is matchesSchema for the S2 and delegate schemas.
func probeMatchesSchema(schema string, value any) bool {
	obj, ok := value.(ledger.Object)
	if !ok {
		return false
	}
	props := probeSchemas[schema]
	known := map[string]probeProp{}
	for _, p := range props {
		known[p.name] = p
	}
	for _, f := range obj {
		if _, ok := known[f.Key]; !ok {
			return false
		}
	}
	present := map[string]bool{}
	for _, f := range obj {
		present[f.Key] = true
	}
	for _, p := range props {
		if !present[p.name] {
			return false
		}
		v := obj.Get(p.name)
		t := probeTypeOf(v)
		if !contains(p.types, t) {
			return false
		}
		if n, isNum := v.(float64); isNum && ((p.min != nil && n < *p.min) || (p.max != nil && n > *p.max)) {
			return false
		}
	}
	return true
}

// ProbeOpts is the probe's SessionOpts: the level is used fallback-free.
type ProbeOpts struct {
	Cfg          map[string]any
	Level, Role  string
	PromptPath   string // absolute
	Cwd          string // the coder's tree ("" = the process cwd)
	Schema       string // "", ProbeSchemaS2 or ProbeSchemaDelegate
	MaxBudgetUsd float64
	TimeoutMs    float64
	RunRoot      string // ctx.runRootDir
}

// ProbeResult is what probes.mjs reads of a session result. Status "error"
// is session()'s catch: Reason is the thrown error's code or name.
type ProbeResult struct {
	Status, Reason string
	Text           string
	Answer         any // the result's answer (nil = null/undefined)
}

// ProbeAnswerText is probes.mjs answerText: the text, a newline, the answer.
func ProbeAnswerText(r ProbeResult) string {
	answer := ""
	switch a := r.Answer.(type) {
	case nil:
	case string:
		answer = a
	default:
		data, err := ledger.Stringify(a, false)
		if err == nil {
			answer = string(data)
		}
	}
	return r.Text + "\n" + answer
}

// jsTruthy is JavaScript truthiness for decoded config values.
func jsTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0 && !math.IsNaN(x)
	case int:
		return x != 0
	}
	return true
}

// ProbeLevel is resolveLevel's provider, model and effort; ok=false is
// resolveLevel's throw.
func ProbeLevel(cfg map[string]any, level string) (provider, model string, effort any, ok bool) {
	l, isMap := section(cfg, "levels")[level].(map[string]any)
	if !isMap {
		return "", "", nil, false
	}
	model, _ = l["model"].(string)
	if model == "" {
		return "", "", nil, false
	}
	raw, has := l["provider"]
	if !has || raw == nil {
		raw = cfg["provider"]
	}
	provider, _ = raw.(string)
	if provider == "" {
		return "", "", nil, false
	}
	return provider, model, l["effort"], true
}

// ProbeCLIName is cliNameForProvider ("" = no mapping).
func ProbeCLIName(provider string) string {
	switch provider {
	case "anthropic":
		return "claude"
	case "openai":
		return "codex"
	case "xai":
		return "grok"
	}
	return ""
}

// probeEffort is `if (effort) params.effort = effort` + assertEffortForProvider:
// "" = none passed; bad = the builder's TypeError.
func probeEffort(provider string, effort any) (string, bool) {
	if !jsTruthy(effort) {
		return "", true
	}
	e, isString := effort.(string)
	if !isString {
		return "", false
	}
	if provider == "anthropic" && !contains(anthropicEfforts, e) {
		return "", false
	}
	return e, true
}

func probeError(reason string) ProbeResult { return ProbeResult{Status: "error", Reason: reason} }

// probeNoToolRole is closed-book.mjs isNoToolRole for the role names spawn takes.
func probeNoToolRole(role string) bool {
	return contains([]string{"reviewer", "judge", "s2", "author", "delegate"}, role)
}

// ProbeClosedBookRefused is closed-book.mjs closedBookRefused(provider, role, openBook).
func ProbeClosedBookRefused(provider, role string, openBook bool) bool {
	return norm(provider) == "openai" && probeNoToolRole(norm(role)) && (!openBook || norm(role) == "delegate")
}

// ProbeCheck tells, before anything is written, whether Probe runs this
// session in Go: an error IsUnsupported for a Codex session.
func ProbeCheck(cfg map[string]any, level, role string) error {
	provider, _, _, ok := ProbeLevel(cfg, level)
	if !ok {
		return nil // resolveLevel throws: an "error" row, no session
	}
	if ProbeClosedBookRefused(provider, role, section(cfg, "review")["allow_open_book_codex"] == true) {
		return nil
	}
	if provider == "openai" {
		return unsupported("a Codex " + role + " session")
	}
	return nil
}

// probeBuild is buildStep + the claude/grok builders for one probe step.
func probeBuild(o ProbeOpts, provider, model, effort, sessionDir string) (built, error) {
	closedBook := o.Role != "coder"
	cwd := o.Cwd
	if closedBook {
		cwd = filepath.Join(sessionDir, "cwd")
		if err := os.Mkdir(cwd, 0o700); err != nil {
			return built{}, err
		}
	} else {
		if cwd == "" {
			cwd = "."
		}
		abs, err := filepath.Abs(cwd)
		if err != nil {
			return built{}, err
		}
		cwd = abs
	}
	budget := ""
	if o.MaxBudgetUsd > 0 {
		budget = strconv.FormatFloat(o.MaxBudgetUsd, 'f', -1, 64)
	}
	grokEnv := []string{"GROK_CURSOR_MCPS_ENABLED=false", "GROK_CLAUDE_MCPS_ENABLED=false"}
	switch {
	case provider == "anthropic" && !closedBook:
		argv := []string{"claude", "-p", "--model", model}
		if effort != "" {
			argv = append(argv, "--effort", effort)
		}
		argv = append(argv, "--permission-mode", "bypassPermissions", "--output-format", "json", "--no-session-persistence")
		if budget != "" {
			argv = append(argv, "--max-budget-usd", budget)
		}
		argv = append(argv, "--disallowedTools")
		argv = append(argv, claudeRuleStrings(forbiddenMerged())...)
		argv = append(argv, "--", o.PromptPath)
		return built{cli: "claude", argv: argv, cwd: cwd}, nil
	case provider == "anthropic":
		facts := o.Role == "facts"
		tools := ""
		if facts {
			tools = "Bash"
		}
		argv := []string{"claude", "-p", "--model", model}
		if effort != "" {
			argv = append(argv, "--effort", effort)
		}
		argv = append(argv, "--safe-mode", "--tools", tools, "--strict-mcp-config", "--no-session-persistence", "--output-format", "json")
		if o.Schema != "" {
			argv = append(argv, "--json-schema", probeSchemaJSON[o.Schema])
		}
		if budget != "" {
			argv = append(argv, "--max-budget-usd", budget)
		}
		argv = append(argv, "--permission-mode", "dontAsk")
		if facts {
			argv = append(argv, "--disallowedTools")
			argv = append(argv, claudeRuleStrings(forbiddenStatic)...)
			argv = append(argv, "Bash(rm*)", "Bash(git push*)", "Bash(git commit*)", "Bash(git merge*)", "Bash(mv*)", "Bash(cp*)", "--restricted")
		}
		return built{cli: "claude", argv: argv, cwd: cwd, stdin: true}, nil
	case !closedBook: // xai coder
		argv := []string{"grok", "--prompt-file", o.PromptPath, "-m", model}
		if effort != "" {
			argv = append(argv, "--reasoning-effort", effort)
		}
		argv = append(argv, "--permission-mode", "bypassPermissions", "--cwd", cwd)
		for _, rule := range claudeRuleStrings(forbiddenMerged()) {
			argv = append(argv, "--deny", rule)
		}
		return built{cli: "grok", argv: argv, cwd: cwd, env: grokEnv}, nil
	default: // xai closed-book
		argv := []string{"grok", "--prompt-file", o.PromptPath, "-m", model}
		if effort != "" {
			argv = append(argv, "--reasoning-effort", effort)
		}
		if o.Schema != "" {
			argv = append(argv, "--json-schema", probeSchemaJSON[o.Schema])
		}
		argv = append(argv, "--disallowed-tools", "Bash,Read,Write,Edit,Glob,Grep,WebFetch,WebSearch", "--deny", "*", "--no-plan", "--no-subagents", "--max-turns", "1", "--permission-mode", "dontAsk", "--cwd", cwd)
		return built{cli: "grok", argv: argv, cwd: cwd, env: grokEnv}, nil
	}
}

// Probe runs one probe session. err is a case Node would not answer the
// same way: IsUnsupported (a Codex session) or an I/O failure Node would
// report as an error code; the caller hands the probe over on any err.
func Probe(o ProbeOpts, deps Deps) (ProbeResult, error) {
	if !contains(Levels, o.Level) || !contains(Roles, o.Role) || !filepath.IsAbs(o.PromptPath) {
		return probeError("usage"), nil
	}
	if o.Schema != "" && probeSchemas[o.Schema] == nil {
		return ProbeResult{}, unsupported("schema " + strconv.Quote(o.Schema))
	}
	if o.TimeoutMs <= 0 || o.TimeoutMs > math.MaxInt32 || o.RunRoot == "" {
		return ProbeResult{}, unsupported("this timeout or run root")
	}
	provider, model, rawEffort, ok := ProbeLevel(o.Cfg, o.Level)
	if !ok {
		return probeError("Error"), nil
	}
	openBook := section(o.Cfg, "review")["allow_open_book_codex"] == true
	if ProbeClosedBookRefused(provider, o.Role, openBook) {
		return probeError("closed-book"), nil
	}
	if provider == "openai" {
		return ProbeResult{}, unsupported("a Codex " + o.Role + " session")
	}
	random := deps.Random
	if random == nil {
		random = mrand.Float64
	}
	waits := 0
	for {
		res, err := probeAttempt(o, deps, provider, model, rawEffort)
		if err != nil || res.Status != "unavailable" || res.Reason != "rate-limited" || waits >= len(rateLimitBackoffMs) {
			return res, err
		}
		wait := BackoffMs(waits, random)
		waits++
		deps.stderr(fmt.Sprintf("rate-limited: provider=%s model=%s retry in %s s\n", provider, model, strconv.FormatFloat(wait/1000, 'f', 1, 64)))
		if deps.Sleep != nil {
			deps.Sleep(wait)
		} else {
			time.Sleep(time.Duration(wait * float64(time.Millisecond)))
		}
	}
}

func probeAttempt(o ProbeOpts, deps Deps, provider, model string, rawEffort any) (ProbeResult, error) {
	release := acquire(provider, providerLimit(o.Cfg, provider))
	defer release()
	sessionDir := filepath.Join(o.RunRoot, "sessions", o.Role+"-"+strconv.FormatInt(int64(deps.now()), 10)+"-"+randomHex())
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		return ProbeResult{}, err
	}
	defer os.RemoveAll(sessionDir)
	if o.Role != "coder" {
		// buildStep makes the closed-book cwd before it maps the provider
		if ProbeCLIName(provider) == "" {
			if err := os.Mkdir(filepath.Join(sessionDir, "cwd"), 0o700); err != nil {
				return ProbeResult{}, err
			}
		}
	}
	if ProbeCLIName(provider) == "" {
		return probeError("usage"), nil
	}
	effort, valid := probeEffort(provider, rawEffort)
	if !valid {
		return probeError("TypeError"), nil
	}
	b, err := probeBuild(o, provider, model, effort, sessionDir)
	if err != nil {
		return ProbeResult{}, err
	}
	if bin, ok := deps.Bins[b.cli]; ok {
		b.argv[0] = bin
	}
	if o.Role == "coder" && forbiddenHit(b.argv, forbiddenMerged()) != "" {
		return probeError("forbidden"), nil
	}
	effortText := "none"
	switch e := rawEffort.(type) {
	case string:
		effortText = e
	case bool:
		effortText = strconv.FormatBool(e)
	case float64:
		effortText = jsNum(e)
	}
	deps.stderr(fmt.Sprintf("level=%s provider=%s model=%s effort=%s fallback_step=0\n", o.Level, provider, model, effortText))
	so := Opts{Cfg: o.Cfg, Level: o.Level, Role: o.Role, PromptPath: o.PromptPath, TimeoutMs: o.TimeoutMs, RunRoot: o.RunRoot}
	res, err := foreground(b, so, deps)
	if err != nil {
		return ProbeResult{}, err
	}
	out := ProbeResult{}
	out.Status, _ = res.Get("status").(string)
	out.Reason, _ = res.Get("reason").(string)
	out.Text, _ = res.Get("text").(string)
	out.Answer = res.Get("answer")
	if out.Status == "ok" && o.Schema != "" && !probeMatchesSchema(o.Schema, out.Answer) {
		out.Status, out.Reason, out.Answer = "invalid-output", "answer does not match the schema", nil
	}
	return out, nil
}

// ProbeInProject is probes.mjs reviewerInProject: the closed-book reviewer
// argv of level L2 run with its cwd set to the project itself (no session
// dir, the packet on stdin for claude), environ minus CODE_FORGE_/JEV_ and
// no builder env. promptPath is the written prompt file.
func ProbeInProject(cfg map[string]any, promptPath, projectDir string, deps Deps) (ProbeResult, error) {
	provider, model, rawEffort, ok := ProbeLevel(cfg, "L2")
	if !ok {
		return probeError("Error"), nil
	}
	cli := ProbeCLIName(provider)
	if cli == "" {
		return probeError("no-cli-mapping"), nil
	}
	if cli == "codex" {
		return ProbeResult{}, unsupported("a Codex isolation probe")
	}
	effort, valid := probeEffort(provider, rawEffort)
	if !valid {
		return probeError("TypeError"), nil
	}
	var argv []string
	var input []byte
	if cli == "claude" {
		argv = []string{"claude", "-p", "--model", model}
		if effort != "" {
			argv = append(argv, "--effort", effort)
		}
		argv = append(argv, "--safe-mode", "--tools", "", "--strict-mcp-config", "--no-session-persistence", "--output-format", "json", "--max-budget-usd", "0.5", "--permission-mode", "dontAsk")
		data, err := os.ReadFile(promptPath)
		if err != nil {
			return ProbeResult{}, err
		}
		input = data
	} else {
		argv = []string{"grok", "--prompt-file", promptPath, "-m", model}
		if effort != "" {
			argv = append(argv, "--reasoning-effort", effort)
		}
		argv = append(argv, "--disallowed-tools", "Bash,Read,Write,Edit,Glob,Grep,WebFetch,WebSearch", "--deny", "*", "--no-plan", "--no-subagents", "--max-turns", "1", "--permission-mode", "dontAsk", "--cwd", projectDir)
	}
	if bin, ok := deps.Bins[cli]; ok {
		argv[0] = bin
	}
	environ := deps.Environ
	if environ == nil {
		environ = os.Environ()
	}
	eo := execOpts{Argv: argv, Cwd: projectDir, Env: childEnv(environ, nil), Input: input, TimeoutMs: 180000, RunIDFromParent: deps.RunID}
	if deps.PidsRoot != nil {
		if dir, err := deps.PidsRoot(); err == nil {
			eo.PidsDir = filepath.Join(dir, "pids")
		}
	}
	runner := deps.Exec
	if runner == nil {
		runner = run
	}
	res := runner(eo)
	parsed, text, answer, hasAnswer := readAnswer(res.Stdout)
	if reason := classifyUnavailable(res, parsed); reason != "" {
		return ProbeResult{Status: "unavailable", Reason: reason, Text: text}, nil
	}
	isError := false
	if p, ok := parsed.(ledger.Object); ok {
		isError = p.Get("is_error") == true
	}
	if !res.OK || isError {
		code := "null"
		if c, ok := res.Code.(float64); ok {
			code = jsNum(c)
		}
		return ProbeResult{Status: "failed", Reason: "exit " + code, Text: text}, nil
	}
	if !hasAnswer {
		answer = nil
	}
	return ProbeResult{Status: "ok", Text: text, Answer: answer}, nil
}

// ProbeGitInit is probes.mjs tempRepo's `git init -q` (20 s, the git child
// env); like Node it ignores how git exits.
func ProbeGitInit(repo string, environ []string) {
	if environ == nil {
		environ = os.Environ()
	}
	env := []string{}
	for _, kv := range environ {
		if len(kv) >= 4 && kv[:4] == "GIT_" {
			continue
		}
		env = append(env, kv)
	}
	env = setEnv(setEnv(setEnv(env, "GIT_CONFIG_NOSYSTEM", "1"), "GIT_CONFIG_GLOBAL", "/dev/null"), "GIT_TERMINAL_PROMPT", "0")
	run(execOpts{Argv: []string{"git", "init", "-q"}, Cwd: repo, Env: env, TimeoutMs: 20000})
}

// DelegateSchema is src/autopilot/delegate.mjs DELEGATE_SCHEMA in its own key
// order (B536: the delegate session of `autopilot ask`).
func DelegateSchema() ledger.Object {
	schema, err := ledger.DecodeObject([]byte(probeSchemaJSON[ProbeSchemaDelegate]))
	if err != nil {
		panic(err)
	}
	return schema
}

// MatchesDelegateSchema is matchesSchema(DELEGATE_SCHEMA, value).
func MatchesDelegateSchema(value any) bool { return probeMatchesSchema(ProbeSchemaDelegate, value) }
