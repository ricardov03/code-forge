// Package session ports the foreground, closed-book path of
// src/session/spawn.mjs (spawnSession / previewArgv): a reviewer, judge, S2,
// author or delegate session run through the claude or grok CLI, with the
// fallback ladder, the provider slot, the rate-limit backoff, the unsigned
// `session` ledger row and Node's result object. Every case it does not port
// is refused BEFORE anything runs (IsUnsupported), so a caller can still hand
// the session to Node: the coder and facts roles, a background session, an
// openai step the open-book opt-in would run, a schema other than
// finding.schema.json, and a level Node's resolveLevel or argv builders would
// throw on. B482: a run-pinned session passes budgetGate (gate.go) first.
package session

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	mrand "math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/ricardov03/code-forge/native/docsync"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/review"
)

// Roles and Levels are spawn.mjs ROLES and LEVELS.
var (
	Roles  = []string{"coder", "reviewer", "judge", "s2", "author", "facts", "delegate"}
	Levels = []string{"L0", "L1", "L2", "L3"}
)

// DefaultTimeoutMs is spawn.mjs DEFAULT_TIMEOUT_MS.
const DefaultTimeoutMs = 15 * 60 * 1000

// ClosedBookRefusal is closed-book.mjs CODEX_CLOSED_BOOK_REFUSAL.
const ClosedBookRefusal = "codex cannot run closed-book yet: it always has a shell; use anthropic or xai for reviewer, judge, S2 and plan author"

// FindingSchemaID is finding.schema.json's $id.
const FindingSchemaID = "https://code-forge.local/schema/review/finding.schema.json"

// FindingSchemaPath names src/review/finding.schema.json; tests point it at the repository copy.
var FindingSchemaPath = func() (string, error) {
	root, err := docsync.PackageRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "src", "review", "finding.schema.json"), nil
}

// LoadFindingSchema reads finding.schema.json in its own key order.
func LoadFindingSchema() (ledger.Object, error) {
	p, err := FindingSchemaPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	return ledger.DecodeObject(data)
}

// Error is spawn.mjs SessionError (code: usage, closed-book, ...).
type Error struct{ Code, Message string }

func (e *Error) Error() string { return e.Message }

type unsupportedError struct{ why string }

func (e *unsupportedError) Error() string { return "session: not ported: " + e.why }

func unsupported(why string) error { return &unsupportedError{why} }

// IsUnsupported reports a refusal made before anything ran: hand the session to Node.
func IsUnsupported(err error) bool {
	var u *unsupportedError
	return errors.As(err, &u)
}

// Opts is spawn.mjs SessionOpts for the ported path.
type Opts struct {
	Cfg        map[string]any
	Level      string
	Role       string
	PromptPath string        // absolute
	Schema     ledger.Object // nil, or finding.schema.json as loaded
	TimeoutMs  float64       // 0 = DefaultTimeoutMs
	Run        *string
	Block      *string
	Slug       string // "" with no Deps.WriteRow = no row
	RowExtra   ledger.Object
	RunRoot    string // "" = Deps.RunRoot()
	// RunGateClear skips the run-pinned gate (budget.usd and the autopilot
	// category cap): the caller checked that spawn.mjs budgetGate is a no-op.
	RunGateClear bool
	// SystemPrompt is spawn.mjs systemPromptText (B536): claude --system-prompt,
	// grok --system-prompt-override; nil = none.
	SystemPrompt *string
	// Validate is matchesSchema for a Schema other than finding.schema.json
	// (B536): the answer is kept as given (no review coercion) when it passes.
	Validate func(answer any) bool
}

// Deps are the seams (SessionDeps).
type Deps struct {
	Stderr   io.Writer
	Environ  []string                  // nil = os.Environ()
	WriteRow func(ledger.Object) error // nil = appendRow to <Home>/.code-forge/ledger when Slug != ""
	Home     string                    // nil WriteRow's ledger home ("" = $HOME)
	Sleep    func(ms float64)          // nil = time.Sleep
	Random   func() float64            // nil = math/rand
	NowMs    func() float64            // nil = the wall clock
	Bins     map[string]string         // replaces argv[0] by CLI name
	RunID    string                    // CODE_FORGE_RUN_ID for the child when the env has none
	RunRoot  func() (string, error)    // the lazy proc root when Opts.RunRoot is ""
	// PidsRoot is exec.mjs pidsDir()'s root (currentRunRoot) when it is not
	// Opts.RunRoot (a --run session registers its child in the process's lazy
	// root); nil = Opts.RunRoot. An error registers no entry, as Node's catch.
	PidsRoot func() (string, error)
	Exec     func(execOpts) execResult // nil = run
	// ReadRows is the run-pinned gate's ledger read (spawn.mjs deps.readRows);
	// nil = every row of the session's slug ledger under Home.
	ReadRows func() ([]ledger.Row, error)
	// Warn is the error log's warning line (logWarning) of the budget.usd 80 %
	// warning; nil = a session that would warn hands over (IsUnsupported).
	Warn func(warning, message string)
}

