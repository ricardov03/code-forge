// Package rebase moves an open worktree block onto its base branch. B431:
// upstream changes to owned files never refuse up front; the block's commits
// are replayed and only a real merge conflict refuses. Changes credited to a
// closed sibling block are reported as accepted_landed, every other owned file
// main changed as upstream_owned, and each changed owned file is re-reviewed.
package rebase

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/humancheck"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/queue"
	"github.com/ricardov03/code-forge/native/sign"
	"github.com/ricardov03/code-forge/native/state"
	"github.com/ricardov03/code-forge/native/ticket"
)

type Accepted struct {
	File   string   `json:"file"`
	Blocks []string `json:"blocks"`
}

type Result struct {
	Block    string     `json:"block"`
	Event    string     `json:"event"`
	OldBase  string     `json:"old_base"`
	NewBase  string     `json:"new_base"`
	Accepted []Accepted `json:"accepted_landed"`
	// Owned files changed by base-branch commits that no closed block of the
	// run vouches for (B431); they replayed clean and go to re-review.
	Upstream []string `json:"upstream_owned,omitempty"`
}

func text(obj ledger.Object, key string) string { s, _ := obj.Get(key).(string); return s }
func object(obj ledger.Object, key string) ledger.Object {
	o, _ := obj.Get(key).(ledger.Object)
	return o
}
func refusal(code, message string) error {
	return &state.Error{Code: code, Message: code + ": " + message}
}

// Never let hook-exported GIT_* variables select another repository or identity.
func gitEnv() []string {
	return gitx.ChildEnv()
}

func gitWithEnv(tree string, env []string, args ...string) (string, error) {
	out, err := gitx.WithEnv("", env, append([]string{"-C", tree, "-c", "commit.gpgsign=false"}, args...)...)
	if e, ok := err.(*gitx.CommandError); ok {
		e.Args = args
	}
	return out, err
}

func git(tree string, args ...string) (string, error) { return gitWithEnv(tree, gitEnv(), args...) }
func resolve(tree, ref string) (string, error) {
	value, err := git(tree, "rev-parse", "--verify", ref+"^{commit}")
	return strings.TrimSpace(value), err
}

