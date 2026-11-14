package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/queue"
	"github.com/ricardov03/code-forge/native/reviewq"
	"github.com/ricardov03/code-forge/native/state"
	"github.com/ricardov03/code-forge/native/ticket"
)

var reviewEnqueueSafeUnknown = regexp.MustCompile(`^--[A-Za-z][A-Za-z0-9-]{0,24}$`)

func reviewEnqueueFlags(args []string) (map[string]string, []string, string) {
	flags, paths := map[string]string{}, []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			paths = append(paths, arg)
			continue
		}
		name, value, inline := strings.Cut(arg[2:], "=")
		boolean := name == "fresh" || name == "rebase-check"
		switch name {
		case "cwd", "block", "run", "wait", "wait-block", "max", "answers", "of", "fresh", "rebase-check":
		default:
			return flags, paths, "unknown flag " + statusQuote(arg)
		}
		if _, exists := flags[name]; exists {
			return flags, paths, "--" + name + " given more than once"
		}
		if boolean {
			if inline {
				return flags, paths, "--" + name + " takes no value"
			}
			flags[name] = "true"
			continue
		}
		if !inline {
			i++
			if i >= len(args) || strings.HasPrefix(args[i], "--") {
				return flags, paths, "--" + name + " needs a value"
			}
			value = args[i]
		}
		if inline && value == "" {
			return flags, paths, "--" + name + " needs a value"
		}
		flags[name] = value
	}
	return flags, paths, ""
}

func reviewEnqueueSay(args []string, out ledger.Object, code int) int {
	data, err := ledger.Stringify(out, false)
	if err != nil {
		return reviewWaitHandover(args, hEnqueueEncoding)
	}
	before := reviewWaitBreadcrumb()
	fmt.Fprintln(os.Stdout, string(data))
	if code != 0 {
		reviewWaitFailure(args, code, nil, before)
	}
	return code
}

func reviewEnqueueError(args []string, err error) int {
	var pathError *gitx.Error
	if !errors.As(err, &pathError) || pathError.Code != "bad-path" {
		return reviewWaitHandover(args, hEnqueuePathError)
	}
	return reviewEnqueueSay(args, ledger.Object{{Key: "status", Value: "refused"}, {Key: "reason", Value: pathError.Code}, {Key: "message", Value: pathError.Message}}, 1)
}

// Select the same first live registry entry as liveWorker, then its legacy
// announcement. Reuse the wait route's heartbeat and bounded PID probes.
func reviewEnqueueWorker(root string) (ledger.Object, bool) {
	ctx, deadline := context.Background(), time.Now().Add(5*time.Second)
	dir := filepath.Join(root, ".code-forge", "workers")
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return nil, false
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		w, matched := reviewWaitJSON(filepath.Join(dir, entry.Name()))
		if !matched {
			return nil, false
		}
		// Node accepts more Date.parse forms; let it decide those shapes.
		for _, key := range []string{"started_at", "heartbeat_at"} {
			if _, isString := w.Get(key).(string); isString {
				if _, ok := reviewWaitDate(w.Get(key)); !ok {
					return nil, false
				}
			}
		}
		pid, start, shape := reviewWaitWorkerShape(w)
		if !shape || entry.Name() != strconv.Itoa(pid)+"-"+start+".json" {
			continue
		}
		live, ok := reviewWaitWorkerLive(ctx, w, deadline)
		if !ok {
			return nil, false
		}
		if live {
			return w, true
		}
	}
	w, matched := reviewWaitJSON(filepath.Join(root, ".code-forge", "queue", "worker.json"))
	if !matched {
		return nil, false
	}
	pid, _ := w.Get("pid").(float64)
	_, run := w.Get("run").(string)
	if pid <= 1 || pid != float64(int(pid)) || !run {
		return nil, true
	}
	live, ok := reviewWaitWorkerLive(ctx, w, deadline)
	if !live {
		return nil, ok
	}
	return w, ok
}

func reviewEnqueueRecord(home, root, run string) (ledger.Object, bool) {
	if !reviewBlockRun.MatchString(run) {
		return nil, false
	}
	for _, base := range []string{home, root} {
		file := filepath.Join(base, ".code-forge", "runs", run+".json")
		// JSON primitives and arrays are not nullish in readJSON's mirror
		// selection. Do not accidentally replace them with the mirror.
		if data, err := os.ReadFile(file); err == nil && json.Valid(data) {
			trimmed := bytes.TrimSpace(data)
			if trimmed[0] != '{' && string(trimmed) != "null" {
				return nil, false
			}
		}
		record, ok := reviewWaitJSON(file)
		if !ok {
			return nil, false
		}
		if record != nil {
			return record, true
		}
	}
	return nil, true
}

