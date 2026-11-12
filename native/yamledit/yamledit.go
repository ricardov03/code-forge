// Package yamledit changes one key path of a .code-forge.yml the way Node's
// editDoc (src/autopilot/limits.mjs) does with the yaml package (2.9.x):
// parseDocument, doc.setIn(segs, doc.createNode(value)) or doc.deleteIn plus
// the empty-parent prune, then String(doc). The whole document is written
// again by the yaml stringifier, so the result keeps comments and blank lines
// where yaml keeps them and normalises what yaml normalises (indentation,
// spacing, CRLF, a missing final newline).
//
// Only a subset of YAML is served: block mappings with plain keys, block
// sequences of single-line scalars, single-line plain and quoted scalars,
// empty flow collections, comments and blank lines in the places yaml keeps
// them, and one document with optional "---" and "..." markers. Any other
// shape returns an *Unsupported error before anything is written, so the
// caller can hand the edit to Node. The expected bytes of every served shape
// come from Node's own code (testdata/golden.json, made by testdata/gen.mjs).
package yamledit

import (
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/ledger"
)

// Unsupported is a document or a value Go does not write the way Node does.
type Unsupported struct{ Reason string }

func (e *Unsupported) Error() string { return "yamledit: not served in Go: " + e.Reason }

// ErrNotMapping is editDoc's failure: a parent of the key path is not a
// mapping (Node throws, and editDoc reports it as a usage error).
var ErrNotMapping = errors.New("yamledit: a parent of the key path is not a mapping")

func refuse(reason string) error { return &Unsupported{Reason: reason} }

// IsUnsupported reports whether err is a shape Go does not serve.
func IsUnsupported(err error) bool {
	var u *Unsupported
	return errors.As(err, &u)
}

// Set is editDoc's set change: the text with segs set to value.
func Set(text string, segs []string, value any) (string, error) {
	doc, err := parse(text)
	if err != nil {
		return "", err
	}
	created, err := createNode(value)
	if err != nil {
		return "", err
	}
	if err := checkSegs(segs); err != nil {
		return "", err
	}
	if err := doc.setIn(segs, created); err != nil {
		return "", err
	}
	return doc.finish()
}

// Remove is editDoc's remove change: segs deleted, then up to prune empty
// parents deleted.
func Remove(text string, segs []string, prune int) (string, error) {
	doc, err := parse(text)
	if err != nil {
		return "", err
	}
	if err := checkSegs(segs); err != nil {
		return "", err
	}
	if err := doc.deleteIn(segs); err != nil {
		return "", err
	}
	for i := 0; i < prune; i++ {
		parent := segs[:len(segs)-1-i]
		if len(parent) == 0 {
			break
		}
		node := doc.getIn(parent)
		if node == nil || node.kind == kScalar || len(node.pairs)+len(node.items) > 0 {
			break
		}
		if err := doc.deleteIn(parent); err != nil {
			return "", err
		}
	}
	return doc.finish()
}

func checkSegs(segs []string) error {
	if len(segs) == 0 {
		return refuse("empty key path")
	}
	for _, seg := range segs {
		if !plainKey(seg) {
			return refuse("key " + strconv.Quote(seg) + " is not a plain key")
		}
	}
	return nil
}

// finish stringifies the edited document and checks it reads back as the
// edited value.
func (d *document) finish() (string, error) {
	out, err := d.stringify()
	if err != nil {
		return "", err
	}
	want, err := canon.Marshal(d.contents.js())
	if err != nil {
		return "", refuse("value: " + err.Error())
	}
	value, err := config.ParseYAML(out)
	if err != nil {
		return "", refuse("the written text does not parse: " + err.Error())
	}
	got, err := canon.Marshal(value)
	if err != nil || string(got) != string(want) {
		return "", refuse("the written text does not read back as the edited value")
	}
	return out, nil
}

// ---------------------------------------------------------------- model

const (
	kScalar = iota
	kMap
	kSeq
)

// node is a yaml Node of the served subset. Decorations follow the yaml
// package: spaceBefore and commentBefore on a sequence item or a pair value,
// comment as the trailing (line or collection-end) comment. Comment strings
// keep yaml's form: the text after each '#', joined by the newline sources
// yaml joined them with.
type node struct {
	kind  int
	out   string // scalar: the stringified text
	value any    // scalar: the JS value
	flow  bool   // a flow collection
	// created: a string made by createNode, written plain in block context
	created bool
	pairs   []*pair
	items   []*node

	spaceBefore   bool
	commentBefore string
	comment       string
}

type pair struct {
	key           string
	spaceBefore   bool
	commentBefore string
	value         *node
}

type document struct {
	commentBefore string
	start, end    bool
	contents      *node
	comment       string
}

func (n *node) js() any {
	switch n.kind {
	case kScalar:
		return n.value
	case kSeq:
		out := make([]any, len(n.items))
		for i, item := range n.items {
			out[i] = item.js()
		}
		return out
	}
	out := map[string]any{}
	for _, p := range n.pairs {
		out[p.key] = p.value.js()
	}
	return out
}

func (n *node) find(key string) *pair {
	for _, p := range n.pairs {
		if p.key == key {
			return p
		}
	}
	return nil
}

// ---------------------------------------------------------------- edits

