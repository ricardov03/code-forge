package reviewq

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

var ErrUnsupported = errors.New("review enqueue state requires Node")

func rebaseText(o ledger.Object, key string) string { s, _ := o.Get(key).(string); return s }
func rebaseObject(o ledger.Object, key string) ledger.Object {
	v, _ := o.Get(key).(ledger.Object)
	return v
}
func rebaseError(code, message string) error { return &state.Error{Code: code, Message: message} }
func rebaseGit(tree string, args ...string) (string, error) {
	out, err := gitx.WithEnv("", gitx.ChildEnv(), append([]string{"-C", tree, "-c", "commit.gpgsign=false"}, args...)...)
	if err != nil {
		if e, ok := err.(*gitx.CommandError); ok {
			if args[0] == "symbolic-ref" && e.Code == 1 {
				return "", nil
			}
			return "", rebaseError("git-failed", fmt.Sprintf("git %s failed (exit %d)", args[0], e.Code))
		}
		return "", err
	}
	return strings.TrimSuffix(out, "\n"), err
}

func rebaseBlob(tree, ref, file string) (string, error) {
	out, err := rebaseGit(tree, "ls-tree", "-r", "-z", "--full-tree", ref, "--", file)
	if err != nil {
		return "", err
	}
	for _, entry := range strings.Split(out, "\x00") {
		meta, name, found := strings.Cut(entry, "\t")
		if found && name == file {
			fields := strings.Split(meta, " ")
			if len(fields) == 3 {
				return fields[2], nil
			}
		}
	}
	return "", nil
}

