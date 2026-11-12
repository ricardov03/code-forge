package main

// B528: the interactive `init` wizard (src/install/wizard/steps.mjs and
// run.mjs with @clack/prompts) in Go, ending in the setup B527 ported.
// Every shape Go cannot draw like Node hands over before the first prompt.

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/ledger"
)

var initWizHints = map[string]string{
	"tools":                   "Recommended adds Solo and the Codex, Grok, Gemini and 1Password CLIs, each only after its own yes.",
	"harnesses":               "The skill is linked into each harness you pick; detected ones are marked.",
	"scope":                   "project puts the link inside this repository; global puts it under your home folder.",
	"method":                  "A symlink follows package updates; a copy stays as installed.",
	"provider":                "The model family every level uses unless a level names its own provider.",
	"levels":                  "L0 is the cheapest model and L3 the strongest; Enter keeps all four.",
	"level":                   "Enter keeps this level as shown.",
	"refresh_models":          "Reads only local cache files; nothing is sent anywhere.",
	"multimodel":              "Two providers review each block and a third one judges; it costs more per block.",
	"second_provider":         "This provider reviews each block next to the L2 reviewer.",
	"judge":                   "The judge settles disagreements between the two reviewers.",
	"engine":                  "auto uses Solo when present, otherwise the subagents of the current harness.",
	"solo_project":            "The Solo project that coder processes run in; leave blank for none.",
	"coder_may_push":          "No: the coder hands back its branch and you (or the orchestrator) push it.",
	"coder_may_open_draft_pr": "Only after an independent review passes; marking it ready and merging stay with you.",
	"settings":                "Pick how to treat the gate and proof values shown above.",
	"apply":                   "Only the listed keys change; your other edits stay.",
}

var initWizSettingHints = map[string]string{
	"test":           "the command that runs your tests; the block cannot close if it fails (blank = no test gate)",
	"lint":           "the command that runs your linter; the block cannot close if it fails (blank = no lint gate)",
	"types":          "the command that runs your type checker; the block cannot close if it fails (blank = no types gate)",
	"format":         "the command that runs your format check; the block cannot close if it fails (blank = no format gate)",
	"high":           "globs where mistakes are costly (auth, billing); these files get deeper reviews and need red→green proof to close (blank = none)",
	"isolation":      "export = run proof tests in a temporary copy (safe with parallel coders); lock = run in your folder and pause other coders",
	"link_dirs":      "big folders the copy links to instead of copying, e.g. node_modules, vendor",
	"copy_untracked": "files git does not track but your tests need, e.g. .env, .env.testing",
}

const (
	initWizCancelText = "cancelled — nothing written"
	initWizEOFLine    = "  const code = await run(process.argv.slice(2), { configureTmpRoot: true });"
)

func initWizWithHint(message, hint string) string { return message + "\n" + hint }

// initWizEnv is the terminal the wizard draws on.
type initWizEnv struct {
	in         io.Reader
	out        io.Writer
	cols, rows int
	// raw puts the input in raw mode for one prompt and returns the undo.
	raw func() func()
}

// initWizOpen is the real terminal: stdin and stdout both terminals.
var initWizOpen = func() (initWizEnv, bool) {
	if !stdinIsTerminal() {
		return initWizEnv{}, false
	}
	cols, rows, ok := initWizSize(1)
	if !ok {
		return initWizEnv{}, false
	}
	return initWizEnv{in: os.Stdin, out: os.Stdout, cols: cols, rows: rows, raw: func() func() {
		if restore, ok := initWizRaw(0); ok {
			return restore
		}
		return func() {}
	}}, true
}

// initWizTerminalOK: the colours, symbols and size Go draws are the ones
// Node draws (picocolors, util.styleText and clack read these).
func initWizTerminalOK(env initWizEnv) bool {
	for _, name := range []string{"NO_COLOR", "FORCE_COLOR", "NODE_DISABLE_COLORS", "CI", "TEAMCITY_VERSION", "ACCESSIBLE", "NO_UNICODE"} {
		if _, set := os.LookupEnv(name); set {
			return false
		}
	}
	switch os.Getenv("TERM") {
	case "xterm", "xterm-256color", "screen", "screen-256color", "tmux", "tmux-256color", "rxvt-unicode", "rxvt-unicode-256color":
	default:
		return false
	}
	return env.cols >= 60 && env.rows >= 24
}

