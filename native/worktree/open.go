// Package worktree plans block isolation before publication, matching Node's
// prepareWorktree. A plan is read-only; Make runs after the ownership checks.
package worktree

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/childreg"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// Settings are proof.worktree from the snapshot. DirSet and BaseSet say the
// value was configured as a string, an empty one too: Node keeps "" apart
// from an absent setting (B581).
type Settings struct {
	Dir, Branch, Base string
	DirSet, BaseSet   bool
}
type Plan struct {
	Path string
	Make func() (*Made, error)
}
type Made struct {
	Record   ledger.Object
	Created  bool
	Rollback func() string
}

func text(v any) string     { s, _ := v.(string); return s }
func quote(s string) string { b, _ := ledger.Stringify(s, false); return string(b) }
func real(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
func occupied(p string) bool { _, err := os.Lstat(p); return err == nil }
func realFuture(p string) string {
	if occupied(p) || filepath.Dir(p) == p {
		return real(p)
	}
	return filepath.Join(realFuture(filepath.Dir(p)), filepath.Base(p))
}
func reason(err error) string {
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
func fail(message string) error { return &state.Error{Code: "worktree-failed", Message: message} }
func branchName(template, run, id string) string {
	return strings.NewReplacer("{run}", run, "{block}", id).Replace(template)
}
func exists(top, branch string) bool {
	_, err := gitx.Run(top, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}
func list(top string) (string, error) {
	out, err := gitx.Run(top, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return "", &state.Error{Code: "git-failed", Message: "git worktree list failed (" + reason(err) + ")"}
	}
	return out, nil
}

type registration struct {
	branch   string
	prunable bool
}

func registered(top, where string) (*registration, error) {
	out, err := list(top)
	if err != nil {
		return nil, err
	}
	var current, found *registration
	for _, line := range strings.Split(out, "\x00") {
		if strings.HasPrefix(line, "worktree ") {
			current = &registration{}
			if real(strings.TrimPrefix(line, "worktree ")) == real(where) {
				found = current
			}
		} else if current != nil && strings.HasPrefix(line, "branch refs/heads/") {
			current.branch = strings.TrimPrefix(line, "branch refs/heads/")
		} else if current != nil && (line == "prunable" || strings.HasPrefix(line, "prunable ")) {
			current.prunable = true
		}
	}
	return found, nil
}
func directory(top string, s Settings, run string) (string, bool, error) {
	old := realFuture(filepath.Join(filepath.Dir(top), filepath.Base(top)+"-worktrees"))
	if s.Dir != "" || s.DirSet {
		dir := s.Dir
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(top, dir)
		}
		return realFuture(filepath.Clean(dir)), false, nil
	}
	if runtime.GOOS != "darwin" {
		return old, false, nil
	}
	if run != "" {
		out, err := list(top)
		if err != nil {
			return "", false, err
		}
		where := ""
		for _, line := range strings.Split(out, "\x00") {
			if strings.HasPrefix(line, "worktree ") {
				where = strings.TrimPrefix(line, "worktree ")
			} else if where != "" && realFuture(filepath.Dir(where)) == old && line == "branch refs/heads/"+branchName(s.Branch, run, filepath.Base(where)) {
				return old, false, nil
			}
		}
	}
	return realFuture(filepath.Join(filepath.Dir(top), filepath.Base(top)+"-worktrees.noindex")), true, nil
}
func markDirectory(dir string) error {
	if err := os.MkdirAll(dir, 0777); err != nil {
		if os.IsExist(err) {
			return nil
		}
		return err
	}
	marker := filepath.Join(dir, ".metadata_never_index")
	if occupied(marker) {
		return nil
	}
	f, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0666)
	if os.IsExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return f.Close()
}
func discard(top, tree, branch string) []string {
	left := []string{}
	if tree != "" {
		if _, err := gitx.Run(top, "worktree", "remove", "--force", tree); err != nil {
			left = append(left, "the worktree "+tree+" ("+reason(err)+")")
		}
	}
	if branch != "" {
		if _, err := gitx.Run(top, "branch", "-D", branch); err != nil {
			left = append(left, "the branch "+branch+" ("+reason(err)+")")
		}
	}
	return left
}
func leftBehind(left []string) string {
	pronoun := "them"
	if len(left) == 1 {
		pronoun = "it"
	}
	return "left behind: " + strings.Join(left, "; ") + " — remove " + pronoun + " by hand"
}
func add(top, id string, want ledger.Object, attach bool) (*Made, error) {
	where, branch := text(want.Get("path")), text(want.Get("branch"))
	newBranch := branch
	if attach {
		newBranch = ""
	}
	args := []string{"worktree", "add", where}
	if attach {
		args = append(args, branch)
	} else {
		args = append(args, "-b", branch, text(want.Get("base_sha")))
	}
	// Checkout gets Node's five-minute allowance; all read-only git calls use
	// the kit's thirty-second deadline and the same sanitized environment.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir, cmd.Env = top, gitx.ChildEnv()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := childreg.Run(cmd); err != nil {
		code := -1
		if e, ok := err.(*exec.ExitError); ok {
			code = e.ExitCode()
		}
		err = &gitx.CommandError{Args: args, Code: code, Stderr: stderr.String(), Cause: err}
		entry, e := registered(top, where)
		if e != nil {
			return nil, e
		}
		tree := ""
		if entry != nil {
			if entry.prunable {
				_, _ = gitx.Run(top, "worktree", "prune")
			} else {
				tree = where
			}
		}
		if newBranch != "" && !exists(top, newBranch) {
			newBranch = ""
		}
		left := discard(top, tree, newBranch)
		outcome := "nothing was recorded"
		if len(left) > 0 {
			outcome += ", but " + leftBehind(left)
		}
		return nil, fail(fmt.Sprintf("block %s: git worktree add failed (%s); %s", id, reason(err), outcome))
	}
	want = want.Clone()
	want.Set("path", real(where))
	return &Made{Record: want, Created: true, Rollback: func() string {
		left := discard(top, text(want.Get("path")), newBranch)
		if len(left) == 0 {
			return ""
		}
		return "Removing the worktree of the failed open " + leftBehind(left) + "."
	}}, nil
}