// setIn is Document.setIn / Collection.setIn on a mapping root.
func (d *document) setIn(segs []string, value *node) error {
	cur := d.contents
	for i, seg := range segs {
		if cur.kind != kMap {
			return refuse("a key path through a sequence")
		}
		if cur.flow && value.kind != kScalar {
			return refuse("a collection set inside a flow collection")
		}
		p := cur.find(seg)
		if i == len(segs)-1 {
			if p != nil {
				p.value = value
			} else {
				cur.pairs = append(cur.pairs, &pair{key: seg, value: value})
			}
			return nil
		}
		if p == nil {
			v := value
			for j := len(segs) - 1; j > i; j-- {
				v = &node{kind: kMap, pairs: []*pair{{key: segs[j], value: v}}}
			}
			cur.pairs = append(cur.pairs, &pair{key: seg, value: v})
			return nil
		}
		if p.value.kind == kScalar {
			return ErrNotMapping
		}
		cur = p.value
	}
	return nil
}

// deleteIn is Document.deleteIn / Collection.deleteIn on a mapping root.
func (d *document) deleteIn(segs []string) error {
	cur := d.contents
	for i, seg := range segs {
		if cur.kind != kMap {
			return refuse("a key path through a sequence")
		}
		p := cur.find(seg)
		if i == len(segs)-1 {
			if p != nil {
				for j, q := range cur.pairs {
					if q == p {
						cur.pairs = append(cur.pairs[:j:j], cur.pairs[j+1:]...)
						break
					}
				}
			}
			return nil
		}
		if p == nil || p.value.kind == kScalar {
			return ErrNotMapping
		}
		cur = p.value
	}
	return nil
}

// getIn is Document.getIn(path, true): nil when a segment is missing.
func (d *document) getIn(segs []string) *node {
	cur := d.contents
	for _, seg := range segs {
		if cur == nil || cur.kind != kMap {
			return nil
		}
		p := cur.find(seg)
		if p == nil {
			return nil
		}
		cur = p.value
	}
	return cur
}

// createNode is doc.createNode for a JSON value (ledger.Object keeps its
// key order, as a JS object does).
func createNode(value any) (*node, error) {
	switch v := value.(type) {
	case nil:
		return &node{kind: kScalar, out: "null"}, nil
	case bool:
		return &node{kind: kScalar, out: strconv.FormatBool(v), value: v}, nil
	case float64:
		out, err := stringifyNumber(v, 0)
		if err != nil {
			return nil, err
		}
		return &node{kind: kScalar, out: out, value: v}, nil
	case int:
		return createNode(float64(v))
	case string:
		out, err := createdString(v)
		if err != nil {
			return nil, err
		}
		return &node{kind: kScalar, out: out, value: v, created: true}, nil
	case []any:
		seq := &node{kind: kSeq}
		for _, item := range v {
			child, err := createNode(item)
			if err != nil {
				return nil, err
			}
			if child.kind != kScalar {
				return nil, refuse("a collection inside a sequence")
			}
			seq.items = append(seq.items, child)
		}
		return seq, nil
	case ledger.Object:
		m := &node{kind: kMap}
		for _, field := range v {
			if !plainKey(field.Key) {
				return nil, refuse("key " + strconv.Quote(field.Key) + " is not a plain key")
			}
			child, err := createNode(field.Value)
			if err != nil {
				return nil, err
			}
			if p := m.find(field.Key); p != nil {
				p.value = child
				continue
			}
			m.pairs = append(m.pairs, &pair{key: field.Key, value: child})
		}
		return m, nil
	}
	return nil, refuse("a value of an unknown type")
}

// ---------------------------------------------------------------- scalars

var (
	reInt      = regexp.MustCompile(`^[-+]?[0-9]+$`)
	reFloat    = regexp.MustCompile(`^[-+]?(?:\.[0-9]+|[0-9]+\.[0-9]*)$`)
	reFloatExp = regexp.MustCompile(`^[-+]?(?:\.[0-9]+|[0-9]+(?:\.[0-9]*)?)[eE][-+]?[0-9]+$`)
	reFloatNaN = regexp.MustCompile(`^(?:[-+]?\.(?:inf|Inf|INF)|\.nan|\.NaN|\.NAN)$`)
	reIntOct   = regexp.MustCompile(`^0o[0-7]+$`)
	reIntHex   = regexp.MustCompile(`^0x[0-9a-fA-F]+$`)
	reNull     = regexp.MustCompile(`^(?:~|[Nn]ull|NULL)?$`)
	reBool     = regexp.MustCompile(`^(?:[Tt]rue|TRUE|[Ff]alse|FALSE)$`)
	reKey      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
	rePlainBad = regexp.MustCompile(`^[\n\t ,[\]{}#&*!|>'"%@` + "`" + `]|^[?-]$|^[?-][ \t]|[\n:][ \t]|[ \t]\n|[\n\t ]#|[\n\t :]$`)
	reDocMark  = regexp.MustCompile(`(?m)^(%|---|\.\.\.)`)
)

// coreTag reports whether a plain string would read as a core-schema type
// other than a string (the actualString test of plainString).
func coreTag(s string) bool {
	return reNull.MatchString(s) || reBool.MatchString(s) || reIntOct.MatchString(s) || reInt.MatchString(s) ||
		reIntHex.MatchString(s) || reFloatNaN.MatchString(s) || reFloatExp.MatchString(s) || reFloat.MatchString(s)
}

func plainKey(s string) bool {
	return reKey.MatchString(s) && !coreTag(s)
}

