package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/session"
)

// B550: `code-forge author` (src/cli/author.mjs + src/session/author.mjs) and
// `code-forge s2` (src/cli/s2.mjs + src/session/s2.mjs) run their closed-book
// L3 session in Go for the anthropic and xai providers. The plan step reads
// and checks everything first, without a side effect; a case Go does not
// serve byte-equal hands the whole call to Node from there.

const b550AuthorLens = "You are the plan author for code-forge: closed-book, no tools, no files. Everything you may use is in the message. " +
	"Never invent a flag, path, version or API that the facts sheet does not back. Reply once, with a JSON object that matches the schema."

const b550S2Lens = "You are System 2 for code-forge: a careful, closed-book reviewer of one decision. " +
	"You have no tools and no files; everything you may use is in the message. " +
	"Answer once, with a JSON object that matches the schema. Never invent facts that are not in the message."

// b550JobRules is JOB_RULES in src/session/author.mjs, authorHeadingRules()
// (src/session/plan-sections.mjs REQUIRED_SECTIONS) spelled out.
var b550JobRules = map[string][]string{
	"plan": {
		"Write the plan draft in markdown. Its §0 is the facts sheet below, VERBATIM (header lines included).",
		"Use these section headings exactly, word for word, at these positions (plan check finds the sections by them):",
		"- \"§0.x Acceptance clauses the facts sheet cannot back\" — every clause that cites a claim the sheet marks NOT-FOUND or UNVERIFIABLE, each with a tolerance naming the block; `none` when there is none.",
		"- \"§3 Caller map\" — mechanism → the block that ships it → the block that calls it; every block id appears.",
		"The level column of the block table is the lane recorded for each block by `forge jev ask lane --block <id> --plan <plan>`",
		"(or `--rules`, the deterministic fallback, when Jev is unavailable): L0 trivial · L1 a plain feature following a pattern ·",
		"L2 money, dates, tenancy, migrations, concurrency. It is never a block category. Leave it as `L?` when no lane is given in",
		"the message; plan check refuses a level that differs from the recorded lane.",
	},
	"harden": {
		"Harden the draft: close every gap you can from the material given; for each decision only the human can make,",
		"ask ONE question (options, why, blocking true when the plan cannot be dispatched without the answer).",
	},
}

const b550S2Header = "# System 2 decision\n\n" +
	"Decide the question below from the material given. Reply with ONE JSON object:\n" +
	"{\"decision\": string, \"confidence\": number 0..1, \"reason\": string of at most 280 characters,\n" +
	" \"overrule\": boolean (true when you disagree with System 1), \"ask_human\": boolean,\n" +
	" \"human_question\": string or null (the one question for the human when ask_human is true)}.\n"

const (
	b550S2MaxTokens = 4000
	b550S2ReasonMax = 280
)

// b550Plan is one checked session call. early is a refusal printed before
// any side effect; refusal one printed once the --run root exists (Node
// makes it while it builds runAuthor's/runS2's options).
type b550Plan struct {
	verb, early, refusal string
	run, tmpRoot         *string
	packet               string
	opts                 session.Opts
	finish               func(o *runOut, res ledger.Object) int
	config               handoverReason // a late case Go leaves to Node
	provider             handoverReason
}

// b550SessionFlags returns the parsed flags of a call that gets past the
// usage checks (metaSessionVerb serves the help and usage forms).
func b550SessionFlags(args []string, spec metaFlagSpec, required []string) (map[string]any, bool) {
	if len(args) > 0 && metaIsHelp(args[0]) {
		return nil, false
	}
	flags, positionals, message := metaParseFlags(args, spec)
	if message != "" || len(positionals) > 0 {
		return nil, false
	}
	for _, name := range required {
		if _, ok := flags[name].(string); !ok {
			return nil, false
		}
	}
	return flags, true
}

// b550TrimEnd is String.prototype.trimEnd.
func b550TrimEnd(s string) string {
	return strings.TrimRightFunc(s, b550JSSpace)
}

