// Package contextpack builds local, Node-compatible coder context packs.
package contextpack

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/sign"
	"github.com/ricardov03/code-forge/native/state"
	"gopkg.in/yaml.v3"
)

type Error struct{ Message string }

func (e *Error) Error() string     { return e.Message }
func refusal(message string) error { return &Error{message} }
func quote(s string) string        { b, _ := canon.Marshal(s); return string(b) }
func control(s string) bool {
	for _, r := range s {
		if r < 32 || r >= 127 && r <= 159 {
			return true
		}
	}
	return false
}
func object(v any) ledger.Object { o, _ := v.(ledger.Object); return o }
func str(v any) string           { s, _ := v.(string); return s }
func split(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// JavaScript trimEnd's whitespace set excludes NEL and includes BOM.
func trimEnd(s string) string {
	return strings.TrimRightFunc(s, func(r rune) bool {
		return r >= 9 && r <= 13 || r == 32 || r == 160 || r == 0x1680 || r >= 0x2000 && r <= 0x200a || r == 0x2028 || r == 0x2029 || r == 0x202f || r == 0x205f || r == 0x3000 || r == 0xfeff
	})
}

var commandKeys = []string{"build", "test", "lint", "format"}

type settings struct {
	rules    []string
	lessons  string
	commands map[string][]string
	max      int
}

func stringsList(v any) ([]string, bool) {
	a, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := []string{}
	for _, v := range a {
		s, ok := v.(string)
		if !ok || s == "" {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}
func settingsFor(cfg map[string]any, root string) (*settings, error) {
	raw, present := cfg["context"]
	if !present {
		return nil, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, refusal("context must be a mapping")
	}
	s := &settings{commands: map[string][]string{}, max: 300}
	if m["rules"] != nil {
		var ok bool
		s.rules, ok = stringsList(m["rules"])
		if !ok {
			return nil, refusal("context.rules must be a list of file paths")
		}
	}
	seen := map[string]bool{}
	for _, file := range s.rules {
		// POSIX normalize preserves a trailing slash.
		n := path.Clean(file)
		if strings.HasSuffix(file, "/") && n != "/" {
			n += "/"
		}
		if seen[n] {
			return nil, refusal("context.rules names a file twice")
		}
		seen[n] = true
	}
	if m["lessons"] != nil {
		var ok bool
		s.lessons, ok = m["lessons"].(string)
		if !ok || s.lessons == "" {
			return nil, refusal("context.lessons must be one file path")
		}
	}
	commands := map[string]any{}
	if m["commands"] != nil {
		var ok bool
		commands, ok = m["commands"].(map[string]any)
		if !ok {
			return nil, refusal("context.commands must be a mapping")
		}
	}
	unknown := []string{}
	for k := range commands {
		if k != "build" && k != "test" && k != "lint" && k != "format" {
			unknown = append(unknown, k)
		}
	}
	// Go maps discard YAML insertion order; Node names the first invalid key,
	// so read the key order back from the config file itself.
	if len(unknown) > 0 {
		return nil, refusal("context.commands." + firstUnknownCommand(root, unknown) + " is not one of build, test, lint, format")
	}
	for _, k := range commandKeys {
		if commands[k] == nil {
			continue
		}
		argv, ok := stringsList(commands[k])
		if !ok || len(argv) == 0 {
			return nil, refusal("context.commands." + k + " must be an argv list of non-empty strings")
		}
		for _, a := range argv {
			if control(a) {
				return nil, refusal("context.commands." + k + " contains a control character")
			}
		}
		s.commands[k] = argv
	}
	if m["require_coder_tests"] != nil {
		if _, ok := m["require_coder_tests"].(bool); !ok {
			return nil, refusal("context.require_coder_tests must be true or false")
		}
	}
	if m["max_lines"] != nil {
		n, ok := m["max_lines"].(float64)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || n < 20 {
			return nil, refusal("context.max_lines must be an integer >= 20")
		}
		if n < float64(math.MaxInt) {
			s.max = int(n)
		} else {
			s.max = math.MaxInt
		}
	}
	return s, nil
}

func errno(err error) string {
	for _, x := range []struct {
		code syscall.Errno
		name string
	}{
		{syscall.ENOENT, "ENOENT"}, {syscall.ENOTDIR, "ENOTDIR"}, {syscall.EISDIR, "EISDIR"}, {syscall.EACCES, "EACCES"}, {syscall.EPERM, "EPERM"}, {syscall.ELOOP, "ELOOP"}, {syscall.ENAMETOOLONG, "ENAMETOOLONG"}, {syscall.EROFS, "EROFS"}, {syscall.ENOSPC, "ENOSPC"}, {syscall.EEXIST, "EEXIST"}, {syscall.ENOTEMPTY, "ENOTEMPTY"},
	} {
		if errors.Is(err, x.code) {
			return x.name
		}
	}
	return "error"
}

// Node's fs error message is surfaced verbatim by the ledger-append refusal.
func nodeError(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		code := errno(err)
		description := map[string]string{"ENOENT": "no such file or directory", "ENOTDIR": "not a directory", "EISDIR": "illegal operation on a directory", "EACCES": "permission denied", "EPERM": "operation not permitted", "ELOOP": "too many symbolic links encountered", "ENAMETOOLONG": "name too long", "EROFS": "read-only file system", "ENOSPC": "no space left on device", "EEXIST": "file already exists", "ENOTEMPTY": "directory not empty"}[code]
		if description != "" {
			message := code + ": " + description + ", " + pe.Op
			if pe.Op != "read" {
				message += " '" + pe.Path + "'"
			}
			return message
		}
	}
	return err.Error()
}
func inside(root, file string) bool {
	rel, err := filepath.Rel(root, file)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Follow broken links too: their target still determines the boundary.
func walkLinks(file string) string {
	parts := strings.Split(strings.TrimPrefix(file, "/"), "/")
	cur, hops := "/", 0
	for len(parts) > 0 {
		next := filepath.Join(cur, parts[0])
		parts = parts[1:]
		st, err := os.Lstat(next)
		if err != nil {
			return filepath.Join(append([]string{next}, parts...)...)
		}
		if st.Mode()&os.ModeSymlink == 0 {
			cur = next
			continue
		}
		hops++
		if hops > 40 {
			return filepath.Join(append([]string{next}, parts...)...)
		}
		target, err := os.Readlink(next)
		if err != nil {
			return next
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(cur, target)
		}
		target = filepath.Clean(target)
		parts = append(strings.Split(strings.TrimPrefix(target, "/"), "/"), parts...)
		cur = "/"
	}
	return cur
}
func readLines(root, rel, key string) ([]string, error) {
	shown := key + ": " + quote(rel)
	if control(rel) {
		return nil, refusal(shown + " contains a control character")
	}
	if filepath.IsAbs(rel) {
		return nil, refusal(shown + " is outside the repository (give a path relative to the repository root)")
	}
	for _, segment := range strings.FieldsFunc(rel, func(r rune) bool { return r == '/' || r == '\\' }) {
		if segment == ".." {
			return nil, refusal(shown + " is outside the repository (give a path relative to the repository root)")
		}
	}
	file := walkLinks(filepath.Join(root, rel))
	if !inside(root, file) {
		return nil, refusal(shown + " leads outside the repository")
	}
	st, err := os.Lstat(file)
	if os.IsNotExist(err) {
		return nil, refusal(shown + " does not exist")
	}
	if err != nil {
		return nil, refusal(shown + " cannot be read (" + errno(err) + ")")
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return nil, refusal(shown + " does not exist")
	}
	if !st.Mode().IsRegular() {
		return nil, refusal(shown + " is not a file")
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, refusal(shown + " cannot be read (" + errno(err) + ")")
	}
	// readFileSync(file, 'utf8') replaces each invalid sequence with U+FFFD.
	return split(trimEnd(DecodeUTF8(b))), nil
}

var bareToken = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func argvText(argv []string) string {
	tokens := []string{}
	for _, token := range argv {
		if !bareToken.MatchString(token) {
			token = "'" + strings.ReplaceAll(token, "'", "'\\''") + "'"
		}
		tokens = append(tokens, token)
	}
	return strings.Join(tokens, " ")
}

// forbiddenSection is B364's section, byte-equal to FORBIDDEN_SECTION in src/context/pack.mjs:
// one line per coder-enforced rule of gatecheck.CoderRules, then the safe ways.
func forbiddenSection() []string {
	out := []string{"## forbidden", "the block close scans your transcript for these rules; one hit blocks the block:"}
	for _, r := range gatecheck.CoderRules {
		out = append(out, "- "+r.ID+": "+r.Description)
	}
	return append(out,
		"safe ways:",
		"- red run on base code: `git show <base>:<file> > <file>`, or a temp `git worktree add` at the base sha; never git stash",
		"- clean-up: delete only files you made inside your own worktree, by exact path; never rm -rf",
		"- run facts (ledger, review state): ask the orchestrator; never read ~/.code-forge/runs or .code-forge/runs",
	)
}

// Keep the built-in rule byte-equal to src/proof/rule.mjs.
const proofRule = "## proof rule (code-forge)\n" +
	"- Red→green: each new or changed test must FAIL on the old code first (red), then PASS on the new code (green). A red caused by an import, compile or crash error proves nothing; the red must be an assertion failure. `code-forge proof red-green` does this with the block's base code.\n" +
	"- No hand mutations. Do not break the code on purpose to test the tests. Red→green is the proof.\n" +
	"- One exact test per acceptance clause, plus one negative or boundary test where the clause can fail. Assertions say which element and how many, never only \"it exists\".\n" +
	"- Proof scales with risk: a light-tier file needs red→green per new test; a high-tier file must have it at the gate. Do not add tests, mutants, layers, options or abstractions that the acceptance does not ask for.\n" +
	"- A weak assertion is fixed by a stronger assertion, not by more tests or mutants.\n" +
	"- Facts, not guesses: every cause, pass or fail you report names its proof (the error text, the log path, or the command and its exit code). No proof means \"not known yet\": get the proof now; never name load, flakiness or old tests as a cause without it. No work stops because of a guess."

type section struct {
	heading string
	body    []string
	dropped int
}
type Result struct {
	Text, Path, SHA256 string
	Tokens, Lines, Cut int
}

// Assemble performs all source reads and validation, before any pack is touched.
func Assemble(root, run, id string, entry ledger.Object, cfg map[string]any, node, bin, version string) (Result, error) {
	r := Result{}
	s, err := settingsFor(cfg, root)
	if err != nil {
		return r, err
	}
	sections := []section{}
	if s != nil {
		for _, rel := range s.rules {
			lines, err := readLines(root, rel, "context.rules")
			if err != nil {
				return r, err
			}
			sections = append(sections, section{heading: "## rules: " + rel, body: lines})
		}
		if s.lessons != "" {
			lines, err := readLines(root, s.lessons, "context.lessons")
			if err != nil {
				return r, err
			}
			sections = append(sections, section{heading: "## lessons: " + s.lessons, body: lines})
		}
	}
	brief := object(entry.Get("brief"))
	var briefLines []string
	briefPath, hasBrief := brief.Get("path").(string)
	if hasBrief {
		briefLines, err = readLines(root, briefPath, "block.brief")
		if err != nil {
			return r, err
		}
	}
	values := []struct {
		label string
		value any
	}{{"level", entry.Get("level")}}
	owned, _ := entry.Get("owned_files").([]any)
	tests, _ := entry.Get("test_files").([]any)
	for _, v := range owned {
		values = append(values, struct {
			label string
			value any
		}{"owned file", v})
	}
	for _, v := range tests {
		values = append(values, struct {
			label string
			value any
		}{"test file", v})
	}
	tree := object(entry.Get("worktree"))
	_, hasTree := tree.Get("path").(string)
	if hasTree {
		values = append(values, struct {
			label string
			value any
		}{"worktree path", tree.Get("path")}, struct {
			label string
			value any
		}{"worktree branch", tree.Get("branch")})
	}
	values = append(values, struct {
		label string
		value any
	}{"code-forge node", node}, struct {
		label string
		value any
	}{"code-forge bin", bin}, struct {
		label string
		value any
	}{"code-forge version", version})
	for _, v := range values {
		if v.value == nil {
			continue
		}
		// Node checks only string values; others print as JS String() does.
		text, ok := v.value.(string)
		if !ok {
			continue
		}
		if control(text) {
			return r, refusal("block " + id + ": " + v.label + " " + quote(text) + " contains a control character")
		}
	}
	level := "(unknown)"
	if entry.Get("level") != nil {
		level = gatecheck.JSString(entry.Get("level"), true)
	}
	header := []string{"# code-forge context pack", "## block", "run: " + run, "block: " + id, "level: " + level, "owned files:"}
	for _, f := range owned {
		header = append(header, "- "+gatecheck.JSString(f, true))
	}
	if len(tests) > 0 {
		header = append(header, "test files:")
		for _, f := range tests {
			header = append(header, "- "+gatecheck.JSString(f, true))
		}
	}
	if hasTree {
		branch := "undefined"
		for _, f := range tree {
			if f.Key == "branch" {
				branch = gatecheck.JSString(f.Value, true)
			}
		}
		header = append(header, "worktree: "+str(tree.Get("path")), "branch: "+branch)
	}
	header = append(header, "## code-forge", "run code-forge verbs with exactly: "+argvText([]string{node, bin}), "version: "+version, "do not use another code-forge from PATH")
	if s != nil {
		header = append(header, "## commands (never run by code-forge)")
		if len(s.commands) == 0 {
			header = append(header, "(none)")
		}
		for _, k := range commandKeys {
			if argv, ok := s.commands[k]; ok {
				header = append(header, k+": "+argvText(argv))
			}
		}
	}
	header = append(header, split(proofRule)...)
	header = append(header, forbiddenSection()...)
	task := []string{}
	if hasBrief {
		task = append(task, "## brief: "+briefPath)
		task = append(task, split(strings.Join(briefLines, "\n"))...)
	}
	if entry.Get("acceptance") != nil {
		clauses, ok := entry.Get("acceptance").([]any)
		if !ok {
			return r, refusal("block " + id + ": acceptance must be a list of clauses")
		}
		if len(clauses) > 0 {
			task = append(task, "## acceptance")
		}
		for _, c := range clauses {
			clause, ok := c.(ledger.Object)
			if !ok {
				return r, refusal("block " + id + ": each acceptance clause must be a mapping")
			}
			_, present := clauseField(clause, "clause")
			task = append(task, "- "+gatecheck.JSString(clause.Get("clause"), present))
			if clause.Get("kind") == "human_check" {
				task = append(task, "  kind: human_check")
			} else {
				tests, ok := clause.Get("tests").([]any)
				if !ok {
					return r, refusal("block " + id + ": an acceptance clause without kind human_check needs a tests list")
				}
				names := []string{}
				for _, t := range tests {
					// Array.prototype.join prints null as an empty string.
					if t == nil {
						names = append(names, "")
					} else {
						names = append(names, gatecheck.JSString(t, true))
					}
				}
				task = append(task, "  tests: "+strings.Join(names, ", "))
			}
		}
	}
	header = append(header, split(strings.Join(task, "\n"))...)
	max := 300
	if s != nil {
		max = s.max
	}
	total := func() int {
		n := len(header)
		for _, s := range sections {
			n += 1 + len(s.body)
			if s.dropped > 0 {
				n++
			}
		}
		return n
	}
	for i := len(sections) - 1; i >= 0; i-- {
		excess := total() - max
		if excess <= 0 {
			break
		}
		s := &sections[i]
		if len(s.body) < 2 {
			continue
		}
		drop := min(len(s.body), excess+1)
		s.body = s.body[:len(s.body)-drop]
		s.dropped = drop
	}
	for _, s := range sections {
		header = append(header, s.heading)
		header = append(header, s.body...)
		if s.dropped > 0 {
			header = append(header, fmt.Sprintf("[cut: %d lines dropped]", s.dropped))
		}
		r.Cut += s.dropped
	}
	r.Text = strings.Join(header, "\n") + "\n"
	r.Lines = len(header)
	r.Tokens = (len(r.Text) + 3) / 4
	r.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(r.Text)))
	return r, nil
}

func loadKey(home, run string) ([]byte, error) {
	file := filepath.Join(home, ".code-forge", "runs", run+".key")
	st, err := os.Stat(file)
	if os.IsNotExist(err) {
		return nil, &state.Error{Code: "no-key", Message: "run " + run + " has no signer key"}
	}
	if err != nil {
		return nil, &state.Error{Code: "bad-key", Message: "run " + run + " signer key cannot be read (" + nodeError(err) + ")"}
	}
	if st.Mode().Perm()&0077 != 0 {
		return nil, &state.Error{Code: "bad-key-mode", Message: fmt.Sprintf("run %s signer key is accessible to other users (mode %o); expected 600", run, st.Mode().Perm())}
	}
	key, err := os.ReadFile(file)
	if err != nil {
		return nil, &state.Error{Code: "bad-key", Message: "run " + run + " signer key cannot be read (" + nodeError(err) + ")"}
	}
	if len(key) != 32 {
		return nil, &state.Error{Code: "bad-key", Message: "run " + run + " signer key is not 32 bytes"}
	}
	return key, nil
}

// LastPack checks verified history before publication; a ledger that cannot be
// read is a refusal before anything is written.
func LastPack(home, run, slug, id string) (map[string]any, error) {
	rows, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge", "ledger"), slug)
	if err != nil {
		return nil, refusal("the ledger cannot be read (" + nodeError(err) + ")")
	}
	matched := []map[string]any{}
	for _, v := range rows {
		if row, ok := v.(map[string]any); ok && row["event"] == "context.pack" && row["run"] == run && row["block"] == id {
			matched = append(matched, row)
		}
	}
	if len(matched) == 0 {
		return nil, nil
	}
	key, err := loadKey(home, run)
	if err != nil {
		return nil, err
	}
	var last map[string]any
	for _, row := range matched {
		// A row the signer cannot check is not a verified pack row.
		checked, err := sign.VerifyRow(row, key)
		if err == nil && checked.OK {
			last = row
		}
	}
	return last, nil
}

// Publish mirrors Node's temp -> signed ledger append -> rename ordering. Node
// takes only the ledger append lock here, not a run lock, and so do we.
func Publish(home, run, id string, r Result, last map[string]any) (string, error) {
	file := r.Path
	tmp := fmt.Sprintf("%s.%d.tmp", file, os.Getpid())
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		return "", refusal("cannot write " + file + " (" + errno(err) + ")")
	}
	if err := os.WriteFile(tmp, []byte(r.Text), 0600); err != nil {
		_ = os.Remove(tmp)
		return "", refusal("cannot write " + file + " (" + errno(err) + ")")
	}
	defer os.Remove(tmp)
	needs := last == nil || last["sha256"] != r.SHA256 || last["path"] != file
	if needs {
		row := ledger.Object{{Key: "event", Value: "context.pack"}, {Key: "block", Value: id}, {Key: "path", Value: file}, {Key: "sha256", Value: r.SHA256}, {Key: "tokens", Value: r.Tokens}, {Key: "lines", Value: r.Lines}, {Key: "cut", Value: r.Cut}}
		if _, err := state.WriteSigned(home, run, row); err != nil {
			var se *state.Error
			if !errors.As(err, &se) {
				return "context: the context.pack ledger row could not be written (" + nodeError(err) + "); nothing was written\n", nil
			}
			return "context: the context.pack ledger row could not be written (" + se.Message + "); nothing was written\n", nil
		}
	}
	// os.Rename preflights directory destinations and substitutes EEXIST on
	// macOS. libuv exposes rename(2)'s EISDIR, which is part of this CLI's text.
	if err := syscall.Rename(tmp, file); err != nil {
		after := "nothing was written"
		if needs {
			after = "its context.pack ledger row was already written"
		}
		return "context: cannot write " + file + " (" + errno(err) + "); " + after + "\n", nil
	}
	return "", nil
}