// initWizEOFFile is the launcher line Node names when the input ends at a
// prompt (the unsettled top-level await), or "" when Go cannot name it.
func initWizEOFFile() string {
	root, ok := upgradePackageRoot()
	if !ok {
		return ""
	}
	file, err := filepath.EvalSymlinks(filepath.Join(root, "bin", "code-forge.mjs"))
	if err != nil || !regexp.MustCompile(`^/[A-Za-z0-9/._~-]+$`).MatchString(file) {
		return ""
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	found := 0
	for i, l := range lines {
		if strings.Contains(l, "await ") && !strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "\t") {
			return "" // another top-level await
		}
		if strings.TrimRight(l, "\r") == initWizEOFLine {
			if found != 0 {
				return ""
			}
			found = i + 1
		}
	}
	if found == 0 {
		return ""
	}
	return "Warning: Detected unsettled top-level await at file://" + file + ":" + strconv.Itoa(found) + "\n" +
		initWizEOFLine + "\n" + strings.Repeat(" ", strings.Index(initWizEOFLine, "await")) + "^\n\n\n\n"
}

// initWizLevelText is levelText in steps.mjs; ok is false for a level shape
// Go does not print like Node.
func initWizLevelText(level any) (string, bool) {
	obj, ok := level.(ledger.Object)
	if !ok {
		return "", false
	}
	model, ok := obj.Get("model").(string)
	if !ok || model == "" {
		return "", false
	}
	text := model
	for _, f := range obj {
		switch f.Key {
		case "effort", "provider":
			if f.Value == nil {
				continue
			}
			s, ok := f.Value.(string)
			if !ok {
				return "", false
			}
			if f.Key == "provider" && s != "" && !initHas(initProviders, s) {
				return "", false
			}
		}
	}
	if e, _ := obj.Get("effort").(string); e != "" {
		text += ":" + e
	}
	if p, _ := obj.Get("provider").(string); p != "" {
		text += "@" + p
	}
	return text, initWizPlainText(text)
}

// initWizTuple is the (provider, model, effort) the validator compares.
func initWizTuple(provider string, level ledger.Object) string {
	p := provider
	if own, ok := level.Get("provider").(string); ok && own != "" {
		p = own
	}
	effort, _ := level.Get("effort").(string)
	data, _ := json.Marshal([]string{p, level.Get("model").(string), effort})
	return string(data)
}

// initWizEffective is effectiveProvider in answers.mjs.
func initWizEffective(v initValues, level string) string {
	if obj, ok := v.levels.Get(level).(ledger.Object); ok {
		if own, ok := obj.Get("provider").(string); ok && own != "" {
			return own
		}
	}
	return v.provider
}

// initWizUI asks the prompts; end says how the last one ended.
type initWizUI struct {
	env  initWizEnv
	term *initWizTerm
	end  initWizOutcome
}

func (u *initWizUI) ask(p *initWizPrompt) bool {
	undo := u.env.raw()
	out := u.term.run(p)
	undo()
	u.end = out
	return out == initWizSubmitted
}

func (u *initWizUI) sel(message string, options []initWizOption, initial string) (string, bool) {
	p := &initWizPrompt{kind: "select", message: message, options: options}
	for i, o := range options {
		if o.value == initial {
			p.cursor = i
			break
		}
	}
	if !u.ask(p) {
		return "", false
	}
	return options[p.cursor].value, true
}

func (u *initWizUI) confirm(message string, initial bool) (bool, bool) {
	p := &initWizPrompt{kind: "confirm", message: message, value: initial}
	if !u.ask(p) {
		return false, false
	}
	return p.value, true
}

func (u *initWizUI) text(message, initial string) (string, bool) {
	p := &initWizPrompt{kind: "text", message: message, input: initial}
	if !u.ask(p) {
		return "", false
	}
	return p.input, true
}

func initWizValues(values ...string) []initWizOption {
	out := []initWizOption{}
	for _, v := range values {
		out = append(out, initWizOption{value: v})
	}
	return out
}

