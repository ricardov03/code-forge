package review

// B545: a light block's batch round (engine.mjs reviewLightBatch and
// packet.mjs assembleLightPacket): every changed file of the block in one
// full-lens L2 session. The worker's lightRound (worker/light.go) owns the
// lock, the cap and the per-file outcomes.

import (
	"os"
	"strings"

	"github.com/ricardov03/code-forge/native/ledger"
)

// LightInput is reviewLightBatch's input.
type LightInput struct {
	RepoRoot     string
	Files        []string
	Base         string // "" = HEAD
	Cfg          map[string]any
	WorkDir      string
	FactsExcerpt string
	RulesDigest  string
}

// LightBatch is one batch round read before any write: the diffs and the
// packet, or ReadErr (a file Node throws on; the caller writes its batch
// row first, then fails like Node).
type LightBatch struct {
	in      LightInput
	packet  *Packet
	ReadErr error
}

const lightLensTail = "\nReview ALL files below in one round. Findings must name their exact file path. Echo every listed hunk in file order, including duplicates."

// PrepareLight reads every file's diff and assembles the batch packet; a
// part Go does not port is ErrUnsupported, nothing written.
func PrepareLight(in LightInput) (*LightBatch, error) {
	diffs := []*FileDiff{}
	for _, f := range in.Files {
		d, err := ReadFileDiff(in.RepoRoot, f, in.Base)
		if err != nil {
			if IsUnsupported(err) {
				return nil, err
			}
			return &LightBatch{in: in, ReadErr: err}, nil
		}
		diffs = append(diffs, d)
	}
	if err := levelGuards(in.Cfg, "L2"); err != nil {
		return nil, err
	}
	p, err := assembleLightPacket(diffs, in.Cfg, in.RulesDigest, in.FactsExcerpt)
	if err != nil {
		return nil, err
	}
	return &LightBatch{in: in, packet: p}, nil
}

// assembleLightPacket is packet.mjs assembleLightPacket: the related file
// names of every diff (B537, the files not shown, sorted, at most 12) go in
// until the packet is over the budget.
func assembleLightPacket(diffs []*FileDiff, cfg map[string]any, rulesDigest, facts string) (*Packet, error) {
	full, err := LoadLens("full")
	if err != nil {
		return nil, err
	}
	lens := full + lightLensTail
	parts := []string{}
	headers := []string{}
	for _, d := range diffs {
		parts = append(parts, "### "+d.File+"\n"+diffSectionOf(d))
		for _, h := range d.Hunks {
			headers = append(headers, h.Header)
		}
	}
	diffText := strings.Join(parts, "\n\n")
	budget := BudgetFor(cfg, "full_in")
	rd := rulesDigest
	digest := SplitLines(&rd)
	if len(digest) > DigestMaxLines {
		digest = digest[:DigestMaxLines]
	}
	context := func(mode string) string {
		out := []string{}
		for _, d := range diffs {
			out = append(out, "### "+d.File+"\n"+BuildContext(d.File, d.Content, d.Hunks, cfg, mode).Text)
		}
		return strings.Join(out, "\n\n")
	}
	mode := "auto"
	ctx := context(mode)
	shown, seen := map[string]bool{}, map[string]bool{}
	for _, d := range diffs {
		shown[d.File] = true
	}
	related := []string{}
	for _, d := range diffs {
		for _, f := range d.Related {
			if !seen[f] && !shown[f] {
				related = append(related, f)
			}
			seen[f] = true
		}
	}
	sortUTF16(related)
	if len(related) > 12 {
		related = related[:12]
	}
	relatedSection := ""
	if len(related) > 0 {
		relatedSection = "## related files (not shown)\n" + strings.Join(related, "\n")
	}
	text := render(lens, digest, facts, ctx, diffText, "", "", relatedSection, "")
	if EstimateTokens(text) > budget && len(related) > 0 {
		text = render(lens, digest, facts, ctx, diffText, "", "", "", "")
	}
	if EstimateTokens(text) > budget {
		mode = "minimal"
		ctx = context(mode)
		text = render(lens, digest, facts, ctx, diffText, "", "", "", "")
	}
	for EstimateTokens(text) > budget && len(digest) > 0 {
		digest = digest[:len(digest)-1]
		text = render(lens, digest, facts, ctx, diffText, "", "", "", "")
	}
	tokens := EstimateTokens(text)
	status := "ok"
	if tokens > budget {
		status = "split_required"
	}
	return &Packet{Status: status, Text: text, HunkHeaders: headers, ContextMode: mode, DigestLines: len(digest), TokensIn: tokens, Budget: budget, OverBudget: tokens > budget}, nil
}