// safeText is printable ASCII: the strings Go writes as yaml does.
func safeText(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// jsNumber is JS Number#toString for a finite double.
func jsNumber(v float64) (string, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "", refuse("a number that is not finite")
	}
	if v == 0 {
		return "0", nil
	}
	if a := math.Abs(v); a < 1e-6 || a >= 1e21 {
		e := strconv.FormatFloat(v, 'e', -1, 64) // 1.5e-07, 1e+21
		i := strings.IndexByte(e, 'e')
		exp := strings.TrimLeft(e[i+2:], "0")
		return e[:i+2] + exp, nil
	}
	return strconv.FormatFloat(v, 'f', -1, 64), nil
}

// stringifyNumber is the yaml package's stringifyNumber.
func stringifyNumber(v float64, minFractionDigits int) (string, error) {
	var n string
	if v == 0 && math.Signbit(v) {
		n = "-0"
	} else {
		var err error
		if n, err = jsNumber(v); err != nil {
			return "", err
		}
	}
	if minFractionDigits > 0 && n[0] >= '0' && n[0] <= '9' {
		i := strings.IndexByte(n, '.')
		if i < 0 {
			i = len(n)
			n += "."
		}
		for d := minFractionDigits - (len(n) - i - 1); d > 0; d-- {
			n += "0"
		}
	}
	return n, nil
}

func singleQuoted(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }

func doubleQuoted(v string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(v, `\`, `\\`), `"`, `\"`) + `"`
}

// quotedString is the yaml package's quotedString (singleQuote unset).
func quotedString(v string) string {
	if strings.Contains(v, `"`) && !strings.Contains(v, "'") {
		return singleQuoted(v)
	}
	return doubleQuoted(v)
}

var reFlowChar = regexp.MustCompile(`[[\]{},]`)

// createdString is plainString for a string made by createNode (a value
// position, block context).
func createdString(v string) (string, error) {
	if !safeText(v) {
		return "", refuse("a string with a character outside printable ASCII")
	}
	if rePlainBad.MatchString(v) {
		return quotedString(v), nil
	}
	if reDocMark.MatchString(v) {
		return "", refuse("a string that starts like a document marker")
	}

	if coreTag(v) {
		return quotedString(v), nil
	}
	return v, nil
}

// plainScalar reads a plain scalar source as the core schema does.
func plainScalar(src string) (*node, error) {
	n := &node{kind: kScalar, out: src}
	switch {
	case reNull.MatchString(src):
		return n, nil
	case reBool.MatchString(src):
		n.value = src[0] == 't' || src[0] == 'T'
		return n, nil
	case reIntOct.MatchString(src), reIntHex.MatchString(src), reFloatNaN.MatchString(src), reFloatExp.MatchString(src):
		return nil, refuse("a number form Go does not write: " + src)
	case reInt.MatchString(src):
		digits := strings.TrimLeft(src, "+-")
		if len(strings.TrimLeft(digits, "0")) > 15 {
			return nil, refuse("an integer with more than 15 digits")
		}
		v, err := strconv.ParseFloat(digits, 64)
		if err != nil {
			return nil, refuse("an integer Go cannot read")
		}
		if src[0] == '-' {
			v = -v
		}
		out, err := stringifyNumber(v, 0)
		if err != nil {
			return nil, err
		}
		n.out, n.value = out, v
		return n, nil
	case reFloat.MatchString(src):
		digits := strings.TrimLeft(strings.ReplaceAll(strings.TrimLeft(src, "+-"), ".", ""), "0")
		if len(digits) > 15 {
			return nil, refuse("a float with more than 15 digits")
		}
		v, err := strconv.ParseFloat(src, 64)
		if err != nil {
			return nil, refuse("a float Go cannot read")
		}
		minFraction := 0
		if dot := strings.IndexByte(src, '.'); dot >= 0 && src[len(src)-1] == '0' {
			minFraction = len(src) - dot - 1
		}
		out, err := stringifyNumber(v, minFraction)
		if err != nil {
			return nil, err
		}
		n.out, n.value = out, v
		return n, nil
	}
	if rePlainBad.MatchString(src) || reDocMark.MatchString(src) {
		return nil, refuse("a plain string yaml writes quoted")
	}
	n.value = src
	return n, nil
}

// foldSafe reports that yaml's foldFlowLines leaves text on one line at the
// given context indent.
func foldSafe(text string, indent int) bool {
	limit := 1 + 80 - indent
	if limit < 21 {
		limit = 21
	}
	return len(text) <= limit
}

// ---------------------------------------------------------------- comments

// isLineBreak is a JS regexp line terminator (multiline ^ and $).
func isLineBreak(c byte) bool { return c == '\n' || c == '\r' }

// lineStarts calls fn at each multiline ^ position of s with whether the
// position is also a $ position.
func lineStarts(s string, fn func(at int, atEnd bool)) {
	for i := 0; i <= len(s); i++ {
		if i == 0 || isLineBreak(s[i-1]) {
			fn(i, i == len(s) || isLineBreak(s[i]))
		}
	}
}

// commentString is stringifyComment: str.replace(/^(?!$)(?: $)?/gm, '#').
func commentString(s string) string {
	var b strings.Builder
	last := 0
	lineStarts(s, func(at int, atEnd bool) {
		if atEnd {
			return
		}
		b.WriteString(s[last:at])
		b.WriteByte('#')
		last = at
		if s[at] == ' ' && (at+1 == len(s) || isLineBreak(s[at+1])) {
			last = at + 1
		}
	})
	b.WriteString(s[last:])
	return b.String()
}

