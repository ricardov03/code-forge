// Package landmerge resolves the merge conflicts land may settle by itself
// (B795): every conflicted file must be append-only. CHANGELOG.md bullets,
// rows of a markdown table in a configured file and files land re-generates
// are such files; anything else is a real conflict, and then nothing is
// written.
package landmerge

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ricardov03/code-forge/native/gitx"
)

// Kinds of a conflicted file.
const (
	KindChangelog = "changelog"
	KindTable     = "table"
	KindGenerated = "generated"
	KindUnion     = "union" // merge=union in .gitattributes (B431)
	KindCode      = "code"
)

// DefaultTables is the table list when land.auto_resolve.tables is unset.
var DefaultTables = []string{"docs/native-engine.md"}

// Generated is a file land re-generates after a merge: the merge keeps main's
// copy, then Argv runs in the repository root and the file is staged again.
type Generated struct {
	Path string
	Argv []string
}

// Settings is land.auto_resolve.
type Settings struct {
	Enabled   bool
	Tables    []string
	Generated []Generated
}

// Defaults is land.auto_resolve when the key is unset: on, with the default
// table list and no generated file.
func Defaults() Settings {
	return Settings{Enabled: true, Tables: append([]string{}, DefaultTables...)}
}

// Legacy is the B431 rule alone: add-only CHANGELOG.md and merge=union files.
func Legacy() Settings { return Settings{} }

// FromConfig reads land.auto_resolve from a parsed config map. `false` turns
// it off; `true`, a missing key or an object without `enabled` keeps it on.
func FromConfig(cfg map[string]any) Settings {
	land, _ := cfg["land"].(map[string]any)
	raw, present := land["auto_resolve"]
	if !present || raw == nil || raw == true {
		return Defaults()
	}
	if raw == false {
		return Legacy()
	}
	object, ok := raw.(map[string]any)
	if !ok {
		return Defaults()
	}
	settings := Defaults()
	if object["enabled"] == false {
		return Legacy()
	}
	if list, ok := object["tables"].([]any); ok {
		settings.Tables = nil
		for _, value := range list {
			if s, ok := value.(string); ok && s != "" {
				settings.Tables = append(settings.Tables, path.Clean(s))
			}
		}
	}
	if list, ok := object["generated"].([]any); ok {
		for _, value := range list {
			item, _ := value.(map[string]any)
			p, _ := item["path"].(string)
			argv := []string{}
			if raw, ok := item["argv"].([]any); ok {
				for _, arg := range raw {
					if s, ok := arg.(string); ok {
						argv = append(argv, s)
					}
				}
			}
			if p != "" {
				settings.Generated = append(settings.Generated, Generated{Path: path.Clean(p), Argv: argv})
			}
		}
	}
	return settings
}

// Kind classifies a conflicted file under these settings.
func (s Settings) Kind(file string, union bool) string {
	clean := path.Clean(file)
	if s.Enabled {
		for _, g := range s.Generated {
			if g.Path == clean {
				return KindGenerated
			}
		}
		for _, t := range s.Tables {
			if t == clean {
				return KindTable
			}
		}
	}
	if path.Base(clean) == "CHANGELOG.md" {
		return KindChangelog
	}
	if union {
		return KindUnion
	}
	return KindCode
}

// File is one conflicted file and its kind.
type File struct {
	Path string `json:"file"`
	Kind string `json:"kind"`
}

// Conflict lists the files of a merge land did not resolve; every unmerged
// file is named, with the kind each one has.
type Conflict struct{ Files []File }

func (c *Conflict) Error() string {
	if len(c.Files) == 0 {
		return "the merge stopped without a merge conflict"
	}
	return "merge conflicts in: " + Describe(c.Files)
}

// Paths lists the file names.
func Paths(files []File) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Path
	}
	return out
}

// Describe is "a (code), b (changelog)".
func Describe(files []File) string {
	parts := make([]string, len(files))
	for i, f := range files {
		parts[i] = fmt.Sprintf("%q (%s)", f.Path, f.Kind)
		if !strings.ContainsFunc(f.Path, func(r rune) bool { return r < 0x20 || r == 0x7f || r == '"' || r == ' ' }) {
			parts[i] = f.Path + " (" + f.Kind + ")"
		}
	}
	return strings.Join(parts, ", ")
}

