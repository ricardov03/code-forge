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
	saved                  ledger.Object // the entry as last read or committed (B426)
	gatesMs                int64
	rebased                bool
	node                   bool // owns or changes a Node product file
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
	// B426: the batch holds only its own blocks' locks through the rebases,
	// gates and merge gate, working on a snapshot of the run record. Each
	// record change (a new base, landed, a restore) is one short commit of
	// these blocks alone, refused with block-changed if another verb changed
	// one of them meanwhile. Verbs on other blocks of the run never wait.
	err := state.WithBlockLocks(home, run, ids, func() (failed error) {
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
			if !landBatchGateScope(args, options) {
				return fmt.Errorf("block %s changed gates are unsupported by native batch landing", id)
			}
			wt := blockObject(entry.Get("worktree"))
			head, err := gitx.Run(tree.Path, "rev-parse", "HEAD")
			if err != nil {
				return err
			}
			node, err := landBatchNodeProduct(tree.Path, blockString(wt.Get("base_sha")), entry)
			if err != nil {
				return err
			}
			batch = append(batch, &landBatchBlock{id: id, branch: blockString(wt.Get("branch")), head: head, original: strings.TrimSpace(head), entryBase: blockString(entry.Get("base_sha")), base: blockString(wt.Get("base_sha")), tree: tree, entry: entry, saved: state.CloneObject(entry), node: node})
		}
		// Block i is rebased onto trial prefix i. Only prefixes that reach main
		// may remain as a base: every later block returns to its original head
		// and base, so a not-landed branch never carries another block's commits.
		keep := 0
		defer func() {
			if err := landBatchRestore(home, run, batch, keep); err != nil {
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
			if err := landBatchCommit(home, run, []*landBatchBlock{block}, ledger.Object{{Key: "event", Value: "block.rebase"}, {Key: "block", Value: block.id}, {Key: "old_base", Value: block.base}, {Key: "new_base", Value: tip}, {Key: "worktree", Value: true}}); err != nil {
				return err
			}
			step = "gates"
			args := []string{"run", "--run", run, "--block", block.id, "--changed", "--cwd", block.tree.Workspace}
			options, _ := gatesParse(args)
			if !landBatchGateScope(args, options) {
				return fmt.Errorf("block %s changed gates are unsupported after rebasing", block.id)
			}
			started := time.Now()
			// B404: every block runs its own selected tests; none waits for a
			// full suite.
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
		// B398/B407: a batch with no Node product file is go_only; its merge
		// gate still runs test/compat/go.test.mjs (go build, vet and test).
		mergeReason := "go_only"
		for _, block := range batch {
			if block.node {
				mergeReason = "node_product"
			}
		}
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
				tests, owned := []string{}, []string{}
				for _, block := range batch[:n] {
					tests = append(tests, gates.Strings(block.entry.Get("test_files"))...)
					owned = append(owned, gates.Strings(block.entry.Get("owned_files"))...)
				}
				scope, err := landMergeScope(cwd, prefixes[0], prefixes[n], tests, owned, mergeReason == "go_only")
				if err != nil {
					return err.Error()
				}
				args := []string{"run", "--cwd", cwd}
				options, _ := gatesParse(args)
				selected := -1
				if landMergeSelectable(cwd) {
					options.scope = &scope
					selected = len(scope.Files)
				}
				if n == len(batch) && selected >= 0 {
					if json {
						emit(ledger.Object{{Key: "event", Value: "merge_gate"}, {Key: "reason", Value: scope.Reason}, {Key: "batch", Value: mergeReason}, {Key: "test_count", Value: len(scope.Files)}, {Key: "skipped", Value: len(scope.Files) == 0}})
					} else if len(scope.Files) == 0 {
						fmt.Fprintf(os.Stdout, "merge gate: no test selected (%s); test gate skipped\n", mergeReason)
					} else {
						fmt.Fprintf(os.Stdout, "merge gate: %d selected test files (%s, %s)\n", len(scope.Files), scope.Reason, mergeReason)
					}
				}
				suite := landCapture(func(s *landStep) int {
					code, safe := gatesNativeStep(args, options, s)
					if !safe {
						return landHandover
					}
					return code
				}, ctx)
				reason = landMergeFailure(home, run, batch[n-1].id, suite, selected)
			}
			if ctx.Err() != nil {
				reason = ctx.Err().Error()
			}
			if tree == "" || gatesCleanTree(temporary) != tree {
				reason = "merge-gate tree changed during the merge gate"
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
			return fmt.Errorf("batch timed out after %dms during the merge gate; no block landed", maxMs)
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
			// B426: the merge gate and the merge ran without the run lock;
			// only this "mark landed" commit takes it.
			for i, block := range batch[:green] {
				block.entry.Set("landed", ledger.Object{{Key: "merge", Value: prefixes[i+1]}, {Key: "main", Value: prefixes[green]}})
			}
			if err := landBatchCommit(home, run, batch[:green]); err != nil {
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
		if _, err := state.WriteSigned(home, run, ledger.Object{{Key: "event", Value: "land.batch"}, {Key: "blocks", Value: ids}, {Key: "tree", Value: strings.TrimSpace(tree)}, {Key: "suite_ms", Value: suiteMs}, {Key: "merge_gate_reason", Value: mergeReason}, {Key: "landed", Value: ids[:green]}}); err != nil {
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
	// B430: push once after the block locks and the run lock are released.
	if pushed, err := landBatchPush(flags, home, run, ids); err != nil {
		stoppedID, step = pushed, "push"
		return stop(err.Error())
	}
	return code
}

// landBatchCommit publishes the entries of these batch blocks, and the signed
// rows, in one short run-lock commit (B426). It refuses with block-changed when
// another verb changed one of them since the batch last read or committed it.
func landBatchCommit(home, run string, list []*landBatchBlock, rows ...ledger.Object) error {
	expected, updates := map[string]ledger.Object{}, map[string]ledger.Object{}
	for _, block := range list {
		expected[block.id] = block.saved
		updates[block.id] = state.CloneObject(block.entry)
	}
	if err := state.CommitBlocks(home, run, expected, updates, rows...); err != nil {
		return err
	}
	for _, block := range list {
		block.saved = updates[block.id]
	}
	return nil
}

// landBatchRestore returns every block after index keep to the head and base
// it had before the batch. A branch the user moved since is left untouched.
func landBatchRestore(home, run string, batch []*landBatchBlock, keep int) error {
	restored, rows := []*landBatchBlock{}, []ledger.Object{}
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
		block.head, block.rebased = block.original, false
		restored = append(restored, block)
		rows = append(rows, ledger.Object{{Key: "event", Value: "block.rebase"}, {Key: "block", Value: block.id}, {Key: "old_base", Value: tip}, {Key: "new_base", Value: block.base}, {Key: "worktree", Value: true}})
	}
	if len(restored) > 0 {
		if err := landBatchCommit(home, run, restored, rows...); err != nil && failed == nil {
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

// A closed batch cannot hand the whole command to Node: a block whose gates
// dialect the native runner does not support refuses the batch. B404: there is
// no full-suite fallback; the native scope planner covers every block.
func landBatchGateScope(args []string, options gatesOptions) bool {
	reason := hLandStepHandoverGates
	code, safe := gatesNativeStep(args, options, &landStep{preflight: true}, &reason)
	return safe && code == 0
}

// landMergeScope is a merge-gate test scope (B404): the B404 selection over
// every file the trial merge changed since main (from..to, relative to cwd),
// with the tests and owned files of the merged blocks.
// landMergeFailure is the merge gate's failure text. An empty selection skips
// the test gate on purpose (B404), so a green run with no test is a pass, not
// a missing gates.test.
func landMergeFailure(home, run, id string, suite landResult, selected int) string {
	if selected < 0 {
		return landSuiteFailure(home, run, id, suite)
	}
	if selected == 0 && suite.code == 0 && suite.data.Get("allOk") == true {
		return ""
	}
	return landSuiteFailure(home, run, id, suite)
}

// landMergeSelectable reports a merge-trial test command (gatesSelectable). A
// command without {test_files} gets the selection appended (gatesWithTestFiles),
// so the merge gate never runs it unscoped. Without a test command the merge
// gate still refuses: it needs a test gate.
func landMergeSelectable(cwd string) bool {
	loaded, err := config.Load(cwd)
	if err != nil {
		return false
	}
	p, _ := gates.Resolve(cwd, blockMap(loaded["gates"]))
	return gatesSelectable(p)
}

func landMergeScope(cwd, from, to string, tests, owned []string, goOnly bool) (gates.Scope, error) {
	listed, err := gates.List(cwd)
	if err != nil {
		return gates.Scope{}, err
	}
	out, err := gitx.Run(cwd, "diff", "--relative", "--name-only", "-z", from, to, "--")
	if err != nil {
		return gates.Scope{}, err
	}
	s := gatesSelection{tests: tests, owned: owned}
	for _, f := range strings.Split(out, "\x00") {
		if f = strings.TrimSpace(f); f != "" {
			s.changed = append(s.changed, f)
		}
	}
	reason := "import_graph"
	if goOnly {
		reason = "go_only"
	}
	return gates.Scope{Kind: "changed", Report: "related", Reason: reason, Files: gatesSelectFiles(cwd, listed, s), Changed: []string{}, Dependents: []string{}}, nil
}

// landBatchNodeProduct reports whether a block owns or changes a file the
// Node suite tests: src/**, bin/**, scripts/**/*.mjs (not the native build
// script), package.json or package-lock.json.
func landBatchNodeProduct(tree, base string, entry ledger.Object) (bool, error) {
	files := gates.Strings(entry.Get("owned_files"))
	changed, err := gitx.Run(tree, "diff", "--name-only", "-z", base, "HEAD", "--")
	if err != nil {
		return false, err
	}
	files = append(files, strings.Split(changed, "\x00")...)
	for _, file := range files {
		file = strings.TrimPrefix(filepath.ToSlash(file), "./")
		switch {
		case file == "package.json" || file == "package-lock.json":
			return true, nil
		case strings.HasPrefix(file, "src/") || strings.HasPrefix(file, "bin/"):
			return true, nil
		case strings.HasPrefix(file, "scripts/") && strings.HasSuffix(file, ".mjs") && file != "scripts/build-native.mjs":
			return true, nil
		}
	}
	return false, nil
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
	var scope gates.Scope
	if gatesSelectable(p) {
		// B404: the same selection as gates run --changed; an empty one skips
		// the test gate instead of running the full suite.
		scope = gatesChangedSelect(block.tree.Workspace, block.entry, listed, delta, gatesGoOnly(block.entry, p))
		scope.Files = gatesRunnerFiles(p.Commands["test"].Argv, scope.Files)
		if len(scope.Files) == 0 {
			commands := map[string]*gates.Command{}
			for name, command := range p.Commands {
				if name != "test" {
					commands[name] = command
				}
			}
			p.Commands = commands
		}
	} else {
		current := gates.Baseline(block.tree.Workspace, block.entry, listed)
		old := gates.BaseHashes(block.tree.Workspace, base, delta)
		scope = gates.ChangedEmptyGraph(old, current, gates.TestScopeCount(block.entry, len(delta), threshold), listed, threshold)
		scope.Report = "full"
		if len(scope.Files) > 0 {
			scope.Report = "related"
		}
	}
	timeout := 300000.0
	if n, ok := gatesNumber(configured["timeout_s"]); ok {
		timeout = n * 1000
	}
	stamp := gatesFullStamp(gates.TreeStamp(block.tree.Workspace), p, configured, block.entry)
	runPlan := p
	if gatesSelectedReason(scope.Reason) {
		runPlan = gatesWithTestFiles(p)
	}
	results, all, err := gates.Run(ctx, runPlan, block.tree.Workspace, run, timeout, scope)
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
	if gatesSelectedReason(scope.Reason) {
		row.Set("test_files", scope.Files)
	}
	if _, err := state.WriteSigned(home, run, row); err != nil {
		return err
	}
	if !all || ctx.Err() != nil {
		return fmt.Errorf("changed gates failed: %s", landSuiteFailure(home, run, block.id, landResult{code: 1, data: ledger.Object{{Key: "results", Value: results}}}))
	}
	return nil
}
