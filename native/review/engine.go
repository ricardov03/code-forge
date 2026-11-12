package review

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ricardov03/code-forge/native/blockopen"
	"github.com/ricardov03/code-forge/native/filelock"
	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/ledger"
)

// Port of reviewFile (src/review/engine.mjs) for one ticket on the adaptive
// single-reviewer path: packet, one L2 session (quick or full, with B30's
// same-model timeout retry), stub guard, the review.plan / review.unavailable /
// review.session_timeout rows and Node's result object. Every case this file
// does not port is refused BEFORE any session is spawned (ErrUnsupported), so a
// caller can still hand the ticket to Node. Three cases are only known after a
// session ran; File stops there with a *GapError (see its doc).
// B514: a valid answer with needs_file gets Node's one attachFiles round, a
// schema miss its signed review.schema_invalid row with the diagnosis, and a
// second timeout of one model timeoutFallbackFor's one other model.

// SpawnOpts is what reviewFile passes to deps.spawn (schema is always
// finding.schema.json).
type SpawnOpts struct {
	Level      string
	Role       string
	PromptPath string
	RowExtra   ledger.Object
	Cfg        map[string]any
}

// Deps are reviewFile's seams.
type Deps struct {
	// Spawn runs one isolated session and returns its result object; an error
	// is a session that threw (an `exit` failure, never retried).
	Spawn func(SpawnOpts) (ledger.Object, error)
	// WriteRow signs and appends one ledger row; nil = no ledger writer. A
	// failed write never fails the review.
	WriteRow func(ledger.Object) error
	// Warn is the local error log's warning line (logWarning); nil = none.
	Warn func(warning, message string)
	// Rand names packet files; nil = crypto/rand.
	Rand io.Reader
	// ReadRows is the worker's ctx.readRows (every ledger row, for
	// timeoutFallbackFor); nil = no rows.
	ReadRows func() ([]any, error)
	// SchemaHistory is the worker's ledgerSchemaHistory for one packet at one
	// level and role; nil = none.
	SchemaHistory func(hash, level, role string) (failures int, fellBack bool, err error)
	// TimeoutFallback is route.mjs timeoutFallbackFor: nil when no other
	// model may run; an error when Go does not model the choice (the session
	// then ends in a *GapError). A nil seam is that error too.
	TimeoutFallback func(TimeoutFallbackInput) (*Fallback, error)
	// Lock is withLock (the schema-miss mark); nil = filelock.WithLock.
	Lock func(key string, fn func() error) error
}

// Input is one ticket's review input (Node's ReviewInput).
type Input struct {
	RepoRoot     string
	File         string
	Base         string // "" = HEAD
	Cfg          map[string]any
	Risk         *float64 // nil: the rules risk (RulesRisk of the diff)
	Kind         string   // the block kind; "" = blockKind of the reviewed file
	RulesDigest  string
	FactsExcerpt string
	WorkDir      string
	Peers        []string
	HasPeerDiffs bool
	OwnedFiles   []string
	ChangedFiles []string // nil = Peers
	TestFiles    []string
	Secrets      []string // the registered redact values
}

// GapError: a session already ran and its result needs a step not ported yet.
// The review.plan row is written and the packet file removed. B514 ported the
// schema-miss diagnosis and the needs_file round; the one Case left is
// "timeout-fallback" when Deps.TimeoutFallback is nil or cannot model the
// choice (an adaptive config Go does not read, a model the session layer
// cannot run). A caller must not hand such a ticket to Node (that would spawn
// again).
type GapError struct{ Case string }

func (e *GapError) Error() string { return "review: " + e.Case + " is not ported to Go" }

type sessionSpec struct{ lens, role, level string }

func numberIn(v any) (float64, bool) {
	n, ok := v.(float64)
	return n, ok && !math.IsNaN(n) && !math.IsInf(n, 0)
}

// SingleReviewerMaxRisk is review.single_reviewer_max_risk (default 1).
func SingleReviewerMaxRisk(cfg map[string]any) float64 {
	if v, ok := numberIn(section(cfg, "review")["single_reviewer_max_risk"]); ok && v >= 0 && v <= 3 {
		return v
	}
	return 1
}

// errConsensus marks planSessions' consensus mode (not ported).
var errConsensus = unsupported("consensus review is not ported")

