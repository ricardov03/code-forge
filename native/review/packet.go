// Package review ports Node's one-file review (src/review/packet.mjs, engine.mjs,
// validate-review.mjs) for the cases Go reproduces byte for byte. Anything else
// returns ErrUnsupported before a session is spawned, so the caller keeps its
// Node hand-over for that ticket.
package review

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/docsync"
	"github.com/ricardov03/code-forge/native/gitx"
)

// ErrUnsupported marks an input Go does not review yet; nothing was spawned.
var ErrUnsupported = errors.New("review: case not ported to Go")

func unsupported(why string) error { return fmt.Errorf("%w: %s", ErrUnsupported, why) }

// PacketError mirrors Node's PacketError: a refusal with a stable code
// (`bad-path`, `bad-lens`, `git`).
type PacketError struct{ Code, Message string }

func (e *PacketError) Error() string { return e.Message }

// Lenses lists the lens files Node knows.
var Lenses = []string{"quick", "full", "recheck", "A", "B", "judge"}

// DigestMaxLines caps the rules digest (§4.3).
const DigestMaxLines = 60

var defaultBudgets = map[string]int{"quick_in": 6000, "full_in": 32000, "judge_in": 8000}

// LensDir names the lens directory; tests point it at a fixture tree.
var LensDir = func() (string, error) {
	root, err := docsync.PackageRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "src", "review", "lenses"), nil
}

// jsSpace reports the characters String.prototype.trim removes.
func jsSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

func trimEnd(s string) string { return strings.TrimRightFunc(s, jsSpace) }
func trimJS(s string) string  { return strings.TrimFunc(s, jsSpace) }

// EstimateTokens is Node's bytes / 4, rounded up.
func EstimateTokens(text string) int { return (len(text) + 3) / 4 }

// LoadLens reads one lens file, trailing white space removed.
func LoadLens(name string) (string, error) {
	known := false
	for _, l := range Lenses {
		known = known || l == name
	}
	if !known {
		q, _ := jsonString(name)
		return "", &PacketError{"bad-lens", "unknown lens " + q}
	}
	dir, err := LensDir()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(dir, name+".md"))
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", unsupported("lens is not UTF-8")
	}
	return trimEnd(string(data)), nil
}

// section returns cfg[a][b]... as a map, or nil.
func section(cfg map[string]any, keys ...string) map[string]any {
	cur := cfg
	for _, k := range keys {
		next, _ := cur[k].(map[string]any)
		if next == nil {
			return nil
		}
		cur = next
	}
	return cur
}

// intOf reports whether v is a JS integer (Number.isInteger) and its value.
func intOf(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case uint64:
		return int(n), true
	case float64:
		if n == math.Trunc(n) && !math.IsInf(n, 0) {
			return int(n), true
		}
	}
	return 0, false
}

// numOf reports whether v is a finite JS number and its value.
func numOf(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint64:
		return float64(n), true
	case float64:
		return n, !math.IsNaN(n) && !math.IsInf(n, 0)
	}
	return 0, false
}

// BudgetFor is `review.budgets.<key>` when a positive integer, else the default.
func BudgetFor(cfg map[string]any, key string) int {
	if v, ok := intOf(section(cfg, "review", "budgets")[key]); ok && v > 0 {
		return v
	}
	return defaultBudgets[key]
}

// Hunk is one `@@` header of a unified diff.
type Hunk struct {
	Header                                 string
	OldStart, OldLines, NewStart, NewLines int
}

var hunkRE = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// ParseDiff reads the hunks and +/- counts of one file's unified diff.
func ParseDiff(text string) (hunks []Hunk, plus, minus int) {
	lines := strings.Split(text, "\n")
	first := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "@@") {
			first = i
			break
		}
	}
	if first < 0 {
		return nil, 0, 0
	}
	num := func(s string, def int) int {
		if s == "" {
			return def
		}
		n, _ := strconv.Atoi(s)
		return n
	}
	for _, line := range lines[first:] {
		switch {
		case strings.HasPrefix(line, "@@"):
			m := hunkRE.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			hunks = append(hunks, Hunk{m[0], num(m[1], 0), num(m[2], 1), num(m[3], 0), num(m[4], 1)})
		case strings.HasPrefix(line, "+"):
			plus++
		case strings.HasPrefix(line, "-"):
			minus++
		}
	}
	return hunks, plus, minus
}