// b550JSSpace is the WhiteSpace and LineTerminator set of String.prototype.trim.
func b550JSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// b550Config is loadProjectConfig with the slug: a refusal is early.
// ok=false hands over (a config Go cannot read like Node).
func b550Config(p *b550Plan, cwd string) (map[string]any, string, bool) {
	cfg, err := config.LoadUnchecked(cwd)
	if err != nil {
		var cfgErr *config.Error
		if !errors.As(err, &cfgErr) || runYAMLSyntax(cfgErr) {
			return nil, "", false // a YAML syntax error is worded by Node's parser
		}
		p.early = p.verb + ": " + cfgErr.Message + "\n"
		return nil, "", true
	}
	root := config.ProjectRootFor(cwd)
	p.opts.Cfg = cfg
	p.opts.Slug = session.SlugFor(cfg, root)
	return cfg, root, true
}

// b550Timeout is intFlag(flags.timeout, 'timeout').
func b550Timeout(p *b550Plan, flags map[string]any) {
	if raw, ok := flags["timeout"].(string); ok {
		n, bad := cliIntFlag(raw, "timeout")
		if bad != "" {
			p.early = p.verb + ": " + bad + "\n"
			return
		}
		p.opts.TimeoutMs = n * 1000
	}
}

// b550Run is runRootFor(run, cfg?.tmp?.root) before it makes the root: a
// bad id is early; ok=false hands over (runRootFor throws a TypeError).
func b550Run(p *b550Plan, flags map[string]any, cfg map[string]any) bool {
	run, ok := flags["run"].(string)
	if !ok {
		return true
	}
	if !spawnRunID.MatchString(run) {
		p.early = p.verb + ": a run id is one path segment of letters, digits, \".\", \"_\" or \"-\"\n"
		return true
	}
	if tmp, ok := cfg["tmp"].(map[string]any); ok && tmp["root"] != nil {
		root, isString := tmp["root"].(string)
		if !isString || !filepath.IsAbs(root) {
			return false
		}
		p.tmpRoot = &root
	}
	p.run = &run
	p.opts.Run = &run
	return true
}

// b550Read reads a text file: ok=false when it cannot be read; bad=true when
// its bytes are not UTF-8 (Node would read U+FFFD).
func b550Read(file string) (text string, ok, bad bool) {
	data, err := os.ReadFile(file)
	if err != nil {
		return "", false, false
	}
	if !utf8.Valid(data) {
		return "", true, true
	}
	return string(data), true, false
}

func runAuthorVerb(args []string) int {
	spec := metaFlagSpec{values: []string{"job", "brief", "facts", "draft", "answers", "out", "run", "timeout"}}
	if flags, ok := b550SessionFlags(args, spec, []string{"job", "brief"}); ok {
		return b550RunSession("author", args, func(cwd string) (*b550Plan, handoverReason) { return b550AuthorPlan(cwd, flags) }, hAuthorHome)
	}
	return b550Usage("author", authorUsage, spec, args)
}

func runS2Verb(args []string) int {
	spec := metaFlagSpec{values: []string{"packet", "run", "block", "timeout"}}
	if flags, ok := b550SessionFlags(args, spec, []string{"packet"}); ok {
		return b550RunSession("s2", args, func(cwd string) (*b550Plan, handoverReason) { return b550S2Plan(cwd, flags) }, hS2Home)
	}
	return b550Usage("s2", s2Usage, spec, args)
}

// b550Usage serves the help and usage forms (every other form runs above).
func b550Usage(verb, usage string, spec metaFlagSpec, args []string) int {
	if metaHelp(args, usage, nil) {
		return 0
	}
	_, _, message := metaParseFlags(args, spec)
	return metaServe(verb, "", args, func(_, stderr io.Writer) int {
		if message != "" {
			fmt.Fprintf(stderr, "%s: %s\n", verb, message)
		} else {
			fmt.Fprint(stderr, usage)
		}
		return 2
	})
}