// origins checks every touching commit, including merge parents and changes later
// reverted. A block only vouches for commits between its recorded bases and branch;
// inherited history and commit messages prove nothing. A landing merge can also
// vouch for a block when a non-first parent is credited to it and the merge adds
// no files beyond those changed by that block's credited commits.
// B431: a commit no closed block vouches for is not refused; its owned files
// are returned as upstream (sorted, once each) for the replay to settle.
func origins(tree, runID, oldBase, newBase string, blocks ledger.Object, owns func(string) bool) ([]Accepted, []string, error) {
	originCommits := map[string]map[string]bool{}
	for _, field := range blocks {
		b, _ := field.Value.(ledger.Object)
		if text(b, "status") != "closed" && text(b, "status") != humancheck.DeferredStatus {
			continue
		}
		base, err := resolve(tree, text(b, "base_sha"))
		if err != nil {
			continue // No credit without a verifiable recorded base.
		}
		bases := []string{base}
		if recorded := text(object(b, "worktree"), "base_sha"); recorded != "" {
			base, err := resolve(tree, recorded)
			if err != nil {
				continue
			}
			bases = append(bases, base)
		}
		branches := []string{}
		for _, branch := range []string{"forge/" + runID + "/" + field.Key, text(object(b, "worktree"), "branch")} {
			if branch == "" {
				continue
			}
			sha, err := resolve(tree, "refs/heads/"+branch)
			if err == nil {
				branches = append(branches, sha)
			}
		}
		if len(branches) == 0 {
			continue
		}
		args := append([]string{"rev-list"}, branches...)
		args = append(args, "--not")
		commits, err := git(tree, append(args, bases...)...)
		if err != nil {
			return nil, nil, err
		}
		originCommits[field.Key] = map[string]bool{}
		for _, commit := range strings.Fields(commits) {
			originCommits[field.Key][commit] = true
		}
	}
	commits, err := git(tree, "rev-list", "--reverse", oldBase+".."+newBase)
	if err != nil {
		return nil, nil, err
	}
	changedFiles := func(revisions ...string) (string, error) {
		args := []string{"diff-tree", "--root", "--no-commit-id", "--no-renames", "--name-only", "-r", "-m", "-z"}
		args = append(args, revisions...)
		return git(tree, append(args, "--")...)
	}
	originFiles := map[string]map[string]bool{}
	accepted := map[string]map[string]bool{}
	upstream := map[string]bool{}
	for _, commit := range strings.Fields(commits) {
		changed, err := changedFiles(commit)
		if err != nil {
			return nil, nil, err
		}
		files := []string{}
		for _, file := range strings.Split(changed, "\x00") {
			if file != "" && owns(file) {
				files = append(files, file)
			}
		}
		if len(files) == 0 {
			continue
		}
		ids := []string{}
		for id, commits := range originCommits {
			if commits[commit] {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			parents, err := git(tree, "show", "-s", "--format=%P", commit, "--")
			if err != nil {
				return nil, nil, err
			}
			if parents := strings.Fields(parents); len(parents) > 1 {
				changed, err := changedFiles(parents[0], commit)
				if err != nil {
					return nil, nil, err
				}
				for id, commits := range originCommits {
					creditedParent := false
					for _, parent := range parents[1:] {
						creditedParent = creditedParent || commits[parent]
					}
					if !creditedParent {
						continue
					}
					if originFiles[id] == nil {
						originFiles[id] = map[string]bool{}
						for origin := range commits {
							parents, err := git(tree, "show", "-s", "--format=%P", origin, "--")
							if err != nil {
								return nil, nil, err
							}
							// Sync merges expose main's files against other parents;
							// only non-merge commits define the block's file set.
							if len(strings.Fields(parents)) > 1 {
								continue
							}
							changed, err := changedFiles(origin)
							if err != nil {
								return nil, nil, err
							}
							for _, file := range strings.Split(changed, "\x00") {
								if file != "" {
									originFiles[id][file] = true
								}
							}
						}
					}
					valid := true
					for _, file := range strings.Split(changed, "\x00") {
						if file != "" && !originFiles[id][file] {
							valid = false
							break
						}
					}
					if valid {
						ids = append(ids, id)
					}
				}
				// Only the first-parent delta belongs to this merge, credited or
				// not. Main's changes relative to other parents were already on
				// the old base (B589, issue #36: a plain feature merge must not
				// report owned files that main already had as upstream).
				files = nil
				for _, file := range strings.Split(changed, "\x00") {
					if file != "" && owns(file) {
						files = append(files, file)
					}
				}
			}
			if len(ids) == 0 {
				for _, file := range files {
					upstream[file] = true
				}
				continue
			}
		}
		for _, file := range files {
			if accepted[file] == nil {
				accepted[file] = map[string]bool{}
			}
			for _, id := range ids {
				accepted[file][id] = true
			}
		}
	}
	result := []Accepted{}
	for file, blocks := range accepted {
		ids := []string{}
		for id := range blocks {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		result = append(result, Accepted{file, ids})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].File < result[j].File })
	uncredited := []string{}
	for file := range upstream {
		uncredited = append(uncredited, file)
	}
	sort.Strings(uncredited)
	return result, uncredited, nil
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

// Use git's rebase lifecycle, with exec steps replaying each commit under its own
// original committer. Like Node's replay this preserves authors and refuses merges.
// The editor and todo are private temporary files; no repository config is changed.
func replay(tree, oldBase, newBase, oldHead string) error {
	commits, err := git(tree, "log", "--format=%H%x00%cn%x00%ce", "--reverse", oldBase+".."+oldHead, "--not", newBase)
	if err != nil {
		return err
	}
	identity, err := git(tree, "show", "-s", "--format=%cn%x00%ce", oldHead)
	if err != nil {
		return err
	}
	id := strings.Split(strings.TrimSuffix(identity, "\n"), "\x00")
	if len(id) != 2 {
		return fmt.Errorf("invalid committer")
	}
	todo := ""
	for _, line := range strings.Split(strings.TrimSuffix(commits, "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x00")
		if len(parts) != 3 || strings.ContainsAny(parts[1]+parts[2], "\r\n") {
			return fmt.Errorf("invalid committer")
		}
		todo += "exec env GIT_COMMITTER_NAME=" + quote(parts[1]) + " GIT_COMMITTER_EMAIL=" + quote(parts[2]) + " git -c commit.gpgsign=false cherry-pick --allow-empty " + parts[0] + "\n"
	}
	if todo == "" {
		todo = "noop\n"
	}
	dir, err := os.MkdirTemp("", "forge-rebase-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	todoFile, editor := filepath.Join(dir, "todo"), filepath.Join(dir, "editor")
	if err := os.WriteFile(todoFile, []byte(todo), 0600); err != nil {
		return err
	}
	if err := os.WriteFile(editor, []byte("#!/bin/sh\ncp "+quote(todoFile)+" \"$1\"\n"), 0700); err != nil {
		return err
	}
	env := append(gitEnv(), "GIT_SEQUENCE_EDITOR="+quote(editor), "GIT_EDITOR=true", "GIT_COMMITTER_NAME="+id[0], "GIT_COMMITTER_EMAIL="+id[1])
	_, err = gitWithEnv(tree, env, "-c", UnionOff, "-c", "rebase.updateRefs=false", "rebase", "--interactive", "--no-autosquash", "--no-autostash", "--no-reschedule-failed-exec", "--onto", newBase, oldBase)
	if err != nil {
		// B431: an add-only CHANGELOG.md is merged and the replay goes on;
		// any other stop is a real conflict naming its files.
		err = Continue(tree, gitEnv())
	}
	return err
}

// Run validates and replays on a snapshot of the run record while it holds
// only the block's own lock (B426): the run lock is taken for the final short
// publication, which refuses with block-changed if another verb changed this
// block meanwhile. Other blocks of the run are never blocked by a rebase.
func Run(home, runID, blockID string) (Result, error) { return run(home, runID, blockID, false) }

// RunPlain is the plain `block rebase <id> --run <r>` of an open worktree block
// (B431). It is Run, except that a pending rebase checkpoint signed by
// review-file --rebase-check is verified and finished instead of refused.
func RunPlain(home, runID, blockID string) (Result, error) { return run(home, runID, blockID, true) }

func signerKey(home, runID string) ([]byte, error) {
	keyFile := filepath.Join(home, ".code-forge", "runs", runID+".key")
	info, err := os.Stat(keyFile)
	if os.IsNotExist(err) {
		return nil, refusal("no-key", "run "+runID+" has no signer key")
	}
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, refusal("bad-key-mode", fmt.Sprintf("run %s signer key is accessible to other users (mode %o); expected 600", runID, info.Mode().Perm()))
	}
	key, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, refusal("bad-key", "run "+runID+" signer key is not 32 bytes")
	}
	return key, nil
}