// RebaseCheck retains the candidate HEAD and signs its checkpoint while leaving
// both recorded bases untouched. It never emits a ledger row. Unsupported
// representations are rejected before taking a lock or changing the tree.
func RebaseCheck(home, root, run, id string, exclude func(string) (string, bool)) (result ledger.Object, err error) {
	initial, err := state.ReadRun(home, run)
	if err != nil {
		return nil, err
	}
	block := rebaseObject(rebaseObject(initial, "blocks"), id)
	if block.Get("worktree") == nil {
		return nil, rebaseError("no-block", fmt.Sprintf("block %s is not open in run %s", id, run))
	}
	idx := queue.Index{Dir: filepath.Join(root, ".code-forge", "queue")}
	if err := idx.Refresh(); err != nil {
		return nil, ErrUnsupported
	}
	// The run snapshot must be readable in the same form during publication.
	if block.Get("owned_files") != nil {
		if _, ok := block.Get("owned_files").([]any); !ok {
			return nil, ErrUnsupported
		}
	}
	// B426: only the block lock is held through the git work; the run lock
	// guards the final short publication of this block alone.
	err = state.WithBlockLock(home, run, id, func() (runErr error) {
		record, err := state.ReadRun(home, run)
		if err != nil {
			return err
		}
		if rebaseText(record, "status") != "active" {
			return rebaseError("run-ended", "run "+run+" has ended")
		}
		blocks := rebaseObject(record, "blocks")
		b := rebaseObject(blocks, id)
		snapshot := state.CloneObject(b)
		wt := rebaseObject(b, "worktree")
		status := rebaseText(b, "status")
		if (status != "open" && status != "closed" && status != "awaiting_human_check") || wt == nil {
			return rebaseError("no-block", fmt.Sprintf("block %s is not open in run %s", id, run))
		}
		closed := status != "open"
		if rebaseText(b, "base_branch") == "" {
			return rebaseError("rebase-no-base", "block "+id+" has no recorded base branch")
		}
		tree := rebaseText(wt, "path")
		branch, err := rebaseGit(tree, "symbolic-ref", "--quiet", "HEAD")
		if err != nil {
			return err
		}
		if branch != "refs/heads/"+rebaseText(wt, "branch") {
			return rebaseError("worktree-rebase", fmt.Sprintf("block %s: its worktree is not on branch %s", id, rebaseText(wt, "branch")))
		}
		dirty, err := rebaseGit(tree, "status", "--porcelain")
		if err != nil {
			return err
		}
		if dirty != "" {
			if closed {
				return rebaseError("rebase-conflict", "rebase-conflict")
			}
			return rebaseError("rebase-dirty", "rebase-dirty: block "+id+": commit or clean first")
		}
		oldHead, err := rebaseGit(tree, "rev-parse", "--verify", "HEAD^{commit}")
		if err != nil {
			return err
		}
		keyFile := filepath.Join(home, ".code-forge", "runs", run+".key")
		info, err := os.Stat(keyFile)
		if os.IsNotExist(err) {
			return rebaseError("no-key", "run "+run+" has no signer key")
		}
		if err != nil {
			return err
		}
		if info.Mode().Perm()&0077 != 0 {
			return rebaseError("bad-key-mode", fmt.Sprintf("run %s signer key is accessible to other users (mode %o); expected 600", run, info.Mode().Perm()))
		}
		key, err := os.ReadFile(keyFile)
		if err != nil {
			return err
		}
		if len(key) != 32 {
			return rebaseError("bad-key", "run "+run+" signer key is not 32 bytes")
		}
		pending := rebaseObject(b, "rebase_check")
		if b.Get("rebase_check") != nil {
			verified, e := sign.VerifyRow(pending.Map(), key)
			if e != nil || !verified.OK || pending.Get("run") != run || pending.Get("block") != id || pending.Get("old_base") != b.Get("base_sha") {
				return rebaseError("rebase-check-invalid", "the pending rebase checkpoint is invalid")
			}
			if _, err := rebaseGit(tree, "merge-base", "--is-ancestor", rebaseText(pending, "head"), oldHead); err != nil {
				return rebaseError("rebase-check-invalid", "the worktree no longer contains the pending rebase")
			}
		}
		reviewedHead := oldHead
		if pending != nil {
			reviewedHead = rebaseText(pending, "old_head")
		}
		oldBase := rebaseText(b, "base_sha")
		workspace := rebaseText(record, "workspace")
		newBase, err := rebaseGit(workspace, "rev-parse", "--verify", "refs/heads/"+rebaseText(b, "base_branch")+"^{commit}")
		if err != nil {
			return err
		}
		top, err := gitx.RepoTop(workspace)
		if err != nil {
			return err
		}
		realWorkspace, err := filepath.EvalSymlinks(workspace)
		if err != nil {
			return err
		}
		prefix, err := filepath.Rel(top, realWorkspace)
		if err != nil {
			return err
		}
		if prefix == "." {
			prefix = ""
		}
		prefix = filepath.ToSlash(prefix)
		paths, _ := b.Get("owned_files").([]any)
		kinds := rebaseObject(b, "owned_kinds")
		owns := func(file string) bool {
			return owned.OwnsFile(paths, kinds, file) || prefix != "" && strings.HasPrefix(file, prefix+"/") && owned.OwnsFile(paths, kinds, strings.TrimPrefix(file, prefix+"/"))
		}
		files := []string{}
		if closed {
			diff, err := rebaseGit(tree, "diff", "--no-renames", "--name-only", "-z", oldBase, reviewedHead, "--")
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
			upstream, err := rebaseGit(tree, "log", "--format=", "--no-renames", "--name-only", "-z", "-m", oldBase+".."+newBase, "--")
			if err != nil {
				return err
			}
			conflicts := map[string]bool{}
			for _, file := range strings.Split(upstream, "\x00") {
				if file != "" && owns(file) {
					conflicts[file] = true
				}
			}
			if len(conflicts) > 0 {
				names := []string{}
				for file := range conflicts {
					names = append(names, file)
				}
				sort.Strings(names)
				return rebaseError("owned-conflict", "block "+id+": commits since its base changed owned files: "+strings.Join(names, ", "))
			}
		}
		before := map[string]string{}
		excluded := map[string]string{}
		for _, file := range files {
			rule, supported := exclude(file)
			if !supported {
				return ErrUnsupported
			}
			excluded[file] = rule
			blob, e := rebaseBlob(tree, reviewedHead, file)
			if e != nil {
				return e
			}
			before[file] = blob
		}
		commits, err := rebaseGit(tree, "log", "--format=%H%x00%cn%x00%ce", "--reverse", oldBase+".."+oldHead, "--not", newBase)
		if err != nil {
			return err
		}
		// From here onward failures are native refusals, with exact HEAD rollback.
		defer func() {
			if runErr != nil {
				_, _ = rebaseGit(tree, "cherry-pick", "--abort")
				_, _ = rebaseGit(tree, "reset", "--hard", oldHead)
			}
		}()
		if _, err := rebaseGit(tree, "reset", "--hard", newBase); err != nil {
			return rebaseError("rebase-conflict", "rebase-conflict")
		}
		for _, line := range strings.Split(commits, "\n") {
			if line == "" {
				continue
			}
			parts := strings.Split(line, "\x00")
			if len(parts) != 3 {
				return rebaseError("rebase-conflict", "rebase-conflict")
			}
			env := append(gitx.ChildEnv(), "GIT_COMMITTER_NAME="+parts[1], "GIT_COMMITTER_EMAIL="+parts[2])
			if _, err := gitx.WithEnv("", env, "-C", tree, "-c", "commit.gpgsign=false", "cherry-pick", "--allow-empty", parts[0]); err != nil {
				return rebaseError("rebase-conflict", "rebase-conflict")
			}
		}
		changed := []string{}
		for _, file := range files {
			blob, e := rebaseBlob(tree, "HEAD", file)
			if e != nil {
				return e
			}
			if blob != before[file] {
				changed = append(changed, file)
			}
		}
		head, err := rebaseGit(tree, "rev-parse", "--verify", "HEAD^{commit}")
		if err != nil {
			return err
		}
		started := rebaseText(pending, "started_at")
		if started == "" {
			started = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
		}
		checkpoint := ledger.Object{{Key: "event", Value: "block.rebase_check"}, {Key: "run", Value: run}, {Key: "block", Value: id}, {Key: "old_base", Value: oldBase}, {Key: "new_base", Value: newBase}, {Key: "old_head", Value: reviewedHead}, {Key: "head", Value: head}, {Key: "started_at", Value: started}}
		signed, err := sign.SignRow(checkpoint.Map(), key)
		if err != nil {
			return err
		}
		checkpoint.Set("ts", signed["ts"])
		checkpoint.Set("mac", signed["mac"])
		b.Set("rebase_check", checkpoint)
		realTree, err := filepath.EvalSymlinks(tree)
		if err != nil {
			return err
		}
		queued := []any{}
		for _, file := range changed {
			if rule := excluded[file]; rule != "" {
				queued = append(queued, ledger.Object{{Key: "status", Value: "excluded"}, {Key: "file", Value: file}, {Key: "rule", Value: rule}})
				continue
			}
			hash, e := gitx.ContentHash(realTree, file)
			if e != nil {
				return e
			}
			p := ticket.Parts{Run: run, Block: id, File: file, ContentHash: hash, Tree: &realTree}
			if revision, ok := b.Get("acceptance_revision").(string); ok {
				p.AcceptanceRevision = &revision
			}
			if e = idx.Refresh(); e != nil {
				return e
			}
			p, e = idx.ResolveAfter(p, started)
			if e != nil {
				return e
			}
			ticketID := ticket.ReviewID(p)
			if e := queue.Publish(idx.Dir, p, ticketID, time.Now()); e != nil {
				return e
			}
			status := "queued"
			if _, e := os.Stat(filepath.Join(idx.Dir, ticketID+".done")); e == nil {
				status = "done"
			}
			row := ledger.Object{{Key: "ticket", Value: ticketID}, {Key: "status", Value: status}, {Key: "file", Value: file}, {Key: "content_hash", Value: hash}, {Key: "tree", Value: realTree}}
			if p.AcceptanceRevision != nil {
				row.Set("acceptance_revision", *p.AcceptanceRevision)
			}
			if p.ReopenedAt != nil {
				row.Set("reopened_at", *p.ReopenedAt)
			}
			queued = append(queued, row)
		}
		if err := state.CommitBlocks(home, run, map[string]ledger.Object{id: snapshot}, map[string]ledger.Object{id: b}); err != nil {
			return err
		}
		result = ledger.Object{{Key: "old_base", Value: oldBase}, {Key: "new_base", Value: newBase}, {Key: "files", Value: queued}, {Key: "pending", Value: true}}
		return nil
	})
	return result, err
}
