package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/landmerge"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/rebase"
	"github.com/ricardov03/code-forge/native/state"
)

// B795: land resolves a merge conflict by itself when every conflicted file
// is append-only (land.auto_resolve); any other conflict aborts the merge,
// leaves main as it was and writes one land.conflict row naming the files and
// their kinds.

func landConflictList(files []landmerge.File) []any {
	out := make([]any, len(files))
	for i, f := range files {
		out[i] = ledger.Object{{Key: "file", Value: f.Path}, {Key: "kind", Value: f.Kind}}
	}
	return out
}

func landConflictRow(home, run, id string, files []landmerge.File, next string) error {
	_, err := state.WriteSigned(home, run, ledger.Object{{Key: "event", Value: "land.conflict"}, {Key: "block", Value: id}, {Key: "files", Value: landConflictList(files)}, {Key: "next", Value: next}})
	return err
}

func landResolvedRow(home, run, id string, files []landmerge.File) error {
	_, err := state.WriteSigned(home, run, ledger.Object{{Key: "event", Value: "land.resolved"}, {Key: "block", Value: id}, {Key: "files", Value: landConflictList(files)}})
	return err
}

// lcfMerge merges branch into the checkout dir. With land.auto_resolve on,
// git's union driver is off and a conflict whose files are all append-only
// is resolved and committed (when wantTree is set, only if the result is that
// tree). Any other conflict aborts the merge, so dir is as it was, and
// returns the files with their kinds. resolved lists the files land merged.
// env, when set, is the git environment of the merge and its commit (B797:
// the land token and reflog action on main); nil uses the plain child env.
func lcfMerge(dir, branch string, auto landmerge.Settings, wantTree string, env []string) (resolved, conflicts []landmerge.File, output string, err error) {
	run := func(args ...string) (string, error) {
		if env == nil {
			return gitx.Run(dir, args...)
		}
		return gitx.WithEnv(dir, env, args...)
	}
	// B813: a clean merge that would repeat a CHANGELOG ### heading stops
	// before its commit so the headings are folded first.
	fold := landmerge.FoldNeeded(dir, gitx.ChildEnv(), branch)
	args := []string{"merge", "--no-ff", "--no-edit", "--", branch}
	if fold {
		args = []string{"merge", "--no-ff", "--no-edit", "--no-commit", "--", branch}
	}
	if auto.Enabled {
		args = append([]string{"-c", rebase.UnionOff}, args...)
	}
	output, err = run(args...)
	if err == nil {
		if !fold {
			return nil, nil, output, nil
		}
		return lcfCommit(dir, nil, wantTree, output, run)
	}
	files, classifyErr := landmerge.Classify(dir, gitx.ChildEnv(), auto)
	if classifyErr != nil || len(files) == 0 {
		lcfAbort(dir)
		return nil, nil, output, err
	}
	if !auto.Enabled {
		lcfAbort(dir)
		for i := range files {
			files[i].Kind = landmerge.KindCode // nothing is append-only with auto_resolve off
		}
		return nil, files, output, err
	}
	resolved, resolveErr := landmerge.Resolve(dir, gitx.ChildEnv(), auto)
	var conflict *landmerge.Conflict
	if errors.As(resolveErr, &conflict) {
		lcfAbort(dir)
		return nil, conflict.Files, output, err
	}
	if resolveErr != nil {
		lcfAbort(dir)
		return nil, nil, output, resolveErr
	}
	return lcfCommit(dir, resolved, wantTree, output, run)
}

// lcfCommit folds repeated CHANGELOG ### headings in the stopped merge (B813),
// checks the result is wantTree when set, and commits the merge.
func lcfCommit(dir string, resolved []landmerge.File, wantTree, output string, run func(...string) (string, error)) ([]landmerge.File, []landmerge.File, string, error) {
	if _, err := landmerge.FoldIndex(dir, gitx.ChildEnv()); err != nil {
		lcfAbort(dir)
		return nil, nil, output, err
	}
	if wantTree != "" {
		tree, treeErr := gitx.Run(dir, "write-tree")
		if treeErr != nil || strings.TrimSpace(tree) != wantTree {
			lcfAbort(dir)
			return nil, nil, output, fmt.Errorf("the resolved merge differs from the tree the merge gate checked")
		}
	}
	if out, commitErr := run("-c", "commit.gpgsign=false", "commit", "--no-edit", "--cleanup=strip"); commitErr != nil {
		lcfAbort(dir)
		return nil, nil, landFirst(out, output), commitErr
	}
	return resolved, nil, output, nil
}

// lcfAbort ends a stopped merge so the checkout is back at its HEAD.
func lcfAbort(dir string) {
	_, _ = gitx.Run(dir, "merge", "--abort")
}

// lcfReason is the refusal for a conflict land does not resolve.
func lcfReason(files []landmerge.File) string {
	return "merge conflict land cannot resolve by itself; main is unchanged: " + landmerge.Describe(files)
}

// lcfPrecheck merges the block worktree's HEAD into base in memory (B814,
// before close): it returns the conflicted files with their kinds when any
// one of them is not one land resolves by itself, else nil.
func lcfPrecheck(main, base, tree string, auto landmerge.Settings) ([]landmerge.File, error) {
	head, err := gitx.Run(tree, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("cannot read the block head: %s", landGitError(err, err.Error()))
	}
	head = strings.TrimSpace(head)
	ours := "refs/heads/" + base
	mergeBase, err := gitx.Run(main, "merge-base", ours, head)
	if err != nil {
		return nil, nil
	}
	out, err := gitx.Run(main, "merge-tree", "--write-tree", "--name-only", "--no-messages", ours, head)
	if err == nil {
		return nil, nil
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if command, ok := err.(*gitx.CommandError); !ok || command.Code != 1 || len(lines) < 2 {
		return nil, fmt.Errorf("merge-tree check failed: %s", landGitError(err, err.Error()))
	}
	files, seen := []string{}, map[string]bool{}
	for _, file := range lines[1:] {
		if file = strings.TrimSpace(file); file != "" && !seen[file] {
			seen[file] = true
			files = append(files, file)
		}
	}
	if _, ok, err := landBatchAppendTree(main, strings.TrimSpace(lines[0]), strings.TrimSpace(mergeBase), ours, head, files, auto); err != nil || ok {
		return nil, err
	}
	kinds := make([]landmerge.File, len(files))
	for i, file := range files {
		kinds[i] = landmerge.File{Path: file, Kind: auto.Kind(file, false)}
	}
	return kinds, nil
}

func lcfPrecloseReason(branch, base string, files []landmerge.File) string {
	return "merge conflict with " + base + " found before close; the block stays open and main is unchanged: " + landmerge.Describe(files) + "; rebase " + branch + " onto " + base + " and land again"
}
