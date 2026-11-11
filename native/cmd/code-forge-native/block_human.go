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
	"github.com/ricardov03/code-forge/native/blockstate"
	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/humanask"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/sign"
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
func humanConfirm(stderr io.Writer, sub, id, message string) int {
	result, err := humanask.Confirm(message)
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

// Ordinary signed finding rows have all the facts needed by Node's decision
// preview. Queue-only descriptions and other decision dialects retain the
// existing close-decide boundary before a prompt or a write.
func humanDecisions(run, id, reason string, gate gatecheck.Result, rows []gatecheck.Row, key []byte) ([]humanDecision, []gatecheck.Row, bool) {
	actions := []humanDecision{}
	preview := append([]gatecheck.Row{}, rows...)
	seen := map[string]bool{}
	for _, b := range gate.Blockers {
		// Cap, autopilot and acknowledgement decisions keep Node's preview.
		if b.Code == "rule_break" || b.Code == "review_cap" || b.Code == "autopilot_waiver_invalid" || b.Code == "agent-waivers-not-acked" {
			return nil, nil, false
		}
		if b.Code != "not_approved" && b.Code != "late_unruled" {
			continue
		}
		if b.File == "" || b.Finding == "" || b.Finding == "proof" {
			continue
		}
		if blockstate.Finding(b.Finding) != "" || blockstate.File(b.File, false) != "" {
			return nil, nil, false
		}
		pair := b.File + "\x00" + b.Finding
		if seen[pair] {
			continue
		}
		seen[pair] = true
		claim, named := "", false
		for _, r := range rows {
			if r["run"] != run || r["block"] != id || r["file"] != b.File {
				continue
			}
			if r["ticket"] != nil {
				return nil, nil, false
			}
			findings := []map[string]any{}
			if (r["event"] == "review.triage" || r["event"] == "review.late_finding") && r["finding"] == b.Finding {
				named = true
			}
			if r["finding"] == b.Finding {
				findings = append(findings, r)
			}
			for _, v := range blockstate.Array(r["open"]) {
				f := blockstate.Map(v)
				if f["id"] == b.Finding {
					if r["event"] == "review.result" {
						named = true
					}
					findings = append(findings, f)
				}
			}
			if gatecheck.Verified(r, key) {
				for _, f := range findings {
					if s, ok := f["claim"].(string); ok {
						claim = s
					}
				}
			}
		}
		if !named {
			return nil, nil, false
		}
		if claim == "" {
			claim = b.Detail
		}
		if !closeTextSafe(claim) {
			return nil, nil, false
		}
		severity := blockstate.Severity(rows, id, b.File, b.Finding, key)
		if severity == "not-recorded" {
			severity = "unknown"
		}
		row := ledger.Object{{Key: "event", Value: "review.waived"}, {Key: "block", Value: id}, {Key: "file", Value: b.File}, {Key: "finding", Value: b.Finding}, {Key: "reason", Value: humanTrim(reason)}, {Key: "by", Value: "human"}}
		signed := row.Map()
		signed["run"] = run
		signed, err := sign.SignRow(signed, key)
		if err != nil {
			return nil, nil, false
		}
		preview = append(preview, signed)
		actions = append(actions, humanDecision{fmt.Sprintf("%s %s %s: %s", b.File, b.Finding, severity, claim), row})
	}
	return actions, preview, true
}

func runBlockHumanPass(args []string) int {
	var stdout bytes.Buffer
	stderr := &closeErrorBuffer{}
	delegated := false
	var endHook func()
	code := blockHumanPass(args, &stdout, stderr, &delegated, &endHook)
	if delegated {
		return code
	}
	before := gatesBreadcrumbName("block")
	fmt.Fprint(os.Stdout, stdout.String())
	fmt.Fprint(os.Stderr, stderr.String())
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
	// B451: a case the Go engine does not serve is refused with its cause;
	// Node never runs. The caller logs the refusal as a failed verb.
	refuse := func(cause string) int {
		fmt.Fprintf(stderr, "block human-pass: refused: %s (%s); %s\n", cause, handoverReasons[hBlockUnportedSubcommand], blockRefusalFix("human-pass", hBlockUnportedSubcommand))
		return 1
	}
	fail := func(err error) int {
		code := 1
		if se, ok := err.(*state.Error); ok && se.Code == "usage" {
			code = 2
		}
		fmt.Fprintln(stderr, "block human-pass:", err)
		return code
	}
	usage := func(s string) int { return fail(&state.Error{Code: "usage", Message: s}) }
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			fmt.Fprint(stdout, blockVerbUsage("human-pass", "<id> --run <r> --note <text> [--proof <dir-or-file>]"))
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
		fmt.Fprintf(stderr, "block human-pass: refused: the home directory cannot be found (%s); %s\n", handoverReasons[hBlockUnportedSubcommand], blockRefusalFix("human-pass", hCloseHome))
		return 1
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
	if !humanask.Available() {
		return humanNoTTY(stderr, "human-pass")
	}
	entry := blockObject(blockObject(record.Get("blocks")).Get(id))
	if entry.Get("status") != "awaiting_human_check" {
		return fail(&state.Error{Code: "not-awaiting", Message: fmt.Sprintf("block %s is not awaiting a human check", id)})
	}
	if !blockGrantEndSafe(record) {
		return refuse(blockRefusalCauses[hCloseAutopilotGrant])
	}
	if !blockStorageSafe(home, run, record) {
		return refuse(blockRefusalCauses[hCloseStateStorage])
	}
	if !closeTextSafe(note) {
		return refuse("the --note text holds a secret-like token or the run key")
	}
	checks, ok := entry.Get("human_checks").([]any)
	if !ok {
		return refuse(blockRefusalCauses[hCloseHumanChecksShape])
	}
	for _, check := range checks {
		if _, ok := check.(string); !ok {
			return refuse(blockRefusalCauses[hCloseHumanChecksValue])
		}
	}
	grant, err := autopilot.CheckExpiry(home, run, time.Now())
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
			s, err := autopilot.GrantState(g, time.Now())
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
	*endHook = func() { blockRunEndHook(home, run, id, "closed", done, record, os.Stderr) }
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
