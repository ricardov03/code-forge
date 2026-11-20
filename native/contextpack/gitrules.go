package contextpack

import (
	"regexp"
	"strings"

	"github.com/ricardov03/code-forge/native/gitx"
)

// githubRemote matches a GitHub origin in its ssh, scp or https form.
var githubRemote = regexp.MustCompile(`^(?:git@github\.com:|ssh://git@github\.com/|https://(?:[^@/]+@)?github\.com/)([^/\s]+/[^/\s]+?)(?:\.git)?/?$`)

// pushCommand is the one push form a coder may use: for a GitHub origin the
// HTTPS push through the gh credential helper (no ssh key prompt), else a
// plain push to origin. "" when the repository has no origin.
func pushCommand(root, branch string) string {
	out, err := gitx.Run(root, "remote", "get-url", "origin")
	url := strings.TrimSpace(out)
	if err != nil || url == "" {
		return ""
	}
	if m := githubRemote.FindStringSubmatch(url); m != nil {
		return "git -c credential.helper= -c credential.helper='!gh auth git-credential' push https://github.com/" + m[1] + ".git " + branch + ":" + branch
	}
	return "git push origin " + branch + ":" + branch
}

// gitRulesSection is B797's generated git safety section: every pack lists
// the moves the ref guard refuses, the other forbidden git actions and the
// push form, so no brief has to write them by hand.
func gitRulesSection(root, branch, base string) []string {
	if branch == "" {
		branch = "<your-branch>"
	}
	if base == "" {
		base = "the main branch"
	}
	out := []string{
		"## git rules (code-forge)",
		"- never move " + base + ": no git branch -f, git update-ref, git reset or git fetch into it; read the remote with git fetch origin, then use FETCH_HEAD (git log FETCH_HEAD). Only code-forge block land moves " + base + "; the ref guard hook refuses these moves in this worktree",
		"- never move or delete another block's branch; commit only on " + branch,
		"- never force push (--force, --force-with-lease, a +refspec), never rm -rf (delete only your own files by exact path), never git stash, never git reset --hard",
	}
	if push := pushCommand(root, branch); push != "" {
		return append(out, "- push only with: "+push)
	}
	return append(out, "- this repository has no origin remote: do not push")
}

// rebaseRuleLine (B942) closes the git rules: rebase on the newest base
// branch just before the CI push, so CI tests the block on today's base.
func rebaseRuleLine(base, id, run string) string {
	if base == "" {
		base = "the main branch"
	}
	return "- rebase on the newest " + base + " just before the CI push: code-forge block rebase " + id + " --run " + run + " (block ready lists stale_base while " + base + " moved past your base)"
}

// RebaseRuleLine is the spawned coder's form of the B942 rule.
const RebaseRuleLine = "Rebase on the newest base branch just before the CI push (code-forge block ready lists stale_base with the rebase command while it moved past your base)."