func unmerged(tree string, env []string) ([]string, error) {
	out, err := gitx.WithEnv("", env, "-C", tree, "diff", "--name-only", "--diff-filter=U", "-z", "--")
	if err != nil {
		return nil, err
	}
	files := []string{}
	for _, file := range strings.Split(out, "\x00") {
		if file != "" {
			files = append(files, file)
		}
	}
	sort.Strings(files)
	return files, nil
}

func unionAttr(tree string, env []string, file string) bool {
	out, err := gitx.WithEnv("", env, "-C", tree, "check-attr", "-z", "merge", "--", file)
	if err != nil {
		return false
	}
	fields := strings.Split(out, "\x00")
	return len(fields) >= 3 && fields[2] == "union"
}

func stages(tree string, env []string, file string) (map[string]string, error) {
	out, err := gitx.WithEnv("", env, "-C", tree, "ls-files", "-u", "-z", "--", file)
	if err != nil {
		return nil, err
	}
	found := map[string]string{}
	for _, entry := range strings.Split(out, "\x00") {
		meta, name, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if ok && name == file && len(fields) == 3 {
			found[fields[2]] = fields[1]
		}
	}
	return found, nil
}

func blob(tree string, env []string, id string) (string, error) {
	if id == "" {
		return "", nil
	}
	return gitx.WithEnv("", env, "-C", tree, "cat-file", "blob", id)
}

// Classify lists the unmerged files of tree with their kinds.
func Classify(tree string, env []string, settings Settings) ([]File, error) {
	names, err := unmerged(tree, env)
	if err != nil {
		return nil, err
	}
	files := make([]File, len(names))
	for i, name := range names {
		files[i] = File{name, settings.Kind(name, unionAttr(tree, env, name))}
	}
	return files, nil
}

// Merge merges one file's three texts by its kind; ok is false when the kind
// cannot settle these changes. A generated file keeps ours.
func Merge(kind, base, ours, theirs string) (merged string, ok bool, err error) {
	if body, clean, err := threeWay(base, ours, theirs, false); err != nil || clean {
		if clean && kind == KindChangelog {
			body = foldHeadings(body, ours)
		}
		return body, clean, err
	}
	switch kind {
	case KindChangelog:
		return Changelog(base, ours, theirs)
	case KindTable:
		merged, ok = Table(base, ours, theirs)
		return merged, ok, nil
	case KindGenerated:
		return ours, true, nil
	case KindUnion:
		return threeWay(base, ours, theirs, true)
	}
	return "", false, nil
}

// Resolve merges and stages every unmerged file in tree. It returns the
// resolved files, or a *Conflict naming every unmerged file (with its kind)
// when any one of them is not append-only; then nothing is written.
func Resolve(tree string, env []string, settings Settings) ([]File, error) {
	files, err := Classify(tree, env, settings)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, &Conflict{}
	}
	merged := map[string]string{}
	refuse := func() ([]File, error) { return nil, &Conflict{files} }
	for _, file := range files {
		if file.Kind == KindCode {
			return refuse()
		}
		ids, err := stages(tree, env, file.Path)
		if err != nil {
			return nil, err
		}
		if ids["2"] == "" || ids["3"] == "" {
			return refuse()
		}
		var text [3]string
		for i, stage := range []string{"1", "2", "3"} {
			if text[i], err = blob(tree, env, ids[stage]); err != nil {
				return nil, err
			}
		}
		body, ok, err := Merge(file.Kind, text[0], text[1], text[2])
		if err != nil {
			return nil, err
		}
		if !ok {
			return refuse()
		}
		merged[file.Path] = body
	}
	for _, file := range files {
		info, err := os.Lstat(filepath.Join(tree, file.Path))
		if err != nil || !info.Mode().IsRegular() {
			return refuse()
		}
	}
	for _, file := range files {
		info, _ := os.Lstat(filepath.Join(tree, file.Path))
		if err := os.WriteFile(filepath.Join(tree, file.Path), []byte(merged[file.Path]), info.Mode().Perm()); err != nil {
			return nil, err
		}
		if _, err := gitx.WithEnv("", env, "-C", tree, "add", "--", file.Path); err != nil {
			return nil, err
		}
	}
	for _, g := range settings.Generated {
		hit := false
		for _, file := range files {
			hit = hit || file.Kind == KindGenerated && path.Clean(file.Path) == g.Path
		}
		if !hit || len(g.Argv) == 0 {
			continue
		}
		cmd := exec.Command(g.Argv[0], g.Argv[1:]...)
		cmd.Dir, cmd.Env = tree, env
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("re-generating %s failed: %v: %s", g.Path, err, strings.TrimSpace(string(out)))
		}
		if _, err := gitx.WithEnv("", env, "-C", tree, "add", "--", g.Path); err != nil {
			return nil, err
		}
	}
	return files, nil
}

