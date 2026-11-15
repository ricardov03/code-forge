package main

// B726: the text width and the NFC normalization clack's wizard uses, so Go
// draws wide text (CJK, emoji and their sequences, combining marks, zero
// width characters, escapes) like Node. initWizWidth is fast-string-width
// (fast-string-truncated-width 3.0.3 with no limit); initWizNFC is
// String.prototype.normalize('NFC'), which fast-wrap-ansi applies before it
// wraps. The Unicode data in init_wizard_unicode.go is recorded from Node.

import (
	"regexp"
	"sort"
	"unicode/utf8"
)

var (
	initWizAnsiRe        = regexp.MustCompile(`^(?:[\x1b\x{9b}][\[()#;?]*(?:[0-9]{1,4}(?:;[0-9]{0,4})*)?[0-9A-ORZcf-nqry=><]|\x1b\]8;[^;]*;[^\n\r\x{2028}\x{2029}]*?(?:\x07|\x1b\\))`)
	initWizEmojiSet      = initWizProps["emoji"]
	initWizPresentation  = initWizProps["presentation"]
	initWizModifierBase  = initWizProps["modifierBase"]
	initWizModifierSet   = initWizProps["modifier"]
	initWizMarkSet       = initWizProps["mark"]
	initWizCJKTSet       = initWizProps["cjkt"]
	initWizHangulIndexed = struct{ s, l, v, t, lCount, vCount, tCount, nCount, sCount rune }{0xAC00, 0x1100, 0x1161, 0x11A7, 19, 21, 28, 21 * 28, 19 * 21 * 28}
)

// initWizIn reports r inside the sorted ranges.
func initWizIn(ranges [][2]rune, r rune) bool {
	i := sort.Search(len(ranges), func(i int) bool { return ranges[i][1] >= r })
	return i < len(ranges) && ranges[i][0] <= r
}

// initWizFullWidth is isFullWidth in fast-string-truncated-width.
func initWizFullWidth(x rune) bool {
	return x == 0x3000 || x >= 0xFF01 && x <= 0xFF60 || x >= 0xFFE0 && x <= 0xFFE6
}

// initWizWideOther is isWideNotCJKTNotEmoji in fast-string-truncated-width.
func initWizWideOther(x rune) bool {
	return x == 0x231B || x == 0x2329 || x >= 0x2FF0 && x <= 0x2FFF || x >= 0x3001 && x <= 0x303E || x >= 0x3099 && x <= 0x30FF ||
		x >= 0x3105 && x <= 0x312F || x >= 0x3131 && x <= 0x318E || x >= 0x3190 && x <= 0x31E3 || x >= 0x31EF && x <= 0x321E ||
		x >= 0x3220 && x <= 0x3247 || x >= 0x3250 && x <= 0x4DBF || x >= 0xFE10 && x <= 0xFE19 || x >= 0xFE30 && x <= 0xFE52 ||
		x >= 0xFE54 && x <= 0xFE66 || x >= 0xFE68 && x <= 0xFE6B || x >= 0x1F200 && x <= 0x1F202 || x >= 0x1F210 && x <= 0x1F23B ||
		x >= 0x1F240 && x <= 0x1F248 || x >= 0x20000 && x <= 0x2FFFD || x >= 0x30000 && x <= 0x3FFFD
}

func initWizRuneAt(rs []rune, i int) rune {
	if i < len(rs) {
		return rs[i]
	}
	return -1
}

// initWizEmojiPart is one emoji of EMOJI_RE's third branch: a keycap or
// variation form, a modifier base with its modifier, or a presentation
// emoji, tried in the regular expression's order (inner: after a ZWJ). It
// returns the end, or -1.
func initWizEmojiPart(rs []rune, i int, inner bool) int {
	r := initWizRuneAt(rs, i)
	keycap := func() int {
		if initWizIn(initWizEmojiSet, r) && initWizRuneAt(rs, i+1) == 0xFE0F {
			if initWizRuneAt(rs, i+2) == 0x20E3 {
				return i + 3
			}
			return i + 2
		}
		return -1
	}
	base := func() int {
		if initWizIn(initWizModifierBase, r) {
			if initWizIn(initWizModifierSet, initWizRuneAt(rs, i+1)) {
				return i + 2
			}
			return i + 1
		}
		return -1
	}
	presentation := func() int {
		if initWizIn(initWizPresentation, r) {
			return i + 1
		}
		return -1
	}
	order := []func() int{keycap, base, presentation}
	if inner {
		order = []func() int{base, presentation, keycap}
	}
	for _, f := range order {
		if end := f(); end >= 0 {
			return end
		}
	}
	return -1
}

// initWizEmojiAt is EMOJI_RE matched at i: the end of the emoji, or -1.
func initWizEmojiAt(rs []rune, i int) int {
	at := func(j int) rune { return initWizRuneAt(rs, j) }
	regional := func(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }
	if regional(at(i)) && regional(at(i+1)) {
		return i + 2
	}
	if at(i) == 0x1F3F4 {
		tag := func(r rune) bool { return r >= 0xE0061 && r <= 0xE007A }
		if tag(at(i+1)) && tag(at(i+2)) {
			j, n := i+3, 0
			for n < 3 && (tag(at(j)) || at(j) >= 0xE0030 && at(j) <= 0xE0039) {
				j++
				n++
			}
			if n > 0 && at(j) == 0xE007F {
				return j + 1
			}
		}
	}
	end := initWizEmojiPart(rs, i, false)
	if end < 0 {
		return -1
	}
	for at(end) == 0x200D {
		next := initWizEmojiPart(rs, end+1, true)
		if next < 0 {
			break
		}
		end = next
	}
	return end
}