// indentComment is the yaml package's indentComment.
func indentComment(s, indent string) string {
	onlyNewlines := s != ""
	for i := 0; i < len(s); i++ {
		if s[i] != '\n' {
			onlyNewlines = false
			break
		}
	}
	if onlyNewlines {
		return s[1:]
	}
	if indent == "" {
		return s
	}
	var b strings.Builder
	last := 0
	lineStarts(s, func(at int, _ bool) {
		j := at
		for j < len(s) && s[j] == ' ' {
			j++
		}
		if j == len(s) || isLineBreak(s[j]) {
			return // (?! *$)
		}
		b.WriteString(s[last:at])
		b.WriteString(indent)
		last = at
	})
	b.WriteString(s[last:])
	return b.String()
}

// lineComment is the yaml package's lineComment.
func lineComment(str, indent, comment string) string {
	if strings.HasSuffix(str, "\n") {
		return indentComment(comment, indent)
	}
	if strings.ContainsAny(comment, "\n") {
		return "\n" + indentComment(comment, indent)
	}
	if strings.HasSuffix(str, " ") {
		return comment
	}
	return " " + comment
}

// token is a lexer token of the trivia between content lines.
type token struct {
	comment bool
	source  string // comment: from '#'; newline: "\n" or "\r\n"
}

// resolveProps is the comment and blank-line part of the yaml package's
// resolveProps.
func resolveProps(tokens []token, startOnNewline bool) (spaceBefore bool, comment string) {
	atNewline := startOnNewline
	commentSep := ""
	for _, t := range tokens {
		if t.comment {
			cb := t.source[1:]
			if cb == "" {
				cb = " "
			}
			if comment == "" {
				comment = cb
			} else {
				comment += commentSep + cb
			}
			commentSep = ""
			atNewline = false
			continue
		}
		if atNewline {
			if comment != "" {
				comment += t.source
			} else {
				spaceBefore = true
			}
		} else {
			commentSep += t.source
		}
		atNewline = true
	}
	return spaceBefore, comment
}

// resolveEnd is the comment part of the yaml package's resolveEnd.
func resolveEnd(tokens []token) string {
	comment, sep := "", ""
	for _, t := range tokens {
		if t.comment {
			cb := t.source[1:]
			if cb == "" {
				cb = " "
			}
			if comment == "" {
				comment = cb
			} else {
				comment += sep + cb
			}
			sep = ""
		} else if comment != "" {
			sep += t.source
		}
	}
	return comment
}

// joinComment appends a comment on a new line, as the Composer does.
func joinComment(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "\n" + b
}

// parsePrelude is the Composer's parsePrelude for comment and newline
// sources.
func parsePrelude(tokens []token) (comment string, afterEmptyLine bool) {
	atComment := false
	for _, t := range tokens {
		if t.comment {
			cb := t.source[1:]
			if cb == "" {
				cb = " "
			}
			switch {
			case comment == "":
			case afterEmptyLine:
				comment += "\n\n"
			default:
				comment += "\n"
			}
			comment += cb
			atComment, afterEmptyLine = true, false
			continue
		}
		if !atComment {
			afterEmptyLine = true
		}
		atComment = false
	}
	return comment, afterEmptyLine
}

// ---------------------------------------------------------------- stringify

func (d *document) stringify() (string, error) {
	lines := []string{}
	if d.start {
		lines = append(lines, "---")
	}
	if d.commentBefore != "" {
		if len(lines) != 1 {
			lines = append([]string{""}, lines...)
		}
		lines = append([]string{indentComment(commentString(d.commentBefore), "")}, lines...)
	}
	if d.contents.spaceBefore && d.start {
		lines = append(lines, "")
	}
	if d.contents.commentBefore != "" {
		lines = append(lines, indentComment(commentString(d.contents.commentBefore), ""))
	}
	body, done, err := stringifyNode(d.contents, "")
	if err != nil {
		return "", err
	}
	if d.contents.comment != "" && !done {
		body += lineComment(body, "", commentString(d.contents.comment))
	}
	lines = append(lines, body)
	if d.end {
		if d.comment != "" {
			cs := commentString(d.comment)
			if strings.Contains(cs, "\n") {
				lines = append(lines, "...", indentComment(cs, ""))
			} else {
				lines = append(lines, "... "+cs)
			}
		} else {
			lines = append(lines, "...")
		}
	} else if d.comment != "" {
		if lines[len(lines)-1] != "" {
			lines = append(lines, "")
		}
		lines = append(lines, indentComment(commentString(d.comment), ""))
	}
	return strings.Join(lines, "\n") + "\n", nil
}

// stringifyNode stringifies a node at a context indent; done reports that a
// collection wrote its own comment (the onComment callback).
func stringifyNode(n *node, indent string) (string, bool, error) {
	if n.kind == kScalar {
		if !foldSafe(n.out, len(indent)) {
			return "", false, refuse("a scalar long enough for yaml to fold")
		}
		return n.out, false, nil
	}
	if n.flow {
		str, err := stringifyFlow(n)
		if err != nil {
			return "", false, err
		}
		if len(indent)+len(str) > 60 {
			return "", false, refuse("a flow collection long enough for yaml to break")
		}
		return str, false, nil
	}
	// stringifyBlockCollection
	lines := []string{}
	add := func(spaceBefore bool, commentBefore string) {
		if spaceBefore {
			lines = append(lines, "")
		}
		if commentBefore != "" {
			lines = append(lines, strings.TrimLeft(indentComment(commentString(commentBefore), indent), " \t\n\r"))
		}
	}
	if n.kind == kMap {
		for _, p := range n.pairs {
			add(p.spaceBefore, p.commentBefore)
			str, err := stringifyPair(p, indent)
			if err != nil {
				return "", false, err
			}
			lines = append(lines, str)
		}
	} else {
		itemIndent := indent + "  "
		for _, item := range n.items {
			add(item.spaceBefore, item.commentBefore)
			str, _, err := stringifyNode(item, itemIndent)
			if err != nil {
				return "", false, err
			}
			if item.comment != "" {
				str += lineComment(str, itemIndent, commentString(item.comment))
			}
			lines = append(lines, "- "+str)
		}
	}
	var str string
	if len(lines) == 0 {
		if n.kind == kSeq {
			str = "[]"
		} else {
			str = "{}"
		}
	} else {
		str = lines[0]
		for _, line := range lines[1:] {
			if line == "" {
				str += "\n"
			} else {
				str += "\n" + indent + line
			}
		}
	}
	if n.comment != "" {
		str += "\n" + indentComment(commentString(n.comment), indent)
		return str, true, nil
	}
	return str, false, nil
}

