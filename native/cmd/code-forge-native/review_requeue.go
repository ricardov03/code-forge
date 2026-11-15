package main

// B717: review-file.mjs requeueStoppedFiles in Go. A block join (review-file
// --wait-block, close's requeue and the --auto-claim join) restarts each
// changed stopped review whose findings were not waived: reopenFile's
// automatic checks, clearDone, the signed review.reopened row (by: auto) and
// the ticket enqueued after it.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"

	"github.com/ricardov03/code-forge/native/blockclose"
	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/proof"
	"github.com/ricardov03/code-forge/native/queue"
	"github.com/ricardov03/code-forge/native/reviewq"
	"github.com/ricardov03/code-forge/native/state"
	"github.com/ricardov03/code-forge/native/ticket"
	"github.com/ricardov03/code-forge/native/worker"
)

// errRequeueUnsupported marks a value only Node reads exactly (a ledger null
// row, a state file or date shape Go does not carry, an exclusion list it
// cannot match). The caller hands over; it is found before any write.
var errRequeueUnsupported = errors.New("requeue: a value only Node reads")

// errRequeueExclusion is errRequeueUnsupported for a review.exclude Go
// cannot match like Node; block close hands it over as close-excluded-file.
var errRequeueExclusion = fmt.Errorf("%w: review.exclude", errRequeueUnsupported)

// reviewRequeue is requeueStoppedFiles' options.
type reviewRequeue struct {
	home, repoRoot, run, block, tree string
	files                            []string
	print                            io.Writer
}

// reviewRequeueCodes are the refusals of checkBlockReviews that still need a
// review (needsReview in requeueStoppedFiles).
var reviewRequeueCodes = []string{"unreviewed", "unsigned", "not_approved", "review_cap", "autopilot_waiver_invalid"}

// reviewRequeueStopped runs requeueStoppedFiles. A first pass makes every
// read and check without writing, so an unsupported value hands over before
// any row is signed; the second pass is Node's loop. Coded refusals are
// *state.Error (Node's WorkerError and StateError codes); other errors are
// Node's uncoded throws.
func reviewRequeueStopped(o reviewRequeue) (map[string]string, error) {
	if _, err := o.pass(true); err != nil {
		var se *state.Error
		if !errors.As(err, &se) {
			return nil, err
		}
		// A coded refusal ends Node's loop after the files before it.
	}
	return o.pass(false)
}

