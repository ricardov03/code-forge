package review

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

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

type sessionSpec struct {
	lens, role, level string
	// cfg is planSessions' per-session config (consensus slot B); nil: the
	// run's config.
	cfg  map[string]any
	slot string // "" is null
}

func (s sessionSpec) slotValue() any {
	if s.slot == "" {
		return nil
	}
	return s.slot
}

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

// blockKinds is escalation.mjs BLOCK_KINDS.
func isDocsKind(kind string) bool { return kind == "docs" || kind == "contract" }

// sessionPlan is planSessions' plan: the reviewers in order, the judge
// (nil: none) and a consensus refusal ("" = none).
type sessionPlan struct {
	mode, depth string
	reviewers   []sessionSpec
	judge       *sessionSpec
	refused     string
}

func (pl sessionPlan) depthValue() any {
	if pl.depth == "" {
		return nil
	}
	return pl.depth
}

// specs is every session of the plan, the judge last.
func (pl sessionPlan) specs() []sessionSpec {
	out := append([]sessionSpec{}, pl.reviewers...)
	if pl.judge != nil {
		out = append(out, *pl.judge)
	}
	return out
}

// consensusSecond is engine.mjs secondLevel: slot B's levels.L2, or the
// refusal reason.
func consensusSecond(cfg map[string]any) (map[string]any, string) {
	first, ok := resolveLevelOf(cfg, "L2")
	if !ok {
		return nil, "consensus-no-l2"
	}
	override, _ := section(cfg, "review", "second_levels")["L2"].(map[string]any)
	provider := override["provider"]
	if provider == nil {
		provider = section(cfg, "review")["second_provider"]
	}
	p, _ := provider.(string)
	if p == "" {
		return nil, "consensus-no-second-provider"
	}
	if p == first.provider {
		return nil, "consensus-same-provider"
	}
	if m, _ := override["model"].(string); m == "" {
		return nil, "consensus-no-second-model"
	}
	level := map[string]any{}
	for k, v := range override {
		level[k] = v
	}
	level["provider"] = p
	fallback := []any{}
	if list, ok := override["fallback"].([]any); ok {
		for _, f := range list {
			if fm, ok := f.(map[string]any); ok && fm["provider"] == first.provider {
				continue
			}
			fallback = append(fallback, f)
		}
	}
	level["fallback"] = fallback
	out := map[string]any{}
	for k, v := range cfg {
		out[k] = v
	}
	levels := map[string]any{}
	for k, v := range section(cfg, "levels") {
		levels[k] = v
	}
	levels["L2"] = level
	out["levels"] = levels
	return out, ""
}

// planSessions is engine.mjs planSessions (B34): consensus, one L2
// session (quick or full), or two blind lenses and an L3 judge.
func planSessions(risk float64, cfg map[string]any, kind string) (sessionPlan, error) {
	if math.IsNaN(risk) || risk < 0 || risk > 3 {
		return sessionPlan{}, unsupported("risk outside 0-3")
	}
	judge := &sessionSpec{lens: "judge", role: "judge", level: "L3"}
	single := risk <= SingleReviewerMaxRisk(cfg)
	review := section(cfg, "review")
	docsOff := isDocsKind(kind) && review["multimodel_for_docs"] != true
	if review["multimodel"] == true && !single && !docsOff {
		second, refused := consensusSecond(cfg)
		if refused != "" {
			return sessionPlan{mode: "consensus", refused: refused}, nil
		}
		return sessionPlan{mode: "consensus", depth: "consensus", judge: judge, reviewers: []sessionSpec{
			{lens: "full", role: "reviewer", level: "L2", slot: "A"}, {lens: "full", role: "reviewer", level: "L2", slot: "B", cfg: second}}}, nil
	}
	if risk < 1 {
		return sessionPlan{mode: "adaptive", depth: "quick", reviewers: []sessionSpec{{lens: "quick", role: "reviewer", level: "L2"}}}, nil
	}
	if risk < 2 || single {
		return sessionPlan{mode: "adaptive", depth: "full", reviewers: []sessionSpec{{lens: "full", role: "reviewer", level: "L2"}}}, nil
	}
	return sessionPlan{mode: "adaptive", depth: "dual", judge: judge, reviewers: []sessionSpec{
		{lens: "A", role: "reviewer", level: "L2", slot: "A"}, {lens: "B", role: "reviewer", level: "L2", slot: "B"}}}, nil
}

