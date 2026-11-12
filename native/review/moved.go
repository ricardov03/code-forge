package review

// B537: src/review/moved.mjs — code moved between this file and another
// changed file of the block (both diffed against the block base), rendered as
// the packet's `## moved code` section; a Markdown section packet carries
// only the moves under its heading groups (movedText with keys).

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/ricardov03/code-forge/native/gitx"
)

const (
	mvMinLineAlnum  = 4
	mvMinMovedLines = 3
	mvMinMovedChars = 120
	mvMaxEntries    = 12
	mvMaxPeers      = 50
	mvMaxDiffBytes  = 2 * 1024 * 1024
	mvMaxCandidates = 200
	mvHead          = "## moved code"
)

// mvGitDiff is moved.mjs GIT_DIFF without the leading `git`.
var mvGitDiff = []string{"-c", "core.quotePath=false", "-c", "diff.noprefix=false", "-c", "diff.mnemonicPrefix=false", "diff", "--no-color", "--no-ext-diff", "--src-prefix=a/", "--dst-prefix=b/"}

var (
	mvControl  = regexp.MustCompile(`[\x{0}-\x{1f}\x{7f}-\x{9f}\x{2028}\x{2029}\x{200e}\x{200f}\x{202a}-\x{202e}\x{2066}-\x{2069}]`)
	mvQuoted   = regexp.MustCompile(`["\\]`)
	mvWSRun    = regexp.MustCompile(jsWS + `+`)
	mvHunkRE   = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)
	mvChunkCut = regexp.MustCompile(`(?:^|[\n\r\x{2028}\x{2029}])diff --git `)
)

func mvPrintable(p string) bool { return !mvControl.MatchString(p) && !mvQuoted.MatchString(p) }

func mvNormalise(line string) string { return mvWSRun.ReplaceAllString(trimJS(line), " ") }

func mvSignificant(norm string) bool {
	n := 0
	for _, r := range norm {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			n++
		}
	}
	return n >= mvMinLineAlnum
}

// mvSig is SigLine: at is its position in the current file (a removed
// line: the current line just before the cut, 0 at the top).
type mvSig struct {
	line int
	norm string
	at   int
}

type mvRun []mvSig

// mvDiffRuns is diffRuns(): the removed and added runs, significant lines only.
func mvDiffRuns(diffText string) (removed, added []mvRun) {
	if len(diffText) > mvMaxDiffBytes {
		return nil, nil
	}
	lines := strings.Split(diffText, "\n")
	first := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "@@") {
			first = i
			break
		}
	}
	if first < 0 {
		return nil, nil
	}
	oldNo, newNo := 0, 0
	open := byte(0)
	run := mvRun{}
	closeRun := func() {
		if open == '-' && len(run) > 0 {
			removed = append(removed, run)
		}
		if open == '+' && len(run) > 0 {
			added = append(added, run)
		}
		open = 0
		run = mvRun{}
	}
	for _, line := range lines[first:] {
		if m := mvHunkRE.FindStringSubmatch(line); m != nil {
			closeRun()
			oldNo, _ = strconv.Atoi(m[1])
			newNo, _ = strconv.Atoi(m[2])
			if m[3] == "0" {
				newNo++
			}
			continue
		}
		sign := byte(0)
		if line != "" {
			sign = line[0]
		}
		if sign != '-' && sign != '+' {
			closeRun()
			if sign == ' ' {
				oldNo++
				newNo++
			}
			continue
		}
		if open != sign {
			closeRun()
		}
		open = sign
		at := newNo - 1
		no := newNo
		if sign == '-' {
			no = oldNo
			oldNo++
		} else {
			newNo++
			at = no
		}
		norm := mvNormalise(line[1:])
		if mvSignificant(norm) {
			run = append(run, mvSig{no, norm, at})
		}
	}
	closeRun()
	return removed, added
}

type mvMatch struct {
	start, end         int
	at                 int // its position in the current file (section filtering)
	peer               string
	peerStart, peerEnd int
}

type mvPeerRuns struct {
	file string
	runs []mvRun
}

// mvMatchRuns is matchRuns().
func mvMatchRuns(own []mvRun, peers []mvPeerRuns) []mvMatch {
	type pos struct{ p, r, i int }
	index := map[string][]pos{}
	for p, peer := range peers {
		for r, run := range peer.runs {
			for i, sig := range run {
				index[sig.norm] = append(index[sig.norm], pos{p, r, i})
			}
		}
	}
	out := []mvMatch{}
	for _, run := range own {
		k := 0
		for k < len(run) {
			bestLen, bestChars := 0, 0
			var best pos
			cands := index[run[k].norm]
			if len(cands) > mvMaxCandidates {
				cands = cands[:mvMaxCandidates]
			}
			for _, c := range cands {
				target := peers[c.p].runs[c.r]
				n, chars := 0, 0
				for k+n < len(run) && c.i+n < len(target) && run[k+n].norm == target[c.i+n].norm {
					chars += utf16Len(run[k+n].norm)
					n++
				}
				if n > bestLen {
					bestLen, bestChars, best = n, chars, c
				}
			}
			if bestLen > 0 && (bestLen >= mvMinMovedLines || bestChars >= mvMinMovedChars) {
				target := peers[best.p].runs[best.r]
				out = append(out, mvMatch{run[k].line, run[k+bestLen-1].line, run[k].at, peers[best.p].file, target[best.i].line, target[best.i+bestLen-1].line})
				k += bestLen
			} else {
				k++
			}
		}
	}
	return out
}

