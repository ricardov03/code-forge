package proof

import (
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
		return nil, fmt.Errorf("malformed worktree record (it needs an absolute path, a branch and a base_sha)")
	}
	top, e := filepath.EvalSymlinks(p)
	if e != nil {
		return nil, fmt.Errorf("the worktree %s is gone (removed or moved)", p)
	}
	st, e := os.Stat(top)
	if e != nil || !st.IsDir() {
		return nil, fmt.Errorf("the worktree %s is not a directory", p)
	}
	head, e := gitx.WithEnv(top, append(gitx.ChildEnv(), "GIT_CEILING_DIRECTORIES="+filepath.Dir(top)), "-C", top, "symbolic-ref", "-q", "HEAD")
	head = strings.TrimSpace(head)
	if e != nil || head != "refs/heads/"+branch {
		now := "its HEAD is detached, or it is not a git worktree"
		if e == nil {
			now = "it is on " + strings.TrimPrefix(head, "refs/heads/")
		}
		return nil, fmt.Errorf("the worktree %s is not on its branch %s (%s)", p, branch, now)
	}
	common := func(dir string) string {
		v, e := gitx.Run(dir, "rev-parse", "--git-common-dir")
		if e != nil {
			return ""
		}
		v = strings.TrimSpace(v)
		if !filepath.IsAbs(v) {
			v = filepath.Join(dir, v)
		}
		return Real(v)
	}
	if common(top) != common(workspace) {
		return nil, fmt.Errorf("the worktree %s belongs to another repository", p)
	}
	main, e := gitx.RepoTop(workspace)
	if e != nil {
		return nil, e
	}
	rel, _ := filepath.Rel(main, Real(workspace))
	if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return nil, fmt.Errorf("the run's workspace %s is not inside its repository, so it has no place in the worktree %s", workspace, p)
	}
	inTree := filepath.Join(top, rel)
	st, e = os.Stat(inTree)
	if e != nil || !st.IsDir() {
		return nil, fmt.Errorf("the worktree %s has no %s directory (the run's workspace)", p, rel)
	}
	return &Tree{p, inTree}, nil
}