func b550AuthorPlan(cwd string, flags map[string]any) (*b550Plan, handoverReason) {
	p := &b550Plan{verb: "author", config: hAuthorConfig, provider: hAuthorProvider}
	abs := func(name string) (string, bool) {
		v, ok := flags[name].(string)
		if !ok {
			return "", false
		}
		return spawnResolve(cwd, v), true
	}
	brief, _ := abs("brief")
	cfg, root, ok := b550Config(p, cwd)
	if !ok {
		return nil, hAuthorConfig
	}
	if p.early == "" {
		b550Timeout(p, flags)
	}
	if p.early == "" && !b550Run(p, flags, cfg) {
		return nil, hAuthorConfig
	}
	if p.early != "" {
		return p, 0
	}
	refuse := func(message string) (*b550Plan, handoverReason) {
		p.refusal = "author: " + message + "\n"
		return p, 0
	}
	job, _ := flags["job"].(string)
	rules, known := b550JobRules[job]
	if !known {
		return refuse("--job must be one of plan, harden")
	}
	factsPath, hasFacts := abs("facts")
	if !hasFacts && job == "plan" {
		return refuse("facts sheet required: run forge facts first")
	}
	if hasFacts {
		text, ok, bad := b550Read(factsPath)
		if !ok {
			return refuse("--facts cannot be read")
		}
		if bad {
			return nil, hAuthorInput
		}
		sheet, ok := parseSheet(text)
		if !ok {
			return refuse("facts sheet is not a facts sheet: run forge facts first")
		}
		data, err := os.ReadFile(brief)
		if err != nil {
			return nil, hAuthorInput // checkSheetFresh throws reading the brief
		}
		if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != sheet.briefSha {
			return refuse("facts sheet is stale: brief changed after it was built")
		}
	}
	texts := map[string]*string{}
	for _, name := range []string{"brief", "facts", "draft", "answers"} {
		file, given := abs(name)
		if !given {
			continue
		}
		text, ok, bad := b550Read(file)
		if !ok {
			return refuse("--" + name + " cannot be read")
		}
		if bad {
			return nil, hAuthorInput
		}
		texts[name] = &text
	}
	out := []string{"# Author job: " + job, ""}
	out = append(out, rules...)
	out = append(out, "", `Reply with {"draft": markdown, "questions": [{"id", "question", "options", "why", "blocking"}]} — questions empty when none remain.`, "")
	out = append(out, "## Brief", "", b550TrimEnd(*texts["brief"]), "")
	for _, part := range []struct{ name, heading string }{{"facts", "## Facts sheet"}, {"draft", "## Prior draft"}, {"answers", "## The human's answers"}} {
		if text := texts[part.name]; text != nil {
			out = append(out, part.heading, "", b550TrimEnd(*text), "")
		}
	}
	p.packet = strings.Join(out, "\n")
	draftOut, given := abs("out")
	if !given {
		plansDir := "plans"
		if project, ok := cfg["project"].(map[string]any); ok && project["plans_dir"] != nil {
			text, isString := project["plans_dir"].(string)
			if !isString {
				return nil, hAuthorConfig // path.resolve throws after the session
			}
			plansDir = text
		}
		name := jsBasename(brief)
		if strings.HasPrefix(name, ".") || name == "" {
			return nil, hAuthorInput // path.extname's dot-file rules
		}
		if dot := strings.LastIndexByte(name, '.'); dot > 0 {
			name = name[:dot]
		}
		draftOut = filepath.Join(spawnResolve(root, plansDir), name+"."+job+".md")
	}
	if !b550Writable(draftOut) {
		return nil, hAuthorInput // mkdirSync or writeFileSync throws after the session
	}
	p.opts.Level, p.opts.Role, p.opts.Schema = "L3", "author", session.AuthorSchema()
	p.opts.SystemPrompt, p.opts.Validate = ptrString(b550AuthorLens), session.MatchesAuthorSchema
	p.finish = func(o *runOut, res ledger.Object) int {
		answer, _ := res.Get("answer").(ledger.Object)
		draft, _ := answer.Get("draft").(string)
		if err := os.MkdirAll(filepath.Dir(draftOut), 0o777); err != nil {
			return b550Crash(o, "author", err)
		}
		if err := os.WriteFile(draftOut, []byte(draft), 0o666); err != nil {
			return b550Crash(o, "author", err)
		}
		row, _ := res.Get("row").(ledger.Object)
		orNull := func(key string) any {
			if v := row.Get(key); v != nil {
				return v
			}
			return nil
		}
		cost := ledger.Object{{Key: "tokens_in", Value: orNull("tokens_in")}, {Key: "tokens_out", Value: orNull("tokens_out")},
			{Key: "tokens_source", Value: orNull("tokens_source")}, {Key: "usd_estimated", Value: b550CostUSD(res.Get("provider"), row)}}
		line, err := ledger.Stringify(ledger.Object{{Key: "draft", Value: draftOut}, {Key: "questions", Value: answer.Get("questions")}, {Key: "cost", Value: cost}}, false)
		if err != nil {
			return b550Crash(o, "author", err)
		}
		o.out(string(line) + "\n")
		return 0
	}
	return b550Checked(p)
}

