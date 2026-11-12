package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/ricardov03/code-forge/native/autopilot"
	"github.com/ricardov03/code-forge/native/blockstate"
	"github.com/ricardov03/code-forge/native/humanask"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/rebase"
	"github.com/ricardov03/code-forge/native/state"
)

// B449: every form of `block rebase` runs in Go, in Node's order (src/cli/block.mjs).
// Seams: the owner's terminal and the clock.
var (
	rebaseTTY     = humanask.Available
	rebaseConfirm = humanask.Confirm
	rebaseNow     = time.Now
)

const (
	rebaseForm = "<id> --run <r> [--head <ref>] [--accept-changed <file>… --reason <text>] [--accept-landed]"
)

var (
	rebaseRunID   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	rebaseBlockID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)
	rebaseLineEnd = regexp.MustCompile("[\r\n  ]+")
)

// rebaseFlags is Node's parseFlags with the rebase declarations: the block
// flags plus multi --accept-changed and boolean --accept-landed.
func rebaseFlags(args []string) (map[string]any, []string, string) {
	flags, positionals := map[string]any{}, []string{}
	values := strings.Fields(" run level acceptance brief attempt base lines head worker-pid reason file transcript kind report rule line note proof max ")
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			positionals = append(positionals, arg)
			continue
		}
		name, value, inline := strings.Cut(arg[2:], "=")
		multi := name == "owned" || name == "tests" || name == "accept-changed"
		boolean := name == "json" || name == "remove-worktree" || name == "no-require-reviews" || name == "accept-landed"
		if !multi && !boolean && !slices.Contains(values, name) {
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

// rebaseHandoverError carries the narrow reason a guarded state stays with
// Node (B578); it is raised before anything is kept.
type rebaseHandoverError struct{ reason handoverReason }

func (e *rebaseHandoverError) Error() string { return handoverReasons[e.reason] }

// rebaseRecordShape: the run and block fields the Go rebase reads have the
// types Node's code assumes (B578); other shapes stay with Node.
func rebaseRecordShape(record ledger.Object, id string) bool {
	blocks, ok := record.Get("blocks").(ledger.Object)
	if !ok {
		return false
	}
	for _, field := range blocks {
		if _, ok := field.Value.(ledger.Object); !ok {
			return false
		}
	}
	value := blocks.Get(id)
	if value == nil {
		return true
	}
	block := value.(ledger.Object)
	if wt := block.Get("worktree"); wt != nil {
		object, ok := wt.(ledger.Object)
		if !ok {
			return false
		}
		if _, ok := object.Get("path").(string); !ok {
			return false
		}
		if _, ok := object.Get("branch").(string); !ok {
			return false
		}
	}
	for _, key := range []string{"status", "base_branch", "base_sha"} {
		if v := block.Get(key); v != nil {
			if _, ok := v.(string); !ok {
				return false
			}
		}
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
	return true
}

// rebaseStep runs one `block rebase` call, or returns the reason a guarded
// run state stays with Node before anything was printed or changed.
func rebaseStep(args []string, stdout, stderr io.Writer) (int, handoverReason) {
	var reason handoverReason
	code := rebaseStepBody(args, stdout, stderr, &reason)
	return code, reason
}

func rebaseStepBody(args []string, stdout, stderr io.Writer, handed *handoverReason) int {
	handOver := func(reason handoverReason) int { *handed = reason; return 0 }
	fail := func(err error) int {
		var h *rebaseHandoverError
		if errors.As(err, &h) {
			return handOver(h.reason)
		}
		var se *state.Error
		fmt.Fprintf(stderr, "block rebase: %s\n", err.Error())
		if errors.As(err, &se) && se.Code == "usage" {
			return 2
		}
		return 1
	}
	usage := func(text string) int { return fail(&state.Error{Code: "usage", Message: text}) }
	flags, pos, message := rebaseFlags(args)
	if message != "" {
		return usage(message)
	}
	run := blockString(flags["run"])
	if len(pos) == 0 || pos[0] == "" || run == "" {
		return usage("a block id and --run are required")
	}
	id := pos[0]
	asJSON := flags["json"] == true
	home, err := os.UserHomeDir()
	if err != nil {
		return handOver(hRebaseHome)
	}
	if flags["accept-landed"] == true {
		for name := range flags {
			if name != "run" && name != "json" && name != "accept-landed" {
				return usage("--accept-landed takes one block id, --run and optional --json")
			}
		}
		if len(pos) != 1 {
			return usage("--accept-landed takes one block id, --run and optional --json")
		}
		return rebaseAcceptLanded(home, run, id, asJSON, stdout, stderr)
	}
	if flags["remove-worktree"] == true {
		return usage("--remove-worktree is a block close flag")
	}
	record, err := state.ReadRun(home, run)
	if err != nil {
		var se *state.Error
		if !errors.As(err, &se) {
			return handOver(hRebaseRunRecord)
		}
		return fail(err)
	}
	// B578: grants and approvals run in Go (Node's expire(): checkExpiry,
	// then checkApprovals and its restore). Shapes the Go code does not
	// read, unusual storage and a restore Go does not serve stay with Node,
	// decided before anything is written.
	now := rebaseNow()
	if _, err := autopilot.ReadGrant(record, now); err != nil || errors.Is(autopilot.Approvals(record, now), autopilot.ErrUnsupported) {
		return handOver(hRebaseAutopilotShape)
	}
	if !rebaseRecordShape(record, id) {
		return handOver(hRebaseRecordShape)
	}
	if !blockStorageSafe(home, run, record) {
		return handOver(hRebaseStorage)
	}
	if _, reason := autopilotRestorePlan(home, run, record, now); reason != 0 {
		return handOver(reason)
	}
	if _, err := autopilot.CheckGrantExpiry(home, run, now); err != nil {
		fmt.Fprintf(stderr, "autopilot: WARN the expiry check failed (%s)\n", blockIOMessage(err))
	} else if fresh, err := state.ReadRun(home, run); err == nil && errors.Is(autopilot.Approvals(fresh, now), autopilot.ErrApprovalDue) {
		check, restoreErr, reason := autopilotRestore(home, run, fresh, now)
		if reason != 0 {
			return handOver(reason)
		}
		if restoreErr != nil {
			fmt.Fprintf(stderr, "autopilot: WARN the expiry check failed (%s)\n", blockIOMessage(restoreErr))
		} else {
			fmt.Fprint(stderr, check.text(true))
		}
	}
	accept := []string{}
	if list, ok := flags["accept-changed"].([]string); ok {
		for _, file := range list {
			if !slices.Contains(accept, file) {
				accept = append(accept, file)
			}
		}
	}
	reason, hasReason := flags["reason"].(string)
	if len(accept) > 0 {
		if len(pos) != 1 || !hasReason || humanTrim(reason) == "" || strings.ContainsFunc(reason, unicode.IsControl) {
			return usage("--accept-changed needs one block id and a nonblank --reason without control characters")
		}
		for _, file := range accept {
			if text := blockstate.File(file, true); text != "" {
				return usage(text)
			}
		}
		if !rebaseTTY() {
			return humanNoTTY(stderr, "rebase")
		}
		grant, err := autopilot.CheckGrantExpiry(home, run, rebaseNow())
		if err != nil {
			return fail(err)
		}
		if grant.State == "active" {
			return fail(&state.Error{Code: "autopilot-active", Message: "block rebase --accept-changed is refused while an autopilot grant is active"})
		}
		answer, err := rebaseConfirm(fmt.Sprintf("Accept rebased changes in %s of block %s (run %s)? Only the owner may say yes.", strings.Join(accept, ", "), id, run))
		if answer == humanask.NoTTY {
			return humanNoTTY(stderr, "rebase")
		}
		if err != nil || answer != humanask.Confirmed {
			fmt.Fprintf(stderr, "block %s rebase: cancelled — nothing was recorded\n", id)
			return 1
		}
	}
	// rebaseBlock: a worktree block (or a closed one) rebases its branch.
	record, err = state.ReadRun(home, run)
	if err != nil {
		return fail(err)
	}
	if !rebaseRecordShape(record, id) {
		return handOver(hRebaseRecordShape)
	}
	block := blockObject(blockObject(record.Get("blocks")).Get(id))
	status := blockString(block.Get("status"))
	var oldBase, newBase string
	switch {
	case block.Get("worktree") != nil && status == "open" && len(accept) == 0 && blockString(block.Get("base_branch")) != "":
		// B431: owned files main changed are replayed and queued for re-review.
		result, err := rebase.RunPlain(home, run, id)
		if err != nil {
			fmt.Fprintf(stderr, "block rebase: %s\n", strings.Join(strings.Fields(err.Error()), " "))
			return 1
		}
		if asJSON {
			out := ledger.Object{{Key: "block", Value: id}, {Key: "event", Value: "rebased"}, {Key: "old_base", Value: result.OldBase}, {Key: "new_base", Value: result.NewBase}}
			if len(result.Upstream) > 0 {
				files := []any{}
				for _, file := range result.Upstream {
					files = append(files, file)
				}
				out.Set("upstream_owned", files)
			}
			data, err := ledger.Stringify(out, false)
			if err != nil {
				return 1
			}
			fmt.Fprintln(stdout, string(data))
			return 0
		}
		fmt.Fprintf(stdout, "block %s rebased %s → %s\n", id, short12(result.OldBase), short12(result.NewBase))
		if len(result.Upstream) > 0 {
			fmt.Fprintf(stdout, "upstream_owned: %s (main changed these owned files; queued for re-review)\n", rebase.FileList(result.Upstream))
		}
		return 0
	case block.Get("worktree") != nil || status == "closed" || status == "awaiting_human_check":
		oldBase, newBase, err = rebaseWorktreeBlock(home, run, id, accept, reason)
	case len(accept) > 0:
		return usage("rebase clearance requires a worktree block")
	default:
		head := "HEAD"
		if value, ok := flags["head"].(string); ok {
			head = value
		}
		oldBase, newBase, err = rebaseBaseBlock(home, run, id, head)
	}
	if err != nil {
		return fail(err)
	}
	if asJSON {
		data, _ := ledger.Stringify(ledger.Object{{Key: "block", Value: id}, {Key: "event", Value: "rebased"}, {Key: "old_base", Value: oldBase}, {Key: "new_base", Value: newBase}}, false)
		fmt.Fprintln(stdout, string(data))
		return 0
	}
	fmt.Fprintf(stdout, "block %s rebased %s → %s\n", id, short12(oldBase), short12(newBase))
	return 0
}

// rebaseAcceptLanded is Node's call of the internal block-rebase-landed
// command, run in-process: its stderr reaches the user on one line.
func rebaseAcceptLanded(home, run, id string, asJSON bool, stdout, stderr io.Writer) int {
	if !rebaseRunID.MatchString(run) {
		fmt.Fprintln(stderr, "code-forge-native: block-rebase-landed requires --home <dir> and a valid --run <id>")
		return 2
	}
	if !rebaseBlockID.MatchString(id) {
		fmt.Fprintln(stderr, "code-forge-native: block-rebase-landed requires a valid --block <id>")
		return 2
	}
	result, err := rebase.Run(home, run, id)
	if err != nil {
		text := strings.TrimSpace(rebaseLineEnd.ReplaceAllString("code-forge-native: "+err.Error(), " "))
		fmt.Fprintln(stderr, text)
		return 1
	}
	var line bytes.Buffer
	if err := json.NewEncoder(&line).Encode(result); err != nil {
		return 1
	}
	if asJSON {
		fmt.Fprint(stdout, line.String())
		return 0
	}
	fmt.Fprintf(stdout, "block %s rebased %s → %s\n", id, short12(result.OldBase), short12(result.NewBase))
	return 0
}

// rebaseBaseBlock is rebaseBlock's mutateRun path for a block without a
// worktree: move base_sha to head when the old base is its ancestor and no
// commit in between touched an owned file.
func rebaseBaseBlock(home, run, id, head string) (oldBase, newBase string, err error) {
	err = state.UpdateBlock(home, run, id, func(record, block *ledger.Object) ([]ledger.Object, error) {
		cwd := blockString(record.Get("workspace"))
		if head == "" || strings.HasPrefix(head, "-") {
			var quoted bytes.Buffer
			encoder := json.NewEncoder(&quoted)
			encoder.SetEscapeHTML(false)
			_ = encoder.Encode(head)
			return nil, &state.Error{Code: "bad-ref", Message: fmt.Sprintf("a git ref must be non-empty and not start with \"-\" (got %s)", strings.TrimSuffix(quoted.String(), "\n"))}
		}
		resolved, err := rebaseNodeGit(cwd, nil, "rev-parse", head+"^{commit}")
		if err != nil {
			return nil, err
		}
		resolved = strings.TrimSpace(resolved)
		base := blockString(block.Get("base_sha"))
		if _, err := rebaseNodeGit(cwd, []int{1}, "merge-base", "--is-ancestor", base, resolved); err != nil {
			if errors.Is(err, errRebaseExitOne) {
				return nil, &state.Error{Code: "base-not-ancestor", Message: fmt.Sprintf("block %s: base %s is not an ancestor of %s", id, short12(base), head)}
			}
			return nil, err
		}
		diff, err := rebaseNodeGit(cwd, nil, "diff", "--no-renames", "--name-only", base, resolved)
		if err != nil {
			return nil, err
		}
		paths, _ := block.Get("owned_files").([]any)
		kinds, _ := block.Get("owned_kinds").(ledger.Object)
		conflicts := []string{}
		for _, file := range strings.Split(diff, "\n") {
			if file != "" && rebaseOwns(paths, kinds, file) {
				conflicts = append(conflicts, file)
			}
		}
		if len(conflicts) > 0 {
			return nil, &state.Error{Code: "owned-conflict", Message: fmt.Sprintf("block %s: commits since its base changed owned files: %s", id, strings.Join(conflicts, ", "))}
		}
		oldBase, newBase = base, resolved
		block.Set("base_sha", resolved)
		return []ledger.Object{{{Key: "event", Value: "block.rebase"}, {Key: "block", Value: id}, {Key: "old_base", Value: base}, {Key: "new_base", Value: resolved}, {Key: "changed_reviewed", Value: []any{}}, {Key: "cleared", Value: []any{}}}}, nil
	})
	return oldBase, newBase, err
}