func (d Deps) now() float64 {
	if d.NowMs != nil {
		return d.NowMs()
	}
	return float64(time.Now().UnixMilli())
}

type step struct {
	provider, model string
	effort          any // nil or a string
	fallbackStep    int
}

func (s step) effortValue() any { return s.effort }

func (s step) effortText() string {
	if s.effort == nil {
		return "none"
	}
	return s.effort.(string)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func section(cfg map[string]any, key string) map[string]any {
	m, _ := cfg[key].(map[string]any)
	return m
}

func norm(s string) string { return strings.ToLower(strings.TrimFunc(s, jsSpace)) }

var anthropicEfforts = []string{"low", "medium", "high", "xhigh", "max"}

// plan is validateSessionOpts + sessionSteps, refusing every case not ported.
// skipped is the number of openai fallback steps dropped.
func plan(o Opts) ([]step, int, error) {
	if !contains(Levels, o.Level) {
		return nil, 0, &Error{"usage", "level must be one of " + strings.Join(Levels, ", ")}
	}
	if !contains(Roles, o.Role) {
		return nil, 0, &Error{"usage", "role must be one of " + strings.Join(Roles, ", ")}
	}
	if !filepath.IsAbs(o.PromptPath) {
		return nil, 0, &Error{"usage", "promptPath must be an absolute path"}
	}
	if o.Role == "coder" || o.Role == "facts" {
		return nil, 0, unsupported("the " + o.Role + " role")
	}
	if o.Schema != nil && o.Schema.Get("$id") != FindingSchemaID && o.Validate == nil {
		return nil, 0, unsupported("a schema other than finding.schema.json")
	}
	if o.TimeoutMs < 0 || o.TimeoutMs > math.MaxInt32 {
		return nil, 0, unsupported("this timeout")
	}
	// resolveLevel: a throw there is a crash in Node, not a refusal
	level, ok := section(o.Cfg, "levels")[o.Level].(map[string]any)
	if !ok {
		return nil, 0, unsupported("levels." + o.Level + " is missing")
	}
	model, _ := level["model"].(string)
	if model == "" {
		return nil, 0, unsupported("levels." + o.Level + " has no model")
	}
	rawProvider, has := level["provider"]
	if !has || rawProvider == nil {
		rawProvider = o.Cfg["provider"]
	}
	provider, _ := rawProvider.(string)
	if provider == "" {
		return nil, 0, unsupported("levels." + o.Level + " has no provider")
	}
	first := step{provider: provider, model: model, effort: level["effort"]}
	ladder := []step{first}
	if fb, has := level["fallback"]; has && fb != nil {
		list, ok := fb.([]any)
		if !ok {
			return nil, 0, unsupported("a fallback that is not a list")
		}
		for k, raw := range list {
			entry, ok := raw.(map[string]any)
			if !ok {
				return nil, 0, unsupported("a fallback entry that is not an object")
			}
			p, _ := entry["provider"].(string)
			m, _ := entry["model"].(string)
			if p == "" || m == "" {
				return nil, 0, unsupported("a fallback entry without provider and model")
			}
			ladder = append(ladder, step{provider: p, model: m, effort: entry["effort"], fallbackStep: k + 1})
		}
	}
	openBook := section(o.Cfg, "review")["allow_open_book_codex"] == true
	refused := func(s step) bool {
		return norm(s.provider) == "openai" && (!openBook || o.Role == "delegate")
	}
	if refused(ladder[0]) {
		return nil, 0, &Error{"closed-book", ClosedBookRefusal}
	}
	var steps []step
	for _, s := range ladder {
		if refused(s) {
			continue
		}
		if s.provider != "anthropic" && s.provider != "xai" {
			return nil, 0, unsupported("provider " + strconv.Quote(s.provider))
		}
		if s.effort != nil {
			e, ok := s.effort.(string)
			if !ok {
				return nil, 0, unsupported("an effort that is not a string")
			}
			if e != "" && s.provider == "anthropic" && !contains(anthropicEfforts, e) {
				return nil, 0, unsupported("an effort the claude builder refuses")
			}
		}
		steps = append(steps, s)
	}
	return steps, len(ladder) - len(steps), nil
}

// Check tells, before anything runs, whether Spawn serves these options.
func Check(o Opts) error {
	_, _, err := plan(o)
	return err
}

type built struct {
	cli   string
	argv  []string
	cwd   string
	stdin bool // pipe the prompt file's bytes
	env   []string
}

// buildStep is spawn.mjs buildStep + the claude/grok closed-book builders.
func buildStep(o Opts, s step, sessionDir string) (built, error) {
	cwd := filepath.Join(sessionDir, "cwd")
	if err := os.Mkdir(cwd, 0o700); err != nil {
		return built{}, err
	}
	effort, _ := s.effort.(string)
	switch s.provider {
	case "anthropic":
		argv := []string{"claude", "-p", "--model", s.model}
		if effort != "" {
			argv = append(argv, "--effort", effort)
		}
		argv = append(argv, "--safe-mode", "--tools", "", "--strict-mcp-config", "--no-session-persistence")
		if o.SystemPrompt != nil {
			argv = append(argv, "--system-prompt", *o.SystemPrompt)
		}
		argv = append(argv, "--output-format", "json")
		if o.Schema != nil {
			body := o.Schema.Clone()
			body.Delete("$schema")
			text, err := ledger.Stringify(body, false)
			if err != nil {
				return built{}, err
			}
			argv = append(argv, "--json-schema", string(text))
		}
		argv = append(argv, "--permission-mode", "dontAsk")
		return built{cli: "claude", argv: argv, cwd: cwd, stdin: true}, nil
	default: // xai
		argv := []string{"grok", "--prompt-file", o.PromptPath, "-m", s.model}
		if effort != "" {
			argv = append(argv, "--reasoning-effort", effort)
		}
		if o.Schema != nil {
			text, err := ledger.Stringify(o.Schema, false)
			if err != nil {
				return built{}, err
			}
			argv = append(argv, "--json-schema", string(text))
		}
		argv = append(argv, "--disallowed-tools", "Bash,Read,Write,Edit,Glob,Grep,WebFetch,WebSearch", "--deny", "*", "--no-plan", "--no-subagents", "--max-turns", "1", "--permission-mode", "dontAsk", "--cwd", cwd)
		if o.SystemPrompt != nil {
			argv = append(argv, "--system-prompt-override", *o.SystemPrompt)
		}
		return built{cli: "grok", argv: argv, cwd: cwd, env: []string{"GROK_CURSOR_MCPS_ENABLED=false", "GROK_CLAUDE_MCPS_ENABLED=false"}}, nil
	}
}

// Preview is previewArgv: step 0's argv with the session dir as <session>.
func Preview(o Opts, deps Deps) (ledger.Object, error) {
	steps, skipped, err := plan(o)
	if err != nil {
		return nil, err
	}
	deps.skipNote(skipped)
	s := steps[0]
	dir, err := os.MkdirTemp(tempDir(), "code-forge-preview-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	b, err := buildStep(o, s, dir)
	if err != nil {
		return nil, err
	}
	if bin, ok := deps.Bins[b.cli]; ok {
		b.argv[0] = bin
	}
	argv := make([]any, len(b.argv))
	for i, a := range b.argv {
		argv[i] = strings.ReplaceAll(a, dir, "<session>")
	}
	return ledger.Object{{Key: "argv", Value: argv}, {Key: "cwd", Value: strings.ReplaceAll(b.cwd, dir, "<session>")},
		{Key: "provider", Value: s.provider}, {Key: "model", Value: s.model}, {Key: "effort", Value: s.effortValue()},
		{Key: "fallback_step", Value: float64(s.fallbackStep)}}, nil
}

// tempDir is os.tmpdir(): $TMPDIR without a trailing slash, else /tmp.
func tempDir() string {
	dir := os.Getenv("TMPDIR")
	if dir == "" {
		return "/tmp"
	}
	if len(dir) > 1 && strings.HasSuffix(dir, "/") {
		dir = dir[:len(dir)-1]
	}
	return dir
}

func (d Deps) stderr(s string) {
	if d.Stderr != nil {
		_, _ = io.WriteString(d.Stderr, s)
	}
}

func (d Deps) skipNote(skipped int) {
	if skipped > 0 {
		d.stderr(fmt.Sprintf("fallback skipped: %d openai step(s) — %s\n", skipped, ClosedBookRefusal))
	}
}

// ProviderLimit is spawn.mjs providerLimit (B485: the worker's session cap).
func ProviderLimit(cfg map[string]any, provider string) int { return providerLimit(cfg, provider) }

// providerLimit is spawn.mjs providerLimit.
func providerLimit(cfg map[string]any, provider string) int {
	set, ok := section(section(cfg, "review"), "provider_concurrency")[provider].(float64)
	if ok && set >= 1 && set == math.Trunc(set) && set <= 1<<53 {
		if set > 1<<20 {
			return 1 << 20
		}
		return int(set)
	}
	switch provider {
	case "anthropic":
		return 4
	case "openai", "xai":
		return 2
	}
	return 1
}

var (
	semMu      sync.Mutex
	semaphores = map[string]chan struct{}{}
)

func acquire(provider string, limit int) func() {
	key := provider + ":" + strconv.Itoa(limit)
	semMu.Lock()
	sem, ok := semaphores[key]
	if !ok {
		sem = make(chan struct{}, limit)
		semaphores[key] = sem
	}
	semMu.Unlock()
	sem <- struct{}{}
	return func() { <-sem }
}

// rateLimitBackoffMs is RATE_LIMIT_BACKOFF_MS.
var rateLimitBackoffMs = []float64{2000, 6000}

func jsRound(x float64) float64 { return math.Floor(x + 0.5) }

// BackoffMs is spawn.mjs backoffMs.
func BackoffMs(n int, random func() float64) float64 {
	return jsRound(rateLimitBackoffMs[n] * (1 + 0.3*(2*random()-1)))
}

var pricePerK = map[string]map[string]float64{
	"anthropic": {"L0": 0.001, "L1": 0.003, "L2": 0.015, "L3": 0.03},
	"openai":    {"L0": 0.001, "L1": 0.0025, "L2": 0.012, "L3": 0.025},
	"xai":       {"L0": 0.001, "L1": 0.002, "L2": 0.01, "L3": 0.02},
}

// Spawn is spawnSession for the ported path. A non-nil error is a refusal
// before anything ran (*Error, or IsUnsupported) or a throw after (an
// unreadable prompt, a failed ledger write).
func Spawn(o Opts, deps Deps) (ledger.Object, error) {
	steps, skipped, perr := plan(o)
	var sessionErr *Error
	if perr != nil && (IsUnsupported(perr) || (errors.As(perr, &sessionErr) && sessionErr.Code == "usage")) {
		return nil, perr
	}
	// Node resolves the run root after the usage checks, before the ladder
	runRoot := o.RunRoot
	if runRoot == "" {
		if deps.RunRoot == nil {
			return nil, errors.New("session: no run root")
		}
		var err error
		if runRoot, err = deps.RunRoot(); err != nil {
			return nil, err
		}
		o.RunRoot = runRoot
	}
	if perr != nil {
		return nil, perr
	}
	deps.skipNote(skipped)
	writeRow := deps.WriteRow
	if writeRow == nil && o.Slug != "" {
		home := deps.Home
		if home == "" {
			home = os.Getenv("HOME")
		}
		writeRow = func(row ledger.Object) error {
			_, err := ledger.AppendObject(filepath.Join(home, ".code-forge", "ledger"), o.Slug, row, ledger.DefaultRotateAtBytes)
			return err
		}
	}
	sleep := deps.Sleep
	if sleep == nil {
		sleep = func(ms float64) { time.Sleep(time.Duration(ms * float64(time.Millisecond))) }
	}
	random := deps.Random
	if random == nil {
		random = mrand.Float64
	}
	attempts := []any{}
	waits := 0
	for i := 0; i < len(steps); i++ {
		s := steps[i]
		release := acquire(s.provider, providerLimit(o.Cfg, s.provider))
		// B40/B41/B48: the provider slot first, then the run-pinned gate
		releaseGate := func() {}
		if !o.RunGateClear {
			refused, rel, err := budgetGate(o, deps, writeRow, s)
			if err != nil {
				release()
				return nil, err
			}
			if refused != nil {
				release()
				out := refused.Clone()
				out.Set("provider", s.provider)
				out.Set("model", s.model)
				out.Set("effort", s.effortValue())
				out.Set("fallback_step", float64(s.fallbackStep))
				out.Set("attempts", append([]any{}, attempts...))
				return out, nil
			}
			releaseGate = rel
		}
		res, waitMs, err := attempt(o, deps, s, i+1 == len(steps), runRoot, writeRow, &attempts, &waits, random, releaseGate)
		releaseGate()
		release()
		if err != nil || res != nil {
			return res, err
		}
		if waitMs >= 0 {
			deps.stderr(fmt.Sprintf("rate-limited: provider=%s model=%s retry in %s s\n", s.provider, s.model, strconv.FormatFloat(waitMs/1000, 'f', 1, 64)))
			sleep(waitMs)
			i--
			continue
		}
		waits = 0
	}
	return nil, errors.New("spawnSession: unreachable — the ladder always has a step")
}

func randomHex() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// attempt is runAttempt: a result ends the session; waitMs ≥ 0 retries the
// step after the wait; waitMs < 0 goes to the next step.
func attempt(o Opts, deps Deps, s step, isLast bool, runRoot string, writeRow func(ledger.Object) error, attempts *[]any, waits *int, random func() float64, releaseGate func()) (ledger.Object, float64, error) {
	sessionDir := filepath.Join(runRoot, "sessions", o.Role+"-"+strconv.FormatInt(int64(deps.now()), 10)+"-"+randomHex())
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		return nil, -1, err
	}
	defer os.RemoveAll(sessionDir)
	b, err := buildStep(o, s, sessionDir)
	if err != nil {
		return nil, -1, err
	}
	if bin, ok := deps.Bins[b.cli]; ok {
		b.argv[0] = bin
	}
	deps.stderr(fmt.Sprintf("level=%s provider=%s model=%s effort=%s fallback_step=%d\n", o.Level, s.provider, s.model, s.effortText(), s.fallbackStep))
	base := ledger.Object{{Key: "role", Value: o.Role}, {Key: "level", Value: o.Level}, {Key: "provider", Value: s.provider}, {Key: "model", Value: s.model},
		{Key: "effort", Value: s.effortValue()}, {Key: "fallback_step", Value: float64(s.fallbackStep)}, {Key: "run", Value: strPtr(o.Run)}, {Key: "block", Value: strPtr(o.Block)}}
	if o.Role == "s2" {
		source := "s2"
		if s.fallbackStep > 0 {
			source = "s2-fallback"
		}
		base.Set("source", source)
	}
	for _, f := range o.RowExtra {
		base.Set(f.Key, f.Value)
	}
	res, err := foreground(b, o, deps)
	if err != nil {
		return nil, -1, err
	}
	usage := res.Get("usage").(ledger.Object)
	row := ledger.Object{{Key: "event", Value: "session"}}
	for _, f := range base {
		row.Set(f.Key, f.Value)
	}
	row.Set("status", res.Get("status"))
	reason, _ := res.Get("reason").(string)
	if reason != "" {
		row.Set("reason", reason)
	}
	tin, tout := usage.Get("tokens_in").(float64), usage.Get("tokens_out").(float64)
	row.Set("tokens_in", tin)
	row.Set("tokens_out", tout)
	row.Set("tokens_source", usage.Get("tokens_source"))
	usd := jsRound((tin+tout)/1000*pricePerK[s.provider][o.Level]*10000) / 10000
	if math.IsInf(usd, 0) || math.IsNaN(usd) {
		row.Set("usd", nil)
		row.Set("usd_unknown", true)
	} else {
		row.Set("usd", usd)
		row.Set("cost_source", "estimated")
	}
	row.Set("duration_ms", res.Get("duration_ms"))
	if o.Level == "L3" && s.fallbackStep > 0 {
		row.Set("l3_fallback", true)
	}
	if writeRow != nil {
		err := writeRow(row)
		releaseGate() // B41: the row now carries the spend (or the write failed)
		if err != nil {
			return nil, -1, err
		}
	}
	releaseGate()
	var reasonValue any
	if reason != "" {
		reasonValue = reason
	}
	*attempts = append(*attempts, ledger.Object{{Key: "fallback_step", Value: float64(s.fallbackStep)}, {Key: "provider", Value: s.provider},
		{Key: "model", Value: s.model}, {Key: "status", Value: res.Get("status")}, {Key: "reason", Value: reasonValue}})
	status := res.Get("status")
	if status == "unavailable" && reason == "rate-limited" && *waits < len(rateLimitBackoffMs) {
		wait := BackoffMs(*waits, random)
		*waits++
		return nil, wait, nil
	}
	if status == "unavailable" && !isLast {
		return nil, -1, nil
	}
	out := res.Clone()
	out.Set("provider", s.provider)
	out.Set("model", s.model)
	out.Set("effort", s.effortValue())
	out.Set("fallback_step", float64(s.fallbackStep))
	out.Set("row", row)
	out.Set("attempts", append([]any{}, *attempts...))
	return out, -1, nil
}

func strPtr(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// childEnv drops code-forge's own and Jev variables (§8.2), then adds the builder's.
func childEnv(environ, extra []string) []string {
	out := []string{}
	for _, kv := range environ {
		if strings.HasPrefix(kv, "CODE_FORGE_") || strings.HasPrefix(kv, "JEV_") {
			continue
		}
		out = append(out, kv)
	}
	for _, kv := range extra {
		k, v, _ := strings.Cut(kv, "=")
		out = setEnv(out, k, v)
	}
	return out
}

func ceilQuarter(n int) float64 { return float64((n + 3) / 4) }

// foreground is runForeground for the claude and grok CLIs.
func foreground(b built, o Opts, deps Deps) (ledger.Object, error) {
	var input []byte
	if b.stdin {
		data, err := os.ReadFile(o.PromptPath)
		if err != nil {
			return nil, err
		}
		input = data
	}
	started := deps.now()
	silence, ok := section(o.Cfg, "review")["silence_timeout_s"].(float64)
	if !ok || silence <= 0 || silence != math.Trunc(silence) {
		silence = 60
	}
	silenceMs := silence * 1000
	timeoutMs := o.TimeoutMs
	if timeoutMs == 0 {
		timeoutMs = DefaultTimeoutMs
	}
	environ := deps.Environ
	if environ == nil {
		environ = os.Environ()
	}
	eo := execOpts{Argv: b.argv, Cwd: b.cwd, Env: childEnv(environ, b.env), Input: input, TimeoutMs: timeoutMs, RunIDFromParent: deps.RunID}
	if o.Role == "reviewer" || o.Role == "judge" {
		eo.SilenceMs = silenceMs
	}
	if deps.PidsRoot != nil {
		if dir, err := deps.PidsRoot(); err == nil {
			eo.PidsDir = filepath.Join(dir, "pids")
		}
	} else if o.RunRoot != "" {
		eo.PidsDir = filepath.Join(o.RunRoot, "pids")
	}
	runner := deps.Exec
	if runner == nil {
		runner = run
	}
	res := runner(eo)
	duration := deps.now() - started
	parsed, text, answer, hasAnswer := readAnswer(res.Stdout)
	inputBytes := len(input)
	if input == nil {
		if st, err := os.Stat(o.PromptPath); err == nil {
			inputBytes = int(st.Size())
		}
	}
	usage := parseUsage(b.cli, parsed)
	if usage == nil {
		usage = ledger.Object{{Key: "tokens_in", Value: ceilQuarter(inputBytes)}, {Key: "tokens_out", Value: ceilQuarter(len(res.Stdout))}, {Key: "tokens_source", Value: "estimated"}}
	}
	common := ledger.Object{{Key: "usage", Value: usage}, {Key: "duration_ms", Value: duration}, {Key: "text", Value: text}, {Key: "exit_code", Value: res.Code}, {Key: "stderr", Value: res.Stderr}}
	with := func(head ledger.Object, tail ...ledger.Field) ledger.Object {
		out := head
		for _, f := range common {
			out.Set(f.Key, f.Value)
		}
		for _, f := range tail {
			out.Set(f.Key, f.Value)
		}
		return out
	}
	isError := false
	if p, ok := parsed.(ledger.Object); ok {
		isError = p.Get("is_error") == true
	}
	if reason := classifyUnavailable(res, parsed); reason != "" {
		return with(ledger.Object{{Key: "status", Value: "unavailable"}, {Key: "reason", Value: reason}, {Key: "answer", Value: nil}}), nil
	}
	if res.TimedOut {
		var packet *string
		if input != nil {
			t := utf8Text(input)
			packet = &t
		} else if data, err := os.ReadFile(o.PromptPath); err == nil {
			t := utf8Text(data)
			packet = &t
		}
		tail, note := StderrTail(res.Stderr, packet)
		reason := "killed after " + jsNum(timeoutMs) + " ms"
		if res.SilenceTimedOut {
			reason = "killed after " + jsNum(silenceMs) + " ms without output"
		}
		fields := []ledger.Field{{Key: "stderr_tail", Value: tail}}
		if note != "" {
			fields = append(fields, ledger.Field{Key: "tail_note", Value: note})
		}
		fields = append(fields, stdoutEvents(res.Stdout)...)
		return with(ledger.Object{{Key: "status", Value: "timeout"}, {Key: "reason", Value: reason}, {Key: "answer", Value: nil}}, fields...), nil
	}
	if !res.OK || isError {
		code := "null"
		if c, ok := res.Code.(float64); ok {
			code = jsNum(c)
		}
		return with(ledger.Object{{Key: "status", Value: "failed"}, {Key: "reason", Value: "exit " + code}, {Key: "answer", Value: nil}}), nil
	}
	if o.Schema != nil && o.Validate != nil {
		if !hasAnswer || !o.Validate(answer) {
			return with(ledger.Object{{Key: "status", Value: "invalid-output"}, {Key: "reason", Value: "answer does not match the schema"}, {Key: "answer", Value: nil}}), nil
		}
		return with(ledger.Object{{Key: "status", Value: "ok"}, {Key: "answer", Value: answer}}), nil
	}
	if o.Schema != nil {
		var raw any
		if hasAnswer {
			raw = answer
		}
		coercedAnswer, coerced := review.CoerceReviewPassed(raw)
		providerAnswer := raw
		if coerced {
			p := raw.(ledger.Object).Clone()
			p.Set("passed", coercedAnswer.(ledger.Object).Get("passed"))
			providerAnswer = p
		}
		if !hasAnswer || !review.ValidAnswer(providerAnswer) {
			return with(ledger.Object{{Key: "status", Value: "invalid-output"}, {Key: "reason", Value: "answer does not match the schema"}, {Key: "answer", Value: nil}}), nil
		}
		var tail []ledger.Field
		if coerced {
			tail = append(tail, ledger.Field{Key: "passed_coerced", Value: true})
		}
		return with(ledger.Object{{Key: "status", Value: "ok"}, {Key: "answer", Value: coercedAnswer}}, tail...), nil
	}
	var value any = text
	if hasAnswer && answer != nil {
		value = answer
	}
	return with(ledger.Object{{Key: "status", Value: "ok"}, {Key: "answer", Value: value}}), nil
}

// jsNum is String(n) for the integers these messages carry.
func jsNum(n float64) string {
	if n == math.Trunc(n) && math.Abs(n) < 1e21 {
		return strconv.FormatFloat(n, 'f', -1, 64)
	}
	return strconv.FormatFloat(n, 'g', -1, 64)
}

// jsSpace is the white space String.prototype.trim removes.
func jsSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

func trimJS(s string) string { return strings.TrimFunc(s, jsSpace) }

// tryJSON is spawn.mjs tryJSON: (value, true) for one JSON text, ordered.
func tryJSON(text string) (any, bool) {
	if trimJS(text) == "" {
		return nil, false
	}
	dec := json.NewDecoder(strings.NewReader(text))
	v, err := decodeValue(dec)
	if err != nil {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false
	}
	return v, true
}

func decodeValue(dec *json.Decoder) (any, error) {
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
		obj := ledger.Object{}
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return nil, err
			}
			value, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			obj.Set(key.(string), value)
		}
		_, err := dec.Token()
		return obj, err
	case '[':
		array := []any{}
		for dec.More() {
			value, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		_, err := dec.Token()
		return array, err
	}
	return nil, fmt.Errorf("unexpected JSON delimiter %q", delim)
}