// NodeError is Node's fs error message ("EACCES: permission denied, mkdir '<p>'").
func NodeError(err error) string { return nodeError(err) }

// Errno is the fs error code Node reports for err ("ENOTDIR"), or "error".
func Errno(err error) string { return errno(err) }

func clauseField(o ledger.Object, key string) (any, bool) {
	for _, f := range o {
		if f.Key == key {
			return f.Value, true
		}
	}
	return nil, false
}

// firstUnknownCommand is the first of unknown in the config file's own key
// order under context.commands (Node's Object.keys order).
func firstUnknownCommand(root string, unknown []string) string {
	sort.Strings(unknown)
	want := map[string]bool{}
	for _, k := range unknown {
		want[k] = true
	}
	raw, err := os.ReadFile(filepath.Join(config.ProjectRootFor(root), config.Filename))
	if err != nil {
		return unknown[0]
	}
	var doc yaml.Node
	if yaml.Unmarshal(raw, &doc) != nil || len(doc.Content) == 0 {
		return unknown[0]
	}
	child := func(n *yaml.Node, key string) *yaml.Node {
		if n == nil || n.Kind != yaml.MappingNode {
			return nil
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == key {
				return n.Content[i+1]
			}
		}
		return nil
	}
	commands := child(child(doc.Content[0], "context"), "commands")
	if commands == nil || commands.Kind != yaml.MappingNode {
		return unknown[0]
	}
	for i := 0; i+1 < len(commands.Content); i += 2 {
		if want[commands.Content[i].Value] {
			return commands.Content[i].Value
		}
	}
	return unknown[0]
}

