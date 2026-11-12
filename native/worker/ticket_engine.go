package worker

// B497: the review session of a queue ticket (engine.mjs reviewTicket after
// its refusals, through fixLoopRound). enginePlan is the dry pass: it reads
// the block entry, the facts, the rules digest and the file context, and
// runs every ported dry check (review.Check, FixLoopCheck, session.Check for
// the level the round spawns at) before the worker writes anything. A ticket
// any part of which is not ported is ErrUnsupported with nothing written.

import (
	"encoding/hex"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/ricardov03/code-forge/native/contextpack"
	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
	"github.com/ricardov03/code-forge/native/review"
	"github.com/ricardov03/code-forge/native/session"
)

// blockEntry is engine.mjs blockEntryFor's result.
type blockEntry struct {
	base       *string
	level      string
	owned      []any
	ownedKinds ledger.Object
	testFiles  []string
	acceptance any
	kind       string
	tree       *WorktreeEntry
}

var levelForm = regexp.MustCompile(`^L[0-3]$`)
var globChar = regexp.MustCompile(`[*?\[\]{}]`)

// blockKindOf is escalation.mjs blockKind.
func blockKindOf(owned []string, declared any) string {
	if d, ok := declared.(string); ok && (d == "code" || d == "docs" || d == "contract") {
		return d
	}
	if len(owned) == 0 {
		return "code"
	}
	return blockKindFiles(owned)
}

// blockKindFiles is blockKind over a non-empty owned list (blockopen.Kind's
// per-file kinds).
func blockKindFiles(owned []string) string {
	kind := "docs"
	for _, entry := range owned {
		lower := strings.ToLower(entry)
		one := "code"
		for _, suffix := range []string{".md", ".mdx", ".markdown", ".rst", ".adoc"} {
			if strings.HasSuffix(lower, suffix) {
				one = "docs"
			}
		}
		for _, suffix := range []string{".schema.json", ".schema.yaml", ".schema.yml", ".proto", ".graphql", ".gql", ".avsc", ".openapi.json", ".openapi.yaml", ".openapi.yml"} {
			if strings.HasSuffix(lower, suffix) {
				one = "contract"
			}
		}
		if strings.HasSuffix(lower, ".json") || strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml") {
			parts := strings.Split(lower, "/")
			for _, dir := range parts[:len(parts)-1] {
				if dir == "schema" || dir == "schemas" || dir == "contract" || dir == "contracts" {
					one = "contract"
				}
			}
		}
		if one == "code" {
			return "code"
		}
		if one == "contract" {
			kind = "contract"
		}
	}
	return kind
}

// blockEntryOf is blockEntryFor over the record engineRefusal read (nil: no
// run record). The refusals (unknown block, no base, a bad worktree record)
// were answered before.
func blockEntryOf(rec ledger.Object, block, file string) blockEntry {
	if rec == nil {
		return blockEntry{level: "L2", owned: []any{}, ownedKinds: ledger.Object{}, testFiles: []string{}, kind: blockKindOf([]string{file}, nil)}
	}
	blocks, _ := rec.Get("blocks").(ledger.Object)
	entry, _ := blocks.Get(block).(ledger.Object)
	sha, _ := entry.Get("base_sha").(string)
	e := blockEntry{level: "L2", owned: []any{}, ownedKinds: ledger.Object{}, testFiles: []string{}, acceptance: entry.Get("acceptance")}
	names := []string{}
	if list, ok := entry.Get("owned_files").([]any); ok {
		for _, f := range list {
			if s, ok := f.(string); ok {
				e.owned = append(e.owned, s)
				names = append(names, s)
			}
		}
	}
	if kinds, ok := entry.Get("owned_kinds").(ledger.Object); ok {
		e.ownedKinds = kinds
	}
	if list, ok := entry.Get("test_files").([]any); ok {
		for _, f := range list {
			if s, ok := f.(string); ok && !globChar.MatchString(s) && owned.OwnsFile(e.owned, e.ownedKinds, s) {
				e.testFiles = append(e.testFiles, s)
			}
		}
	}
	e.base = &sha
	if wt, ok := entry.Get("worktree").(ledger.Object); ok {
		p, _ := wt.Get("path").(string)
		b, _ := wt.Get("branch").(string)
		s, _ := wt.Get("base_sha").(string)
		e.tree = &WorktreeEntry{Path: p, Branch: b, BaseSHA: s}
		e.base = &s
	}
	if l, ok := entry.Get("level").(string); ok && levelForm.MatchString(l) {
		e.level = l
	}
	if entry.Get("kind") == "light" {
		e.kind = "light"
	} else {
		e.kind = blockKindOf(names, entry.Get("kind"))
	}
	return e
}

