package review

// B545: Markdown section packets (sections.mjs, block B54). A Markdown file
// whose diff alone is over the packet budget is reviewed section by
// section: its hunks are cut at the heading lines of the current file and
// packed, in file order, into section packets within the budget.

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/ricardov03/code-forge/native/ledger"
)

var (
	sectionFenceRE   = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})([^\r  ]*)$")
	sectionHeadingRE = regexp.MustCompile(`^ {0,3}#{1,6}(?:[ \t]|$)`)
)

const sectionHeadingMax = 120

// markdownHeadings is sections.mjs markdownHeadings: 1-based line → the
// heading line, trimmed and cut at 120 UTF-16 units. ok false when a cut
// would split a surrogate pair (Node keeps half of it; Go does not).
func markdownHeadings(content *string) (map[int]string, bool) {
	out := map[int]string{}
	lines := SplitLines(content)
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	closeAt := -1
	if len(lines) > 0 && lines[0] == "---" {
		for i := 1; i < len(lines); i++ {
			if lines[i] == "---" || lines[i] == "..." {
				closeAt = i
				break
			}
		}
	}
	var fenceChar byte
	fenceLen := 0
	for i, line := range lines {
		if i <= closeAt {
			continue
		}
		f := sectionFenceRE.FindStringSubmatch(line)
		if fenceLen > 0 {
			if f != nil && f[1][0] == fenceChar && len(f[1]) >= fenceLen && trimJS(f[2]) == "" {
				fenceLen = 0
			}
			continue
		}
		if f != nil && !(f[1][0] == '`' && strings.Contains(f[2], "`")) {
			fenceChar, fenceLen = f[1][0], len(f[1])
			continue
		}
		if sectionHeadingRE.MatchString(line) {
			text, ok := sliceUTF16(trimJS(line), sectionHeadingMax)
			if !ok {
				return nil, false
			}
			out[i+1] = text
		}
	}
	return out, true
}

// sliceUTF16 is JS s.slice(0, n); ok false when the cut splits a pair.
func sliceUTF16(s string, n int) (string, bool) {
	units := 0
	for i, r := range s {
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if units+w > n {
			return s[:i], units == n
		}
		units += w
	}
	return s, true
}

type sectionPiece struct {
	key                  int
	header               *string
	oldCursor, newCursor int
	lines                []string
}

func pieceChanges(p *sectionPiece) bool {
	for _, l := range p.lines {
		if strings.HasPrefix(l, "+") || strings.HasPrefix(l, "-") {
			return true
		}
	}
	return false
}

func headingAtOrBefore(headings map[int]string, line int) int {
	best := 0
	for h := range headings {
		if h <= line && h > best {
			best = h
		}
	}
	return best
}

func onNewSide(l string) bool { return strings.HasPrefix(l, " ") || strings.HasPrefix(l, "+") }

