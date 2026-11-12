package main

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/jev"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/session"
)

// B550: `code-forge jev ask` (src/cli/jev.mjs) in Go: the --rules answer,
// and the Jev answer with the key from the environment or the 0600 file
// store (CODE_FORGE_KEY_BACKEND=file). The plan step reads the state file,
// the config and the key and builds the payload without a side effect; a
// case Go does not serve byte-equal hands the whole call to Node from there.

// Seams: tests point the Jev client at a local fake server and fix the
// clock and the decision id.
var (
	b550JevOpts = jev.Opts{}
	b550JevNow  = func() float64 { return float64(time.Now().UnixMilli()) }
	b550JevID   = blockUUID
)

type b550JevQuestion struct {
	kind, instructions string
	criteria           ledger.Object
}

// b550JevQuestions is QUESTIONS in src/decide/questions.mjs (wire fields only).
var b550JevQuestions = map[string]b550JevQuestion{
	"lane": {"choice", "Which is the lowest model level that can reliably complete `block.task` within `block.owned_files`?", ledger.Object{
		{Key: "L0", Value: "trivial"},
		{Key: "L1", Value: "plain feature, following an existing pattern"},
		{Key: "L2", Value: "hard (money, dates, tenancy, migrations, concurrency, unknown failure)"},
		{Key: "split", Value: "the block is too large or mixes two concerns — re-decompose before dispatch"}}},
	"risk": {"score", "How much damage could a subtle mistake here cause in production?", ledger.Object{
		{Key: "0", Value: "cosmetic"},
		{Key: "1", Value: "wrong behaviour, easy to notice and revert"},
		{Key: "2", Value: "silent wrong data for some users"},
		{Key: "3", Value: "silent wrong money or cross-tenant leak"}}},
	"security_sensitive": {"noul", "Does this change touch authentication, authorization, tenancy boundaries, secrets, money movement or webhook verification?", b550YesNo},
	"next": {"choice", "Given `evidence` from deterministic checks after a coding attempt, what should the orchestrator do next?", ledger.Object{
		{Key: "complete", Value: "all green, tests added or no_new_tests_reason given"},
		{Key: "retry", Value: "contained failure the same coder can fix in one more attempt"},
		{Key: "escalate", Value: "repeated or spreading failures, or risky area"},
		{Key: "stop", Value: "the evidence contradicts the brief (wrong base, missing dependency, acceptance impossible)"}}},
	"scope":    {"noul", "Do these hunks contain a change unrelated to `acceptance`?", b550YesNo},
	"defect":   {"noul", "Is `finding` a real defect in `file_diff_hunk` that must be fixed before merge (not a style preference)?", ledger.Object{{Key: "true", Value: "real correctness, safety or rule-violation defect"}, {Key: "false", Value: "style, naming or preference only"}}},
	"resolved": {"noul", "Does `fix_hunk` resolve `finding` without introducing a new problem in the shown lines?", b550YesNo},
}

var b550YesNo = ledger.Object{{Key: "true", Value: "yes"}, {Key: "false", Value: "no"}}

// b550JevCall is one checked `jev ask` call.
type b550JevCall struct {
	id, slug, key, warning string
	early                  string // printed before anything else
	earlyCode              int
	state                  any
	scope                  ledger.Object
	rules                  any // the --rules answer, nil for a Jev call
	questions              ledger.Object
	bands                  [3]float64 // act, check, close_margin
}

func runJevVerb(args []string) int {
	if metaHelp(args, jevUsage, map[string]string{"ask": jevUsage}) {
		return 0
	}
	refusal := jevAskUsage(args)
	if refusal == "" {
		if h := os.Getenv("HOME"); h == "" || !filepath.IsAbs(h) {
			return metaHandover("jev", args, hJevAskHome)
		}
		call, reason := b550JevPlan(args)
		if call == nil {
			return metaHandover("jev", args, reason)
		}
		late := handoverReason(0)
		code := metaServe("jev", metaSub(args, "ask"), args, func(stdout, stderr io.Writer) int {
			var code int
			code, late = call.run(stdout, stderr)
			return code
		})
		if late != 0 {
			return metaHandover("jev", args, late)
		}
		return code
	}
	return metaServe("jev", metaSub(args, "ask"), args, func(_, stderr io.Writer) int {
		fmt.Fprint(stderr, refusal)
		return 2
	})
}

