package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/session"
)

// B536: the delegate session of `autopilot ask` in Go (askDelegate in
// src/autopilot/delegate.mjs after grantFor lets the question through, and
// runAsk's output in src/cli/autopilot.mjs). A case Go cannot run the way
// Node does is found before anything is written and hands over
// (autopilot-ask): a run without a config snapshot (Node falls back to the
// project config), a tmp.root that is not a string, a grant whose scopes or
// delegate level are not text, a context file that is not UTF-8, and a
// session the Go spawner refuses (session.IsUnsupported).

const (
	apdMaxPromptTokens   = 4000
	apdReasonMax         = 400
	apdQuestionMaxBytes  = 1000
	apdDefaultConfidence = 0.7
)

// apdLens is DELEGATE_LENS.
const apdLens = "You are the autopilot delegate for code-forge: you answer one question for the project owner while they are away, " +
	"and only inside the scope the owner granted. You have no tools and no files; everything you may use is in the message. " +
	"Answer once, with a JSON object that matches the schema. Never invent facts that are not in the message. " +
	"When the question is outside the scope, touches anything on the deny list, or you are unsure, set escalate to true."

// apdHeader is delegate.mjs HEADER.
var apdHeader = strings.Join([]string{
	"# Autopilot delegate decision",
	"",
	"The owner is away and granted you the scopes below until a fixed time. Decide the question from the",
	"material given. Reply with ONE JSON object:",
	`{"decision": string (exactly one of the options when options are listed), "within_scope": boolean`,
	` (true only when answering decides nothing beyond the scope asked), "confidence": number 0..1,`,
	` "reason": string of at most ` + strconv.Itoa(apdReasonMax) + ` characters, "escalate": boolean (true hands the question back to the owner)}.`,
	"",
}, "\n")

// apdAllowableWhat is ALLOWABLE_WHAT (src/autopilot/scopes.mjs).
var apdAllowableWhat = map[string]string{
	"waive:warning": "waive a warning finding",
	"waive:nit":     "waive a nit finding",
	"round:extra":   "one fix round past the cap for a file",
	"model:choose":  "choose the coder level for a block",
	"close:routine": "close a routine block",
	// B630
	"review:substitute": "run a substitute review on another provider when a file's review stays unavailable",
}

// apdTimeoutMs is the ask's session timeout (0 = spawn.mjs's default, as the
// CLI passes none); tests shorten it.
var apdTimeoutMs float64

// apdSessionBins replaces a CLI's argv[0] (tests only; nil = PATH).
var apdSessionBins map[string]string

// apdAsk is what the plan settled before any write.
type apdAsk struct {
	scope, question, context string
	options                  []string
	subject                  ledger.Object // nil = no subject
	cfg                      map[string]any
	tmpRoot                  *string
	slug                     string
}

// apdScrub is delegate.mjs scrub: registered secrets, then secret-shaped tokens.
func apdScrub(text string) string { return bnMaskSecrets(autopilotRedact(text)) }

// apdCutBytes is cutBytes: at most max UTF-8 bytes without splitting a character.
func apdCutBytes(text string, max int) string {
	if len(text) <= max {
		return text
	}
	cut := max
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && text[cut]&0xc0 == 0x80 {
		cut--
	}
	return text[:cut]
}

