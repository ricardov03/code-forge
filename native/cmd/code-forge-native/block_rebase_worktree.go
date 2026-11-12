package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
	"github.com/ricardov03/code-forge/native/queue"
	"github.com/ricardov03/code-forge/native/sign"
	"github.com/ricardov03/code-forge/native/state"
	"github.com/ricardov03/code-forge/native/ticket"
)

// B449: Node's rebaseWorktreeBlock (src/state/block.mjs) for a closed or
// awaiting worktree block, and for --accept-changed. The plain rebase of an
// open worktree block stays rebase.RunPlain (B431).

var (
	errRebaseUnported = errors.New("rebase state requires Node")
	errRebaseExitOne  = errors.New("git exit 1")
)

func rebaseOwns(paths []any, kinds ledger.Object, file string) bool {
	return owned.OwnsFile(paths, kinds, file)
}

// rebaseNodeGit is block.mjs git(): Node's scrubbed git environment and its
// `git <sub> failed (exit N)` refusal. An exit code listed in ok returns
// errRebaseExitOne (only 1 is ever listed).
func rebaseNodeGit(cwd string, ok []int, args ...string) (string, error) {
	out, err := gitx.WithEnv(cwd, gitx.ChildEnv(), args...)
	if err == nil {
		return out, nil
	}
	var e *gitx.CommandError
	if errors.As(err, &e) {
		if slices.Contains(ok, e.Code) {
			return out, errRebaseExitOne
		}
		return "", &state.Error{Code: "git-failed", Message: fmt.Sprintf("git %s failed (exit %d)", args[0], e.Code)}
	}
	return "", err
}

func rebaseTime(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, s)
	return t, err == nil
}

// rebaseBlobs is committedBlobs: blob ids at ref, "" for a deletion.
func rebaseBlobs(tree, ref string, files []string) (map[string]string, error) {
	hashes := map[string]string{}
	for _, file := range files {
		hashes[file] = ""
	}
	if len(files) == 0 {
		return hashes, nil
	}
	out, err := rebaseNodeGit(tree, nil, append([]string{"ls-tree", "-r", "-z", "--full-tree", ref, "--"}, files...)...)
	if err != nil {
		return nil, err
	}
	for _, entry := range strings.Split(out, "\x00") {
		meta, file, found := strings.Cut(entry, "\t")
		if _, known := hashes[file]; found && known {
			if fields := strings.Split(meta, " "); len(fields) == 3 {
				hashes[file] = fields[2]
			}
		}
	}
	return hashes, nil
}

// rebaseTicket is reviewIdentity's ticket, read-only: no marker is published.
func rebaseTicket(idx *queue.Index, p ticket.Parts, after string) string {
	matching := []queue.Object{}
	for _, t := range idx.Block(p.Run, p.Block, p.Tree, p.AcceptanceRevision) {
		if t["file"] == p.File && t["content_hash"] == p.ContentHash {
			matching = append(matching, t)
		}
	}
	var latest, fresh queue.Object
	afterTime, afterOK := rebaseTime(after)
	for _, t := range matching {
		if _, ok := t["reopened_at"].(string); ok {
			latest = t
		}
		if stamp, ok := rebaseTime(blockString(t["enqueued_at"])); ok && afterOK && stamp.After(afterTime) {
			fresh = t
		}
	}
	switch {
	case fresh != nil:
		if at, ok := fresh["reopened_at"].(string); ok {
			p.ReopenedAt = &at
		}
	case afterOK:
		p.ReopenedAt = &after
	case latest != nil:
		at := latest["reopened_at"].(string)
		p.ReopenedAt = &at
	}
	return ticket.ReviewID(p)
}

