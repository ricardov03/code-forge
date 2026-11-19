package landmerge

import "strings"

// A table row is a line that starts with `|`. Rows are keyed by their table's
// first line (the header) and their own first cell, so two blocks that each
// add or change a different row of one table merge row by row.

type tableRow struct {
	key  string
	line string
}

func isRow(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), "|") }

func firstCell(line string) string {
	cells := strings.Split(strings.TrimSpace(trimmed(line)), "|")
	if len(cells) < 2 {
		return strings.TrimSpace(trimmed(line))
	}
	return strings.TrimSpace(cells[1])
}

// tableParse keys every row line; skeleton is the text without its rows.
// ok is false when one table has two rows with the same key.
func tableParse(text []string) (keys []string, rows map[string]string, skeleton []string, ok bool) {
	keys, rows = make([]string, len(text)), map[string]string{}
	header := ""
	for i, line := range text {
		if !isRow(line) {
			header = ""
			skeleton = append(skeleton, line)
			continue
		}
		if header == "" {
			header = trimmed(line)
		}
		key := header + "\x00" + firstCell(line)
		if _, dup := rows[key]; dup {
			return nil, nil, nil, false
		}
		keys[i], rows[key] = key, trimmed(line)
	}
	return keys, rows, skeleton, true
}

func sameLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if trimmed(a[i]) != trimmed(b[i]) {
			return false
		}
	}
	return true
}

// Table merges a markdown file whose tables both sides changed: per row, a
// side that left the row as it was takes the other side's row; a row only
// theirs added goes after the row it follows in theirs (after any rows ours
// added there). ok is false when both sides changed one row differently, a
// side changed the text outside the tables, or a new row has no place.
func Table(base, ours, theirs string) (string, bool) {
	terminated := strings.HasSuffix(ours, "\n") || ours == ""
	norm := func(text string) []string {
		out := lines(text)
		if len(out) > 0 && !strings.HasSuffix(out[len(out)-1], "\n") {
			out[len(out)-1] += "\n"
		}
		return out
	}
	b, o, t := norm(base), norm(ours), norm(theirs)
	_, bRows, bSkel, ok1 := tableParse(b)
	oKeys, oRows, oSkel, ok2 := tableParse(o)
	tKeys, tRows, tSkel, ok3 := tableParse(t)
	if !ok1 || !ok2 || !ok3 {
		return "", false
	}
	if !sameLines(tSkel, bSkel) && !sameLines(tSkel, oSkel) {
		return "", false
	}
	// result rows by key, starting from ours.
	result := make([]tableRow, len(o))
	for i, line := range o {
		result[i] = tableRow{key: oKeys[i], line: line}
	}
	index := func(key string) int {
		for i, r := range result {
			if r.key == key {
				return i
			}
		}
		return -1
	}
	all := map[string]bool{}
	for k := range bRows {
		all[k] = true
	}
	for k := range oRows {
		all[k] = true
	}
	for k := range tRows {
		all[k] = true
	}
	inserts := []int{}
	for i, key := range tKeys {
		if key == "" {
			continue
		}
		_, inB := bRows[key]
		_, inO := oRows[key]
		if !inB && !inO {
			inserts = append(inserts, i)
		}
	}
	for key := range all {
		bv, inB := bRows[key]
		ov, inO := oRows[key]
		tv, inT := tRows[key]
		switch {
		case inT == inB && tv == bv, inT == inO && tv == ov:
			// theirs left the row as it was, or made ours' change
		case inO == inB && ov == bv:
			if !inB {
				continue // a new theirs row, placed below
			}
			at := index(key)
			if inT {
				result[at].line = tv + "\n"
			} else {
				result = append(result[:at], result[at+1:]...)
			}
		default:
			return "", false
		}
	}
	for _, i := range inserts {
		prev := -1
		for j := i - 1; j >= 0 && isRow(t[j]); j-- {
			if tKeys[j] != "" {
				prev = j
				break
			}
		}
		if prev < 0 {
			return "", false
		}
		at := index(tKeys[prev])
		if at < 0 {
			return "", false
		}
		at++
		for at < len(result) && result[at].key != "" {
			if _, inB := bRows[result[at].key]; inB {
				break
			}
			if _, inT := tRows[result[at].key]; inT {
				break
			}
			at++
		}
		row := tableRow{key: tKeys[i], line: tRows[tKeys[i]] + "\n"}
		result = append(result[:at], append([]tableRow{row}, result[at:]...)...)
	}
	var out strings.Builder
	for _, r := range result {
		out.WriteString(r.line)
	}
	merged := out.String()
	if !terminated {
		merged = strings.TrimSuffix(merged, "\n")
	}
	return merged, true
}
