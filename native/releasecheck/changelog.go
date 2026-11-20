package releasecheck

import (
	"regexp"
	"strconv"
	"strings"
)

// The CHANGELOG rules of plans/tools/forge/p4/changelog-check.py (B516, B785) and of the
// release's own refusals (cf-release releaseChangelog): [Unreleased] exists and has entries, no
// entry sits before its first ### heading, no ### kind appears twice, and no line added since the
// last release tag sits under a released version.

var (
	clUnreleasedRe = regexp.MustCompile(`(?i)^## +\[unreleased\]`)
	clLinkDefRe    = regexp.MustCompile(`^\[[^\]]+\]:\s`)
	clHeadingRe    = regexp.MustCompile(`^###\s+(.+?)\s*$`)
	clBulletRe     = regexp.MustCompile(`^[-*]\s+\S`)
	clReleasedRe   = regexp.MustCompile(`^## \[\d`)
	clHunkRe       = regexp.MustCompile(`(?m)^@@ -\S+ \+(\d+)(?:,(\d+))? @@`)
)

const clKinds = "Added, Changed, Deprecated, Removed, Fixed, Security"

// unreleased returns the [Unreleased] section's heading line and end line (exclusive); start -1
// when there is none.
func unreleased(lines []string) (int, int) {
	start := -1
	for i, l := range lines {
		if clUnreleasedRe.MatchString(l) {
			start = i
			break
		}
	}
	if start < 0 {
		return -1, -1
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") || clLinkDefRe.MatchString(lines[i]) {
			end = i
			break
		}
	}
	return start, end
}

// ChangelogFaults lists every fault of ## [Unreleased]; line numbers are 1-based.
func ChangelogFaults(text string) []string {
	lines := strings.Split(text, "\n")
	start, end := unreleased(lines)
	if start < 0 {
		return []string{`CHANGELOG.md has no "## [Unreleased]" heading`}
	}
	var out []string
	seen := map[string]int{}
	heading := ""
	entries := 0
	for i := start + 1; i < end; i++ {
		if h := clHeadingRe.FindStringSubmatch(lines[i]); h != nil {
			heading = h[1]
			kind := strings.ToLower(heading)
			if first, dup := seen[kind]; dup {
				out = append(out, "CHANGELOG.md line "+strconv.Itoa(i+1)+": \"### "+heading+"\" repeats in \"## [Unreleased]\" (first at line "+
					strconv.Itoa(first+1)+"): merge them into one ### "+heading+" heading")
			} else {
				seen[kind] = i
			}
			continue
		}
		if clBulletRe.MatchString(lines[i]) {
			entries++
			if heading == "" {
				out = append(out, "CHANGELOG.md line "+strconv.Itoa(i+1)+": an entry of \"## [Unreleased]\" comes before its first ### heading: put each entry under ### "+clKinds)
			}
		}
	}
	if entries == 0 {
		out = append(out, `"## [Unreleased]" has no entries: there is nothing to release`)
	}
	return out
}

// Bump is the version bump [Unreleased] asks for: minor when ### Added has an entry, else patch.
func Bump(text string) (string, string) {
	lines := strings.Split(text, "\n")
	start, end := unreleased(lines)
	counts := map[string]int{}
	var order []string
	heading := ""
	if start >= 0 {
		for i := start + 1; i < end; i++ {
			if h := clHeadingRe.FindStringSubmatch(lines[i]); h != nil {
				heading = h[1]
				continue
			}
			if heading != "" && clBulletRe.MatchString(lines[i]) {
				if counts[heading] == 0 {
					order = append(order, heading)
				}
				counts[heading]++
			}
		}
	}
	var parts []string
	added := 0
	for _, h := range order {
		parts = append(parts, h+" "+strconv.Itoa(counts[h]))
		if strings.EqualFold(h, "Added") {
			added += counts[h]
		}
	}
	found := "no entries"
	if len(parts) > 0 {
		found = strings.Join(parts, ", ")
	}
	if added > 0 {
		return "minor", "### Added has entries (" + found + ")"
	}
	return "patch", "no ### Added entries (" + found + ")"
}

// changelogOutside lists the CHANGELOG lines added since the last v* release tag that sit under a
// released version instead of [Unreleased] (B516). No tag: nothing to compare.
func changelogOutside(git func(...string) Exec, text string) []string {
	tag := strings.TrimSpace(git("describe", "--tags", "--abbrev=0", "--match", "v[0-9]*", "HEAD").Stdout)
	if tag == "" {
		return nil
	}
	old := git("show", tag+":CHANGELOG.md")
	if old.Code != 0 {
		return nil
	}
	oldSet := map[string]bool{}
	for _, l := range strings.Split(old.Stdout, "\n") {
		oldSet[l] = true
	}
	diff := git("diff", "-U0", tag, "--", "CHANGELOG.md")
	if diff.Code != 0 {
		return nil
	}
	lines := strings.Split(text, "\n")
	firstRel := len(lines) + 1
	for i, l := range lines {
		if clReleasedRe.MatchString(l) {
			firstRel = i + 1
			break
		}
	}
	var out []string
	for _, m := range clHunkRe.FindAllStringSubmatch(diff.Stdout, -1) {
		from, _ := strconv.Atoi(m[1])
		n := 1
		if m[2] != "" {
			n, _ = strconv.Atoi(m[2])
		}
		for ln := from; ln < from+n; ln++ {
			if ln >= firstRel && ln-1 < len(lines) && strings.TrimSpace(lines[ln-1]) != "" && !oldSet[lines[ln-1]] {
				l := lines[ln-1]
				if r := []rune(l); len(r) > 80 {
					l = string(r[:80]) + "…"
				}
				out = append(out, "CHANGELOG.md line "+strconv.Itoa(ln)+" was added under a released version, not [Unreleased] (since "+tag+"): "+l)
			}
		}
	}
	return out
}

// ChangelogRepeats lists the ### kinds (lower case) that appear more than once in
// ## [Unreleased], each once, in the order of their second heading (B890: the shape check a
// rebase runs before its result can be pushed).
func ChangelogRepeats(text string) []string {
	lines := strings.Split(text, "\n")
	start, end := unreleased(lines)
	if start < 0 {
		return nil
	}
	seen, listed := map[string]bool{}, map[string]bool{}
	var out []string
	for i := start + 1; i < end; i++ {
		if h := clHeadingRe.FindStringSubmatch(lines[i]); h != nil {
			kind := strings.ToLower(h[1])
			if seen[kind] && !listed[kind] {
				listed[kind] = true
				out = append(out, kind)
			}
			seen[kind] = true
		}
	}
	return out
}
