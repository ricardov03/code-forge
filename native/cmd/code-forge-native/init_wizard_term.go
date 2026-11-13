package main

// The terminal side of the interactive init wizard (B528): a port of the
// parts of @clack/prompts and @clack/core (select, multiselect, confirm,
// text), Node's util.styleText, fast-wrap-ansi, fast-string-truncated-width,
// readline's key decoding and line editing that `code-forge init` uses, so
// the bytes on the terminal are Node's.

import (
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// --- util.styleText ---------------------------------------------------------

var initWizStyles = map[string][2]int{
	"reset": {0, 0}, "bold": {1, 22}, "dim": {2, 22}, "italic": {3, 23}, "underline": {4, 24},
	"inverse": {7, 27}, "hidden": {8, 28}, "strikethrough": {9, 29},
	"red": {31, 39}, "green": {32, 39}, "yellow": {33, 39}, "cyan": {36, 39}, "gray": {90, 39},
	"bgWhite": {47, 49},
}

func initWizSGR(code int) string { return "\x1b[" + strconv.Itoa(code) + "m" }

// initWizStyle is util.styleText(formats, text) on a stream with colors.
func initWizStyle(text string, formats ...string) string {
	left, right := "", ""
	for _, f := range formats {
		codes := initWizStyles[f]
		open, closing := initWizSGR(codes[0]), initWizSGR(codes[1])
		left += open
		right = closing + right
		replacement := open
		if codes[1] == 22 {
			replacement = closing + open
		}
		text = strings.ReplaceAll(text, closing, replacement)
	}
	return left + text + right
}

// --- string width (fast-string-truncated-width) -----------------------------

var initWizAnsiRe = regexp.MustCompile(`^[\x1b\x{9b}][\[()#;?]*(?:[0-9]{1,4}(?:;[0-9]{0,4})*)?[0-9A-ORZcf-nqry=><]`)

var initWizWideRanges = [][2]rune{
	{0x1100, 0x115F}, {0x231A, 0x231B}, {0x2329, 0x232A}, {0x2E80, 0x303E}, {0x3041, 0x33FF},
	{0x3400, 0x4DBF}, {0x4E00, 0x9FFF}, {0xA000, 0xA4CF}, {0xA960, 0xA97F}, {0xAC00, 0xD7A3},
	{0xF900, 0xFAFF}, {0xFE10, 0xFE19}, {0xFE30, 0xFE6F}, {0xFF01, 0xFF60}, {0xFFE0, 0xFFE6},
	{0x17000, 0x18AFF}, {0x1F200, 0x1F2FF}, {0x20000, 0x3FFFD},
}

// initWizRuneWidth is the column width of one code point; emoji and other
// shapes this port does not model are kept out by initWizPlainText.
func initWizRuneWidth(r rune) int {
	switch {
	case r == '\t':
		return 8
	case r < 0x20 || r >= 0x7F && r <= 0x9F:
		return 0
	case r < 0x7F || r >= 0xA0 && r <= 0xFF:
		return 1
	case unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r):
		return 0
	}
	for _, rg := range initWizWideRanges {
		if r >= rg[0] && r <= rg[1] {
			return 2
		}
	}
	return 1
}