// LightSpawnChecks are the configs a batch session may spawn on: the L2
// level and each pinned ladder entry (a batch never takes a second level).
func LightSpawnChecks(cfg map[string]any) []SpawnCheck {
	out := []SpawnCheck{{Level: "L2", Role: "reviewer", Cfg: cfg}}
	if lv, ok := resolveLevelOf(cfg, "L2"); ok {
		for _, e := range lv.entries() {
			if !reviewClosedBook(e.provider, "reviewer", openBookAllowed(cfg)) {
				out = append(out, SpawnCheck{Level: "L2", Role: "reviewer", Cfg: pinLevel(cfg, "L2", e, []any{})})
			}
		}
	}
	return out
}

// RecheckSpawnChecks are the configs a recheck session may spawn on (B545):
// the run's L2 level (its first attempt pins nothing), and the second level
// with each of its runnable ladder entries pinned.
func RecheckSpawnChecks(cfg map[string]any) []SpawnCheck {
	out := []SpawnCheck{{Level: "L2", Role: "reviewer", Cfg: cfg}}
	second := secondLevelFor(cfg, "L2", nil, nil, "reviewer")
	if !second.ok {
		return out
	}
	out = append(out, SpawnCheck{Level: "L2", Role: "reviewer", Cfg: second.cfg})
	if lv, ok := resolveLevelOf(second.cfg, "L2"); ok {
		for _, e := range lv.entries() {
			if !reviewClosedBook(e.provider, "reviewer", openBookAllowed(second.cfg)) {
				out = append(out, SpawnCheck{Level: "L2", Role: "reviewer", Cfg: pinLevel(second.cfg, "L2", e, []any{})})
			}
		}
	}
	return out
}

// Run is reviewLightBatch after its reads: the split result, or one full
// L2 reviewer session (no schema fallback) and its verdict.
func (b *LightBatch) Run(deps Deps) (ledger.Object, error) {
	if b.ReadErr != nil {
		return nil, b.ReadErr
	}
	head := ledger.Object{{Key: "engine", Value: "adaptive"}, {Key: "depth", Value: "full"}}
	result := func(fields ...ledger.Field) ledger.Object {
		return append(head.Clone(), fields...)
	}
	p := b.packet
	if p.Status != "ok" {
		return result(ledger.Field{Key: "status", Value: "split_required"}, ledger.Field{Key: "approved", Value: false},
			ledger.Field{Key: "tokens_in", Value: float64(p.TokensIn)}, ledger.Field{Key: "budget", Value: float64(p.Budget)}, ledger.Field{Key: "sessions", Value: []any{}}), nil
	}
	if err := os.MkdirAll(b.in.WorkDir, 0o700); err != nil {
		return nil, err
	}
	in := Input{RepoRoot: b.in.RepoRoot, Cfg: b.in.Cfg, WorkDir: b.in.WorkDir}
	final, err := runOneWith(sessionSpec{lens: "full", role: "reviewer", level: "L2"}, p.Text, p, in, deps, false)
	if err != nil {
		return nil, err
	}
	sessions := []any{publicSummary(final)}
	unavailable := func(reason any) ledger.Object {
		return result(ledger.Field{Key: "status", Value: "unavailable"}, ledger.Field{Key: "reason", Value: reason}, ledger.Field{Key: "approved", Value: false}, ledger.Field{Key: "sessions", Value: sessions})
	}
	if final.Get("status") != "ok" {
		return unavailable(final.Get("reason")), nil
	}
	review := final.Get("review").(ledger.Object)
	findings, _ := review.Get("findings").([]any)
	for _, f := range findings {
		fo, _ := f.(ledger.Object)
		named, _ := fo.Get("file").(string)
		listed := false
		for _, file := range b.in.Files {
			if file == named && fo.Get("file") != nil {
				listed = true
			}
		}
		if !listed {
			return unavailable("finding-file"), nil
		}
	}
	if needs, ok := review.Get("needs_file").([]any); ok && len(needs) > 0 {
		return unavailable("needs-file"), nil
	}
	return result(ledger.Field{Key: "status", Value: "reviewed"}, ledger.Field{Key: "approved", Value: review.Get("passed") == true},
		ledger.Field{Key: "findings", Value: review.Get("findings")}, ledger.Field{Key: "sessions", Value: sessions}), nil
}
