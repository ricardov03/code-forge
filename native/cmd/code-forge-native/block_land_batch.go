package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/proof"
	"github.com/ricardov03/code-forge/native/state"
)

type landBatchBlock struct {
	id, branch, head, base string
	original, entryBase    string // head and entry base_sha before the batch
	tree                   *proof.Tree
	entry                  ledger.Object
	gatesMs                int64
	fullScope, rebased     bool
}

// Batch landing consumes closed blocks. Keep the open/join/close single-block
// path separate so its output and hand-over boundary remain unchanged.
func landBatch(flags map[string]any, home, run string, ids []string) int {
	json := flags["json"] == true
	emit := func(row ledger.Object) {
		data, _ := ledger.Stringify(row, false)
		fmt.Fprintln(os.Stdout, string(data))
	}
	stoppedID, step := ids[0], "preflight"
	stop := func(reason string) int {
		fmt.Fprintf(os.Stderr, "block %s land stopped at %s: %s\n", stoppedID, step, strings.TrimSpace(reason))
		if json {
			emit(ledger.Object{{Key: "block", Value: stoppedID}, {Key: "event", Value: "refused"}, {Key: "step", Value: step}, {Key: "reason", Value: strings.TrimSpace(reason)}})
		}
		return 1
	}
	code := 0
	// Closed-block checks cannot use proof.WithSlot (it requires open blocks).
	// The run lock protects the recorded bases and claims through publication.
	err := state.WithRunLock(home, run, func() (failed error) {
		record, err := state.ReadRun(home, run)
		if err != nil {
			return err
		}
		if record.Get("status") != "active" {
			return fmt.Errorf("run %s has ended", run)
		}
		if !blockGrantEndSafe(record) || !blockStorageSafe(home, run, record) {
			return fmt.Errorf("batch run state or storage is unsupported")
		}
		if flags["no-merge-gate"] == true {
			return fmt.Errorf("a batch requires the full merge-gate suite")
		}
		if holder := blockString(blockObject(record.Get("proof_lock")).Get("block")); holder != "" {
			return fmt.Errorf("proof.busy: %s", holder)
		}
		maxMs := int64(1800000)
		if raw, ok := flags["max"].(string); ok {
			n, valid := landMax(raw)
			if !valid || n <= 0 {
				return fmt.Errorf("--max must be a positive duration like 90s, 30m or 1500ms")
			}
			maxMs = n
		}
		workspace := blockString(record.Get("workspace"))
		main, err := gitx.RepoTop(workspace)
		if err != nil {
			return err
		}
		prefix, err := filepath.Rel(main, proof.Real(workspace))
		if err != nil || filepath.IsAbs(prefix) || strings.HasPrefix(prefix, "..") {
			return fmt.Errorf("merge-gate workspace prefix is outside the repository")
		}
		mainHead, err := gitx.Run(main, "rev-parse", "--verify", "HEAD")
		if err != nil {
			return err
		}
		baseBranch := ""
		blocks := blockObject(record.Get("blocks"))
		batch := []*landBatchBlock{}
		seen := map[string]bool{}
		// Validate every block before rebasing any of them or running gates.
		for _, id := range ids {
			stoppedID = id
			if seen[id] {
				return fmt.Errorf("duplicate block %s", id)
			}
			seen[id] = true
			entry := blockObject(blocks.Get(id))
			if entry.Get("status") != "closed" {
				return fmt.Errorf("block %s must be closed before batch landing", id)
			}
			if entry.Get("landed") != nil {
				return fmt.Errorf("block %s has already landed", id)
			}
			tree, err := proof.BlockTree(workspace, entry)
			if err != nil {
				return err
			}
			if tree == nil {
				return fmt.Errorf("block %s has no recorded worktree and branch", id)
			}
			base := blockString(entry.Get("base_branch"))
			if base == "" || baseBranch != "" && baseBranch != base {
				return fmt.Errorf("batch blocks must have the same recorded base branch")
			}
			baseBranch = base
			if err := landBatchCheckout(main, mainHead, baseBranch); err != nil {
				return err
			}
			if gatesCleanTree(tree.Path) == "" {
				return fmt.Errorf("block %s worktree is dirty or unreadable", id)
			}
			files, err := landClaimFiles(home, run, id, tree.Path, main, record, entry)
			if err != nil {
				return err
			}
			if len(files) > 0 {
				return fmt.Errorf("block %s has unclaimed changes: %s", id, strings.Join(files, ", "))
			}
			args := []string{"run", "--run", run, "--block", id, "--changed", "--cwd", tree.Workspace}
			options, _ := gatesParse(args)
			if _, safe := landBatchGateScope(args, options); !safe {
				return fmt.Errorf("block %s changed gates are unsupported by native batch landing", id)
			}
			wt := blockObject(entry.Get("worktree"))
			head, err := gitx.Run(tree.Path, "rev-parse", "HEAD")
			if err != nil {
				return err
			}
			batch = append(batch, &landBatchBlock{id: id, branch: blockString(wt.Get("branch")), head: head, original: strings.TrimSpace(head), entryBase: blockString(entry.Get("base_sha")), base: blockString(wt.Get("base_sha")), tree: tree, entry: entry})
		}
		// Block i is rebased onto trial prefix i. Only prefixes that reach main
		// may remain as a base: every later block returns to its original head
		// and base, so a not-landed branch never carries another block's commits.
		keep := 0
		defer func() {
			if err := landBatchRestore(home, run, record, blocks, batch, keep); err != nil {
				fmt.Fprintln(os.Stderr, "cannot restore not-landed block:", err)
				if failed == nil {
					failed = err
				}
			}
		}()
		temporary, err := os.MkdirTemp(filepath.Dir(batch[0].tree.Path), ".merge-gate-")
		if err != nil {
			return err
		}
		added := false
		defer func() {
			if added {
				if _, err := gitx.Run(main, "worktree", "remove", "--force", temporary); err != nil {
					fmt.Fprintln(os.Stderr, "cannot remove merge-gate worktree:", err)
					code = 1
					return
				}
			}
			_ = os.RemoveAll(temporary)
		}()
		if _, err := gitx.Run(main, "worktree", "add", "--detach", temporary, strings.TrimSpace(mainHead)); err != nil {
			return err
		}
		added = true
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(maxMs)*time.Millisecond)
		defer cancel()
		prefixes := []string{strings.TrimSpace(mainHead)}
		for _, block := range batch {
			stoppedID, step = block.id, "rebase"
			tip := prefixes[len(prefixes)-1]
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if _, err := gitx.Run(block.tree.Path, "merge-base", "--is-ancestor", block.base, "HEAD"); err != nil {
				return fmt.Errorf("block %s recorded base is not an ancestor", block.id)
			}
			if _, err := gitx.Run(block.tree.Path, "-c", "rebase.updateRefs=false", "rebase", "--no-autostash", "--onto", tip, block.base); err != nil {
				_, _ = gitx.Run(block.tree.Path, "rebase", "--abort")
				return fmt.Errorf("block %s rebase failed: %s", block.id, landGitError(err, err.Error()))
			}
			block.rebased = true
			block.head, err = gitx.Run(block.tree.Path, "rev-parse", "HEAD")
			if err != nil {
				return err
			}
			block.entry.Set("base_sha", tip)
			wt := blockObject(block.entry.Get("worktree"))
			wt.Set("base_sha", tip)
			block.entry.Set("worktree", wt)
			blocks.Set(block.id, block.entry)
			record.Set("blocks", blocks)
			if _, err := state.WriteSigned(home, run, ledger.Object{{Key: "event", Value: "block.rebase"}, {Key: "block", Value: block.id}, {Key: "old_base", Value: block.base}, {Key: "new_base", Value: tip}, {Key: "worktree", Value: true}}); err != nil {
				return err
			}
			if err := state.SaveRun(home, record); err != nil {
				return err
			}
			step = "gates"
			args := []string{"run", "--run", run, "--block", block.id, "--changed", "--cwd", block.tree.Workspace}
			options, _ := gatesParse(args)
			full, safe := landBatchGateScope(args, options)
			if !safe {
				return fmt.Errorf("block %s changed gates are unsupported after rebasing", block.id)
			}
			block.fullScope = full
			started := time.Now()
			err = landBatchChanged(ctx, home, workspace, run, block)
			block.gatesMs = time.Since(started).Milliseconds()
			if err != nil {
				return err
			}
			step = "merge"
			if _, err := gitx.Run(temporary, "merge", "--no-ff", "--no-edit", "--", strings.TrimSpace(block.head)); err != nil {
				return fmt.Errorf("block %s trial merge failed: %s", block.id, landGitError(err, err.Error()))
			}
			head, err := gitx.Run(temporary, "rev-parse", "HEAD")
			if err != nil {
				return err
			}
			prefixes = append(prefixes, strings.TrimSpace(head))
		}
		step = "merge-gate"
		suiteMs := int64(0)
		tested := map[int]string{}
		testPrefix := func(n int) string {
			if reason, ok := tested[n]; ok {
				return reason
			}
			if _, err := gitx.Run(temporary, "checkout", "--detach", "--force", prefixes[n]); err != nil {
				return err.Error()
			}
			started := time.Now()
			defer func() { suiteMs += time.Since(started).Milliseconds() }()
			cwd := filepath.Join(temporary, prefix)
			tree := gatesCleanTree(temporary)
			reason := ""
			if !gatesReusableFull(cwd) {
				args := []string{"run", "--cwd", cwd}
				options, _ := gatesParse(args)
				suite := landCapture(func(s *landStep) int {
					code, safe := gatesNativeStep(args, options, s)
					if !safe {
						return landHandover
					}
					return code
				}, ctx)
				reason = landSuiteFailure(suite)
			}
			if ctx.Err() != nil {
				reason = ctx.Err().Error()
			}
			if tree == "" || gatesCleanTree(temporary) != tree {
				reason = "merge-gate tree changed during the full suite"
			}
			tested[n] = reason
			return reason
		}
		// A suite cut short by the deadline says nothing about any block, so a
		// timeout refuses the whole batch instead of naming a culprit.
		timedOut := func() error {
			if ctx.Err() == nil {
				return nil
			}
			stoppedID = strings.Join(ids, " ")
			return fmt.Errorf("batch timed out after %dms during the full suite; no block landed", maxMs)
		}
		green, culprit, failure := len(batch), -1, ""
		if reason := testPrefix(len(batch)); reason != "" {
			low, high := 0, len(batch)
			failure = reason
			for high-low > 1 {
				if err := timedOut(); err != nil {
					return err
				}
				mid := (low + high) / 2
				if reason := testPrefix(mid); reason != "" {
					high, failure = mid, reason
				} else {
					low = mid
				}
			}
			green, culprit = low, high-1
		}
		if err := timedOut(); err != nil {
			return err
		}
		// Only a prefix with an actual green result may advance main. The
		// original checkout and all rebased branches must still match our trial.
		if err := landBatchCheckout(main, mainHead, baseBranch); err != nil {
			return err
		}
		for _, block := range batch {
			head, err := gitx.Run(main, "rev-parse", "--verify", "refs/heads/"+block.branch)
			if err != nil || head != block.head || gatesCleanTree(block.tree.Path) == "" {
				return fmt.Errorf("block %s changed during the full suite", block.id)
			}
		}
		if green > 0 {
			if reason, checked := tested[green]; !checked || reason != "" {
				return fmt.Errorf("refusing a prefix whose tree failed")
			}
			if _, err := gitx.Run(main, "merge", "--ff-only", "--", prefixes[green]); err != nil {
				return err
			}
			// Single-block land keeps a landed block "closed" (dependents test
			// for that status); the landed marker stops a second landing.
			for i, block := range batch[:green] {
				block.entry.Set("landed", ledger.Object{{Key: "merge", Value: prefixes[i+1]}, {Key: "main", Value: prefixes[green]}})
				blocks.Set(block.id, block.entry)
			}
			record.Set("blocks", blocks)
			if err := state.SaveRun(home, record); err != nil {
				return err
			}
		}
		keep = green
		for i, block := range batch {
			row := ledger.Object{{Key: "event", Value: "land.timing"}, {Key: "block", Value: block.id}, {Key: "join_ms", Value: 0}, {Key: "gates_ms", Value: block.gatesMs}, {Key: "merge_gate_ms", Value: 0}, {Key: "parallel", Value: false}}
			if _, err := state.WriteSigned(home, run, row); err != nil {
				return err
			}
			if i < green {
				if json {
					emit(ledger.Object{{Key: "block", Value: block.id}, {Key: "event", Value: "landed"}})
				} else {
					fmt.Fprintf(os.Stdout, "block %s landed\n", block.id)
				}
			}
		}
		tree, err := gitx.Run(main, "rev-parse", prefixes[len(batch)]+"^{tree}")
		if err != nil {
			return err
		}
		if _, err := state.WriteSigned(home, run, ledger.Object{{Key: "event", Value: "land.batch"}, {Key: "blocks", Value: ids}, {Key: "tree", Value: strings.TrimSpace(tree)}, {Key: "suite_ms", Value: suiteMs}, {Key: "landed", Value: ids[:green]}}); err != nil {
			return err
		}
		if culprit >= 0 {
			stoppedID = ids[culprit]
			code = stop(failure)
			for _, id := range ids[culprit+1:] {
				if json {
					emit(ledger.Object{{Key: "block", Value: id}, {Key: "event", Value: "not landed"}, {Key: "after", Value: stoppedID}})
				} else {
					fmt.Fprintf(os.Stdout, "block %s not landed (after %s)\n", id, stoppedID)
				}
			}
		}
		return nil
	})
	if err != nil {
		return stop(err.Error())
	}
	return code
}

