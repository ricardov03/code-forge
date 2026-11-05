package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/daemonclient"
	"github.com/ricardov03/code-forge/native/ledger"
)

var reviewBlockID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var reviewBlockRun = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
var reviewBlockControl = regexp.MustCompile(`[\x00-\x1f\x7f]`)

// Translate only the mode name to reuse B266's strict value-flag parser.
// All ambiguous forms and usage errors retain Node's validation and logging.
func reviewBlockFlags(args []string) (map[string]string, bool) {
	translated := append([]string{}, args...)
	for i, arg := range translated {
		if arg == "--wait" || strings.HasPrefix(arg, "--wait=") {
			return nil, false
		}
		if arg == "--wait-block" || strings.HasPrefix(arg, "--wait-block=") {
			translated[i] = strings.Replace(arg, "--wait-block", "--wait", 1)
		}
	}
	flags, usage, ok := reviewWaitFlags(translated)
	return flags, ok && usage == "" && reviewBlockID.MatchString(flags["wait"]) && reviewBlockRun.MatchString(flags["run"])
}

func runReviewWaitBlock(args []string) int {
	flags, ok := reviewBlockFlags(args)
	if !ok || !daemonclient.Enabled(nil) {
		return reviewWaitHandover(args)
	}
	maxMs := int64(90000)
	if raw, exists := flags["max"]; exists {
		match := reviewWaitMax.FindStringSubmatch(raw)
		if match == nil {
			return reviewWaitHandover(args)
		}
		maxMs, _ = strconv.ParseInt(match[1], 10, 64)
		if match[2] != "ms" {
			maxMs *= 1000
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return reviewWaitHandover(args)
	}
	if dir, exists := flags["cwd"]; exists {
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(cwd, dir)
		}
		cwd = dir
	}
	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		return reviewWaitHandover(args)
	}
	ctx := context.Background()
	root, ok := reviewWaitRoot(ctx, cwd)
	if !ok {
		return reviewWaitHandover(args)
	}
	block, run := flags["wait"], flags["run"]
	// Like waitBlockScope, validate scope/config before starting the deadline,
	// and re-read the workspace mirror on each join (run reload can change it).
	cfg, ok := reviewBlockConfig(ctx, root, cwd, run, block)
	if !ok || !daemonclient.Enabled(cfg) {
		return reviewWaitHandover(args)
	}
	deadline := time.Now().UnixMilli() + maxMs
	for {
		cfg, ok = reviewBlockConfig(ctx, root, cwd, run, block)
		if !ok || !daemonclient.Enabled(cfg) {
			return reviewWaitHandover(args)
		}
		remaining := max(int64(0), deadline-time.Now().UnixMilli())
		answer, err := daemonclient.Request(run, map[string]any{"method": "wait-block", "block": block, "max_ms": remaining}, time.Duration(remaining+2500)*time.Millisecond)
		if err != nil {
			return reviewWaitHandover(args)
		}
		out, ok := reviewBlockAnswer(block, answer)
		if !ok {
			return reviewWaitHandover(args)
		}
		if answer["reason"] == "wait_timeout" && time.Now().UnixMilli() < deadline {
			continue
		}
		// Validate encoding before publishing a breadcrumb or a partial answer.
		data, err := ledger.Stringify(out, false)
		if err != nil {
			return reviewWaitHandover(args)
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

// This is the informational workspace mirror, never the private run record.
// The main tree wins, followed by cwd's ancestors up to its git top level.
func reviewBlockConfig(ctx context.Context, root, cwd, run, block string) (map[string]any, bool) {
	top, ok := reviewBlockGit(ctx, cwd, "rev-parse", "--show-toplevel")
	if !ok {
		return nil, false
	}
	top, err := filepath.EvalSymlinks(top)
	if err != nil {
		return nil, false
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
		blocks, _ := record.Get("blocks").(ledger.Object)
		entry, _ := blocks.Get(block).(ledger.Object)
		owned, owns := entry.Get("owned_files").([]any)
		if !ok || record.Get("run_id") != run || !owns || len(owned) == 0 {
			return nil, false
		}
		if entry.Get("worktree") != nil && !reviewBlockTree(ctx, root, record, entry) {
			return nil, false
		}
		// configInForce returns null for pre-snapshot runs; it does not load
		// the current project YAML as statusConfig's legacy fallback does.
		state, _ := record.Get("config").(ledger.Object)
		if _, snapshot := state.Get("hash").(string); !snapshot {
			return nil, true
		}
		return statusConfig(record)
	}
	return nil, false
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
		if !ok || file["outcome"] == "stopped" {
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