func Prepare(cwd string, s Settings, run, id, base string, explicit bool, previous ledger.Object) (*Plan, error) {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\\x00") {
		return nil, fail("block " + quote(id) + `: a block id must be one plain path segment (no "/", "\\" or "..") to name its worktree; nothing was recorded`)
	}
	top, err := gitx.RepoTop(cwd)
	if err != nil {
		return nil, err
	}
	refuseOccupied := func(p string) error {
		if occupied(p) {
			return fail(fmt.Sprintf("block %s: %s already exists and is not this block's worktree — move it away or change proof.worktree.dir; nothing was recorded", id, p))
		}
		return nil
	}
	if previous != nil {
		want := previous.Clone()
		where, branch := text(want.Get("path")), text(want.Get("branch"))
		dir, mark, err := directory(top, s, "")
		if err != nil {
			return nil, err
		}
		return &Plan{Path: where, Make: func() (*Made, error) {
			entry, err := registered(top, where)
			if err != nil {
				return nil, err
			}
			mark := mark && realFuture(filepath.Dir(where)) == dir
			if entry != nil && !entry.prunable && occupied(where) {
				if entry.branch != branch {
					actual := entry.branch
					if actual == "" {
						actual = "a detached HEAD"
					}
					return nil, fail(fmt.Sprintf("block %s: worktree %s is on %s, not %s — check out %s in it; nothing was recorded", id, where, actual, branch, branch))
				}
				if mark {
					if err := markDirectory(dir); err != nil {
						return nil, err
					}
				}
				return &Made{Record: want}, nil
			}
			if entry != nil {
				_, _ = gitx.Run(top, "worktree", "prune")
			}
			if err := refuseOccupied(where); err != nil {
				return nil, err
			}
			attach := exists(top, branch)
			if mark {
				if err := markDirectory(dir); err != nil {
					return nil, err
				}
			}
			return add(top, id, want, attach)
		}}, nil
	}
	branch := branchName(s.Branch, run, id)
	out, err := gitx.Run(top, "check-ref-format", "--branch", branch)
	if strings.HasPrefix(branch, "-") || err != nil || strings.TrimSpace(out) != branch {
		return nil, fail(fmt.Sprintf("block %s: proof.worktree.branch gives %s, which is not a valid git branch name; nothing was recorded", id, quote(branch)))
	}
	attach := exists(top, branch)
	dir, mark, err := directory(top, s, run)
	if err != nil {
		return nil, err
	}
	if !explicit && (s.Base != "" || s.BaseSet) {
		out, err := gitx.Run(top, "rev-parse", "--verify", "--quiet", s.Base+"^{commit}")
		if strings.HasPrefix(s.Base, "-") || err != nil {
			return nil, fail(fmt.Sprintf("block %s: proof.worktree.base %s is not a commit of the repository; nothing was recorded", id, quote(s.Base)))
		}
		base = strings.TrimSpace(out)
	}
	want := ledger.Object{{Key: "path", Value: filepath.Join(dir, id)}, {Key: "branch", Value: branch}, {Key: "base_sha", Value: base}}
	descendant := func() error {
		_, err := gitx.Run(top, "merge-base", "--is-ancestor", base, "refs/heads/"+branch)
		if err == nil {
			return nil
		}
		if e, ok := err.(*gitx.CommandError); ok && e.Code == 1 {
			return fail(fmt.Sprintf("block %s: branch %s does not descend from base %s — delete the branch or pick another block id; nothing was recorded", id, branch, base))
		}
		return fail(fmt.Sprintf("block %s: cannot check branch %s against base %s (%s); nothing was recorded", id, branch, base, reason(err)))
	}
	if attach {
		if err := descendant(); err != nil {
			return nil, err
		}
	}
	if err := refuseOccupied(text(want.Get("path"))); err != nil {
		return nil, err
	}
	return &Plan{Path: text(want.Get("path")), Make: func() (*Made, error) {
		if attach {
			if err := descendant(); err != nil {
				return nil, err
			}
		} else if exists(top, branch) {
			return nil, fail(fmt.Sprintf("block %s: branch %s already exists — delete it or change proof.worktree.branch; nothing was recorded", id, branch))
		}
		if mark {
			if err := markDirectory(dir); err != nil {
				return nil, err
			}
		}
		return add(top, id, want, attach)
	}}, nil
}
