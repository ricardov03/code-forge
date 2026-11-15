package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/autopilot"
	"github.com/ricardov03/code-forge/native/blockclose"
	"github.com/ricardov03/code-forge/native/humanask"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

func init() { blockSubs["human-pass"] = runBlockHumanPass }

func humanTrim(s string) string {
	return strings.Trim(s, "\t\n\v\f\r \u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000\ufeff")
}
func humanNoTTY(stderr io.Writer, sub string) int {
	fmt.Fprintf(stderr, "block %s needs the human at a terminal to confirm; nothing was recorded (there is no --yes)\n", sub)
	return 2
}

// humanTTY and humanAsk are the terminal seam of the block verbs' owner
// prompts (tests answer them; production asks the real terminal).
var (
	humanTTY = humanask.Available
	humanAsk = humanask.Confirm
)

func humanConfirm(stderr io.Writer, sub, id, message string) int {
	result, err := humanAsk(message)
	if result == humanask.NoTTY {
		return humanNoTTY(stderr, sub)
	}
	if err != nil || result != humanask.Confirmed {
		fmt.Fprintf(stderr, "block %s %s: cancelled — nothing was recorded\n", id, sub)
		return 1
	}
	return 0
}

type humanDecision struct {
	label string
	row   ledger.Object
}

func runBlockHumanPass(args []string) int {
	// B622: --dry-run previews the human-pass checks; nothing is asked or written.
	if rest, dry, message := dryRunArgs(args); message != "" {
		fmt.Fprintln(safeStderr, "block human-pass:", message)
		return 2
	} else if dry {
		return humanPassDryRun(rest)
	}
	var stdout bytes.Buffer
	stderr := &closeErrorBuffer{}
	delegated := false
	var endHook func()
	code := blockHumanPass(args, &stdout, stderr, &delegated, &endHook)
	if delegated {
		return code
	}
	before := gatesBreadcrumbName("block")
	fmt.Fprint(safeStdout, stdout.String())
	fmt.Fprint(safeStderr, stderr.String())
	if endHook != nil {
		endHook()
	}
	if code != 0 {
		var message any
		if stderr.last != "" {
			message = stderr.last
		}
		nativeVerbFailure("block", "", append([]string{"human-pass"}, args...), code, message, before)
	}
	return code
}

func blockHumanPass(args []string, stdout, stderr io.Writer, delegated *bool, endHook *func()) int {
	fallback := func() int {
		*delegated = true
		return blockHandover(append([]string{"human-pass"}, args...), hBlockUnportedSubcommand)
	}
	fail := func(err error) int {
		code := 1
		if se, ok := err.(*state.Error); ok && se.Code == "usage" {
			code = 2
		}
		// B451: the error prints in Go; runBlockHumanPass logs it as Node does.
		fmt.Fprintln(stderr, "block human-pass:", err)
		return code
	}
	usage := func(s string) int { return fail(&state.Error{Code: "usage", Message: s}) }
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			fmt.Fprint(stdout, blockVerbUsage("human-pass", "<id> --run <r> --note <text> [--proof <dir-or-file>] [--dry-run]"))
			return 0
		}
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
	home, err := os.UserHomeDir()
	if err != nil {
		return fallback()
	}
	record, err := state.ReadRun(home, run)
	if err != nil {
		return fail(err)
	}
	note, ok := flags["note"].(string)
	if len(pos) != 1 || !ok || humanTrim(note) == "" {
		return usage("block human-pass needs one block id and --note <text>")
	}
	var proof any
	if text, ok := flags["proof"].(string); ok {
		if _, err := os.Stat(text); err != nil {
			return usage("--proof must name an existing directory or file")
		}
		proof = text
	}
	if !humanTTY() {
		return humanNoTTY(stderr, "human-pass")
	}
	entry := blockObject(blockObject(record.Get("blocks")).Get(id))
	if entry.Get("status") != "awaiting_human_check" {
		return fail(&state.Error{Code: "not-awaiting", Message: fmt.Sprintf("block %s is not awaiting a human check", id)})
	}
	if !closeGrantEndSafe(record) || !blockStorageSafe(home, run, record) || !closeTextSafe(note) {
		return fallback()
	}
	checks, ok := entry.Get("human_checks").([]any)
	if !ok {
		return fallback()
	}
	for _, check := range checks {
		if _, ok := check.(string); !ok {
			return fallback()
		}
	}
	grant, err := autopilot.CheckExpiryNode(home, run, time.Now())
	if err != nil {
		fmt.Fprintln(stderr, "block human-pass:", blockIOMessage(err))
		return 1
	}
	if grant.State == "active" {
		return fail(&state.Error{Code: "autopilot-active", Message: fmt.Sprintf("block human-pass is refused while an autopilot grant is active on run %s", run)})
	}
	if code := humanConfirm(stderr, "human-pass", id, fmt.Sprintf("Confirm all device checks of block %s (run %s) passed? Only the owner may say yes.", id, run)); code != 0 {
		return code
	}
	var row ledger.Object
	err = state.UpdateBlock(home, run, "", func(current, _ *ledger.Object) ([]ledger.Object, error) {
		blocks := blockObject(current.Get("blocks"))
		entry := blockObject(blocks.Get(id))
		if entry.Get("status") != "awaiting_human_check" {
			return nil, &state.Error{Code: "not-awaiting", Message: fmt.Sprintf("block %s is not awaiting a human check", id)}
		}
		if proof != nil {
			if _, err := os.Stat(proof.(string)); err != nil {
				return nil, &state.Error{Code: "usage", Message: "--proof must name an existing directory or file"}
			}
		}
		if g := blockObject(current.Get("autopilot")); g != nil {
			s, err := autopilot.GrantStateNode(g, time.Now())
			if err != nil || s == "active" {
				return nil, &state.Error{Code: "autopilot-active", Message: fmt.Sprintf("block human-pass is refused while an autopilot grant is active on run %s", run)}
			}
		}
		row = ledger.Object{{Key: "event", Value: "block.human_pass"}, {Key: "block", Value: id}, {Key: "note", Value: note}, {Key: "proof", Value: proof}, {Key: "checks", Value: entry.Get("human_checks")}, {Key: "by", Value: "human"}}
		entry.Set("status", "closed")
		blocks.Set(id, entry)
		current.Set("blocks", blocks)
		return []ledger.Object{row}, nil
	})
	// Publication has begun: errors can never retry through Node.
	if err != nil {
		fmt.Fprintln(stderr, "block human-pass:", blockIOMessage(err))
		var se *state.Error
		if errors.As(err, &se) && se.Code == "usage" {
			return 2
		}
		return 1
	}
	key, _ := blockclose.ReadKey(home, run)
	done := closeWriteDone(home, run, id, record, blockclose.Result{OK: true, Status: "closed", Event: "block.human_pass"}, nil, nil, key, stderr)
	*endHook = func() { blockRunEndHook(home, run, id, "closed", done, record, safeStderr) }
	if flags["json"] == true {
		row.Set("status", "closed")
		blockDoneJSON(&row, record, done)
		data, _ := ledger.Stringify(row, false)
		fmt.Fprintln(stdout, string(data))
	} else {
		fmt.Fprintf(stdout, "block %s closed (human check passed)\n", id)
	}
	return 0
}
