package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/docsync"
	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/yamledit"

	"gopkg.in/yaml.v3"
)

// `code-forge init` past the refusals, without prompts (B527): the setup
// path of runInit in src/install/wizard/run.mjs. initSetupPlan reads only;
// it returns the run (every write happens inside it) or the narrow reason a
// step Go does not serve hands over before anything is written: a Jev key
// read from the key store (init-jev-key), a 1Password item lookup
// (init-jev-op), the tools step (init-tools), the model refresh
// (init-models), the doctor (init-doctor), a link Go cannot place like Node
// (init-links), a config Go cannot rebuild byte for byte
// (init-config-shape), a config the schema or the secret scan judges
// (init-config-check) and a user config Go cannot read like Node
// (init-user-config).

// initNoEngineStop is NO_ENGINE_STOP_TEXT in run.mjs.
const initNoEngineStop = "code-forge: no Solo and no subagent tool in this harness.\n" +
	"  To run coders as detached CLI processes (power-user mode) add to .code-forge.yml:\n" +
	"    engine: subprocess\n" +
	"  Then re-run. (R2: this engine is never selected automatically.)"

var (
	initGateNames     = []string{"test", "lint", "types", "format"}
	initProofKeys     = []string{"isolation", "high", "link_dirs", "copy_untracked"}
	initProofOrder    = []string{"high", "isolation", "link_dirs", "copy_untracked"}
	initProofLabels   = map[string]string{"high": "high-risk paths", "isolation": "isolation", "link_dirs": "link dirs", "copy_untracked": "copy untracked"}
	initSettingKeys   = map[string]string{"test": "gates.test", "lint": "gates.lint", "types": "gates.types", "format": "gates.format", "high": "proof.tiers.high.paths", "isolation": "proof.isolation", "link_dirs": "proof.export.link_dirs", "copy_untracked": "proof.export.copy_untracked"}
	initGateMissing   = map[string]string{"test": "no test command found", "lint": "no linter found", "types": "no type checker found", "format": "no formatter found"}
	initStackMarker   = map[string]string{"rust": "Cargo.toml", "python": "pyproject.toml", "go": "go.mod"}
	initCopyUntracked = map[string][]string{"laravel": {".env", ".env.testing"}, "php": {".env"}, "node": {".env", ".env.test"}, "python": {".env"}}
	initProfiles      = []string{"laravel", "php", "node", "python", "rust", "go", "unknown"}
	initLinkDirs      = []struct {
		dir       string
		manifests []string
	}{
		{"vendor", []string{"composer.json", "go.mod"}},
		{"node_modules", []string{"package.json"}},
		{".venv", []string{"pyproject.toml", "requirements.txt", "setup.py", "Pipfile"}},
		{"target", []string{"Cargo.toml"}},
	}
	initArrayIndex = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)
	initShownQuote = regexp.MustCompile(`["'\\]`)
	initSlugRun    = regexp.MustCompile(`[^a-z0-9]+`)
	initSoloName   = regexp.MustCompile(`(?i)solo`)
)

// initCtx is gatherContext in answers.mjs.
type initCtx struct {
	cwd, home         string
	stack             string
	detected          map[string]any
	profile           string
	gitRepo           bool
	detectedHarnesses []string
	agent             bool
	harness           string
	solo              bool
	name, slug        string
}

func initExists(p string) bool { _, err := os.Stat(p); return err == nil }