// b550JevPlan reads and checks everything runJev reads before it asks Jev.
func b550JevPlan(args []string) (*b550JevCall, handoverReason) {
	call := &b550JevCall{id: args[1]}
	values, rules := map[string]string{}, false
	for i := 2; i < len(args); i++ {
		if args[i] == "--rules" {
			rules = true
			continue
		}
		values[args[i]] = args[i+1]
		i++
	}
	if block, ok := values["--block"]; ok {
		call.scope = append(call.scope, ledger.Field{Key: "block", Value: block})
	}
	if plan, ok := values["--plan"]; ok {
		call.scope = append(call.scope, ledger.Field{Key: "plan", Value: b550NodeBasename(plan)})
	}
	cwd, given := values["--cwd"]
	if !given {
		wd, err := os.Getwd()
		if err != nil {
			return nil, hJevAskConfig
		}
		cwd = wd
	} else if !filepath.IsAbs(cwd) {
		return nil, hJevAskConfig // projectRootFor keeps a relative spelling
	}
	data, err := os.ReadFile(spawnResolve(cwd, values["--state"]))
	if err != nil {
		for code, errno := range map[string]syscall.Errno{"ENOENT": syscall.ENOENT, "EISDIR": syscall.EISDIR, "EACCES": syscall.EACCES, "ENOTDIR": syscall.ENOTDIR} {
			if errors.Is(err, errno) {
				call.early, call.earlyCode = "jev ask: could not read --state file: "+code+"\n", 1
				return call, 0
			}
		}
		return nil, hJevAskState
	}
	if !utf8.Valid(data) || b550LoneSurrogate.Match(data) {
		return nil, hJevAskState
	}
	state, err := ledger.DecodeValue(data)
	if err != nil {
		return nil, hJevAskState // JSON.parse's message is V8's
	}
	call.state = state
	root := config.ProjectRootFor(cwd)
	cfg, ordered, err := config.LoadFileOrdered(filepath.Join(root, config.Filename))
	if err != nil {
		var cfgErr *config.Error
		if !errors.As(err, &cfgErr) || runYAMLSyntax(cfgErr) {
			return nil, hJevAskConfig // a YAML syntax error is worded by Node's parser
		}
		if cfgErr.Kind != "not-found" {
			call.early, call.earlyCode = "jev ask: "+cfgErr.Message+"\n", 1
			return call, 0
		}
		cfg = map[string]any{}
	}
	call.slug = values["--slug"]
	if _, ok := values["--slug"]; !ok {
		call.slug = session.SlugFor(cfg, root)
	}
	if !ledgerSlugPattern.MatchString(call.slug) {
		return nil, hJevAskConfig // appendRow throws
	}
	system1, _ := cfg["system1"].(map[string]any)
	if disabled, ok := system1["disable"].([]any); ok {
		for _, item := range disabled {
			if item == call.id {
				call.early, call.earlyCode = `jev ask: question "`+call.id+`" is disabled by system1.disable`+"\n", 1
				return call, 0
			}
		}
	}
	if rules {
		return b550JevRules(call, state)
	}
	if !b550JevKey(call, cfg, values) {
		return nil, hJevAskKey
	}
	if call.early != "" {
		return call, 0
	}
	if _, isObject := state.(ledger.Object); !isObject {
		return nil, hJevAskState // askJev throws a TypeError (a stack trace)
	}
	if !b550JevPayload(call, cfg, system1, ordered) {
		return nil, hJevAskPayload
	}
	if !b550JevBands(call, cfg) {
		return nil, hJevAskConfig
	}
	return call, 0
}

// b550Has: the object has the key (JSON.parse keeps a null value).
func b550Has(o ledger.Object, key string) bool {
	for _, field := range o {
		if field.Key == key {
			return true
		}
	}
	return false
}

// b550NodeBasename is path.basename (trailing slashes dropped).
func b550NodeBasename(p string) string {
	trimmed := strings.TrimRight(p, "/")
	if trimmed == "" {
		return ""
	}
	return jsBasename(trimmed)
}

