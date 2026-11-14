package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/autopilot"
	"github.com/ricardov03/code-forge/native/blockclose"
	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B622: block close <id> --dry-run and block human-pass <id> --dry-run run
// the verb's checks, write nothing (no ledger row, no run record change, no
// lock left, no breadcrumb, no error log) and print the blocker list: the
// ready --json shape plus dry_run, verb and each blocker's clears_by. Exit 0
// when the verb would pass, 1 when it would not, 2 on usage.

// closeDry is what a close preview collected (see blockCloseReviewedStep).
type closeDry struct {
	blockers     []gatecheck.Blocker
	ownerConfirm bool
	collected    bool
}

// refusal turns a close refusal into one blocker. ok false: not handled.
func (d *closeDry) refusal(err error, stderr io.Writer) (int, bool) {
	var se *state.Error
	if errors.As(err, &se) {
		if se.Code == "usage" {
			fmt.Fprintln(stderr, "block close:", err)
			return 2, true
		}
		d.blockers = append(d.blockers, gatecheck.Blocker{Code: se.Code, Detail: err.Error()})
		return 1, true
	}
	d.blockers = append(d.blockers, gatecheck.Blocker{Code: "error", Detail: blockIOMessage(err)})
	return 1, true
}

// collect keeps every blocker the real close would meet, in Node's preview
// order: the worker pin, the tamper check, each failed check, the orphans.
func (d *closeDry) collect(run, id, event, reason string, checks []closeCheck, record ledger.Object, rows []gatecheck.Row, key []byte) {
	d.collected = true
	if d.ownerConfirm {
		d.blockers = append(d.blockers, gatecheck.Blocker{Code: "owner_confirm", Detail: "close.require_human is set and no autopilot grant is active: only the owner can confirm this close at a terminal"})
	}
	if event != "" {
		d.blockers = append(d.blockers, gatecheck.Blocker{Code: event, Detail: reason})
		if event != "ledger.tamper" {
			if why := blockclose.Tamper(run, id, rows, key); why != "" {
				d.blockers = append(d.blockers, gatecheck.Blocker{Code: "ledger.tamper", Detail: why})
			}
		}
	}
	for _, check := range checks {
		d.blockers = append(d.blockers, check.Blockers...)
	}
	orphans, _ := record.Get("orphans").([]any)
	for _, v := range orphans {
		file := blockString(v)
		d.blockers = append(d.blockers, gatecheck.Blocker{Code: "unowned_change", File: file, Detail: "orphan in run registry: " + file})
	}
}

// dryRunArgs removes one --dry-run from args. found false: no --dry-run.
func dryRunArgs(args []string) (rest []string, found bool, message string) {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			// The verb prints its usage; nothing runs.
			return args, false, ""
		}
	}
	for _, arg := range args {
		switch {
		case arg == "--dry-run":
			if found {
				return nil, true, "--dry-run given more than once"
			}
			found = true
		case strings.HasPrefix(arg, "--dry-run="):
			return nil, true, "--dry-run takes no value"
		default:
			rest = append(rest, arg)
		}
	}
	return rest, found, ""
}

// dryRunPrint prints the preview and returns its exit code.
func dryRunPrint(stdout io.Writer, verb, id string, formatted []any, jsonFlag bool) int {
	ready := len(formatted) == 0
	if jsonFlag {
		data, _ := ledger.Stringify(ledger.Object{{Key: "block", Value: id}, {Key: "dry_run", Value: true}, {Key: "verb", Value: verb}, {Key: "ready", Value: ready}, {Key: "blockers", Value: formatted}}, false)
		fmt.Fprintln(stdout, string(data))
	} else {
		for _, b := range formatted {
			o := b.(ledger.Object)
			fmt.Fprintf(stdout, "%s %s %s\n  fix: %s\n  clears by: %s\n", o.Get("code"), closeDash(o.Get("file")), o.Get("detail"), o.Get("fix"), o.Get("clears_by"))
		}
		if ready {
			fmt.Fprintf(stdout, "block %s %s dry run: would pass (nothing was recorded)\n", id, verb)
		} else {
			fmt.Fprintf(stdout, "block %s %s dry run: would not pass, %d blockers (nothing was recorded)\n", id, verb, len(formatted))
		}
	}
	if ready {
		return 0
	}
	return 1
}

// closeDryRun is block close <id> --run <r> --dry-run [--json] (args without
// --dry-run). It runs the close's read-only boundary and never hands over.
func closeDryRun(args []string) int {
	id, rows, flags, code := closeDryRows(args, os.Stderr)
	if code >= 0 {
		return code
	}
	return dryRunPrint(os.Stdout, "close", id, rows, flags["json"] == true)
}