func initGather(cwd, home string) initCtx {
	plan := gates.Detect(cwd)
	ctx := initCtx{cwd: cwd, home: home, stack: plan.Stack, detected: map[string]any{}}
	for _, name := range initGateNames {
		ctx.detected[name] = nil
		if c := plan.Commands[name]; c != nil && c.Argv != nil {
			argv := []any{}
			for _, w := range c.Argv {
				argv = append(argv, w)
			}
			ctx.detected[name] = argv
		}
	}
	ctx.profile = initProofProfile(plan.Stack, cwd)
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if initExists(filepath.Join(dir, ".git")) {
			ctx.gitRepo = true
			break
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	for _, h := range installHarnesses {
		detected := commandOnPath(h.command, os.Getenv("PATH"))
		if info, err := os.Stat(filepath.Join(home, h.homeMarker)); err == nil && info.IsDir() {
			detected = true
		}
		if detected {
			ctx.detectedHarnesses = append(ctx.detectedHarnesses, h.id)
		}
	}
	for _, name := range []string{"CLAUDECODE", "CLAUDE_CODE", "CURSOR_AGENT", "AI_AGENT"} {
		if os.Getenv(name) != "" {
			ctx.agent = true
			switch name {
			case "CLAUDECODE", "CLAUDE_CODE":
				ctx.harness = "claude"
			case "CURSOR_AGENT":
				ctx.harness = "cursor"
			}
			break
		}
	}
	if ctx.harness == "" && (os.Getenv("CODEX_SANDBOX") != "" || os.Getenv("CODEX_SANDBOX_NETWORK_DISABLED") != "") {
		ctx.agent, ctx.harness = true, "codex"
	}
	if ctx.agent && ctx.harness == "" {
		ctx.harness = "unknown"
	}
	ctx.solo = os.Getenv("SOLO_MCP_PATH") != ""
	if !ctx.solo {
		if raw, err := os.ReadFile(filepath.Join(home, ".claude.json")); err == nil {
			var parsed map[string]any
			if json.Unmarshal(raw, &parsed) == nil {
				if servers, ok := parsed["mcpServers"].(map[string]any); ok {
					for name := range servers {
						ctx.solo = ctx.solo || initSoloName.MatchString(name)
					}
				}
			}
		}
	}
	ctx.name, ctx.slug = initProjectIdentity(cwd)
	return ctx
}

// initProofProfile is proofProfile in answers.mjs.
func initProofProfile(stack, cwd string) string {
	composer := initExists(filepath.Join(cwd, "composer.json"))
	php := strings.HasPrefix(stack, "php") || composer
	switch {
	case php && initExists(filepath.Join(cwd, "artisan")):
		return "laravel"
	case strings.HasPrefix(stack, "php"):
		return "php"
	case stack != "unknown" && initHas(initProfiles, stack):
		return stack
	case composer:
		return "php"
	}
	return "unknown"
}

func initSlugify(name string) string {
	slug := strings.Trim(initSlugRun.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if slug == "" {
		return "project"
	}
	return slug
}

// initProjectIdentity is projectIdentity in answers.mjs.
func initProjectIdentity(cwd string) (string, string) {
	for _, file := range []string{"package.json", "composer.json"} {
		raw, err := os.ReadFile(filepath.Join(cwd, file))
		if err != nil {
			continue
		}
		var parsed any
		if json.Unmarshal(raw, &parsed) != nil {
			continue
		}
		obj, _ := parsed.(map[string]any)
		name, _ := obj["name"].(string)
		if file == "composer.json" && name != "" {
			name = name[strings.LastIndex(name, "/")+1:]
		}
		if name != "" {
			if strings.HasPrefix(name, "@") {
				name = name[strings.LastIndex(name, "/")+1:]
			}
			return name, initSlugify(name)
		}
	}
	base := filepath.Base(cwd)
	return base, initSlugify(base)
}

// ── plain JSON values: ledger.Object, []any, string, float64, bool, nil ──

// initPlainValue converts a parsed value to plain JSON, false for a value
// Go does not rebuild like Node (another type, a non-finite number, a key
// JavaScript orders first or treats as the prototype).
func initPlainValue(value any) (any, bool) {
	switch v := value.(type) {
	case nil, string, bool:
		return v, true
	case float64:
		return v, !math.IsNaN(v) && !math.IsInf(v, 0)
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case []config.Pair:
		out := ledger.Object{}
		for _, p := range v {
			if !initPlainKey(p.Key) {
				return nil, false
			}
			child, ok := initPlainValue(p.Value)
			if !ok {
				return nil, false
			}
			out = append(out, ledger.Field{Key: p.Key, Value: child})
		}
		return out, true
	case ledger.Object:
		out := ledger.Object{}
		for _, f := range v {
			if !initPlainKey(f.Key) {
				return nil, false
			}
			child, ok := initPlainValue(f.Value)
			if !ok {
				return nil, false
			}
			out = append(out, ledger.Field{Key: f.Key, Value: child})
		}
		return out, true
	case []any:
		out := []any{}
		for _, item := range v {
			child, ok := initPlainValue(item)
			if !ok {
				return nil, false
			}
			out = append(out, child)
		}
		return out, true
	}
	return nil, false
}

func initPlainKey(key string) bool {
	return key != "__proto__" && !initArrayIndex.MatchString(key) && utf8.ValidString(key)
}

// initClone is clone (a JSON round trip) on plain JSON.
func initClone(value any) any {
	switch v := value.(type) {
	case ledger.Object:
		out := make(ledger.Object, len(v))
		for i, f := range v {
			out[i] = ledger.Field{Key: f.Key, Value: initClone(f.Value)}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = initClone(item)
		}
		return out
	}
	return value
}

func initIsMap(v any) bool { _, ok := v.(ledger.Object); return ok }

// initField is obj?.[key] through maps only (undefined → present false).
func initField(v any, path ...string) (any, bool) {
	for _, key := range path {
		obj, ok := v.(ledger.Object)
		if !ok {
			return nil, false
		}
		found := false
		for _, f := range obj {
			if f.Key == key {
				v, found = f.Value, true
				break
			}
		}
		if !found {
			return nil, false
		}
	}
	return v, true
}

// initGiven is a value both present and not null (the `set` helper's test).
func initKept(v any, present bool) bool { return present && v != nil }

func initStrings(v any) ([]string, bool) {
	list, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := []string{}
	for _, item := range list {
		s, ok := item.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

func initAnyList(list []string) []any {
	out := []any{}
	for _, s := range list {
		out = append(out, s)
	}
	return out
}

// initDeepEqual is isDeepStrictEqual on plain JSON (key order ignored).
func initDeepEqual(a, b any) bool {
	switch x := a.(type) {
	case ledger.Object:
		y, ok := b.(ledger.Object)
		if !ok || len(x) != len(y) {
			return false
		}
		for _, f := range x {
			v, present := initField(y, f.Key)
			if !present || !initDeepEqual(f.Value, v) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !initDeepEqual(x[i], y[i]) {
				return false
			}
		}
		return true
	case float64:
		y, ok := b.(float64)
		return ok && x == y && math.Signbit(x) == math.Signbit(y)
	}
	return a == b
}

// initMergeInto is mergeInto in project-file.mjs.
func initMergeInto(base, over ledger.Object) ledger.Object {
	out := base.Clone()
	for _, f := range over {
		baseValue, _ := initField(base, f.Key)
		if o, ok := f.Value.(ledger.Object); ok && initIsMap(baseValue) {
			out.Set(f.Key, initMergeInto(baseValue.(ledger.Object), o))
		} else {
			out.Set(f.Key, f.Value)
		}
	}
	return out
}

// initChangedPaths is changedPaths in project-file.mjs.
func initChangedPaths(a, b any, prefix string) []string {
	x, okA := a.(ledger.Object)
	y, okB := b.(ledger.Object)
	if okA && okB {
		keys := []string{}
		for _, f := range append(x.Clone(), y...) {
			if !initHas(keys, f.Key) {
				keys = append(keys, f.Key)
			}
		}
		out := []string{}
		for _, k := range keys {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			va, inA := initField(x, k)
			vb, inB := initField(y, k)
			if inA != inB {
				out = append(out, p)
				continue
			}
			out = append(out, initChangedPaths(va, vb, p)...)
		}
		return out
	}
	if initDeepEqual(a, b) {
		return nil
	}
	if prefix == "" {
		prefix = "(root)"
	}
	return []string{prefix}
}

// initYAMLPlain reports a YAML text with no anchor, alias, explicit tag or
// merge key, the constructs Go's reader and Node's may build differently.
func initYAMLPlain(source string) bool {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(source), &doc); err != nil {
		return false
	}
	var walk func(n *yaml.Node) bool
	walk = func(n *yaml.Node) bool {
		if n.Kind == yaml.AliasNode || n.Anchor != "" || n.Style&yaml.TaggedStyle != 0 {
			return false
		}
		for i, c := range n.Content {
			if n.Kind == yaml.MappingNode && i%2 == 0 && c.Value == "<<" {
				return false
			}
			if !walk(c) {
				return false
			}
		}
		return true
	}
	return walk(&doc)
}

// initDefaultLevels is defaultsForProvider(p).levels in key order.
func initDefaultLevels(provider string) ledger.Object {
	defaults := config.DefaultsForProvider(provider)
	levels, _ := defaults["levels"].(map[string]any)
	out := ledger.Object{}
	for _, name := range []string{"L0", "L1", "L2", "L3"} {
		spec, _ := levels[name].(map[string]any)
		entry := ledger.Object{{Key: "model", Value: spec["model"]}}
		if effort, ok := spec["effort"]; ok {
			entry = append(entry, ledger.Field{Key: "effort", Value: effort})
		}
		out = append(out, ledger.Field{Key: name, Value: entry})
	}
	return out
}

// initValues is the answers resolveAnswers returns that the setup reads.
type initValues struct {
	tools          any
	harnesses      []string
	scope, method  string
	provider       string
	levels         ledger.Object
	refreshModels  bool
	multimodel     bool
	secondProvider string
	jevMode        string
	jevRef         string
	engine         any
	soloProject    any
	push, draft    bool
	gates          map[string]any
	proof          map[string]any
	doctor         bool
	gateOrigin     map[string]string
	proofOrigin    map[string]string
}

var initAnswerKey = map[string]string{
	"--tools": "tools", "--harness": "harnesses", "-g": "scope", "-p": "scope", "--copy": "method",
	"--provider": "provider", "--level": "levels", "--refresh-models": "refresh_models", "--multimodel": "multimodel",
	"--second-provider": "second_provider", "--jev-ref": "jev", "--jev-env": "jev", "--no-jev": "jev",
	"--engine": "engine", "--solo-project": "solo_project", "--gate": "gates", "--proof": "proof",
	"--skip-doctor": "doctor", "--coder-may-push": "coder_may_push", "--coder-may-open-draft-pr": "coder_may_open_draft_pr",
}

func initFlagsFor(parsed initParsed, key string) []initGiven {
	var out []initGiven
	for _, g := range parsed.given {
		if initAnswerKey[g.flag] == key {
			out = append(out, g)
		}
	}
	return out
}

func initLastFor(parsed initParsed, key string) (initGiven, bool) {
	list := initFlagsFor(parsed, key)
	if len(list) == 0 {
		return initGiven{}, false
	}
	return list[len(list)-1], true
}

// initResolve is resolveAnswers in answers.mjs; false hands over a value
// shape Go does not follow like Node.
func initResolve(ctx initCtx, ex, user ledger.Object, existing bool, parsed initParsed) (initValues, bool) {
	v := initValues{gateOrigin: map[string]string{}, proofOrigin: map[string]string{}}
	v.tools = "current"
	if t, present := initField(user, "tools"); initKept(t, present) {
		v.tools = t
	}
	if g, ok := initLastFor(parsed, "tools"); ok {
		v.tools = g.value
	}
	if h, present := initField(ex, "harnesses"); initKept(h, present) {
		list, ok := initStrings(h)
		if !ok {
			return v, false
		}
		for _, id := range list {
			if !initHas(initHarnessIDs, id) {
				return v, false
			}
		}
		v.harnesses = list
	} else if v.tools == "current" && ctx.harness != "" && ctx.harness != "unknown" {
		v.harnesses = []string{ctx.harness}
	} else {
		v.harnesses = append([]string{}, ctx.detectedHarnesses...)
	}
	if g, ok := initLastFor(parsed, "harnesses"); ok {
		v.harnesses = g.value.([]string)
	}
	v.scope, v.method = "global", "symlink"
	if ctx.gitRepo {
		v.scope = "project"
	}
	if g, ok := initLastFor(parsed, "scope"); ok {
		v.scope = g.value.(string)
	}
	if _, ok := initLastFor(parsed, "method"); ok {
		v.method = "copy"
	}
	exProvider, hasProvider := initField(ex, "provider")
	v.provider = "anthropic"
	if initKept(exProvider, hasProvider) {
		s, ok := exProvider.(string)
		if !ok || !initHas(initProviders, s) {
			return v, false
		}
		v.provider = s
	}
	if g, ok := initLastFor(parsed, "provider"); ok {
		v.provider = g.value.(string)
	}
	v.levels = initDefaultLevels(v.provider)
	if exLevels, present := initField(ex, "levels"); exProvider == v.provider && initKept(exLevels, present) {
		obj, ok := exLevels.(ledger.Object)
		if !ok {
			return v, false
		}
		v.levels = initClone(obj).(ledger.Object)
	}
	for _, g := range initFlagsFor(parsed, "levels") {
		lv := g.value.(initLevelValue)
		v.levels.Set(lv.level, lv.spec.Clone())
	}
	_, v.refreshModels = initLastFor(parsed, "refresh_models")
	if m, present := initField(ex, "review", "multimodel"); initKept(m, present) {
		b, ok := m.(bool)
		if !ok {
			return v, false
		}
		v.multimodel = b
	}
	if g, ok := initLastFor(parsed, "multimodel"); ok {
		v.multimodel = g.value.(bool)
	}
	if s, present := initField(ex, "review", "second_provider"); initKept(s, present) {
		p, ok := s.(string)
		if !ok || !initHas(initProviders, p) {
			return v, false
		}
		v.secondProvider = p
	}
	if g, ok := initLastFor(parsed, "second_provider"); ok {
		v.secondProvider = g.value.(string)
	}
	switch jev, _ := initField(ex, "keys", "jev"); {
	case func() bool { _, ok := jev.(string); return ok }():
		v.jevMode, v.jevRef = "ref", jev.(string)
	case existing:
		v.jevMode = "none"
	default:
		v.jevMode = "auto"
	}
	if g, ok := initLastFor(parsed, "jev"); ok {
		switch g.flag {
		case "--no-jev":
			v.jevMode, v.jevRef = "none", ""
		case "--jev-env":
			v.jevMode, v.jevRef = "ref", "env:"+g.value.(string)
		default:
			v.jevMode, v.jevRef = "ref", g.value.(string)
		}
	}
	v.engine = "auto"
	if e, present := initField(ex, "engine"); initKept(e, present) {
		v.engine = initClone(e)
	}
	if g, ok := initLastFor(parsed, "engine"); ok {
		v.engine = g.value
	}
	if p, present := initField(user, "solo", "project_id"); initKept(p, present) {
		v.soloProject = initClone(p)
	}
	if g, ok := initLastFor(parsed, "solo_project"); ok {
		v.soloProject = g.value
	}
	draftFlag := false
	if b, ok := func() (any, bool) { return initField(ex, "autonomy", "coder_may_push") }(); ok {
		v.push, _ = b.(bool)
	}
	if g, ok := initLastFor(parsed, "coder_may_push"); ok {
		v.push = g.value.(bool)
	}
	if b, ok := initField(ex, "autonomy", "coder_may_open_draft_pr"); ok {
		v.draft, _ = b.(bool)
	}
	if g, ok := initLastFor(parsed, "coder_may_open_draft_pr"); ok {
		v.draft, draftFlag = g.value.(bool), true
	}
	if !v.push && !draftFlag {
		v.draft = false
	}

	// gates: flag, else the existing config, else the project
	v.gates = map[string]any{}
	exGates, hasGates := initField(ex, "gates")
	if initKept(exGates, hasGates) && !initIsMap(exGates) {
		return v, false
	}
	for _, name := range initGateNames {
		kept, present := initField(exGates, name)
		if initKept(kept, present) {
			if !initGateShape(kept) {
				return v, false
			}
			v.gates[name], v.gateOrigin[name] = initClone(kept), "existing"
		} else {
			v.gates[name], v.gateOrigin[name] = ctx.detected[name], "detected"
		}
	}
	for _, g := range initFlagsFor(parsed, "gates") {
		gv := g.value.(initGateValue)
		v.gates[gv.name], v.gateOrigin[gv.name] = gv.argv, "flag"
	}

	exProof, hasProof := initField(ex, "proof")
	if initKept(exProof, hasProof) && !initIsMap(exProof) {
		return v, false
	}
	proposal := initProposeProof(ctx)
	paths := map[string][]string{"high": {"tiers", "high", "paths"}, "isolation": {"isolation"}, "link_dirs": {"export", "link_dirs"}, "copy_untracked": {"export", "copy_untracked"}}
	v.proof = map[string]any{}
	for _, key := range initProofKeys {
		kept, present := initField(exProof, paths[key]...)
		if initKept(kept, present) {
			if _, isString := kept.(string); key == "isolation" && !isString {
				return v, false
			}
			if _, ok := initStrings(kept); key != "isolation" && !ok {
				return v, false
			}
			v.proof[key], v.proofOrigin[key] = initClone(kept), "existing"
		} else {
			v.proof[key] = proposal[key]
			v.proofOrigin[key] = "default"
			if key == "link_dirs" || key == "copy_untracked" {
				v.proofOrigin[key] = "detected"
			}
		}
	}
	for _, g := range initFlagsFor(parsed, "proof") {
		pv := g.value.(initProofValue)
		v.proof[pv.key], v.proofOrigin[pv.key] = pv.value, "flag"
	}
	_, skip := initLastFor(parsed, "doctor")
	v.doctor = !skip
	return v, true
}

// initGateShape: a kept gate is an argv list or {argv, env?} with string
// words and an env mapping.
func initGateShape(gate any) bool {
	if _, ok := initStrings(gate); ok {
		return true
	}
	obj, ok := gate.(ledger.Object)
	if !ok {
		return false
	}
	argv, present := initField(obj, "argv")
	if _, ok := initStrings(argv); !present || !ok {
		return false
	}
	env, present := initField(obj, "env")
	return !present || initIsMap(env)
}

func initLinkDirsFound(cwd string) map[string][]string {
	out := map[string][]string{}
	for _, e := range initLinkDirs {
		for _, m := range e.manifests {
			if initExists(filepath.Join(cwd, m)) {
				out[e.dir] = append(out[e.dir], m)
			}
		}
	}
	return out
}

// initProposeProof is proposeProof(profile, cwd).
func initProposeProof(ctx initCtx) map[string]any {
	found := initLinkDirsFound(ctx.cwd)
	dirs := []any{}
	for _, e := range initLinkDirs {
		if len(found[e.dir]) > 0 {
			dirs = append(dirs, e.dir)
		}
	}
	copies := []any{}
	for _, rel := range initCopyUntracked[ctx.profile] {
		if initExists(filepath.Join(ctx.cwd, rel)) {
			copies = append(copies, rel)
		}
	}
	return map[string]any{"high": []any{}, "isolation": "export", "link_dirs": dirs, "copy_untracked": copies}
}

func initEffectiveProvider(v initValues, level string) string {
	if p, ok := initField(v.levels, level, "provider"); ok {
		if s, ok := p.(string); ok && s != "" {
			return s
		}
	}
	return v.provider
}

func initJSONString(s string) string {
	data, _ := ledger.Stringify(s, false)
	return string(data)
}

func initShown(argv []string) string {
	parts := []string{}
	for _, w := range argv {
		if w == "" || initShownQuote.MatchString(w) || strings.IndexFunc(w, isJSSpace) >= 0 {
			w = initJSONString(w)
		}
		parts = append(parts, w)
	}
	return strings.Join(parts, " ")
}

func initFirstPresent(cwd string, names ...string) string {
	for _, n := range names {
		if initExists(filepath.Join(cwd, n)) {
			return n
		}
	}
	return ""
}

// initGateEvidence is gateEvidence in summary.mjs.
func initGateEvidence(stack string, argv []string, cwd string) string {
	or := func(a, b string) string {
		if a != "" {
			return a
		}
		return b
	}
	if strings.HasPrefix(stack, "php") {
		tool := ""
		if len(argv) > 0 && argv[0] != "" {
			tool = filepath.Base(argv[0])
		}
		switch {
		case tool == "phpstan":
			return or(initFirstPresent(cwd, "phpstan.neon", "phpstan.neon.dist"), "vendor/bin/phpstan")
		case tool == "pint":
			return or(initFirstPresent(cwd, "pint.json"), "vendor/bin/pint")
		case tool == "php" && len(argv) > 1 && argv[1] == "artisan":
			return "artisan"
		case tool == "pest":
			return "vendor/bin/pest"
		}
		return "composer.json"
	}
	if stack == "node" {
		if len(argv) > 1 && argv[0] == "npx" && argv[1] == "tsc" {
			return "tsconfig.json"
		}
		if len(argv) > 1 && argv[0] == "npx" && argv[1] == "prettier" {
			return or(initFirstPresent(cwd, ".prettierrc", ".prettierrc.json", ".prettierrc.js"), "package.json")
		}
		return "package.json"
	}
	if m, ok := initStackMarker[stack]; ok {
		return m
	}
	return "the project"
}

func initKeptFrom(source, flag string) string {
	switch source {
	case "flag":
		return flag
	case "existing":
		return ".code-forge.yml"
	}
	return "default"
}

// initSummary is summarizeSettings in summary.mjs.
func initSummary(v initValues, ctx initCtx) (ledger.Object, []string, []string) {
	settings := ledger.Object{}
	blank := []string{}
	lines := []string{"Project settings (detected)"}
	for _, name := range initGateNames {
		key := initSettingKeys[name]
		command := v.gates[name]
		var argv any = command
		var envKeys []string
		if obj, ok := command.(ledger.Object); ok {
			argv, _ = initField(obj, "argv")
			if env, ok := initField(obj, "env"); ok {
				for _, f := range env.(ledger.Object) {
					envKeys = append(envKeys, f.Key)
				}
				sort.Strings(envKeys)
			}
		}
		envNote := ""
		if len(envKeys) > 0 {
			envNote = " (env: " + strings.Join(envKeys, ", ") + ")"
		}
		source := v.gateOrigin[name]
		words, _ := initStrings(argv)
		var note string
		if len(words) == 0 {
			switch source {
			case "flag":
				note = "set blank by --gate"
			case "existing":
				note = "empty in .code-forge.yml"
			default:
				note = initGateMissing[name]
			}
			blank = append(blank, key)
			lines = append(lines, "  "+name+": blank"+envNote+" — "+note)
		} else {
			if source == "detected" {
				note = initGateEvidence(ctx.stack, words, ctx.cwd)
			} else {
				note = initKeptFrom(source, "--gate")
			}
			lines = append(lines, "  "+name+": "+initShown(words)+envNote+" (from "+note+")")
		}
		entry := ledger.Object{{Key: "value", Value: argv}, {Key: "source", Value: source}, {Key: "note", Value: note}}
		if len(envKeys) > 0 {
			entry = append(entry, ledger.Field{Key: "env_keys", Value: initAnyList(envKeys)})
		}
		settings = append(settings, ledger.Field{Key: key, Value: entry})
	}
	found := initLinkDirsFound(ctx.cwd)
	for _, pk := range initProofOrder {
		key := initSettingKeys[pk]
		value := v.proof[pk]
		source := v.proofOrigin[pk]
		label := initProofLabels[pk]
		list, isList := initStrings(value)
		var note string
		if value == nil || value == "" || isList && len(list) == 0 {
			switch {
			case source == "flag":
				note = "set blank by --proof"
			case source == "existing":
				note = "empty in .code-forge.yml"
			case pk == "high":
				note = "none set (all files light tier by path)"
			case pk == "link_dirs":
				note = "no dependency manifest found"
			default:
				looked := initCopyUntracked[ctx.profile]
				note = "none for this stack"
				if len(looked) > 0 {
					note = "none found (looked for " + strings.Join(looked, ", ") + ")"
				}
			}
			blank = append(blank, key)
			lines = append(lines, "  "+label+": blank — "+note)
		} else {
			if !isList {
				list = []string{value.(string)}
			}
			foundOnDisk := source == "detected" && pk == "copy_untracked"
			switch {
			case foundOnDisk:
				note = "found in the project"
			case source != "detected":
				note = initKeptFrom(source, "--proof")
			default:
				names := []string{}
				for _, d := range list {
					manifests, ok := found[d]
					if !ok {
						for _, e := range initLinkDirs {
							if e.dir == d {
								manifests = e.manifests
							}
						}
					}
					for _, m := range manifests {
						if !initHas(names, m) {
							names = append(names, m)
						}
					}
				}
				note = strings.Join(names, ", ")
			}
			if foundOnDisk {
				lines = append(lines, "  "+label+": "+strings.Join(list, ", ")+" ("+note+")")
			} else {
				lines = append(lines, "  "+label+": "+strings.Join(list, ", ")+" (from "+note+")")
			}
		}
		settings = append(settings, ledger.Field{Key: key, Value: ledger.Object{{Key: "value", Value: value}, {Key: "source", Value: source}, {Key: "note", Value: note}}})
	}
	if len(blank) > 0 {
		lines = append(lines, "Set the blanks later: edit .code-forge.yml ("+strings.Join(blank, ", ")+"), then run `code-forge validate`.")
	} else {
		lines = append(lines, "Change any of these later: edit .code-forge.yml, then run `code-forge validate`.")
	}
	return settings, blank, lines
}

// initGenerated is generatedConfig in project-file.mjs.
func initGenerated(v initValues, ctx initCtx, jevRef string) ledger.Object {
	review := ledger.Object{{Key: "multimodel", Value: v.multimodel}}
	if v.multimodel && v.secondProvider != "" {
		second := initDefaultLevels(v.secondProvider)
		l2 := second.Get("L2").(ledger.Object).Clone()
		l2.Set("provider", v.secondProvider)
		l3 := second.Get("L3").(ledger.Object).Clone()
		l3.Set("provider", v.secondProvider)
		review = append(review, ledger.Field{Key: "second_provider", Value: v.secondProvider},
			ledger.Field{Key: "second_levels", Value: ledger.Object{{Key: "L2", Value: l2}, {Key: "L3", Value: l3}}})
	}
	coders := float64(1)
	if v.engine == "solo" || v.engine == "auto" && ctx.solo {
		coders = 2
	}
	cfg := ledger.Object{
		{Key: "version", Value: float64(1)},
		{Key: "project", Value: ledger.Object{{Key: "name", Value: ctx.name}, {Key: "slug", Value: ctx.slug}}},
		{Key: "provider", Value: v.provider},
		{Key: "levels", Value: v.levels},
		{Key: "caps", Value: ledger.Object{{Key: "coders", Value: coders}}},
		{Key: "review", Value: review},
		{Key: "engine", Value: v.engine},
		{Key: "harnesses", Value: initAnyList(v.harnesses)},
	}
	if jevRef != "" {
		cfg = append(cfg, ledger.Field{Key: "keys", Value: ledger.Object{{Key: "jev", Value: jevRef}}})
	} else {
		cfg = append(cfg, ledger.Field{Key: "system1", Value: ledger.Object{{Key: "fallback", Value: "rules"}}})
	}
	cfg = append(cfg, ledger.Field{Key: "gates", Value: ledger.Object{
		{Key: "test", Value: v.gates["test"]}, {Key: "lint", Value: v.gates["lint"]},
		{Key: "types", Value: v.gates["types"]}, {Key: "format", Value: v.gates["format"]},
	}})
	cfg = append(cfg, ledger.Field{Key: "proof", Value: ledger.Object{
		{Key: "tiers", Value: ledger.Object{{Key: "high", Value: ledger.Object{{Key: "paths", Value: initClone(v.proof["high"])}}}}},
		{Key: "isolation", Value: v.proof["isolation"]},
		{Key: "export", Value: ledger.Object{{Key: "link_dirs", Value: initClone(v.proof["link_dirs"])}, {Key: "copy_untracked", Value: initClone(v.proof["copy_untracked"])}}},
	}})
	cfg = append(cfg, ledger.Field{Key: "autonomy", Value: ledger.Object{
		{Key: "coder_may_push", Value: v.push},
		{Key: "coder_may_open_draft_pr", Value: v.push && v.draft},
	}})
	return cfg
}

// initDropKeys removes keys from a map value, nil when nothing is left.
func initDropKeys(obj ledger.Object, keys ...string) ledger.Object {
	out := ledger.Object{}
	for _, f := range obj {
		if !initHas(keys, f.Key) {
			out = append(out, f)
		}
	}
	return out
}

// initMergeConfig is mergeConfig in project-file.mjs.
func initMergeConfig(existing, generated ledger.Object) ledger.Object {
	merged := initMergeInto(existing, generated)
	if review, ok := merged.Get("review").(ledger.Object); ok {
		if _, has := initField(generated, "review", "second_provider"); !has {
			merged.Set("review", initDropKeys(review, "second_provider", "second_levels"))
		}
	}
	jev, _ := initField(generated, "keys", "jev")
	_, hasJev := jev.(string)
	if keys, ok := merged.Get("keys").(ledger.Object); ok && !hasJev {
		if _, has := initField(keys, "jev"); has {
			if rest := initDropKeys(keys, "jev"); len(rest) > 0 {
				merged.Set("keys", rest)
			} else {
				merged = initDropKeys(merged, "keys")
			}
		}
	}
	if system1, ok := merged.Get("system1").(ledger.Object); ok && hasJev {
		if _, has := initField(system1, "fallback"); has {
			if rest := initDropKeys(system1, "fallback"); len(rest) > 0 {
				merged.Set("system1", rest)
			} else {
				merged = initDropKeys(merged, "system1")
			}
		}
	}
	return merged
}

// initReadUserConfig is readUserConfig: nil for a missing file or a value
// that is not a mapping; false for a file Go cannot read like Node.
func initReadUserConfig(file string) (ledger.Object, bool) {
	raw, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return nil, true
	}
	if err != nil || !utf8.Valid(raw) || strings.HasPrefix(string(raw), "\ufeff") || !initYAMLPlain(string(raw)) {
		return nil, false
	}
	parsed, err := config.ParseOrdered(string(raw))
	if err != nil {
		return nil, false
	}
	if _, ok := parsed.([]config.Pair); !ok {
		return nil, true
	}
	value, ok := initPlainValue(parsed)
	if !ok {
		return nil, false
	}
	return value.(ledger.Object), true
}

// initLinkStep is one harness link of step 2.
type initLinkStep struct {
	id, target string
}

// initSetupPlan decides the non-interactive setup without side effects:
// the run, or the reason it hands over.
func initSetupPlan(parsed initParsed, home, cwd string, jsonMode, existingFile bool) (func(stdout, stderr io.Writer) int, handoverReason) {
	st, reason := initSetupLoad(parsed, home, cwd, existingFile)
	if reason != 0 {
		return nil, reason
	}
	r, reason := initSetupFinish(st, parsed, jsonMode, nil)
	if reason != 0 {
		return nil, reason
	}
	return initSetupRun(r), 0
}

// initSetupState is what runInit has read before the answers are asked.
type initSetupState struct {
	home, cwd, configFile, userFile string
	existingFile                    bool
	existing, user                  ledger.Object
	ctx                             initCtx
	v                               initValues
}

// initSetupWizard is what the terminal wizard (B528) adds to the setup:
// the summary and settings lines it already printed, and the tools and
// model-cache steps it serves.
type initSetupWizard struct {
	said    []string // lines said after the summary, before the jev line
	printed int      // lines already on stdout
	// the summary as shown before "Customize now" changed the answers
	summary  []string
	settings ledger.Object
	blank    []string
	// jev is step 5's answer the wizard gave for Jev auto (B611); nil reads
	// the key chain here
	jev *initWizJev
}

// initSetupLoad reads the existing config, the user config and the
// project, and resolves the answers from defaults, existing and flags.
func initSetupLoad(parsed initParsed, home, cwd string, existingFile bool) (initSetupState, handoverReason) {
	configFile := filepath.Join(cwd, config.Filename)
	var existing ledger.Object
	if existingFile {
		raw, err := os.ReadFile(configFile)
		if err != nil || !initYAMLPlain(string(raw)) {
			return initSetupState{}, hInitConfigShape
		}
		m, order, err := config.ParseTextOrdered(string(raw), configFile)
		if err != nil {
			return initSetupState{}, hInitConfigShape
		}
		value, ok := initPlainValue(order.Object(m))
		if !ok {
			return initSetupState{}, hInitConfigShape
		}
		existing = value.(ledger.Object)
	}
	userFile := filepath.Join(home, ".code-forge", "config.yml")
	user, ok := initReadUserConfig(userFile)
	if !ok {
		return initSetupState{}, hInitUserConfig
	}
	ctx := initGather(cwd, home)
	v, ok := initResolve(ctx, existing, user, existingFile, parsed)
	if !ok {
		return initSetupState{}, hInitConfigShape
	}
	return initSetupState{home: home, cwd: cwd, configFile: configFile, userFile: userFile, existingFile: existingFile,
		existing: existing, user: user, ctx: ctx, v: v}, 0
}

// initSetupFinish is runInit from the summary on, decided without side
// effects: what the run says, writes and returns. wiz is nil for the
// --no-interaction path (B527).
func initSetupFinish(st initSetupState, parsed initParsed, jsonMode bool, wiz *initSetupWizard) (initSetupResult, handoverReason) {
	home, cwd, configFile, userFile := st.home, st.cwd, st.configFile, st.userFile
	existingFile, existing, user, ctx, v := st.existingFile, st.existing, st.user, st.ctx, st.v
	var err error
	settings, blank, summaryLines := initSummary(v, ctx)
	if wiz != nil && wiz.summary != nil {
		settings, blank, summaryLines = wiz.settings, wiz.blank, wiz.summary
	}

	// what the run says and does, decided here; fail ends it before writes
	lines := append([]string{}, summaryLines...)
	printed := 0
	if wiz != nil {
		lines = append(lines, wiz.said...)
		printed = wiz.printed
	}
	// the Jev key read from the store (B548): its side effect and the values
	// Node registers for redaction reach the run on every exit
	var pre func()
	var secrets []string
	fail := func(code int, message string) (initSetupResult, handoverReason) {
		return initSetupResult{home: home, jsonMode: jsonMode, lines: lines, shown: len(summaryLines), printed: printed, code: code, err: message, settings: settings, blank: blank, pre: pre, secrets: secrets}, 0
	}

	jevRef, jevSource := "", "none"
	var missing []string
	switch v.jevMode {
	case "auto":
		if wiz != nil && wiz.jev != nil {
			secrets = wiz.jev.secrets
			jevRef, jevSource = wiz.jev.ref, wiz.jev.source
			break
		}
		auto, reason := initJevAutoPlan(home)
		if reason != 0 {
			return initSetupResult{}, reason
		}
		pre, secrets = auto.pre, auto.secrets
		if auto.found {
			jevRef, jevSource = auto.ref, auto.source
		} else {
			missing = append(missing, "keys.jev (pass --jev-ref, --jev-env or --no-jev)")
		}
	case "ref":
		jevRef = v.jevRef
		if !strings.HasPrefix(jevRef, "env:") && !initOpRef.MatchString(jevRef) && jevRef != "user" && !strings.HasPrefix(jevRef, "keychain:") {
			return initSetupResult{}, hInitJevOp
		}
		switch {
		case strings.HasPrefix(jevRef, "env:"):
			jevSource = "env"
		case strings.HasPrefix(jevRef, "op://"):
			jevSource = "op"
		default:
			jevSource = "keychain"
		}
	}
	if v.multimodel && v.secondProvider == "" {
		missing = append(missing, "review.second_provider (pass --second-provider)")
	}
	if len(missing) > 0 {
		return fail(2, "missing required answers, nothing written:\n  "+strings.Join(missing, "\n  "))
	}
	lines = append(lines, "keys: jev from "+jevSource)
	if v.multimodel {
		reviewer1 := initEffectiveProvider(v, "L2")
		if v.secondProvider == reviewer1 {
			return fail(2, "review.second_provider equals the effective L2 provider ("+reviewer1+") — multimodel needs two providers; pass --second-provider with another one. Nothing written.")
		}
		reviewers := []string{reviewer1, v.secondProvider}
		if judge := initEffectiveProvider(v, "L3"); initHas(reviewers, judge) {
			third := ""
			for _, p := range initProviders {
				if !initHas(reviewers, p) {
					third = p
					break
				}
			}
			l3Flagged := false
			for _, g := range initFlagsFor(parsed, "levels") {
				l3Flagged = l3Flagged || g.value.(initLevelValue).level == "L3"
			}
			if l3Flagged || third == "" {
				thirdNote := ""
				if third != "" {
					thirdNote = " (" + third + ")"
				}
				return fail(2, "--level L3 pins the judge to "+judge+", a reviewer's provider — the consensus judge must come from a third provider"+thirdNote+"; change or drop --level L3. Nothing written.")
			}
			l3 := initDefaultLevels(third).Get("L3").(ledger.Object).Clone()
			l3.Set("provider", third)
			v.levels.Set("L3", l3)
			lines = append(lines, "levels.L3: judge proposed from "+third+" (consensus needs a third provider; reviewers "+strings.Join(reviewers, ", ")+")")
		}
	}

	merged := initMergeConfig(existing, initGenerated(v, ctx, jevRef))
	text, err := yamledit.StringifyPlain(merged)
	if err != nil {
		return initSetupResult{}, hInitConfigShape
	}
	cfg, order, err := config.ParseTextOrdered(text, configFile)
	if err != nil || !initDeepEqual(order.Object(cfg), merged) {
		return initSetupResult{}, hInitConfigShape
	}
	if len(config.SchemaIssues(cfg, "")) > 0 || configHoldsSecretShape(cfg) {
		return initSetupResult{}, hInitConfigCheck
	}
	if sandbox, ok := objAt(cfg, "coder", "sandbox"); ok {
		if dirs, ok := sandbox["add_dirs"].([]any); ok && len(dirs) > 0 {
			return initSetupResult{}, hInitConfigCheck
		}
	}
	errs, warns := configValidator{cfg: cfg, order: order, hasCli: func(string) bool { return true }}.run()
	if len(errs) > 0 {
		parts := []string{}
		for _, e := range errs {
			parts = append(parts, "  "+e.rule+": "+maskTokens(e.message))
		}
		return fail(1, "the resulting config is invalid, nothing written:\n"+strings.Join(parts, "\n"))
	}
	for _, w := range warns {
		lines = append(lines, "config warning: "+w.rule)
	}
	writeConfig, changeCount := true, 0
	if existingFile {
		changed := initChangedPaths(existing, merged, "")
		changeCount = len(changed)
		if len(changed) == 0 {
			writeConfig = false
			lines = append(lines, config.Filename+": unchanged")
		} else {
			lines = append(lines, config.Filename+": changes "+strings.Join(changed, ", "))
		}
	}

	// the steps after the write (B548): a cache Go cannot write like Node,
	// or a doctor row Go cannot give like Node, hands over now; the wizard
	// said its tool lines before its first prompt
	var after []string
	if v.tools == "recommended" && wiz != nil {
		after = initWizToolLines(ctx)
	}
	if v.refreshModels && !modelsCacheWritable(home) {
		return initSetupResult{}, hInitModels
	}
	var doctor *doctorQuick
	if v.doctor {
		var reason handoverReason
		if doctor, reason = initDoctorPlan(cwd, home, writeConfig, cfg, order); reason != 0 {
			return initSetupResult{}, reason
		}
	}
	var links []initLinkStep
	source, version := "", ""
	if len(v.harnesses) > 0 {
		root, ok := upgradePackageRoot()
		if !ok {
			return initSetupResult{}, hInitLinks
		}
		source = filepath.Join(root, "skill")
		if initExists(source) {
			if version, err = docsync.InstalledVersion(root); err != nil {
				return initSetupResult{}, hInitLinks
			}
			installs := installsFilePath(home)
			if _, ok := upgradeReadTargets(installs); !ok || !cacheFileWritable(installs) || !initOutputSafe(source) {
				return initSetupResult{}, hInitLinks
			}
			for _, id := range v.harnesses {
				var h installHarness
				for _, row := range installHarnesses {
					if row.id == id {
						h = row
					}
				}
				target := filepath.Join(home, h.homeMarker, "skills", "code-forge")
				if v.scope == "project" {
					target = filepath.Join(cwd, h.projectSkillsDir, "code-forge")
				}
				t := upgradeTarget{installRecord{target: target}, v.method}
				if _, ok := upgradeLinkable(source, t, home); !ok {
					return initSetupResult{}, hInitLinks
				}
				links = append(links, initLinkStep{id, target})
			}
		}
	}

	userOver := ledger.Object{
		{Key: "tools", Value: v.tools},
		{Key: "keys", Value: ledger.Object{{Key: "jev", Value: ledger.Object{{Key: "source", Value: jevSource}}}}},
	}
	if v.soloProject != nil {
		userOver = append(userOver, ledger.Field{Key: "solo", Value: ledger.Object{{Key: "project_id", Value: v.soloProject}}})
	}
	userBase := user
	if userBase == nil {
		userBase = ledger.Object{}
	}
	userNext := initMergeInto(userBase, userOver)
	userText := ""
	if len(initChangedPaths(userBase, userNext, "")) > 0 {
		if userText, err = yamledit.StringifyPlain(userNext); err != nil {
			return initSetupResult{}, hInitConfigShape
		}
	}
	engineStop := ctx.agent && ctx.harness != "claude" && !ctx.solo && (merged.Get("engine") == "auto" || merged.Get("engine") == "harness")

	return initSetupResult{
		home: home, jsonMode: jsonMode, lines: lines, shown: len(summaryLines), printed: printed, settings: settings, blank: blank,
		after: after, refreshModels: v.refreshModels, existingFile: existingFile, changeCount: changeCount,
		configFile: configFile, configText: text, writeConfig: writeConfig,
		links: links, source: source, version: version, scope: v.scope, method: v.method, harnessCount: len(v.harnesses),
		userFile: userFile, userText: userText, engineStop: engineStop, engine: merged.Get("engine"),
		pre: pre, secrets: secrets, tools: v.tools == "recommended" && wiz == nil, solo: ctx.solo, yesTools: initYesTools(parsed),
		doctor: doctor,
	}, 0
}

// initSetupResult is everything the run writes and says.
type initSetupResult struct {
	home     string
	jsonMode bool
	lines    []string
	shown    int
	printed  int
	after    []string // tool lines, said after the config write (wizard)
	// refreshModels: read the CLI model caches after the tools (wizard)
	refreshModels bool
	existingFile  bool
	changeCount   int
	code          int
	err           string
	settings      ledger.Object
	blank         []string
	configFile    string
	configText    string
	writeConfig   bool
	links         []initLinkStep
	source        string
	version       string
	scope         string
	method        string
	harnessCount  int
	userFile      string
	userText      string
	engineStop    bool
	engine        any
	// B548: the steps after the write
	pre      func()   // the key store's side effect, run first
	secrets  []string // the values Node registered for redaction
	tools    bool     // step 1: --tools recommended (no wizard)
	solo     bool
	yesTools []string
	doctor   *doctorQuick // step 9, nil with --skip-doctor
}

func initWriteText(file, text string) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o777); err != nil {
		return err
	}
	return os.WriteFile(file, []byte(text), 0o666)
}

