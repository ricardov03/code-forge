package main

import "testing"

// B464: these reason codes had no Go path left that returned them, so they were
// removed. A merge=union merge of an old branch can bring a line back; this
// test stops that from reaching main. A path that needs one of them again must
// add a new code at the end of the list instead.
var b464RemovedReasons = []string{
	"gates-unported-subcommand",
	"gates-home",
	"gates-unscoped",
	"gates-timeout-radix",
	"gates-timeout-overflow",
	"gates-unsupported-flag",
	"gates-empty-scope",
	"logs-unported-subcommand",
	"logs-help",
	"ledger-unported-subcommand",
	"ledger-error-log",
	"open-error-log",
	"open-help",
	"open-unsupported-flag",
	"open-no-level",
	"open-js-semantics",
	"open-home",
	"open-acceptance-read",
	"open-acceptance-format",
	"open-brief-resolve",
	"open-brief-absolute",
	"open-brief-relative",
	"open-brief-read",
	"open-worktree-settings",
	"claim-stop-error-log",
	"claim-stop-error",
	"claim-stop-auto",
	"stop-daemon",
	"close-error-log",
	"close-acceptance-revision",
	"close-worktree-shape",
	"close-worktree-resolve",
	"close-worktree-stat",
	"close-worktree-prefix",
	"close-ledger-shape",
	"close-transcript-read",
	"land-error-log",
	"land-help",
	"enqueue-worktree-resolve",
	"enqueue-cached-after-publish",
	"close-remove-worktree",
	"close-help",
	"enqueue-of",
	"claim-auto",
	"claim-auto-claim",
	"stop-auto",
	"stop-auto-claim",
	"open-after",
	"open-after-outcome",
	"small-verb-unported",
	"sweep-unported",
	"upgrade-relink",
	"plan-check-unported",
	"upgrade-docs-sync",
	"worker-watch",
	"worker-jev-key",
	"review-flow",
	"autopilot-approve",
	"autopilot-restore",
	"autopilot-restore-reload",
	"autopilot-approve-write",
	"author-session",
	"system-two-session",
	"jev-ask",
	"doctor-live-probes",
	"doctor-full-no-config",
	"run-unported-subcommand",
	"run-reload-record-unreadable",
	"init-tools",
	"doctor-run-root",
	"spawn-codex-step",
	"spawn-facts-cwd",
	"review-worker-handoff",
	"facts-codex-step",
	"facts-brief-encoding",
	"facts-root-in-git",
	"facts-outside-source",
}

func TestB464RemovedReasonsStayRemoved(t *testing.T) {
	if len(b464RemovedReasons) != 77 {
		t.Fatalf("removed list has %d codes, want 77", len(b464RemovedReasons))
	}
	live := map[string]bool{}
	for _, code := range handoverReasons {
		live[code] = true
	}
	for _, code := range b464RemovedReasons {
		if live[code] {
			t.Errorf("reason %q is back in handoverReasons; B464 removed it", code)
		}
	}
	// handoverFlagReason builds codes from a prefix and a condition; the state
	// reasons it can still return stay in the list.
	for _, scope := range []string{"claim", "stop", "open", "close", "land"} {
		for _, cond := range []string{"autopilot-grant", "pending-approval", "end-hook"} {
			if got := handoverFlagReason(scope, cond, hUnrouted); handoverReasons[got] != scope+"-"+cond {
				t.Errorf("handoverFlagReason(%q, %q) = %q, want %q", scope, cond, handoverReasons[got], scope+"-"+cond)
			}
		}
	}
}