// blockKinds is escalation.mjs BLOCK_KINDS.
func isDocsKind(kind string) bool { return kind == "docs" || kind == "contract" }

// planSpec is planSessions for the adaptive single-session plans; anything
// else (two lenses + judge, consensus) is unsupported.
func planSpec(risk float64, cfg map[string]any, kind string) (sessionSpec, string, error) {
	if math.IsNaN(risk) || risk < 0 || risk > 3 {
		return sessionSpec{}, "", unsupported("risk outside 0-3")
	}
	single := risk <= SingleReviewerMaxRisk(cfg)
	review := section(cfg, "review")
	docsOff := isDocsKind(kind) && review["multimodel_for_docs"] != true
	if review["multimodel"] == true && !single && !docsOff {
		return sessionSpec{}, "", errConsensus
	}
	if risk < 1 {
		return sessionSpec{"quick", "reviewer", "L2"}, "quick", nil
	}
	if risk < 2 || single {
		return sessionSpec{"full", "reviewer", "L2"}, "full", nil
	}
	return sessionSpec{}, "", unsupported("the dual-lens review with a judge is not ported")
}

var markdownFile = regexp.MustCompile(`(?i)\.(md|markdown)$`)

// testLike is a superset of Node's isCheckedTestFile: such files may get the
// B92 test-quality hint, which is not ported.
func testLike(file string) bool {
	low := strings.ToLower(file)
	return strings.Contains(low, "test") || strings.Contains(low, "spec")
}

func preSpawnGuards(in Input) error {
	review := section(in.Cfg, "review")
	if routes, ok := review["routes"].([]any); ok && len(routes) > 0 {
		return unsupported("review.routes recovery is not ported")
	}
	if review["second_levels"] != nil {
		return unsupported("review.second_levels is not ported")
	}
	level, _ := section(in.Cfg, "levels")["L2"].(map[string]any)
	if fb, ok := level["fallback"].([]any); ok && len(fb) > 0 {
		return unsupported("a levels.L2 fallback ladder is not ported")
	}
	if level == nil {
		return unsupported("levels.L2 is not configured")
	}
	if m, _ := level["model"].(string); m == "" {
		return unsupported("levels.L2 has no model")
	}
	p, _ := level["provider"].(string)
	if top, _ := in.Cfg["provider"].(string); p == "" && top == "" {
		return unsupported("levels.L2 has no provider")
	}
	return nil
}