func (o reviewRequeue) pass(dry bool) (map[string]string, error) {
	restarted := map[string]string{}
	if len(o.files) == 0 {
		return restarted, nil
	}
	record, err := state.ReadRun(o.home, o.run)
	if err != nil {
		// readRun's own refusal texts stay with Node.
		return nil, errRequeueUnsupported
	}
	blocks := blockObject(record.Get("blocks"))
	entry := blockObject(blocks.Get(o.block))
	if entry.Get("status") != "open" {
		return restarted, nil
	}
	workspace, _ := record.Get("workspace").(string)
	realRepo, err := filepath.EvalSymlinks(o.repoRoot)
	if err != nil {
		return nil, errRequeueUnsupported
	}
	main, ok := reviewWaitRoot(context.Background(), workspace)
	if !ok {
		return nil, errRequeueUnsupported
	}
	if real, err := filepath.EvalSymlinks(main); err != nil || real != realRepo {
		return nil, &state.Error{Code: "other-workspace", Message: "the run belongs to another workspace"}
	}
	recorded := o.repoRoot
	wt, err := proof.BlockTree(workspace, entry)
	if err != nil {
		var se *state.Error
		if errors.As(err, &se) {
			return nil, err
		}
		return nil, errRequeueUnsupported
	}
	if wt != nil {
		recorded = wt.Path
	}
	realRecorded, err1 := filepath.EvalSymlinks(recorded)
	realTree, err2 := filepath.EvalSymlinks(o.tree)
	if err1 != nil || err2 != nil {
		return nil, errRequeueUnsupported
	}
	if realRecorded != realTree {
		return nil, &state.Error{Code: "worktree-mismatch", Message: "the block scope does not match the recorded content tree"}
	}
	prefix := gatecheck.Prefix(realRepo, workspace)
	key, err := blockclose.ReadKey(o.home, o.run)
	if err != nil {
		return nil, errRequeueUnsupported
	}
	project, _ := record.Get("project").(string)
	ledgerDir := filepath.Join(o.home, ".code-forge", "ledger")
	values, err := ledger.ReadAllOrdered(ledgerDir, project)
	if err != nil {
		return nil, errRequeueUnsupported
	}
	ordered := []ledger.Object{}
	rows := []gatecheck.Row{}
	for _, v := range values {
		row, isObject := v.(ledger.Object)
		if v == nil {
			// rows.filter((row) => row.run === run) on a null row throws.
			return nil, errRequeueUnsupported
		}
		if !isObject || row.Get("run") != o.run {
			continue
		}
		ordered = append(ordered, row)
		rows = append(rows, row.Map())
	}
	cfg, ok := blockSnapshot(record)
	if !ok {
		return nil, errRequeueUnsupported
	}
	runRoot, ok := reviewRequeueRunRoot(cfg, o.run)
	if !ok {
		return nil, errRequeueUnsupported
	}
	dir := filepath.Join(realRepo, ".code-forge", "queue")
	for _, file := range o.files {
		if !gatecheck.Owns(prefix, entry, file) {
			continue
		}
		rule, supported := reviewq.Exclusion(cfg, file)
		if !supported {
			return nil, errRequeueExclusion
		}
		if rule != "" {
			continue
		}
		other := false
		for _, field := range blocks {
			block := blockObject(field.Value)
			if field.Key != o.block && block.Get("status") == "open" && gatecheck.Owns(prefix, block, file) {
				other = true
				break
			}
		}
		if other {
			continue
		}
		loaded, err := worker.LoadState(worker.StateWhere{Home: o.home, RunRootDir: runRoot, RunID: o.run, Block: o.block, File: file, ContentRoot: o.tree, Key: key, Rows: ordered}, false)
		if err != nil {
			return nil, errRequeueUnsupported
		}
		if (loaded.Status != "ok" && loaded.Status != "restored") || loaded.State.Get("status") != "stopped" || !reviewRequeueLength(loaded.State.Get("open")) {
			continue
		}
		fromHash, known := worker.StopContentHash(o.run, o.block, file, key, loaded.State, ordered)
		current, err := gitx.ContentHash(o.tree, file)
		if err != nil {
			return nil, errRequeueUnsupported
		}
		if !known || fromHash == current {
			continue
		}
		mine := []gatecheck.Row{}
		for _, r := range rows {
			if v, has := r["file"]; !has || v == file {
				mine = append(mine, r)
			}
		}
		needsReview := func(hash string) (bool, error) {
			result := gatecheck.Evaluate(gatecheck.Input{Run: o.run, Block: o.block, Key: key, Rows: mine, Files: []gatecheck.File{{File: file, Hash: hash}}, ProofFiles: []gatecheck.File{}, HighPaths: []any{}})
			if result.Unknown {
				return false, errRequeueUnsupported
			}
			return slices.ContainsFunc(result.Blockers, func(b gatecheck.Blocker) bool {
				return b.File == file && slices.Contains(reviewRequeueCodes, b.Code)
			}), nil
		}
		need, err := needsReview(current)
		if err != nil {
			return nil, err
		}
		if !need {
			continue
		}
		if need, err = needsReview(fromHash); err != nil {
			return nil, err
		} else if !need {
			continue
		}
		// reopenFile (automatic): one verified reopen row per at_seq.
		seq := loaded.Seq
		if math.IsNaN(seq) || math.IsInf(seq, 0) || seq != math.Trunc(seq) || seq < 0 {
			return nil, &state.Error{Code: "reopen-tampered", Message: fmt.Sprintf("reopen-tampered: the review state of %s has no valid anchor seq; nothing was recorded", file)}
		}
		earlier := worker.LatestReopenAt(o.run, o.block, file, key, seq, ordered)
		if earlier != nil && earlier.Get("to_hash") != current {
			return nil, &state.Error{Code: "reopen-already-recorded", Message: fmt.Sprintf("reopen-already-recorded: %s was already reopened at state seq %s for other content; run review-file, or the content changed since the reopen; nothing was recorded", file, reviewRequeueNumber(seq))}
		}
		parts := ticket.Parts{Run: o.run, Block: o.block, File: file, ContentHash: current}
		if realTree != realRepo {
			tree := realTree
			parts.Tree = &tree
		}
		identity, ok := reviewEnqueueRecord(o.home, realRepo, o.run)
		if !ok {
			return nil, errRequeueUnsupported
		}
		if revision, isText := blockObject(blockObject(identity.Get("blocks")).Get(o.block)).Get("acceptance_revision").(string); isText {
			parts.AcceptanceRevision = &revision
		}
		if _, err := queue.Identity(dir, parts); errors.Is(err, queue.ErrScript) {
			// B730: clearDone's reviewIdentity throws this TypeError before
			// the file's reopen row is written.
			return nil, err
		} else if err != nil {
			return nil, errRequeueUnsupported
		}
		if _, ok := reviewEnqueueExclusion(o.home, realRepo, o.run, file); !ok {
			return nil, errRequeueUnsupported
		}
		if dry {
			restarted[file] = ""
			continue
		}
		// C11: the current content's cached result is dropped FIRST.
		if _, err := reopenClearDone(dir, parts); err != nil {
			return nil, err
		}
		row, existing := earlier, earlier != nil
		if !existing {
			var stopReason any = "stopped"
			if reason, isText := blockObject(loaded.State.Get("next")).Get("reason").(string); isText {
				stopReason = reason
			}
			fresh := ledger.Object{{Key: "event", Value: worker.ReopenedEvent}, {Key: "block", Value: o.block}, {Key: "file", Value: file}, {Key: "from", Value: "stopped"}, {Key: "stop_reason", Value: stopReason}, {Key: "at_seq", Value: seq}, {Key: "from_hash", Value: fromHash}, {Key: "to_hash", Value: current}, {Key: "reason", Value: worker.AutoRequeueReason}, {Key: "by", Value: "auto"}}
			written, err := state.WriteSigned(o.home, o.run, fresh)
			if err != nil {
				return nil, &state.Error{Code: "reopen-write-failed", Message: fmt.Sprintf("reopen-write-failed: the signed review.reopened row for %s was not written (%s); run block reopen again", file, bjCauseCode(err))}
			}
			row = written
		}
		ts, _ := row.Get("ts").(string)
		id, err := reopenEnqueue(o.home, realRepo, dir, o.run, parts, o.tree, file, ts)
		if err != nil {
			return nil, err
		}
		restarted[file] = ts
		if !existing {
			if id == "" {
				// enqueue's excluded answer has no ticket.
				id = "undefined"
			}
			fmt.Fprintf(o.print, "auto-requeue %s: content changed after stopped review (ticket %s)\n", file, id)
		}
	}
	return restarted, nil
}

