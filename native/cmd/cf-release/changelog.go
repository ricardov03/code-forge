package main

// `npm run changelog -- <type> "<text>"` and `npm run changelog -- list` (B23), and the CHANGELOG
// parser the release tool shares. Port of scripts/changelog.mjs (B570).

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var clTypes = []string{"Added", "Changed", "Deprecated", "Removed", "Fixed", "Security"}

func clTypesLower(sep string) string {
	parts := make([]string, len(clTypes))
	for i, t := range clTypes {
		parts[i] = strings.ToLower(t)
	}
	return strings.Join(parts, sep)
}

var clUsage = "usage: npm run changelog -- <" + clTypesLower("|") + "> \"<text>\"\n       npm run changelog -- list"

type clError struct {
	msg  string
	code int
}

func (e *clError) Error() string { return e.msg }

var (
	clBulletRe  = regexp.MustCompile(`^[-*]\s+\S`)
	clBulletPfx = regexp.MustCompile(`^[-*]\s+`)
	clHeadingRe = regexp.MustCompile(`^###\s+(.+?)\s*$`)
	clLinkDefRe = regexp.MustCompile(`^\[[^\]]+\]:\s`)
	clContRe    = regexp.MustCompile(`^ {2}\S`)
)

type clSection struct{ start, end int }

// findSection finds `## [<name>]`: its heading line and the line where it ends (the next `## `
// heading, the first link definition, or the end of the file).
func findSection(lines []string, name string) *clSection {
	want := "[" + strings.ToLower(name) + "]"
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "## ") && strings.ToLower(jsSplitWS(jsTrim(l[3:]))[0]) == want {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") || clLinkDefRe.MatchString(lines[i]) {
			end = i
			break
		}
	}
	return &clSection{start, end}
}

func findUnreleased(lines []string) *clSection { return findSection(lines, "Unreleased") }

type clBullet struct {
	text        string
	first, last int
}

type clSub struct {
	typ          string
	heading, end int
	bullets      []*clBullet
}

// parseSubsections lists the subsections of lines [from, to) and their bullets.
func parseSubsections(lines []string, from, to int) []*clSub {
	subs := []*clSub{{typ: "", heading: from - 1, end: to}}
	var bullet *clBullet
	for i := from; i < to; i++ {
		line := lines[i]
		if h := clHeadingRe.FindStringSubmatch(line); h != nil {
			subs[len(subs)-1].end = i
			subs = append(subs, &clSub{typ: h[1], heading: i, end: to})
			bullet = nil
		} else if clBulletRe.MatchString(line) {
			bullet = &clBullet{text: jsTrim(clBulletPfx.ReplaceAllString(line, "")), first: i, last: i}
			last := subs[len(subs)-1]
			last.bullets = append(last.bullets, bullet)
		} else if bullet != nil && jsTrim(line) != "" && line != "" && jsSpace([]rune(line)[0]) {
			bullet.text = bullet.text + " " + jsTrim(line)
			bullet.last = i
		} else {
			bullet = nil
		}
	}
	return subs
}

type clGroup struct {
	typ     string
	entries []string
}

func unreleasedEntries(text string) []clGroup {
	lines := strings.Split(text, "\n")
	sec := findUnreleased(lines)
	if sec == nil {
		return nil
	}
	var out []clGroup
	for _, s := range parseSubsections(lines, sec.start+1, sec.end) {
		if len(s.bullets) == 0 {
			continue
		}
		g := clGroup{typ: s.typ}
		for _, b := range s.bullets {
			g.entries = append(g.entries, b.text)
		}
		out = append(out, g)
	}
	return out
}

// wrapColumn is the longest bullet or continuation line, clamped to 72..120; 100 with none.
func wrapColumn(lines []string) int {
	widest, any := 0, false
	for _, l := range lines {
		if clBulletRe.MatchString(l) || clContRe.MatchString(l) {
			any = true
			if n := jsLen(l); n > widest {
				widest = n
			}
		}
	}
	if !any {
		return 100
	}
	return min(120, max(72, widest))
}

func wrapBullet(text string, width int) []string {
	var out []string
	line := ""
	for _, word := range jsFields(text) {
		switch {
		case line == "":
			line = "- " + word
		case jsLen(line)+1+jsLen(word) <= width:
			line = line + " " + word
		default:
			out = append(out, line)
			line = "  " + word
		}
	}
	return append(out, line)
}

func splice(lines []string, at, del int, items ...string) []string {
	out := make([]string, 0, len(lines)-del+len(items))
	out = append(out, lines[:at]...)
	out = append(out, items...)
	return append(out, lines[at+del:]...)
}