// readAnswer is spawn.mjs readAnswer for claude and grok: (parsed or nil,
// text, answer, whether answer is defined).
func readAnswer(stdout string) (any, string, any, bool) {
	parsed, _ := tryJSON(trimJS(stdout))
	text := stdout
	obj, isObj := parsed.(ledger.Object)
	if isObj {
		if s, ok := obj.Get("result").(string); ok {
			text = s
		} else if s, ok := obj.Get("text").(string); ok {
			text = s
		}
		for _, key := range []string{"structured_output", "structuredOutput"} {
			if v := obj.Get(key); v != nil {
				return parsed, text, v, true
			}
		}
	}
	answer, ok := tryJSON(text)
	return parsed, text, answer, ok
}

func nonNegInt(v any) (float64, bool) {
	n, ok := v.(float64)
	return n, ok && n >= 0 && n == math.Trunc(n) && !math.IsInf(n, 0)
}

// parseUsage is usage-parse.mjs for claude and grok; nil = estimated.
func parseUsage(cli string, parsed any) ledger.Object {
	obj, ok := parsed.(ledger.Object)
	if !ok {
		return nil
	}
	usage, ok := obj.Get("usage").(ledger.Object)
	if !ok {
		return nil
	}
	reported := func(in, out float64) ledger.Object {
		return ledger.Object{{Key: "tokens_in", Value: in}, {Key: "tokens_out", Value: out}, {Key: "tokens_source", Value: "reported"}}
	}
	if cli == "claude" {
		in, ok1 := nonNegInt(usage.Get("input_tokens"))
		out, ok2 := nonNegInt(usage.Get("output_tokens"))
		if !ok1 || !ok2 {
			return nil
		}
		for _, key := range []string{"cache_creation_input_tokens", "cache_read_input_tokens"} {
			if v := usage.Get(key); v != nil {
				n, ok := nonNegInt(v)
				if !ok {
					return nil
				}
				in += n
			}
		}
		return reported(in, out)
	}
	if in, ok := nonNegInt(usage.Get("input_tokens")); ok {
		if out, ok := nonNegInt(usage.Get("output_tokens")); ok {
			return reported(in, out)
		}
	}
	if in, ok := nonNegInt(usage.Get("prompt_tokens")); ok {
		if out, ok := nonNegInt(usage.Get("completion_tokens")); ok {
			return reported(in, out)
		}
	}
	return nil
}