// enginePlan is a ticket ready for its fix-loop round.
type enginePlan struct {
	ticket  FixLoopTicket
	request FixLoopRequest
	input   review.Input
	workDir string
}

func (w *TicketRunner) secrets() []string {
	out := []string{}
	for _, s := range w.Secrets {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// fileContext is reviewTicket's fileContext: the block's peers (changed
// owned files other than this one, not excluded), the changed and owned
// files and the tests; all empty when the change listing fails.
func (w *TicketRunner) fileContext(e blockEntry, contentRoot, file string, cfg map[string]any) (peers, changed, ownedFiles, tests []string, err error) {
	peers = []string{}
	if e.base != nil && len(e.owned) > 0 {
		all, ok, err := computeFileSet(contentRoot, *e.base)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		if !ok {
			return []string{}, []string{}, []string{}, []string{}, nil
		}
		for _, f := range all {
			if f == file || !owned.OwnsFile(e.owned, e.ownedKinds, f) {
				continue
			}
			rule, err := w.exclusion(f, cfg)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			if rule == "" {
				peers = append(peers, f)
			}
		}
	}
	changed = []string{}
	for _, f := range append(append([]string{}, peers...), file) {
		if owned.OwnsFile(e.owned, e.ownedKinds, f) {
			changed = append(changed, f)
		}
	}
	seen := map[string]bool{}
	ownedFiles = []string{}
	for _, f := range append(append([]string{}, changed...), e.testFiles...) {
		if !seen[f] {
			seen[f] = true
			ownedFiles = append(ownedFiles, f)
		}
	}
	return peers, changed, ownedFiles, append([]string{}, e.testFiles...), nil
}

func (w *TicketRunner) sessionOpts(cfg map[string]any, block string, timeoutS float64) session.Opts {
	run, b := w.RunID, block
	return session.Opts{Cfg: cfg, Run: &run, Block: &b, Slug: w.Slug, RunRoot: w.RunRootDir, TimeoutMs: timeoutS * 1000}
}

func (w *TicketRunner) sessionDeps() session.Deps {
	d := w.SessionDeps
	if d.Home == "" {
		d.Home = w.Home
	}
	if d.Environ == nil {
		d.Environ = SessionEnv(os.Environ(), []string{w.JevKey, hex.EncodeToString(w.Key)})
	}
	if d.RunID == "" {
		d.RunID = w.RunID
	}
	if d.Warn == nil {
		d.Warn = w.Warn
	}
	return d
}

// planEngine is the part of engine.mjs reviewTicket after its refusals, up
// to the fixLoopRound call, as a dry pass: an outcome (context-unreadable),
// a plan, or ErrUnsupported. A read that fails in the dry pass (a git
// listing, the state file) is left to Node too: Go has written nothing yet.
func (w *TicketRunner) planEngine(t ledger.Object, rec ledger.Object, cfg map[string]any, contentRoot string, timeoutS float64) (ledger.Object, *enginePlan, error) {
	out, plan, err := w.planEngineDry(t, rec, cfg, contentRoot, timeoutS)
	if err != nil && !IsUnsupported(err) {
		return nil, nil, unsupported("the review dry pass: " + err.Error())
	}
	return out, plan, err
}

func (w *TicketRunner) planEngineDry(t ledger.Object, rec ledger.Object, cfg map[string]any, contentRoot string, timeoutS float64) (ledger.Object, *enginePlan, error) {
	block, _ := t.Get("block").(string)
	file, _ := t.Get("file").(string)
	hash, _ := t.Get("content_hash").(string)
	id, _ := t.Get("ticket").(string)
	e := blockEntryOf(rec, block, file)
	facts := FactsFor(e.acceptance, e.tree, cfg, w.secrets())
	digest, err := contextpack.ReviewDigest(w.RepoRoot, cfg, func(s string) string { return gates.Mask(s, w.secrets()) })
	if err != nil {
		if _, ok := err.(*contextpack.Error); ok {
			// reviewWith's mapping of the engine's {status, reason, approved, engine, sessions}
			return ledger.Object{{Key: "event", Value: "review.result"}, {Key: "status", Value: "unavailable"}, {Key: "approved", Value: false},
				{Key: "engine", Value: "adaptive"}, {Key: "reason", Value: "context-unreadable"}, {Key: "sessions", Value: []any{}}}, nil, nil
		}
		return nil, nil, unsupported("the context digest: " + err.Error())
	}
	if e.kind == "light" {
		return nil, nil, unsupported("a light block's batch round")
	}
	if len(w.Key) == 0 {
		return nil, nil, unsupported("a review with no run key (reviewFile without the fix loop)")
	}
	if w.JevKey != "" {
		return nil, nil, unsupported("System 1 (Jev) triage in the fix loop")
	}
	budget, _ := obj(cfg["budget"])
	if usd, ok := budget["usd"].(float64); ok && usd > 0 && !math.IsInf(usd, 0) && w.sessionDeps().Warn == nil {
		return nil, nil, unsupported("budget.usd with no error-log warning seam")
	}
	peers, changed, ownedFiles, tests, err := w.fileContext(e, contentRoot, file, cfg)
	if err != nil {
		return nil, nil, err
	}
	workDir := filepath.Join(w.RunRootDir, "packets", id)
	in := review.Input{RepoRoot: contentRoot, File: file, Cfg: cfg, Kind: e.kind, RulesDigest: digest, FactsExcerpt: facts, WorkDir: workDir,
		Peers: peers, HasPeerDiffs: len(peers) > 0, OwnedFiles: ownedFiles, ChangedFiles: changed, TestFiles: tests, Secrets: w.secrets()}
	if e.base != nil {
		in.Base = *e.base
	}
	tk := FixLoopTicket{Block: block, File: file, ContentHash: hash}
	stamp := func(row ledger.Object) error {
		out := ledger.Object{{Key: "run", Value: w.RunID}}
		for _, f := range row {
			out.Set(f.Key, f.Value)
		}
		out.Set("block", block)
		out.Set("file", file)
		out.Set("content_hash", hash)
		signed, err := w.signObject(out)
		if err != nil {
			return err
		}
		return w.writeRow(signed)
	}
	sopts, sdeps := w.sessionOpts(cfg, block, timeoutS), w.sessionDeps()
	req := FixLoopRequest{Home: w.Home, RunID: w.RunID, RunRootDir: w.RunRootDir, ContentRoot: contentRoot, Level: e.level, Cfg: cfg, Key: w.Key,
		Base: e.base, Owned: e.owned, OwnedKinds: e.ownedKinds, Secrets: w.secrets(), ReadRows: w.readObjects, WriteRow: stamp, Now: w.now,
		Review: func(write func(ledger.Object) error) (ledger.Object, error) {
			return review.File(in, review.Deps{Spawn: w.pooled(id, cfg, session.ReviewSpawn(sopts, sdeps)), WriteRow: write, Warn: w.Warn,
				ReadRows: w.readRows, SchemaHistory: schemaHistory(w.readRows, w.Key, w.RunID, file, hash),
				TimeoutFallback: func(ti review.TimeoutFallbackInput) (*review.Fallback, error) {
					fb, err := timeoutFallbackFor(ti.Cfg, ti, w.now())
					if err != nil || fb == nil {
						return nil, err
					}
					// a model the Go session layer cannot run is not modelled here
					o := sopts
					o.Cfg = fb.Cfg
					if err := session.CheckReview(o, ti.Level, ti.Role); err != nil {
						return nil, err
					}
					return fb, nil
				}})
		}}
	spawns, err := FixLoopCheck(tk, req)
	if err != nil {
		return nil, nil, err
	}
	if spawns {
		level, role, ok, err := review.SpawnLevel(in)
		if err != nil {
			return nil, nil, err
		}
		if ok {
			if err := session.CheckReview(sopts, level, role); err != nil {
				return nil, nil, unsupported("the review session: " + err.Error())
			}
		}
	}
	return nil, &enginePlan{ticket: tk, request: req, input: in, workDir: workDir}, nil
}

// runEngine is the fixLoopRound call and loop.mjs reviewWith's outcome
// mapping; the packet directory is removed after (reviewTicket's finally).
func (w *TicketRunner) runEngine(p *enginePlan, base func(string, string) ledger.Object) (ledger.Object, error) {
	defer os.RemoveAll(p.workDir)
	outcome, err := FixLoopRound(p.ticket, p.request)
	if err != nil {
		if IsUnsupported(err) {
			return nil, err
		}
		return base("unavailable", "engine-error"), nil
	}
	return w.engineResult(outcome, base), nil
}

var loopFieldNames = []string{"round", "kind", "level", "next", "trigger", "stopped", "late"}

// engineResult is reviewWith's mapping of the engine outcome.
func (w *TicketRunner) engineResult(o ledger.Object, base func(string, string) ledger.Object) ledger.Object {
	head := base("", "")
	out := ledger.Object{}
	for _, f := range head {
		if f.Key == "event" || f.Key == "block" || f.Key == "file" || f.Key == "content_hash" {
			out = append(out, f)
		}
	}
	out.Set("status", o.Get("status"))
	out.Set("approved", o.Get("approved") == true)
	if hasKey(o, "engine") {
		out.Set("engine", o.Get("engine"))
	}
	if s, ok := o.Get("reason").(string); ok {
		out.Set("reason", s)
	}
	if s, ok := o.Get("message").(string); ok {
		out.Set("message", s)
	}
	if list, ok := o.Get("findings").([]any); ok {
		out.Set("findings", list)
	}
	for _, f := range packetSizeOf(o) {
		out.Set(f.Key, f.Value)
	}
	for _, k := range loopFieldNames {
		if hasKey(o, k) {
			out.Set(k, o.Get(k))
		}
	}
	if hasKey(o, "sessions") {
		out.Set("sessions", o.Get("sessions"))
	}
	return out
}

// packetSizeOf is loop.mjs packetSize.
func packetSizeOf(o ledger.Object) ledger.Object {
	if o.Get("status") != "split_required" && o.Get("stopped") != "split_required" {
		return nil
	}
	out := ledger.Object{{Key: "tokens_in", Value: finiteOrNull(o.Get("tokens_in"))}, {Key: "budget", Value: finiteOrNull(o.Get("budget"))}}
	if s, ok := o.Get("section").(string); ok {
		out = append(out, ledger.Field{Key: "section", Value: s})
	}
	return out
}

var findingID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)

var openRank = map[string]int{"nit": 0, "warning": 1, "critical": 2, "unknown": 3}

// openFindingsOf is gate-check.mjs openFindings.
func openFindingsOf(findings any) []any {
	list, ok := findings.([]any)
	if !ok {
		return []any{}
	}
	order := []string{}
	worst := map[string]string{}
	for i, f := range list {
		o, _ := f.(ledger.Object)
		id, _ := o.Get("id").(string)
		if !findingID.MatchString(id) {
			id = "X" + strconv.Itoa(i+1)
		}
		severity, _ := o.Get("severity").(string)
		if _, known := openRank[severity]; !known || severity == "unknown" {
			severity = "unknown"
		}
		had, seen := worst[id]
		if !seen {
			order = append(order, id)
		}
		if !seen || openRank[severity] > openRank[had] {
			worst[id] = severity
		}
	}
	out := []any{}
	for _, id := range order {
		out = append(out, ledger.Object{{Key: "id", Value: id}, {Key: "severity", Value: worst[id]}})
	}
	return out
}

// recordedVerdict is loop.mjs recordedVerdict: approved and open set on the outcome.
func recordedVerdict(outcome ledger.Object) ledger.Object {
	listed := openFindingsOf(outcome.Get("findings"))
	critical := false
	for _, f := range listed {
		if f.(ledger.Object).Get("severity") == "critical" {
			critical = true
		}
	}
	out := outcome.Clone()
	if outcome.Get("approved") == true && !critical {
		out.Set("approved", true)
		out.Set("open", []any{})
		return out
	}
	out.Set("approved", false)
	out.Set("open", listed)
	return out
}

// needsFileSessions is complete's ledger `sessions`: the needs_file
// diagnostics of the result's sessions only.
func needsFileSessions(v any) []any {
	list, _ := v.([]any)
	fields := []string{"needs_file_round", "needs_file_requested", "needs_file_errors"}
	out := []any{}
	for _, s := range list {
		o, _ := s.(ledger.Object)
		keep := false
		for _, k := range fields {
			if hasKey(o, k) {
				keep = true
			}
		}
		if !keep {
			continue
		}
		row := ledger.Object{}
		for _, k := range append([]string{"lens", "status"}, fields...) {
			if hasKey(o, k) {
				row = append(row, ledger.Field{Key: k, Value: o.Get(k)})
			}
		}
		out = append(out, row)
	}
	return out
}

// Provider-CLI auth variables a reviewer CLI may need (loop.mjs PROVIDER_AUTH_VARS).
var providerAuthVars = map[string]bool{"ANTHROPIC_API_KEY": true, "OPENAI_API_KEY": true, "CODEX_API_KEY": true, "XAI_API_KEY": true, "GROK_API_KEY": true}

var secretName = regexp.MustCompile(`(?i)(_TOKEN|_KEY|_SECRET|_PASSWORD)$`)
var forgeName = regexp.MustCompile(`(?i)^(CODE_FORGE_|JEV_)`)

// SessionEnv is loop.mjs sessionEnv: the env every session gets — no
// code-forge or Jev variables, no secret-shaped names but the provider auth
// ones, and no variable whose value holds one of secrets (8+ characters).
func SessionEnv(env []string, secrets []string) []string {
	live := []string{}
	for _, s := range secrets {
		if len(s) >= 8 {
			live = append(live, s)
		}
	}
	out := []string{}
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		if forgeName.MatchString(name) || (secretName.MatchString(name) && !providerAuthVars[name]) {
			continue
		}
		hit := false
		for _, s := range live {
			if strings.Contains(value, s) {
				hit = true
			}
		}
		if !hit {
			out = append(out, kv)
		}
	}
	return out
}