// stringifyPair is the yaml package's stringifyPair for a plain key.
func stringifyPair(p *pair, indent string) (string, error) {
	ctxIndent := indent + "  "
	str := p.key + ":"
	value := p.value
	valueStr, done, err := stringifyNode(value, ctxIndent)
	if err != nil {
		return "", err
	}
	ws := " "
	switch {
	case value.spaceBefore || value.commentBefore != "":
		ws = ""
		if value.spaceBefore {
			ws = "\n"
		}
		if value.commentBefore != "" {
			ws += "\n" + indentComment(commentString(value.commentBefore), ctxIndent)
		}
		if valueStr == "" {
			if ws == "\n" && value.comment != "" {
				ws = "\n\n"
			}
		} else {
			ws += "\n" + ctxIndent
		}
	case value.kind != kScalar:
		flow := value.flow || len(value.pairs)+len(value.items) == 0
		if strings.Contains(valueStr, "\n") || !flow {
			ws = "\n" + ctxIndent
		}
	case valueStr == "" || valueStr[0] == '\n':
		ws = ""
	}
	str += ws + valueStr
	if value.comment != "" && !done {
		str += lineComment(str, ctxIndent, commentString(value.comment))
	}
	return str, nil
}

// ---------------------------------------------------------------- parse

type line struct {
	text   string
	nl     string
	indent int
	rest   string
	kind   int
}

const (
	lBlank = iota
	lComment
	lDocStart
	lDocEnd
	lContent
)

// content is a parsed content line.
type content struct {
	indent  int
	item    bool
	key     string
	value   *node // nil: a key whose value is on the next lines
	comment string
}

func splitLines(text string) ([]line, error) {
	if !utf8.ValidString(text) {
		return nil, refuse("bytes that are not UTF-8")
	}
	for _, r := range text {
		switch {
		case r == '\t':
			return nil, refuse("a tab")
		case r == '\n' || r == '\r':
		case r < 0x20 || r == 0x7f || r >= 0x80 && r <= 0x9f:
			return nil, refuse("a control character")
		case r == 0xfeff || r == 0x2028 || r == 0x2029:
			return nil, refuse("a byte order mark or a Unicode line separator")
		}
	}
	lines := []line{}
	for len(text) > 0 {
		l := line{}
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			l.text, l.nl, text = text[:i], "\n", text[i+1:]
			if strings.HasSuffix(l.text, "\r") {
				l.text, l.nl = l.text[:len(l.text)-1], "\r\n"
			}
		} else {
			l.text, text = text, ""
		}
		if strings.Contains(l.text, "\r") {
			return nil, refuse("a carriage return that does not end a line")
		}
		l.rest = strings.TrimLeft(l.text, " ")
		l.indent = len(l.text) - len(l.rest)
		switch {
		case l.rest == "":
			l.kind = lBlank
		case l.rest[0] == '#':
			l.kind = lComment
		case l.text == "---":
			l.kind = lDocStart
		case l.text == "..." || strings.HasPrefix(l.text, "... ") && strings.HasPrefix(strings.TrimLeft(l.text[3:], " "), "#"):
			l.kind = lDocEnd
		default:
			if strings.HasPrefix(l.text, "---") || strings.HasPrefix(l.text, "...") {
				return nil, refuse("a document marker with more on its line")
			}
			l.kind = lContent
		}
		lines = append(lines, l)
	}
	return lines, nil
}

