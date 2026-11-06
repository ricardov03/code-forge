package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ricardov03/code-forge/native/blockops"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/runstatus"
	"github.com/ricardov03/code-forge/native/state"
)

func init() {
	blockSubs["claim"] = func(args []string) int { return blockClaimStop("claim", args) }
	blockSubs["stop"] = func(args []string) int { return blockClaimStop("stop", args) }
}

func blockObject(v any) ledger.Object { o, _ := v.(ledger.Object); return o }
func blockString(v any) string        { s, _ := v.(string); return s }

// The shared parser's declarations for these two verbs. Unimplemented internal
// flags go to Node before parsing, with the complete original argv.
func blockFlags(args []string) (map[string]any, []string, string) {
	flags, positionals := map[string]any{}, []string{}
	values := " run level acceptance brief attempt base lines head worker-pid reason file transcript kind report rule line note proof max "
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			positionals = append(positionals, arg)
			continue
		}
		name, value, inline := strings.Cut(arg[2:], "=")
		multi := name == "owned" || name == "tests"
		boolean := name == "json" || name == "remove-worktree" || name == "no-require-reviews"
		if !multi && !boolean && !slices.Contains(strings.Fields(values), name) {
			return flags, positionals, "unknown flag " + statusQuote(arg)
		}
		if _, exists := flags[name]; exists && !multi {
			return flags, positionals, "--" + name + " given more than once"
		}
		if boolean {
			if inline {
				return flags, positionals, "--" + name + " takes no value"
			}
			flags[name] = true
		} else if multi {
			collected, _ := flags[name].([]string)
			if inline && value != "" {
				collected = append(collected, value)
			}
			for i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				i++
				collected = append(collected, args[i])
			}
			if len(collected) == 0 {
				return flags, positionals, "--" + name + " needs at least one value"
			}
			flags[name] = collected
		} else {
			if !inline {
				if i+1 == len(args) || strings.HasPrefix(args[i+1], "--") {
					return flags, positionals, "--" + name + " needs a value"
				}
				i++
				value = args[i]
			} else if value == "" {
				return flags, positionals, "--" + name + " needs a value"
			}
			flags[name] = value
		}
	}
	return flags, positionals, ""
}

func blockSnapshot(record ledger.Object) (map[string]any, bool) {
	// Match Node's configInForce: legacy runs have no hook or claim guards.
	// statusConfig's live-workspace fallback belongs to status, not these verbs.
	if _, ok := blockObject(record.Get("config")).Get("hash").(string); !ok {
		return nil, true
	}
	return statusConfig(record)
}
func blockMap(v any) map[string]any { m, _ := v.(map[string]any); return m }

func blockEndSafe(record ledger.Object) bool {
	if !blockops.QuietExpiry(record) {
		return false
	}
	blocks, ok := record.Get("blocks").(ledger.Object)
	if !ok {
		return false
	}
	for _, field := range blocks {
		block, ok := field.Value.(ledger.Object)
		if !ok {
			return false
		}
		files, ok := block.Get("owned_files").([]any)
		if !ok {
			return false
		}
		for _, file := range files {
			if _, ok := file.(string); !ok {
				return false
			}
		}
	}
	orphans, ok := record.Get("orphans").([]any)
	if !ok {
		return false
	}
	for _, orphan := range orphans {
		if _, ok := orphan.(string); !ok {
			return false
		}
	}
	_, ok = blockSnapshot(record)
	return ok
}

// Explain the guarded cases, including the hook boundary retained by land.
func blockStateHandoverReason(record ledger.Object, scope string, fallback handoverReason) handoverReason {
	condition := ""
	if record.Get("autopilot") != nil {
		condition = "autopilot-grant"
	} else if !blockops.QuietExpiry(record) {
		condition = "pending-approval"
	} else if cfg, ok := blockSnapshot(record); ok && blockMap(cfg["hooks"])["on_block_end"] != nil {
		condition = "end-hook"
	}
	if condition != "" {
		return handoverFlagReason(scope, condition, fallback)
	}
	return fallback
}