// landBatchRestore returns every block after index keep to the head and base
// it had before the batch. A branch the user moved since is left untouched.
func landBatchRestore(home, run string, record, blocks ledger.Object, batch []*landBatchBlock, keep int) error {
	changed := false
	var failed error
	for i, block := range batch {
		if i <= keep || !block.rebased {
			continue
		}
		head, err := gitx.Run(block.tree.Path, "rev-parse", "HEAD")
		if err != nil || strings.TrimSpace(head) != strings.TrimSpace(block.head) || gatesCleanTree(block.tree.Path) == "" {
			if failed == nil {
				failed = fmt.Errorf("block %s changed during the batch; its rebased base is not on main", block.id)
			}
			continue
		}
		if _, err := gitx.Run(block.tree.Path, "reset", "--hard", "--quiet", block.original); err != nil {
			if failed == nil {
				failed = err
			}
			continue
		}
		tip := blockString(block.entry.Get("base_sha"))
		block.entry.Set("base_sha", block.entryBase)
		wt := blockObject(block.entry.Get("worktree"))
		wt.Set("base_sha", block.base)
		block.entry.Set("worktree", wt)
		blocks.Set(block.id, block.entry)
		block.head, block.rebased, changed = block.original, false, true
		if _, err := state.WriteSigned(home, run, ledger.Object{{Key: "event", Value: "block.rebase"}, {Key: "block", Value: block.id}, {Key: "old_base", Value: tip}, {Key: "new_base", Value: block.base}, {Key: "worktree", Value: true}}); err != nil {
			return err
		}
	}
	if changed {
		record.Set("blocks", blocks)
		if err := state.SaveRun(home, record); err != nil && failed == nil {
			failed = err
		}
	}
	return failed
}