// addEntry adds one bullet under `## [Unreleased]` → `### <Type>`.
func addEntry(text, typ, entry string) (string, error) {
	heading := ""
	for _, t := range clTypes {
		if strings.ToLower(t) == strings.ToLower(typ) {
			heading = t
		}
	}
	if heading == "" {
		return "", &clError{"unknown type \"" + typ + "\": use one of " + clTypesLower(", "), 2}
	}
	clean := strings.Join(jsFields(entry), " ")
	if clean == "" {
		return "", &clError{"the entry text is empty", 2}
	}
	lines := strings.Split(text, "\n")
	bullet := wrapBullet(clean, wrapColumn(lines))
	sec := findUnreleased(lines)
	if sec == nil {
		first := -1
		for i, l := range lines {
			if strings.HasPrefix(l, "## ") {
				first = i
				break
			}
		}
		block := append([]string{"## [Unreleased]", "", "### " + heading, ""}, bullet...)
		block = append(block, "")
		if first >= 0 {
			lines = splice(lines, first, 0, block...)
		} else {
			for len(lines) > 0 && lines[len(lines)-1] == "" {
				lines = lines[:len(lines)-1]
			}
			lines = append(lines, "")
			lines = append(lines, block...)
		}
		return strings.Join(lines, "\n"), nil
	}
	subs := parseSubsections(lines, sec.start+1, sec.end)
	lastNonBlank := func(from, to int) int {
		for i := to - 1; i > from; i-- {
			if jsTrim(lines[i]) != "" {
				return i
			}
		}
		return from
	}
	for _, s := range subs {
		if strings.ToLower(s.typ) != strings.ToLower(heading) {
			continue
		}
		for _, b := range s.bullets {
			if b.text == clean {
				return "", &clError{"\"" + clean + "\" is already under " + heading + " in [Unreleased]", 1}
			}
		}
		last := lastNonBlank(s.heading, s.end)
		if last == s.heading {
			lines = splice(lines, last+1, 0, append([]string{""}, bullet...)...)
		} else {
			lines = splice(lines, last+1, 0, bullet...)
		}
		return strings.Join(lines, "\n"), nil
	}
	rank := func(t string) int {
		for i, k := range clTypes {
			if strings.ToLower(k) == strings.ToLower(t) {
				return i
			}
		}
		return -1
	}
	for _, s := range subs {
		if s.typ != "" && rank(s.typ) > rank(heading) {
			block := append([]string{"### " + heading, ""}, bullet...)
			lines = splice(lines, s.heading, 0, append(block, "")...)
			return strings.Join(lines, "\n"), nil
		}
	}
	block := append([]string{"", "### " + heading, ""}, bullet...)
	lines = splice(lines, lastNonBlank(sec.start, sec.end)+1, 0, block...)
	return strings.Join(lines, "\n"), nil
}

func formatList(text string) string {
	groups := unreleasedEntries(text)
	if len(groups) == 0 {
		return "[Unreleased] has no entries\n"
	}
	var b strings.Builder
	for _, g := range groups {
		name := g.typ
		if name == "" {
			name = "(no subsection)"
		}
		b.WriteString(name + " (" + strconv.Itoa(len(g.entries)) + ")\n")
		for _, e := range g.entries {
			b.WriteString("  - " + e + "\n")
		}
	}
	return b.String()
}

func changelogMain(a *app, argv []string, root string) int {
	file := filepath.Join(root, "CHANGELOG.md")
	err := func() error {
		if len(argv) == 1 && argv[0] == "list" {
			data, err := os.ReadFile(file)
			if err != nil {
				return &clError{"CHANGELOG.md not found", 1}
			}
			a.out(formatList(string(data)))
			return nil
		}
		if len(argv) != 2 {
			return &clError{clUsage, 2}
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return &clError{"CHANGELOG.md not found", 1}
		}
		after, err := addEntry(string(data), argv[0], argv[1])
		if err != nil {
			return err
		}
		if err := os.WriteFile(file, []byte(after), 0o644); err != nil {
			return &clError{err.Error(), 1}
		}
		heading := ""
		for _, t := range clTypes {
			if strings.ToLower(t) == strings.ToLower(argv[0]) {
				heading = t
			}
		}
		a.out("added to [Unreleased] → " + heading + "\n")
		return nil
	}()
	if err == nil {
		return 0
	}
	e := err.(*clError)
	extra := ""
	if e.code == 2 && e.msg != clUsage {
		extra = clUsage + "\n"
	}
	a.err("REFUSED: " + e.msg + "\n" + extra)
	return e.code
}