// Abnormal publication paths stay with Node before a row can be appended.
// In particular a mirror obstruction must never cause a retry after signing.
func blockStorageSafe(home, run string, record ledger.Object) bool {
	workspace, ok := record.Get("workspace").(string)
	if !ok || workspace == "" {
		return false
	}
	slug := blockString(record.Get("project"))
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`).MatchString(slug) {
		return false
	}
	rel, err := filepath.Rel(statusReal(workspace), filepath.Join(statusReal(home), ".code-forge", "runs"))
	if err != nil || !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
		return false
	}
	dirs := []string{filepath.Join(home, ".code-forge", "runs"), filepath.Join(home, ".code-forge", "ledger"), filepath.Join(workspace, ".code-forge", "runs")}
	for _, dir := range dirs {
		for {
			st, err := os.Stat(dir)
			if err == nil {
				if !st.IsDir() || syscall.Access(dir, 3) != nil {
					return false
				}
				break
			}
			if !os.IsNotExist(err) {
				return false
			}
			dir = filepath.Dir(dir)
		}
	}
	for _, file := range []string{
		filepath.Join(home, ".code-forge", "ledger", slug+".jsonl"),
		filepath.Join(workspace, ".code-forge", "runs", run+".json"),
		filepath.Join(workspace, ".code-forge", "runs", run+".json.tmp"),
	} {
		st, err := os.Stat(file)
		if err == nil && (!st.Mode().IsRegular() || syscall.Access(file, 2) != nil) {
			return false
		}
		if err != nil && !os.IsNotExist(err) {
			return false
		}
	}
	return true
}

func blockClaimStop(sub string, args []string) int {
	original := append([]string{sub}, args...)
	fallback := func(reason handoverReason) int { return blockHandover(original, reason) }
	failure := func(err error) int {
		var publication *blockops.PublicationError
		var se *state.Error
		var ge *gitx.Error
		publishing := errors.As(err, &publication)
		isState := errors.As(err, &se)
		// Signer StateErrors occur before append. Once publication may have
		// begun, never execute the mutation a second time through Node.
		if publishing && !isState {
			fmt.Fprintf(os.Stderr, "block %s: %s\n", sub, blockIOMessage(err))
			return 1
		}
		if errors.Is(err, blockops.ErrHandover) {
			return fallback(hClaimStopStateChanged)
		}
		// The native error-log scrubber does not yet cover block messages with
		// arbitrary user paths. Delegate before emitting or changing state.
		if reviewWaitLogging() {
			return fallback(hClaimStopErrorLog)
		}
		if !isState && !errors.As(err, &ge) {
			return fallback(hClaimStopError)
		}
		fmt.Fprintf(os.Stderr, "block %s: %s\n", sub, err.Error())
		if se != nil && se.Code == "usage" {
			return 2
		}
		return 1
	}
	usage := func(message string) int { return failure(&state.Error{Code: "usage", Message: message}) }
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			blockBreadcrumb(sub)
			usage := "<id> <path> --run <r>"
			if sub == "stop" {
				usage = "<id> --run <r> --reason <text>"
			}
			fmt.Fprintf(os.Stdout, "usage: code-forge block %s %s [--json] [--help|-h]\n  --help, -h  Print this usage and exit without changing state.\n  --json      Print one JSON line on success instead of text.\n", sub, usage)
			return 0
		}
	}
	for _, arg := range args {
		name, _, _ := strings.Cut(arg, "=")
		if name == "--auto" || name == "--auto-claim" {
			return fallback(handoverFlagReason(sub, name, hClaimStopAuto))
		}
	}
	flags, positionals, message := blockFlags(args)
	if message != "" {
		return usage(message)
	}
	run := blockString(flags["run"])
	if len(positionals) == 0 || positionals[0] == "" || run == "" {
		return usage("a block id and --run are required")
	}
	id := positionals[0]
	if flags["remove-worktree"] == true {
		return usage("--remove-worktree is a block close flag")
	}
	if sub == "claim" && (len(positionals) != 2 || positionals[1] == "") {
		return usage("block claim needs exactly one path")
	}
	reason, hasReason := flags["reason"].(string)
	if sub == "stop" && !hasReason {
		return usage("block stop needs --reason")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fallback(hClaimStopHome)
	}
	record, err := state.ReadRun(home, run)
	if err != nil {
		return failure(err)
	}
	if !blockEndSafe(record) {
		return fallback(blockStateHandoverReason(record, sub, hClaimStopState))
	}
	if !blockStorageSafe(home, run, record) {
		return fallback(hClaimStopStorage)
	}
	safe := func(current ledger.Object) bool { return blockEndSafe(current) && blockStorageSafe(home, run, current) }
	// A serving daemon can return a different rows snapshot. Let Node negotiate
	// its protocol rather than mixing that snapshot with a direct ledger read.
	if sub == "stop" && os.Getenv("CODE_FORGE_NATIVE") != "0" {
		if _, err := os.Lstat(filepath.Join(home, ".code-forge", "runs", run, "native.sock")); err == nil {
			return fallback(hStopDaemon)
		}
	}
	if sub == "claim" {
		file := positionals[1]
		if err := blockops.ValidateClaim(file); err != nil {
			return failure(err)
		}
		cfg, _ := blockSnapshot(record)
		why := ""
		external := blockMap(blockMap(cfg["review"])["external"])
		if external["required"] == true && strings.TrimSpace(blockString(external["name"])) != "" {
			why = "review.external.required is true"
		} else if blockMap(cfg["proof"])["runners"] != nil {
			why = "proof.runners is set"
		}
		var guarded []string
		guarded, err = blockops.Guarded(record, why)
		if err != nil {
			return failure(err)
		}
		err = blockops.Claim(home, run, id, file, guarded, why, safe)
	} else {
		if _, ok := blockDoneFacts(home, run, id, record); !ok {
			return fallback(hStopDoneFacts)
		}
		err = blockops.Stop(home, run, id, reason, safe)
	}
	if err != nil {
		return failure(err)
	}
	blockBreadcrumb(sub)
	doneFile := ""
	if sub == "stop" {
		doneFile = blockWriteDone(home, run, id, reason, record)
		defer blockRunEndHook(home, run, id, "stopped", doneFile, record, os.Stderr)
	}
	if flags["json"] == true {
		out := ledger.Object{{Key: "block", Value: id}, {Key: "event", Value: "stopped"}}
		if sub == "claim" {
			out.Set("event", "claimed")
			out.Set("path", positionals[1])
		} else {
			blockDoneJSON(&out, record, doneFile)
		}
		data, _ := ledger.Stringify(out, false)
		fmt.Fprintln(os.Stdout, string(data))
	} else if sub == "claim" {
		fmt.Fprintf(os.Stdout, "block %s claimed %s\n", id, positionals[1])
	} else {
		fmt.Fprintf(os.Stdout, "block %s stopped\n", id)
	}
	return 0
}

func blockIOMessage(err error) string {
	var pathErr *os.PathError
	var linkErr *os.LinkError
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return err.Error()
	}
	code := blockErrno(errno)
	why := errno.Error()
	switch errno {
	case syscall.EEXIST:
		why = "file already exists"
	case syscall.EISDIR:
		why = "illegal operation on a directory"
	}
	if errors.As(err, &pathErr) {
		return fmt.Sprintf("%s: %s, %s '%s'", code, why, pathErr.Op, pathErr.Path)
	}
	if errors.As(err, &linkErr) {
		return fmt.Sprintf("%s: %s, %s '%s' -> '%s'", code, why, linkErr.Op, linkErr.Old, linkErr.New)
	}
	return err.Error()
}

// Use the existing status evaluator for spend and findings. Restrict its block
// registry to the requested block, presented as open so ended blocks are counted.
// Unsupported gate-clearing rules trigger delegation before stop changes state.
func blockDoneFacts(home, run, id string, record ledger.Object) (ledger.Object, bool) {
	rows, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge", "ledger"), blockString(record.Get("project")))
	if err != nil {
		return ledger.Object{{Key: "spent_usd", Value: nil}, {Key: "unknown_usd_sessions", Value: nil}, {Key: "findings", Value: nil}}, true
	}
	mapping, err := statusMap(record)
	if err != nil {
		return nil, false
	}
	mapping["blocks"] = map[string]any{id: map[string]any{"status": "open"}}
	all, ok := runstatus.Compute(mapping, rows, home)
	if !ok {
		return nil, false
	}
	mine := []ledger.Row{}
	for _, value := range rows {
		if row, ok := value.(map[string]any); ok && row["block"] == id {
			mine = append(mine, row)
		}
	}
	spend, ok := runstatus.Compute(mapping, mine, home)
	if !ok {
		return nil, false
	}
	blocks, _ := all.Get("blocks").([]any)
	if len(blocks) != 1 {
		return nil, false
	}
	return ledger.Object{{Key: "spent_usd", Value: spend.Get("spent_usd")}, {Key: "unknown_usd_sessions", Value: spend.Get("unknown_usd_sessions")}, {Key: "findings", Value: blockObject(blocks[0]).Get("findings")}}, true
}

var blockCommit = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

func blockWriteDone(home, run, id, reason string, record ledger.Object) string {
	at := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	if id == "." || id == ".." || strings.ContainsAny(id, "/\\\x00") {
		fmt.Fprintf(os.Stderr, "warning: done file not written: block id %s cannot name a done file\n", statusQuote(id))
		return ""
	}
	facts, _ := blockDoneFacts(home, run, id, record)
	var commit any
	workspace := blockString(record.Get("workspace"))
	tree := blockString(blockObject(blockObject(blockObject(record.Get("blocks")).Get(id)).Get("worktree")).Get("path"))
	if tree == "" {
		tree = workspace
	}
	if out, err := gitx.Run(tree, "rev-parse", "--verify", "HEAD"); err == nil && blockCommit.MatchString(strings.TrimSpace(out)) {
		commit = strings.TrimSpace(out)
	}
	out := ledger.Object{{Key: "block", Value: id}, {Key: "run_id", Value: run}, {Key: "status", Value: "stopped"}, {Key: "event", Value: "block.stop"}, {Key: "reason", Value: reason}, {Key: "commit", Value: commit}}
	for _, f := range facts {
		out.Set(f.Key, f.Value)
	}
	out.Set("at", at)
	file := filepath.Join(home, ".code-forge", "runs", run, "blocks", id+".done.json")
	dir := filepath.Dir(file)
	err := os.MkdirAll(dir, 0700)
	if err != nil {
		if st, statErr := os.Stat(dir); statErr == nil && !st.IsDir() {
			err = syscall.EEXIST
		}
	}
	if err == nil {
		tmp := filepath.Join(dir, "."+filepath.Base(file)+"."+strconv.Itoa(os.Getpid())+"."+strconv.FormatInt(time.Now().UnixMilli(), 10)+".tmp")
		defer os.Remove(tmp)
		data, _ := ledger.Stringify(out, false)
		err = os.WriteFile(tmp, append(data, '\n'), 0600)
		if err == nil {
			err = os.Rename(tmp, file)
			if err != nil {
				// libuv reports a directory destination as EISDIR on macOS;
				// Go's rename syscall reports EEXIST for that same operation.
				if st, statErr := os.Stat(file); statErr == nil && st.IsDir() {
					err = syscall.EISDIR
				}
			}
		}
	}
	if err != nil {
		code := "UNKNOWN"
		var errno syscall.Errno
		if errors.As(err, &errno) {
			code = blockErrno(errno)
		}
		fmt.Fprintln(os.Stderr, "warning: done file not written:", code)
		return ""
	}
	return file
}

func blockErrno(err syscall.Errno) string {
	for _, pair := range []struct {
		err  syscall.Errno
		name string
	}{{syscall.EEXIST, "EEXIST"}, {syscall.EACCES, "EACCES"}, {syscall.EPERM, "EPERM"}, {syscall.ENOENT, "ENOENT"}, {syscall.ENOTDIR, "ENOTDIR"}, {syscall.EISDIR, "EISDIR"}, {syscall.ENOSPC, "ENOSPC"}, {syscall.EROFS, "EROFS"}, {syscall.ENAMETOOLONG, "ENAMETOOLONG"}, {syscall.ELOOP, "ELOOP"}} {
		if pair.err == err {
			return pair.name
		}
	}
	return "UNKNOWN"
}

// Node noteVerb's successful block breadcrumb; errors are delegated before this
// write so Node alone owns their scrubbed failure entry.
func blockBreadcrumb(sub string) {
	if !reviewWaitLogging() || os.Getenv("HOME") == "" {
		return
	}
	dir := filepath.Join(os.Getenv("HOME"), ".code-forge", "logs")
	file := filepath.Join(dir, "breadcrumbs.json")
	before := []any{}
	if data, err := os.ReadFile(file); err == nil {
		_ = json.Unmarshal(data, &before)
	}
	next := []any{}
	for _, value := range before {
		if s, ok := value.(string); ok && reviewWaitCrumb.MatchString(s) {
			next = append(next, s)
		}
	}
	next = append(next, "block "+sub)
	if len(next) > 5 {
		next = next[len(next)-5:]
	}
	if os.MkdirAll(dir, 0700) != nil {
		return
	}
	_ = os.Chmod(dir, 0700)
	tmp := file + ".tmp-" + strconv.Itoa(os.Getpid())
	defer os.Remove(tmp)
	data, _ := ledger.Stringify(next, false)
	if os.WriteFile(tmp, append(data, '\n'), 0600) == nil {
		_ = os.Rename(tmp, file)
	}
}
