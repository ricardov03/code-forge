package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
)

// closeLandedTree measures the original block commit in a private clone.
// A merge is measured against its first parent, including conflict resolutions.
// Later commits (including edits to shared files) never change the bytes checked
// by the ordinary close gates. No branch or worktree of the user is modified.
func closeLandedTree(repo, workspace, id, sha string, entry ledger.Object) (tree, parent string, cleanup func(), err error) {
	cleanup = func() {}
	refuse := func(why string) (string, string, func(), error) {
		return "", "", cleanup, fmt.Errorf("landed-close: %s", why)
	}
	if !blockCommit.MatchString(sha) {
		return refuse("--landed needs a full commit SHA")
	}
	subject, e := gitx.Run(repo, "show", "-s", "--format=%s", sha)
	if e != nil || !(strings.HasPrefix(subject, id+":") || strings.HasPrefix(subject, id+" ")) {
		return refuse("commit subject must start with the block id followed by ':' or a space")
	}
	parents, e := gitx.Run(repo, "show", "-s", "--format=%P", sha)
	ps := strings.Fields(parents)
	if e != nil || len(ps) == 0 {
		return refuse("commit must have at least one parent")
	}
	parent = ps[0]
	base := blockString(entry.Get("base_sha"))
	if base == "" {
		return refuse("block has no base SHA")
	}
	if _, e = gitx.Run(repo, "merge-base", "--is-ancestor", base, parent); e != nil {
		return refuse("block base is not an ancestor of the commit's parent")
	}
	// base_branch records where coding started, not an integration authority:
	// a linked-worktree run commonly records its own lane branch there.
	_, e = gitx.Run(repo, "merge-base", "--is-ancestor", sha, "refs/heads/main")
	landed := e == nil
	// A combine caller may close a lane run in another worktree of this repo.
	// Its HEAD is accepted only after checking the shared git directory identity.
	if !landed {
		cwd, ce := os.Getwd()
		caller, ce2 := gitx.RepoTop(cwd)
		blockTree := blockString(blockObject(entry.Get("worktree")).Get("path"))
		if ce == nil && ce2 == nil && statusReal(caller) != statusReal(repo) && (blockTree == "" || statusReal(caller) != statusReal(blockTree)) {
			common, a := gitx.Run(repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
			other, b := gitx.Run(caller, "rev-parse", "--path-format=absolute", "--git-common-dir")
			if a == nil && b == nil && statusReal(strings.TrimSpace(common)) == statusReal(strings.TrimSpace(other)) {
				// A lane tip alone is not integration evidence. Find the
				// first-parent merge that introduced sha from a side branch.
				merges, err := gitx.Run(caller, "rev-list", "--first-parent", "--merges", "HEAD", "^"+sha)
				if err == nil {
					for _, merge := range strings.Fields(merges) {
						parents, err := gitx.Run(caller, "show", "-s", "--format=%P", merge)
						ps := strings.Fields(parents)
						if err != nil || len(ps) < 2 {
							continue
						}
						if _, err := gitx.Run(caller, "merge-base", "--is-ancestor", sha, ps[0]); err == nil {
							continue
						}
						landed = slices.ContainsFunc(ps[1:], func(parent string) bool {
							_, err := gitx.Run(caller, "merge-base", "--is-ancestor", sha, parent)
							return err == nil
						})
						if landed {
							break
						}
					}
				}
			}
		}
	}
	if !landed {
		return refuse("commit is not an ancestor of main or this repository's separate combine HEAD")
	}
	changed, e := gitx.Run(repo, "diff", "--name-only", "--no-renames", "-z", parent, sha, "--")
	if e != nil {
		return refuse("cannot read commit paths")
	}
	count := 0
	for _, file := range strings.Split(changed, "\x00") {
		if file == "" {
			continue
		}
		count++
		if !gatecheck.Owns(gatecheck.Prefix(repo, workspace), entry, file) {
			return refuse("commit changes unowned file " + file)
		}
	}
	if count == 0 {
		return refuse("commit has no changes")
	}
	temp, e := os.MkdirTemp("", "code-forge-B1017-")
	if e != nil {
		return "", "", cleanup, e
	}
	cleanup = func() { os.RemoveAll(temp) }
	tree = filepath.Join(temp, "tree")
	if _, e = gitx.Run("", "clone", "--shared", "--no-checkout", "--quiet", "--", repo, tree); e == nil {
		_, e = gitx.Run(tree, "-c", "core.hooksPath="+os.DevNull, "checkout", "--quiet", "--detach", sha)
	}
	if e != nil {
		cleanup()
		return "", "", func() {}, e
	}
	return tree, parent, cleanup, nil
}