// askLevel is askLevel in steps.mjs.
func (u *initWizUI) askLevel(v initValues, level, message, hint string, shown ledger.Object) (ledger.Object, bool) {
	shownText := ""
	if shown != nil {
		shownText, _ = initWizLevelText(shown)
	}
	initial, problem := shownText, ""
	for {
		h := hint
		if problem != "" {
			h = hint + "\n" + problem
		}
		typed, ok := u.text(initWizWithHint(message, h), initial)
		if !ok {
			return nil, false
		}
		initial = strings.TrimFunc(typed, isJSSpace)
		var candidate ledger.Object
		if shown != nil && initial == shownText {
			candidate = shown
		} else {
			value, usage, _ := initConvert("--level", level+"="+initial)
			if usage != "" {
				problem = usage
				continue
			}
			candidate = value.(initLevelValue).spec
		}
		dup := ""
		mine := initWizTuple(v.provider, candidate)
		for _, earlier := range []string{"L0", "L1", "L2", "L3"} {
			if earlier == level {
				break
			}
			if obj, ok := v.levels.Get(earlier).(ledger.Object); ok && initWizTuple(v.provider, obj) == mine {
				dup = earlier
				break
			}
		}
		if dup != "" {
			problem = level + " is the same as " + dup + ". Each level must be a different model or effort."
			continue
		}
		return candidate, true
	}
}

// initWizJudge is judgeCollision in answers.mjs: the reviewers and the
// provider left for the judge, or ok false.
func initWizJudge(v initValues) (reviewers []string, third string, ok bool) {
	if !v.multimodel || v.secondProvider == "" {
		return nil, "", false
	}
	reviewers = []string{initWizEffective(v, "L2"), v.secondProvider}
	if !initHas(reviewers, initWizEffective(v, "L3")) {
		return nil, "", false
	}
	for _, p := range initProviders {
		if !initHas(reviewers, p) {
			return reviewers, p, true
		}
	}
	return reviewers, "", true
}