// apdPlan settles the ask Node would run a session for; ok false = hand over.
func apdPlan(record ledger.Object, grant ledger.Object, flags autopilotFlags, scope string) (apdAsk, error, bool) {
	ask := apdAsk{scope: scope}
	scopes, isList := grant.Get("scopes").([]any)
	if !isList {
		return ask, nil, false
	}
	for _, s := range scopes {
		if _, ok := s.(string); !ok {
			return ask, nil, false
		}
	}
	if level, ok := grant.Get("delegate").(string); !ok || (level != "L2" && level != "L3") {
		return ask, nil, false
	}
	if _, ok := grant.Get("grant_id").(string); !ok {
		return ask, nil, false
	}
	if file, ok := flags.values["context-file"]; ok {
		at := file
		if !filepath.IsAbs(at) {
			cwd, err := os.Getwd()
			if err != nil {
				return ask, nil, false
			}
			at = filepath.Join(cwd, at)
		}
		data, err := os.ReadFile(at)
		if err != nil || !utf8.Valid(data) {
			return ask, nil, false
		}
		ask.context = apdScrub(string(data))
	}
	snapshot, hasSnapshot, valid := sweepConfigInForce(record)
	if !hasSnapshot {
		return ask, nil, false // runConfig falls back to the project config: Node's
	}
	if !valid {
		runID, _ := record.Get("run_id").(string)
		return ask, &runFail{"run " + runID + "'s config snapshot does not match its hash", false}, true
	}
	cfg, _ := runDeepMap(snapshot).(map[string]any)
	ask.cfg = cfg
	if tmp, ok := cfg["tmp"].(map[string]any); ok {
		if value, present := tmp["root"]; present && value != nil {
			text, isString := value.(string)
			if !isString {
				return ask, nil, false
			}
			ask.tmpRoot = &text
		}
	} else if value, present := cfg["tmp"]; present && value != nil {
		if _, isMap := value.(map[string]any); !isMap {
			return ask, nil, false
		}
	}
	ask.slug, _ = record.Get("project").(string)
	ask.question = apdScrub(autopilotJSTrim(flags.values["question"]))
	ask.options = []string{}
	for _, option := range autopilotItems(flags.values["options"]) {
		option = apdScrub(option)
		if !apdHas(ask.options, option) {
			ask.options = append(ask.options, option)
		}
	}
	for _, name := range []string{"block", "file", "finding"} {
		if value, ok := flags.values[name]; ok {
			if name == "file" {
				value = apdNormalize(value)
			}
			ask.subject = append(ask.subject, ledger.Field{Key: name, Value: value})
		}
	}
	level, _ := grant.Get("delegate").(string)
	check := session.Opts{Cfg: cfg, Level: level, Role: "delegate", PromptPath: "/packet.md", Schema: session.DelegateSchema(),
		SystemPrompt: ptrString(apdLens), Validate: session.MatchesDelegateSchema, TimeoutMs: apdTimeoutMs}
	if err := session.Check(check); session.IsUnsupported(err) {
		return ask, nil, false
	}
	return ask, nil, true
}

func ptrString(s string) *string { return &s }

