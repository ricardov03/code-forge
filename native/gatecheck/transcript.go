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

// Rule is one forbidden rule the block close's transcript scan enforces for a coder (B364).
type Rule struct{ ID, Description string }

// CoderRules is the coder rule table the context pack lists: mergeForbidden() in
// src/util/forbidden.mjs (FORBIDDEN, then CODER_ONLY_FORBIDDEN), same ids, descriptions and
// order. test/compat/parity-context-pack.test.mjs keeps the Go pack's lines equal to Node's.
var CoderRules = []Rule{
	{"gh-pr-ready", "Mark a PR ready for review"},
	{"gh-pr-merge", "Merge a PR"},
	{"gh-pr-close", "Close a PR"},
	{"gh-pr-base-retarget", "Retarget a PR base branch (--base / -B / --base=, anywhere after `gh pr edit`)"},
	{"git-push-force", "Force-push (rewrites remote history): --force/-f (also inside a cluster like -uf), --force-with-lease[=…], --force-if-includes, --mirror, or a +refspec"},
	{"git-reset-hard", "Discard local changes (--hard anywhere after `git reset`)"},
	{"git-checkout-discard", "Discard working-tree changes (`git checkout [<ref>] -- <path>`, or a forced checkout)"},
	{"git-restore", "Discard working-tree changes to a path (the modern replacement for checkout --)"},
	{"git-clean", "Delete untracked files"},
	{"git-stash", "Stash working-tree state"},
	{"git-branch-force-delete", "Force-delete a branch (short form -D, also inside a cluster)"},
	{"git-branch-force-delete-long", "Force-delete a branch (--delete/-d together with --force/-f, any order)"},
	{"git-rm", "Delete a tracked file via git — includes deleting a test file"},
	{"rm-rf", "rm with ANY recursive or force flag (-r, -R, -f, any cluster containing one, --recursive, --force) — a coder never legitimately needs either; the package cleans .code-forge/ itself"},
	{"production-marker", "Any argv token CONTAINING a configured production.markers/production.names value (env flag, DB name, prod host, deploy tool) — this entry is a placeholder; mergeForbidden() adds the real, project-configured tokens"},
	{"code-forge-runs-access", "Any read or write of ~/.code-forge/runs/ (the run record pin and the per-run HMAC key) — a coder never needs it; reading the key is a named rule-break, not a boundary (§8.6)"},
	{"code-forge-worker-from-coder", "Start a worker or a run, or reload its config (forge worker / code-forge worker / run start / run reload) from a coder — the orchestrator only"},
	{"code-forge-reviews-write", "Any write under .code-forge/reviews/ or to .code-forge/queue/*.done — only the worker writes review results"},
	{"code-forge-block-waive-from-coder", "Waive a review finding (forge block waive) — human-only, typed by the human in their own terminal (§4.9, B80)"},
	{"code-forge-block-reopen-from-coder", "Reopen the review of a stopped or lost file (forge block reopen) — human-only, typed by the human in their own terminal (B72, B80)"},
	{"code-forge-autopilot-from-coder", "Any autopilot command (forge autopilot start / status / stop / …) — the owner grants, reads and ends autopilot, never a coder"},
	{"code-forge-no-require-reviews", "Close a block without the per-file review check (--no-require-reviews) — human-only"},
	{"code-forge-remove-worktree", "Remove a block's worktree at close (block close --remove-worktree, B62a) — the orchestrator only"},
	{"code-forge-block-accept-hit-from-coder", "Accept a false transcript hit — human-only, confirmed in the human terminal (B127)"},
	{"code-forge-block-unwaive-from-coder", "Undo agent waivers — human-only, confirmed in the human terminal (B124b)"},
	{"code-forge-block-ack-waivers-from-coder", "Acknowledge agent waivers — human-only, confirmed in the human terminal (B124b)"},
	{"code-forge-block-human-pass-from-coder", "Pass a human device check — human-only, confirmed in the human terminal (B137)"},
	{"code-forge-add-worker-from-coder", "Add a review worker to a run — the orchestrator only (B107)"},
	{"code-forge-block-amend-acceptance-from-coder", "Correct a block acceptance list — human-only, confirmed in the human terminal (B143)"},
}