var statusReasons = map[int]string{401: "login-expired", 402: "http-402", 403: "login-expired", 429: "rate-limited", 503: "at-capacity", 529: "at-capacity"}

var threeDigits = regexp.MustCompile(`^\d{3}$`)

func reasonForStatus(code any) string {
	switch v := code.(type) {
	case string:
		if threeDigits.MatchString(v) {
			n, _ := strconv.Atoi(v)
			return statusReasons[n]
		}
	case float64:
		if v == math.Trunc(v) && v >= 0 && v < 1000 {
			return statusReasons[int(v)]
		}
	}
	return ""
}

// JS \s and . (no line terminators) in the stderr patterns.
const jsWS = `[\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]`
const jsDot = `[^\n\r\x{2028}\x{2029}]`

var (
	capacityWord = regexp.MustCompile(`(?i)\b(?:at[ -]capacity|overloaded)\b`)
	codexStatus  = regexp.MustCompile(`^unexpected status (\d{3})\b`)
	errCoded     = regexp.MustCompile(`^(?:API )?Error:` + jsWS + `*(?:HTTP` + jsWS + `+)?(\d{3})\b`)
	errLogin     = regexp.MustCompile(`(?i)^(?:API )?Error:` + jsDot + `*\b(?:not logged in|login (?:expired|required))`)
	errRate      = regexp.MustCompile(`(?i)^(?:API )?Error:` + jsDot + `*\brate` + jsDot + `?limit`)
	errCapacity  = regexp.MustCompile(`(?i)^(?:API )?Error:` + jsDot + `*\b(?:at[ -]capacity|overloaded)\b`)
)

