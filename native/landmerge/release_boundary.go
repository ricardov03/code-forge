package landmerge

import "strings"

// Merge by release section when a branch released the base's Unreleased
// entries. A whole-file union can attach the older lane's additions to that
// new release. Section keys keep those additions in Unreleased.
func mergeReleaseBoundary(base, ours, theirs string) (string, bool, error) {
	type document struct {
		keys   []string
		bodies map[string]string
	}
	parse := func(text string) document {
		keys, bodies := changelogSections(strings.SplitAfter(text, "\n"))
		d := document{keys, map[string]string{}}
		for i, k := range keys {
			d.bodies[k] = strings.Join(bodies[i], "")
		}
		return d
	}
	b, o, t := parse(base), parse(ours), parse(theirs)
	newRelease := func(d document) bool {
		for _, k := range d.keys {
			if k != "" && k != "## [Unreleased]" && b.bodies[k] == "" {
				return true
			}
		}
		return false
	}
	if !newRelease(o) && !newRelease(t) {
		return "", false, nil
	}
	// Use the release branch's section order, retaining sections unique to the
	// other side. Entries already shipped are removed only when byte-identical
	// to a base entry, so edits and new multiline entries survive.
	order, other := o, t
	if !newRelease(o) {
		order, other = t, o
	}
	keys := append([]string{}, order.keys...)
	for _, k := range other.keys {
		if order.bodies[k] == "" {
			keys = append(keys, k)
		}
	}
	released := map[string]bool{}
	for _, d := range []document{o, t} {
		for _, k := range d.keys {
			if k != "" && k != "## [Unreleased]" {
				for _, entry := range releaseEntries(d.bodies[k]) {
					released[entry] = true
				}
			}
		}
	}
	baseEntries := map[string]bool{}
	for _, entry := range releaseEntries(b.bodies["## [Unreleased]"]) {
		baseEntries[entry] = true
	}
	var out strings.Builder
	for _, k := range keys {
		bs, os, ts := b.bodies[k], o.bodies[k], t.bodies[k]
		if k == "## [Unreleased]" {
			drop := map[string]bool{}
			for entry := range baseEntries {
				if released[entry] {
					drop[entry] = true
				}
			}
			bs, os, ts = releaseDrop(bs, drop), releaseDrop(os, drop), releaseDrop(ts, drop)
		}
		merged, ok, err := threeWay(bs, os, ts, true)
		if err != nil || !ok {
			return "", true, err
		}
		out.WriteString(merged)
	}
	return FoldChangelog(out.String(), ours), true, nil
}

// A bullet and its continuation lines are one entry. Blank separators are
// excluded from its identity, but preserved when the entry is kept.
func releaseChunks(text string) []string {
	var chunks []string
	for _, line := range strings.SplitAfter(text, "\n") {
		if len(chunks) == 0 || strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "#") {
			chunks = append(chunks, line)
		} else {
			chunks[len(chunks)-1] += line
		}
	}
	return chunks
}
func releaseEntries(text string) []string {
	var out []string
	for _, chunk := range releaseChunks(text) {
		if strings.HasPrefix(chunk, "- ") {
			out = append(out, strings.TrimRight(chunk, "\n"))
		}
	}
	return out
}
func releaseDrop(text string, drop map[string]bool) string {
	var out strings.Builder
	for _, chunk := range releaseChunks(text) {
		if !drop[strings.TrimRight(chunk, "\n")] {
			out.WriteString(chunk)
		}
	}
	body := out.String()
	// Empty kind headings left by shipped entries must not act as anchors
	// that let a deletion absorb a lane's still-populated group.
	chunks := strings.SplitAfter(body, "\n")
	var kept strings.Builder
	for i := 0; i < len(chunks); {
		if !strings.HasPrefix(chunks[i], "### ") {
			kept.WriteString(chunks[i])
			i++
			continue
		}
		j := i + 1
		for j < len(chunks) && !strings.HasPrefix(chunks[j], "#") {
			j++
		}
		if strings.TrimSpace(strings.Join(chunks[i+1:j], "")) != "" {
			kept.WriteString(strings.Join(chunks[i:j], ""))
		}
		i = j
	}
	return kept.String()
}