// threeWay runs git merge-file on the three texts; ok is false when a plain
// merge (union false) has conflicts.
func threeWay(base, ours, theirs string, union bool) (merged string, ok bool, err error) {
	dir, err := os.MkdirTemp("", "forge-landmerge-")
	if err != nil {
		return "", false, err
	}
	defer os.RemoveAll(dir)
	names := []string{filepath.Join(dir, "ours"), filepath.Join(dir, "base"), filepath.Join(dir, "theirs")}
	for i, body := range []string{ours, base, theirs} {
		if err := os.WriteFile(names[i], []byte(body), 0600); err != nil {
			return "", false, err
		}
	}
	args := []string{"merge-file", "-p"}
	if union {
		args = append(args, "--union")
	}
	cmd := exec.Command("git", append(args, names...)...)
	cmd.Env = gitx.ChildEnv()
	out, err := cmd.Output()
	if exit, isExit := err.(*exec.ExitError); isExit && exit.ExitCode() > 0 && exit.ExitCode() < 128 {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(out), true, nil
}

// lines splits text into lines that keep their newline.
func lines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.SplitAfter(strings.TrimSuffix(text, "\n"), "\n")
}

// addOnly reports whether side is base with lines only inserted.
func addOnly(base, side string) bool {
	b, s := lines(base), lines(side)
	j := 0
	for _, line := range s {
		if j < len(b) && strings.TrimSuffix(line, "\n") == strings.TrimSuffix(b[j], "\n") {
			j++
		}
	}
	return j == len(b)
}

// Changelog merges a CHANGELOG.md to which both sides only added lines: the
// lines both added at one place keep ours (main) first, then theirs (the
// block). A `### Kind` heading the merge would repeat inside one release
// section is folded into the first one, so each kind keeps one heading, and
// an entry both sides added is kept once. ok is false when a side removed or
// changed a line.
func Changelog(base, ours, theirs string) (merged string, ok bool, err error) {
	if !addOnly(base, ours) || !addOnly(base, theirs) {
		return "", false, nil
	}
	// git's diff sees an unterminated last line and the same line with a
	// newline as different lines; terminate all three texts, merge, and
	// drop the newline again when neither side had one.
	if base == "" || strings.HasSuffix(base, "\n") {
		merged, ok, err = threeWay(base, ours, theirs, true)
	} else {
		terminate := func(text string) string {
			if text == "" || strings.HasSuffix(text, "\n") {
				return text
			}
			return text + "\n"
		}
		merged, ok, err = threeWay(base+"\n", terminate(ours), terminate(theirs), true)
		if ok && !strings.HasSuffix(ours, "\n") && !strings.HasSuffix(theirs, "\n") {
			merged = strings.TrimSuffix(merged, "\n")
		}
	}
	if !ok || err != nil {
		return merged, ok, err
	}
	return foldHeadings(merged, ours), true, nil
}

func trimmed(line string) string { return strings.TrimRight(line, "\r\n") }

// changelogSections splits text at `## ` lines; key is the `## ` line ("" for
// the preamble).
func changelogSections(text []string) (keys []string, bodies [][]string) {
	keys, bodies = []string{""}, [][]string{nil}
	for _, line := range text {
		if strings.HasPrefix(line, "## ") {
			keys, bodies = append(keys, trimmed(line)), append(bodies, []string{line})
			continue
		}
		bodies[len(bodies)-1] = append(bodies[len(bodies)-1], line)
	}
	return keys, bodies
}

func headingKey(line string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(trimmed(line), "### ")))
}