func cutHunk(h Hunk, headerLine string, body []string, headings map[int]string) []*sectionPiece {
	oldCursor, newCursor := h.OldStart, h.NewStart
	if h.OldLines == 0 {
		oldCursor++
	}
	if h.NewLines == 0 {
		newCursor++
	}
	at := newCursor - 1
	if len(body) > 0 && onNewSide(body[0]) {
		at = newCursor
	}
	hl := headerLine
	pieces := []*sectionPiece{{key: headingAtOrBefore(headings, at), header: &hl, oldCursor: oldCursor, newCursor: newCursor}}
	for _, line := range body {
		current := pieces[len(pieces)-1]
		if _, isHeading := headings[newCursor]; onNewSide(line) && isHeading {
			real := false
			for _, l := range current.lines {
				if !strings.HasPrefix(l, "\\") {
					real = true
				}
			}
			if real {
				pieces = append(pieces, &sectionPiece{key: newCursor, oldCursor: oldCursor, newCursor: newCursor})
			}
		}
		last := pieces[len(pieces)-1]
		last.lines = append(last.lines, line)
		switch {
		case strings.HasPrefix(line, " "):
			oldCursor++
			newCursor++
		case strings.HasPrefix(line, "+"):
			newCursor++
		case strings.HasPrefix(line, "-"):
			oldCursor++
		}
	}
	if len(pieces) == 1 {
		return pieces
	}
	for i := 0; i < len(pieces) && len(pieces) > 1; {
		p := pieces[i]
		if pieceChanges(p) {
			i++
			continue
		}
		if i+1 < len(pieces) {
			next := pieces[i+1]
			pieces[i+1] = &sectionPiece{key: next.key, header: next.header, oldCursor: p.oldCursor, newCursor: p.newCursor, lines: append(append([]string{}, p.lines...), next.lines...)}
		} else {
			pieces[i-1].lines = append(pieces[i-1].lines, p.lines...)
		}
		pieces = append(pieces[:i], pieces[i+1:]...)
	}
	for _, p := range pieces {
		p.header = nil
	}
	if len(pieces) == 1 {
		pieces[0].header = &hl
	}
	return pieces
}

func headerOf(p *sectionPiece) string {
	if p.header != nil {
		return *p.header
	}
	oldCount, newCount := 0, 0
	for _, l := range p.lines {
		if strings.HasPrefix(l, " ") || strings.HasPrefix(l, "-") {
			oldCount++
		}
		if onNewSide(l) {
			newCount++
		}
	}
	o, n := p.oldCursor, p.newCursor
	if oldCount == 0 {
		o--
	}
	if newCount == 0 {
		n--
	}
	return "@@ -" + strconv.Itoa(o) + "," + strconv.Itoa(oldCount) + " +" + strconv.Itoa(n) + "," + strconv.Itoa(newCount) + " @@"
}

type sectionGroup struct {
	key     int
	heading string
	diff    *FileDiff
}

func reparsed(d *FileDiff, text string) *FileDiff {
	hunks, plus, minus := ParseDiff(text)
	return &FileDiff{File: d.File, Kind: d.Kind, DiffText: text, Content: d.Content, Hunks: hunks, PlusCount: plus, MinusCount: minus}
}

// sectionGroups is sections.mjs sectionGroups; ok false is Node's null.
// ErrUnsupported when a heading cut Go does not match (see markdownHeadings).
func sectionGroups(d *FileDiff) ([]sectionGroup, bool, error) {
	headings, cut := markdownHeadings(d.Content)
	if !cut {
		return nil, false, unsupported("a Markdown heading cut inside a surrogate pair is not ported")
	}
	all := strings.Split(d.DiffText, "\n")
	if all[len(all)-1] == "" {
		all = all[:len(all)-1]
	}
	first := -1
	for i, l := range all {
		if strings.HasPrefix(l, "@@") {
			first = i
			break
		}
	}
	if first < 0 {
		return []sectionGroup{}, len(d.Hunks) == 0, nil
	}
	fileHeader := all[:first]
	pieces := []*sectionPiece{}
	h := 0
	for i := first; i < len(all); {
		end := i + 1
		for end < len(all) && !strings.HasPrefix(all[end], "@@") {
			end++
		}
		if h >= len(d.Hunks) || !strings.HasPrefix(all[i], d.Hunks[h].Header) {
			return nil, false, nil
		}
		pieces = append(pieces, cutHunk(d.Hunks[h], all[i], all[i+1:end], headings)...)
		h++
		i = end
	}
	if h != len(d.Hunks) {
		return nil, false, nil
	}
	type raw struct {
		key    int
		pieces []*sectionPiece
	}
	groups := []*raw{}
	for _, p := range pieces {
		if n := len(groups); n > 0 && groups[n-1].key == p.key {
			groups[n-1].pieces = append(groups[n-1].pieces, p)
		} else {
			groups = append(groups, &raw{p.key, []*sectionPiece{p}})
		}
	}
	out := []sectionGroup{}
	for _, g := range groups {
		heading, ok := headings[g.key]
		if !ok {
			heading = "(before first heading)"
		}
		lines := append([]string{}, fileHeader...)
		for _, p := range g.pieces {
			lines = append(append(lines, headerOf(p)), p.lines...)
		}
		out = append(out, sectionGroup{g.key, heading, reparsed(d, strings.Join(lines, "\n")+"\n")})
	}
	return out, true, nil
}

