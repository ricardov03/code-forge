package blockclose

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ricardov03/code-forge/native/gitx"
)

func gitReason(err error) string {
	if e, ok := err.(*gitx.CommandError); ok {
		for _, line := range strings.Split(e.Stderr, "\n") {
			if strings.HasPrefix(line, "fatal: ") || strings.HasPrefix(line, "error: ") {
				return strings.TrimSpace(strings.SplitN(line, ": ", 2)[1])
			}
		}
		return fmt.Sprintf("git exited %d", e.Code)
	}
	return err.Error()
}

// RemoveWorktree never forces a dirty tree and always retains its branch.
// Like Node, prune an already missing registration, and explain partial removal
// when git unregistered a tree but could not remove the remaining files.
func RemoveWorktree(workspace, path string) string {
	_, err := gitx.Run(workspace, "worktree", "remove", path)
	if err == nil {
		return ""
	}
	listing, listErr := gitx.Run(workspace, "worktree", "list", "--porcelain", "-z")
	if listErr != nil {
		return gitReason(err)
	}
	real := func(p string) string {
		if resolved, e := filepath.EvalSymlinks(p); e == nil {
			return resolved
		}
		return filepath.Clean(p)
	}
	found, current, prunable := false, false, false
	for _, field := range strings.Split(listing, "\x00") {
		if strings.HasPrefix(field, "worktree ") {
			current = real(strings.TrimPrefix(field, "worktree ")) == real(path)
			found = found || current
		} else if current && (field == "prunable" || strings.HasPrefix(field, "prunable ")) {
			prunable = true
		}
	}
	command, _ := err.(*gitx.CommandError)
	if !found && command != nil && strings.Contains(strings.ToLower(command.Stderr), "directory not empty") {
		quoted := path
		if !regexp.MustCompile(`^[a-zA-Z0-9_./-]+$`).MatchString(path) {
			quoted = "'" + strings.ReplaceAll(path, "'", "'\\''") + "'"
		}
		return fmt.Sprintf("worktree %s was unregistered but files are left; remove them with: rm -rf %s", path, quoted)
	}
	if !prunable {
		return gitReason(err)
	}
	if _, err := gitx.Run(workspace, "worktree", "prune"); err != nil {
		return gitReason(err)
	}
	return ""
}