// classifyUnavailable is spawn.mjs classifyUnavailable ("" = available).
func classifyUnavailable(res execResult, parsed any) string {
	if strings.Contains(res.Error, "ENOENT") {
		return "cli-missing"
	}
	obj, _ := parsed.(ledger.Object)
	isError := obj != nil && obj.Get("is_error") == true
	if res.OK && !isError {
		return ""
	}
	var errObj ledger.Object
	if obj != nil {
		errObj, _ = obj.Get("error").(ledger.Object)
	}
	statuses := []any{nil, nil}
	if obj != nil {
		statuses[0] = obj.Get("api_error_status")
	}
	if errObj != nil {
		statuses[1] = errObj.Get("status")
	}
	for _, s := range statuses {
		if r := reasonForStatus(s); r != "" {
			return r
		}
	}
	message := ""
	if s, ok := obj.Get("message").(string); ok && obj != nil {
		message = s
	} else if s, ok := errObj.Get("message").(string); ok && errObj != nil {
		message = s
	}
	result, _ := obj.Get("result").(string)
	_, resultIsString := obj.Get("result").(string)
	if (errObj != nil && errObj.Get("type") == "overloaded_error") || capacityWord.MatchString(message) || (isError && resultIsString && capacityWord.MatchString(result)) {
		return "at-capacity"
	}
	if m := codexStatus.FindStringSubmatch(message); m != nil {
		if r := reasonForStatus(m[1]); r != "" {
			return r
		}
	}
	for _, raw := range strings.Split(res.Stderr, "\n") {
		line := trimJS(raw)
		if m := errCoded.FindStringSubmatch(line); m != nil {
			if r := reasonForStatus(m[1]); r != "" {
				return r
			}
		}
		if errLogin.MatchString(line) {
			return "login-expired"
		}
		if errRate.MatchString(line) {
			return "rate-limited"
		}
		if errCapacity.MatchString(line) {
			return "at-capacity"
		}
	}
	return ""
}

