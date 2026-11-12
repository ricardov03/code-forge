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
	"github.com/ricardov03/code-forge/native/ledger"
)

// Port of reviewFile (src/review/engine.mjs) for one ticket on the adaptive
// single-reviewer path: packet, one L2 session (quick or full, with B30's
// same-model timeout retry), stub guard, the review.plan / review.unavailable /
// review.session_timeout rows and Node's result object. Every case this file
// does not port is refused BEFORE any session is spawned (ErrUnsupported), so a
// caller can still hand the ticket to Node. Three cases are only known after a
// session ran; File stops there with a *GapError (see its doc).

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
// The review.plan row is written and the packet file removed; Node would go on
// with Case: "schema-miss-diagnosis" (signed review.schema_invalid row with the
// Ajv/V8 diagnosis), "timeout-fallback" (a second timeout on one model:
// timeoutFallbackFor over the ledger) or "needs-file" (attachFiles round).
// A caller must not hand such a ticket to Node (that would spawn again).
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
	done, err := runOne(spec, packet, in, deps)
	if err != nil {
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

func runOne(spec sessionSpec, packet *Packet, in Input, deps Deps) (ledger.Object, error) {
	suffix, err := randomHex(deps.Rand)
	if err != nil {
		return nil, err
	}
	promptPath := filepath.Join(in.WorkDir, spec.lens+"-"+suffix+".md")
	if err := os.WriteFile(promptPath, []byte(packet.Text), 0o600); err != nil {
		return nil, err
	}
	defer os.Remove(promptPath)
	sum := sha256.Sum256([]byte(packet.Text))
	hash := hex.EncodeToString(sum[:])
	rowExtra := ledger.Object{{Key: "lens", Value: spec.lens}, {Key: "context_mode", Value: packet.ContextMode}, {Key: "ctx_tokens_in", Value: float64(packet.TokensIn)}}
	spawn := func() (ledger.Object, bool) {
		res, err := deps.Spawn(SpawnOpts{Level: spec.level, Role: spec.role, PromptPath: promptPath, RowExtra: rowExtra.Clone(), Cfg: in.Cfg})
		if err != nil {
			return nil, false
		}
		out, _ := TruncateReviewFields(res)
		return out, true
	}
	// spawnWithTimeoutRetry with no ladder; tryNext('timeout') counts timeouts
	// per provider/model and only acts (timeoutFallbackFor) at the second one.
	timeouts := map[string]int{}
	var res ledger.Object
	attempts := 0
	for {
		attempts++
		out, ok := spawn()
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
			if timeouts[key] >= 2 {
				return nil, &GapError{"timeout-fallback"}
			}
		}
		if !retried {
			break
		}
		if deps.Warn != nil {
			deps.Warn("review_retry", "a review session timed out; it was retried once")
		}
	}
	verdict := ValidateReview(res, packet.HunkHeaders, MinTokensOut(in.Cfg))
	if !verdict.OK && verdict.Reason == "schema" && deps.WriteRow != nil {
		return nil, &GapError{"schema-miss-diagnosis"}
	}
	var usage ledger.Object
	if res != nil {
		usage, _ = res.Get("usage").(ledger.Object)
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
	meta.Set("tokens_in", orDefault(get(usage, "tokens_in"), nil))
	meta.Set("tokens_out", orDefault(get(usage, "tokens_out"), nil))
	if !verdict.OK {
		meta.Set("status", "unavailable")
		meta.Set("reason", verdict.Reason)
		meta.Set("review", nil)
		return meta, nil
	}
	if needs, ok := verdict.Review.Get("needs_file").([]any); ok && len(needs) > 0 {
		return nil, &GapError{"needs-file"}
	}
	meta.Set("status", "ok")
	meta.Set("reason", nil)
	meta.Set("review", verdict.Review)
	return meta, nil
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