func reviewEnqueueExclusion(home, root, run, file string) (string, bool) {
	if run == "" {
		return reviewq.Exclusion(nil, file)
	}
	record, ok := reviewEnqueueRecord(home, root, run)
	if !ok {
		return "", false
	}
	configState, _ := record.Get("config").(ledger.Object)
	if _, snapshot := configState.Get("hash").(string); !snapshot {
		return reviewq.Exclusion(nil, file)
	}
	cfg, ok := statusConfig(record)
	// A corrupt snapshot disables exclusion; the worker records its refusal.
	if !ok {
		return "", true
	}
	return reviewq.Exclusion(cfg, file)
}

// reviewFileScope is what the review-file dispatcher already settled: the
// parsed flags, the effective cwd and the main tree.
type reviewFileScope struct {
	flags     map[string]string
	paths     []string
	cwd, root string
}

// reviewEnqueueStep queues one file for a land or close step. Steps pass
// generated argv; anything unusual returns landHandover to the Node step.
func reviewEnqueueStep(args []string, step *landStep) int {
	return reviewEnqueueWith(args, step, nil)
}

func reviewEnqueueWith(args []string, step *landStep, pre *reviewFileScope) int {
	stdout, stderr := step.writers()
	reviewWaitHandover := func(args []string, reason handoverReason) int {
		if step != nil {
			return landHandover
		}
		return reviewWaitHandover(args, reason)
	}
	reviewEnqueueSay := func(args []string, out ledger.Object, code int) int {
		if step == nil {
			return reviewEnqueueSay(args, out, code)
		}
		data, err := ledger.Stringify(out, false)
		if err != nil {
			return landHandover
		}
		fmt.Fprintln(stdout, string(data))
		return code
	}

	reviewEnqueueError := func(args []string, err error) int {
		if step == nil {
			return reviewEnqueueError(args, err)
		}
		var pathError *gitx.Error
		if !errors.As(err, &pathError) || pathError.Code != "bad-path" {
			return landHandover
		}
		return reviewEnqueueSay(args, ledger.Object{{Key: "status", Value: "refused"}, {Key: "reason", Value: pathError.Code}, {Key: "message", Value: pathError.Message}}, 1)
	}
	reviewWaitUsageError := func(args []string, message string) int {
		if step == nil {
			return reviewWaitUsageError(args, message)
		}
		fmt.Fprint(stderr, "review-file: "+message+"\n"+reviewWaitUsage)
		return 2
	}
	var flags map[string]string
	var paths []string
	var cwd, root string
	if pre != nil {
		flags, paths, cwd, root = pre.flags, pre.paths, pre.cwd, pre.root
	} else {
		for i, arg := range args {
			if !utf8.ValidString(arg) || i < 2 && (arg == "--help" || arg == "-h") {
				return landHandover
			}
		}
		var usage string
		flags, paths, usage = reviewEnqueueFlags(args)
		if usage != "" {
			return reviewWaitUsageError(args, usage)
		}
		if reviewHas(flags, "wait") || reviewHas(flags, "wait-block") {
			return landHandover
		}
		var err error
		if cwd, err = os.Getwd(); err != nil {
			return landHandover
		}
		if dir, exists := flags["cwd"]; exists {
			if !filepath.IsAbs(dir) {
				dir = filepath.Join(cwd, dir)
			}
			info, err := os.Stat(dir)
			if err != nil || !info.IsDir() {
				return reviewWaitUsageError(args, "--cwd must name an existing directory")
			}
			cwd = filepath.Clean(dir)
		}
		var ok bool
		if root, ok = reviewWaitRoot(context.Background(), cwd); !ok {
			return landHandover
		}
	}
	ctx := context.Background()
	if flags["rebase-check"] == "true" {
		if step != nil {
			return reviewWaitHandover(args, hEnqueueRebaseCheck)
		}
		return reviewEnqueueRebase(args, flags, paths, root)
	}
	_, blockGiven := flags["block"]
	_, maxGiven := flags["max"]
	if len(paths) != 1 || !blockGiven || maxGiven {
		return reviewWaitUsageError(args, "give one path and --block")
	}
	block := flags["block"]
	if !reviewBlockID.MatchString(block) {
		return reviewWaitUsageError(args, "block id must match /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/")
	}
	_, answersGiven := flags["answers"]
	_, ofGiven := flags["of"]
	if flags["fresh"] == "true" && (answersGiven || ofGiven) {
		return reviewWaitUsageError(args, "--fresh takes no --answers or --of")
	}
	top, err := gitx.RepoTop(cwd)
	if err != nil {
		return reviewWaitHandover(args, hEnqueueRepo)
	}
	file, err := reviewq.Normalize(paths[0], cwd, top)
	if err != nil {
		return reviewEnqueueError(args, err)
	}
	byFile := file
	if ofGiven {
		file, err = reviewq.Normalize(flags["of"], cwd, top)
		if err != nil {
			return reviewEnqueueError(args, err)
		}
	}
	worker, ok := reviewEnqueueWorker(root)
	if !ok {
		return reviewWaitHandover(args, hEnqueueWorker)
	}
	run, requested := flags["run"]
	if !requested {
		run, _ = worker.Get("run").(string)
	}
	home := os.Getenv("HOME")
	if home == "" {
		return reviewWaitHandover(args, hEnqueueHome)
	}
	for _, candidate := range []string{byFile, file} {
		rule, ok := reviewEnqueueExclusion(home, root, run, candidate)
		if !ok {
			return reviewWaitHandover(args, hEnqueueExclusion)
		}
		if rule != "" {
			if step != nil && step.preflight {
				return 0
			}
			if step == nil {
				reviewWaitBreadcrumb()
			}
			fmt.Fprintf(stdout, "not reviewed (review.exclude %s): %s\n", rule, candidate)
			return 0
		}
	}
	var answerIDs []string
	if answersGiven || ofGiven {
		if step != nil {
			return reviewWaitHandover(args, hEnqueueAnswers)
		}
		var usage string
		answerIDs, usage = reviewEnqueueAnswersFlags(flags)
		if usage != "" {
			return reviewWaitUsageError(args, usage)
		}
		if file == byFile {
			return reviewEnqueueSay(args, reviewEnqueueRefusal("answers-same-file", file+" cannot answer its own findings: give the file that resolves them"), 1)
		}
	}
	if worker == nil || requested && run != worker.Get("run") {
		return reviewEnqueueSay(args, ledger.Object{{Key: "status", Value: "worker_down"}, {Key: "file", Value: file}}, 3)
	}
	// Only a missing/inaccessible authoritative record falls back to cwd's
	// content tree. An existing record with no worktree selects the main tree.
	private, matched := reviewWaitJSON(filepath.Join(home, ".code-forge", "runs", run+".json"))
	if !matched {
		return reviewWaitHandover(args, hEnqueuePrivateRecord)
	}
	record, readErr := state.ReadRun(home, run)
	tree := top
	if readErr == nil {
		if private == nil {
			return reviewWaitHandover(args, hEnqueueRunMirror)
		}
		tree = root
		blocks, _ := record.Get("blocks").(ledger.Object)
		entry, _ := blocks.Get(block).(ledger.Object)
		if entry.Get("worktree") != nil {
			worktree, refusal, ok := reviewEnqueueTree(ctx, record, entry, block)
			if !ok {
				return reviewWaitHandover(args, hEnqueueWorktree)
			}
			if refusal != nil {
				return reviewEnqueueSay(args, refusal, 1)
			}
			tree = worktree
		}
	} else {
		var stateErr *state.Error
		if !(errors.As(readErr, &stateErr) && stateErr.Code == "no-run") && !os.IsPermission(readErr) {
			// blockTreeOf: a record that is not JSON, or JSON that is not an
			// object, is refused; an array reads as an object with no blocks.
			data, err := os.ReadFile(filepath.Join(home, ".code-forge", "runs", run+".json"))
			var value any
			switch {
			case err != nil:
				return reviewWaitHandover(args, hEnqueueRunRead)
			case !json.Valid(data):
				return reviewEnqueueSay(args, reviewEnqueueRefusal("run-record-unreadable", "the run record of "+run+" cannot be read (corrupt or not a record); nothing was queued"), 1)
			case json.Unmarshal(data, &value) != nil:
				return reviewWaitHandover(args, hEnqueueRunRead)
			}
			if _, isArray := value.([]any); !isArray {
				if _, isObject := value.(map[string]any); isObject {
					return reviewWaitHandover(args, hEnqueueRunRead)
				}
				return reviewEnqueueSay(args, reviewEnqueueRefusal("run-record-unreadable", "the run record of "+run+" cannot be read (corrupt or not a record); nothing was queued"), 1)
			}
			tree = root
		}
	}
	if err := reviewq.Inside(tree, file); err != nil {
		return reviewEnqueueError(args, err)
	}
	if answersGiven || ofGiven {
		if err := reviewq.Inside(tree, byFile); err != nil {
			return reviewEnqueueError(args, err)
		}
		out, code, supported := reviewEnqueueAnswers(home, root, run, block, tree, file, byFile, answerIDs)
		if !supported {
			return reviewWaitHandover(args, hEnqueueAnswers)
		}
		return reviewEnqueueSay(args, out, code)
	}
	hash, err := gitx.ContentHash(tree, file)
	if err != nil {
		return reviewEnqueueError(args, err)
	}
	p := ticket.Parts{Run: run, Block: block, File: file, ContentHash: hash}
	if tree != root {
		p.Tree = &tree
	}
	identityRecord, ok := reviewEnqueueRecord(home, root, run)
	if !ok {
		return reviewWaitHandover(args, hEnqueueIdentityRecord)
	}
	blocks, _ := identityRecord.Get("blocks").(ledger.Object)
	entry, _ := blocks.Get(block).(ledger.Object)
	if revision, ok := entry.Get("acceptance_revision").(string); ok {
		p.AcceptanceRevision = &revision
	}
	dir := filepath.Join(root, ".code-forge", "queue")
	// reviewIdentity: ordinary enqueues reuse the latest reopened attempt.
	p, id, err := queue.Resolve(dir, p, nil)
	if err != nil {
		return reviewWaitHandover(args, hEnqueueIdentity)
	}
	// Preflight every fallback before publishing a ticket. In particular a
	// land preflight cannot consume a cached result's rename claim.
	if !reviewEnqueueCacheSupported(root, id, identityRecord) {
		return reviewWaitHandover(args, hEnqueueCached)
	}
	reviewFreshSubst = flags["fresh"] == "true" && step == nil && apdgSubstRevertedRun(home, run, block, file, hash) // B635
	if flags["fresh"] == "true" || !reviewEnqueueCached(root, id) {
		if step != nil {
			return reviewWaitHandover(args, hEnqueueFresh)
		}
		out, code := reviewEnqueueFresh(root, p, id, flags["fresh"] != "true")
		return reviewEnqueueSay(args, out, code)
	}
	if step != nil && step.preflight {
		return 0
	}
	if err := queue.Publish(dir, p, id, time.Now()); err != nil {
		return reviewWaitHandover(args, hEnqueuePublish)
	}
	if !reviewEnqueueCached(root, id) {
		// A worker can finish between publication and the cache read. All
		// byte-checked claims remain native after the first queue write.
		out, code := reviewEnqueueFresh(root, p, id, true)
		return reviewEnqueueSay(args, out, code)
	}
	status := "queued"
	if _, err := os.Stat(filepath.Join(dir, id+".done")); err == nil {
		status = "done"
	}
	out := ledger.Object{{Key: "ticket", Value: id}, {Key: "status", Value: status}, {Key: "file", Value: file}, {Key: "content_hash", Value: hash}}
	if p.Tree != nil {
		out.Set("tree", *p.Tree)
	}
	if p.AcceptanceRevision != nil {
		out.Set("acceptance_revision", *p.AcceptanceRevision)
	}
	if p.ReopenedAt != nil {
		out.Set("reopened_at", *p.ReopenedAt)
	}
	return reviewEnqueueSay(args, out, 0)
}

func reviewEnqueueCached(root, id string) bool {
	marker := filepath.Join(root, ".code-forge", "queue", id+".done")
	m, ok := reviewWaitJSON(marker)
	if !ok {
		return false
	}
	if m.Get("status") == "refused" && m.Get("reason") == "superseded" {
		return false
	}
	r, ok := reviewWaitResult(root, marker)
	if !ok {
		return false
	}
	result, _ := r.(ledger.Object)
	return result.Get("status") != "unavailable" && result.Get("status") != "split_required" && result.Get("stopped") != "split_required"
}