// mvPeerDiff is moved.mjs PeerDiff.
type mvPeerDiff struct {
	file, diffText string
}

// mvMoves is moved.mjs Moves.
type mvMoves struct {
	out, into []mvMatch
}

// mvMovedCode is movedCode().
func mvMovedCode(diffText string, peers []mvPeerDiff) mvMoves {
	others := []mvPeerDiff{}
	for _, p := range peers {
		if mvPrintable(p.file) {
			others = append(others, p)
		}
	}
	for i := 1; i < len(others); i++ {
		for j := i; j > 0 && lessUTF16Str(others[j].file, others[j-1].file); j-- {
			others[j], others[j-1] = others[j-1], others[j]
		}
	}
	if len(others) > mvMaxPeers {
		others = others[:mvMaxPeers]
	}
	if len(others) == 0 {
		return mvMoves{}
	}
	ownRemoved, ownAdded := mvDiffRuns(diffText)
	addedOf, removedOf := []mvPeerRuns{}, []mvPeerRuns{}
	for _, p := range others {
		r, a := mvDiffRuns(p.diffText)
		addedOf = append(addedOf, mvPeerRuns{p.file, a})
		removedOf = append(removedOf, mvPeerRuns{p.file, r})
	}
	return mvMoves{mvMatchRuns(ownRemoved, addedOf), mvMatchRuns(ownAdded, removedOf)}
}

func mvRange(a, b int) string {
	if a == b {
		return strconv.Itoa(a)
	}
	return strconv.Itoa(a) + "-" + strconv.Itoa(b)
}

// renderMoved is renderMoved(): ” when nothing moved.
func renderMoved(m *mvMoves) string {
	if m == nil || (len(m.out) == 0 && len(m.into) == 0) {
		return ""
	}
	entries := []string{}
	for _, x := range m.out {
		entries = append(entries, "- base lines "+mvRange(x.start, x.end)+" removed here were added in "+x.peer+" lines "+mvRange(x.peerStart, x.peerEnd)+" in this block.")
	}
	for _, x := range m.into {
		entries = append(entries, "- lines "+mvRange(x.start, x.end)+" were moved here from "+x.peer+" (its base lines "+mvRange(x.peerStart, x.peerEnd)+") in this block.")
	}
	shown := entries
	if len(shown) > mvMaxEntries {
		shown = append([]string{}, entries[:mvMaxEntries]...)
		shown = append(shown, "- ("+strconv.Itoa(len(entries)-mvMaxEntries)+" more moved ranges not listed)")
	}
	out := append([]string{mvHead}, shown...)
	if len(m.out) > 0 {
		out = append(out, "Do not report the removal of moved lines itself as a defect; review only whether references and imports are updated.")
	}
	if len(m.into) > 0 {
		out = append(out, "Moved-in lines are reviewed in their new place like any change (imports, references, behaviour).")
	}
	return strings.Join(out, "\n")
}

// movedText is moved.mjs movedText: keys nil is every move (a whole-file
// packet); else only the moves whose current-file position falls under one
// of those heading groups (the heading line, 0 = before the first heading).
func movedText(m *mvMoves, content *string, keys []int) string {
	if m == nil {
		return ""
	}
	if keys == nil {
		return renderMoved(m)
	}
	heads := mvHeadingLines(content)
	in := func(x mvMatch) bool {
		key := 0
		for _, h := range heads {
			if h <= x.at {
				key = h
			}
		}
		for _, k := range keys {
			if k == key {
				return true
			}
		}
		return false
	}
	f := mvMoves{out: []mvMatch{}, into: []mvMatch{}}
	for _, x := range m.out {
		if in(x) {
			f.out = append(f.out, x)
		}
	}
	for _, x := range m.into {
		if in(x) {
			f.into = append(f.into, x)
		}
	}
	return renderMoved(&f)
}

// mvHeadingLines is moved.mjs headingLines: the heading line numbers of the
// current file, ascending. Only the section path asks for it, after
// sectionGroups accepted the same headings (a surrogate cut refuses there).
func mvHeadingLines(content *string) []int {
	headings, _ := markdownHeadings(content)
	out := make([]int, 0, len(headings))
	for line := range headings {
		out = append(out, line)
	}
	sort.Ints(out)
	return out
}

