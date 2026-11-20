package review

import "strings"

// splitSectionGroup bisects an oversized heading group, preferring a blank
// paragraph boundary near the middle. Long paragraphs fall back to lines.
// Hunk coordinates and no-newline markers travel with their original lines.
func splitSectionGroup(g sectionGroup) ([]sectionGroup, bool) {
	lines := strings.Split(strings.TrimSuffix(g.diff.DiffText, "\n"), "\n")
	first := 0
	for first < len(lines) && !strings.HasPrefix(lines[first], "@@") {
		first++
	}
	type span struct {
		piece      *sectionPiece
		start, end int
	}
	spans := []span{}
	count := 0
	for _, h := range g.diff.Hunks {
		end := first + 1
		for end < len(lines) && !strings.HasPrefix(lines[end], "@@") {
			end++
		}
		old, new := h.OldStart, h.NewStart
		if h.OldLines == 0 {
			old++
		}
		if h.NewLines == 0 {
			new++
		}
		p := &sectionPiece{oldCursor: old, newCursor: new, lines: lines[first+1 : end]}
		spans = append(spans, span{p, count, count + len(p.lines)})
		count += len(p.lines)
		first = end
	}
	if count < 2 {
		return nil, false
	}
	// A marker cannot begin a part; it describes the preceding diff line.
	valid := func(at int) bool {
		for _, s := range spans {
			if at >= s.start && at < s.end {
				return !strings.HasPrefix(s.piece.lines[at-s.start], "\\")
			}
		}
		return false
	}
	cut := count / 2
	bestDistance := count
	for _, s := range spans {
		for i, l := range s.piece.lines {
			at := s.start + i + 1
			if at < count/4 || at > count*3/4 || at >= count || !valid(at) {
				continue
			}
			if len(l) > 0 && strings.TrimSpace(l[1:]) == "" {
				distance := at - count/2
				if distance < 0 {
					distance = -distance
				}
				if distance < bestDistance {
					cut, bestDistance = at, distance
				}
			}
		}
	}
	for cut < count && !valid(cut) {
		cut++
	}
	if cut == count {
		cut = count / 2
		for cut > 0 && !valid(cut) {
			cut--
		}
	}
	if cut == 0 || cut == count {
		return nil, false
	}
	headerEnd := strings.Index(g.diff.DiffText, "@@")
	header := g.diff.DiffText[:headerEnd]
	var left, right strings.Builder
	left.WriteString(header)
	right.WriteString(header)
	for _, s := range spans {
		old, new := s.piece.oldCursor, s.piece.newCursor
		boundary := max(0, min(len(s.piece.lines), cut-s.start))
		for side, body := range [][]string{s.piece.lines[:boundary], s.piece.lines[boundary:]} {
			if len(body) == 0 {
				continue
			}
			p := &sectionPiece{oldCursor: old, newCursor: new, lines: body}
			b := &left
			if side == 1 {
				b = &right
			}
			b.WriteString(headerOf(p))
			b.WriteByte('\n')
			for _, l := range body {
				b.WriteString(l)
				b.WriteByte('\n')
				if strings.HasPrefix(l, " ") || strings.HasPrefix(l, "-") {
					old++
				}
				if onNewSide(l) {
					new++
				}
			}
		}
	}
	return []sectionGroup{{g.key, g.heading, reparsed(g.diff, left.String())}, {g.key, g.heading, reparsed(g.diff, right.String())}}, true
}
