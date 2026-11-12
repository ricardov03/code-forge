package review

// B537: a recheck's cross-file evidence (packet.mjs crossFileEvidence,
// context.mjs packExtraDiffs and currentDiffLines): the base diffs of the
// block's other changed files a finding names or the reviewed file imports or
// calls, packed into an equal byte share each under one cap.

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/ricardov03/code-forge/native/gitx"
)

// context.mjs EXTRA_FILE_CAP and EXTRA_BYTE_CAP.
const (
	extraFileCap = 4
	extraByteCap = 16000
)

// extraEvidence is packExtraDiffs' result.
type extraEvidence struct {
	text    string
	files   []string
	omitted int
	trimmed []string
}

// packExtraDiffs is context.mjs packExtraDiffs.
func packExtraDiffs(peers []mvPeerDiff, byteCap int) extraEvidence {
	const head = "## cross-file evidence\n"
	const advice = "Evidence only: decide whether these changes fix the open findings.\n"
	const marker = "\n(trimmed: over budget)"
	selected := peers
	if len(selected) > extraFileCap {
		selected = selected[:extraFileCap]
	}
	for len(selected) > 0 {
		omitted := len(peers) - len(selected)
		tail := ""
		if omitted > 0 {
			tail = fmt.Sprintf("(%d other files omitted: cap)\n", omitted)
		}
		overhead := head + advice + tail
		for _, p := range selected {
			overhead += "### " + p.file + "\n" + marker + "\n"
		}
		share := floorDiv(byteCap-len(overhead), len(selected))
		if share < 64 {
			selected = selected[:len(selected)-1]
			continue
		}
		trimmed := []string{}
		var b strings.Builder
		b.WriteString(head + advice)
		for _, p := range selected {
			body := trimEnd(p.diffText)
			if len(body) > share {
				trimmed = append(trimmed, p.file)
				end := share
				for end > 0 && body[end]&0xc0 == 0x80 {
					end--
				}
				body = body[:end]
				if nl := strings.LastIndex(body, "\n"); nl >= 0 && 2*utf16Len(body[:nl]) >= share {
					body = body[:nl]
				}
				body += marker
			}
			b.WriteString("### " + p.file + "\n" + body + "\n")
		}
		b.WriteString(tail)
		files := []string{}
		for _, p := range selected {
			files = append(files, p.file)
		}
		return extraEvidence{text: b.String(), files: files, omitted: omitted, trimmed: trimmed}
	}
	return extraEvidence{files: []string{}, omitted: len(peers), trimmed: []string{}}
}

// floorDiv is Math.floor(a / b) for b > 0.
func floorDiv(a, b int) int {
	q := a / b
	if a%b != 0 && a < 0 {
		q--
	}
	return q
}

var currentHunkRE = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+\d+(?:,\d+)? @@`)

// currentDiffLines is context.mjs currentDiffLines.
func currentDiffLines(text string) string {
	lines := []string{}
	inHunk := false
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git ") || strings.HasPrefix(line, "### "):
			inHunk = false
		case currentHunkRE.MatchString(line):
			inHunk = true
		case inHunk && (strings.HasPrefix(line, "+") || strings.HasPrefix(line, " ")):
			lines = append(lines, line[1:])
		}
	}
	return strings.Join(lines, "\n")
}

// readRepoTextOrNil is packet.mjs readRepoText where any throw is a null
// (every caller here catches it): missing, refused or unreadable.
func readRepoTextOrNil(repoRoot, rel string) *string {
	data, err := gitx.ReadRegularFileNoFollow(repoRoot, rel)
	if err != nil || data == nil {
		return nil
	}
	s := decodeUTF8(data)
	return &s
}

var mentionTokenRE = regexp.MustCompile(`[\w./-]+`)
var mentionTailRE = regexp.MustCompile(`[.!?]+$`)

// stringValues is Object.values(f).filter((v) => typeof v === 'string').
func stringValues(v any) []string {
	out := []string{}
	for _, f := range asObject(v) {
		if s, ok := f.Value.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// crossFileEvidence is packet.mjs crossFileEvidence over the peers' diffs
// the reader gave (readPeerDiffs; a failure is none).
func crossFileEvidence(repoRoot, file string, read []mvPeerDiff, content *string, findings []any, answer string, cfg map[string]any) extraEvidence {
	empty := packExtraDiffs(nil, 0)
	if len(findings) == 0 {
		return empty
	}
	safe := func(rel string) bool { return !IsSecretLike(rel) && AssertPacketPath(repoRoot, rel) == nil }
	diffs := []mvPeerDiff{}
	for _, p := range read {
		if p.file != file && safe(p.file) {
			diffs = append(diffs, p)
		}
	}
	if len(diffs) == 0 {
		return empty
	}
	allowed := map[string]bool{}
	order := []string{}
	for _, p := range diffs {
		if !allowed[p.file] {
			allowed[p.file] = true
			order = append(order, p.file)
		}
	}
	readFile := func(rel string) *string {
		if rel == file {
			return content
		}
		if allowed[rel] && safe(rel) {
			return readRepoTextOrNil(repoRoot, rel)
		}
		return nil
	}
	linked := map[string]bool{}
	for _, f := range relatedFiles(file, relatedIO{read: readFile, list: func() []string { return []string{} }}) {
		linked[f] = true
	}
	header := ""
	if content != nil {
		header = *content
	}
	diffLines := []string{"@@ -0,0 +1 @@"}
	for _, line := range SplitLines(content) {
		diffLines = append(diffLines, "+"+line)
	}
	for _, d := range findDefinitions(file, diffLines, header, &defIO{read: readFile, list: func() []string { return append([]string{}, order...) }}) {
		linked[d.Path] = true
	}
	mentions := []string{answer}
	for _, f := range findings {
		mentions = append(mentions, stringValues(f)...)
	}
	tokens := map[string]bool{}
	for _, t := range mentionTokenRE.FindAllString(strings.Join(mentions, "\n"), -1) {
		t = strings.TrimPrefix(t, "./")
		tokens[mentionTailRE.ReplaceAllString(t, "")] = true
	}
	named := map[string]bool{}
	selected := []mvPeerDiff{}
	for _, p := range diffs {
		if tokens[p.file] || tokens[path.Base(p.file)] {
			named[p.file] = true
		}
	}
	for _, p := range diffs {
		if named[p.file] || linked[p.file] {
			selected = append(selected, p)
		}
	}
	before := func(a, b mvPeerDiff) bool {
		if named[a.file] != named[b.file] {
			return named[a.file]
		}
		return lessUTF16Str(a.file, b.file)
	}
	for i := 1; i < len(selected); i++ {
		for j := i; j > 0 && before(selected[j], selected[j-1]); j-- {
			selected[j], selected[j-1] = selected[j-1], selected[j]
		}
	}
	return packExtraDiffs(selected, min(extraByteCap, BudgetFor(cfg, "full_in")/8*4))
}
