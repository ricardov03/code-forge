package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
	"github.com/ricardov03/code-forge/native/queue"
	"github.com/ricardov03/code-forge/native/reviewq"
	"github.com/ricardov03/code-forge/native/state"
	"github.com/ricardov03/code-forge/native/ticket"
)

func init() { routes["review-file"] = runReviewFileWait }

func reviewEnqueueRebase(args []string, flags map[string]string, paths []string, root string) int {
	if len(paths) != 0 || flags["block"] == "" || flags["run"] == "" {
		return reviewWaitUsageError(args, "--rebase-check takes --block and --run only")
	}
	for _, key := range []string{"wait", "wait-block", "max", "answers", "of", "fresh"} {
		if _, ok := flags[key]; ok {
			return reviewWaitUsageError(args, "--rebase-check takes --block and --run only")
		}
	}
	block, run := flags["block"], flags["run"]
	if !reviewBlockID.MatchString(block) {
		return reviewWaitUsageError(args, "block id must match /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/")
	}
	if !reviewBlockRun.MatchString(run) {
		return reviewEnqueueSay(args, reviewEnqueueRefusal("bad-run-id", "run id must match /^[a-z0-9][a-z0-9-]{0,63}$/"), 1)
	}
	w, ok := reviewEnqueueWorker(root)
	if !ok {
		return reviewWaitHandover(args, hEnqueueWorker)
	}
	if w == nil || w.Get("run") != run {
		return reviewEnqueueSay(args, ledger.Object{{Key: "status", Value: "worker_down"}, {Key: "block", Value: block}}, 3)
	}
	home := os.Getenv("HOME")
	r, ok := reviewEnqueueRecord(home, root, run)
	if !ok {
		return reviewWaitHandover(args, hEnqueueIdentityRecord)
	}
	if r != nil {
		workspace, _ := r.Get("workspace").(string)
		top, ok := reviewWaitRoot(context.Background(), workspace)
		if !ok {
			return reviewWaitHandover(args, hEnqueueRoot)
		}
		if top != root {
			return reviewEnqueueSay(args, reviewEnqueueRefusal("other-workspace", "the run belongs to another workspace"), 1)
		}
		blocks, _ := r.Get("blocks").(ledger.Object)
		entry, _ := blocks.Get(block).(ledger.Object)
		if entry.Get("worktree") == nil && entry.Get("status") != "closed" && entry.Get("status") != "awaiting_human_check" {
			return reviewWaitUsageError(args, "rebase clearance requires a worktree block")
		}
	}
	result, err := reviewq.RebaseCheck(home, root, run, block, func(file string) (string, bool) { return reviewEnqueueExclusion(home, root, run, file) })
	if err == reviewq.ErrUnsupported {
		return reviewWaitHandover(args, hEnqueueRebaseCheck)
	}
	if err != nil {
		if e, ok := err.(*state.Error); ok {
			if e.Code == "usage" {
				return reviewWaitUsageError(args, e.Message)
			}
			return reviewEnqueueSay(args, reviewEnqueueRefusal(e.Code, e.Message), 1)
		}
		return reviewEnqueueSay(args, reviewEnqueueRefusal("error", err.Error()), 1)
	}
	out := ledger.Object{{Key: "block", Value: block}, {Key: "status", Value: "queued"}}
	for _, field := range result {
		out.Set(field.Key, field.Value)
	}
	out.Set("next", fmt.Sprintf("code-forge block rebase %s --run %s", block, run))
	return reviewEnqueueSay(args, out, 0)
}

func reviewEnqueueRefusal(reason, message string) ledger.Object {
	return ledger.Object{{Key: "status", Value: "refused"}, {Key: "reason", Value: reason}, {Key: "message", Value: message}}
}

var reviewAnswerID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)

func reviewEnqueueAnswersFlags(flags map[string]string) ([]string, string) {
	if flags["of"] == "" {
		return nil, "--answers needs --of <source>"
	}
	raw, exists := flags["answers"]
	if !exists {
		return nil, "--answers needs finding ids"
	}
	ids := strings.Split(raw, ",")
	if len(ids) > 10 {
		return nil, "--answers takes at most 10 finding ids"
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return nil, "--answers names a finding id twice"
		}
		seen[id] = true
	}
	for _, id := range ids {
		if !reviewAnswerID.MatchString(id) {
			return nil, "a finding id is letters, digits, . _ : - (at most 64)"
		}
	}
	sort.Strings(ids)
	return ids, ""
}

