package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/daemon"
	"github.com/ricardov03/code-forge/native/daemonclient"
	"github.com/ricardov03/code-forge/native/ledger"
)

var reviewBlockID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var reviewBlockRun = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
var reviewBlockControl = regexp.MustCompile(`[\x00-\x1f\x7f]`)

// reviewWaitBlockStep joins a block for a land or close step through the run
// daemon only. Any other answer returns landHandover to the Node step.
func reviewWaitBlockStep(args []string, step *landStep) int {
	stdout, _ := step.writers()
	flags, paths, usage := reviewEnqueueFlags(args)
	if usage != "" || len(paths) > 0 || !daemonclient.Enabled(nil) {
		return landHandover
	}
	for key := range flags {
		if key != "wait-block" && key != "run" && key != "max" && key != "cwd" {
			return landHandover
		}
	}
	block, run := flags["wait-block"], flags["run"]
	maxWait, valid := reviewParseMax(flags["max"], reviewHas(flags, "max"))
	if !reviewBlockID.MatchString(block) || !reviewBlockRun.MatchString(run) || !valid {
		return landHandover
	}
	cwd, err := os.Getwd()
	if err != nil {
		return landHandover
	}
	if dir, exists := flags["cwd"]; exists {
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(cwd, dir)
		}
		cwd = dir
	}
	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		return landHandover
	}
	ctx := context.Background()
	root, ok := reviewWaitRoot(ctx, cwd)
	if !ok {
		return landHandover
	}
	cfg, _, scope := reviewBlockConfig(ctx, root, cwd, run, block)
	if scope != reviewScopeOK || !daemonclient.Enabled(cfg) {
		return landHandover
	}
	if step.preflight {
		_, err := daemonclient.Request(run, map[string]any{"method": "ping"}, 6500*time.Millisecond)
		if err != nil {
			return landHandover
		}
		return 0
	}
	deadline := time.Now().Add(maxWait)
	for {
		cfg, _, scope = reviewBlockConfig(ctx, root, cwd, run, block)
		if scope != reviewScopeOK || !daemonclient.Enabled(cfg) {
			return landHandover
		}
		remaining := max(int64(0), time.Until(deadline).Milliseconds())
		answer, err := daemonclient.Request(run, map[string]any{"method": "wait-block", "block": block, "max_ms": remaining}, time.Duration(remaining+2500)*time.Millisecond)
		if err != nil || reviewBlockStopped(answer) {
			return landHandover
		}
		out, ok := reviewBlockAnswer(block, answer)
		if !ok {
			return landHandover
		}
		if answer["reason"] == "wait_timeout" && time.Now().Before(deadline) {
			continue
		}
		data, err := ledger.Stringify(out, false)
		if err != nil {
			return landHandover
		}
		fmt.Fprintln(stdout, string(data))
		if answer["status"] == "worker_down" {
			return 3
		}
		return 0
	}
}