// parseFlow reads a one-line flow collection at the start of s and returns
// it and the index after it.
func parseFlow(s string) (*node, int, error) {
	n := &node{kind: kSeq, flow: true}
	closer := byte(']')
	if s[0] == '{' {
		n.kind, closer = kMap, '}'
	}
	i := 1
	skip := func() {
		for i < len(s) && s[i] == ' ' {
			i++
		}
	}
	value := func() (*node, error) {
		if i >= len(s) {
			return nil, refuse("a flow collection over more than one line")
		}
		switch s[i] {
		case '[', '{':
			child, end, err := parseFlow(s[i:])
			if err != nil {
				return nil, err
			}
			i += end
			return child, nil
		case '\'', '"':
			child, end, err := readQuoted(s[i:])
			if err != nil {
				return nil, err
			}
			i += end
			return child, nil
		}
		j := i
		for j < len(s) && !strings.ContainsRune(",[]{}#", rune(s[j])) {
			j++
		}
		src := strings.TrimRight(s[i:j], " ")
		if src == "" || j < len(s) && s[j] == '#' || strings.Contains(src, ":") || src[0] == '?' || src[0] == '-' && (len(src) == 1 || src[1] == ' ') || !safeText(src) {
			return nil, refuse("a flow item Go does not read")
		}
		child, err := plainScalar(src)
		if err != nil {
			return nil, err
		}
		if child.out != src && child.value == nil {
			return nil, refuse("a flow item Go does not read")
		}
		i += len(src)
		return child, nil
	}
	skip()
	if i < len(s) && s[i] == closer {
		return n, i + 1, nil
	}
	for {
		skip()
		if n.kind == kMap {
			j := strings.IndexByte(s[i:], ':')
			if j <= 0 || !plainKey(s[i:i+j]) || i+j+1 >= len(s) || s[i+j+1] != ' ' {
				return nil, 0, refuse("a flow mapping key Go does not read")
			}
			key := s[i : i+j]
			if n.find(key) != nil {
				return nil, 0, refuse("a key written twice")
			}
			i += j + 1
			skip()
			child, err := value()
			if err != nil {
				return nil, 0, err
			}
			n.pairs = append(n.pairs, &pair{key: key, value: child})
		} else {
			child, err := value()
			if err != nil {
				return nil, 0, err
			}
			n.items = append(n.items, child)
		}
		skip()
		if i >= len(s) {
			return nil, 0, refuse("a flow collection over more than one line")
		}
		if s[i] == closer {
			return n, i + 1, nil
		}
		if s[i] != ',' {
			return nil, 0, refuse("a flow collection Go does not read")
		}
		i++
		skip()
		if i < len(s) && s[i] == closer {
			return nil, 0, refuse("a trailing comma in a flow collection")
		}
	}
}

// stringifyFlow is stringifyFlowCollection on one line.
func stringifyFlow(n *node) (string, error) {
	parts := []string{}
	for _, item := range n.items {
		str, err := flowPart(item)
		if err != nil {
			return "", err
		}
		parts = append(parts, str)
	}
	for _, p := range n.pairs {
		if p.spaceBefore || p.commentBefore != "" {
			return "", refuse("a comment inside a flow collection")
		}
		str, err := flowPart(p.value)
		if err != nil {
			return "", err
		}
		parts = append(parts, p.key+": "+str)
	}
	start, end := "[", "]"
	if n.kind == kMap {
		start, end = "{", "}"
	}
	if len(parts) == 0 {
		return start + end, nil
	}
	return start + " " + strings.Join(parts, ", ") + " " + end, nil
}

func flowPart(n *node) (string, error) {
	if n.spaceBefore || n.commentBefore != "" || n.comment != "" {
		return "", refuse("a comment inside a flow collection")
	}
	if n.kind != kScalar {
		if !n.flow {
			return "", refuse("a block collection inside a flow collection")
		}
		return stringifyFlow(n)
	}
	if n.out == "" {
		return "", refuse("an empty value inside a flow collection")
	}
	if v, ok := n.value.(string); ok && n.created && n.out == v && reFlowChar.MatchString(v) {
		return quotedString(v), nil // plainString's inFlow rule
	}
	return n.out, nil
}