// SplitLines drops the empty string after a final newline.
func SplitLines(content *string) []string {
	if content == nil || *content == "" {
		return nil
	}
	lines := strings.Split(*content, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

type window struct{ start, end int }

func hunkWindows(hunks []Hunk, total, radius int) []window {
	if total == 0 {
		return nil
	}
	raw := []window{}
	for _, h := range hunks {
		anchor := max(1, h.NewStart)
		last := anchor + max(h.NewLines, 1) - 1
		w := window{max(1, anchor-radius), min(total, last+radius)}
		if w.start <= w.end {
			raw = append(raw, w)
		}
	}
	// stable sort by start, as Array.prototype.sort is stable
	for i := 1; i < len(raw); i++ {
		for j := i; j > 0 && raw[j].start < raw[j-1].start; j-- {
			raw[j], raw[j-1] = raw[j-1], raw[j]
		}
	}
	merged := []window{}
	for _, w := range raw {
		if n := len(merged); n > 0 && w.start <= merged[n-1].end+1 {
			merged[n-1].end = max(merged[n-1].end, w.end)
		} else {
			merged = append(merged, w)
		}
	}
	return merged
}

func contextSetting(cfg map[string]any, key string, def int) int {
	if v, ok := intOf(section(cfg, "review", "context")[key]); ok && v >= 0 {
		return v
	}
	return def
}

// Context is the rendered `## context` body.
type Context struct {
	Mode      string
	Text      string
	LineCount int
	Windows   []window
}

// BuildContext is context.mjs buildContext; mode is auto, minimal or recheck.
func BuildContext(file string, content *string, hunks []Hunk, cfg map[string]any, mode string) Context {
	lines := SplitLines(content)
	chosen := mode
	if mode != "minimal" && mode != "recheck" {
		chosen = "hunks"
		if len(lines) <= contextSetting(cfg, "whole_file_max_lines", 400) {
			chosen = "whole"
		}
	}
	numbered := func(out []string, start, end int) []string {
		for n := start; n <= end; n++ {
			out = append(out, strconv.Itoa(n)+"| "+lines[n-1])
		}
		return out
	}
	if chosen == "whole" {
		out := numbered([]string{fmt.Sprintf("### %s (whole file, %d lines)", file, len(lines))}, 1, len(lines))
		windows := []window{}
		if len(lines) > 0 {
			windows = append(windows, window{1, len(lines)})
		}
		return Context{chosen, strings.Join(out, "\n"), len(lines), windows}
	}
	radius := contextSetting(cfg, "hunk_context_lines", 40)
	if chosen == "minimal" {
		radius = contextSetting(cfg, "min_context_lines", 10)
	}
	parts := []string{}
	count := 0
	windows := hunkWindows(hunks, len(lines), radius)
	for _, w := range windows {
		parts = numbered(append(parts, fmt.Sprintf("### %s lines %d-%d", file, w.start, w.end)), w.start, w.end)
		count += w.end - w.start + 1
	}
	if len(windows) == 0 {
		parts = append(parts, "### "+file+" (no current content)")
	}
	return Context{chosen, strings.Join(parts, "\n"), count, windows}
}

// importHeaderExt is importHeaderEnd's file test, case-insensitive for ASCII
// letters only (a JS /i regex without u never folds ſ to s).
var importHeaderExt = regexp.MustCompile(`\.(?:[cCmM]?[jJtT][sS][xX]?|[cC][sS][sS]|[sS][cC][sS][sS]|[hH][tT][mM][lL]?|[aA][sS][tT][rR][oO]|[vV][uU][eE])$`)

var importHeaderTokenRE = regexp.MustCompile("//[^\\r\\n]*|/\\*" + jsAny + "*?\\*/|<!--" + jsAny + "*?-->|`(?:\\\\" + jsAny + "|[^`\\\\])*`|'(?:\\\\" + jsAny + "|[^'\\\\])*'|\"(?:\\\\" + jsAny + "|[^\"\\\\])*\"|[\\w$]+|" + jsNonWS)

// importHeaderEnd is packet.mjs importHeaderEnd: the last line of a
// top-level import/require, 0 for none.
func importHeaderEnd(file string, content *string) int {
	if content == nil || *content == "" || !importHeaderExt.MatchString(file) {
		return 0
	}
	tokens := []jsToken{}
	for _, t := range jsTokens(importHeaderTokenRE, *content) {
		if !strings.HasPrefix(t.value, "//") && !strings.HasPrefix(t.value, "/*") && !strings.HasPrefix(t.value, "<!--") {
			tokens = append(tokens, t)
		}
	}
	at := func(i int) (string, bool) {
		if i < 0 || i >= len(tokens) {
			return "", false
		}
		return tokens[i].value, true
	}
	is := func(i int, v string) bool { t, ok := at(i); return ok && t == v }
	depth := 0
	importing, importSource, requiring := false, false, false
	end := 0
	for i, tk := range tokens {
		token := tk.value
		if depth == 0 && token == "import" && !is(i+1, "(") && !is(i+1, ".") && !is(i-1, ".") {
			importing = true
			importSource = false
		}
		if depth == 0 && token == "require" && is(i+1, "(") && !is(i-1, ".") {
			requiring = true
		}
		switch token {
		case "{", "(", "[":
			depth++
		case "}", ")", "]":
			depth = max(0, depth-1)
		}
		quoted := strings.HasPrefix(token, "'") || strings.HasPrefix(token, `"`)
		if depth == 0 && ((importing && (quoted || (importSource && token == "}") || token == ";")) || (requiring && token == ")")) {
			end = tk.index + len(token)
			importSource = true
			if !((is(i+1, "with") || is(i+1, "assert")) && is(i+2, "{")) {
				importing = false
			}
			requiring = false
		}
	}
	if end == 0 {
		return 0
	}
	return strings.Count((*content)[:end], "\n") + 1
}

// packetContext is packet.mjs packetContext: the import header lines no
// window shows, before the context.
func packetContext(file string, content *string, hunks []Hunk, cfg map[string]any, mode string) Context {
	ctx := BuildContext(file, content, hunks, cfg, mode)
	end := importHeaderEnd(file, content)
	lines := SplitLines(content)
	missing := []string{}
	for n := 1; n <= end; n++ {
		shown := false
		for _, w := range ctx.Windows {
			shown = shown || (w.start <= n && n <= w.end)
		}
		if !shown {
			missing = append(missing, strconv.Itoa(n)+"| "+lines[n-1])
		}
	}
	if len(missing) == 0 {
		return ctx
	}
	ctx.LineCount += len(missing)
	ctx.Text = strings.Join(append(append([]string{fmt.Sprintf("### %s (import header context, lines 1-%d)", file, end)}, missing...), ctx.Text), "\n")
	return ctx
}

// FileDiff is one file's diff against the block base.
type FileDiff struct {
	File        string
	Kind        string // tracked or new
	DiffText    string
	Content     *string // nil: deleted
	Hunks       []Hunk
	PlusCount   int
	MinusCount  int
	Related     []string     // related file names, never their content (B537)
	Definitions []definition // called-symbol bodies (B537)
}

var secretLike = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(^|/)\.env[^/]*$`), regexp.MustCompile(`(?i)\.pem$`), regexp.MustCompile(`(?i)\.key$`),
	regexp.MustCompile(`(?i)\.p12$`), regexp.MustCompile(`(?i)(^|/)id_(rsa|ed25519|ecdsa)[^/]*$`),
	regexp.MustCompile(`(?i)(^|/)credentials[^/]*$`), regexp.MustCompile(`(?i)(^|/)\.code-forge(/|$)`),
}

// IsSecretLike is Node's isSecretLike.
func IsSecretLike(rel string) bool {
	for _, re := range secretLike {
		if re.MatchString(rel) {
			return true
		}
	}
	return false
}

func assertRowPath(value string) error {
	bad := func(m string) error { return &PacketError{"bad-path", m} }
	if value == "" || utf16Len(value) > 4096 {
		return bad("file must be a non-empty relative path")
	}
	if strings.ContainsAny(value, "\x00\\") {
		return bad("file must be a POSIX path")
	}
	if strings.HasPrefix(value, "/") {
		return bad("file must be relative to the repository root, not absolute")
	}
	segments := strings.Split(value, "/")
	for _, s := range segments {
		if s == "" || s == "." || s == ".." {
			return bad(`file must not contain "", "." or ".." segments`)
		}
	}
	if top := strings.ToLower(segments[0]); top == ".git" || top == ".code-forge" {
		return bad("file may not be under " + segments[0] + "/")
	}
	return nil
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r >= 0x10000 {
			n++
		}
	}
	return n
}

// AssertPacketPath is Node's assertPacketPath (V4).
func AssertPacketPath(repoRoot, file string) error {
	if err := assertRowPath(file); err != nil {
		return err
	}
	if strings.ContainsAny(file, "*?[]") || strings.HasPrefix(file, ":") {
		return &PacketError{"bad-path", `file may not contain glob characters or start with ":"`}
	}
	if st, err := os.Lstat(filepath.Join(repoRoot, file)); err == nil {
		if st.Mode()&os.ModeSymlink != 0 {
			return &PacketError{"bad-path", "the path is a symlink"}
		}
		if !st.Mode().IsRegular() {
			return &PacketError{"bad-path", "not a regular file"}
		}
	}
	return asPacketError(gitx.AssertInside(repoRoot, file))
}

func asPacketError(err error) error {
	var ge *gitx.Error
	if errors.As(err, &ge) && ge.Code == "bad-path" {
		return &PacketError{"bad-path", ge.Message}
	}
	return err
}

func reviewGitEnv() []string { return append(gitx.ChildEnv(), "GIT_LITERAL_PATHSPECS=1") }

// gitOK runs git and accepts the listed exit codes (0 always).
func gitOK(root string, env []string, ok []int, args ...string) (string, int, bool) {
	out, err := gitx.WithEnv(root, env, args...)
	if err == nil {
		return out, 0, true
	}
	var ce *gitx.CommandError
	if errors.As(err, &ce) {
		for _, c := range ok {
			if ce.Code == c {
				return out, c, true
			}
		}
		return out, ce.Code, false
	}
	return out, -1, false
}

// ReadFileDiff is Node's readFileDiff, with the related file names and the
// called-symbol definitions (B537); ErrUnsupported for a diff or file that
// is not UTF-8.
func ReadFileDiff(repoRoot, file, base string) (*FileDiff, error) {
	if err := AssertPacketPath(repoRoot, file); err != nil {
		return nil, err
	}
	if IsSecretLike(file) {
		return nil, &PacketError{"bad-path", "secret-like path"}
	}
	ref := base
	if ref == "" {
		ref = "HEAD"
	}
	if strings.HasPrefix(ref, "-") {
		return nil, &PacketError{"git", "the base must be a commit id"}
	}
	env := reviewGitEnv()
	listed, code, ok := gitOK(repoRoot, env, nil, "ls-files", "-z", "--", file)
	if !ok {
		return nil, &PacketError{"git", fmt.Sprintf("git ls-files failed (exit %d)", code)}
	}
	full := filepath.Join(repoRoot, file)
	_, statErr := os.Stat(full)
	onDisk := statErr == nil
	kind := "tracked"
	if listed == "" && onDisk {
		kind = "new"
		_, code, ok := gitOK(repoRoot, gitx.ChildEnv(), []int{1}, "check-ignore", "-q", "--", file)
		if !ok {
			return nil, &PacketError{"git", fmt.Sprintf("git check-ignore failed (exit %d)", code)}
		}
		if code == 0 {
			return nil, &PacketError{"bad-path", "the file is untracked and gitignored"}
		}
	}
	args := []string{"diff", "--no-color", "--no-ext-diff", ref, "--", file}
	okCodes := []int(nil)
	if kind == "new" {
		args = []string{"diff", "--no-color", "--no-ext-diff", "--no-index", "--", "/dev/null", file}
		okCodes = []int{1}
	}
	diffText, code, ok := gitOK(repoRoot, env, okCodes, args...)
	if !ok {
		return nil, &PacketError{"git", fmt.Sprintf("git diff failed for %s (exit %d)", file, code)}
	}
	if !utf8.ValidString(diffText) {
		return nil, unsupported("the diff is not UTF-8")
	}
	var content *string
	if onDisk {
		data, err := gitx.ReadRegularFileNoFollow(repoRoot, file)
		if err != nil {
			return nil, asPacketError(err)
		}
		if data != nil {
			if !utf8.Valid(data) {
				return nil, unsupported("the file is not UTF-8")
			}
			s := string(data)
			content = &s
		}
	}
	readFile := func(rel string) *string {
		if IsSecretLike(rel) {
			return nil
		}
		if rel == file {
			return content
		}
		if AssertPacketPath(repoRoot, rel) != nil {
			return nil
		}
		return readRepoTextOrNil(repoRoot, rel)
	}
	var tracked []string
	listFiles := func() []string {
		if tracked == nil {
			tracked = gitListFiles(repoRoot)
		}
		return append([]string{}, tracked...)
	}
	related := []string{}
	if trimJS(diffText) != "" {
		related = relatedFiles(file, relatedIO{read: readFile, list: listFiles})
	}
	hunks, plus, minus := ParseDiff(diffText)
	definitions := []definition{}
	if content != nil && trimJS(diffText) != "" {
		windows := []window{}
		for _, h := range hunks {
			windows = append(windows, window{h.NewStart, h.NewStart + h.NewLines - 1})
		}
		lines := SplitLines(content)
		header := strings.Join(lines[:min(len(lines), importHeaderEnd(file, content))], "\n")
		definitions = findDefinitions(file, strings.Split(diffText, "\n"), header, &defIO{read: readFile, list: listFiles, windows: windows})
	}
	return &FileDiff{File: file, Kind: kind, DiffText: diffText, Content: content, Hunks: hunks, PlusCount: plus, MinusCount: minus,
		Related: related, Definitions: definitions}, nil
}

// Packet is an assembled reviewer packet.
type Packet struct {
	Status      string // ok or split_required
	Text        string
	HunkHeaders []string
	ContextMode string
	ContextLine int
	DigestLines int
	TokensIn    int
	Budget      int
	OverBudget  bool
	// a recheck packet's cross-file evidence (buildRecheckPacket, B537):
	// HasExtra when the packet carries the extra_files fields.
	HasExtra     bool
	ExtraText    string
	ExtraFiles   []string
	ExtraOmitted int
	ExtraTrimmed []string
}

func diffSectionOf(d *FileDiff) string {
	parts := []string{"file: " + d.File, "hunks:"}
	for _, h := range d.Hunks {
		parts = append(parts, "- "+h.Header)
	}
	return strings.Join(append(parts, "", trimEnd(d.DiffText)), "\n")
}

// render is packet.mjs render: context, related, definitions, paired, moved,
// then the diff.
func render(lens string, digest []string, facts, context, diff, moved, paired, related, definitions string) string {
	digestText := "(none)"
	if len(digest) > 0 {
		digestText = strings.Join(digest, "\n")
	}
	factsText := "(none)"
	if trimJS(facts) != "" {
		factsText = trimEnd(facts)
	}
	if context == "" {
		context = "(none)"
	}
	parts := []string{"# code-forge review packet", "## lens", lens, "## project rules", digestText, "## facts", factsText, "## context", context}
	if related != "" {
		parts = append(parts, related)
	}
	if definitions != "" {
		parts = append(parts, trimEnd(definitions))
	}
	if paired != "" {
		parts = append(parts, paired)
	}
	if moved != "" {
		parts = append(parts, moved)
	}
	return strings.Join(append(parts, "## diff", diff, ""), "\n")
}

// AssemblePacket is Node's assemblePacket: paired is engine.mjs
// pairedContext's text and moved the file's `## moved code` / deleted /
// test-quality text (” for none).
func AssemblePacket(d *FileDiff, lens, rulesDigest, factsExcerpt string, cfg map[string]any, paired, moved string) (*Packet, error) {
	return assemblePacketMode(d, lens, rulesDigest, factsExcerpt, cfg, paired, moved, "auto")
}

// assemblePacketMode is assemblePacket with its contextMode (auto, or
// recheck for a fix-hunk packet); over the budget it falls to minimal.
func assemblePacketMode(d *FileDiff, lens, rulesDigest, factsExcerpt string, cfg map[string]any, paired, moved, mode string) (*Packet, error) {
	lensText, err := LoadLens(lens)
	if err != nil {
		return nil, err
	}
	key := "full_in"
	if lens == "quick" {
		key = "quick_in"
	}
	limit := BudgetFor(cfg, key)
	fullIn := BudgetFor(cfg, "full_in")
	headers := []string{}
	for _, h := range d.Hunks {
		headers = append(headers, h.Header)
	}
	diff := diffSectionOf(d)
	if bare := EstimateTokens(render(lensText, nil, "", "", diff, moved, "", "", "")); bare > limit {
		return &Packet{Status: "split_required", TokensIn: bare, Budget: limit}, nil
	}
	rd := rulesDigest
	digest := SplitLines(&rd)
	if len(digest) > DigestMaxLines {
		digest = digest[:DigestMaxLines]
	}
	ctx := packetContext(d.File, d.Content, d.Hunks, cfg, mode)
	pairedSection := boundedPaired(paired, fullIn)
	relatedSection := ""
	if (lens == "quick" || lens == "full") && len(d.Related) > 0 {
		relatedSection = "## related files (not shown)\n" + strings.Join(d.Related, "\n")
	}
	if EstimateTokens(render(lensText, digest, factsExcerpt, ctx.Text, diff, moved, "", "", "")) > limit {
		relatedSection = ""
		pairedSection = ""
		ctx = packetContext(d.File, d.Content, d.Hunks, cfg, "minimal")
	}
	definitions := []definition{}
	if lens != "judge" {
		for _, def := range d.Definitions {
			shown := false
			for _, w := range ctx.Windows {
				shown = shown || (w.start <= def.Start && def.End <= w.end)
			}
			if def.Path != d.File || !shown {
				definitions = append(definitions, def)
			}
		}
	}
	for len(definitions) > 0 && EstimateTokens(definitionsSection(definitions)) > fullIn/8 {
		definitions = definitions[:len(definitions)-1]
	}
	renderPacket := func() string {
		return render(lensText, digest, factsExcerpt, ctx.Text, diff, moved, pairedSection, relatedSection, definitionsSection(definitions))
	}
	text := renderPacket()
	if EstimateTokens(text) > limit && relatedSection != "" {
		relatedSection = ""
		text = renderPacket()
	}
	for EstimateTokens(text) > limit && len(definitions) > 0 {
		definitions = definitions[:len(definitions)-1]
		text = renderPacket()
	}
	if EstimateTokens(text) > limit && pairedSection != "" {
		pairedSection = ""
		text = renderPacket()
	}
	for EstimateTokens(text) > limit && len(digest) > 0 {
		digest = digest[:len(digest)-1]
		text = renderPacket()
	}
	tokens := EstimateTokens(text)
	return &Packet{Status: "ok", Text: text, HunkHeaders: headers, ContextMode: ctx.Mode, ContextLine: ctx.LineCount, DigestLines: len(digest),
		TokensIn: tokens, Budget: limit, OverBudget: tokens > limit}, nil
}

func jsonString(s string) (string, error) {
	data, err := canon.Marshal(s)
	return string(data), err
}