func landBatchCheckout(main, head, branch string) error {
	current, e1 := gitx.Run(main, "rev-parse", "--verify", "HEAD")
	ref, e2 := gitx.Run(main, "symbolic-ref", "-q", "HEAD")
	status, e3 := gitx.Run(main, "status", "--porcelain", "--untracked-files=all")
	if e1 != nil || e2 != nil || e3 != nil || current != head || strings.TrimSpace(ref) != "refs/heads/"+branch || status != "" {
		return fmt.Errorf("main checkout changed, is dirty, or is not on recorded base branch %s", branch)
	}
	return nil
}

// A closed batch cannot hand the whole command to Node. When native imports
// or an executable test graph require the Node scope expander, running all
// block gates is the conservative fallback. Other unsupported dialects refuse.
func landBatchGateScope(args []string, options gatesOptions) (full, safe bool) {
	reason := hLandStepHandoverGates
	code, safe := gatesNativeStep(args, options, &landStep{preflight: true}, &reason)
	if safe {
		return false, code == 0
	}
	if reason != hGatesChangedGraph && reason != hGatesChangedNative {
		return false, false
	}
	options.changed = false
	code, safe = gatesNativeStep(args, options, &landStep{preflight: true})
	return true, safe && code == 0
}

// Reuse the native changed-scope planner without the open-block proof slot.
// Its dialect is checked for every block before the batch begins mutating Git.
func landBatchChanged(ctx context.Context, home, workspace, run string, block *landBatchBlock) error {
	loaded, err := config.Load(workspace)
	if err != nil {
		return err
	}
	configured := blockMap(loaded["gates"])
	p, safe := gates.Resolve(block.tree.Workspace, configured)
	if !safe || !gates.ResolveCustom(&p, configured) {
		return fmt.Errorf("unsupported changed gates")
	}
	if kinds := blockMap(configured["kinds"]); kinds != nil {
		kind, _ := gatesKind(block.entry)
		if names, ok := kinds[kind].([]any); ok {
			p.Kind, p.Restricted, p.Names = kind, true, gates.Strings(names)
		}
	}
	if block.entry.Get("kind") == "light" {
		p.Light = gates.TestScope(block.entry, nil, int(^uint(0)>>1)).Files
	}
	if n, ok := gatesNumber(configured["rerun_failed"]); ok {
		p.Rerun = n != 0
	}
	if n, ok := gatesNumber(configured["repeat_new_tests"]); ok {
		p.Repeat = &gates.Repeat{N: int(n), Base: blockString(block.entry.Get("base_sha")), Block: block.entry}
	}
	threshold := 10
	if n, ok := gatesNumber(configured["full_suite_threshold_files"]); ok {
		threshold = int(n)
	}
	listed, err := gates.List(block.tree.Workspace)
	if err != nil {
		return err
	}
	base := blockString(block.entry.Get("base_sha"))
	delta, err := gates.BaseDelta(block.tree.Workspace, base, block.entry)
	if err != nil {
		return err
	}
	current := gates.Baseline(block.tree.Workspace, block.entry, listed)
	old := gates.BaseHashes(block.tree.Workspace, base, delta)
	scope := gates.ChangedEmptyGraph(old, current, gates.TestScopeCount(block.entry, len(delta), threshold), listed, threshold)
	scope.Report = "full"
	if len(scope.Files) > 0 {
		scope.Report = "related"
	}
	if block.fullScope {
		scope = gates.Scope{Kind: "full", Reason: "native_scope_fallback"}
	}
	timeout := 300000.0
	if n, ok := gatesNumber(configured["timeout_s"]); ok {
		timeout = n * 1000
	}
	stamp := gatesFullStamp(gates.TreeStamp(block.tree.Workspace), p, configured, block.entry)
	results, all, err := gates.Run(ctx, p, block.tree.Workspace, run, timeout, scope)
	if err != nil {
		return err
	}
	summaries := []any{}
	for _, value := range results {
		result := blockObject(value)
		summary := ledger.Object{}
		for _, name := range []string{"gate", "ok", "skipped", "duration_ms", "flaky"} {
			if value := result.Get(name); value != nil {
				summary.Set(name, value)
			}
		}
		if all && p.Repeat == nil && stamp != "" && result.Get("gate") == "test" && result.Get("scope") == "full" && result.Get("skipped") != true && result.Get("flaky") == nil && stamp == gatesFullStamp(gates.TreeStamp(block.tree.Workspace), p, configured, block.entry) {
			summary.Set("full_stamp", stamp)
		}
		summaries = append(summaries, summary)
	}
	row := ledger.Object{{Key: "event", Value: "gate.done"}, {Key: "block", Value: block.id}, {Key: "worktree", Value: block.tree.Path}, {Key: "all_ok", Value: all && ctx.Err() == nil}, {Key: "results", Value: summaries}, {Key: "test_base", Value: base}}
	gatesAdd(&row, scope.Fields(true))
	if _, err := state.WriteSigned(home, run, row); err != nil {
		return err
	}
	if !all || ctx.Err() != nil {
		return fmt.Errorf("changed gates failed: %s", landSuiteFailure(landResult{code: 1, data: ledger.Object{{Key: "results", Value: results}}}))
	}
	return nil
}