// initWizRun is how many code points from i (at most 1000, as in the
// regular expressions' {1,1000}) satisfy ok.
func initWizRun(rs []rune, i int, ok func(j int) bool) int {
	n := 0
	for n < 1000 && i+n < len(rs) && ok(i+n) {
		n++
	}
	return n
}

// initWizWidth is fastStringWidth(s): at each point the first block that
// matches, in fast-string-truncated-width's order: a Latin run (1 each),
// an ANSI escape or OSC 8 link (0), a control run (0), a tab run (8 each),
// an emoji (2), a CJK script run (2 a code point); else the code point
// alone: a mark 0, a full-width or wide one 2, any other 1.
func initWizWidth(s string) int {
	rs := []rune(s)
	w := 0
	for i := 0; i < len(rs); {
		if n := initWizRun(rs, i, func(j int) bool {
			r := rs[j]
			return (r >= 0x20 && r <= 0x7E || r >= 0xA0 && r <= 0xFF) && initWizRuneAt(rs, j+1) != 0xFE0F
		}); n > 0 {
			w += n
			i += n
			continue
		}
		r := rs[i]
		if r == 0x1B || r == 0x9B {
			if m := initWizAnsiRe.FindString(string(rs[i:])); m != "" {
				i += utf8.RuneCountInString(m)
				continue
			}
		}
		if n := initWizRun(rs, i, func(j int) bool {
			r := rs[j]
			return r >= 0 && r <= 0x08 || r >= 0x0A && r <= 0x1F || r >= 0x7F && r <= 0x9F
		}); n > 0 {
			i += n
			continue
		}
		if n := initWizRun(rs, i, func(j int) bool { return rs[j] == '\t' }); n > 0 {
			w += 8 * n
			i += n
			continue
		}
		if end := initWizEmojiAt(rs, i); end > i {
			w += 2
			i = end
			continue
		}
		if n := initWizRun(rs, i, func(j int) bool {
			r := rs[j]
			return initWizIn(initWizCJKTSet, r) && !(r >= 0xFF00 && r <= 0xFFEF)
		}); n > 0 {
			w += 2 * n
			i += n
			continue
		}
		switch {
		case initWizIn(initWizMarkSet, r):
		case initWizFullWidth(r), initWizWideOther(r):
			w += 2
		default:
			w++
		}
		i++
	}
	return w
}

// --- String.prototype.normalize('NFC') -------------------------------------

func initWizCCC(r rune) int { return int(initWizCCCRank[r]) }

func initWizHangulCompose(a, b rune) (rune, bool) {
	h := initWizHangulIndexed
	if a >= h.l && a < h.l+h.lCount && b >= h.v && b < h.v+h.vCount {
		return h.s + ((a-h.l)*h.vCount+(b-h.v))*h.tCount, true
	}
	if a >= h.s && a < h.s+h.sCount && (a-h.s)%h.tCount == 0 && b > h.t && b < h.t+h.tCount {
		return a + (b - h.t), true
	}
	return 0, false
}

// initWizNFC is s.normalize('NFC'): the full canonical decomposition, the
// canonical order of marks, then the canonical composition.
func initWizNFC(s string) string {
	plain := true
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			plain = false
			break
		}
	}
	if plain {
		return s
	}
	h := initWizHangulIndexed
	var d []rune
	for _, r := range s {
		switch {
		case r >= h.s && r < h.s+h.sCount:
			i := r - h.s
			d = append(d, h.l+i/h.nCount, h.v+(i%h.nCount)/h.tCount)
			if t := i % h.tCount; t != 0 {
				d = append(d, h.t+t)
			}
		case initWizDecomp[r] != nil:
			d = append(d, initWizDecomp[r]...)
		default:
			d = append(d, r)
		}
	}
	for i := 0; i < len(d); {
		if initWizCCC(d[i]) == 0 {
			i++
			continue
		}
		j := i
		for j < len(d) && initWizCCC(d[j]) != 0 {
			j++
		}
		run := d[i:j]
		sort.SliceStable(run, func(a, b int) bool { return initWizCCC(run[a]) < initWizCCC(run[b]) })
		i = j
	}
	if len(d) == 0 {
		return ""
	}
	out := []rune{d[0]}
	starter := 0
	last := initWizCCC(d[0])
	if last != 0 {
		last = 256
	}
	for _, ch := range d[1:] {
		class := initWizCCC(ch)
		composite, ok := initWizCompose[[2]rune{out[starter], ch}]
		if !ok {
			composite, ok = initWizHangulCompose(out[starter], ch)
		}
		if ok && (last < class || last == 0) {
			out[starter] = composite
			continue
		}
		if class == 0 {
			starter = len(out)
		}
		last = class
		out = append(out, ch)
	}
	return string(out)
}