// Diagnostic tail (src/util/diagnostic-tail.mjs).
const (
	diagnosticTailBytes = 4096
	stderrMaxBytes      = 1024 * 1024
	packetLineMin       = 16
)

var (
	pemBegin = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*-----`)
	pemEnd   = regexp.MustCompile(`-----END [A-Z0-9 ]*-----`)
)

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

// StderrTail is stderrTail: (tail or nil, note or "").
func StderrTail(text string, packet *string) (any, string) {
	if packet == nil {
		return nil, "withheld: packet unreadable"
	}
	if text == "" {
		return "", ""
	}
	if len(text) > stderrMaxBytes {
		return nil, "stderr too large, withheld"
	}
	seen := map[string]bool{}
	var lines []string
	for _, l := range strings.Split(*packet, "\n") {
		t := trimJS(l)
		if utf16Len(t) >= packetLineMin && !seen[t] {
			seen[t] = true
			lines = append(lines, t)
		}
	}
	inPem := false
	var kept []string
	for _, line := range strings.Split(text, "\n") {
		t := trimJS(line)
		if inPem {
			if pemEnd.MatchString(t) {
				inPem = false
			}
			continue
		}
		if pemBegin.MatchString(t) {
			inPem = !pemEnd.MatchString(t)
			continue
		}
		if utf16Len(t) >= packetLineMin {
			hit := false
			for _, p := range lines {
				if strings.Contains(t, p) {
					hit = true
					break
				}
			}
			if hit {
				continue
			}
		}
		kept = append(kept, line)
	}
	safe := strings.Join(kept, "\n")
	buf := []byte(safe)
	if len(buf) <= diagnosticTailBytes {
		return safe, ""
	}
	from := len(buf) - diagnosticTailBytes
	for from < len(buf) && buf[from]&0xc0 == 0x80 {
		from++
	}
	piece := string(buf[from:])
	if from > 0 && buf[from-1] == '\n' {
		return piece, ""
	}
	nl := strings.IndexByte(piece, '\n')
	if nl < 0 {
		return "", "one long line, withheld"
	}
	return piece[nl+1:], ""
}

var eventType = regexp.MustCompile(`^[a-z0-9_.-]+$`)

// stdoutEvents is spawn.mjs stdoutEvents.
func stdoutEvents(text string) []ledger.Field {
	fields := []ledger.Field{{Key: "stdout_bytes", Value: float64(len(text))}}
	lines := strings.Split(text, "\n")
	var types []string
	for i, scanned := len(lines)-1, 0; i >= 0 && len(types) < 20 && scanned < 5000; i, scanned = i-1, scanned+1 {
		line := trimJS(lines[i])
		if !strings.HasPrefix(line, "{") {
			continue
		}
		v, ok := tryJSON(line)
		obj, isObj := v.(ledger.Object)
		if !ok || !isObj {
			continue
		}
		t, ok := obj.Get("type").(string)
		if !ok {
			continue
		}
		u := utf16.Encode([]rune(t))
		if len(u) > 40 {
			t = string(utf16.Decode(u[:40]))
		}
		if eventType.MatchString(t) {
			types = append([]string{t}, types...)
		}
	}
	if len(types) > 0 {
		list := make([]any, len(types))
		for i, t := range types {
			list[i] = t
		}
		fields = append(fields, ledger.Field{Key: "stdout_events", Value: list})
	}
	return fields
}

// SlugFor is config/load.mjs slugFor with the project root already found.
func SlugFor(cfg map[string]any, projectRoot string) string {
	if s, ok := section(cfg, "project")["slug"].(string); ok && s != "" {
		return s
	}
	base := strings.ToLower(filepath.Base(projectRoot))
	var b bytes.Buffer
	dash := false
	for _, r := range base {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "project"
	}
	return out
}