// initWizWidth is the visible width of s: ANSI escapes count 0.
func initWizWidth(s string) int {
	w := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b || strings.HasPrefix(s[i:], "\u009b") {
			if m := initWizAnsiRe.FindString(s[i:]); m != "" {
				i += len(m)
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		w += initWizRuneWidth(r)
		i += size
	}
	return w
}

// initWizPlainText reports text whose width this port measures exactly as
// Node does: no emoji, variation selectors, zero-width joiners or
// unassigned code points.
func initWizPlainText(s string) bool {
	for _, r := range s {
		switch {
		case r == 0x200D || r >= 0xFE00 && r <= 0xFE0F || r >= 0x1F000:
			return false
		case r >= 0x2600 && r <= 0x27BF:
			return false
		case !unicode.IsPrint(r) && r != '\t':
			return false
		}
	}
	return true
}

func initWizU16(s string) int { return len(utf16.Encode([]rune(s))) }

// --- wrap-ansi (fast-wrap-ansi, hard: true, trim: false) ---------------------

var initWizCloseCodes = func() map[int]int {
	m := map[int]int{1: 22, 2: 22, 3: 23, 4: 24, 7: 27, 8: 28, 9: 29}
	for c := 30; c <= 37; c++ {
		m[c] = 39
	}
	for c := 90; c <= 97; c++ {
		m[c] = 39
	}
	for c := 40; c <= 47; c++ {
		m[c] = 49
	}
	for c := 100; c <= 107; c++ {
		m[c] = 49
	}
	return m
}()

var initWizSGRAt = regexp.MustCompile(`^\[(\d+)m`)

func initWizWrapWord(rows []string, word string, cols int) []string {
	chars := []rune(word)
	inEscape := false
	visible := initWizWidth(rows[len(rows)-1])
	for i, ch := range chars {
		l := initWizWidth(string(ch))
		if ch == 0x1b || ch == 0x9b {
			l = 0
		}
		if visible+l <= cols {
			rows[len(rows)-1] += string(ch)
		} else {
			rows = append(rows, string(ch))
			visible = 0
		}
		if ch == 0x1b || ch == 0x9b {
			inEscape = true
		}
		if inEscape {
			if ch == 'm' {
				inEscape = false
			}
			continue
		}
		visible += l
		if visible == cols && i < len(chars)-1 {
			rows = append(rows, "")
			visible = 0
		}
	}
	if visible == 0 && len(rows[len(rows)-1]) > 0 && len(rows) > 1 {
		last := rows[len(rows)-1]
		rows = rows[:len(rows)-1]
		rows[len(rows)-1] += last
	}
	return rows
}

func initWizWrapLine(str string, cols int) string {
	rows := []string{""}
	rowLength := 0
	words := strings.Split(str, " ")
	for index, word := range words {
		if index != 0 {
			if rowLength >= cols {
				rows = append(rows, "")
				rowLength = 0
			}
			rows[len(rows)-1] += " "
			rowLength++
		}
		wl := initWizWidth(word)
		if wl > cols {
			remaining := cols - rowLength
			breaksThis := 1 + (wl-remaining-1)/cols
			if wl-remaining-1 < 0 {
				breaksThis = 1 + -((-(wl - remaining - 1) + cols - 1) / cols)
			}
			breaksNext := (wl - 1) / cols
			if breaksNext < breaksThis {
				rows = append(rows, "")
			}
			rows = initWizWrapWord(rows, word, cols)
			rowLength = initWizWidth(rows[len(rows)-1])
			continue
		}
		if rowLength+wl > cols && rowLength > 0 && wl > 0 {
			rows = append(rows, "")
			rowLength = 0
		}
		rows[len(rows)-1] += word
		rowLength += wl
	}
	pre := []rune(strings.Join(rows, "\n"))
	var out strings.Builder
	escapeCode := 0
	for i, ch := range pre {
		out.WriteRune(ch)
		if ch == 0x1b || ch == 0x9b {
			if m := initWizSGRAt.FindStringSubmatch(string(pre[i+1:])); m != nil {
				code, _ := strconv.Atoi(m[1])
				if code == 39 {
					code = 0
				}
				escapeCode = code
			}
		}
		closing, has := initWizCloseCodes[escapeCode]
		if i+1 < len(pre) && pre[i+1] == '\n' {
			if escapeCode != 0 && has {
				out.WriteString(initWizSGR(closing))
			}
		} else if ch == '\n' {
			if escapeCode != 0 && has {
				out.WriteString(initWizSGR(escapeCode))
			}
		}
	}
	return out.String()
}

// initWizWrap is wrapAnsi(text, cols, {hard: true, trim: false}).
func initWizWrap(text string, cols int) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i, l := range lines {
		lines[i] = initWizWrapLine(l, cols)
	}
	return strings.Join(lines, "\n")
}

