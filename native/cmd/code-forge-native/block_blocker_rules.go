package main

import (
	"strings"

	"github.com/ricardov03/code-forge/native/autopilot"
	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/ledger"
)

// B622: who clears a close blocker. "owner" means the only fix is a
// human-only verb (block close --decide or --no-require-reviews, waive,
// reopen, amend-acceptance, accept-hit, ack-waivers, unwaive, rebase
// --accept-changed, human-pass, any autopilot verb); everything else the
// orchestrator (an agent) can run itself.
const (
	clearsByOrchestrator = "orchestrator"
	clearsByOwner        = "owner"
)

// blockerFixInput is what a blocker rule may read to build its fix line.
type blockerFixInput struct {
	p       gatecheck.Blocker
	id, run string
	flags   map[string]any
	entry   ledger.Object
}

// blockerRule is one row of the table: who clears the blocker and how. argv
// returns the fix command (nil: no command); text returns a fix text that
// is used when argv gives no command ("" keeps "no automatic fix: <code>").
type blockerRule struct {
	clearsBy string
	argv     func(in blockerFixInput) []string
	text     func(in blockerFixInput, fix string) string
}

func blockerCloseDecide(in blockerFixInput) []string {
	argv := []string{"code-forge", "block", "close", in.id, "--run", in.run, "--decide", "--reason", "<reason>"}
	for _, flag := range []string{"report", "transcript"} {
		if value, ok := in.flags[flag].(string); ok {
			argv = append(argv, "--"+flag, value)
		}
	}
	return argv
}

func blockerWaiveOrReopen(in blockerFixInput) []string {
	if in.p.File == "" {
		return nil
	}
	if in.p.Finding != "" {
		return []string{"code-forge", "block", "waive", in.id, in.p.Finding, "--run", in.run, "--file", in.p.File, "--reason", "<reason>"}
	}
	return []string{"code-forge", "block", "reopen", in.id, "--run", in.run, "--file", in.p.File, "--reason", "<reason>"}
}

func blockerPlainClose(in blockerFixInput) []string {
	return []string{"code-forge", "block", "close", in.id, "--run", in.run}
}

// blockerRules is the one table: the fix command and clears_by of each code.
var blockerRules = map[string]blockerRule{
	"unowned_change": {clearsByOrchestrator, func(in blockerFixInput) []string {
		if in.p.File == "" {
			return nil
		}
		return []string{"code-forge", "block", "claim", in.id, in.p.File, "--run", in.run}
	}, nil},
	"unreviewed": {clearsByOrchestrator, func(in blockerFixInput) []string {
		return []string{"code-forge", "review-file", "--wait-block", in.id, "--run", in.run}
	}, nil},
	"unsigned": {clearsByOrchestrator, func(in blockerFixInput) []string {
		return []string{"code-forge", "review-file", "--wait-block", in.id, "--run", in.run}
	}, nil},
	"no_green_gates": {clearsByOrchestrator, func(in blockerFixInput) []string {
		return []string{"code-forge", "gates", "run", "--run", in.run, "--block", in.id}
	}, nil},
	"light_limits": {clearsByOrchestrator, nil, func(_ blockerFixInput, fix string) string {
		return fix + "; stop this attempt, then reopen with --kind code --attempt <n+1> and the original base, ownership and acceptance, or split the block within light limits"
	}},
	"no_changes": {clearsByOwner, func(in blockerFixInput) []string {
		return []string{"code-forge", "block", "close", in.id, "--run", in.run, "--no-require-reviews"}
	}, nil},
	// B630: the owner grants the scope; the review stays the substitute's.
	substCloseCode: {clearsByOwner, func(in blockerFixInput) []string {
		return []string{"code-forge", "autopilot", "start", "--run", in.run, "--until", "<time>", "--delegate", "L2", "--allow", autopilot.SubstituteScope}
	}, nil},
	"not_approved":             {clearsByOwner, blockerWaiveOrReopen, nil},
	"late_unruled":             {clearsByOwner, blockerWaiveOrReopen, nil},
	"review_cap":               {clearsByOwner, blockerWaiveOrReopen, nil},
	"autopilot_waiver_invalid": {clearsByOwner, blockerWaiveOrReopen, nil},
	"unproven": {clearsByOrchestrator, func(in blockerFixInput) []string {
		test := "<test-file>"
		if tests, ok := in.entry.Get("test_files").([]any); ok && len(tests) > 0 {
			test = blockString(tests[0])
		}
		return []string{"code-forge", "proof", "red-green", in.id, "--run", in.run, "--test", test}
	}, nil},
	"rule_break":        {clearsByOwner, blockerCloseDecide, nil},
	"close-not-routine": {clearsByOwner, blockerCloseDecide, nil},
	"agent-waivers-not-acked": {clearsByOwner, func(in blockerFixInput) []string {
		return []string{"code-forge", "block", "ack-waivers", in.id, "--run", in.run}
	}, nil},
	"lockfile": {clearsByOrchestrator, nil, func(in blockerFixInput, fix string) string {
		lockfile := in.p.File
		if lockfile == "" {
			lockfile = "<lockfile>"
		}
		return fix + "; repair or regenerate " + closeCommand([]string{lockfile}) + " as valid lock JSON with HTTPS resolved URLs on review.lockfile_registries and nonempty integrity for resolved packages"
	}},
	"git-failed": {clearsByOrchestrator, func(in blockerFixInput) []string {
		return []string{"code-forge", "block", "rebase", in.id, "--run", in.run}
	}, nil},
	"stale_base": {clearsByOrchestrator, func(in blockerFixInput) []string {
		return []string{"code-forge", "block", "rebase", in.id, "--run", in.run, "--accept-landed"}
	}, nil},
	"ledger.tamper": {clearsByOrchestrator, func(blockerFixInput) []string {
		return []string{"code-forge", "doctor"}
	}, nil},
	"coder_report_failed": {clearsByOrchestrator, nil, blockerCoderReportFix},
	// The dry-run codes (B622): a close that needs the owner's terminal
	// confirmation, a human-pass refused under an active grant, a human-pass
	// on a block that is not awaiting its check, and a case the Go dry run
	// cannot judge (the real verb then decides).
	"owner_confirm": {clearsByOwner, blockerPlainClose, nil},
	"autopilot-active": {clearsByOwner, func(in blockerFixInput) []string {
		return []string{"code-forge", "autopilot", "stop", "--run", in.run}
	}, nil},
	"not-awaiting": {clearsByOrchestrator, func(in blockerFixInput) []string {
		if in.entry.Get("status") != "open" {
			return nil
		}
		return blockerPlainClose(in)
	}, nil},
	"dry_run_unsupported":       {clearsByOrchestrator, blockerPlainClose, nil},
	"dry_run_unsupported_human": {clearsByOwner, blockerHumanPass, nil},
}