// b550JevRules is the --rules branch: badRuleFacts, then the fallback rules.
func b550JevRules(call *b550JevCall, state any) (*b550JevCall, handoverReason) {
	names := []string{"filesChanged", "linesAdded", "touchesMigration", "touchesPolicyOrMiddleware", "pathFloorHit", "keywordsFound"}
	facts, isObject := state.(ledger.Object)
	var bad []string
	for i, name := range names {
		if !isObject {
			bad = names
			break
		}
		v := facts.Get(name)
		switch {
		case i < 2:
			n, ok := v.(float64)
			if !ok || math.IsInf(n, 0) || math.IsNaN(n) || n < 0 {
				bad = append(bad, name)
			}
		case i < 5:
			if _, ok := v.(bool); !ok {
				bad = append(bad, name)
			}
		default:
			list, ok := v.([]any)
			for _, item := range list {
				if _, isString := item.(string); !isString {
					ok = false
				}
			}
			if !ok {
				bad = append(bad, name)
			}
		}
	}
	if len(bad) > 0 {
		call.early = "jev ask: --rules needs the fallback facts in the --state object; missing or wrong type: " + strings.Join(bad, ", ") + "\n"
		call.earlyCode = 2
		return call, 0
	}
	files, lines := facts.Get("filesChanged").(float64), facts.Get("linesAdded").(float64)
	migration, policy, floor := facts.Get("touchesMigration").(bool), facts.Get("touchesPolicyOrMiddleware").(bool), facts.Get("pathFloorHit").(bool)
	big := files > 4 || lines > 200
	switch call.id {
	case "lane":
		call.rules = "L0"
		if migration || policy || floor {
			call.rules = "L2"
		} else if big {
			call.rules = "L1"
		}
	case "risk":
		risk := 0.0
		switch {
		case floor:
			risk = 3
		case migration || policy:
			risk = 2
		case big:
			risk = 1
		}
		call.rules = risk
	default:
		sensitive := floor
		for _, k := range facts.Get("keywordsFound").([]any) {
			sensitive = sensitive || k == "Auth" || k == "Webhook" || k == "Money"
		}
		call.rules = sensitive
	}
	return call, 0
}

// b550JevKey is cacheHoursFrom and resolveKey('jev') for the cases Go
// serves: the env variable, or a live entry of the file store. false hands
// over (the keychain, a 1Password read, an expired or too-long entry that
// resolveKey rewrites or deletes).
func b550JevKey(call *b550JevCall, cfg map[string]any, values map[string]string) bool {
	if keys, ok := cfg["keys"].(map[string]any); ok {
		if raw, present := keys["cache_hours"]; present {
			if _, valid := keysIsCacheHours(raw); !valid {
				call.warning = "WARN jev ask: keys.cache_hours must be a whole number of hours from 1 to 24; caching for the default 8 h\n"
			}
		}
	}
	hours := 8.0
	if keys, ok := cfg["keys"].(map[string]any); ok {
		if n, valid := keysIsCacheHours(keys["cache_hours"]); valid {
			hours = float64(n)
		}
	}
	ref, hasRef := values["--key-ref"]
	if !hasRef {
		if keys, ok := cfg["keys"].(map[string]any); ok && keys["jev"] != nil {
			text, isString := keys["jev"].(string)
			if !isString {
				return false // ref?.startsWith is not a function
			}
			ref, hasRef = text, true
		}
	}
	envName, storeName, opRef := "CODE_FORGE_KEY_JEV", "jev", ""
	if hasRef && strings.HasPrefix(ref, "env:") {
		envName = ref[4:]
	}
	if hasRef && strings.HasPrefix(ref, "keychain:") {
		storeName = ref[9:]
		if !keyNamePattern.MatchString(storeName) {
			call.early, call.earlyCode = call.warning+"jev ask: key name must match /^[a-z][a-z0-9_-]{0,63}$/\n", 2
			return true
		}
	}
	if hasRef && strings.HasPrefix(ref, "op://") {
		if !opRefPattern.MatchString(ref) {
			call.early, call.earlyCode = call.warning+"jev ask: key \"jev\": malformed 1Password reference\n", 2
			return true
		}
		opRef = ref
	}
	if value, ok := os.LookupEnv(envName); ok && value != "" {
		call.key = value
		return true
	}
	if os.Getenv("CODE_FORGE_KEY_BACKEND") != "file" {
		return false
	}
	raw, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".code-forge", "store", storeName+".key"))
	text := ""
	switch {
	case err == nil:
		if !utf8.Valid(raw) {
			return false
		}
		text = strings.TrimSuffix(string(raw), "\n")
	case errors.Is(err, os.ErrNotExist):
	default:
		return false // a read failure is a reason line
	}
	if text != "" {
		value, exp, ok := decodeKeyEntry(text)
		now := b550JevNow()
		if !ok || value == "" || (exp != nil && (*exp <= now || *exp > now+hours*3_600_000)) {
			return false
		}
		call.key = value
		return true
	}
	if opRef != "" {
		return false
	}
	call.early, call.earlyCode = call.warning+"jev ask: no Jev key resolved (set one with \"code-forge keys set jev\")\n", 1
	return true
}