// askAnswers is askAnswers in steps.mjs.
func (u *initWizUI) askAnswers(v *initValues, ctx initCtx, parsed initParsed) bool {
	open := func(key string) bool { _, flagged := initLastFor(parsed, key); return !flagged }
	var ok bool
	if open("tools") {
		cur, _ := v.tools.(string)
		if v.tools, ok = u.sel(initWizWithHint("Install the recommended tools, or use only the current harness?", initWizHints["tools"]),
			[]initWizOption{{value: "current", label: "current harness"}, {value: "recommended", label: "recommended tools"}}, cur); !ok {
			return false
		}
	}
	if open("harnesses") {
		options := []initWizOption{}
		for _, h := range installHarnesses {
			label := h.label
			if initHas(ctx.detectedHarnesses, h.id) {
				label += " (detected)"
			}
			options = append(options, initWizOption{value: h.id, label: label})
		}
		chosen := []string{}
		for _, id := range v.harnesses {
			if initHas(initHarnessIDs, id) {
				chosen = append(chosen, id)
			}
		}
		p := &initWizPrompt{kind: "multiselect", message: initWizWithHint("Install the skill into:", initWizHints["harnesses"]), options: options, chosen: chosen}
		if !u.ask(p) {
			return false
		}
		v.harnesses = append([]string{}, p.chosen...)
	}
	if open("scope") {
		if v.scope, ok = u.sel(initWizWithHint("Scope", initWizHints["scope"]), initWizValues("project", "global"), v.scope); !ok {
			return false
		}
	}
	if open("method") {
		if v.method, ok = u.sel(initWizWithHint("Method", initWizHints["method"]), initWizValues("symlink", "copy"), v.method); !ok {
			return false
		}
	}
	if open("provider") {
		if v.provider, ok = u.sel(initWizWithHint("Default provider", initWizHints["provider"]), initWizValues(initProviders...), v.provider); !ok {
			return false
		}
	}
	if open("levels") {
		parts := []string{}
		for _, l := range []string{"L0", "L1", "L2", "L3"} {
			text, _ := initWizLevelText(v.levels.Get(l))
			parts = append(parts, l+"="+text)
		}
		keep, ok := u.confirm(initWizWithHint("Keep the level matrix? "+strings.Join(parts, " "), initWizHints["levels"]), true)
		if !ok {
			return false
		}
		if !keep {
			for _, l := range []string{"L0", "L1", "L2", "L3"} {
				shown, _ := v.levels.Get(l).(ledger.Object)
				next, ok := u.askLevel(*v, l, l+" model[:effort][@provider]", initWizHints["level"], shown)
				if !ok {
					return false
				}
				v.levels.Set(l, next)
			}
		}
	}
	if open("refresh_models") {
		if v.refreshModels, ok = u.confirm(initWizWithHint("Read model ids from the Codex/Grok CLI caches on this machine?", initWizHints["refresh_models"]), false); !ok {
			return false
		}
	}
	if open("multimodel") {
		if v.multimodel, ok = u.confirm(initWizWithHint("Multimodel review (consensus)?", initWizHints["multimodel"]), v.multimodel); !ok {
			return false
		}
	}
	if v.multimodel && open("second_provider") {
		reviewer1 := initWizEffective(*v, "L2")
		others := []string{}
		for _, p := range initProviders {
			if p != reviewer1 {
				others = append(others, p)
			}
		}
		initial := others[0]
		if initHas(others, v.secondProvider) {
			initial = v.secondProvider
		}
		if v.secondProvider, ok = u.sel(initWizWithHint("Second provider (must differ from the effective L2 provider, "+reviewer1+")", initWizHints["second_provider"]),
			initWizValues(others...), initial); !ok {
			return false
		}
	}
	if v.secondProvider != initWizEffective(*v, "L2") {
		for {
			reviewers, third, collision := initWizJudge(*v)
			if !collision {
				break
			}
			var proposal ledger.Object
			if third != "" {
				proposal = initDefaultLevels(third).Get("L3").(ledger.Object).Clone()
				proposal.Set("provider", third)
			}
			next, ok := u.askLevel(*v, "L3", "L3 judge — must come from a third provider (reviewers: "+strings.Join(reviewers, ", ")+"); model[:effort][@provider]",
				initWizHints["judge"], proposal)
			if !ok {
				return false
			}
			v.levels.Set("L3", next)
		}
	}
	if open("engine") && v.engine != "subprocess" {
		cur, _ := v.engine.(string)
		if v.engine, ok = u.sel(initWizWithHint("Engine", initWizHints["engine"]), initWizValues("auto", "solo", "harness"), cur); !ok {
			return false
		}
	}
	if open("solo_project") && ctx.solo && v.engine != "harness" {
		initial, _ := initWizSoloText(v.soloProject)
		typed, ok := u.text(initWizWithHint("Solo project id (blank: none)", initWizHints["solo_project"]), initial)
		if !ok {
			return false
		}
		t := strings.TrimFunc(typed, isJSSpace)
		v.soloProject = nil
		if regexp.MustCompile(`^[1-9][0-9]{0,9}$`).MatchString(t) {
			n, _ := strconv.ParseFloat(t, 64)
			v.soloProject = n
		}
	}
	if open("coder_may_push") {
		if v.push, ok = u.confirm(initWizWithHint("May a coder push its block branch on its own?", initWizHints["coder_may_push"]), v.push); !ok {
			return false
		}
	}
	if v.push && open("coder_may_open_draft_pr") {
		if v.draft, ok = u.confirm(initWizWithHint("May a coder open a draft PR on its own?", initWizHints["coder_may_open_draft_pr"]), v.draft); !ok {
			return false
		}
	} else if open("coder_may_open_draft_pr") {
		v.draft = false
	}
	return true
}

// initWizSoloText is String(values.solo_project) for the shapes Go prints
// like Node: none, or a whole number.
func initWizSoloText(project any) (string, bool) {
	switch p := project.(type) {
	case nil:
		return "", true
	case float64:
		if p == float64(int64(p)) && p >= 0 && p < 1e15 {
			return strconv.FormatInt(int64(p), 10), true
		}
	case string:
		return p, initWizPlainText(p)
	}
	return "", false
}