// b550CostUSD is author.mjs costOf's estimateCostUsd at L3, null where it throws.
func b550CostUSD(provider any, row ledger.Object) any {
	perK := map[string]float64{"anthropic": 0.03, "openai": 0.025, "xai": 0.02}
	name, _ := provider.(string)
	price, ok := perK[name]
	if !ok {
		return nil
	}
	count := func(key string) (float64, bool) {
		v := row.Get(key)
		if v == nil {
			return 0, true
		}
		n, ok := v.(float64)
		return n, ok && n >= 0 && !math.IsInf(n, 0) && !math.IsNaN(n)
	}
	in, ok1 := count("tokens_in")
	out, ok2 := count("tokens_out")
	if !ok1 || !ok2 {
		return nil
	}
	x := float64(float64(float64((in+out)/1000)*price) * 10000)
	return math.Floor(x+0.5) / 10000
}

// b550Writable: mkdirSync(dirname, {recursive}) and writeFileSync(file)
// would not throw for a reason Go can see now (a directory at the file, or a
// file where a parent directory goes).
func b550Writable(file string) bool {
	if info, err := os.Stat(file); err == nil && info.IsDir() {
		return false
	}
	for dir := filepath.Dir(file); ; dir = filepath.Dir(dir) {
		info, err := os.Stat(dir)
		if err == nil {
			return info.IsDir()
		}
		if !os.IsNotExist(err) {
			return false
		}
		if filepath.Dir(dir) == dir {
			return false
		}
	}
}

func b550S2Plan(cwd string, flags map[string]any) (*b550Plan, handoverReason) {
	p := &b550Plan{verb: "s2", config: hS2Config, provider: hS2Provider}
	cfg, _, ok := b550Config(p, cwd)
	if !ok {
		return nil, hS2Config
	}
	if p.early == "" {
		b550Timeout(p, flags)
	}
	if p.early != "" {
		return p, 0
	}
	// readJSONFlag sits between --timeout and --run in s2.mjs
	packetFile, _ := flags["packet"].(string)
	data, err := os.ReadFile(spawnResolve(cwd, packetFile))
	if err != nil {
		p.early = "s2: --packet cannot be read\n"
		return p, 0
	}
	if !utf8.Valid(data) || b550LoneSurrogate.Match(data) {
		return nil, hS2Input // Node's string differs from Go's
	}
	packet, err := ledger.DecodeValue(data)
	if err != nil {
		if json.Valid(data) {
			return nil, hS2Input // a number JSON.parse reads and Go refuses
		}
		p.early = "s2: --packet is not a valid JSON file\n"
		return p, 0
	}
	if !b550Run(p, flags, cfg) {
		return nil, hS2Config
	}
	if p.early != "" {
		return p, 0
	}
	return b550S2Build(p, flags, packet)
}