// reviewWaitBlockDirect is runReviewFile's --wait-block mode and waitBlock:
// the run daemon answers when it can; when it is disabled, unreachable,
// answers with an invalid envelope or reports a stopped review, the join runs
// here without it, as Node's local loop does.
func reviewWaitBlockDirect(args []string, flags map[string]string, paths []string, cwd, root string) int {
	bad := len(paths) > 0 || flags["fresh"] == "true" || !reviewHas(flags, "run")
	for _, key := range []string{"block", "wait", "answers", "of"} {
		bad = bad || reviewHas(flags, key)
	}
	if bad {
		return reviewWaitUsageError(args, "--wait-block takes a block, --run and --max only")
	}
	block, run := flags["wait-block"], flags["run"]
	if !reviewBlockID.MatchString(block) {
		return reviewWaitUsageError(args, "block id must match /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/")
	}
	if !reviewBlockRun.MatchString(run) {
		return reviewEnqueueSay(args, reviewEnqueueRefusal("bad-run-id", "run id must match /^[a-z0-9][a-z0-9-]{0,63}$/"), 1)
	}
	maxWait, valid := reviewParseMax(flags["max"], reviewHas(flags, "max"))
	if !valid {
		return reviewWaitUsageError(args, "--max must look like 90s or 1500ms")
	}
	ctx := context.Background()
	real, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return reviewWaitHandover(args, hWaitBlockConfig)
	}
	readScope := func() (map[string]any, string, int) {
		cfg, file, scope := reviewBlockConfig(ctx, root, real, run, block)
		switch scope {
		case reviewScopeUnreadable:
			return nil, "", reviewEnqueueSay(args, reviewEnqueueRefusal("block-scope-unreadable", "the block scope cannot be read"), 1)
		case reviewScopeUnsupported:
			return nil, "", reviewWaitHandover(args, hWaitBlockConfig)
		}
		return cfg, file, -1
	}
	// waitBlockScope validates the scope before the deadline starts, then
	// re-reads the workspace mirror on each join (run reload can change it).
	if _, _, code := readScope(); code >= 0 {
		return code
	}
	home, _ := os.UserHomeDir()
	live := func() (bool, bool) {
		w, ok := reviewEnqueueWorker(root)
		return ok && w != nil && w.Get("run") == run, ok
	}
	deadline := time.Now().Add(maxWait)
	for {
		cfg, scopeFile, code := readScope()
		if code >= 0 {
			return code
		}
		var answer map[string]any
		if daemonclient.Enabled(cfg) {
			remaining := max(int64(0), time.Until(deadline).Milliseconds())
			answer, _ = daemonclient.Request(run, map[string]any{"method": "wait-block", "block": block, "max_ms": remaining}, time.Duration(remaining+2500)*time.Millisecond)
			if reviewBlockStopped(answer) {
				answer = nil
			}
			if answer != nil && answer["reason"] == "wait_timeout" && time.Now().Before(deadline) {
				continue
			}
		}
		if answer == nil {
			var failure string
			answer, failure = daemon.Join(daemon.JoinOptions{Home: home, Repo: root, Scope: scopeFile, Run: run, Block: block, Deadline: deadline, Live: live,
				Sleep: func(pause time.Duration) bool { time.Sleep(pause); return true }})
			switch failure {
			case "":
			case "block-scope-unreadable":
				return reviewEnqueueSay(args, reviewEnqueueRefusal("block-scope-unreadable", "the block scope cannot be read"), 1)
			case "bad-ref":
				// changedFiles → treeAtOpen refuses a base that is no commit id.
				return reviewEnqueueSay(args, reviewEnqueueRefusal("bad-ref", "the block base must be a commit id"), 1)
			case "stopped":
				return reviewWaitHandover(args, hWaitBlockStopped)
			default:
				return reviewWaitHandover(args, hWaitBlockLocal)
			}
		}
		out, ok := reviewBlockAnswer(block, answer)
		if !ok {
			return reviewWaitHandover(args, hWaitBlockAnswer)
		}
		// Validate encoding before publishing a breadcrumb or a partial answer.
		data, err := ledger.Stringify(out, false)
		if err != nil {
			return reviewWaitHandover(args, hWaitBlockEncoding)
		}
		before := reviewWaitBreadcrumb()
		fmt.Fprintln(os.Stdout, string(data))
		if answer["status"] == "worker_down" {
			reviewWaitFailure(args, 3, nil, before)
			return 3
		}
		return 0
	}
}

// reviewBlockStopped: Node leaves a daemon answer naming a stopped review to
// its own join, which may restart it with a signed reopen.
func reviewBlockStopped(answer map[string]any) bool {
	files, _ := answer["files"].([]any)
	for _, value := range files {
		if file, _ := value.(map[string]any); file["outcome"] == "stopped" {
			return true
		}
	}
	return false
}

const (
	reviewScopeOK = iota
	reviewScopeUnreadable
	reviewScopeUnsupported
)

