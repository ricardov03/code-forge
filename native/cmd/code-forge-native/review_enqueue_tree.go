package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
)

var reviewTreeObjectID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// reviewEnqueueTree is blockTreeOf's blockTree in src/proof/worktree.mjs for
// an entry whose worktree is set: the realpath of the block's worktree, or
// Node's refusal for it ({status: refused, reason: <code>, message: "block
// <id>: <message>; nothing was queued"}). ok=false only where Node's message
// carries a git failure text Go does not reproduce (a git that cannot run, or
// a failed rev-parse).
func reviewEnqueueTree(ctx context.Context, record, entry ledger.Object, block string) (string, ledger.Object, bool) {
	refuse := func(code, message string) (string, ledger.Object, bool) {
		return "", reviewEnqueueRefusal(code, "block "+block+": "+message+"; nothing was queued"), true
	}
	tree, isObject := entry.Get("worktree").(ledger.Object)
	path, p := tree.Get("path").(string)
	branch, b := tree.Get("branch").(string)
	base, h := tree.Get("base_sha").(string)
	if !isObject || !p || !filepath.IsAbs(path) || reviewBlockControl.MatchString(path) || !b || branch == "" || reviewBlockControl.MatchString(branch) || !h || !reviewTreeObjectID.MatchString(base) {
		return refuse("worktree-malformed", "malformed worktree record (it needs an absolute path, a branch and a base_sha)")
	}
	top, err := filepath.EvalSymlinks(path)
	if err != nil {
		return refuse("worktree-missing", "the worktree "+path+" is gone (removed or moved)")
	}
	if info, err := os.Stat(top); err != nil || !info.IsDir() {
		return refuse("worktree-not-dir", "the worktree "+path+" is not a directory")
	}
	// assertOnBranch: git symbolic-ref -q HEAD that never looks above top.
	head, exited := reviewTreeGit(ctx, top, []string{"GIT_CEILING_DIRECTORIES=" + filepath.Dir(top)}, "-C", top, "symbolic-ref", "-q", "HEAD")
	if !exited {
		return "", nil, false
	}
	if head != "refs/heads/"+branch {
		now := "its HEAD is detached, or it is not a git worktree"
		if head != "" {
			now = "it is on " + strings.TrimPrefix(head, "refs/heads/")
		}
		return refuse("worktree-branch-mismatch", "the worktree "+path+" is not on its branch "+branch+" ("+now+")")
	}
	workspace, ok := record.Get("workspace").(string)
	if !ok {
		return "", nil, false
	}
	common := func(dir string) (string, bool) {
		out, ok := reviewBlockGit(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if !ok || out == "" {
			return "", false
		}
		if real, err := filepath.EvalSymlinks(out); err == nil {
			return real, true
		}
		return out, true
	}
	treeCommon, ok := common(top)
	if !ok {
		return "", nil, false
	}
	spaceCommon, ok := common(workspace)
	if !ok {
		return "", nil, false
	}
	if treeCommon != spaceCommon {
		return refuse("worktree-foreign", "the worktree "+path+" belongs to another repository")
	}
	spaceTop, ok := reviewBlockGit(ctx, workspace, "rev-parse", "--show-toplevel")
	if !ok || spaceTop == "" {
		return "", nil, false
	}
	if spaceTop, err = filepath.EvalSymlinks(spaceTop); err != nil {
		return "", nil, false
	}
	realSpace := workspace
	if r, err := filepath.EvalSymlinks(workspace); err == nil {
		realSpace = r
	}
	rel, err := filepath.Rel(spaceTop, realSpace)
	if err != nil {
		return "", nil, false
	}
	if rel == "." {
		rel = ""
	}
	if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return refuse("worktree-workspace-missing", "the run's workspace "+workspace+" is not inside its repository, so it has no place in the worktree "+path)
	}
	if rel != "" {
		if info, err := os.Stat(filepath.Join(top, rel)); err != nil || !info.IsDir() {
			return refuse("worktree-workspace-missing", "the worktree "+path+" has no "+rel+" directory (the run's workspace)")
		}
	}
	return top, nil, true
}

// reviewTreeGit runs git with Node's gitEnv plus extra. exited=false: git did
// not run to an exit (spawn failure, timeout, signal). A non-zero exit gives
// an empty output, as Node reads a failed symbolic-ref.
func reviewTreeGit(ctx context.Context, cwd string, extra []string, args ...string) (string, bool) {
	env := []string{}
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_") {
			env = append(env, e)
		}
	}
	env = append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	env = append(env, extra...)
	childCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(childCtx, "git", args...)
	cmd.Dir, cmd.Env = cwd, env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = reviewWaitPipeGrace
	out, err := cmd.Output()
	if childCtx.Err() != nil {
		return "", false
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if !exit.Exited() {
			return "", false
		}
		return "", true
	}
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}
