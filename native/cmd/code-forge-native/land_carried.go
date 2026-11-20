package main

import (
	"strings"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/landmerge"
	"github.com/ricardov03/code-forge/native/rebase"
)

// landCarriedPair is the merge base and head the batch precheck merges into
// running for block (B938). A branch that carries commits running already
// has by patch is checked as its own commits replayed onto running, the way
// the batch rebase replays it; a replay step that does not merge falls back
// to the whole branch, which then parks as before.
func landCarriedPair(main, running string, block *landBatchBlock, auto landmerge.Settings) (string, string, error) {
	head := strings.TrimSpace(block.head)
	carried, err := rebase.Carried(main, gitx.ChildEnv(), running, block.base, head)
	if err != nil || len(carried) == 0 {
		return block.base, head, err
	}
	skip := map[string]bool{}
	for _, sha := range carried {
		skip[sha] = true
	}
	list, err := gitx.Run(main, "rev-list", "--reverse", "--no-merges", block.base+".."+head)
	if err != nil {
		return "", "", err
	}
	replayed := running
	for _, sha := range strings.Fields(list) {
		if skip[sha] {
			continue
		}
		out, err := gitx.Run(main, "merge-tree", "--write-tree", "--name-only", "--no-messages", "--merge-base="+sha+"^", replayed, sha)
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		tree := strings.TrimSpace(lines[0])
		if err != nil {
			command, ok := err.(*gitx.CommandError)
			if !ok || command.Code != 1 || len(lines) < 2 {
				return "", "", err
			}
			merged, ok, err := landBatchAppendTree(main, tree, sha+"^", replayed, sha, landCarriedFiles(lines[1:]), auto)
			if err != nil {
				return "", "", err
			}
			if !ok {
				return block.base, head, nil
			}
			tree = merged
		}
		commit, err := gitx.Run(main, "commit-tree", tree, "-p", replayed, "-m", "code-forge batch precheck replay "+block.id)
		if err != nil {
			return "", "", err
		}
		replayed = strings.TrimSpace(commit)
	}
	return running, replayed, nil
}

func landCarriedFiles(lines []string) []string {
	files, seen := []string{}, map[string]bool{}
	for _, file := range lines {
		if file = strings.TrimSpace(file); file != "" && !seen[file] {
			seen[file] = true
			files = append(files, file)
		}
	}
	return files
}