// closeDryRows runs the close preview and returns its blocker rows (the
// dry-run shape with clears_by) and the block id. code is -1 when rows are valid, else the
// exit code of a usage error (2) or of Node's throw guard (1); its message
// went to stderr. B626 block settle re-derives its blocker list through it.
func closeDryRows(args []string, errOut io.Writer) (id string, rows []any, flags map[string]any, code int) {
	flags, pos, message := closeFlags(args)
	if message == "" {
		for _, flag := range []string{"decide", "auto-claim", "no-require-reviews"} {
			if flags[flag] == true {
				message = "--dry-run cannot be combined with --" + flag
			}
		}
	}
	if message != "" {
		fmt.Fprintln(errOut, "block close:", message)
		return "", nil, flags, 2
	}
	if len(pos) == 0 || pos[0] == "" || blockString(flags["run"]) == "" {
		fmt.Fprintln(errOut, "block close: a block id and --run are required")
		return "", nil, flags, 2
	}
	id = pos[0]
	run := blockString(flags["run"])
	var stderr strings.Builder
	dry := &closeDry{}
	reason := hCloseAutoClaim
	code = 0
	if !closeTextSafe(strings.Join(args, "\n")) {
		code = landHandover
		reason = hCloseUnsupportedFlag
	} else {
		code = blockCloseStepWithReason(args, &landStep{preflight: true, stdout: io.Discard, stderr: &stderr, dry: dry}, &reason)
	}
	if code == 2 {
		fmt.Fprint(errOut, stderr.String())
		return "", nil, flags, 2
	}
	if code == landHandover {
		// The real close would hand this case to Node; Go cannot preview it.
		dry.blockers = append(dry.blockers, gatecheck.Blocker{Code: "dry_run_unsupported", Detail: "the Go dry run cannot judge this close (" + handoverReasons[reason] + "); the real block close decides"})
	} else if code != 0 && len(dry.blockers) == 0 {
		// A refusal printed as text (no TTY and the like): report it as is.
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = fmt.Sprintf("block close exits %d", code)
		}
		dry.blockers = append(dry.blockers, gatecheck.Blocker{Code: "refused", Detail: detail})
	}
	for _, b := range dry.blockers {
		if gatecheck.LandThrows(b) {
			fmt.Fprintln(errOut, "block close: arg.replaceAll is not a function")
			return id, nil, flags, 1
		}
	}
	entry := dryRunEntry(run, id)
	return id, closeBlockersRows(dry.blockers, id, run, flags, entry, true), flags, -1
}

// dryRunEntry is the block's run entry for the fix lines (nil when unread).
func dryRunEntry(run, id string) ledger.Object {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	record, err := state.ReadRun(home, run)
	if err != nil {
		return nil
	}
	return blockObject(blockObject(record.Get("blocks")).Get(id))
}

// humanPassDryRun is block human-pass <id> --run <r> --dry-run [--json]
// [--note <text>] [--proof <path>]: every check of the real verb except the
// owner's terminal question, which a dry run never asks. --note is optional.
func humanPassDryRun(args []string) int {
	usage := func(text string) int {
		fmt.Fprintln(os.Stderr, "block human-pass:", text)
		return 2
	}
	flags, pos, message := blockFlags(args)
	if message != "" {
		return usage(message)
	}
	run := blockString(flags["run"])
	if len(pos) == 0 || pos[0] == "" || run == "" {
		return usage("a block id and --run are required")
	}
	if flags["remove-worktree"] == true {
		return usage("--remove-worktree is a block close flag")
	}
	id := pos[0]
	if len(pos) != 1 {
		return usage("block human-pass needs one block id and --note <text>")
	}
	if note, has := flags["note"]; has {
		if text, ok := note.(string); !ok || humanTrim(text) == "" {
			return usage("block human-pass needs one block id and --note <text>")
		}
	}
	if text, ok := flags["proof"].(string); ok {
		if _, err := os.Stat(text); err != nil {
			return usage("--proof must name an existing directory or file")
		}
	}
	blockers := []gatecheck.Blocker{}
	var entry ledger.Object
	home, err := os.UserHomeDir()
	if err != nil {
		blockers = append(blockers, gatecheck.Blocker{Code: "dry_run_unsupported_human", Detail: "the Go dry run cannot read HOME; the real block human-pass decides"})
	} else if record, err := state.ReadRun(home, run); err != nil {
		var se *state.Error
		code := "error"
		if errors.As(err, &se) {
			code = se.Code
		}
		blockers = append(blockers, gatecheck.Blocker{Code: code, Detail: err.Error()})
	} else {
		entry = blockObject(blockObject(record.Get("blocks")).Get(id))
		blockers = humanPassDryBlockers(home, run, id, record, entry, blockString(flags["note"]))
	}
	return dryRunPrint(os.Stdout, "human-pass", id, closeBlockersRows(blockers, id, run, flags, entry, true), flags["json"] == true)
}

// humanPassDryBlockers mirrors blockHumanPass's checks in its order; the
// grant is read without the expiry write the real verb may make.
func humanPassDryBlockers(home, run, id string, record, entry ledger.Object, note string) []gatecheck.Blocker {
	if entry.Get("status") != "awaiting_human_check" {
		return []gatecheck.Blocker{{Code: "not-awaiting", Detail: fmt.Sprintf("block %s is not awaiting a human check", id)}}
	}
	unsupported := []gatecheck.Blocker{{Code: "dry_run_unsupported_human", Detail: "the Go dry run cannot judge this human-pass (its run state is one the real verb hands to Node); the real block human-pass decides"}}
	if !blockGrantEndSafe(record) || !blockStorageSafe(home, run, record) || !closeTextSafe(note) {
		return unsupported
	}
	checks, ok := entry.Get("human_checks").([]any)
	if !ok {
		return unsupported
	}
	for _, check := range checks {
		if _, ok := check.(string); !ok {
			return unsupported
		}
	}
	if grant := blockObject(record.Get("autopilot")); grant != nil {
		s, err := autopilot.GrantState(grant, time.Now())
		if err != nil || s == "active" {
			return []gatecheck.Blocker{{Code: "autopilot-active", Detail: fmt.Sprintf("block human-pass is refused while an autopilot grant is active on run %s", run)}}
		}
	}
	return nil
}
