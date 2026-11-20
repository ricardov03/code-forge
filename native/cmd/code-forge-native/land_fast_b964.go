package main

import (
	"regexp"
	"strings"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/releasecheck"
)

// B964: the land fast checks of our land scripts (land2.sh, landbatch.sh), in
// Go, before any CI push, rebase or gate. Each looks only at what the block
// adds against its base branch (base...head):
//   - conflict markers added by the block (B571);
//   - test files under a testdata/ or fixtures/ folder that git ignores, so
//     they were never committed (B566);
//   - a Markdown table row whose first cell repeats another row's in the same
//     file (or is it plus more commands), new in the block (B518);
//   - a CHANGELOG.md the block changed whose "## [Unreleased]" gets a fault
//     of releasecheck.ChangelogFaults that the base did not have (B911).
// It returns one problem per line, or nil.

var landFastLineNo = regexp.MustCompile(`line [0-9]+`)

var landFastMarker = regexp.MustCompile(`^\+(<<<<<<<|>>>>>>>)( |$)|^\+=======$`)

func landFastChecks(tree, base, head string) []string {
	problems := []string{}
	diff, err := gitx.Run(tree, "diff", "--no-color", "--no-ext-diff", base+"..."+head)
	if err != nil {
		return []string{"cannot diff " + base + "..." + head + ": " + strings.SplitN(err.Error(), "\n", 2)[0]}
	}
	file := ""
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+++ ") {
			file = strings.TrimPrefix(strings.TrimPrefix(line, "+++ "), "b/")
			continue
		}
		if landFastMarker.MatchString(line) {
			problems = append(problems, "conflict marker added in "+file+": "+landFastCut(line[1:]))
		}
	}
	if ignored, err := gitx.Run(tree, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "--no-empty-directory"); err == nil {
		for _, path := range strings.Split(strings.TrimSpace(ignored), "\n") {
			if p := "/" + path; strings.Contains(p, "/testdata/") || strings.Contains(p, "/fixtures/") {
				problems = append(problems, "test file ignored by git, never committed: "+path+" (git add -f it)")
			}
		}
	}
	changed, err := gitx.Run(tree, "diff", "--name-only", "--no-renames", "--diff-filter=AM", base+"..."+head)
	if err != nil {
		return append(problems, "cannot list the changed files: "+strings.SplitN(err.Error(), "\n", 2)[0])
	}
	for _, path := range strings.Split(strings.TrimSpace(changed), "\n") {
		if !strings.HasSuffix(path, ".md") {
			continue
		}
		now, _ := gitx.Run(tree, "show", head+":"+path)
		before, _ := gitx.Run(tree, "show", landFastMergeBase(tree, base, head)+":"+path)
		old := map[string]bool{}
		for _, pair := range landFastDupRows(before) {
			old[pair] = true
		}
		for _, pair := range landFastDupRows(now) {
			if !old[pair] {
				problems = append(problems, "duplicate docs row in "+path+": "+pair)
			}
		}
		if path == "CHANGELOG.md" && strings.Contains(now, "## [Unreleased]") {
			// Only a fault the block brings: a project whose [Unreleased]
			// already had it (or never uses ### headings) is not refused.
			had := map[string]bool{}
			if strings.Contains(before, "## [Unreleased]") {
				for _, fault := range releasecheck.ChangelogFaults(before) {
					had[landFastLineNo.ReplaceAllString(fault, "line N")] = true
				}
			}
			for _, fault := range releasecheck.ChangelogFaults(now) {
				if !had[landFastLineNo.ReplaceAllString(fault, "line N")] {
					problems = append(problems, fault)
				}
			}
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return problems
}

// landFastMergeBase is where head left base, or base when git cannot tell.
func landFastMergeBase(tree, base, head string) string {
	if sha, err := gitx.Run(tree, "merge-base", base, head); err == nil && strings.TrimSpace(sha) != "" {
		return strings.TrimSpace(sha)
	}
	return base
}

// landFastDupRows lists the pairs of table rows ("| `cmd` | ...") of a
// Markdown text whose first cells are equal, or one is the other plus more
// commands ("`a`" and "`a`, `b`").
func landFastDupRows(text string) []string {
	keys := []string{}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "| `") {
			if cells := strings.SplitN(line, "|", 3); len(cells) == 3 {
				keys = append(keys, strings.TrimSpace(cells[1]))
			}
		}
	}
	pairs := []string{}
	for i, a := range keys {
		for _, b := range keys[i+1:] {
			if a == b || strings.HasPrefix(b, a+",") || strings.HasPrefix(a, b+",") {
				pairs = append(pairs, landFastCut(a)+" <-> "+landFastCut(b))
			}
		}
	}
	return pairs
}

func landFastCut(s string) string {
	if len(s) > 90 {
		return s[:90]
	}
	return s
}