// initWizCSV is csv in steps.mjs (commas inside braces stay).
func initWizCSV(text string) []string {
	items, current, depth := []string{}, "", 0
	for _, ch := range text {
		if ch == '{' {
			depth++
		} else if ch == '}' && depth > 0 {
			depth--
		}
		if ch == ',' && depth == 0 {
			items = append(items, current)
			current = ""
		} else {
			current += string(ch)
		}
	}
	items = append(items, current)
	out := []string{}
	for _, s := range items {
		if s = strings.TrimFunc(s, isJSSpace); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func initWizGateArgv(gate any) []string {
	if obj, ok := gate.(ledger.Object); ok {
		gate = obj.Get("argv")
	}
	words, _ := initStrings(gate)
	if gate == nil {
		return nil
	}
	return words
}

// askProjectSettings is askProjectSettings in steps.mjs; usage is a
// shellWords error (Node's UsageError, exit 2).
func (u *initWizUI) askProjectSettings(v *initValues) (changed []string, usage string, ok bool) {
	changed = []string{}
	for _, name := range initGateNames {
		if v.gateOrigin[name] == "flag" {
			continue
		}
		current := v.gates[name]
		argv := initWizGateArgv(current)
		typed, ok := u.text(initWizWithHint(`Gate `+name+` (blank or "none": no gate)`, initWizSettingHints[name]), strings.Join(argv, " "))
		if !ok {
			return nil, "", false
		}
		text := strings.TrimFunc(typed, isJSSpace)
		var next []string
		if text != "" && text != "none" {
			words, problem := initShellWords(text)
			if problem != "" {
				return nil, problem, true
			}
			next = words
		}
		a, _ := json.Marshal(next)
		b, _ := json.Marshal(argv)
		if string(a) == string(b) {
			continue
		}
		changed = append(changed, "gates."+name)
		switch {
		case next == nil:
			v.gates[name] = nil
		case initIsMap(current):
			obj := current.(ledger.Object).Clone()
			obj.Set("argv", initAnyList(next))
			v.gates[name] = obj
		default:
			v.gates[name] = initAnyList(next)
		}
	}
	list := func(key, message, path string) bool {
		if v.proofOrigin[key] == "flag" {
			return true
		}
		cur, _ := initStrings(v.proof[key])
		typed, ok := u.text(initWizWithHint(message, initWizSettingHints[key]), strings.Join(cur, ","))
		if !ok {
			return false
		}
		next := initWizCSV(typed)
		a, _ := json.Marshal(next)
		b, _ := json.Marshal(cur)
		if string(a) != string(b) {
			changed = append(changed, path)
		}
		v.proof[key] = initAnyList(next)
		return true
	}
	if !list("high", "High-risk paths (comma list of globs)", "proof.tiers.high.paths") {
		return nil, "", false
	}
	if v.proofOrigin["isolation"] != "flag" {
		cur, _ := v.proof["isolation"].(string)
		next, ok := u.sel(initWizWithHint("Proof isolation", initWizSettingHints["isolation"]), initWizValues("export", "lock"), cur)
		if !ok {
			return nil, "", false
		}
		if next != cur {
			changed = append(changed, "proof.isolation")
		}
		v.proof["isolation"] = next
	}
	if !list("link_dirs", "Export: directories to link", "proof.export.link_dirs") ||
		!list("copy_untracked", "Export: untracked files to copy", "proof.export.copy_untracked") {
		return nil, "", false
	}
	return changed, "", true
}

// initWizCloneValues copies the answers a dry run may change.
func initWizCloneValues(v initValues) initValues {
	out := v
	out.levels = initClone(v.levels).(ledger.Object)
	out.harnesses = append([]string{}, v.harnesses...)
	out.gates, out.proof = map[string]any{}, map[string]any{}
	for k, x := range v.gates {
		out.gates[k] = initClone(x)
	}
	for k, x := range v.proof {
		out.proof[k] = initClone(x)
	}
	return out
}

// initWizardPrecheck decides, before the first prompt, every hand-over the
// wizard can meet: the terminal, the setup steps Go does not serve, and the
// shapes it does not draw like Node. It returns the EOF warning text.
func initWizardPrecheck(st initSetupState, parsed initParsed, env initWizEnv, envOK bool) (string, handoverReason) {
	if !envOK || !initWizTerminalOK(env) {
		return "", hInitInteractive
	}
	v := st.v
	switch v.jevMode {
	case "auto":
		return "", hInitJevKey
	case "ref":
		if !strings.HasPrefix(v.jevRef, "env:") && !initOpRef.MatchString(v.jevRef) && v.jevRef != "user" && !strings.HasPrefix(v.jevRef, "keychain:") {
			return "", hInitJevOp
		}
	}
	if v.doctor {
		return "", hInitDoctor
	}
	if _, yes := parsed.initLast("--yes-tool"); yes {
		return "", hInitWizardTools
	}
	if v.refreshModels || func() bool { _, f := initLastFor(parsed, "refresh_models"); return !f }() {
		if !modelsCacheWritable(st.home) {
			return "", hInitWizardModels
		}
		for _, source := range []string{".codex", ".grok"} {
			if data, err := os.ReadFile(filepath.Join(st.home, source, "models_cache.json")); err == nil && !json.Valid(data) {
				return "", hInitWizardModels
			}
		}
	}
	// what the prompts and the summary show must be plain text Go measures
	for _, l := range []string{"L0", "L1", "L2", "L3"} {
		if _, ok := initWizLevelText(v.levels.Get(l)); !ok {
			return "", hInitWizardShape
		}
	}
	if _, ok := initWizSoloText(v.soloProject); !ok {
		return "", hInitWizardShape
	}
	if _, ok := v.tools.(string); !ok {
		return "", hInitWizardShape
	}
	for _, name := range initGateNames {
		if !initWizPlainText(strings.Join(initWizGateArgv(v.gates[name]), " ")) {
			return "", hInitWizardShape
		}
	}
	for _, key := range initProofKeys {
		if list, ok := initStrings(v.proof[key]); ok && !initWizPlainText(strings.Join(list, ",")) {
			return "", hInitWizardShape
		}
	}
	_, _, lines := initSummary(v, st.ctx)
	for _, l := range lines {
		if !initWizPlainText(l) {
			return "", hInitWizardShape
		}
	}
	for _, p := range initProviders {
		if p == "" {
			return "", hInitWizardShape
		}
	}
	eof := initWizEOFFile()
	if eof == "" {
		return "", hInitWizardEOF
	}
	// a dry run of the setup over every scope and method, with every harness
	for _, scope := range []string{"project", "global"} {
		for _, method := range []string{"symlink", "copy"} {
			dry := initWizCloneValues(v)
			if _, f := initLastFor(parsed, "harnesses"); !f {
				dry.harnesses = append([]string{}, initHarnessIDs...)
			}
			if _, f := initLastFor(parsed, "scope"); !f {
				dry.scope = scope
			}
			if _, f := initLastFor(parsed, "method"); !f {
				dry.method = method
			}
			dry.tools = "current"
			dry.refreshModels = false
			s := st
			s.v = dry
			if _, reason := initSetupFinish(s, parsed, false, &initSetupWizard{}); reason != 0 {
				return "", reason
			}
		}
	}
	return eof, 0
}

// initWizardServe runs the wizard: the prompts, the summary and the
// settings choice, then the setup. eof reports that the input ended at a
// prompt (Node exits 13 without its error log).
func initWizardServe(parsed initParsed, home, cwd string, existingFile bool) (func(stdout, stderr io.Writer, eof *bool) int, handoverReason) {
	env, envOK := initWizOpen()
	if !envOK || !initWizTerminalOK(env) {
		return nil, hInitInteractive
	}
	st, reason := initSetupLoad(parsed, home, cwd, existingFile)
	if reason != 0 {
		return nil, reason
	}
	eofText, reason := initWizardPrecheck(st, parsed, env, envOK)
	if reason != 0 {
		return nil, reason
	}
	return func(_, stderr io.Writer, eof *bool) int {
		u := &initWizUI{env: env, term: &initWizTerm{in: env.in, out: env.out, cols: env.cols, rows: env.rows}}
		ended := func() int {
			if u.end == initWizEOF {
				*eof = true
				_, _ = io.WriteString(stderr, eofText)
				return 13
			}
			return initFinish(env.out, stderr, home, false, 1, initWizCancelText)
		}
		v := st.v
		if !u.askAnswers(&v, st.ctx, parsed) {
			return ended()
		}
		if v.draft && !v.push {
			return initFinish(env.out, stderr, home, false, 2, initDraftRefusal)
		}
		settings, blank, summary := initSummary(v, st.ctx)
		_, _ = io.WriteString(env.out, strings.Join(summary, "\n")+"\n")
		wiz := &initSetupWizard{printed: len(summary), summary: summary, settings: settings, blank: blank}
		choice, ok := u.sel(initWizWithHint("Gates and proof settings:", initWizHints["settings"]), []initWizOption{
			{value: "use", label: "Use these", hint: "keep the detected values; blanks stay blank"},
			{value: "customize", label: "Customize now", hint: "ask each gate and proof value, pre-filled with what was detected"},
			{value: "later", label: "Leave for later", hint: "keep the detected values and print the keys to edit later"},
		}, "use")
		if !ok {
			return initWizFail(u, ended, home, summary, wiz, stderr)
		}
		switch choice {
		case "customize":
			changed, usage, ok := u.askProjectSettings(&v)
			if !ok {
				return initWizFail(u, ended, home, summary, wiz, stderr)
			}
			if usage != "" {
				return initSetupRun(initSetupResult{home: home, lines: summary, shown: len(summary), printed: len(summary), code: 2, err: usage, settings: settings, blank: blank})(env.out, stderr)
			}
			text := "no change"
			if len(changed) > 0 {
				text = strings.Join(changed, ", ")
			}
			wiz.said = append(wiz.said, "settings: customized ("+text+")")
		case "later":
			keys := blank
			if len(keys) == 0 {
				keys = []string{}
				for _, f := range settings {
					keys = append(keys, f.Key)
				}
			}
			line := "settings: edit .code-forge.yml later: " + strings.Join(keys, ", ") + " — then run `code-forge validate`"
			wiz.said = append(wiz.said, line)
			_, _ = io.WriteString(env.out, line+"\n")
			wiz.printed++
		}
		st.v = v
		r, reason := initSetupFinish(st, parsed, false, wiz)
		if reason != 0 {
			// the dry run before the first prompt covers every answer; a
			// reason here is a bug, reported like Node's unexpected error
			return initSetupRun(initSetupResult{home: home, lines: append(append([]string{}, summary...), wiz.said...), shown: len(summary), printed: wiz.printed,
				code: 1, err: "unexpected error: " + handoverReasons[reason], settings: settings, blank: blank})(env.out, stderr)
		}
		if r.code == 0 && r.existingFile && r.writeConfig {
			apply, ok := u.confirm(initWizWithHint("Apply "+strconv.Itoa(r.changeCount)+" change(s) to "+config.Filename+"?", initWizHints["apply"]), true)
			if !ok && u.end == initWizEOF {
				return ended()
			}
			if !ok || !apply {
				r = initSetupResult{home: home, lines: r.lines, shown: r.shown, printed: r.printed, code: 1, err: initWizCancelText, settings: settings, blank: blank}
			}
		}
		return initSetupRun(r)(env.out, stderr)
	}, 0
}

// initWizFail ends the run on a cancelled or closed prompt after the
// summary: Node's finish keeps the lines said so far in the log.
func initWizFail(u *initWizUI, ended func() int, home string, summary []string, wiz *initSetupWizard, stderr io.Writer) int {
	if u.end == initWizEOF {
		return ended()
	}
	lines := append(append([]string{}, summary...), wiz.said...)
	return initSetupRun(initSetupResult{home: home, lines: lines, shown: len(summary), printed: wiz.printed, code: 1, err: initWizCancelText,
		settings: wiz.settings, blank: wiz.blank})(u.env.out, stderr)
}

// initWizToolLines is step 1 of runInit for `tools: recommended` without
// --yes-tool: each tool present, or its install hint.
func initWizToolLines(ctx initCtx) []string {
	out := []string{}
	for _, tool := range toolRows {
		if tool.id == "solo" && ctx.solo || detectTool(tool) {
			out = append(out, "tool "+tool.id+": present")
			continue
		}
		argv, hint := toolInstallPlan(tool)
		how := hint
		if argv != nil {
			how = "install with: " + strings.Join(argv, " ")
		}
		out = append(out, "tool "+tool.id+": missing — "+how+" (or: code-forge tools install)")
	}
	return out
}