// DecodeUTF8 is the WHATWG UTF-8 decoder Node's readFileSync(file, 'utf8')
// uses: each maximal invalid subpart becomes one U+FFFD.
func DecodeUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var out strings.Builder
	for i := 0; i < len(b); {
		r, n := utf8.DecodeRune(b[i:])
		if r != utf8.RuneError || n > 1 {
			out.WriteRune(r)
			i += n
			continue
		}
		lead := b[i]
		need, lo, hi := 0, byte(0x80), byte(0xBF)
		switch {
		case lead >= 0xC2 && lead <= 0xDF:
			need = 1
		case lead == 0xE0:
			need, lo = 2, 0xA0
		case lead >= 0xE1 && lead <= 0xEC, lead == 0xEE, lead == 0xEF:
			need = 2
		case lead == 0xED:
			need, hi = 2, 0x9F
		case lead == 0xF0:
			need, lo = 3, 0x90
		case lead >= 0xF1 && lead <= 0xF3:
			need = 3
		case lead == 0xF4:
			need, hi = 3, 0x8F
		}
		j := i + 1
		for k := 0; k < need && j < len(b); k++ {
			if b[j] < lo || b[j] > hi {
				break
			}
			lo, hi = 0x80, 0xBF
			j++
		}
		out.WriteRune(utf8.RuneError)
		i = j
	}
	return out.String()
}