func mergeGroups(d *FileDiff, groups []sectionGroup) *FileDiff {
	if len(groups) == 1 {
		return groups[0].diff
	}
	bodyOf := func(text string) string {
		if strings.HasPrefix(text, "@@") {
			return text
		}
		return text[strings.Index(text, "\n@@")+1:]
	}
	head := groups[0].diff.DiffText
	fileHeader := ""
	if !strings.HasPrefix(head, "@@") {
		fileHeader = head[:strings.Index(head, "\n@@")+1]
	}
	var b strings.Builder
	b.WriteString(fileHeader)
	for _, g := range groups {
		b.WriteString(bodyOf(g.diff.DiffText))
	}
	return reparsed(d, b.String())
}

type mdSection struct {
	index    int
	headings []string
	keys     []int
	diff     *FileDiff
}

// packResult is packSections' answer: sections, or split at tokensIn with
// the named section ("" = none).
type packResult struct {
	ok       bool
	sections []mdSection
	tokensIn int
	section  string
}

// measure gets the part and its heading-group keys (nil = the whole file).
func packSections(d *FileDiff, budget int, measure func(*FileDiff, []int) int) (packResult, error) {
	groups, ok, err := sectionGroups(d)
	if err != nil {
		return packResult{}, err
	}
	if !ok || len(groups) == 0 {
		return packResult{tokensIn: measure(d, nil)}, nil
	}
	size := func(set []sectionGroup) int {
		keys := make([]int, 0, len(set))
		for _, g := range set {
			keys = append(keys, g.key)
		}
		return measure(mergeGroups(d, set), keys)
	}
	sections := []mdSection{}
	open := []sectionGroup{}
	flush := func() {
		if len(open) == 0 {
			return
		}
		s := mdSection{index: len(sections) + 1, diff: mergeGroups(d, open)}
		for _, g := range open {
			s.headings = append(s.headings, g.heading)
			s.keys = append(s.keys, g.key)
		}
		sections = append(sections, s)
		open = nil
	}
	for _, g := range groups {
		alone := size([]sectionGroup{g})
		if alone > budget {
			return packResult{tokensIn: alone, section: g.heading}, nil
		}
		if len(open) > 0 && size(append(append([]sectionGroup{}, open...), g)) > budget {
			flush()
		}
		open = append(open, g)
	}
	flush()
	return packResult{ok: true, sections: sections}, nil
}

// sectionPacket is one Markdown section and its packets (one per reviewer).
type sectionPacket struct {
	sec     mdSection
	packets []*Packet
}

