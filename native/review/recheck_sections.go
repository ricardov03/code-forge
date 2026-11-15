package review

// B683: the Markdown recheck section by section (fixloop.mjs packRecheck,
// recheckTokens, mergePacketInfo and runRound's parts; sections.mjs
// sectionOf and sectionFindings). A Markdown recheck packet over the budget
// is cut at the current file's headings (packSections), each open finding
// goes to the section whose hunks span or sit nearest its line_start, and
// every section packet is built and held to the whole packet's budget
// before any session runs.

import (
	"math"
	"strconv"

	"github.com/ricardov03/code-forge/native/ledger"
)

// recheckPart is one recheck packet's input: the whole diff (index 0) or a
// section. open holds indices into the plan's pending findings.
type recheckPart struct {
	index   int // 0: the whole file
	heading string
	split   bool // heading's cut split a surrogate pair (markdownHeadings)
	diff    *FileDiff
	open    []int
	keys    []int
}

// recheckStop is stopBeforeRound's packet: a status with its size, and the
// section heading a Markdown section stop names.
type recheckStop struct {
	status     string
	tokensIn   int
	budget     int
	section    string
	hasSection bool
	split      bool // section's cut split a surrogate pair
}

func rsStopOf(p *Packet) *recheckStop {
	return &recheckStop{status: p.Status, tokensIn: p.TokensIn, budget: p.Budget}
}

// recheckTokens is fixloop.mjs recheckTokens: the bare recheck packet
// (lens, diff headers, moved code) plus the open findings' text.
func recheckTokens(lens string, d *FileDiff, open []any, moved string) int {
	text := openFindingsText(open)
	return EstimateTokens(render(lens, nil, "", "", diffSectionOf(d), moved, "", "", "")) + (len(text)+3)/4
}

// rsLineStart is a finding's line_start as JS compares it (missing: NaN).
func rsLineStart(f ledger.Object) float64 {
	v, ok := lookup(f, "line_start")
	if !ok {
		return math.NaN()
	}
	return jsToNumber(v)
}

// rsSectionOf is sections.mjs sectionOf over heading groups: the group whose
// hunks span the finding's line_start, else the nearest (ties: the first);
// -1 when there is no group.
func rsSectionOf(groups []sectionGroup, f ledger.Object) int {
	best, bestDistance := -1, math.Inf(1)
	line := rsLineStart(f)
	for i, g := range groups {
		start, end := math.Inf(1), math.Inf(-1)
		for _, h := range g.diff.Hunks {
			start = math.Min(start, float64(h.NewStart))
			end = math.Max(end, float64(h.NewStart+max(h.NewLines, 1)-1))
		}
		distance := 0.0
		if line < start {
			distance = start - line
		} else if line > end {
			distance = line - end
		}
		if distance < bestDistance {
			best, bestDistance = i, distance
		}
	}
	return best
}

// packRecheck is fixloop.mjs packRecheck: the section parts, or the stop.
func packRecheck(lens string, d *FileDiff, pending []any, budget int, moves *mvMoves) ([]recheckPart, *recheckStop) {
	all := make([]int, len(pending))
	for i := range pending {
		all[i] = i
	}
	findings := func(idx []int) []any {
		out := make([]any, 0, len(idx))
		for _, i := range idx {
			out = append(out, pending[i])
		}
		return out
	}
	whole := func() *recheckStop {
		return &recheckStop{status: "split_required", tokensIn: recheckTokens(lens, d, pending, movedText(moves, d.Content, nil)), budget: budget}
	}
	groups, ok := sectionGroups(d)
	if !ok || len(groups) == 0 {
		return nil, whole()
	}
	keyOf := map[int]int{}
	for i, f := range pending {
		if at := rsSectionOf(groups, asObject(f)); at >= 0 {
			keyOf[i] = groups[at].key
		}
	}
	openIn := func(keys []int) []int {
		out := []int{}
		for _, i := range all {
			k, has := keyOf[i]
			if !has {
				continue
			}
			for _, want := range keys {
				if want == k {
					out = append(out, i)
					break
				}
			}
		}
		return out
	}
	packed := packSections(d, budget, func(part *FileDiff, keys []int) int {
		if keys == nil {
			return recheckTokens(lens, part, pending, movedText(moves, d.Content, nil))
		}
		return recheckTokens(lens, part, findings(openIn(keys)), movedText(moves, d.Content, keys))
	})
	if !packed.ok {
		return nil, &recheckStop{status: "split_required", tokensIn: packed.tokensIn, budget: budget,
			section: packed.section, hasSection: packed.section != "", split: packed.sectionSplit}
	}
	if len(packed.sections) == 0 {
		return nil, whole()
	}
	parts := []recheckPart{}
	listed := map[int]bool{}
	for _, sec := range packed.sections {
		open := openIn(sec.keys)
		for _, i := range open {
			listed[i] = true
		}
		parts = append(parts, recheckPart{index: sec.index, heading: sec.headings[0], split: sec.split[0], diff: sec.diff, open: open, keys: sec.keys})
	}
	// a finding no packet holds goes to the last packet
	last := &parts[len(parts)-1]
	for _, i := range all {
		if !listed[i] {
			last.open = append(last.open, i)
		}
	}
	return parts, nil
}

// mergePacketInfo is fixloop.mjs mergePacketInfo: the first packet's
// context mode (`mixed` once two differ) and the summed context lines.
func mergePacketInfo(info ledger.Object, p *Packet) ledger.Object {
	if info == nil {
		return ledger.Object{{Key: "context_mode", Value: p.ContextMode}, {Key: "context_lines", Value: float64(p.ContextLine)}}
	}
	mode := info.Get("context_mode")
	if mode != p.ContextMode {
		mode = "mixed"
	}
	lines, _ := info.Get("context_lines").(float64)
	return ledger.Object{{Key: "context_mode", Value: mode}, {Key: "context_lines", Value: lines + float64(p.ContextLine)}}
}

// sectionFindings is sections.mjs sectionFindings: copies with the id
// `S<index>.<id>` (the id keeps its place).
func sectionFindings(found []any, index int) []any {
	out := make([]any, 0, len(found))
	for _, v := range found {
		f := asObject(v)
		id, has := lookup(f, "id")
		c := f.Clone()
		c.Set("id", "S"+strconv.Itoa(index)+"."+jsString(id, has))
		out = append(out, c)
	}
	return out
}