func apdHas(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// apdNormalize is path.posix.normalize for a file autopilotSubjectFile let through.
func apdNormalize(file string) string {
	normalized := filepath.ToSlash(filepath.Clean(file))
	if strings.HasSuffix(file, "/") {
		normalized += "/"
	}
	return normalized
}

// apdPrompt is buildDelegatePrompt; a non-nil error is SessionError delegate-too-large.
func apdPrompt(ask apdAsk, grantScopes []any) (string, error) {
	what := func(s string) string {
		if w, ok := apdAllowableWhat[s]; ok {
			return w
		}
		return s
	}
	var allowed []string
	for _, s := range grantScopes {
		text := s.(string)
		allowed = append(allowed, "- "+text+": "+what(text))
	}
	var deny []string
	for _, f := range autopilotFixedDeny {
		deny = append(deny, "- "+f.Key+": "+f.Value.(string))
	}
	fixed := apdHeader + "## Scope asked\n" + ask.scope + ": " + what(ask.scope) + "\n" +
		"\n## Scopes the owner granted\n" + strings.Join(allowed, "\n") + "\n" +
		"\n## Never allowed (fixed deny list, enforced in code; escalate anything that touches these)\n" + strings.Join(deny, "\n") + "\n" +
		"\n## Question\n" + autopilotJSTrim(ask.question) + "\n"
	if ask.subject != nil {
		var parts []string
		for _, f := range ask.subject {
			parts = append(parts, f.Key+" "+f.Value.(string))
		}
		fixed += "\n## Subject\n" + strings.Join(parts, " · ") + "\n"
	}
	if len(ask.options) > 0 {
		var lines []string
		for _, o := range ask.options {
			lines = append(lines, "- "+o)
		}
		fixed += "\n## Options\n" + strings.Join(lines, "\n") + "\n"
	}
	maxBytes := apdMaxPromptTokens * 4
	if len(fixed) > maxBytes {
		tokens := int(math.Ceil(float64(len(fixed)) / 4))
		return "", &session.Error{Code: "delegate-too-large", Message: "delegate: the question part is " + strconv.Itoa(tokens) + " tokens (max " + strconv.Itoa(apdMaxPromptTokens) + ")"}
	}
	if ask.context == "" {
		return fixed, nil
	}
	heading := "\n## Context\n"
	whole := heading + ask.context + "\n"
	if len(fixed)+len(whole) <= maxBytes {
		return fixed + whole, nil
	}
	note := "\n[context cut: " + strconv.Itoa(len(ask.context)) + " bytes given]\n"
	room := maxBytes - len(fixed) - len(heading) - len(note)
	if room <= 0 {
		return fixed, nil
	}
	return fixed + heading + apdCutBytes(ask.context, room) + note, nil
}

// apdMinConfidence is minConfidenceOf.
func apdMinConfidence(cfg map[string]any) float64 {
	if section, ok := cfg["autopilot"].(map[string]any); ok {
		if v, ok := section["min_confidence"].(float64); ok && !math.IsInf(v, 0) && !math.IsNaN(v) && v >= 0 && v <= 1 {
			return v
		}
	}
	return apdDefaultConfidence
}

// apdOwnerReason is ownerReason ("" = it may act).
func apdOwnerReason(answer ledger.Object, options []string, min float64) string {
	if len(options) == 0 {
		return "no-options"
	}
	if answer.Get("within_scope") != true {
		return "outside-scope"
	}
	if answer.Get("escalate") != false {
		return "escalated"
	}
	if c, _ := answer.Get("confidence").(float64); !(c >= min) {
		return "low-confidence"
	}
	decision, _ := answer.Get("decision").(string)
	if !apdHas(options, decision) {
		return "not-an-option"
	}
	return ""
}

// apdSession runs the session: (answer, failure, provider, model, handover).
func apdSession(o *runOut, home, runID string, ask apdAsk, grant ledger.Object) (ledger.Object, string, any, any, bool) {
	scopes, _ := grant.Get("scopes").([]any)
	level, _ := grant.Get("delegate").(string)
	var dir string
	defer func() {
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
	}()
	prompt, err := apdPrompt(ask, scopes)
	if err != nil {
		return nil, apdFailureOf(err), nil, nil, false
	}
	if ask.tmpRoot == nil {
		contextApplyTmpRoot()
	}
	root, err := contextRunRoot(runID, ask.tmpRoot)
	if err != nil {
		return nil, "internal-error", nil, nil, false
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return nil, "internal-error", nil, nil, false
	}
	at := filepath.Join(root, "delegate", strconv.FormatInt(time.Now().UnixMilli(), 10)+"-"+hex.EncodeToString(suffix))
	if err := os.MkdirAll(at, 0o700); err != nil {
		return nil, "internal-error", nil, nil, false
	}
	dir = at
	packet := filepath.Join(at, "packet.md")
	if err := os.WriteFile(packet, []byte(prompt), 0o600); err != nil {
		return nil, "internal-error", nil, nil, false
	}
	run := runID
	grantID, _ := grant.Get("grant_id").(string)
	opts := session.Opts{Cfg: ask.cfg, Level: level, Role: "delegate", PromptPath: packet, Schema: session.DelegateSchema(),
		SystemPrompt: ptrString(apdLens), Validate: session.MatchesDelegateSchema, TimeoutMs: apdTimeoutMs,
		RunRoot: root, Run: &run, Slug: ask.slug, RowExtra: ledger.Object{{Key: "grant_id", Value: grantID}}}
	var procRoot string
	lazyRoot := func() (string, error) {
		if procRoot != "" {
			return procRoot, nil
		}
		made, err := contextRunRoot("proc-"+strconv.Itoa(os.Getpid())+"-"+strconv.FormatInt(time.Now().UnixMilli(), 10), nil)
		procRoot = made
		return made, err
	}
	defer func() {
		if procRoot != "" {
			spawnReleaseRoot(procRoot)
		}
	}()
	childRun, hasRunID := os.LookupEnv("CODE_FORGE_RUN_ID")
	if !hasRunID {
		childRun = runID
	}
	deps := session.Deps{Stderr: spawnStderr{o}, RunID: childRun, Warn: spawnLogWarning, RunRoot: lazyRoot, PidsRoot: lazyRoot, Home: home, Bins: apdSessionBins}
	res, err := session.Spawn(opts, deps)
	if session.IsUnsupported(err) {
		return nil, "", nil, nil, true
	}
	if err != nil {
		return nil, apdFailureOf(err), nil, nil, false
	}
	provider, model := res.Get("provider"), res.Get("model")
	status, _ := res.Get("status").(string)
	answer, _ := res.Get("answer").(ledger.Object)
	switch {
	case status != "ok":
		failure := "session " + status
		if reason, _ := res.Get("reason").(string); reason != "" {
			failure += " (" + reason + ")"
		}
		return nil, failure, provider, model, false
	case !session.MatchesDelegateSchema(res.Get("answer")):
		return nil, "session invalid-output (answer does not match the delegate schema)", provider, model, false
	}
	reason, _ := answer.Get("reason").(string)
	if len(utf16.Encode([]rune(reason))) > apdReasonMax {
		return nil, "session invalid-output (reason is longer than " + strconv.Itoa(apdReasonMax) + " characters)", provider, model, false
	}
	return answer, "", provider, model, false
}

// apdFailureOf is askDelegate's catch: a SessionError keeps its code and message.
func apdFailureOf(err error) string {
	var sessionErr *session.Error
	if errors.As(err, &sessionErr) {
		return sessionErr.Code + ": " + sessionErr.Message
	}
	return "internal-error"
}

func apdOrNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// apdRun is askDelegate after grantFor lets the question through, then runAsk's output.
func apdRun(o *runOut, home, runID string, flags autopilotFlags, ask apdAsk, grant ledger.Object) int {
	answer, failure, provider, model, handover := apdSession(o, home, runID, ask, grant)
	if handover {
		autopilotLate = hAutopilotAsk // the session wrote nothing; Node runs the ask
		return 0
	}
	toOwner := "session-failed"
	if failure == "" {
		toOwner = apdOwnerReason(answer, ask.options, apdMinConfidence(ask.cfg))
		if toOwner == "" {
			// the session may have outlived the window or a stop: the grant is asked again
			again, err := autopilotCheckExpiry(home, runID, autopilotNow().Truncate(time.Millisecond))
			if err != nil {
				failure, toOwner = "internal-error", "internal-error"
			} else if why := autopilotAskRefusal(again, ask.scope); why != "" {
				toOwner = "grant-" + why
			}
		}
	}
	acted := toOwner == ""
	decisionID, err := autopilotDecisionID()
	if err != nil {
		return autopilotReport(o, "ask", err)
	}
	var subject any
	if ask.subject != nil {
		subject = ask.subject
	}
	options := make([]any, len(ask.options))
	for i, s := range ask.options {
		options[i] = s
	}
	var decision, withinScope, confidence, answerReason, escalate any
	if answer != nil {
		d, _ := answer.Get("decision").(string)
		r, _ := answer.Get("reason").(string)
		decision, answerReason = apdScrub(d), apdScrub(r)
		withinScope, confidence, escalate = answer.Get("within_scope"), answer.Get("confidence"), answer.Get("escalate")
	}
	var failureValue any
	if failure != "" {
		failureValue = apdScrub(failure)
	}
	row := ledger.Object{
		{Key: "event", Value: "autopilot.decision"}, {Key: "decision_id", Value: decisionID}, {Key: "grant_id", Value: grant.Get("grant_id")},
		{Key: "scope", Value: ask.scope}, {Key: "subject", Value: subject}, {Key: "question", Value: apdCutBytes(ask.question, apdQuestionMaxBytes)},
		{Key: "options", Value: options}, {Key: "decision", Value: decision}, {Key: "within_scope", Value: withinScope},
		{Key: "confidence", Value: confidence}, {Key: "reason", Value: answerReason}, {Key: "escalate", Value: escalate},
		{Key: "acted", Value: acted}, {Key: "to_owner", Value: !acted}, {Key: "owner_reason", Value: apdOrNil(toOwner)},
		{Key: "failure", Value: failureValue}, {Key: "level", Value: grant.Get("delegate")}, {Key: "provider", Value: provider},
		{Key: "model", Value: model}, {Key: "ts", Value: autopilotStamp(autopilotNow())},
	}
	if _, err := autopilotWriteSigned(home, runID, row); err != nil {
		return autopilotReport(o, "ask", err)
	}
	// B49a: the owner's files follow the decision; best effort
	_, _, _ = bnRefresh(home, runID, autopilotNow().Truncate(time.Millisecond))
	reason := failure
	if reason == "" {
		reason = toOwner
	}
	code := 3
	if acted {
		code = 0
	}
	if flags.bools["json"] {
		data, err := ledger.Stringify(ledger.Object{
			{Key: "run", Value: runID}, {Key: "scope", Value: flags.values["scope"]}, {Key: "grant_id", Value: grant.Get("grant_id")},
			{Key: "decision_id", Value: decisionID}, {Key: "answered", Value: answer != nil}, {Key: "acted", Value: acted},
			{Key: "to_owner", Value: !acted}, {Key: "reason", Value: apdOrNil(reason)}, {Key: "decision", Value: decision},
			{Key: "confidence", Value: confidence}, {Key: "within_scope", Value: withinScope}, {Key: "escalate", Value: escalate},
			{Key: "answer_reason", Value: answerReason},
		}, false)
		if err != nil {
			return autopilotReport(o, "ask", err)
		}
		o.out(string(data) + "\n")
		return code
	}
	head := "to the owner (" + reason + ")"
	if acted {
		head = "acted · decision " + decisionID
	}
	o.out("autopilot ask run " + runID + ": " + head + "\n")
	if answer != nil {
		o.out("  decision  " + decision.(string) + " · confidence " + jsNumber(confidence.(float64)) +
			" · within scope " + strconv.FormatBool(withinScope.(bool)) + " · escalate " + strconv.FormatBool(escalate.(bool)) +
			"\n  reason    " + answerReason.(string) + "\n")
	}
	return code
}