// planGuards is levelGuards for every session of the plan, each on its own
// config.
func planGuards(pl sessionPlan, cfg map[string]any) error {
	for _, spec := range pl.specs() {
		c := cfg
		if spec.cfg != nil {
			c = spec.cfg
		}
		if err := levelGuards(c, spec.level); err != nil {
			return err
		}
	}
	return nil
}

var markdownFile = regexp.MustCompile(`(?i)\.(md|markdown)$`)

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
// split_required, refused) or the plan and its packets.
type prepared struct {
	early   ledger.Object // no session
	mkdir   bool          // Node made the work dir before the early result
	plan    sessionPlan
	risk    float64
	diff    *FileDiff
	packets []*Packet // one per reviewer
	stepUp  ledger.Object
	head    ledger.Object
	// sections: a Markdown file over the budget, one packet set per section
	sections []sectionPacket
	// extras: the movedOf parts every packet carries (B537)
	extras packetParts
}

// packetsFor is engine.mjs packetsFor: one packet per reviewer lens, each
// with moved (movedOf of the part's keys).
func packetsFor(pl sessionPlan, d *FileDiff, in Input, paired, moved string) ([]*Packet, *Packet, error) {
	out := []*Packet{}
	var split *Packet
	for _, spec := range pl.reviewers {
		pk, err := AssemblePacket(d, spec.lens, in.RulesDigest, in.FactsExcerpt, in.Cfg, paired, moved)
		if err != nil {
			return nil, nil, err
		}
		if split == nil && pk.Status == "split_required" {
			split = pk
		}
		out = append(out, pk)
	}
	return out, split, nil
}

// prepare is reviewFile before any write: the diff, the risk, the plan, the
// packets. Every part Go does not port is ErrUnsupported here.
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
	plan, err := planSessions(risk, in.Cfg, kind)
	if err != nil {
		return nil, err
	}
	head := ledger.Object{{Key: "engine", Value: plan.mode}, {Key: "depth", Value: plan.depthValue()}, {Key: "risk", Value: risk}, {Key: "file", Value: diff.File}}
	p := &prepared{plan: plan, risk: risk, head: head, diff: diff}
	result := func(fields ledger.Object, tail ...ledger.Field) ledger.Object {
		out := append(ledger.Object{}, fields...)
		out = append(out, p.head...)
		return append(out, tail...)
	}
	if plan.refused != "" {
		p.early = result(ledger.Object{{Key: "status", Value: "refused"}, {Key: "reason", Value: plan.refused}, {Key: "approved", Value: false}}, ledger.Field{Key: "sessions", Value: []any{}})
		return p, nil
	}
	if err := planGuards(plan, in.Cfg); err != nil {
		return nil, err
	}
	if trimJS(diff.DiffText) == "" {
		p.early = result(ledger.Object{{Key: "status", Value: "no_change"}, {Key: "approved", Value: false}}, ledger.Field{Key: "sessions", Value: []any{}})
		return p, nil
	}
	p.mkdir = true
	extras, err := packetExtras(in, diff)
	if err != nil {
		return nil, err
	}
	p.extras = extras
	changed := in.ChangedFiles
	if changed == nil {
		changed = in.Peers
	}
	paired := PairedContext(PairedInput{RepoRoot: in.RepoRoot, File: in.File, OwnedFiles: in.OwnedFiles, ChangedFiles: changed, TestFiles: in.TestFiles, Secrets: in.Secrets})
	packets, split, err := packetsFor(plan, diff, in, paired, extras.movedOf(nil))
	if err != nil {
		return nil, err
	}
	if split != nil && plan.depth == "quick" {
		full, err := planSessions(1, in.Cfg, kind)
		if err != nil {
			return nil, err
		}
		// a configured consensus/refusal is not the single full session this step permits
		if full.depth == "full" {
			if err := planGuards(full, in.Cfg); err != nil {
				return nil, err
			}
			p.stepUp = ledger.Object{{Key: "step_up", Value: ledger.Object{{Key: "from", Value: "quick"}, {Key: "tokens_in", Value: float64(split.TokensIn)}, {Key: "budget", Value: float64(split.Budget)}}}}
			p.plan = full
			p.head.Set("engine", full.mode)
			p.head.Set("depth", full.depth)
			p.head = append(p.head, p.stepUp...)
			if packets, split, err = packetsFor(full, diff, in, paired, extras.movedOf(nil)); err != nil {
				return nil, err
			}
		}
	}
	if split != nil {
		if markdownFile.MatchString(diff.File) {
			return prepareSections(p, diff, split, in, paired, result)
		}
		p.early = result(ledger.Object{{Key: "status", Value: "split_required"}, {Key: "approved", Value: false}},
			ledger.Field{Key: "tokens_in", Value: float64(split.TokensIn)}, ledger.Field{Key: "budget", Value: float64(split.Budget)}, ledger.Field{Key: "sessions", Value: []any{}})
		return p, nil
	}
	p.packets = packets
	return p, nil
}