func blockerHumanPass(in blockerFixInput) []string {
	return []string{"code-forge", "block", "human-pass", in.id, "--run", in.run, "--note", "<device check note>"}
}

func blockerCoderReportFix(in blockerFixInput, fix string) string {
	p, id := in.p, in.id
	report := "<coder-report>"
	if value, ok := in.flags["report"].(string); ok {
		report = value
	}
	quoted := closeCommand([]string{report})
	repair := "repair " + quoted + " to address the reported failure"
	if p.File != "" {
		repair = "add the review-file ticket id for " + closeCommand([]string{p.File}) + " to " + quoted
	} else if p.Detail == "coder report FAILED: context.require_coder_tests needs --report" {
		repair = "create a coder report and pass its path with --report"
	} else if p.Detail == "coder report FAILED: --report cannot be read" {
		repair = "restore readable report " + quoted + " or correct --report"
	} else if p.Detail == "coder report FAILED: no ===BLOCK "+id+" COMPLETE=== sentinel" {
		repair = "add ===BLOCK " + id + " COMPLETE=== to " + quoted
	} else if p.Detail == "coder report FAILED: no TESTS line" {
		repair = "add TESTS <command> exit=<n> with the actual test result before ===BLOCK " + id + " COMPLETE=== in " + quoted
	} else if strings.HasPrefix(p.Detail, "coder report FAILED: TESTS line malformed:") {
		repair = "correct the malformed TESTS line to TESTS <command> exit=<n> with the actual test result before ===BLOCK " + id + " COMPLETE=== in " + quoted
	} else if strings.HasPrefix(p.Detail, "coder report FAILED: TESTS exit=") {
		repair = "fix the failing tests, rerun them, and update the TESTS line in " + quoted + " with the actual command and exit code"
	}
	return fix + "; " + repair + "; rerun block ready"
}

// blockerFix is a blocker's fix line and who clears it, from its table row.
// A worker.* code restarts the worker; an unknown code has no automatic fix.
func blockerFix(in blockerFixInput) (fix, clearsBy string) {
	fix = "no automatic fix: " + in.p.Code
	rule, known := blockerRules[in.p.Code]
	if !known && strings.HasPrefix(in.p.Code, "worker.") {
		rule = blockerRule{clearsByOrchestrator, func(in blockerFixInput) []string {
			return []string{"code-forge", "run", "start", "--reattach", "--run", in.run}
		}, nil}
	}
	if rule.clearsBy == "" {
		rule.clearsBy = clearsByOrchestrator
	}
	var argv []string
	if rule.argv != nil {
		argv = rule.argv(in)
	}
	if argv != nil {
		return closeCommand(argv), rule.clearsBy
	}
	if rule.text != nil {
		fix = rule.text(in, fix)
	}
	return fix, rule.clearsBy
}