// foldHeadings removes, inside each `## ` section, every `### Kind` heading
// the merge repeats beyond ours' count: the entries of the extra group (a
// group with no line of ours is chosen first) move to the end of the first
// remaining group of that kind, after ours' entries. A bullet the merge
// repeats beyond ours' count in one section is dropped.
func foldHeadings(merged, ours string) string {
	terminated := strings.HasSuffix(merged, "\n")
	text := lines(merged)
	if len(text) > 0 {
		text[len(text)-1] = trimmed(text[len(text)-1]) + "\n"
	}
	oursKeys, oursBodies := changelogSections(lines(ours))
	headingCount := map[string]map[string]int{}
	bulletCount := map[string]map[string]int{}
	oursLines := map[string]map[string]bool{}
	for i, key := range oursKeys {
		if headingCount[key] == nil {
			headingCount[key], bulletCount[key], oursLines[key] = map[string]int{}, map[string]int{}, map[string]bool{}
		}
		for _, line := range oursBodies[i] {
			oursLines[key][trimmed(line)] = true
			if strings.HasPrefix(line, "### ") {
				headingCount[key][headingKey(line)]++
			} else if strings.HasPrefix(line, "- ") {
				bulletCount[key][trimmed(line)]++
			}
		}
	}
	keys, bodies := changelogSections(text)
	out := []string{}
	for i, key := range keys {
		for _, line := range foldSection(bodies[i], headingCount[key], bulletCount[key], oursLines[key]) {
			// A union can drop the blank line above a heading; keep one.
			if strings.HasPrefix(line, "#") && len(out) > 0 && strings.HasPrefix(out[len(out)-1], "- ") {
				out = append(out, "\n")
			}
			out = append(out, line)
		}
	}
	result := strings.Join(out, "")
	if !terminated {
		result = strings.TrimSuffix(result, "\n")
	}
	return result
}

type clGroup struct {
	heading string
	lines   []string
	folded  bool
}

func foldSection(body []string, allowed, bullets map[string]int, oursLines map[string]bool) []string {
	groups := []*clGroup{{}}
	for _, line := range body {
		if strings.HasPrefix(line, "### ") {
			groups = append(groups, &clGroup{heading: line})
			continue
		}
		groups[len(groups)-1].lines = append(groups[len(groups)-1].lines, line)
	}
	byKey, order := map[string][]*clGroup{}, []string{}
	for _, g := range groups[1:] {
		key := headingKey(g.heading)
		if byKey[key] == nil {
			order = append(order, key)
		}
		byKey[key] = append(byKey[key], g)
	}
	for _, key := range order {
		list := byKey[key]
		excess := len(list) - max(allowed[key], 1)
		if excess <= 0 {
			continue
		}
		// Groups with no line of ours fold first, then the last ones.
		for _, g := range list {
			if excess > 0 && !clHasOurs(g, oursLines) {
				g.folded, excess = true, excess-1
			}
		}
		for i := len(list) - 1; i >= 0 && excess > 0; i-- {
			if !list[i].folded {
				list[i].folded, excess = true, excess-1
			}
		}
		var target *clGroup
		for _, g := range list {
			if !g.folded {
				target = g
				break
			}
		}
		for _, g := range list {
			if g.folded {
				target.lines = appendEntries(target.lines, g.lines)
			}
		}
	}
	count := map[string]int{}
	out := []string{}
	for _, g := range groups {
		if g.folded {
			continue
		}
		if g.heading != "" {
			out = append(out, g.heading)
		}
		for _, line := range g.lines {
			if strings.HasPrefix(line, "- ") {
				count[trimmed(line)]++
				if count[trimmed(line)] > max(bullets[trimmed(line)], 1) {
					continue
				}
			}
			out = append(out, line)
		}
	}
	return out
}

func clHasOurs(g *clGroup, oursLines map[string]bool) bool {
	for _, line := range g.lines {
		if strings.TrimSpace(line) != "" && oursLines[trimmed(line)] {
			return true
		}
	}
	return false
}

// appendEntries puts the non-blank lines of extra after the last non-blank
// line of target, keeping target's trailing blank lines after them.
func appendEntries(target, extra []string) []string {
	start, end := 0, len(extra)
	for start < end && strings.TrimSpace(extra[start]) == "" {
		start++
	}
	for end > start && strings.TrimSpace(extra[end-1]) == "" {
		end--
	}
	if start == end {
		return target
	}
	last := len(target)
	for last > 0 && strings.TrimSpace(target[last-1]) == "" {
		last--
	}
	out := append([]string{}, target[:last]...)
	out = append(out, extra[start:end]...)
	return append(out, target[last:]...)
}