// packetParts is reviewFile's parts of movedOf (B537): the `## deleted file`
// note of a pure deletion, the moves for the `## moved code` section (B56)
// and the `## test-quality` hint (B92).
type packetParts struct {
	deleted, quality string
	moves            *mvMoves
	content          *string
}

// movedOf is engine.mjs movedOf(keys): keys nil is the whole file, else a
// Markdown section's heading-group keys (only its moves).
func (pp packetParts) movedOf(keys []int) string {
	parts := []string{}
	for _, t := range []string{pp.deleted, movedText(pp.moves, pp.content, keys), pp.quality} {
		if t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n")
}

// packetExtras reads reviewFile's movedOf parts once.
func packetExtras(in Input, diff *FileDiff) (packetParts, error) {
	pp := packetParts{content: diff.Content}
	if diff.Content == nil && isAbsent(in.RepoRoot, diff.File) {
		note, err := deletionNote(in.RepoRoot, diff.File)
		if err != nil {
			if note, err = renderDeletionNote(diff.File, delRefs{status: "not_checked", paths: []string{}}); err != nil {
				return pp, err
			}
		}
		pp.deleted = note
	}
	pp.moves = movesFor(diff.DiffText, in.RepoRoot, in.Base, diff.File, in.Peers)
	pp.quality = testQualityNote(diff.File, diff.Content)
	return pp, nil
}

// isAbsent is engine.mjs isAbsent: lstat fails with ENOENT or ENOTDIR.
func isAbsent(repoRoot, file string) bool {
	_, err := os.Lstat(filepath.Join(repoRoot, file))
	return err != nil && (errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ENOTDIR))
}

// Check is File's dry pass: nil when File would serve the input (a session
// may still end in a *GapError), ErrUnsupported when a part it needs is not
// ported. Nothing is written.
func Check(in Input) error {
	_, err := prepare(in)
	return err
}

// SpawnLevel is the level and role File's first session runs at (Check's
// pass); ok false when File ends before any session.
func SpawnLevel(in Input) (level, role string, ok bool, err error) {
	p, err := prepare(in)
	if err != nil || p.early != nil {
		return "", "", false, err
	}
	return p.plan.reviewers[0].level, p.plan.reviewers[0].role, true, nil
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
	result := func(fields ledger.Object, tail ...ledger.Field) ledger.Object {
		out := append(ledger.Object{}, fields...)
		out = append(out, p.head...)
		return append(out, tail...)
	}
	if p.sections != nil {
		return fileSections(p, in, deps, result)
	}
	part, err := reviewPart(p, p.diff, p.packets, in, deps, p.stepUp, nil)
	if err != nil {
		return nil, err
	}
	sessions := []any{}
	for _, s := range part.sessions {
		sessions = append(sessions, s)
	}
	if !part.reviewed {
		return result(ledger.Object{{Key: "status", Value: "unavailable"}, {Key: "reason", Value: part.reason}, {Key: "approved", Value: false}}, ledger.Field{Key: "sessions", Value: sessions}), nil
	}
	return result(ledger.Object{{Key: "status", Value: "reviewed"}, {Key: "approved", Value: part.approved}},
		ledger.Field{Key: "summary", Value: part.summary}, ledger.Field{Key: "findings", Value: part.findings}, ledger.Field{Key: "sessions", Value: sessions}), nil
}