// b550JevPayload is buildQuestionPayload([id], cfg) with toWire.
func b550JevPayload(call *b550JevCall, cfg, system1 map[string]any, ordered config.Ordered) bool {
	spec := b550JevQuestions[call.id]
	instructions, criteria := spec.instructions, spec.criteria.Clone()
	extras, _ := system1["criteria_extra"].(map[string]any)
	if extra, ok := extras[call.id].(map[string]any); ok {
		if text, ok := extra["instructions"].(string); ok && text != "" {
			instructions = text
		}
		switch more := extra["criteria"].(type) {
		case nil, bool, float64, int, int64:
		case map[string]any:
			object, _ := ordered.Object(more).(ledger.Object)
			if object == nil {
				return false
			}
			for _, field := range object {
				criteria.Set(field.Key, field.Value)
			}
		default:
			return false // a spread string or list
		}
	}
	var wire any = criteria
	if spec.kind == "score" {
		list := make([]any, len(criteria))
		for _, field := range criteria {
			n, ok := arrayIndexKey(field.Key)
			if !ok || n >= uint64(len(criteria)) {
				return false // toWire throws
			}
			list[n] = field.Value
		}
		wire = list
	}
	call.questions = ledger.Object{{Key: call.id, Value: ledger.Object{{Key: "type", Value: spec.kind}, {Key: "instructions", Value: instructions}, {Key: "criteria", Value: wire}}}}
	return true
}

// arrayIndexKey: a canonical array index key (JS enumerates these first).
func arrayIndexKey(key string) (uint64, bool) {
	n, err := strconv.ParseUint(key, 10, 32)
	return n, err == nil && n < 4294967295 && strconv.FormatUint(n, 10) == key
}

// b550JevBands is bandsFor(id, cfg); false where a band is not a number.
func b550JevBands(call *b550JevCall, cfg map[string]any) bool {
	thresholds, _ := cfg["thresholds"].(map[string]any)
	per, _ := thresholds[call.id].(map[string]any)
	dflt, _ := thresholds["default"].(map[string]any)
	for i, name := range []string{"act", "check", "close_margin"} {
		call.bands[i] = []float64{0.9, 0.6, 0.15}[i]
		for _, m := range []map[string]any{per, dflt} {
			if v, present := m[name]; present && v != nil {
				n, ok := v.(float64)
				if !ok {
					return false
				}
				call.bands[i] = n
				break
			}
		}
	}
	return true
}

// b550JSEntries is Object.entries order: array index keys first, ascending.
func b550JSEntries(o ledger.Object) ledger.Object {
	fields := o.Clone()
	sort.SliceStable(fields, func(i, j int) bool {
		left, a := arrayIndexKey(fields[i].Key)
		right, b := arrayIndexKey(fields[j].Key)
		if a != b {
			return a
		}
		return a && left < right
	})
	return fields
}

// b550JevDecide is computeConfidenceMargin plus decide's stage; ok=false
// where Node's decide throws.
func b550JevDecide(answer any, bands [3]float64) (ledger.Object, bool) {
	obj, ok := answer.(ledger.Object)
	if !ok {
		return nil, false
	}
	var confidence, margin float64
	var value any
	kind, _ := obj.Get("type").(string)
	switch kind {
	case "choice", "score":
		probabilities, ok := obj.Get("probabilities").(ledger.Object)
		if !ok || len(probabilities) == 0 {
			return nil, false
		}
		entries := b550JSEntries(probabilities)
		values := make([]float64, len(entries))
		best, bestKey := math.Inf(-1), ""
		for i, field := range entries {
			n, ok := field.Value.(float64)
			if !ok || math.IsInf(n, 0) || math.IsNaN(n) {
				return nil, false
			}
			values[i] = n
			if n > best {
				best, bestKey = n, field.Key
			}
		}
		sort.SliceStable(values, func(i, j int) bool { return values[i] > values[j] })
		top2 := 0.0
		if len(values) > 1 {
			top2 = values[1]
		}
		confidence, margin, value = values[0], values[0]-top2, bestKey
		if kind == "score" {
			score, ok := obj.Get("score").(float64)
			if !ok || math.IsInf(score, 0) || math.IsNaN(score) {
				return nil, false
			}
			value = score
		}
	case "noul":
		p, ok := obj.Get("noul").(float64)
		if !ok || math.IsNaN(p) || p < 0 || p > 1 {
			return nil, false
		}
		confidence, margin, value = math.Max(p, 1-p), math.Abs(float64(2*p)-1), p >= 0.5
	default:
		return nil, false
	}
	stage := "decide"
	if confidence >= bands[0] && margin >= bands[2] {
		stage = "act"
	} else if confidence >= bands[1] {
		stage = "check"
	}
	return ledger.Object{{Key: "decided", Value: stage == "act"}, {Key: "stage", Value: stage}, {Key: "value", Value: value},
		{Key: "confidence", Value: confidence}, {Key: "margin", Value: margin}}, true
}