// prepareSections is reviewFile's B54 branch before any session: a
// split_required early result, or every section's packets assembled.
func prepareSections(p *prepared, d *FileDiff, split *Packet, in Input, paired string, result func(ledger.Object, ...ledger.Field) ledger.Object) (*prepared, error) {
	lenses := []string{}
	for _, spec := range p.plan.reviewers {
		text, err := LoadLens(spec.lens)
		if err != nil {
			return nil, err
		}
		lenses = append(lenses, text)
	}
	measure := func(part *FileDiff, keys []int) int {
		moved := p.extras.movedOf(keys)
		most := 0
		for i, lens := range lenses {
			if n := EstimateTokens(render(lens, nil, "", "", diffSectionOf(part), moved, "", "", "")); i == 0 || n > most {
				most = n
			}
		}
		return most
	}
	splitOutcome := func(tokens, budget int, section string) *prepared {
		tail := []ledger.Field{{Key: "tokens_in", Value: float64(tokens)}, {Key: "budget", Value: float64(budget)}}
		if section != "" {
			tail = append(tail, ledger.Field{Key: "section", Value: section})
		}
		tail = append(tail, ledger.Field{Key: "sessions", Value: []any{}})
		p.early = result(ledger.Object{{Key: "status", Value: "split_required"}, {Key: "approved", Value: false}}, tail...)
		return p
	}
	packed, err := packSections(d, split.Budget, measure)
	if err != nil {
		return nil, err
	}
	if !packed.ok {
		return splitOutcome(packed.tokensIn, split.Budget, packed.section), nil
	}
	if len(packed.sections) == 0 {
		return splitOutcome(split.TokensIn, split.Budget, ""), nil
	}
	out := []sectionPacket{}
	for _, sec := range packed.sections {
		packets, over, err := packetsFor(p.plan, sec.diff, in, paired, p.extras.movedOf(sec.keys))
		if err != nil {
			return nil, err
		}
		if over != nil {
			heading := "(before first heading)"
			if len(sec.headings) > 0 {
				heading = sec.headings[0]
			}
			return splitOutcome(over.TokensIn, over.Budget, heading), nil
		}
		out = append(out, sectionPacket{sec, packets})
	}
	p.sections = out
	return p, nil
}

// fileSections reviews each section in file order: findings S<i>.<id>,
// summaries S<i>: ..., the first unavailable section ends the file.
func fileSections(p *prepared, in Input, deps Deps, result func(ledger.Object, ...ledger.Field) ledger.Object) (ledger.Object, error) {
	list := []any{}
	for _, sp := range p.sections {
		headings := []any{}
		for _, h := range sp.sec.headings {
			headings = append(headings, h)
		}
		list = append(list, ledger.Object{{Key: "index", Value: float64(sp.sec.index)}, {Key: "headings", Value: headings}, {Key: "hunks", Value: float64(len(sp.sec.diff.Hunks))}})
	}
	sessions, findings, summaries, approved := []any{}, []any{}, []string{}, true
	for _, sp := range p.sections {
		index := float64(sp.sec.index)
		extra := append(p.stepUp.Clone(), ledger.Field{Key: "section", Value: index}, ledger.Field{Key: "sections", Value: float64(len(p.sections))})
		part, err := reviewPart(p, sp.sec.diff, sp.packets, in, deps, extra, sp.sec.keys)
		if err != nil {
			return nil, err
		}
		for _, s := range part.sessions {
			c := s.Clone()
			c.Set("section", index)
			sessions = append(sessions, c)
		}
		if !part.reviewed {
			return result(ledger.Object{{Key: "status", Value: "unavailable"}, {Key: "reason", Value: part.reason}, {Key: "approved", Value: false}},
				ledger.Field{Key: "sections", Value: list}, ledger.Field{Key: "sessions", Value: sessions}), nil
		}
		approved = approved && part.approved
		fs, _ := part.findings.([]any)
		for _, f := range fs {
			fo, _ := f.(ledger.Object)
			c := fo.Clone()
			id, _ := fo.Get("id").(string)
			c.Set("id", "S"+strconv.Itoa(sp.sec.index)+"."+id)
			findings = append(findings, c)
		}
		summary, _ := part.summary.(string)
		summaries = append(summaries, "S"+strconv.Itoa(sp.sec.index)+": "+summary)
	}
	return result(ledger.Object{{Key: "status", Value: "reviewed"}, {Key: "approved", Value: approved}},
		ledger.Field{Key: "summary", Value: strings.Join(summaries, "\n")}, ledger.Field{Key: "findings", Value: findings},
		ledger.Field{Key: "sessions", Value: sessions}, ledger.Field{Key: "sections", Value: list}), nil
}