func reviewEnqueueAnswers(home, root, run, block, tree, file, byFile string, ids []string) (ledger.Object, int, bool) {
	record, err := state.ReadRun(home, run)
	if err == nil {
		blocks, _ := record.Get("blocks").(ledger.Object)
		entry, _ := blocks.Get(block).(ledger.Object)
		if entry != nil {
			paths, _ := entry.Get("owned_files").([]any)
			kinds, _ := entry.Get("owned_kinds").(ledger.Object)
			if !owned.OwnsFile(paths, kinds, file) || !owned.OwnsFile(paths, kinds, byFile) {
				return reviewEnqueueRefusal("answers-not-owned", fmt.Sprintf("block %s does not own both %s and %s; nothing was queued", block, byFile, file)), 1, true
			}
		}
	}
	hash, err := gitx.ContentHash(tree, file)
	if err != nil {
		return nil, 0, false
	}
	byHash, err := gitx.ContentHash(tree, byFile)
	if err != nil {
		return nil, 0, false
	}
	p := ticket.Parts{Run: run, Block: block, File: file, ContentHash: hash}
	if tree != root {
		p.Tree = &tree
	}
	id := queue.AnswersID(p, byFile, byHash, ids)
	if record != nil {
		blocks, _ := record.Get("blocks").(ledger.Object)
		if entry, _ := blocks.Get(block).(ledger.Object); entry != nil {
			slug, _ := record.Get("project").(string)
			rows, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge", "ledger"), slug)
			if err == nil {
				missing, supported := reviewq.MissingAnswers(rows, run, block, file, hash, byFile, byHash, id, ids)
				if !supported {
					return nil, 0, false
				}
				if len(missing) > 0 {
					return reviewEnqueueRefusal("answers-not-open", strings.Join(missing, ", ")+" is not an open finding of "+file+" at its current content; nothing was queued"), 1, true
				}
			}
		}
	}
	dir := filepath.Join(root, ".code-forge", "queue")
	if err := queue.PublishAnswers(dir, p, id, byFile, byHash, ids, time.Now()); err != nil {
		return nil, 0, false
	}
	retry := false
	marker := filepath.Join(dir, id+".done")
	result, _ := reviewWaitResult(root, marker)
	if r, _ := result.(ledger.Object); r.Get("status") == "unavailable" {
		if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
			return reviewEnqueueRefusal("error", err.Error()), 1, true
		}
		if err := queue.PublishAnswers(dir, p, id, byFile, byHash, ids, time.Now()); err != nil {
			return reviewEnqueueRefusal("error", err.Error()), 1, true
		}
		retry = true
	}
	status := "queued"
	if _, err := os.Stat(marker); err == nil {
		status = "done"
	}
	out := ledger.Object{{Key: "ticket", Value: id}, {Key: "status", Value: status}, {Key: "file", Value: file}, {Key: "content_hash", Value: hash}, {Key: "by_file", Value: byFile}, {Key: "by_hash", Value: byHash}, {Key: "answers", Value: ids}}
	if p.Tree != nil {
		out.Set("tree", *p.Tree)
	}
	out.Set("kind", "answers")
	if retry {
		out.Set("retry", true)
	}
	return out, 0, true
}

// Budget-sensitive split results need the reloaded config and remain on Node
// until that dialect can be classified before any queue publication.
func reviewEnqueueCacheSupported(root, id string, record ledger.Object) bool {
	marker, ok := reviewWaitJSON(filepath.Join(root, ".code-forge", "queue", id+".done"))
	if !ok || marker.Get("status") == "refused" && marker.Get("reason") == "superseded" {
		return false
	}
	r, ok := reviewWaitResult(root, filepath.Join(root, ".code-forge", "queue", id+".done"))
	if !ok {
		return false
	}
	result, _ := r.(ledger.Object)
	return result.Get("stopped") != "split_required" && !(result.Get("status") == "split_required" && result.Get("lens") != nil)
}

