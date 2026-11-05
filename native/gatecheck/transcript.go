package gatecheck

import (
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var transcriptTokens = regexp.MustCompile(">>?|[^\\s\\x0b'\"`;|&(),{}\\[\\]>]+")

// Transcript handles the plain-text grep. Coder tool JSON, protected paths and
// code-forge launcher spellings stay with Node's full interpreter-aware scanner.
// Returning unsupported happens before any close warning is published.
func Transcript(text string) ([]Blocker, bool) {
	for _, needle := range []string{"thread.started", ".code-forge", "forge", "--no-require-reviews", "--remove-worktree"} {
		if strings.Contains(text, needle) {
			return nil, false
		}
	}
	for _, r := range text {
		if r > 127 {
			return nil, false
		}
	}
	hits := []Blocker{}
	order := []string{"gh-pr-ready", "gh-pr-merge", "gh-pr-close", "gh-pr-base-retarget", "git-push-force", "git-reset-hard", "git-checkout-discard", "git-restore", "git-clean", "git-stash", "git-branch-force-delete", "git-branch-force-delete-long", "git-rm", "rm-rf", "production-marker"}
	for lineNo, line := range strings.Split(text, "\n") {
		tokens := transcriptTokens.FindAllString(line, -1)
		ids := map[string]bool{}
		if strings.Contains(line, "--env=production") {
			ids["production-marker"] = true
		}
		for i, token := range tokens {
			name := path.Base(token)
			if strings.HasSuffix(strings.ToLower(name), ".exe") {
				name = name[:len(name)-4]
			}
			if !slices.Contains([]string{"git", "gh", "rm"}, name) {
				continue
			}
			if token != name {
				return nil, false
			}
			end := min(len(tokens), i+64)
			words := tokens[i:end]
			if len(words) < 2 {
				continue
			}
			if strings.HasPrefix(words[1], "-") && name != "rm" {
				return nil, false
			}
			flags := func(exact, prefix []string, short string) bool {
				for _, w := range words[1:] {
					if slices.Contains(exact, w) {
						return true
					}
					for _, p := range prefix {
						if strings.HasPrefix(w, p) {
							return true
						}
					}
					if strings.HasPrefix(w, "-") && !strings.HasPrefix(w, "--") && strings.ContainsAny(w[1:], short) {
						return true
					}
				}
				return false
			}
			switch name {
			case "gh":
				if words[1] != "pr" || len(words) < 3 {
					continue
				}
				if strings.HasPrefix(words[2], "-") {
					return nil, false
				}
				if slices.Contains([]string{"ready", "merge", "close"}, words[2]) {
					ids["gh-pr-"+words[2]] = true
				}
				if words[2] == "edit" && flags([]string{"--base", "-B"}, []string{"--base=", "-B"}, "") {
					ids["gh-pr-base-retarget"] = true
				}
			case "git":
				switch words[1] {
				case "push":
					if flags([]string{"--force", "-f", "--force-with-lease", "--force-if-includes", "--mirror"}, []string{"--force-with-lease=", "--force-if-includes=", "+"}, "f") {
						ids["git-push-force"] = true
					}
				case "reset":
					if flags([]string{"--hard"}, nil, "") {
						ids["git-reset-hard"] = true
					}
				case "checkout":
					if flags([]string{"--", "--force"}, nil, "f") {
						ids["git-checkout-discard"] = true
					}
				case "restore", "clean", "stash", "rm":
					ids["git-"+words[1]] = true
				case "branch":
					if flags([]string{"-D"}, nil, "D") {
						ids["git-branch-force-delete"] = true
					}
					if flags([]string{"--delete"}, nil, "d") && flags([]string{"--force"}, nil, "f") {
						ids["git-branch-force-delete-long"] = true
					}
				}
			case "rm":
				if flags([]string{"--recursive", "--force"}, nil, "rRf") {
					ids["rm-rf"] = true
				}
			}
		}
		for _, id := range order {
			if ids[id] {
				hits = append(hits, Blocker{Code: "rule_break", Detail: "transcript line " + strconv.Itoa(lineNo+1) + ": " + id})
			}
		}
	}
	return hits, true
}