// reviewRequeueRunRoot is tmp.mjs runRootPath(run, {root: tmp.root}) without
// creating it: tmp.root, else an absolute CODE_FORGE_TMP_ROOT, else
// <os.tmpdir()>/code-forge. A tmp.root that is not an absolute path is
// tmpBase's TypeError (false).
func reviewRequeueRunRoot(cfg map[string]any, run string) (string, bool) {
	if v, has := blockMap(cfg["tmp"])["root"]; has && v != nil {
		root, isText := v.(string)
		if !isText || !filepath.IsAbs(root) {
			return "", false
		}
		return filepath.Join(root, run), true
	}
	if env := os.Getenv("CODE_FORGE_TMP_ROOT"); filepath.IsAbs(env) {
		return filepath.Join(env, run), true
	}
	return filepath.Join(os.TempDir(), "code-forge", run), true
}

// reviewRequeueLength is `value?.length > 0` for a state's open findings.
func reviewRequeueLength(v any) bool {
	switch x := v.(type) {
	case []any:
		return len(x) > 0
	case string:
		return x != ""
	}
	return false
}

// reviewRequeueNumber prints an integer seq as JavaScript does.
func reviewRequeueNumber(seq float64) string {
	if seq < 1e21 {
		return fmt.Sprintf("%.0f", seq)
	}
	return fmt.Sprint(seq)
}