// initWizPrefixWrap is wrapTextWithPrefix(output, text, prefix, start, end).
func initWizPrefixWrap(cols int, text, prefix, start, end string) string {
	lines := strings.Split(initWizWrap(text, cols-initWizU16(prefix)), "\n")
	for i, l := range lines {
		switch {
		case i == 0:
			lines[i] = start + l
		case i == len(lines)-1:
			lines[i] = end + l
		default:
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n")
}

// --- symbols ----------------------------------------------------------------

const (
	initWizBar    = "│"
	initWizBarEnd = "└"
)

func initWizSymbol(state string) string {
	switch state {
	case "cancel":
		return initWizStyle("■", "red")
	case "error":
		return initWizStyle("▲", "yellow")
	case "submit":
		return initWizStyle("◇", "green")
	}
	return initWizStyle("◆", "cyan")
}

func initWizSymbolBar(state string) string {
	switch state {
	case "cancel":
		return initWizStyle(initWizBar, "red")
	case "error":
		return initWizStyle(initWizBar, "yellow")
	case "submit":
		return initWizStyle(initWizBar, "green")
	}
	return initWizStyle(initWizBar, "cyan")
}

func initWizFooter(parts []string) []string {
	return []string{initWizStyle(initWizBar, "cyan") + "  " + strings.Join(parts, " • "), initWizStyle(initWizBarEnd, "cyan")}
}

// --- keys (readline emitKeys) -------------------------------------------------

type initWizKey struct {
	char     string // the first keypress argument; "" is undefined
	name     string
	sequence string
	ctrl     bool
	meta     bool
	shift    bool
}

var initWizCSI = regexp.MustCompile(`^\x1b(\[\[?|O)([0-9;]*)([A-Za-z~])`)

// initWizKeys decodes one read chunk the way readline's emitKeys does for the
// keys a person types at these prompts.
func initWizKeys(chunk string) []initWizKey {
	var keys []initWizKey
	for len(chunk) > 0 {
		if m := initWizCSI.FindStringSubmatch(chunk); m != nil {
			k := initWizKey{sequence: m[0]}
			code := m[1] + m[2] + m[3]
			params := strings.Split(m[2], ";")
			if len(params) == 2 {
				mod, _ := strconv.Atoi(params[1])
				mod--
				k.shift, k.meta, k.ctrl = mod&1 != 0, mod&10 != 0, mod&4 != 0
				code = m[1] + params[0] + m[3]
				if params[0] == "1" {
					code = m[1] + m[3]
				}
			}
			switch code {
			case "[A", "OA":
				k.name = "up"
			case "[B", "OB":
				k.name = "down"
			case "[C", "OC":
				k.name = "right"
			case "[D", "OD":
				k.name = "left"
			case "[H", "OH", "[1~", "[7~":
				k.name = "home"
			case "[F", "OF", "[4~", "[8~":
				k.name = "end"
			case "[3~":
				k.name = "delete"
			case "[2~":
				k.name = "insert"
			case "[5~":
				k.name = "pageup"
			case "[6~":
				k.name = "pagedown"
			default:
				k.name = "undefined"
			}
			keys = append(keys, k)
			chunk = chunk[len(m[0]):]
			continue
		}
		r, size := utf8.DecodeRuneInString(chunk)
		s := chunk[:size]
		chunk = chunk[size:]
		k := initWizKey{char: s, sequence: s}
		if r == 0x1b {
			if len(chunk) == 0 {
				keys = append(keys, initWizKey{name: "escape", sequence: s, meta: true})
				continue
			}
			// meta + one key
			r2, size2 := utf8.DecodeRuneInString(chunk)
			s2 := chunk[:size2]
			chunk = chunk[size2:]
			k = initWizKey{sequence: s + s2, meta: true}
			switch {
			case r2 == '\x7f' || r2 == '\b':
				k.name = "backspace"
			case r2 >= 'a' && r2 <= 'z' || r2 >= '0' && r2 <= '9':
				k.name = s2
			case r2 >= 'A' && r2 <= 'Z':
				k.name, k.shift = strings.ToLower(s2), true
			case r2 == 0x1b:
				k.name = "escape"
			}
			keys = append(keys, k)
			continue
		}
		switch {
		case r == '\r':
			k.name = "return"
		case r == '\n':
			k.name = "enter"
		case r == '\t':
			k.name = "tab"
		case r == '\b' || r == 0x7f:
			k.name = "backspace"
		case r == ' ':
			k.name = "space"
		case r < 0x1b:
			k.name, k.ctrl = string(rune(r+'a'-1)), true
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			k.name = s
		case r >= 'A' && r <= 'Z':
			k.name, k.shift = strings.ToLower(s), true
		}
		keys = append(keys, k)
	}
	return keys
}

// --- readline line editing ------------------------------------------------

type initWizLine struct {
	line   []rune
	cursor int
	closed bool // ctrl-d on an empty line (EOF) or ctrl-c
	eof    bool
}

func initWizWordLeft(line []rune, cursor int) int {
	i := cursor
	for i > 0 && !initWizWordRune(line[i-1]) {
		i--
	}
	for i > 0 && initWizWordRune(line[i-1]) {
		i--
	}
	return i
}

func initWizWordRight(line []rune, cursor int) int {
	i := cursor
	for i < len(line) && !initWizWordRune(line[i]) {
		i++
	}
	for i < len(line) && initWizWordRune(line[i]) {
		i++
	}
	return i
}

func initWizWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

func (l *initWizLine) cut(from, to int) {
	l.line = append(append([]rune{}, l.line[:from]...), l.line[to:]...)
}

// key is readline's _ttyWrite for the keys this port models.
func (l *initWizLine) key(k initWizKey) {
	if l.closed {
		return
	}
	switch {
	case k.ctrl && k.shift:
		return
	case k.ctrl:
		switch k.name {
		case "c":
			l.closed = true
		case "h":
			if l.cursor > 0 {
				l.cut(l.cursor-1, l.cursor)
				l.cursor--
			}
		case "d":
			if l.cursor == 0 && len(l.line) == 0 {
				l.closed, l.eof = true, true
			} else if l.cursor < len(l.line) {
				l.cut(l.cursor, l.cursor+1)
			}
		case "u":
			l.cut(0, l.cursor)
			l.cursor = 0
		case "k":
			l.line = l.line[:l.cursor]
		case "a":
			l.cursor = 0
		case "e":
			l.cursor = len(l.line)
		case "b":
			if l.cursor > 0 {
				l.cursor--
			}
		case "f":
			if l.cursor < len(l.line) {
				l.cursor++
			}
		case "w", "backspace":
			to := initWizWordLeft(l.line, l.cursor)
			l.cut(to, l.cursor)
			l.cursor = to
		case "delete":
			l.cut(l.cursor, initWizWordRight(l.line, l.cursor))
		case "left":
			l.cursor = initWizWordLeft(l.line, l.cursor)
		case "right":
			l.cursor = initWizWordRight(l.line, l.cursor)
		}
	case k.meta:
		switch k.name {
		case "b":
			l.cursor = initWizWordLeft(l.line, l.cursor)
		case "f":
			l.cursor = initWizWordRight(l.line, l.cursor)
		case "d", "delete":
			l.cut(l.cursor, initWizWordRight(l.line, l.cursor))
		case "backspace":
			to := initWizWordLeft(l.line, l.cursor)
			l.cut(to, l.cursor)
			l.cursor = to
		}
	default:
		switch k.name {
		case "return", "enter":
			l.line, l.cursor = nil, 0
		case "backspace":
			if l.cursor > 0 {
				l.cut(l.cursor-1, l.cursor)
				l.cursor--
			}
		case "delete":
			if l.cursor < len(l.line) {
				l.cut(l.cursor, l.cursor+1)
			}
		case "left":
			if l.cursor > 0 {
				l.cursor--
			}
		case "right":
			if l.cursor < len(l.line) {
				l.cursor++
			}
		case "home":
			l.cursor = 0
		case "end":
			l.cursor = len(l.line)
		case "up", "down", "escape", "pageup", "pagedown", "insert", "undefined":
		default:
			if k.char != "" {
				ins := []rune(k.char)
				l.line = append(append(append([]rune{}, l.line[:l.cursor]...), ins...), l.line[l.cursor:]...)
				l.cursor += len(ins)
			}
		}
	}
}

// --- the prompt engine (@clack/core Prompt) --------------------------------

type initWizOption struct {
	value, label, hint string
}

type initWizPrompt struct {
	kind    string // select, multiselect, confirm, text, password
	message string
	options []initWizOption
	cursor  int
	value   bool     // confirm
	chosen  []string // multiselect
	rl      initWizLine
	input   string // text userInput
	state   string
}

type initWizTerm struct {
	in         io.Reader
	out        io.Writer
	cols, rows int
	prevFrame  string
}

// initWizOutcome is how a prompt ended.
type initWizOutcome int

const (
	initWizSubmitted initWizOutcome = iota
	initWizCancelled
	initWizEOF
)

func (p *initWizPrompt) optionLabel(o initWizOption) string {
	if o.label != "" {
		return o.label
	}
	return o.value
}

func initWizEachLine(s string, f func(string) string) string {
	parts := strings.Split(s, "\n")
	for i, x := range parts {
		parts[i] = f(x)
	}
	return strings.Join(parts, "\n")
}

func (p *initWizPrompt) frame(cols int) string {
	gray := initWizStyle(initWizBar, "gray")
	cyan := initWizStyle(initWizBar, "cyan")
	dim := func(s string) string { return initWizStyle(s, "dim") }
	switch p.kind {
	case "password":
		// B611: @clack/prompts password: the input shown as one mask per
		// UTF-16 unit, the cursor drawn on the mask
		head := gray + "\n" + initWizSymbol(p.state) + "  " + p.message + "\n"
		masked := initWizMask(p.input)
		switch p.state {
		case "submit":
			n := ""
			if masked != "" {
				n = dim(masked)
			}
			return head + gray + "  " + n
		case "cancel":
			n, tail := "", ""
			if masked != "" {
				n = initWizStyle(masked, "strikethrough", "dim")
				tail = "\n" + gray
			}
			return head + gray + "  " + n + tail
		}
		shown := masked + initWizStyle("_", "inverse", "hidden")
		units, at := []rune(masked), initWizU16(string(p.rl.line[:min(p.rl.cursor, len(p.rl.line))]))
		if at < len(units) {
			shown = string(units[:at]) + initWizStyle(string(units[at]), "inverse") + string(units[at+1:])
		}
		return head + cyan + "  " + shown + "\n" + initWizStyle(initWizBarEnd, "cyan") + "\n"
	case "text":
		head := gray + "\n" + initWizSymbol(p.state) + "  " + p.message + "\n"
		shown := initWizStyle("_", "inverse", "hidden")
		if p.input != "" {
			runes := []rune(p.input)
			if p.rl.cursor >= len(runes) {
				shown = p.input + "█"
			} else {
				shown = string(runes[:p.rl.cursor]) + initWizStyle(string(runes[p.rl.cursor]), "inverse") + string(runes[p.rl.cursor+1:])
			}
		}
		switch p.state {
		case "submit":
			n := ""
			if p.input != "" {
				n = "  " + dim(p.input)
			}
			return head + gray + n
		case "cancel":
			n := ""
			if p.input != "" {
				n = "  " + initWizStyle(p.input, "strikethrough", "dim")
			}
			tail := ""
			if strings.TrimSpace(p.input) != "" {
				tail = "\n" + gray
			}
			return head + gray + n + tail
		}
		return head + cyan + "  " + shown + "\n" + initWizStyle(initWizBarEnd, "cyan") + "\n"
	case "confirm":
		head := gray + "\n" + initWizPrefixWrap(cols, p.message, gray+"  ", initWizSymbol(p.state)+"  ", gray+"  ") + "\n"
		c := "No"
		if p.value {
			c = "Yes"
		}
		switch p.state {
		case "submit":
			return head + gray + "  " + dim(c)
		case "cancel":
			return head + gray + "  " + initWizStyle(c, "strikethrough", "dim") + "\n" + gray
		}
		yes := initWizStyle("○", "dim") + " " + dim("Yes")
		no := initWizStyle("●", "green") + " No"
		if p.value {
			yes = initWizStyle("●", "green") + " Yes"
			no = initWizStyle("○", "dim") + " " + dim("No")
		}
		return head + cyan + "  " + yes + " " + dim("/") + " " + no + "\n" + initWizStyle(initWizBarEnd, "cyan") + "\n"
	case "select":
		head := gray + "\n" + initWizPrefixWrap(cols, p.message, initWizSymbolBar(p.state)+"  ", initWizSymbol(p.state)+"  ", initWizSymbolBar(p.state)+"  ") + "\n"
		label := p.optionLabel(p.options[p.cursor])
		switch p.state {
		case "submit":
			return head + initWizPrefixWrap(cols, initWizEachLine(label, dim), gray+"  ", gray+"  ", gray+"  ")
		case "cancel":
			return head + initWizPrefixWrap(cols, initWizEachLine(label, func(s string) string { return initWizStyle(s, "strikethrough", "dim") }), gray+"  ", gray+"  ", gray+"  ") + "\n" + gray
		}
		lines := []string{}
		for i, o := range p.options {
			var s string
			if i == p.cursor {
				s = initWizStyle("●", "green") + " " + p.optionLabel(o)
				if o.hint != "" {
					s += " " + dim("("+o.hint+")")
				}
			} else {
				s = initWizStyle("○", "dim") + " " + initWizEachLine(p.optionLabel(o), dim)
			}
			lines = append(lines, strings.Split(initWizWrap(s, cols-initWizU16(cyan+"  ")), "\n")...)
		}
		return head + cyan + "  " + strings.Join(lines, "\n"+cyan+"  ") + "\n" +
			strings.Join(initWizFooter([]string{dim("↑/↓") + " to navigate", dim("Enter:") + " confirm"}), "\n") + "\n"
	}
	// multiselect
	head := gray + "\n" + initWizPrefixWrap(cols, p.message, initWizSymbolBar(p.state)+"  ", initWizSymbol(p.state)+"  ", initWizSymbolBar(p.state)+"  ") + "\n"
	picked := []string{}
	for _, o := range p.options {
		if initHas(p.chosen, o.value) {
			picked = append(picked, o.label)
		}
	}
	switch p.state {
	case "submit":
		parts := []string{}
		for _, l := range picked {
			parts = append(parts, initWizEachLine(l, dim))
		}
		n := strings.Join(parts, dim(", "))
		if n == "" {
			n = dim("none")
		}
		return head + initWizPrefixWrap(cols, n, gray+"  ", gray+"  ", gray+"  ")
	case "cancel":
		parts := []string{}
		for _, l := range picked {
			parts = append(parts, initWizEachLine(l, func(s string) string { return initWizStyle(s, "strikethrough", "dim") }))
		}
		n := strings.Join(parts, dim(", "))
		if strings.TrimSpace(n) == "" {
			return head + gray
		}
		return head + initWizPrefixWrap(cols, n, gray+"  ", gray+"  ", gray+"  ") + "\n" + gray
	}
	lines := []string{}
	for i, o := range p.options {
		sel := initHas(p.chosen, o.value)
		var s string
		switch {
		case i == p.cursor && sel:
			s = initWizStyle("◼", "green") + " " + o.label
		case sel:
			s = initWizStyle("◼", "green") + " " + initWizEachLine(o.label, dim)
		case i == p.cursor:
			s = initWizStyle("◻", "cyan") + " " + o.label
		default:
			s = initWizStyle("◻", "dim") + " " + initWizEachLine(o.label, dim)
		}
		lines = append(lines, strings.Split(initWizWrap(s, cols-initWizU16(cyan+"  ")), "\n")...)
	}
	return head + cyan + "  " + strings.Join(lines, "\n"+cyan+"  ") + "\n" +
		strings.Join(initWizFooter([]string{dim("↑/↓") + " to navigate", dim("Space:") + " select", dim("Enter:") + " confirm"}), "\n") + "\n"
}

// initWizMask is PasswordPrompt.masked: userInput.replaceAll(/./g, mask),
// one mask per UTF-16 unit except line terminators.
func initWizMask(input string) string {
	var b strings.Builder
	for _, r := range input {
		switch {
		case r == '\n' || r == '\r' || r == 0x2028 || r == 0x2029:
			b.WriteRune(r)
		case r > 0xFFFF:
			b.WriteString("▪▪")
		default:
			b.WriteString("▪")
		}
	}
	return b.String()
}

func initWizMove(x, y int) string {
	s := ""
	if x < 0 {
		s += "\x1b[" + strconv.Itoa(-x) + "D"
	} else if x > 0 {
		s += "\x1b[" + strconv.Itoa(x) + "C"
	}
	if y < 0 {
		s += "\x1b[" + strconv.Itoa(-y) + "A"
	} else if y > 0 {
		s += "\x1b[" + strconv.Itoa(y) + "B"
	}
	return s
}

// render is Prompt.render: the first frame hides the cursor, later frames
// are written as a diff against the previous one.
func (t *initWizTerm) render(p *initWizPrompt) {
	frame := initWizWrap(p.frame(t.cols), t.cols)
	if frame == t.prevFrame {
		return
	}
	w := func(s string) { _, _ = io.WriteString(t.out, s) }
	if p.state == "initial" {
		w("\x1b[?25l")
	} else {
		a, b := strings.Split(t.prevFrame, "\n"), strings.Split(frame, "\n")
		w(initWizMove(-999, -(len(a) - 1)))
		var diff []int
		for i := 0; i < len(a) || i < len(b); i++ {
			var x, y string
			okA, okB := i < len(a), i < len(b)
			if okA {
				x = a[i]
			}
			if okB {
				y = b[i]
			}
			if x != y || okA != okB {
				diff = append(diff, i)
			}
		}
		s := max(0, len(b)-t.rows)
		r := max(0, len(a)-t.rows)
		n := -1
		for _, d := range diff {
			if d >= s {
				n = d
				break
			}
		}
		if n < 0 {
			t.prevFrame = frame
			return
		}
		if len(diff) == 1 {
			w(initWizMove(0, n-r))
			w("\x1b[2K\x1b[G")
			if n < len(b) {
				w(b[n])
			}
			t.prevFrame = frame
			w(initWizMove(0, len(b)-n-1))
			return
		}
		if s < r {
			n = s
		} else if h := n - r; h > 0 {
			w(initWizMove(0, h))
		}
		w("\x1b[J")
		if n < len(b) {
			w(strings.Join(b[n:], "\n"))
		}
		t.prevFrame = frame
		return
	}
	w(frame)
	if p.state == "initial" {
		p.state = "active"
	}
	t.prevFrame = frame
}

// run shows p and reads keys until it is submitted, cancelled or the input
// ends. Keys left in a read chunk after the prompt closes are dropped, as
// Node drops them with the prompt's readline.
func (t *initWizTerm) run(p *initWizPrompt) initWizOutcome {
	p.state = "initial"
	t.prevFrame = ""
	if p.kind == "text" || p.kind == "password" {
		p.rl.line = []rune(p.input)
		p.rl.cursor = len(p.rl.line)
	}
	t.render(p)
	buf := make([]byte, 4096)
	shown := false
	closeOnce := func() {
		_, _ = io.WriteString(t.out, "\n")
		if !shown {
			shown = true
			_, _ = io.WriteString(t.out, "\x1b[?25h")
		}
	}
	for {
		n, err := t.in.Read(buf)
		if n == 0 && err != nil {
			return initWizEOF
		}
		for _, k := range initWizKeys(string(buf[:n])) {
			p.rl.key(k)
			if p.rl.eof {
				return initWizEOF
			}
			if (p.kind == "text" || p.kind == "password") && k.name != "return" {
				if k.char == "\t" {
					p.rl.key(initWizKey{name: "h", ctrl: true})
				}
				p.input = string(p.rl.line)
			}
			if k.name != "" {
				if p.kind != "text" && p.kind != "password" {
					if alias := map[string]string{"k": "up", "j": "down", "h": "left", "l": "right", "escape": "cancel"}[k.name]; alias != "" {
						p.cursorEvent(alias)
					}
				}
				switch k.name {
				case "up", "down", "left", "right", "space", "enter", "cancel":
					p.cursorEvent(k.name)
				}
			}
			if p.kind == "confirm" && (strings.EqualFold(k.char, "y") || strings.EqualFold(k.char, "n")) {
				_, _ = io.WriteString(t.out, "\x1b[1A")
				p.value = strings.EqualFold(k.char, "y")
				p.state = "submit"
				closeOnce()
			}
			if p.kind == "multiselect" && k.name == "a" {
				if len(p.chosen) == len(p.options) {
					p.chosen = []string{}
				} else {
					p.chosen = []string{}
					for _, o := range p.options {
						p.chosen = append(p.chosen, o.value)
					}
				}
			}
			if p.kind == "multiselect" && k.name == "i" {
				next := []string{}
				for _, o := range p.options {
					if !initHas(p.chosen, o.value) {
						next = append(next, o.value)
					}
				}
				p.chosen = next
			}
			if k.name == "return" {
				p.state = "submit"
			}
			if k.char == "\x03" || k.name == "escape" {
				p.state = "cancel"
			}
			t.render(p)
			if p.state == "submit" || p.state == "cancel" {
				closeOnce()
				if p.state == "cancel" {
					return initWizCancelled
				}
				return initWizSubmitted
			}
		}
	}
}

func (p *initWizPrompt) cursorEvent(name string) {
	switch p.kind {
	case "confirm":
		p.value = !p.value
	case "select", "multiselect":
		switch name {
		case "left", "up":
			p.cursor = (p.cursor - 1 + len(p.options)) % len(p.options)
		case "down", "right":
			p.cursor = (p.cursor + 1) % len(p.options)
		case "space":
			if p.kind == "multiselect" {
				v := p.options[p.cursor].value
				if initHas(p.chosen, v) {
					next := []string{}
					for _, c := range p.chosen {
						if c != v {
							next = append(next, c)
						}
					}
					p.chosen = next
				} else {
					p.chosen = append(p.chosen, v)
				}
			}
		}
	}
}