func rebaseWorktreeBlock(home, run, id string, accept []string, reason string) (oldBase, newBase string, err error) {
	initial, err := state.ReadRun(home, run)
	if err != nil {
		return "", "", err
	}
	top, err := gitx.RepoTop(blockString(initial.Get("workspace")))
	if err != nil {
		return "", "", &rebaseHandoverError{hRebasePath}
	}
	idx := &queue.Index{Dir: filepath.Join(top, ".code-forge", "queue")}
	if idx.Refresh() != nil {
		return "", "", &rebaseHandoverError{hRebaseQueue}
	}
	err = state.WithRunLock(home, run, func() (runErr error) {
		record, err := state.ReadRun(home, run)
		if err != nil {
			return err
		}
		if blockString(record.Get("status")) != "active" {
			return &state.Error{Code: "run-ended", Message: "run " + run + " has ended"}
		}
		blocks := blockObject(record.Get("blocks"))
		b := blockObject(blocks.Get(id))
		status := blockString(b.Get("status"))
		wt := blockObject(b.Get("worktree"))
		if (status != "open" && status != "closed" && status != "awaiting_human_check") || b.Get("worktree") == nil {
			return &state.Error{Code: "no-block", Message: fmt.Sprintf("block %s is not open in run %s", id, run)}
		}
		if wt == nil {
			return &rebaseHandoverError{hRebaseRecordShape}
		}
		closed := status != "open"
		if blockString(b.Get("base_branch")) == "" {
			return &state.Error{Code: "rebase-no-base", Message: "block " + id + " has no recorded base branch"}
		}
		tree, workspace := blockString(wt.Get("path")), blockString(record.Get("workspace"))
		branch, err := rebaseNodeGit(tree, []int{1}, "symbolic-ref", "--quiet", "HEAD")
		if err != nil && !errors.Is(err, errRebaseExitOne) {
			return err
		}
		if strings.TrimSpace(branch) != "refs/heads/"+blockString(wt.Get("branch")) {
			return &state.Error{Code: "worktree-rebase", Message: fmt.Sprintf("block %s: its worktree is not on branch %s", id, blockString(wt.Get("branch")))}
		}
		dirty, err := rebaseNodeGit(tree, nil, "status", "--porcelain")
		if err != nil {
			return err
		}
		if dirty != "" {
			if closed {
				return &state.Error{Code: "rebase-conflict", Message: "rebase-conflict"}
			}
			return &state.Error{Code: "rebase-dirty", Message: "rebase-dirty: block " + id + ": commit or clean first"}
		}
		oldHead, err := rebaseNodeGit(tree, nil, "rev-parse", "HEAD^{commit}")
		if err != nil {
			return err
		}
		oldHead = strings.TrimSpace(oldHead)
		key, err := rebaseKey(home, run)
		if errors.Is(err, errRebaseUnported) {
			return &rebaseHandoverError{hRebaseKey}
		}
		if err != nil {
			return err
		}
		// B578: Node tests the checkpoint for truth: a falsy one is no
		// checkpoint, any other value that is not a signed row is invalid.
		check := b.Get("rebase_check")
		pending := blockObject(check)
		base := blockString(b.Get("base_sha"))
		if jsTruthy(check, true) {
			if pending == nil {
				return &state.Error{Code: "rebase-check-invalid", Message: "the pending rebase checkpoint is invalid"}
			}
			verified, e := sign.VerifyRow(pending.Map(), key)
			if e != nil || !verified.OK || pending.Get("run") != run || pending.Get("block") != id || pending.Get("old_base") != base {
				return &state.Error{Code: "rebase-check-invalid", Message: "the pending rebase checkpoint is invalid"}
			}
			if _, err := rebaseNodeGit(tree, []int{1}, "merge-base", "--is-ancestor", blockString(pending.Get("head")), oldHead); err != nil {
				if errors.Is(err, errRebaseExitOne) {
					return &state.Error{Code: "rebase-check-invalid", Message: "the worktree no longer contains the pending rebase"}
				}
				return err
			}
		}
		reviewedHead := oldHead
		if pending != nil {
			reviewedHead = blockString(pending.Get("old_head"))
		}
		resolved, err := rebaseNodeGit(workspace, nil, "rev-parse", "refs/heads/"+blockString(b.Get("base_branch"))+"^{commit}")
		if err != nil {
			return err
		}
		resolved = strings.TrimSpace(resolved)
		realWorkspace, err := filepath.EvalSymlinks(workspace)
		if err != nil {
			return &rebaseHandoverError{hRebasePath}
		}
		prefix, err := filepath.Rel(top, realWorkspace)
		if err != nil {
			return &rebaseHandoverError{hRebasePath}
		}
		if prefix == "." {
			prefix = ""
		}
		prefix = filepath.ToSlash(prefix)
		paths, _ := b.Get("owned_files").([]any)
		kinds := blockObject(b.Get("owned_kinds"))
		owns := func(file string) bool {
			return owned.OwnsFile(paths, kinds, file) || prefix != "" && strings.HasPrefix(file, prefix+"/") && owned.OwnsFile(paths, kinds, strings.TrimPrefix(file, prefix+"/"))
		}
		files := []string{}
		if closed {
			diff, err := rebaseNodeGit(tree, nil, "diff", "--no-renames", "--name-only", "-z", base, reviewedHead, "--")
			if err != nil {
				return err
			}
			for _, file := range strings.Split(diff, "\x00") {
				if file != "" && owns(file) {
					files = append(files, file)
				}
			}
			sort.Strings(files)
		} else {
			upstream, err := rebaseNodeGit(tree, nil, "log", "--format=", "--no-renames", "--name-only", "-z", "-m", base+".."+resolved, "--")
			if err != nil {
				return err
			}
			conflicts := []string{}
			for _, file := range strings.Split(upstream, "\x00") {
				if file != "" && owns(file) && !slices.Contains(conflicts, file) {
					conflicts = append(conflicts, file)
				}
			}
			sort.Strings(conflicts)
			if len(conflicts) > 0 {
				return &state.Error{Code: "owned-conflict", Message: fmt.Sprintf("block %s: commits since its base changed owned files: %s", id, strings.Join(conflicts, ", "))}
			}
		}
		var before map[string]string
		if closed {
			if before, err = rebaseBlobs(tree, reviewedHead, files); err != nil {
				return err
			}
		}
		// From here a failure aborts a conflicted replay and restores the
		// exact old head (the tree was clean before).
		defer func() {
			if runErr != nil {
				_, _ = gitx.WithEnv(workspace, gitx.ChildEnv(), "-C", tree, "cherry-pick", "--abort")
				_, _ = rebaseNodeGit(tree, nil, "reset", "--hard", oldHead)
			}
		}()
		conflict := &state.Error{Code: "rebase-conflict", Message: "rebase-conflict"}
		commits, err := rebaseNodeGit(tree, nil, "log", "--format=%H%x00%cn%x00%ce", "--reverse", base+".."+oldHead, "--not", resolved)
		if err != nil {
			return conflict
		}
		if _, err := rebaseNodeGit(tree, nil, "reset", "--hard", resolved); err != nil {
			return conflict
		}
		for _, line := range strings.Split(strings.TrimRight(commits, " \t\r\n"), "\n") {
			if line == "" {
				continue
			}
			parts := strings.Split(line, "\x00")
			if len(parts) != 3 {
				return conflict
			}
			env := append(gitx.ChildEnv(), "GIT_COMMITTER_NAME="+parts[1], "GIT_COMMITTER_EMAIL="+parts[2])
			if _, err := gitx.WithEnv(workspace, env, "-C", tree, "cherry-pick", "--allow-empty", parts[0]); err != nil {
				return conflict
			}
		}
		changed := []string{}
		if closed {
			after, err := rebaseBlobs(tree, "HEAD", files)
			if err != nil {
				return err
			}
			for _, file := range files {
				if before[file] != after[file] {
					changed = append(changed, file)
				}
			}
		}
		invalid := []string{}
		for _, file := range accept {
			if !slices.Contains(changed, file) {
				invalid = append(invalid, file)
			}
		}
		if len(invalid) > 0 {
			return &state.Error{Code: "usage", Message: "--accept-changed names files not changed by this rebase: " + strings.Join(invalid, ", ")}
		}
		rows := []map[string]any{}
		if pending != nil {
			all, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge", "ledger"), blockString(record.Get("project")))
			if err != nil {
				return &rebaseHandoverError{hRebaseLedger}
			}
			started, startedOK := rebaseTime(blockString(pending.Get("started_at")))
			for _, value := range all {
				row, ok := value.(map[string]any)
				if !ok || row["run"] != run || row["block"] != id {
					continue
				}
				verified, e := sign.VerifyRow(row, key)
				ts, tsOK := rebaseTime(blockString(row["ts"]))
				if e == nil && verified.OK && tsOK && startedOK && !ts.Before(started) {
					rows = append(rows, row)
				}
			}
		}
		realTree, err := filepath.EvalSymlinks(tree)
		if err != nil {
			return &rebaseHandoverError{hRebasePath}
		}
		cleared := []any{}
		clearedFiles := map[string]bool{}
		for _, file := range changed {
			if slices.Contains(accept, file) {
				cleared = append(cleared, ledger.Object{{Key: "file", Value: file}, {Key: "via", Value: "human"}, {Key: "reason", Value: reason}})
				clearedFiles[file] = true
				continue
			}
			hash, err := gitx.ContentHash(realTree, file)
			if err != nil {
				return &rebaseHandoverError{hRebaseContentHash}
			}
			p := ticket.Parts{Run: run, Block: id, File: file, ContentHash: hash, Tree: &realTree}
			if revision, ok := b.Get("acceptance_revision").(string); ok {
				p.AcceptanceRevision = &revision
			}
			ticketID := rebaseTicket(idx, p, blockString(pending.Get("started_at")))
			relevant := []map[string]any{}
			for _, row := range rows {
				if row["file"] == file && row["content_hash"] == hash {
					relevant = append(relevant, row)
				}
			}
			approval := -1
			for i, row := range relevant {
				if t, has := row["ticket"]; row["event"] == "review.approved" && (!has || t == ticketID) {
					approval = i
				}
			}
			denied := false
			for _, row := range relevant[approval+1:] {
				open, _ := row["open_after"].(float64)
				if row["event"] == "review.cap" || row["event"] == "review.result" && row["approved"] == false || row["event"] == "review.round" && open > 0 {
					denied = true
				}
			}
			if approval >= 0 && !denied {
				cleared = append(cleared, ledger.Object{{Key: "file", Value: file}, {Key: "via", Value: "review"}, {Key: "ticket", Value: ticketID}})
				clearedFiles[file] = true
			}
		}
		uncleared := []string{}
		for _, file := range changed {
			if !clearedFiles[file] {
				uncleared = append(uncleared, file)
			}
		}
		if len(uncleared) > 0 {
			return &state.Error{Code: "rebase-changed-reviewed", Message: fmt.Sprintf("rebase-changed-reviewed %s\nRe-review: code-forge review-file --rebase-check --block %s --run %s\nHuman accept (repeat --accept-changed for each file): code-forge block rebase %s --run %s --accept-changed <file> --reason <text>", strings.Join(uncleared, ", "), id, run, id, run)}
		}
		b.Set("base_sha", resolved)
		wt.Set("base_sha", resolved)
		b.Set("worktree", wt)
		b.Delete("rebase_check")
		blocks.Set(id, b)
		record.Set("blocks", blocks)
		reviewed := []any{}
		for _, file := range changed {
			reviewed = append(reviewed, file)
		}
		row := ledger.Object{{Key: "event", Value: "block.rebase"}, {Key: "block", Value: id}, {Key: "old_base", Value: base}, {Key: "new_base", Value: resolved}, {Key: "changed_reviewed", Value: reviewed}, {Key: "cleared", Value: cleared}}
		if closed {
			row.Set("closed", true)
		} else {
			row.Set("worktree", true)
		}
		if _, err := state.WriteSigned(home, run, row); err != nil {
			return err
		}
		if err := state.SaveRun(home, record); err != nil {
			return err
		}
		oldBase, newBase = base, resolved
		return nil
	})
	return oldBase, newBase, err
}

// rebaseKey is Node's loadKey with its three refusals.
func rebaseKey(home, run string) ([]byte, error) {
	keyFile := filepath.Join(home, ".code-forge", "runs", run+".key")
	info, err := os.Stat(keyFile)
	if os.IsNotExist(err) {
		return nil, &state.Error{Code: "no-key", Message: "run " + run + " has no signer key"}
	}
	if err != nil {
		return nil, errRebaseUnported
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, &state.Error{Code: "bad-key-mode", Message: fmt.Sprintf("run %s signer key is accessible to other users (mode %o); expected 600", run, info.Mode().Perm())}
	}
	key, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, errRebaseUnported
	}
	if len(key) != 32 {
		return nil, &state.Error{Code: "bad-key", Message: "run " + run + " signer key is not 32 bytes"}
	}
	return key, nil
}