// partOutcome is engine.mjs reviewPart's answer for one packet set.
type partOutcome struct {
	reviewed bool
	reason   any
	approved bool
	summary  any
	findings any
	sessions []ledger.Object
}

// reviewPart is engine.mjs reviewPart: the review.plan row (rowExtra
// appended), each reviewer's session and needs_file round, then the judge
// on both reports. Node starts the reviewers together; Go runs them in
// plan order. keys: the section's heading-group keys for the judge packet's
// moved text (nil = the whole file).
func reviewPart(p *prepared, part *FileDiff, packets []*Packet, in Input, deps Deps, rowExtra ledger.Object, keys []int) (partOutcome, error) {
	forecast := 0
	for _, pk := range packets {
		forecast += pk.TokensIn
	}
	depth := p.plan.depthValue()
	plan := ledger.Object{{Key: "event", Value: "review.plan"}, {Key: "depth_unconstrained", Value: depth}, {Key: "depth_chosen", Value: depth},
		{Key: "degrade_step", Value: 0.0}, {Key: "risk", Value: p.risk}, {Key: "context_mode", Value: packets[0].ContextMode}, {Key: "forecast_tokens", Value: float64(forecast)}}
	deps.note(append(plan, rowExtra...))
	schemaFallback := p.plan.mode != "consensus"
	done := []ledger.Object{}
	for i, spec := range p.plan.reviewers {
		res, err := runOneWith(spec, packets[i].Text, packets[i], in, deps, schemaFallback)
		if err != nil {
			return partOutcome{}, err
		}
		if res, err = needsFileRound(res, spec, packets[i], in, deps); err != nil {
			return partOutcome{}, err
		}
		done = append(done, res)
	}
	out := partOutcome{}
	for _, d := range done {
		out.sessions = append(out.sessions, publicSummary(d))
	}
	for _, d := range done {
		if d.Get("status") != "ok" {
			out.reason = d.Get("reason")
			deps.note(append(ledger.Object{{Key: "event", Value: "review.unavailable"}, {Key: "reason", Value: out.reason}, {Key: "lens", Value: d.Get("lens")}}, rowExtra...))
			return out, nil
		}
	}
	final := done[0]
	if judge := p.plan.judge; judge != nil {
		reports := map[string]any{}
		for i, spec := range p.plan.reviewers {
			reports[spec.slot] = done[i].Get("review")
		}
		jp, err := assembleJudgePacket(part, reports["A"], reports["B"], in.Cfg, p.extras.movedOf(keys))
		if err != nil {
			return partOutcome{}, err
		}
		if final, err = runOneWith(*judge, jp.Text, jp, in, deps, schemaFallback); err != nil {
			return partOutcome{}, err
		}
		out.sessions = append(out.sessions, publicSummary(final))
		if final.Get("status") != "ok" {
			out.reason = final.Get("reason")
			deps.note(append(ledger.Object{{Key: "event", Value: "review.unavailable"}, {Key: "reason", Value: out.reason}, {Key: "lens", Value: "judge"}}, rowExtra...))
			return out, nil
		}
	}
	review := final.Get("review").(ledger.Object)
	findings, _ := review.Get("findings").([]any)
	out.reviewed, out.approved = true, review.Get("passed") == true
	for _, f := range findings {
		if fo, _ := f.(ledger.Object); fo.Get("severity") != "nit" {
			out.approved = false
		}
	}
	out.summary, out.findings = review.Get("summary"), review.Get("findings")
	return out, nil
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

// timeoutRow is spawnWithTimeoutRetry's review.session_timeout note (with
// the packet hash; Node's note has no second_level field).
func timeoutRow(spec sessionSpec, res ledger.Object, attempts int, retried bool, hash string) ledger.Object {
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
	return note
}

type brokenTry struct {
	res      ledger.Object
	attempts int
	to       ledger.Object
}

var errFellBack = errors.New("fell back")

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
	out, err := runOneWith(spec, packet.Text+extra, packet, in, deps, true)
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
