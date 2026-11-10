package gates

import (
	"strings"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
)

// Stale is a worktree block whose base branch moved past its recorded base.
type Stale struct {
	Branch, Recorded, Tip string
}

// StaleBase (B363) compares an open worktree block's recorded base (worktree
// base_sha, else base_sha) with the tip of its base branch in the main repo.
// Only a strict move forward is stale: an equal base, a missing branch, or a
// tip that does not contain the recorded base gates as before.
func StaleBase(workspace string, block ledger.Object) *Stale {
	tree, _ := block.Get("worktree").(ledger.Object)
	if tree == nil || block.Get("status") != "open" || workspace == "" {
		return nil
	}
	branch, _ := block.Get("base_branch").(string)
	recorded, _ := tree.Get("base_sha").(string)
	if recorded == "" {
		recorded, _ = block.Get("base_sha").(string)
	}
	if branch == "" || !baseSHA.MatchString(recorded) {
		return nil
	}
	tip, err := gitx.Run(workspace, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch+"^{commit}")
	tip = strings.TrimSpace(tip)
	if err != nil || !baseSHA.MatchString(tip) || tip == recorded {
		return nil
	}
	if _, err := gitx.Run(workspace, "merge-base", "--is-ancestor", recorded, tip); err != nil {
		return nil
	}
	return &Stale{Branch: branch, Recorded: recorded, Tip: tip}
}