func run(home, runID, blockID string, plain bool) (result Result, err error) {
	err = state.WithBlockLock(home, runID, blockID, func() (runErr error) {
		record, err := state.ReadRun(home, runID)
		if err != nil {
			return err
		}
		if text(record, "status") != "active" {
			return refusal("run-ended", "run "+runID+" has ended")
		}
		blocks := object(record, "blocks")
		block := object(blocks, blockID)
		snapshot := state.CloneObject(block)
		worktree := object(block, "worktree")
		if text(block, "status") != "open" || worktree == nil {
			return refusal("no-block", "block "+blockID+" is not open in run "+runID)
		}
		if text(block, "base_branch") == "" {
			return refusal("rebase-no-base", "block "+blockID+" has no recorded base branch")
		}
		checkpoint := object(block, "rebase_check")
		if block.Get("rebase_check") != nil {
			if !plain {
				return refusal("rebase-check-invalid", "finish the pending rebase checkpoint first")
			}
			key, err := signerKey(home, runID)
			if err != nil {
				return err
			}
			verified, err := sign.VerifyRow(checkpoint.Map(), key)
			if err != nil || !verified.OK || text(checkpoint, "run") != runID || text(checkpoint, "block") != blockID || text(checkpoint, "old_base") != text(block, "base_sha") {
				return refusal("rebase-check-invalid", "the pending rebase checkpoint is invalid")
			}
		}
		tree, workspace := text(worktree, "path"), text(record, "workspace")
		branch, err := git(tree, "symbolic-ref", "--quiet", "HEAD")
		if err != nil || strings.TrimSpace(branch) != "refs/heads/"+text(worktree, "branch") {
			return refusal("worktree-rebase", "worktree is not on its recorded branch")
		}
		status, err := git(tree, "status", "--porcelain")
		if err != nil {
			return err
		}
		if status != "" {
			return refusal("rebase-dirty", "commit or clean first")
		}
		oldHead, err := resolve(tree, "HEAD")
		if err != nil {
			return err
		}
		if checkpoint != nil {
			if _, err := git(tree, "merge-base", "--is-ancestor", text(checkpoint, "head"), oldHead); err != nil {
				return refusal("rebase-check-invalid", "the worktree no longer contains the pending rebase")
			}
		}
		oldBase, err := resolve(tree, text(block, "base_sha"))
		if err != nil {
			return err
		}
		newBase, err := resolve(workspace, "refs/heads/"+text(block, "base_branch"))
		if err != nil {
			return err
		}
		top, err := git(workspace, "rev-parse", "--show-toplevel")
		if err != nil {
			return err
		}
		top = strings.TrimSpace(top)
		ws, err := filepath.EvalSymlinks(workspace)
		if err != nil {
			return err
		}
		prefix, err := filepath.Rel(top, ws)
		if err != nil {
			return err
		}
		prefix = filepath.ToSlash(prefix)
		if prefix == "." {
			prefix = ""
		}
		owned, _ := block.Get("owned_files").([]any)
		kinds := object(block, "owned_kinds")
		owns := func(file string) bool {
			return ownsFile(owned, kinds, file) || prefix != "" && strings.HasPrefix(file, prefix+"/") && ownsFile(owned, kinds, strings.TrimPrefix(file, prefix+"/"))
		}
		accepted, upstream, err := origins(tree, runID, oldBase, newBase, blocks, owns)
		if err != nil {
			return err
		}
		// A credited sibling may have been rewritten before its landing merge.
		// Its pre-land branch can still be an ancestor of this block even though
		// those commit IDs are absent from newBase. Skip a tip only after proving
		// its skipped commits are represented on newBase, not just its sibling tip.
		replayFrom := oldBase
		for _, field := range blocks {
			b, _ := field.Value.(ledger.Object)
			if text(b, "status") != "closed" && text(b, "status") != humancheck.DeferredStatus {
				continue
			}
			base, err := resolve(tree, text(b, "base_sha"))
			if err != nil {
				continue
			}
			bases := []string{base}
			if recorded := text(object(b, "worktree"), "base_sha"); recorded != "" {
				base, err := resolve(tree, recorded)
				if err != nil {
					continue
				}
				bases = append(bases, base)
			}
			tips := []string{}
			landed := false
			for _, branch := range []string{"forge/" + runID + "/" + field.Key, text(object(b, "worktree"), "branch")} {
				if branch == "" {
					continue
				}
				tip, err := resolve(tree, "refs/heads/"+branch)
				if err != nil {
					continue
				}
				// Like origins, exclude history at or before either recorded base.
				commits, err := git(tree, append([]string{"rev-list", "--max-count=1", tip, "--not"}, bases...)...)
				if err != nil {
					return err
				}
				if strings.TrimSpace(commits) == "" {
					continue
				}
				tips = append(tips, tip)
				if _, err := git(tree, "merge-base", "--is-ancestor", tip, newBase); err == nil {
					landed = true
				}
			}
			// File ownership controls acceptance reporting, not inherited history.
			// Require a verified landing even when Y changed no files X owns.
			if !landed {
				continue
			}
			for _, tip := range tips {
				if _, err := git(tree, "merge-base", "--is-ancestor", replayFrom, tip); err != nil {
					continue
				}
				if _, err := git(tree, "merge-base", "--is-ancestor", tip, oldHead); err != nil {
					continue
				}
				patches, err := git(tree, "cherry", newBase, tip, replayFrom)
				if err != nil {
					return err
				}
				unlanded := false
				for _, line := range strings.Split(patches, "\n") {
					if strings.HasPrefix(line, "+ ") {
						unlanded = true
						break
					}
				}
				if unlanded {
					continue
				}
				// Cherry ignores merges, whose resolutions can contain unique work.
				// Leave unrepresented merges for replay to refuse without mutation.
				merges, err := git(tree, "rev-list", "--merges", replayFrom+".."+tip, "--not", newBase)
				if err != nil {
					return err
				}
				if strings.TrimSpace(merges) != "" {
					continue
				}
				replayFrom = tip
			}
		}
		// No mutation occurs until all origin checks pass. Every failure from
		// replay through publication must restore the exact original HEAD.
		defer func() {
			if runErr != nil {
				if _, resetErr := git(tree, "reset", "--hard", oldHead); resetErr != nil {
					runErr = fmt.Errorf("%w; restore HEAD: %v", runErr, resetErr)
				}
			}
		}()
		if err := replay(tree, replayFrom, newBase, oldHead); err != nil {
			conflict, ok := err.(*ConflictError)
			if !ok || len(conflict.Files) == 0 {
				conflict = &ConflictError{Files: Conflicts(tree)}
			}
			_, _ = git(tree, "cherry-pick", "--abort")
			_, _ = git(tree, "rebase", "--abort")
			return refusal("rebase-conflict", ConflictMessage(blockID, conflict))
		}
		changed, err := git(tree, "diff", "--no-renames", "--name-only", "-z", oldHead, "HEAD", "--")
		if err != nil {
			return err
		}
		idx := queue.Index{Dir: filepath.Join(top, ".code-forge", "queue")}
		if err := idx.Refresh(); err != nil {
			return err
		}
		realTree, err := filepath.EvalSymlinks(tree)
		if err != nil {
			return err
		}
		var revision *string
		if value, ok := block.Get("acceptance_revision").(string); ok {
			revision = &value
		}
		pending := []ticket.Parts{}
		for _, file := range strings.Split(changed, "\x00") {
			if file == "" || !owns(file) {
				continue
			}
			hash, err := contentHash(realTree, file)
			if err != nil {
				return err
			}
			p := ticket.Parts{Run: runID, Block: blockID, File: file, ContentHash: hash, Tree: &realTree, AcceptanceRevision: revision}
			// Ordinary Node enqueues reuse the latest matching reopened attempt.
			for _, t := range idx.Block(runID, blockID, &realTree, revision) {
				if t["file"] == file && t["content_hash"] == hash {
					if at, ok := t["reopened_at"].(string); ok {
						p.ReopenedAt = &at
					}
				}
			}
			pending = append(pending, p)
		}
		// Prepare every hash and ticket before queue publication; signed state
		// publication is last, in the same WriteSigned/SaveRun order as Node.
		for _, p := range pending {
			if err := idx.Ensure(p); err != nil {
				return err
			}
		}
		block.Set("base_sha", newBase)
		block.Delete("rebase_check")
		worktree.Set("base_sha", newBase)
		block.Set("worktree", worktree)
		result = Result{Block: blockID, Event: "rebased", OldBase: oldBase, NewBase: newBase, Accepted: accepted, Upstream: upstream}
		row := ledger.Object{{Key: "event", Value: "block.rebase"}, {Key: "block", Value: blockID}, {Key: "old_base", Value: oldBase}, {Key: "new_base", Value: newBase}, {Key: "worktree", Value: true}, {Key: "accepted_landed", Value: accepted}, {Key: "upstream_owned", Value: upstream}}
		return state.CommitBlocks(home, runID, map[string]ledger.Object{blockID: snapshot}, map[string]ledger.Object{blockID: block}, row)
	})
	return result, err
}

func contentHash(tree, file string) (string, error) {
	// Check every component so a symlink cannot redirect review content outside
	// the worktree. Git tracks only regular files, executable files and symlinks.
	current := tree
	parts := strings.Split(file, "/")
	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("bad-path: %s", file)
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return "deleted", nil
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || i == len(parts)-1 && !info.Mode().IsRegular() {
			return "", fmt.Errorf("bad-path: %s", file)
		}
	}
	data, err := os.ReadFile(current)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}