var b550LoneSurrogate = regexp.MustCompile(`\\u[dD][89abAB][0-9a-fA-F]{2}|\\u[dD][c-fC-F][0-9a-fA-F]{2}`)

func b550S2Build(p *b550Plan, flags map[string]any, packet any) (*b550Plan, handoverReason) {
	if block, ok := flags["block"].(string); ok {
		p.opts.Block = &block
	}
	obj, _ := packet.(ledger.Object)
	question, _ := obj.Get("question").(string)
	if obj == nil || jsTrim(question) == "" {
		p.refusal = "s2: s2: the packet needs a non-empty question\n"
		return p, 0
	}
	fixed := b550S2Header + "## Question\n" + jsTrim(question) + "\n"
	if options, ok := obj.Get("options").([]any); ok && len(options) > 0 {
		lines := make([]string, len(options))
		for i, option := range options {
			text, ok := jsString(option)
			if !ok {
				return nil, hS2Input // String() of an object or a list
			}
			lines[i] = "- " + text
		}
		fixed += "\n## Options\n" + strings.Join(lines, "\n") + "\n"
	}
	if s1 := obj.Get("s1"); jsTruthy(s1, true) {
		data, err := ledger.Stringify(s1, false)
		if err != nil {
			return nil, hS2Input
		}
		fixed += "\n## System 1 answer\n" + string(data) + "\n"
	}
	maxBytes := b550S2MaxTokens * 4
	if len(fixed) > maxBytes {
		p.refusal = fmt.Sprintf("s2: s2: the question part is %d tokens (max %d)\n", (len(fixed)+3)/4, b550S2MaxTokens)
		return p, 0
	}
	context, _ := obj.Get("context").(string)
	prompt := fixed
	if context != "" {
		heading := "\n## Context\n"
		whole := heading + context + "\n"
		if len(fixed)+len(whole) <= maxBytes {
			prompt = fixed + whole
		} else {
			note := "\n[context cut: " + strconv.Itoa(len(context)) + " bytes given]\n"
			room := maxBytes - len(fixed) - len(heading) - len(note)
			if room > 0 {
				cut := room
				for cut > 0 && context[cut]&0xc0 == 0x80 {
					cut--
				}
				prompt = fixed + heading + context[:cut] + note
			}
		}
	}
	p.packet = prompt
	p.opts.Level, p.opts.Role, p.opts.Schema = "L3", "s2", session.S2Schema()
	p.opts.SystemPrompt, p.opts.Validate = ptrString(b550S2Lens), session.MatchesS2Schema
	p.finish = func(o *runOut, res ledger.Object) int {
		answer := res.Get("answer")
		obj, _ := answer.(ledger.Object)
		reason, _ := obj.Get("reason").(string)
		switch {
		case !session.MatchesS2Schema(answer):
			o.err("s2: invalid-output (answer does not match the S2 schema)\n")
			return 1
		case len(utf16.Encode([]rune(reason))) > b550S2ReasonMax:
			o.err("s2: invalid-output (reason is longer than " + strconv.Itoa(b550S2ReasonMax) + " characters)\n")
			return 1
		}
		line, err := ledger.Stringify(answer, false)
		if err != nil {
			return b550Crash(o, "s2", err)
		}
		o.out(string(line) + "\n")
		return 0
	}
	return b550Checked(p)
}

// b550Checked hands the call over when the session would not run in Go (a
// level or effort shape Node throws a TypeError on). A session refusal (B618:
// an openai level without review.allow_open_book_codex, a provider with no
// CLI mapping) is Spawn's, printed as "<verb>: <message>" exit 2 after the
// run root and the packet, like runAuthor and runS2.
func b550Checked(p *b550Plan) (*b550Plan, handoverReason) {
	probe := p.opts
	probe.PromptPath = string(filepath.Separator) + "packet.md"
	if err := session.Check(probe); session.IsUnsupported(err) {
		return nil, p.provider
	}
	return p, 0
}