// run is runJev from the state file on. late != 0 hands the call to Node
// (an answer Node's decide throws on: nothing is written by then).
func (call *b550JevCall) run(stdout, stderr io.Writer) (int, handoverReason) {
	redact := func(s string) string {
		if call.key == "" {
			return s
		}
		return redactSecret(s, call.key)
	}
	if call.early != "" {
		fmt.Fprint(stderr, call.early)
		return call.earlyCode, 0
	}
	ledgerDir := filepath.Join(os.Getenv("HOME"), ".code-forge", "ledger")
	write := func(row ledger.Object, out ledger.Object) int {
		if _, err := ledger.AppendObject(ledgerDir, call.slug, row, ledger.DefaultRotateAtBytes); err != nil {
			fmt.Fprintf(stderr, "code-forge: verb %q failed: Error: %s\n", "jev", redact(err.Error()))
			return 1
		}
		data, err := ledger.Stringify(out, true)
		if err != nil {
			fmt.Fprintf(stderr, "code-forge: verb %q failed: Error: %s\n", "jev", redact(err.Error()))
			return 1
		}
		fmt.Fprint(stdout, redact(string(data)+"\n"))
		return 0
	}
	if call.rules != nil {
		id, err := b550JevID()
		if err != nil {
			fmt.Fprintf(stderr, "code-forge: verb %q failed: Error: %s\n", "jev", err.Error())
			return 1, 0
		}
		row := ledger.Object{{Key: "event", Value: "decision"}, {Key: "decision_id", Value: id}, {Key: "question", Value: call.id}, {Key: "answer", Value: call.rules}, {Key: "source", Value: "rules"}}
		out := ledger.Object{{Key: "decision_id", Value: id}, {Key: "question", Value: call.id}, {Key: "answer", Value: call.rules}, {Key: "source", Value: "rules"}}
		return write(append(row, call.scope...), append(out, call.scope...)), 0
	}
	fmt.Fprint(stderr, call.warning)
	o := b550JevOpts
	o.State, o.Questions, o.Key = call.state, call.questions, call.key
	res, err := jev.Ask(o)
	if err != nil {
		return 0, hJevAskPayload
	}
	if !res.OK {
		line := "jev ask: request failed (" + res.Kind
		if res.Status != 0 {
			line += ", status " + strconv.Itoa(res.Status)
		}
		fmt.Fprint(stderr, redact(line+")\n"))
		return 1, 0
	}
	body, err := ledger.DecodeValue(res.Raw)
	if err != nil {
		return 0, hJevAskAnswer
	}
	bodyObject, _ := body.(ledger.Object)
	answers, _ := bodyObject.Get("answers").(ledger.Object)
	answer := answers.Get(call.id)
	if !jsTruthy(answer, b550Has(answers, call.id)) {
		fmt.Fprint(stderr, `jev ask: Jev did not answer question "`+call.id+`"`+"\n")
		return 1, 0
	}
	decision, ok := b550JevDecide(answer, call.bands)
	if !ok {
		return 0, hJevAskAnswer
	}
	id, err := b550JevID()
	if err != nil {
		fmt.Fprintf(stderr, "code-forge: verb %q failed: Error: %s\n", "jev", err.Error())
		return 1, 0
	}
	row := ledger.Object{{Key: "event", Value: "decision"}, {Key: "decision_id", Value: id}, {Key: "question", Value: call.id},
		{Key: "answer", Value: decision.Get("value")}, {Key: "stage", Value: decision.Get("stage")}, {Key: "confidence", Value: decision.Get("confidence")},
		{Key: "margin", Value: decision.Get("margin")}, {Key: "source", Value: "jev"}}
	row = append(row, call.scope...)
	usage, _ := bodyObject.Get("usage").(ledger.Object)
	for _, pair := range [][2]string{{"tokens_in", "input_tokens"}, {"tokens_out", "output_tokens"}} {
		if b550Has(usage, pair[1]) {
			row = append(row, ledger.Field{Key: pair[0], Value: usage.Get(pair[1])})
		}
	}
	out := ledger.Object{{Key: "decision_id", Value: id}, {Key: "question", Value: call.id}, {Key: "answer", Value: answer}, {Key: "decision", Value: decision}}
	return write(row, out), 0
}
