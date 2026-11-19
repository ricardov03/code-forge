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
	args := []string{"merge", "--no-ff", "--no-edit", "--", branch}
	if auto.Enabled {
		args = append([]string{"-c", rebase.UnionOff}, args...)
	}
	output, err = run(args...)
	if err == nil {
		return nil, nil, output, nil
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
