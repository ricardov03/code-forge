package rebase

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/ricardov03/code-forge/native/gitx"
)

// B938: a block branch can carry a commit of an earlier block that reached
// main under another sha (the same patch, as when that block was rebased
// before it landed). Replaying it onto main, which may have changed those
// lines since, was a conflict that was not the block's. A replay takes only
// the block's own commits: the ones git cherry finds on the new base by
// patch-id are dropped.

// Carried lists the commits of base..head that onto already has by patch
// (git cherry), oldest first. A range with a merge commit lists none, since
// cherry does not compare merges.
func Carried(tree string, env []string, onto, base, head string) ([]string, error) {
	merges, err := gitx.WithEnv("", env, "-C", tree, "rev-list", "--merges", base+".."+head)
	if err != nil || strings.TrimSpace(merges) != "" {
		return nil, err
	}
	out, err := gitx.WithEnv("", env, "-C", tree, "cherry", onto, head, base)
	if err != nil {
		return nil, err
	}
	carried := []string{}
	for _, line := range strings.Split(out, "\n") {
		if sha, ok := strings.CutPrefix(strings.TrimSpace(line), "- "); ok {
			carried = append(carried, sha)
		}
	}
	return carried, nil
}

// Onto rebases tree's HEAD from base onto onto, like `git rebase --onto onto
// base`, without the commits onto already has by patch. It returns the
// dropped commits. A stop is left in progress for ContinueWith, as the plain
// rebase did.
func Onto(tree string, env []string, onto, base string) ([]string, error) {
	plain := []string{"-C", tree, "-c", UnionOff, "-c", "rebase.updateRefs=false", "rebase", "--no-autostash"}
	carried, err := Carried(tree, env, onto, base, "HEAD")
	if err != nil {
		return nil, err
	}
	if len(carried) == 0 {
		_, err := gitx.WithEnv("", env, append(plain, "--onto", onto, base)...)
		return nil, err
	}
	skip := map[string]bool{}
	for _, sha := range carried {
		skip[sha] = true
	}
	list, err := gitx.WithEnv("", env, "-C", tree, "rev-list", "--reverse", "--no-merges", base+"..HEAD")
	if err != nil {
		return nil, err
	}
	todo := ""
	for _, sha := range strings.Fields(list) {
		if !skip[sha] {
			todo += "pick " + sha + "\n"
		}
	}
	if todo == "" {
		todo = "noop\n"
	}
	dir, err := os.MkdirTemp("", "forge-onto-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	todoFile, editor := filepath.Join(dir, "todo"), filepath.Join(dir, "editor")
	if err := os.WriteFile(todoFile, []byte(todo), 0600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(editor, []byte("#!/bin/sh\ncp "+quote(todoFile)+" \"$1\"\n"), 0700); err != nil {
		return nil, err
	}
	run := append(append([]string{}, env...), "GIT_SEQUENCE_EDITOR="+quote(editor), "GIT_EDITOR=true")
	_, err = gitx.WithEnv("", run, append(plain, "--interactive", "--no-autosquash", "--empty=drop", "--onto", onto, base)...)
	return carried, err
}
