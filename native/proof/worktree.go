package proof

import (
	"errors"
	"fmt"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Tree struct{ Path, Workspace string }

func Real(p string) string {
	if r, e := filepath.EvalSymlinks(p); e == nil {
		p = r
	}
	a, _ := filepath.Abs(p)
	return a
}

var controls = regexp.MustCompile(`[\x00-\x1f\x7f]`)
var sha = regexp.MustCompile(`^([a-f0-9]{40}|[a-f0-9]{64})$`)

func BlockTree(workspace string, entry ledger.Object) (*Tree, error) {
	if entry.Get("worktree") == nil {
		return nil, nil
	}
	t, _ := entry.Get("worktree").(ledger.Object)
	p, _ := t.Get("path").(string)
	branch, _ := t.Get("branch").(string)
	base, _ := t.Get("base_sha").(string)
	if !filepath.IsAbs(p) || controls.MatchString(p) || branch == "" || controls.MatchString(branch) || !sha.MatchString(base) {
		return nil, treeErr("worktree-malformed", fmt.Errorf("malformed worktree record (it needs an absolute path, a branch and a base_sha)"))
	}
	top, e := filepath.EvalSymlinks(p)
	if e != nil {
		return nil, treeErr("worktree-missing", fmt.Errorf("the worktree %s is gone (removed or moved)", p))
	}
	st, e := os.Stat(top)
	if e != nil {
		// statSync throws: no code (an I/O failure the caller rethrows)
		return nil, e
	}
	if !st.IsDir() {
		return nil, treeErr("worktree-not-dir", fmt.Errorf("the worktree %s is not a directory", p))
	}
	head, e := gitx.WithEnv(top, append(gitx.ChildEnv(), "GIT_CEILING_DIRECTORIES="+filepath.Dir(top)), "-C", top, "symbolic-ref", "-q", "HEAD")
	if gitErr, ok := e.(*gitx.CommandError); ok && gitErr.Code < 0 {
		return nil, treeErr("git-failed", e)
	}
	head = strings.TrimSpace(head)
	if e != nil || head != "refs/heads/"+branch {
		now := "its HEAD is detached, or it is not a git worktree"
		if e == nil {
			now = "it is on " + strings.TrimPrefix(head, "refs/heads/")
		}
		return nil, treeErr("worktree-branch-mismatch", fmt.Errorf("the worktree %s is not on its branch %s (%s)", p, branch, now))
	}
	common := func(dir string) (string, error) {
		v, e := gitx.Run(dir, "rev-parse", "--git-common-dir")
		if e != nil {
			return "", treeErr("git-failed", e)
		}
		v = strings.TrimSpace(v)
		if !filepath.IsAbs(v) {
			v = filepath.Join(dir, v)
		}
		return Real(v), nil
	}
	treeCommon, e := common(top)
	if e != nil {
		return nil, e
	}
	workspaceCommon, e := common(workspace)
	if e != nil {
		return nil, e
	}
	if treeCommon != workspaceCommon {
		return nil, treeErr("worktree-foreign", fmt.Errorf("the worktree %s belongs to another repository", p))
	}
	main, e := gitx.RepoTop(workspace)
	if e != nil {
		var ge *gitx.Error
		if errors.As(e, &ge) && ge.Code == "git-failed" {
			return nil, treeErr("git-failed", e)
		}
		return nil, e
	}
	rel, _ := filepath.Rel(main, Real(workspace))
	if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return nil, treeErr("worktree-workspace-missing", fmt.Errorf("the run's workspace %s is not inside its repository, so it has no place in the worktree %s", workspace, p))
	}
	inTree := filepath.Join(top, rel)
	st, e = os.Stat(inTree)
	if e != nil || !st.IsDir() {
		return nil, treeErr("worktree-workspace-missing", fmt.Errorf("the worktree %s has no %s directory (the run's workspace)", p, rel))
	}
	return &Tree{p, inTree}, nil
}

// TreeError is a BlockTree refusal with proof/worktree.mjs blockTree's
// StateError code (worktree-malformed, worktree-missing, worktree-not-dir,
// worktree-branch-mismatch, worktree-foreign, worktree-workspace-missing,
// git-failed). Its text is the wrapped error's, so callers that print the
// error see what they saw before (B507).
type TreeError struct {
	Code string
	Err  error
}

func (e *TreeError) Error() string { return e.Err.Error() }
func (e *TreeError) Unwrap() error { return e.Err }

func treeErr(code string, err error) error { return &TreeError{Code: code, Err: err} }

// TreeCode is the BlockTree refusal's code ("" for an error that has none).
func TreeCode(err error) string {
	var te *TreeError
	if errors.As(err, &te) {
		return te.Code
	}
	return ""
}
