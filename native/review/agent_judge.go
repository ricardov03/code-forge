package review

// Exports for the fresh agent judge of block waive --yes and block reopen
// --yes (agent-waiver.mjs judgeAgentReview, B612).

// Ladder status values of JudgeLadder.
const (
	LadderOK          = "ok"
	LadderBroken      = "broken"      // resolveLevel or the fallback map throws: the judge failed
	LadderUnsupported = "unsupported" // Node reads the config in a way the port does not
)

// JudgeLadder is spawnWithBrokenFallback's model list for a level: the
// resolved model, then each distinct fallback model, each a config pinned to
// that one model with no fallback, skipping duplicates and closed-book
// refusals for role.
func JudgeLadder(cfg map[string]any, level, role string) ([]map[string]any, string) {
	l, _ := section(cfg, "levels")[level].(map[string]any)
	if l == nil {
		return nil, LadderBroken
	}
	model, _ := l["model"].(string)
	if model == "" {
		return nil, LadderBroken
	}
	p := l["provider"]
	if p == nil {
		p = cfg["provider"]
	}
	provider, _ := p.(string)
	if provider == "" {
		return nil, LadderBroken
	}
	first := levelEntry{provider: provider, model: model}
	first.effort, first.hasEffort = l["effort"]
	models := []levelEntry{first}
	if raw := l["fallback"]; raw != nil {
		list, ok := raw.([]any)
		if !ok {
			return nil, LadderBroken
		}
		for _, f := range list {
			m, ok := f.(map[string]any)
			if !ok {
				return nil, LadderUnsupported
			}
			e := levelEntry{provider: provider, raw: m}
			if v := m["provider"]; v != nil {
				s, ok := v.(string)
				if !ok {
					return nil, LadderUnsupported
				}
				e.provider = s
			}
			s, ok := m["model"].(string)
			if !ok {
				return nil, LadderUnsupported
			}
			e.model = s
			e.effort, e.hasEffort = m["effort"]
			models = append(models, e)
		}
	}
	out := []map[string]any{}
	seen := map[string]bool{}
	for _, e := range models {
		key := e.provider + "\x00" + e.model
		if seen[key] || reviewClosedBook(e.provider, role, openBookAllowed(cfg)) {
			continue
		}
		seen[key] = true
		out = append(out, pinLevel(cfg, level, e, []any{}))
	}
	return out, LadderOK
}

// AgentEvidence is packExtraDiffs' result: {text, files, omitted, trimmed, bytes}.
type AgentEvidence struct {
	Text    string
	Files   []string
	Omitted int
	Trimmed []string
	Bytes   int
}

// AgentCrossFileEvidence is packet.mjs crossFileEvidence with a peer list:
// none without peers or findings, and a failed peer diff read is none.
func AgentCrossFileEvidence(repoRoot, file, base string, peers []string, content string, findings []any, answer string, cfg map[string]any) AgentEvidence {
	var e extraEvidence
	if len(peers) == 0 || len(findings) == 0 {
		e = packExtraDiffs(nil, 0)
	} else {
		diffs, err := readPeerDiffs(repoRoot, base, file, peers)
		if err != nil {
			diffs = nil
		}
		e = crossFileEvidence(repoRoot, file, diffs, &content, findings, answer, cfg)
	}
	files, trimmed := e.files, e.trimmed
	if files == nil {
		files = []string{}
	}
	if trimmed == nil {
		trimmed = []string{}
	}
	return AgentEvidence{Text: e.text, Files: files, Omitted: e.omitted, Trimmed: trimmed, Bytes: len(e.text)}
}