func b550Crash(o *runOut, verb string, err error) int {
	o.err(fmt.Sprintf("code-forge: verb %q failed: Error: %s\n", verb, err.Error()))
	return 1
}

// b550RunSession runs a planned author or s2 call with the router's
// breadcrumb and error log.
func b550RunSession(verb string, args []string, plan func(cwd string) (*b550Plan, handoverReason), home handoverReason) int {
	if h := os.Getenv("HOME"); h == "" || !filepath.IsAbs(h) {
		return metaHandover(verb, args, home)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return metaHandover(verb, args, home)
	}
	p, reason := plan(cwd)
	if p == nil {
		return metaHandover(verb, args, reason)
	}
	before := gatesBreadcrumbName(verb)
	spawnWarnBefore, spawnWarnVerb = before, verb
	o := &runOut{}
	code, late := b550Exec(o, p)
	if late != 0 {
		return metaHandover(verb, args, late)
	}
	if code != 0 {
		var message any
		if o.last != "" {
			message = strings.TrimSpace(o.last)
		}
		nativeVerbFailure(verb, "", args, code, message, before)
	}
	return code
}

// b550Exec makes the --run root, writes the packet to
// <root>/<verb>/<ms>-<8 hex>/packet.md, runs the session and removes the
// packet directory, as runAuthor and runS2 do.
func b550Exec(o *runOut, p *b550Plan) (int, handoverReason) {
	if p.early != "" {
		o.err(p.early)
		return 2, 0
	}
	root := ""
	if p.run != nil {
		made, err := contextRunRoot(*p.run, p.tmpRoot)
		if err != nil {
			return 0, p.config
		}
		root = made
	}
	if p.refusal != "" {
		o.err(p.refusal)
		return 2, 0
	}
	var procRoot string
	lazyRoot := func() (string, error) {
		if procRoot != "" {
			return procRoot, nil
		}
		contextApplyTmpRoot()
		made, err := contextRunRoot("proc-"+strconv.Itoa(os.Getpid())+"-"+strconv.FormatInt(time.Now().UnixMilli(), 10), nil)
		procRoot = made
		return made, err
	}
	defer func() {
		if procRoot != "" {
			spawnReleaseRoot(procRoot)
		}
	}()
	if root == "" {
		made, err := lazyRoot()
		if err != nil {
			return 0, p.config
		}
		root = made
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return b550Crash(o, p.verb, err), 0
	}
	dir := filepath.Join(root, p.verb, strconv.FormatInt(time.Now().UnixMilli(), 10)+"-"+hex.EncodeToString(suffix))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return b550Crash(o, p.verb, err), 0
	}
	defer os.RemoveAll(dir)
	packet := filepath.Join(dir, "packet.md")
	if err := os.WriteFile(packet, []byte(p.packet), 0o600); err != nil {
		return b550Crash(o, p.verb, err), 0
	}
	opts := p.opts
	opts.PromptPath, opts.RunRoot = packet, root
	runID, hasRunID := os.LookupEnv("CODE_FORGE_RUN_ID")
	deps := session.Deps{Stderr: spawnStderr{o}, RunID: runID, Warn: spawnLogWarning, RunRoot: lazyRoot}
	if p.run != nil {
		if !hasRunID {
			deps.RunID = *p.run
		}
		deps.PidsRoot = lazyRoot
	}
	res, err := session.Spawn(opts, deps)
	if session.IsUnsupported(err) {
		return 0, p.provider
	}
	var sessionErr *session.Error
	if errors.As(err, &sessionErr) {
		o.err(p.verb + ": " + sessionErr.Message + "\n")
		return 2, 0
	}
	if err != nil {
		return b550Crash(o, p.verb, err), 0
	}
	if status, _ := res.Get("status").(string); status != "ok" {
		line := p.verb + ": " + status
		if reason, _ := res.Get("reason").(string); reason != "" {
			line += " (" + reason + ")"
		}
		o.err(line + "\n")
		if status == "unavailable" {
			return 3, 0
		}
		return 1, 0
	}
	return p.finish(o, res), 0
}