func reviewEnqueueResult(root string, marker map[string]any) ledger.Object {
	rel, _ := marker["result"].(string)
	if !strings.HasPrefix(rel, ".code-forge"+string(filepath.Separator)+"reviews"+string(filepath.Separator)) {
		return nil
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == ".." {
			return nil
		}
	}
	result, _ := reviewWaitJSON(filepath.Join(root, rel))
	return result
}

func reviewEnqueueFresh(root string, p ticket.Parts, id string, retry bool) (ledger.Object, int) {
	dir := filepath.Join(root, ".code-forge", "queue")
	if retry {
		if err := queue.Publish(dir, p, id, time.Now()); err != nil {
			return reviewEnqueueRefusal("error", err.Error()), 1
		}
	}
	markerPath := filepath.Join(dir, id+".done")
	verdict, outcome := "counted", "kept"
	for attempt := 0; attempt < 3; attempt++ {
		data, err := os.ReadFile(markerPath)
		if os.IsNotExist(err) {
			outcome = "gone"
			break
		}
		var marker map[string]any
		_ = json.Unmarshal(data, &marker)
		result := reviewEnqueueResult(root, marker)
		verdict = "counted"
		if result != nil && marker["status"] == result.Get("status") {
			switch result.Get("status") {
			case "stale", "unavailable", "split_required":
				verdict = "rerun"
			case "refused":
				reason, _ := result.Get("reason").(string)
				if reason == "review-state-tampered" {
					verdict = "tampered"
				} else if reason == "other-run" || reason == "context-unreadable" || strings.HasPrefix(reason, "worktree-") {
					verdict = "rerun"
				}
			}
		}
		if verdict != "rerun" {
			break
		}
		outcome, err = queue.DropDone(dir, id, data)
		if err != nil {
			if e, ok := err.(*queue.ClaimError); ok {
				return reviewEnqueueRefusal(e.Code, e.Message), 1
			}
			return reviewEnqueueRefusal("fresh-claim-failed", "the claim of the cached result of "+p.File+" failed; nothing was queued"), 1
		}
		if outcome != "changed" {
			break
		}
		verdict, outcome = "counted", "kept"
	}
	if outcome == "dropped" || outcome == "gone" {
		if err := queue.Publish(dir, p, id, time.Now()); err != nil {
			return reviewEnqueueRefusal("error", err.Error()), 1
		}
		status := "queued"
		if _, err := os.Stat(markerPath); err == nil {
			status = "done"
		}
		out := ledger.Object{{Key: "ticket", Value: id}, {Key: "status", Value: status}, {Key: "file", Value: p.File}, {Key: "content_hash", Value: p.ContentHash}}
		if p.Tree != nil {
			out.Set("tree", *p.Tree)
		}
		if p.AcceptanceRevision != nil {
			out.Set("acceptance_revision", *p.AcceptanceRevision)
		}
		if outcome == "dropped" {
			key := "fresh"
			if retry {
				key = "retry"
			}
			out.Set(key, true)
		}
		return out, 0
	}
	reason := "fresh-needs-reopen"
	exits := []string{"block reopen", "block " + "waive"}
	message := "fresh-needs-reopen: the cached result of " + p.File + " counts as a review (or cannot be read, or kept changing under the claim); --fresh re-runs only stale, unavailable, round-1 split_required or refused other-run/worktree-*/context-unreadable results; the human's ways out are block reopen and block " + "waive; the cached result was kept"
	if verdict == "tampered" {
		reason = "fresh-tampered"
		exits = []string{"block close --no-" + "require-reviews"}
		message = "fresh-tampered: the cached result of " + p.File + " is review-state-tampered (a security event, not a stop); only the owner's block close --no-" + "require-reviews gets past it; the cached result was kept"
	}
	return ledger.Object{{Key: "status", Value: "refused"}, {Key: "reason", Value: reason}, {Key: "ticket", Value: id}, {Key: "file", Value: p.File}, {Key: "exits", Value: exits}, {Key: "message", Value: message}}, 1
}