func randomHex(r io.Reader) (string, error) {
	if r == nil {
		r = rand.Reader
	}
	b := make([]byte, 6)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func get(o ledger.Object, key string) any {
	if o == nil {
		return nil
	}
	return o.Get(key)
}

// orDefault is `value ?? fallback`.
func orDefault(v any, fallback any) any {
	if v == nil {
		return fallback
	}
	return v
}

func (d Deps) note(row ledger.Object) {
	if d.WriteRow != nil {
		_ = d.WriteRow(row)
	}
}

// prepared is File up to its first write: an early result (no_change,
// split_required) or the session's spec and packet.
type prepared struct {
	early  ledger.Object // no session
	mkdir  bool          // Node made the work dir before the early result
	spec   sessionSpec
	depth  string
	risk   float64
	packet *Packet
	stepUp ledger.Object
	head   ledger.Object
}

// prepare is reviewFile before any write: the diff, the risk, the plan, the
// packet. Every part Go does not port is ErrUnsupported here.
func prepare(in Input) (*prepared, error) {
	diff, err := ReadFileDiff(in.RepoRoot, in.File, in.Base)
	if err != nil {
		return nil, err
	}
	risk := 0.0
	if in.Risk != nil {
		risk = *in.Risk
	} else {
		risk = RulesRisk(diff.File, diff.PlusCount, diff.MinusCount, in.Cfg)
	}
	kind := in.Kind
	if kind == "" {
		kind = blockopen.Kind([]string{diff.File}, "")
	}
	spec, depth, err := planSpec(risk, in.Cfg, kind)
	if err != nil {
		return nil, err
	}
	if err := preSpawnGuards(in); err != nil {
		return nil, err
	}
	head := ledger.Object{{Key: "engine", Value: "adaptive"}, {Key: "depth", Value: depth}, {Key: "risk", Value: risk}, {Key: "file", Value: diff.File}}
	p := &prepared{spec: spec, depth: depth, risk: risk, head: head}
	result := func(fields ledger.Object, tail ...ledger.Field) ledger.Object {
		out := append(ledger.Object{}, fields...)
		out = append(out, p.head...)
		return append(out, tail...)
	}
	if trimJS(diff.DiffText) == "" {
		p.early = result(ledger.Object{{Key: "status", Value: "no_change"}, {Key: "approved", Value: false}}, ledger.Field{Key: "sessions", Value: []any{}})
		return p, nil
	}
	p.mkdir = true
	if in.HasPeerDiffs {
		return nil, unsupported("moved-code hints are not ported")
	}
	if in.Base != "" {
		for _, peer := range in.Peers {
			if peer != diff.File {
				return nil, unsupported("moved-code hints are not ported")
			}
		}
	}
	if diff.Content == nil {
		return nil, unsupported("the deletion note is not ported")
	}
	if testLike(diff.File) {
		return nil, unsupported("the test-quality hint is not ported")
	}
	changed := in.ChangedFiles
	if changed == nil {
		changed = in.Peers
	}
	paired := PairedContext(PairedInput{RepoRoot: in.RepoRoot, File: in.File, OwnedFiles: in.OwnedFiles, ChangedFiles: changed, TestFiles: in.TestFiles, Secrets: in.Secrets})
	packet, err := AssemblePacket(diff, spec.lens, in.RulesDigest, in.FactsExcerpt, in.Cfg, paired)
	if err != nil {
		return nil, err
	}
	if packet.Status == "split_required" && depth == "quick" {
		fullSpec, fullDepth, err := planSpec(1, in.Cfg, kind)
		if err != nil && err != errConsensus {
			return nil, err
		}
		if err == nil && fullDepth == "full" {
			p.stepUp = ledger.Object{{Key: "step_up", Value: ledger.Object{{Key: "from", Value: "quick"}, {Key: "tokens_in", Value: float64(packet.TokensIn)}, {Key: "budget", Value: float64(packet.Budget)}}}}
			spec, depth = fullSpec, fullDepth
			p.spec, p.depth = spec, depth
			p.head.Set("depth", depth)
			p.head = append(p.head, p.stepUp...)
			if packet, err = AssemblePacket(diff, spec.lens, in.RulesDigest, in.FactsExcerpt, in.Cfg, paired); err != nil {
				return nil, err
			}
		}
	}
	if packet.Status == "split_required" {
		if markdownFile.MatchString(diff.File) {
			return nil, unsupported("Markdown section review is not ported")
		}
		p.early = result(ledger.Object{{Key: "status", Value: "split_required"}, {Key: "approved", Value: false}},
			ledger.Field{Key: "tokens_in", Value: float64(packet.TokensIn)}, ledger.Field{Key: "budget", Value: float64(packet.Budget)}, ledger.Field{Key: "sessions", Value: []any{}})
		return p, nil
	}
	p.packet = packet
	return p, nil
}

// Check is File's dry pass: nil when File would serve the input (a session
// may still end in a *GapError), ErrUnsupported when a part it needs is not
// ported. Nothing is written.
func Check(in Input) error {
	_, err := prepare(in)
	return err
}

// SpawnLevel is the level and role File's session runs at (Check's pass);
// ok false when File ends before any session.
func SpawnLevel(in Input) (level, role string, ok bool, err error) {
	p, err := prepare(in)
	if err != nil || p.early != nil {
		return "", "", false, err
	}
	return p.spec.level, p.spec.role, true, nil
}

// File reviews one file. The outcome is Node's reviewFile result object.
func File(in Input, deps Deps) (ledger.Object, error) {
	p, err := prepare(in)
	if err != nil {
		return nil, err
	}
	if p.mkdir {
		if err := os.MkdirAll(in.WorkDir, 0o700); err != nil {
			return nil, err
		}
	}
	if p.early != nil {
		return p.early, nil
	}
	spec, depth, risk, packet, stepUp := p.spec, p.depth, p.risk, p.packet, p.stepUp
	result := func(fields ledger.Object, tail ...ledger.Field) ledger.Object {
		out := append(ledger.Object{}, fields...)
		out = append(out, p.head...)
		return append(out, tail...)
	}
	plan := ledger.Object{{Key: "event", Value: "review.plan"}, {Key: "depth_unconstrained", Value: depth}, {Key: "depth_chosen", Value: depth},
		{Key: "degrade_step", Value: 0.0}, {Key: "risk", Value: risk}, {Key: "context_mode", Value: packet.ContextMode}, {Key: "forecast_tokens", Value: float64(packet.TokensIn)}}
	deps.note(append(plan, stepUp...))
	done, err := runOne(spec, packet.Text, packet, in, deps)
	if err != nil {
		return nil, err
	}
	if done, err = needsFileRound(done, spec, packet, in, deps); err != nil {
		return nil, err
	}
	sessions := []any{publicSummary(done)}
	if done.Get("status") != "ok" {
		reason := done.Get("reason")
		deps.note(append(ledger.Object{{Key: "event", Value: "review.unavailable"}, {Key: "reason", Value: reason}, {Key: "lens", Value: done.Get("lens")}}, stepUp...))
		return result(ledger.Object{{Key: "status", Value: "unavailable"}, {Key: "reason", Value: reason}, {Key: "approved", Value: false}}, ledger.Field{Key: "sessions", Value: sessions}), nil
	}
	review := done.Get("review").(ledger.Object)
	findings, _ := review.Get("findings").([]any)
	approved := review.Get("passed") == true
	for _, f := range findings {
		if fo, _ := f.(ledger.Object); fo.Get("severity") != "nit" {
			approved = false
		}
	}
	return result(ledger.Object{{Key: "status", Value: "reviewed"}, {Key: "approved", Value: approved}},
		ledger.Field{Key: "summary", Value: review.Get("summary")}, ledger.Field{Key: "findings", Value: review.Get("findings")}, ledger.Field{Key: "sessions", Value: sessions}), nil
}

// timeoutRetries is B30's same-model retry count.
const timeoutRetries = 1

// TimeoutFallbackInput is timeoutFallbackFor's arguments.
type TimeoutFallbackInput struct {
	Cfg                     map[string]any
	Level, Role             string
	FromProvider, FromModel any
	Rows                    []any
	TicketID                string // the packet hash
}

// Fallback is timeoutFallbackFor's answer: the one-attempt config and why.
type Fallback struct {
	Cfg                     map[string]any
	Provider, Model, Reason string
}

func (d Deps) lock(key string, fn func() error) error {
	if d.Lock != nil {
		return d.Lock(key, fn)
	}
	return filelock.WithLock(key, fn, filelock.Options{})
}

// addTokens is engine.mjs addTokens.
func addTokens(a, b any) any {
	x, aok := a.(float64)
	y, bok := b.(float64)
	if !aok {
		if bok {
			return y
		}
		return nil
	}
	if bok {
		return x + y
	}
	return a
}

func usageOf(res ledger.Object) ledger.Object {
	u, _ := get(res, "usage").(ledger.Object)
	return u
}

func stringOrNull(v any) any {
	if s, ok := v.(string); ok {
		return s
	}
	return nil
}

// schemaInvalidRow is the review.schema_invalid row afterSchemaMiss and
// logSecondLevelMiss write.
func schemaInvalidRow(spec sessionSpec, res ledger.Object, hash string, second bool) ledger.Object {
	row := ledger.Object{{Key: "event", Value: "review.schema_invalid"}, {Key: "lens", Value: spec.lens}, {Key: "role", Value: spec.role}, {Key: "level", Value: spec.level},
		{Key: "provider", Value: stringOrNull(get(res, "provider"))}, {Key: "model", Value: stringOrNull(get(res, "model"))},
		{Key: "fallback_step", Value: orDefault(get(res, "fallback_step"), 0.0)}, {Key: "packet_hash", Value: hash}, {Key: "second_level", Value: second}}
	for _, f := range DiagnoseAnswer(res) {
		row.Set(f.Key, f.Value)
	}
	return row
}

func runOne(spec sessionSpec, text string, packet *Packet, in Input, deps Deps) (ledger.Object, error) {
	suffix, err := randomHex(deps.Rand)
	if err != nil {
		return nil, err
	}
	promptPath := filepath.Join(in.WorkDir, spec.lens+"-"+suffix+".md")
	if err := os.WriteFile(promptPath, []byte(text), 0o600); err != nil {
		return nil, err
	}
	defer os.Remove(promptPath)
	sum := sha256.Sum256([]byte(text))
	hash := hex.EncodeToString(sum[:])
	rowExtra := ledger.Object{{Key: "lens", Value: spec.lens}, {Key: "context_mode", Value: packet.ContextMode}, {Key: "ctx_tokens_in", Value: float64(packet.TokensIn)}}
	spawnWith := func(cfg map[string]any) (ledger.Object, bool) {
		res, err := deps.Spawn(SpawnOpts{Level: spec.level, Role: spec.role, PromptPath: promptPath, RowExtra: rowExtra.Clone(), Cfg: cfg})
		if err != nil {
			return nil, false
		}
		out, _ := TruncateReviewFields(res)
		return out, true
	}
	// spawnWithTimeoutRetry with no ladder; tryNext('timeout') counts timeouts
	// per provider/model and acts (timeoutFallbackFor) at the second one.
	timeouts := map[string]int{}
	var res, broken, fellBackTo ledger.Object
	attempts := 0
	for {
		attempts++
		out, ok := spawnWith(in.Cfg)
		if !ok {
			res = nil
			break
		}
		res = out
		if res == nil || res.Get("status") != "timeout" {
			break // at-capacity without a ladder returns as is, like any result
		}
		retried := attempts <= timeoutRetries
		note := ledger.Object{{Key: "event", Value: "review.session_timeout"}, {Key: "lens", Value: spec.lens}, {Key: "role", Value: spec.role},
			{Key: "level", Value: spec.level}, {Key: "provider", Value: orDefault(res.Get("provider"), nil)}, {Key: "model", Value: orDefault(res.Get("model"), nil)},
			{Key: "attempt", Value: float64(attempts)}, {Key: "retried", Value: retried}}
		if n, ok := res.Get("duration_ms").(float64); ok {
			note.Set("duration_ms", n)
		} else {
			note.Set("duration_ms", nil)
		}
		if s, ok := res.Get("stderr_tail").(string); ok {
			note.Set("stderr_tail", s)
		} else {
			note.Set("stderr_tail", nil)
		}
		if s, ok := res.Get("tail_note").(string); ok {
			note.Set("tail_note", s)
		}
		if n, ok := res.Get("stdout_bytes").(float64); ok {
			note.Set("stdout_bytes", n)
		} else {
			note.Set("stdout_bytes", nil)
		}
		if ev, ok := res.Get("stdout_events").([]any); ok {
			note.Set("stdout_events", ev)
		} else {
			note.Set("stdout_events", []any{})
		}
		note.Set("packet_hash", hash)
		deps.note(note)
		if deps.WriteRow != nil {
			taken, err := timeoutTry(spec, res, hash, timeouts, in, deps, func(cfg map[string]any) ledger.Object {
				out, _ := spawnWith(cfg)
				return out
			})
			if err != nil {
				return nil, err
			}
			if taken != nil {
				broken, res, fellBackTo = res, taken.res, taken.to
				attempts++
				break
			}
		}
		if !retried {
			break
		}
		if deps.Warn != nil {
			deps.Warn("review_retry", "a review session timed out; it was retried once")
		}
	}
	var verdict Verdict
	var tokensIn, tokensOut any
	if fellBackTo != nil {
		// B91: the second timeout's one other model ran; a schema miss of it is
		// logged, never counted, never tried again
		verdict = ValidateReview(res, packet.HunkHeaders, MinTokensOut(in.Cfg))
		tokensIn = addTokens(orDefault(get(usageOf(broken), "tokens_in"), nil), get(usageOf(res), "tokens_in"))
		tokensOut = addTokens(orDefault(get(usageOf(broken), "tokens_out"), nil), get(usageOf(res), "tokens_out"))
		if !verdict.OK && verdict.Reason == "schema" {
			deps.note(schemaInvalidRow(spec, res, hash, true))
		}
	} else {
		tokensIn = orDefault(get(usageOf(res), "tokens_in"), nil)
		tokensOut = orDefault(get(usageOf(res), "tokens_out"), nil)
		verdict = ValidateReview(res, packet.HunkHeaders, MinTokensOut(in.Cfg))
		if !verdict.OK && verdict.Reason == "schema" && deps.WriteRow != nil {
			afterSchemaMiss(spec, res, hash, deps)
		}
	}
	meta := ledger.Object{{Key: "lens", Value: spec.lens}, {Key: "role", Value: spec.role}, {Key: "level", Value: spec.level}, {Key: "slot", Value: nil},
		{Key: "context_mode", Value: packet.ContextMode}, {Key: "ctx_tokens_in", Value: float64(packet.TokensIn)},
		{Key: "provider", Value: orDefault(get(res, "provider"), nil)}, {Key: "model", Value: orDefault(get(res, "model"), nil)},
		{Key: "fallback_step", Value: orDefault(get(res, "fallback_step"), 0.0)}, {Key: "attempts", Value: float64(attempts)}}
	if cuts, ok := get(res, "truncated_fields").([]any); ok && len(cuts) > 0 {
		meta.Set("truncated_fields", cuts)
	}
	if verdict.PassedCoerced {
		meta.Set("passed_coerced", true)
	}
	if fellBackTo != nil {
		meta.Set("schema_fallback", fellBackTo)
	}
	meta.Set("tokens_in", tokensIn)
	meta.Set("tokens_out", tokensOut)
	if !verdict.OK {
		meta.Set("status", "unavailable")
		meta.Set("reason", verdict.Reason)
		meta.Set("review", nil)
		return meta, nil
	}
	meta.Set("status", "ok")
	meta.Set("reason", nil)
	meta.Set("review", verdict.Review)
	return meta, nil
}

type brokenTry struct {
	res ledger.Object
	to  ledger.Object
}

// timeoutTry is runOne's tryNext(res, 'timeout') with no second level: nil
// below the second timeout of one provider/model, when the packet already
// fell back, when timeoutFallbackFor names no other model or when its mark
// cannot be written.
func timeoutTry(spec sessionSpec, res ledger.Object, hash string, timeouts map[string]int, in Input, deps Deps, spawn func(map[string]any) ledger.Object) (*brokenTry, error) {
	provider, model := res.Get("provider"), res.Get("model")
	if provider == nil || model == nil {
		level := section(in.Cfg, "levels")["L2"].(map[string]any)
		if provider == nil {
			provider = orDefault(level["provider"], in.Cfg["provider"])
		}
		if model == nil {
			model = level["model"]
		}
	}
	key := jsString(provider, true) + "\x00" + jsString(model, true)
	timeouts[key]++
	count := timeouts[key]
	if count < 2 {
		return nil, nil
	}
	if deps.TimeoutFallback == nil {
		return nil, &GapError{"timeout-fallback"}
	}
	rows := []any{}
	var routingError any
	readErr := func() error {
		if deps.SchemaHistory != nil {
			_, fellBack, err := deps.SchemaHistory(hash, spec.level, spec.role)
			if err != nil {
				return err
			}
			if fellBack {
				return errFellBack
			}
		}
		if deps.ReadRows != nil {
			got, err := deps.ReadRows()
			if err != nil {
				return err
			}
			if got != nil {
				rows = got
			}
		}
		return nil
	}()
	if readErr == errFellBack {
		return nil, nil
	}
	if readErr != nil {
		// a read failure must not strand the ticket: the local count still
		// proves two timeouts; the degraded choice is recorded
		rows, routingError = []any{}, "ledger-read-failed"
	}
	fb, err := deps.TimeoutFallback(TimeoutFallbackInput{Cfg: in.Cfg, Level: spec.level, Role: spec.role, FromProvider: provider, FromModel: model, Rows: rows, TicketID: hash})
	if err != nil {
		return nil, &GapError{"timeout-fallback"}
	}
	if fb == nil {
		return nil, nil
	}
	mark := ledger.Object{{Key: "event", Value: "review.schema_fallback"}, {Key: "lens", Value: spec.lens}, {Key: "role", Value: spec.role}, {Key: "level", Value: spec.level},
		{Key: "packet_hash", Value: hash}, {Key: "from_provider", Value: provider}, {Key: "from_model", Value: model}, {Key: "to", Value: "review.timeout_fallback"},
		{Key: "to_provider", Value: fb.Provider}, {Key: "to_model", Value: fb.Model}, {Key: "attempts", Value: float64(count)},
		{Key: "trigger", Value: "timeout"}, {Key: "reason", Value: "repeated-timeout"}, {Key: "routing_reason", Value: fb.Reason}}
	if routingError != nil {
		mark.Set("routing_error", routingError)
	}
	if err := deps.WriteRow(mark); err != nil {
		return nil, nil // never run a recovery that could not be recorded
	}
	to := ledger.Object{{Key: "level", Value: "review.timeout_fallback"}, {Key: "provider", Value: fb.Provider}, {Key: "model", Value: fb.Model}}
	return &brokenTry{res: spawn(fb.Cfg), to: to}, nil
}

var errFellBack = errors.New("fell back")

// afterSchemaMiss with no second level (Go refuses review.second_levels):
// under the packet's lock, the history read (an unreadable one is one
// warning line) and the signed review.schema_invalid row with its diagnosis.
func afterSchemaMiss(spec sessionSpec, res ledger.Object, hash string, deps Deps) {
	_ = deps.lock("schema-fallback\x00"+hash+"\x00"+spec.level+"\x00"+spec.role, func() error {
		if deps.SchemaHistory != nil {
			if _, _, err := deps.SchemaHistory(hash, spec.level, spec.role); err != nil && deps.Warn != nil {
				deps.Warn("review_schema_history", "the schema-miss history could not be read; no second-level try")
			}
		}
		_ = deps.WriteRow(schemaInvalidRow(spec, res, hash, false))
		return nil
	})
}

// needsFileRound is engine.mjs run's needs_file step: a valid reviewer
// answer that asks for files gets ONE more session with them attached
// (attachFiles), or needs_file-refused when none could be attached and none
// was refused by policy.
func needsFileRound(summary ledger.Object, spec sessionSpec, packet *Packet, in Input, deps Deps) (ledger.Object, error) {
	review, _ := summary.Get("review").(ledger.Object)
	needs, _ := get(review, "needs_file").([]any)
	if spec.role != "reviewer" || summary.Get("status") != "ok" || len(needs) == 0 {
		return summary, nil
	}
	paths := make([]string, 0, len(needs))
	for _, n := range needs {
		s, _ := n.(string) // the finding schema's items are strings
		paths = append(paths, s)
	}
	requested := []any{}
	for _, p := range paths[:min(10, len(paths))] {
		requested = append(requested, cutUTF16(gates.Mask(p, in.Secrets), 200))
	}
	changed := in.ChangedFiles
	if changed == nil {
		changed = in.Peers
	}
	var d AttachDiagnostics
	extra := AttachFiles(in.RepoRoot, paths, BudgetFor(in.Cfg, "full_in"), in.OwnedFiles, changed, &d)
	fields := ledger.Object{{Key: "needs_file_requested", Value: requested}}
	if len(d.Errors) > 0 {
		errs := make([]any, len(d.Errors))
		for i, e := range d.Errors {
			errs[i] = e
		}
		fields.Set("needs_file_errors", errs)
	}
	if d.Attached+d.PolicyRefused == 0 {
		out := summary.Clone()
		for _, f := range fields {
			out.Set(f.Key, f.Value)
		}
		out.Set("status", "unavailable")
		out.Set("reason", "needs_file-refused")
		out.Set("review", nil)
		return out, nil
	}
	out, err := runOne(spec, packet.Text+extra, packet, in, deps)
	if err != nil {
		return nil, err
	}
	for _, f := range fields {
		out.Set(f.Key, f.Value)
	}
	out.Set("needs_file_round", true)
	return out, nil
}

// publicSummary drops the answer and adds its verdict and findings.
func publicSummary(s ledger.Object) ledger.Object {
	review, _ := s.Get("review").(ledger.Object)
	out := s.Clone()
	out.Delete("review")
	if review != nil {
		out.Set("passed", review.Get("passed") == true)
		out.Set("findings", review.Get("findings"))
	}
	return out
}

// IsUnsupported reports whether err says "hand this ticket to Node".
func IsUnsupported(err error) bool { return errors.Is(err, ErrUnsupported) }