// initSetupRun is runInit from the summary on: the writes in Node's order,
// then finish (the log, the JSON line or the text lines).
func initSetupRun(r initSetupResult) func(stdout, stderr io.Writer) int {
	return func(stdout, stderr io.Writer) int {
		if r.pre != nil {
			r.pre()
		}
		secrets := r.secrets
		// every line said after the summary passes through redact
		lines := append([]string{}, r.lines...)
		for i := r.shown; i < len(lines); i++ {
			lines[i] = redactSecret(lines[i], secrets...)
		}
		say := func(line string) { lines = append(lines, redactSecret(line, secrets...)) }
		var doctor any
		printed := r.printed
		if !r.jsonMode && r.shown > printed {
			_, _ = io.WriteString(stdout, strings.Join(lines[printed:r.shown], "\n")+"\n")
			printed = r.shown
		}
		wrote := []any{}
		harnesses := []any{}
		code, failure := r.code, r.err
		if failure == "" {
			step := func() (err error) {
				defer func() {
					if p := recover(); p != nil {
						err = fmt.Errorf("%v", p)
					}
				}()
				if r.writeConfig {
					if err := initWriteText(r.configFile, r.configText); err != nil {
						return err
					}
					wrote = append(wrote, r.configFile)
					say(config.Filename + ": written")
				}
				if r.tools {
					for _, line := range initToolLines(r.solo, r.yesTools) {
						say(line)
					}
				}
				for _, line := range r.after {
					say(line)
				}
				if r.refreshModels {
					line, err := initModelsLine(r.home)
					if err != nil {
						return err
					}
					say(line)
				}
				if r.harnessCount > 0 && len(r.links) == 0 {
					say("links: skill source missing (" + r.source + "); no harness linked")
				}
				for _, l := range r.links {
					if err := upgradeLink(r.source, l.target, r.method); err != nil {
						return err
					}
					file := installsFilePath(r.home)
					rows := []any{}
					for _, rec := range readInstallRecords(file) {
						if !(rec.harness == l.id && rec.scope == r.scope) {
							rows = append(rows, rec.raw)
						}
					}
					rows = append(rows, ledger.Object{
						{Key: "harness", Value: l.id}, {Key: "scope", Value: r.scope}, {Key: "method", Value: r.method},
						{Key: "source", Value: r.source}, {Key: "target", Value: l.target},
						{Key: "linked_at", Value: initNow().UTC().Format("2006-01-02T15:04:05.000Z")}, {Key: "version", Value: r.version},
					})
					writeInstallsFile(file, rows)
					harnesses = append(harnesses, l.id)
					wrote = append(wrote, l.target)
					say("link " + l.id + ": " + r.method + " " + l.target)
				}
				if r.userText != "" {
					if err := initWriteText(r.userFile, r.userText); err != nil {
						return err
					}
					wrote = append(wrote, r.userFile)
				}
				return nil
			}()
			if step != nil {
				code, failure = 1, "unexpected error: "+step.Error()
			} else if r.engineStop {
				_, _ = io.WriteString(stderr, initNoEngineStop+"\n")
				say("engine: no Solo and no subagent tool in this harness (see the stop text)")
			} else {
				engine, _ := ledger.Stringify(r.engine, false)
				if s, ok := r.engine.(string); ok {
					say("engine: " + s)
				} else {
					say("engine: " + string(engine))
				}
			}
		}
		if failure == "" && r.doctor != nil {
			rows, more := r.doctor.run()
			secrets = append(append([]string{}, secrets...), more...)
			for _, row := range rows {
				say("doctor " + row.status + " " + row.label + ": " + row.detail)
			}
			summary, ok := initDoctorCounts(rows)
			doctor = summary
			if !ok {
				code = 1
			}
		}
		if failure != "" {
			say("error: " + failure)
			_, _ = io.WriteString(stderr, redactSecret("init: "+failure+"\n", secrets...))
		}
		var log any
		file := filepath.Join(r.home, ".code-forge", "logs", initLogName(initNow()))
		if initWriteText(file, strings.Join(lines, "\n")+"\n") == nil {
			log = file
		}
		tail := lines
		if len(tail) > 10 {
			tail = tail[len(tail)-10:]
		}
		if r.jsonMode {
			var body ledger.Object
			if failure == "" {
				body = ledger.Object{
					{Key: "ok", Value: code == 0}, {Key: "wrote", Value: wrote}, {Key: "harnesses", Value: harnesses},
					{Key: "engine_stop", Value: r.engineStop}, {Key: "doctor", Value: doctor}, {Key: "settings", Value: r.settings},
					{Key: "blank", Value: initAnyList(r.blank)}, {Key: "log", Value: log}, {Key: "log_tail", Value: initAnyList(tail)},
				}
			} else {
				body = ledger.Object{
					{Key: "ok", Value: false}, {Key: "error", Value: failure}, {Key: "wrote", Value: wrote},
					{Key: "log", Value: log}, {Key: "log_tail", Value: initAnyList(tail)},
				}
			}
			data, _ := ledger.Stringify(body, false)
			_, _ = io.WriteString(stdout, redactSecret(string(data), secrets...)+"\n")
		} else if failure == "" {
			rest := lines[printed:]
			out := ""
			if len(rest) > 0 {
				out = strings.Join(rest, "\n") + "\n"
			}
			logText := "null"
			if log != nil {
				logText = file
			}
			_, _ = io.WriteString(stdout, out+"log: "+logText+"\n")
		}
		return code
	}
}