// mvSplitChunks is stdout.split(/^(?=diff --git )/m).
func mvSplitChunks(stdout string) []string {
	chunks := []string{}
	last := 0
	for _, m := range mvChunkCut.FindAllStringIndex(stdout, -1) {
		at := m[1] - len("diff --git ")
		if at == 0 {
			continue
		}
		chunks = append(chunks, stdout[last:at])
		last = at
	}
	return append(chunks, stdout[last:])
}

// readPeerDiffs is moved.mjs readPeerDiffs; err is its throw (ls-tree or
// check-ignore failed).
func readPeerDiffs(repoRoot, base, file string, peers []string) ([]mvPeerDiff, error) {
	if base == "" || strings.HasPrefix(base, "-") {
		return nil, nil
	}
	sorted := unique(peers)
	sortUTF16(sorted)
	rels := []string{}
	for _, p := range sorted {
		if !mvPrintable(p) || p == file {
			continue
		}
		if AssertPacketPath(repoRoot, p) == nil && !IsSecretLike(p) {
			rels = append(rels, p)
		}
		if len(rels) >= mvMaxPeers {
			break
		}
	}
	if len(rels) == 0 {
		return nil, nil
	}
	env := reviewGitEnv()
	listed, _, ok := gitCapped(repoRoot, env, nil, nil, capDefaultBytes, append([]string{"-c", "core.quotePath=false", "ls-tree", "-r", "-z", "--name-only", base, "--"}, rels...)...)
	if !ok {
		return nil, errMvGit("git ls-tree failed")
	}
	inBase := map[string]bool{}
	for _, f := range strings.Split(listed, "\x00") {
		if f != "" {
			inBase[f] = true
		}
	}
	out := []mvPeerDiff{}
	tracked := []string{}
	for _, r := range rels {
		if inBase[r] {
			tracked = append(tracked, r)
		}
	}
	if len(tracked) > 0 {
		listing := func(files []string) (string, bool) {
			args := append(append(append([]string{}, mvGitDiff...), "--no-renames", base, "--"), files...)
			s, _, ok := gitCapped(repoRoot, env, nil, nil, mvMaxDiffBytes, args...)
			return s, ok
		}
		take := func(stdout string, files []string) {
			chunks := mvSplitChunks(stdout)
			for _, rel := range files {
				for _, c := range chunks {
					if strings.HasPrefix(c, "diff --git a/"+rel+" b/"+rel+"\n") {
						if trimJS(c) != "" {
							out = append(out, mvPeerDiff{rel, c})
						}
						break
					}
				}
			}
		}
		if s, ok := listing(tracked); ok {
			take(s, tracked)
		} else if len(tracked) > 1 {
			for _, rel := range tracked {
				if s, ok := listing([]string{rel}); ok {
					take(s, []string{rel})
				}
			}
		}
	}
	untracked := []string{}
	for _, r := range rels {
		if inBase[r] {
			continue
		}
		if _, err := os.Stat(filepath.Join(repoRoot, r)); err == nil {
			untracked = append(untracked, r)
		}
	}
	if len(untracked) > 0 {
		input := []byte(strings.Join(untracked, "\x00") + "\x00")
		ignored, _, ok := gitCapped(repoRoot, gitx.ChildEnv(), input, []int{0, 1}, capDefaultBytes, "check-ignore", "-z", "--stdin")
		if !ok {
			return nil, errMvGit("git check-ignore failed")
		}
		skip := map[string]bool{}
		for _, f := range strings.Split(ignored, "\x00") {
			skip[f] = true
		}
		for _, rel := range untracked {
			if skip[rel] {
				continue
			}
			args := append(append([]string{}, mvGitDiff...), "--no-index", "--", "/dev/null", rel)
			s, _, ok := gitCapped(repoRoot, env, nil, []int{0, 1}, mvMaxDiffBytes, args...)
			if ok && trimJS(s) != "" {
				out = append(out, mvPeerDiff{rel, s})
			}
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && lessUTF16Str(out[j].file, out[j-1].file); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

type errMvGit string

func (e errMvGit) Error() string { return string(e) }

// movesFor is movesFor() over a peerDiffReader: nil when nothing moved, the
// peers are unread, or any read failed.
func movesFor(diffText, repoRoot, base, file string, peers []string) *mvMoves {
	if base == "" || len(peers) == 0 {
		return nil
	}
	diffs, err := readPeerDiffs(repoRoot, base, file, peers)
	if err != nil || len(diffs) == 0 {
		return nil
	}
	m := mvMovedCode(diffText, diffs)
	if len(m.out)+len(m.into) == 0 {
		return nil
	}
	return &m
}
