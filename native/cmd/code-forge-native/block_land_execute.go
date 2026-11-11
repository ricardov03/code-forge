package main

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/daemonclient"
	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
	"github.com/ricardov03/code-forge/native/proof"
	"github.com/ricardov03/code-forge/native/state"
)

func landExecute(args []string, flags map[string]any, home, run, id string, record, entry ledger.Object, fallback func() int) int {
	return landExecuteWithReason(args, flags, home, run, id, record, entry, func(handoverReason) int { return fallback() })
}

func landExecuteWithReason(args []string, flags map[string]any, home, run, id string, record, entry ledger.Object, fallback func(handoverReason) int) (code int) {
	stderr := &landErrorStream{}
	beforeLog := []any{}
	begin := func() { beforeLog = gatesBreadcrumbName("block") }
	finish := func() {
		if code != 0 {
			var message any
			if stderr.last != "" {
				message = stderr.last
			}
			nativeVerbFailure("block", "", append([]string{"land"}, args...), code, message, beforeLog)
		}
	}
	report, transcript := blockString(flags["report"]), blockString(flags["transcript"])
	maxMs := int64(1800000)
	if raw, ok := flags["max"].(string); ok {
		maxMs, _ = landMax(raw)
	}
	json, merge, skip := flags["json"] == true, flags["merge"] == true, flags["no-merge-gate"] == true
	step := "join"
	next := closeCommand([]string{"code-forge", "review-file", "--wait-block", id, "--run", run})
	joinMs, gatesMs := int64(0), int64(0)
	var mergeGateMs any
	mergeGateTree, mergeGateReason := "", ""
	emit := func(out ledger.Object) {
		data, _ := ledger.Stringify(out, false)
		fmt.Fprintln(os.Stdout, string(data))
	}
	stop := func(reason string) int {
		if step == "join" && strings.Split(reason, "\n")[0] == "wait_timeout" {
			argv := []string{"code-forge", "block", "land", id, "--run", run, "--report", report, "--transcript", transcript, "--max", fmt.Sprintf("%.0fs", math.Max(1800, math.Ceil(float64(maxMs)/500)))}
			if merge {
				argv = append(argv, "--merge")
			}
			if skip {
				argv = append(argv, "--no-merge-gate")
			}
			if json {
				argv = append(argv, "--json")
			}
			next = closeCommand(argv)
		}
		reason = strings.TrimSpace(reason)
		fmt.Fprintf(stderr, "block %s land stopped at %s: %s\nnext: %s\n", id, step, reason, next)
		if json {
			emit(ledger.Object{{Key: "block", Value: id}, {Key: "event", Value: "refused"}, {Key: "step", Value: step}, {Key: "reason", Value: reason}, {Key: "next", Value: next}})
		}
		return 1
	}
	// Refusals before a child step still append Node's timing row.
	timing := func() {
		row := ledger.Object{{Key: "event", Value: "land.timing"}, {Key: "block", Value: id}, {Key: "join_ms", Value: joinMs}, {Key: "gates_ms", Value: gatesMs}, {Key: "merge_gate_ms", Value: mergeGateMs}, {Key: "parallel", Value: true}}
		if mergeGateTree != "" {
			row.Set("merge_gate_reused", true)
			row.Set("merge_gate_tree", mergeGateTree)
		}
		if mergeGateReason != "" {
			row.Set("merge_gate_reason", mergeGateReason)
		}
		if _, err := state.WriteSigned(home, run, row); err != nil {
			fmt.Fprintln(stderr, "block land:", err)
			code = 1
		}
	}
	if entry == nil {
		begin()
		defer finish()
		defer timing()
		return stop("unknown block in run " + run)
	}
	if _, ok := entry.Get("status").(string); !ok {
		return fallback(hLandStatusShape)
	}
	if entry.Get("status") != "open" {
		begin()
		defer finish()
		defer timing()
		return stop("block is " + blockString(entry.Get("status")) + ", not open")
	}
	if !blockGrantEndSafe(record) || !blockStorageSafe(home, run, record) {
		return fallback(blockStateHandoverReason(record, "land", hLandStateStorage))
	}
	workspace := blockString(record.Get("workspace"))
	tree, err := proof.BlockTree(workspace, entry)
	if err != nil {
		begin()
		defer finish()
		defer timing()
		return stop(err.Error())
	}
	if tree == nil {
		begin()
		defer finish()
		defer timing()
		return stop("block has no recorded worktree and branch")
	}
	top, err := gitx.RepoTop(tree.Path)
	if err != nil {
		return fallback(hLandWorktreeRepo)
	}
	main, err := gitx.RepoTop(workspace)
	if err != nil {
		return fallback(hLandMainRepo)
	}
	cfg, ok := blockSnapshot(record)
	if !ok || !daemonclient.Enabled(cfg) {
		return fallback(hLandConfigDaemon)
	}
	dry := &landStep{preflight: true}
	if reviewWaitBlockStep([]string{"--wait-block", id, "--run", run, "--max", "0ms", "--cwd", tree.Workspace}, dry) == landHandover {
		return fallback(hLandStepHandoverJoin)
	}
	gateArgs := []string{"run", "--run", run, "--block", id, "--changed", "--cwd", tree.Workspace}
	gateOptions, message := gatesParse(gateArgs)
	if message != "" {
		return fallback(hLandGatesParse)
	}
	if _, safe := gatesNativeStep(gateArgs, gateOptions, dry); !safe {
		return fallback(hLandStepHandoverGates)
	}
	mergeGate := merge && !skip && blockMap(cfg["gates"])["merge_gate"] != false
	if mergeGate {
		// Check the full-suite dialect before close can publish. The trial
		// worktree will use the main checkout's committed gate configuration.
		fullArgs := []string{"run", "--cwd", tree.Workspace}
		fullOptions, _ := gatesParse(fullArgs)
		if _, safe := gatesNativeStep(fullArgs, fullOptions, dry); !safe {
			return fallback(hLandMergeGate)
		}
	}
	files, err := landClaimFiles(home, run, id, top, main, record, entry)
	if err != nil {
		return fallback(hLandClaimFiles)
	}
	// Close must also accept the ownership that auto-claim will publish.
	encoded, _ := ledger.Stringify(record, false)
	predicted, _ := ledger.DecodeObject(encoded)
	blocks := blockObject(predicted.Get("blocks"))
	predictedEntry := blockObject(blocks.Get(id))
	claimed, _ := predictedEntry.Get("owned_files").([]any)
	kinds := blockObject(predictedEntry.Get("owned_kinds")).Clone()
	for _, file := range files {
		claimed = append(claimed, file)
		kinds.Set(file, owned.PathKind(top, file))
	}
	predictedEntry.Set("owned_files", claimed)
	predictedEntry.Set("owned_kinds", kinds)
	blocks.Set(id, predictedEntry)
	predicted.Set("blocks", blocks)
	dry.record = predicted
	closeArgs := []string{id, "--run", run, "--report", report, "--transcript", transcript, "--json"}
	closeReason := hLandStepHandoverClose
	if blockCloseStepWithReason(closeArgs, dry, &closeReason) == landHandover {
		return fallback(closeReason)
	}
	if err := landAutoClaim(home, run, id, top, record, files, io.Discard, true); err != nil {
		return fallback(hLandStepHandoverClaim)
	}
	// All hand-over predicates are settled. No code below this point starts Node.
	begin()
	defer finish()
	defer timing()
	deadline := time.Now().Add(time.Duration(maxMs) * time.Millisecond)
	print := io.Writer(os.Stdout)
	if json {
		print = stderr
	}
	if err := landAutoClaim(home, run, id, top, record, files, print, false); err != nil {
		return stop(err.Error())
	}
	stamp := func() (string, error) {
		head, err := gitx.Run(top, "rev-parse", "--verify", "HEAD")
		if err != nil {
			return "", fmt.Errorf("cannot stamp block worktree")
		}
		status, err := gitx.Run(top, "status", "--porcelain", "--untracked-files=all")
		if err != nil {
			return "", fmt.Errorf("cannot stamp block worktree")
		}
		return head + "\n" + status, nil
	}
	before, err := stamp()
	if err != nil {
		return stop(err.Error())
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gating := make(chan landResult, 1)
	runGates := func() landResult {
		started := time.Now()
		defer func() { gatesMs += time.Since(started).Milliseconds() }()
		return landCapture(func(s *landStep) int {
			// B363: land merges onto the new tip and its merge gate tests the
			// merged tree, so a stale base gates on with the one WARN line that
			// gates run --allow-stale-base prints (land prints the gate stderr).
			fmt.Fprint(s.stderr, gatesStaleWarning(home, run, id))
			code, safe := gatesNativeStep(gateArgs, gateOptions, s)
			if !safe {
				return landHandover
			}
			return code
		}, ctx)
	}
	go func() { gating <- runGates() }()
	join := func() string {
		started := time.Now()
		defer func() { joinMs = time.Since(started).Milliseconds() }()
		var joined landResult
		for attempt := 0; attempt < 3; attempt++ {
			remaining := max(int64(0), time.Until(deadline).Milliseconds())
			if remaining == 0 {
				return "wait_timeout"
			}
			duration := fmt.Sprintf("%dms", remaining)
			if remaining > 9999999 {
				duration = fmt.Sprintf("%ds", remaining/1000)
			}
			joined = landCapture(func(s *landStep) int {
				return reviewWaitBlockStep([]string{"--wait-block", id, "--run", run, "--max", duration, "--cwd", tree.Workspace}, s)
			}, nil)
			for _, line := range strings.Split(joined.error, "\n") {
				if strings.HasPrefix(line, "auto-requeue ") {
					fmt.Fprintln(stderr, line)
				}
			}
			findings := false
			values, _ := joined.data.Get("files").([]any)
			for _, v := range values {
				if blockObject(v).Get("status") == "findings" {
					findings = true
				}
			}
			if joined.code != 0 || joined.data.Get("done") != true || !findings {
				break
			}
		}
		if joined.code != 0 || joined.data.Get("done") != true {
			if joined.data.Get("status") == "wait_timeout" || joined.data.Get("reason") == "wait_timeout" || !time.Now().Before(deadline) {
				return "wait_timeout"
			}
			return landFirst(joined.error, blockString(joined.data.Get("message")), blockString(joined.data.Get("reason")), blockString(joined.data.Get("status")), "reviews did not finish")
		}
		values, _ := joined.data.Get("files").([]any)
		details := []string{}
		for _, v := range values {
			f := blockObject(v)
			if f.Get("status") == "excluded" || f.Get("outcome") == "approved" {
				continue
			}
			if len(details) == 0 {
				next = closeCommand([]string{"code-forge", "review-file", "--wait", blockString(f.Get("ticket"))})
			}
			outcome := blockString(f.Get("outcome"))
			if f.Get("outcome") == nil {
				outcome = blockString(f.Get("status"))
			}
			details = append(details, blockString(f.Get("file"))+": "+outcome)
			if ticket := blockString(f.Get("ticket")); ticket != "" {
				detail := landTicket(main, ticket)
				details = append(details, landFirst(strings.TrimSpace(detail.output), strings.TrimSpace(detail.error)))
			}
		}
		return strings.Join(details, "\n")
	}
	joinReason := join()
	if joinReason != "" {
		cancel()
	}
	gates := <-gating
	if joinReason == "" {
		after, err := stamp()
		if err != nil {
			return stop(err.Error())
		}
		if before != after {
			gates = runGates()
		}
	}
	gateReason := ""
	if gates.data.Get("cancelled") != true && (gates.code != 0 || gates.data.Get("allOk") != true) {
		gateReason = gates.error + landFirst(gates.output, "gates did not pass")
	}
	if joinReason != "" {
		if gateReason != "" {
			joinReason += "\ngates: " + gateReason
		}
		return stop(joinReason)
	}
	step = "gates"
	next = closeCommand([]string{"code-forge", "gates", "run", "--run", run, "--block", id, "--changed"})
	if gateReason != "" {
		return stop(gateReason)
	}
	fmt.Fprint(stderr, gates.error)
	step = "close"
	next = closeCommand(append([]string{"code-forge", "block", "close"}, closeArgs[:len(closeArgs)-1]...))
	closed := landCapture(func(s *landStep) int { return blockCloseStepWithReason(closeArgs, s, &closeReason) }, nil)
	if closed.code != 0 || closed.data.Get("status") != "closed" {
		if n, ok := closed.data.Get("next_command").(string); ok {
			next = n
		}
		return stop(landFirst(closed.error, blockString(closed.data.Get("reason")), blockString(closed.data.Get("status")), "close refused"))
	}
	fmt.Fprint(stderr, closed.error)
	step = "merge"
	base := blockString(entry.Get("base_branch"))
	if base == "" {
		return stop("block has no recorded base branch")
	}
	branch := blockString(blockObject(entry.Get("worktree")).Get("branch"))
	command := closeCommand([]string{"git", "-C", main, "merge", "--no-ff", "--no-edit", "--", branch})
	next = command
	if !merge {
		fmt.Fprintln(print, "hint: the merge gate runs only with --merge")
		if json {
			emit(ledger.Object{{Key: "block", Value: id}, {Key: "event", Value: "closed"}, {Key: "merge", Value: command}})
		} else {
			fmt.Fprintf(os.Stdout, "block %s closed; merge: %s\n", id, command)
		}
		return 0
	}
	current, err := gitx.Run(main, "symbolic-ref", "-q", "HEAD")
	if err != nil || strings.TrimSpace(current) != "refs/heads/"+base {
		next = closeCommand([]string{"git", "-C", main, "checkout", base}) + " && " + command
		return stop("main checkout must be on recorded base branch " + base)
	}
	status, err := gitx.Run(main, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return stop(landGitError(err, "cannot check main checkout"))
	}
	if status != "" {
		next = closeCommand([]string{"git", "-C", main, "status", "--short"})
		return stop("main checkout is dirty; commit or stash its changes before merging")
	}
	status, err = gitx.Run(top, "status", "--porcelain", "--untracked-files=all")
	if err != nil || status != "" {
		next = closeCommand([]string{"git", "-C", top, "status", "--short"})
		return stop("block worktree is dirty or unreadable; commit and review its changes before merging")
	}
	if mergeGate {
		step = "merge-gate"
		next = closeCommand([]string{"code-forge", "gates", "run", "--cwd", tree.Workspace})
		started := time.Now()
		failure := landMergeGate(home, blockString(record.Get("project")), run, id, main, tree.Path, workspace, branch, base, entry)
		if failure.elapsed != nil {
			mergeGateMs = *failure.elapsed
		}
		if failure.reason != "" {
			return stop(failure.reason)
		}
		if failure.reusedTree != "" {
			mergeGateTree = failure.reusedTree
			fmt.Fprintf(print, "merge gate: reused green full suite on tree %.12s\n", mergeGateTree)
		} else if failure.gateReused != "" {
			mergeGateTree, mergeGateReason = failure.gateReused, failure.scopeReason
			fmt.Fprintf(print, "merge gate: reused green gate on tree %.12s (gate_reused)\n", mergeGateTree)
		} else if failure.selected == 0 {
			fmt.Fprintf(print, "merge gate: no test selected (%s); test gate skipped\n", failure.scopeReason)
		} else {
			fmt.Fprintf(print, "merge gate: scoped tests passed (%d files, %s, %.1fs)\n", failure.selected, failure.scopeReason, float64(time.Since(started).Milliseconds())/1000)
		}
	} else {
		fmt.Fprintln(stderr, "warning: merge gate skipped; merged full suite was not checked")
	}
	step = "merge"
	next = command
	output, err := gitx.Run(main, "-C", main, "merge", "--no-ff", "--no-edit", "--", branch)
	if err != nil {
		next = closeCommand([]string{"git", "-C", main, "status"})
		return stop(landFirst(landGitError(err, ""), output, "git merge failed; resolve the merge before continuing"))
	}
	// B430: main first; push after the merge (flag --push or land.push).
	if landPushWanted(flags, cfg) {
		step = "push"
		next = closeCommand([]string{"git", "-C", main, "push", "origin", base})
		if _, err := landPush(home, run, []string{id}, main, base, flags["watch-ci"] == true, print); err != nil {
			return stop(err.Error())
		}
	}
	if json {
		landed := ledger.Object{{Key: "block", Value: id}, {Key: "event", Value: "landed"}, {Key: "merge", Value: command}}
		if mergeGateReason != "" {
			landed.Set("merge_gate_reason", mergeGateReason)
		}
		emit(landed)
	} else {
		fmt.Fprintf(os.Stdout, "block %s landed\n", id)
	}
	return 0
}

type landMergeResult struct {
	reason      string
	elapsed     *int64
	reusedTree  string
	gateReused  string // B393: tree of the block's own green gate, reused
	selected    int    // B404: test files the scoped merge gate ran; -1 without a test command
	scopeReason string // import_graph or go_only
}

// The trial merge and the merge gate run in an isolated sibling worktree.
// Cleanup finishes before the checked parent identities are compared or main
// is merged. B404: the gate runs the tests selected from what the merge
// changed (landMergeScope), never the full suite; a green full suite already
// recorded for the merged tree is still reused.
func landMergeGate(home, project, run, id, main, treePath, workspace, branch, base string, entry ledger.Object) (result landMergeResult) {
	mainHead, err := gitx.Run(main, "rev-parse", "--verify", "HEAD")
	if err != nil {
		result.reason = "cannot resolve merge parents"
		return
	}
	blockHead, err := gitx.Run(main, "rev-parse", "--verify", "refs/heads/"+branch)
	if err != nil {
		result.reason = "cannot resolve merge parents"
		return
	}
	prefix, err := gitx.Run(workspace, "rev-parse", "--show-prefix")
	if err != nil {
		result.reason = "cannot resolve merge-gate workspace prefix"
		return
	}
	prefix = strings.TrimSuffix(prefix, "\n")
	if strings.HasPrefix(prefix, "..") || filepath.IsAbs(prefix) {
		result.reason = "merge-gate workspace prefix is outside the repository"
		return
	}
	started := time.Now()
	temporary, err := os.MkdirTemp(filepath.Dir(treePath), ".merge-gate-")
	if err != nil {
		result.reason = err.Error()
		return
	}
	added := false
	func() {
		defer func() {
			elapsed := time.Since(started).Milliseconds()
			result.elapsed = &elapsed
			_, err := gitx.Run(main, "worktree", "remove", "--force", temporary)
			if added && err != nil {
				result.reason = "cannot remove merge-gate worktree " + temporary + ": " + landGitError(err, "")
			}
			_ = os.RemoveAll(temporary)
		}()
		if _, err := gitx.Run(main, "worktree", "add", "--detach", temporary, strings.TrimSpace(mainHead)); err != nil {
			result.reason = landGitError(err, "cannot create merge-gate worktree")
			return
		}
		added = true
		output, err := gitx.Run(temporary, "merge", "--no-ff", "--no-edit", "--", strings.TrimSpace(blockHead))
		if err != nil {
			conflicts, _ := gitx.Run(temporary, "diff", "--name-only", "--diff-filter=U")
			if strings.TrimSpace(conflicts) != "" {
				result.reason = "merge conflict:\n" + strings.TrimSpace(conflicts)
			} else {
				result.reason = landFirst(landGitError(err, ""), output, "trial merge failed")
			}
			return
		}
		mergedTree, err := gitx.Run(temporary, "rev-parse", "HEAD^{tree}")
		if err != nil {
			result.reason = "cannot resolve merge-gate tree"
			return
		}
		mergedTree = strings.TrimSpace(mergedTree)
		if gatesReusableFull(filepath.Join(temporary, prefix)) {
			result.reusedTree = mergedTree
			return
		}
		cwd := filepath.Join(temporary, prefix)
		owned := gates.Strings(entry.Get("owned_files"))
		scope, err := landMergeScope(cwd, "HEAD^1", "HEAD", gates.Strings(entry.Get("test_files")), owned, gatesAllGoOnly(owned))
		if err != nil {
			result.reason = err.Error()
			return
		}
		result.selected, result.scopeReason = -1, scope.Reason
		// B393: the block's last gate already ran green on this exact tree
		// and covered what the merge gate would select, so it is not rerun.
		if last, ok := landLastGate(home, project, run, id, treePath); ok && landGateCovers(last, mergedTree, cwd, scope.Files) {
			result.gateReused, result.scopeReason = mergedTree, "gate_reused"
			return
		}
		fullArgs := []string{"run", "--cwd", cwd}
		options, _ := gatesParse(fullArgs)
		if landMergeSelectable(cwd) {
			options.scope = &scope
			result.selected = len(scope.Files)
		}
		suite := landCapture(func(s *landStep) int {
			code, safe := gatesNativeStep(fullArgs, options, s)
			if !safe {
				return landHandover
			}
			return code
		}, nil)
		result.reason = landMergeFailure(home, run, id, suite, result.selected)
	}()
	if result.reason != "" {
		return
	}
	currentMain, e1 := gitx.Run(main, "rev-parse", "--verify", "HEAD")
	currentBlock, e2 := gitx.Run(main, "rev-parse", "--verify", "refs/heads/"+branch)
	currentBranch, e3 := gitx.Run(main, "symbolic-ref", "-q", "HEAD")
	currentStatus, e4 := gitx.Run(main, "status", "--porcelain", "--untracked-files=all")
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || currentMain != mainHead || currentBlock != blockHead || strings.TrimSpace(currentBranch) != "refs/heads/"+base || currentStatus != "" {
		result.reason = "merge parents or main checkout changed during the merge gate; check the checkout and rerun the merge gate"
	}
	return
}

func landGitError(err error, fallback string) string {
	if e, ok := err.(*gitx.CommandError); ok && e.Stderr != "" {
		return e.Stderr
	}
	return fallback
}

// landGreenGate is a block's last gate result on a clean Git tree (B393).
type landGreenGate struct {
	tree  string
	full  bool     // the test gate ran the full suite
	files []string // test files a scoped test gate ran
	tests bool     // the test gate ran (not skipped)
	gates map[string]bool
}

// landGateRow reads a gate.done row as a green gate, or false when the row is
// red, cancelled, flaky or has no recorded tree.
func landGateRow(row map[string]any) (landGreenGate, bool) {
	g := landGreenGate{gates: map[string]bool{}}
	tree, _ := row["tree"].(string)
	if row["event"] != "gate.done" || row["all_ok"] != true || row["cancelled"] == true || row["flaky"] != nil || tree == "" {
		return g, false
	}
	g.tree = tree
	results, _ := row["results"].([]any)
	for _, value := range results {
		r, ok := value.(map[string]any)
		if !ok || r["ok"] != true || r["flaky"] != nil || r["timed_out"] == true || r["cancelled"] == true {
			return g, false
		}
		name, _ := r["gate"].(string)
		if name == "test" {
			g.tests = r["skipped"] != true
		} else if r["skipped"] == true {
			continue
		}
		g.gates[name] = true
	}
	g.full = g.tests && row["scope"] == "full"
	for _, f := range gates.Strings(row["test_files"]) {
		g.files = append(g.files, f)
	}
	return g, true
}

// landLastGate is the block's last gate.done or gate.cancelled row in its
// worktree for this run, when that row is green on a clean tree.
func landLastGate(home, project, run, id, worktree string) (landGreenGate, bool) {
	if project == "" {
		return landGreenGate{}, false
	}
	rows, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge", "ledger"), project)
	if err != nil {
		return landGreenGate{}, false
	}
	var last map[string]any
	for _, value := range rows {
		row, ok := value.(map[string]any)
		if !ok || (row["event"] != "gate.done" && row["event"] != "gate.cancelled") || row["run"] != run || row["block"] != id || row["worktree"] != worktree {
			continue
		}
		last = row
	}
	if last == nil {
		return landGreenGate{}, false
	}
	return landGateRow(last)
}

// landGateCovers reports that a green block gate already checked what the
// merge gate would run on tree (B393): the same tree, every configured gate
// green, and either the full suite or every selected test file.
func landGateCovers(g landGreenGate, tree, cwd string, files []string) bool {
	if g.tree == "" || g.tree != tree {
		return false
	}
	loaded, err := config.Load(cwd)
	if err != nil {
		return false
	}
	configured := blockMap(loaded["gates"])
	p, safe := gates.Resolve(cwd, configured)
	if !safe || !gates.ResolveCustom(&p, configured) || p.Commands["test"] == nil {
		return false
	}
	for name, command := range p.Commands {
		if command != nil && name != "test" && !g.gates[name] {
			return false
		}
	}
	if g.full {
		return true
	}
	if !gatesSelectable(p) {
		return false
	}
	ran := map[string]bool{}
	for _, f := range g.files {
		ran[f] = true
	}
	required := gatesRunnerFiles(p.Commands["test"].Argv, files)
	if len(required) > 0 && !g.tests {
		return false
	}
	for _, f := range required {
		if !ran[f] {
			return false
		}
	}
	return true
}