// This is the informational workspace mirror, never the private run record.
// The main tree wins, followed by cwd's ancestors up to its git top level.
// Like waitBlockScope it returns the mirror's path; a missing, unparsable or
// mismatched scope is unreadable, and shapes only Node reads are unsupported.
func reviewBlockConfig(ctx context.Context, root, cwd, run, block string) (map[string]any, string, int) {
	top, ok := reviewBlockGit(ctx, cwd, "rev-parse", "--show-toplevel")
	if !ok {
		return nil, "", reviewScopeUnsupported
	}
	top, err := filepath.EvalSymlinks(top)
	if err != nil {
		return nil, "", reviewScopeUnsupported
	}
	candidates := []string{root}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if dir != root {
			candidates = append(candidates, dir)
		}
		if dir == top || dir == filepath.Dir(dir) {
			break
		}
	}
	for _, dir := range candidates {
		file := filepath.Join(dir, ".code-forge", "runs", run+".json")
		if _, err := os.Stat(file); os.IsNotExist(err) {
			continue
		}
		record, ok := reviewWaitJSON(file)
		if !ok {
			return nil, "", reviewScopeUnsupported
		}
		blocks, _ := record.Get("blocks").(ledger.Object)
		entry, _ := blocks.Get(block).(ledger.Object)
		owned, owns := entry.Get("owned_files").([]any)
		if record.Get("run_id") != run || !owns || len(owned) == 0 {
			return nil, "", reviewScopeUnreadable
		}
		if entry.Get("worktree") != nil && !reviewBlockTree(ctx, root, record, entry) {
			return nil, "", reviewScopeUnsupported
		}
		// configInForce returns null for pre-snapshot runs; it does not load
		// the current project YAML as statusConfig's legacy fallback does.
		state, _ := record.Get("config").(ledger.Object)
		if _, snapshot := state.Get("hash").(string); !snapshot {
			return nil, file, reviewScopeOK
		}
		cfg, ok := statusConfig(record)
		if !ok {
			return nil, "", reviewScopeUnsupported
		}
		return cfg, file, reviewScopeOK
	}
	return nil, "", reviewScopeUnreadable
}

func reviewBlockGit(ctx context.Context, cwd string, args ...string) (string, bool) {
	env := []string{}
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_") {
			env = append(env, e)
		}
	}
	env = append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	out, ok := reviewWaitCommand(ctx, cwd, "git", args, env, 10*time.Second)
	return strings.TrimSpace(out), ok
}

// Mirror blockTree's refusals before allowing a daemon answer to bypass Node.
func reviewBlockTree(ctx context.Context, root string, record, entry ledger.Object) bool {
	tree, ok := entry.Get("worktree").(ledger.Object)
	path, p := tree.Get("path").(string)
	branch, b := tree.Get("branch").(string)
	base, h := tree.Get("base_sha").(string)
	workspace, w := record.Get("workspace").(string)
	if !ok || !p || !filepath.IsAbs(path) || !b || branch == "" || !h || !regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`).MatchString(base) || !w {
		return false
	}
	if reviewBlockControl.MatchString(path + branch) {
		return false
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		return false
	}
	top, t := reviewBlockGit(ctx, real, "rev-parse", "--show-toplevel")
	actual, a := reviewBlockGit(ctx, real, "symbolic-ref", "--quiet", "--short", "HEAD")
	main, m := reviewWaitRoot(ctx, real)
	spaceMain, s := reviewWaitRoot(ctx, workspace)
	if !t || !a || !m || !s || top != real || actual != branch || main != root || spaceMain != root {
		return false
	}
	spaceTop, s := reviewBlockGit(ctx, workspace, "rev-parse", "--show-toplevel")
	rel, err := filepath.Rel(spaceTop, workspace)
	if !s || err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	info, err := os.Stat(filepath.Join(real, rel))
	return err == nil && info.IsDir()
}

// Request already validates the envelope. Only scalar fields can be reconstructed
// from its maps without losing JSON property order; unusual nested metadata and
// replacement runes (which could have been unpaired surrogates) stay with Node.
func reviewBlockAnswer(block string, answer map[string]any) (ledger.Object, bool) {
	values, filesOK := answer["files"].([]any)
	_, doneOK := answer["done"].(bool)
	if answer["block"] != block || !filesOK || !doneOK {
		return nil, false
	}
	copyFields := func(out *ledger.Object, source map[string]any, keys []string) bool {
		for _, key := range keys {
			v, exists := source[key]
			if !exists {
				continue
			}
			switch scalar := v.(type) {
			case nil, bool, float64:
			case string:
				if strings.ContainsRune(scalar, '\ufffd') {
					return false
				}
			default:
				return false
			}
			out.Set(key, v)
		}
		return true
	}
	files := []any{}
	for _, value := range values {
		file, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		if _, ok := file["file"].(string); !ok {
			return nil, false
		}
		if _, ok := file["status"].(string); !ok {
			return nil, false
		}
		out := ledger.Object{}
		if !copyFields(&out, file, []string{"file", "ticket", "status", "outcome", "rule", "queued_at", "started_at", "finished_at", "duration_ms"}) {
			return nil, false
		}
		files = append(files, out)
	}
	out := ledger.Object{{Key: "block", Value: block}, {Key: "files", Value: files}, {Key: "done", Value: answer["done"]}}
	ok := copyFields(&out, answer, []string{"status", "reason", "next"})
	return out, ok
}