// readQuoted reads a one-line quoted scalar at the start of s and returns it
// and the index after it.
func readQuoted(s string) (*node, int, error) {
	if s[0] == '"' {
		j := strings.IndexByte(s[1:], '"')
		if j < 0 {
			return nil, 0, refuse("a quoted string over more than one line")
		}
		v := s[1 : 1+j]
		if strings.Contains(v, `\`) || !safeText(v) {
			return nil, 0, refuse("a double-quoted string with an escape")
		}
		return &node{kind: kScalar, out: s[:j+2], value: v}, j + 2, nil
	}
	i := 1
	var b strings.Builder
	for {
		j := strings.IndexByte(s[i:], '\'')
		if j < 0 {
			return nil, 0, refuse("a quoted string over more than one line")
		}
		b.WriteString(s[i : i+j])
		i += j + 1
		if i < len(s) && s[i] == '\'' {
			b.WriteByte('\'')
			i++
			continue
		}
		break
	}
	v := b.String()
	if !safeText(v) || singleQuoted(v) != s[:i] {
		return nil, 0, refuse("a quoted string Go does not write")
	}
	return &node{kind: kScalar, out: s[:i], value: v}, i, nil
}

// splitValue reads an inline value and its line comment.
func splitValue(s string) (*node, string, error) {
	var n *node
	var rest string
	switch {
	case s[0] == '[' || s[0] == '{':
		flow, end, err := parseFlow(s)
		if err != nil {
			return nil, "", err
		}
		n, rest = flow, s[end:]
	case s[0] == '\'' || s[0] == '"':
		q, end, err := readQuoted(s)
		if err != nil {
			return nil, "", err
		}
		n, rest = q, s[end:]
	default:
		src, comment := s, ""
		if i := strings.Index(s, " #"); i >= 0 {
			src, comment = s[:i], s[i+1:]
		}
		src = strings.TrimRight(src, " ")
		if !safeText(src) || strings.HasPrefix(src, "- ") || src == "-" || src[0] == '?' || src[0] == ':' || strings.Contains(src, ": ") || strings.HasSuffix(src, ":") {
			return nil, "", refuse("a plain value Go does not read")
		}
		node, err := plainScalar(src)
		if err != nil {
			return nil, "", err
		}
		if comment != "" {
			comment = comment[1:]
			if comment == "" {
				comment = " "
			}
		}
		return node, comment, nil
	}
	trimmed := strings.TrimLeft(rest, " ")
	switch {
	case trimmed == "":
		return n, "", nil
	case trimmed[0] == '#' && len(trimmed) < len(rest):
		comment := trimmed[1:]
		if comment == "" {
			comment = " "
		}
		return n, comment, nil
	}
	return nil, "", refuse("text after a quoted value or flow collection")
}

func parseContent(l line) (content, error) {
	c := content{indent: l.indent}
	rest := l.rest
	if rest == "-" || strings.HasPrefix(rest, "- ") {
		c.item = true
		v := strings.TrimLeft(strings.TrimPrefix(rest, "-"), " ")
		if v == "" || v[0] == '#' {
			return c, refuse("an empty sequence item")
		}
		if v == "-" || strings.HasPrefix(v, "- ") || keyLine(v) {
			return c, refuse("a collection inside a sequence item")
		}
		value, comment, err := splitValue(v)
		if err != nil {
			return c, err
		}
		if value.kind != kScalar {
			return c, refuse("a flow collection inside a sequence")
		}
		c.value, c.comment = value, comment
		return c, nil
	}
	colon := strings.IndexByte(rest, ':')
	if colon <= 0 || !plainKey(rest[:colon]) || colon+1 < len(rest) && rest[colon+1] != ' ' {
		return c, refuse("a line that is not a plain key or a sequence item")
	}
	c.key = rest[:colon]
	v := strings.TrimLeft(rest[colon+1:], " ")
	if v == "" {
		return c, nil
	}
	if v[0] == '#' {
		return c, refuse("a comment after a key with no value on its line")
	}
	value, comment, err := splitValue(v)
	if err != nil {
		return c, err
	}
	c.value, c.comment = value, comment
	return c, nil
}

func keyLine(v string) bool {
	colon := strings.IndexByte(v, ':')
	return colon > 0 && (colon+1 == len(v) || v[colon+1] == ' ')
}

// trivia is the comment and blank lines between two content lines.
type trivia struct {
	lines []line
}

func (t trivia) tokens() []token {
	out := []token{}
	for _, l := range t.lines {
		if l.kind == lComment {
			out = append(out, token{comment: true, source: l.rest})
		}
		if l.nl != "" {
			out = append(out, token{source: l.nl})
		}
	}
	return out
}

// commentIndent is the indent of the first comment line, or -1 when there
// are none. Later comment lines may be less indented (yaml gives the whole run
// to the collection of the first) but never more, and never less than floor.
func (t trivia) commentIndent(floor int) (int, error) {
	k := -1
	for _, l := range t.lines {
		if l.kind != lComment {
			continue
		}
		switch {
		case k < 0:
			k = l.indent
		case l.indent > k:
			return 0, refuse("a comment line more indented than the one before it")
		}
		if l.indent < floor {
			return 0, refuse("a comment less indented than the next line")
		}
	}
	return k, nil
}

// mixed reports comment lines at more than one indent.
func (t trivia) mixed() bool {
	k := -1
	for _, l := range t.lines {
		if l.kind == lComment {
			if k >= 0 && l.indent != k {
				return true
			}
			k = l.indent
		}
	}
	return false
}

// commentIndentLast is the indent of the last comment line, or -1.
func (t trivia) commentIndentLast() (int, error) {
	k := -1
	for _, l := range t.lines {
		if l.kind == lComment {
			k = l.indent
		}
	}
	return k, nil
}

// attachTrailing gives a run of comment lines to a collection as its end
// comment.
func attachTrailing(target *node, t trivia) {
	_, comment := resolveProps(t.tokens(), true)
	if target.kind == kMap && target.comment != "" {
		target.comment += "\n" + comment
	} else {
		target.comment = comment
	}
}

type frame struct {
	indent int
	node   *node
}

func parse(text string) (*document, error) {
	lines, err := splitLines(text)
	if err != nil {
		return nil, err
	}
	doc := &document{}
	i := 0
	// the prelude: comments and blank lines before "---" or the first content
	prelude := trivia{}
	for i < len(lines) && (lines[i].kind == lBlank || lines[i].kind == lComment) {
		prelude.lines = append(prelude.lines, lines[i])
		i++
	}
	for _, l := range prelude.lines {
		if l.indent > 0 {
			return nil, refuse("an indented line before the document")
		}
	}
	preludeComment, afterEmptyLine := parsePrelude(prelude.tokens())
	var firstTrivia trivia
	if i < len(lines) && lines[i].kind == lDocStart {
		doc.start = true
		i++
		for i < len(lines) && (lines[i].kind == lBlank || lines[i].kind == lComment) {
			firstTrivia.lines = append(firstTrivia.lines, lines[i])
			i++
		}
	}
	if i >= len(lines) || lines[i].kind != lContent {
		return nil, refuse("no mapping content")
	}
	first, err := parseContent(lines[i])
	if err != nil {
		return nil, err
	}
	if first.item || first.indent != 0 {
		return nil, refuse("a document that is not a block mapping at column 0")
	}
	root := &node{kind: kMap}
	doc.contents = root
	if doc.start {
		if k, err := firstTrivia.commentIndent(0); err != nil || k > 0 || firstTrivia.mixed() {
			return nil, refuse("an indented comment after ---")
		}
		root.spaceBefore, root.commentBefore = resolveProps(firstTrivia.tokens(), true)
	}
	stack := []frame{{0, root}}
	var pending *pair
	pendingIndent := 0
	add := func(c content, spaceBefore bool, commentBefore string) error {
		top := stack[len(stack)-1].node
		if c.item {
			c.value.spaceBefore, c.value.commentBefore, c.value.comment = spaceBefore, commentBefore, c.comment
			top.items = append(top.items, c.value)
			return nil
		}
		if top.find(c.key) != nil {
			return refuse("a key written twice")
		}
		p := &pair{key: c.key, spaceBefore: spaceBefore, commentBefore: commentBefore}
		top.pairs = append(top.pairs, p)
		if c.value == nil {
			pending, pendingIndent = p, c.indent
			return nil
		}
		c.value.comment = c.comment
		p.value = c.value
		return nil
	}
	if err := add(first, false, ""); err != nil {
		return nil, err
	}
	if preludeComment != "" {
		if afterEmptyLine || doc.start {
			doc.commentBefore = preludeComment
		} else {
			root.pairs[0].commentBefore = preludeComment
		}
	}
	prevNL := lines[i].nl
	i++
	// closePending ends a key with an empty value; at the end of the document
	// yaml gives blank lines after it to the empty value.
	closePending := func(t trivia, atEnd bool) error {
		if pending == nil {
			return nil
		}
		if k, _ := t.commentIndent(0); k >= 0 {
			return refuse("a comment after a key with an empty value")
		}
		pending.value = &node{kind: kScalar, out: "", spaceBefore: atEnd && len(t.lines) > 0}
		pending = nil
		return nil
	}
	// trailing attaches comment lines deeper than the next content line to the
	// open collection at their indent (the collection-end comment).
	trailing := func(t trivia, k int) error {
		var target *node
		for _, f := range stack {
			if f.indent == k {
				target = f.node // the innermost open collection at k
			}
		}
		if target == nil {
			return refuse("a comment at the indent of no open collection")
		}
		attachTrailing(target, t)
		return nil
	}
	for {
		t := trivia{}
		for i < len(lines) && (lines[i].kind == lBlank || lines[i].kind == lComment) {
			t.lines = append(t.lines, lines[i])
			i++
		}
		if i >= len(lines) || lines[i].kind != lContent {
			// the end of the document
			if err := closePending(t, true); err != nil {
				return nil, err
			}
			k, err := t.commentIndent(0)
			if err != nil {
				return nil, err
			}
			switch {
			case k == 0:
				if k2, _ := t.commentIndentLast(); k2 != 0 {
					return nil, refuse("a comment line more indented than the one before it")
				}
				doc.comment = resolveEnd(t.tokens())
			case k > 0:
				if err := trailing(t, k); err != nil {
					return nil, err
				}
			}
			if i < len(lines) {
				if lines[i].kind != lDocEnd {
					return nil, refuse("a second document")
				}
				doc.end = true
				// a comment on the "..." line, then comment lines after it
				if rest := strings.TrimLeft(lines[i].text[3:], " "); rest != "" {
					doc.comment = joinComment(doc.comment, resolveEnd([]token{{comment: true, source: rest}}))
				}
				after := trivia{}
				for i++; i < len(lines); i++ {
					if lines[i].kind != lBlank && lines[i].kind != lComment || lines[i].indent > 0 {
						return nil, refuse("text after the document end marker")
					}
					after.lines = append(after.lines, lines[i])
				}
				comment, _ := parsePrelude(after.tokens())
				doc.comment = joinComment(doc.comment, comment)
			}
			break
		}
		c, err := parseContent(lines[i])
		if err != nil {
			return nil, err
		}
		nl := lines[i].nl
		k, err := t.commentIndent(c.indent)
		if err != nil {
			return nil, err
		}
		if pending != nil && (!c.item && c.indent > pendingIndent || c.item && c.indent >= pendingIndent) {
			// the first line of a block value: the trivia are the value's props
			if k >= 0 && (k != c.indent || t.mixed()) {
				return nil, refuse("a comment before a block value at another indent")
			}
			value := &node{kind: kMap}
			if c.item {
				value.kind = kSeq
			}
			value.spaceBefore, value.commentBefore = resolveProps(append([]token{{source: prevNL}}, t.tokens()...), false)
			pending.value = value
			pending = nil
			stack = append(stack, frame{c.indent, value})
			if err := add(c, false, ""); err != nil {
				return nil, err
			}
			prevNL = nl
			i++
			continue
		}
		if err := closePending(t, false); err != nil {
			return nil, err
		}
		spaceBefore, commentBefore := false, ""
		top := stack[len(stack)-1]
		zeroSeq := !c.item && c.indent > 0 && top.node.kind == kSeq && top.indent == c.indent
		switch {
		case k > c.indent || k == c.indent && zeroSeq:
			if err := trailing(t, k); err != nil {
				return nil, err
			}
		case k >= 0 && t.mixed():
			return nil, refuse("comment lines at different indents")
		default:
			spaceBefore, commentBefore = resolveProps(t.tokens(), true)
		}
		for len(stack) > 1 && stack[len(stack)-1].indent > c.indent {
			stack = stack[:len(stack)-1]
		}
		if top := stack[len(stack)-1]; top.node.kind == kSeq && !c.item && top.indent == c.indent {
			stack = stack[:len(stack)-1]
		}
		top = stack[len(stack)-1]
		if top.indent != c.indent || (top.node.kind == kSeq) != c.item {
			return nil, refuse("a line at an indent of no open collection")
		}
		if err := add(c, spaceBefore, commentBefore); err != nil {
			return nil, err
		}
		prevNL = nl
		i++
	}
	// the safety net: Go's model reads as the Go YAML reader reads the text
	value, err := config.ParseYAML(text)
	if err != nil {
		return nil, refuse("the text does not parse: " + err.Error())
	}
	want, errA := canon.Marshal(value)
	got, errB := canon.Marshal(root.js())
	if errA != nil || errB != nil || string(want) != string(got) {
		return nil, refuse("the text reads differently in Go")
	}
	return doc, nil
}
