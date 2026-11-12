package review

// B545: the judge packet (packet.mjs assembleJudgePacket) for the dual-lens
// and consensus plans: the judge lens, both reviewers' reports and the hunk
// list (the diff itself only with review.judge_sees_diff). B537: the moved
// text (movedOf) goes before the diff when it is not empty.

import (
	"strings"

	"github.com/ricardov03/code-forge/native/ledger"
)

// contextValue is the packet's context_mode; a judge packet has none (null).
func (p *Packet) contextValue() any {
	if p.ContextMode == "" {
		return nil
	}
	return p.ContextMode
}

// stringifyOne is JSON.stringify(v, null, 1).
func stringifyOne(v any) (string, error) {
	data, err := ledger.Stringify(v, true)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(data), "\n")
	for i, l := range lines {
		n := len(l) - len(strings.TrimLeft(l, " "))
		lines[i] = l[n/2:]
	}
	return strings.Join(lines, "\n"), nil
}

func assembleJudgePacket(d *FileDiff, reportA, reportB any, cfg map[string]any, moved string) (*Packet, error) {
	lens, err := LoadLens("judge")
	if err != nil {
		return nil, err
	}
	a, err := stringifyOne(reportA)
	if err != nil {
		return nil, err
	}
	b, err := stringifyOne(reportB)
	if err != nil {
		return nil, err
	}
	headers := []string{}
	diff := []string{"file: " + d.File, "hunks:"}
	for _, h := range d.Hunks {
		headers = append(headers, h.Header)
		diff = append(diff, "- "+h.Header)
	}
	if section(cfg, "review")["judge_sees_diff"] == true {
		diff = append(diff, "", trimEnd(d.DiffText))
	}
	parts := []string{"# code-forge review packet", "## lens", lens, "## reviewer A", a, "## reviewer B", b}
	if moved != "" {
		parts = append(parts, moved)
	}
	text := strings.Join(append(parts, "## diff", strings.Join(diff, "\n"), ""), "\n")
	tokens := EstimateTokens(text)
	return &Packet{Status: "ok", Text: text, HunkHeaders: headers, TokensIn: tokens, Budget: BudgetFor(cfg, "judge_in")}, nil
}
