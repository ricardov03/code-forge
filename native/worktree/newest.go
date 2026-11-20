package worktree

import (
	"fmt"
	"os"
	"strings"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/state"
)

// NewestBase (B942) is the base a new coder worktree starts from: the newer
// of the checked-out branch and origin's copy of it. It fetches origin
// <branch> (with the user's git config, so credentials work, and no prompt),
// then picks the local tip when it contains origin's, or origin's tip when
// it contains the local one. It never moves the local branch: a newer
// origin tip is used by its sha. A detached HEAD, a repository without an
// origin, and a fetch that fails keep "" (the caller uses HEAD as before).
// Local and origin tips that diverged refuse with the next step.
func NewestBase(top string) (sha, branch string, err error) {
	out, e := gitx.Run(top, "symbolic-ref", "--quiet", "--short", "HEAD")
	branch = strings.TrimSpace(out)
	if e != nil || branch == "" || strings.HasPrefix(branch, "-") {
		return "", "", nil
	}
	if url, e := gitx.Run(top, "remote", "get-url", "origin"); e != nil || strings.TrimSpace(url) == "" {
		return "", branch, nil
	}
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if _, e := gitx.WithEnv(top, env, "fetch", "--quiet", "--no-tags", "origin", "refs/heads/"+branch); e != nil {
		return "", branch, nil
	}
	remote, e := gitx.Run(top, "rev-parse", "--verify", "--quiet", "FETCH_HEAD^{commit}")
	if e != nil {
		return "", branch, nil
	}
	local, e := gitx.Run(top, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if e != nil {
		return "", branch, nil
	}
	l, r := strings.TrimSpace(local), strings.TrimSpace(remote)
	switch {
	case l == r:
		return l, branch, nil
	case newestAncestor(top, r, l):
		return l, branch, nil
	case newestAncestor(top, l, r):
		return r, branch, nil
	}
	return "", branch, &state.Error{Code: "base-diverged", Message: fmt.Sprintf("local %s (%s) and origin/%s (%s) diverged, so there is no newest base to build on (B942): push or land the local commits, or open on a commit with --base <sha>; nothing was recorded\nnext: git -C %s log --oneline --left-right %s...FETCH_HEAD", branch, newestShort(l), branch, newestShort(r), newestQuote(top), newestQuote(branch))}
}

// newestAncestor says a is an ancestor of b (or equal); a git error says no.
func newestAncestor(top, a, b string) bool {
	_, err := gitx.Run(top, "merge-base", "--is-ancestor", a, b)
	return err == nil
}

func newestShort(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// newestQuote single-quotes a shell word that needs it.
func newestQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
