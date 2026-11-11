package main

import (
	"fmt"
	"os"
	"strings"
)

// B451: block open, claim, stop, close and land never start Node. A case the
// Go engine does not serve ends here, before any state changes, with one
// stderr line that names the cause, the old hand-over code and the fix.

// blockRefusalCauses names each former hand-over case of these verbs.
var blockRefusalCauses = map[handoverReason]string{
	hOpenUnsupportedFlag:    "a flag block open does not take",
	hOpenNoLevel:            "no --level and no level the run can pick",
	hOpenJsSemantics:        "a block id or --level the run file cannot hold",
	hOpenHome:               "the home directory cannot be found",
	hOpenState:              "the run file or ledger is in a form the Go engine cannot write",
	hOpenAcceptanceFormat:   "the --acceptance file is not a YAML/JSON list the Go engine can read",
	hOpenBriefResolve:       "the --brief path cannot be resolved",
	hOpenBriefAbsolute:      "the --brief path cannot be made absolute",
	hOpenBriefRelative:      "the --brief path cannot be placed in the workspace",
	hOpenBriefRead:          "the --brief file cannot be read",
	hOpenWorktreeSettings:   "proof.worktree.dir or proof.worktree.base is set to an empty value",
	hOpenAfter:              "an --after block in a state the Go engine cannot check",
	hOpenAfterOutcome:       "an --after-outcome step in a form the Go engine cannot check",
	hOpenStateChanged:       "the run changed while block open was checking it",
	hOpenAutopilotGrant:     "an autopilot grant in a form the Go engine cannot check",
	hOpenPendingApproval:    "a pending autopilot approval",
	hOpenEndHook:            "an autopilot end hook",
	hClaimStopError:         "an error the Go engine cannot report",
	hClaimStopHome:          "the home directory cannot be found",
	hClaimStopState:         "the run file is in a form the Go engine cannot write",
	hClaimStopStorage:       "the run file or ledger is not a regular writable file",
	hClaimStopStateChanged:  "the run changed while the command was checking it",
	hStopDaemon:             "a code-forge daemon socket is present",
	hStopDoneFacts:          "spend or findings rows the Go engine cannot count",
	hClaimAutopilotGrant:    "an autopilot grant in a form the Go engine cannot check",
	hClaimPendingApproval:   "a pending autopilot approval",
	hClaimEndHook:           "an autopilot end hook",
	hStopAutopilotGrant:     "an autopilot grant in a form the Go engine cannot check",
	hStopPendingApproval:    "a pending autopilot approval",
	hStopEndHook:            "an autopilot end hook",
	hCloseError:             "an error the Go engine cannot report",
	hCloseUnsupportedFlag:   "an argument that looks like a secret token",
	hCloseHome:              "the home directory cannot be found",
	hCloseStateStorage:      "the run file or ledger is not a regular writable file",
	hCloseConfig:            "the run's config snapshot cannot be read",
	hCloseHuman:             "close.require_human with --auto-claim at a terminal",
	hCloseExternalReview:    "an external reviewer inside a composed close",
	hCloseLight:             "a light block path the diff reader refuses (a pattern, secret-like or unreadable path)",
	hCloseHumanChecksShape:  "human_checks in the run file is not a list",
	hCloseHumanChecksValue:  "human_checks in the run file holds a value that is not text",
	hCloseRepo:              "the workspace is not a git repository",
	hCloseWorktreeShape:     "the block's worktree record is malformed",
	hCloseWorktreeResolve:   "the block's worktree path cannot be resolved",
	hCloseWorktreeStat:      "the block's worktree path is not a directory",
	hCloseWorktreeBranch:    "the block's worktree is not on its recorded branch",
	hCloseWorktreeCommonDir: "the block's worktree git directory cannot be read",
	hCloseWorktreeIdentity:  "the block's worktree belongs to another repository",
	hCloseWorktreePrefix:    "the workspace folder is missing in the block's worktree",
	hCloseLedgerRead:        "the ledger cannot be read",
	hCloseLedgerShape:       "a ledger row is not a JSON object",
	hCloseGatesFormat:       "ledger rows the Go close gate cannot judge (rule_break, late finding, review cap, agent waiver or decision rows)",
	hCloseTranscriptRead:    "the transcript cannot be read",
	hCloseTranscriptFormat:  "a transcript the Go scanner cannot read (a Codex log, invalid UTF-8, or a reviews path)",
	hCloseHighPaths:         "the high-risk path list cannot be read",
	hCloseChangedFiles:      "the changed files cannot be listed",
	hCloseFileSet:           "the review file set cannot be built",
	hCloseRequeue:           "a stopped review that may requeue",
	hCloseQueueRefresh:      "the review queue index cannot be refreshed",
	hCloseQueueIdentity:     "a review ticket identity cannot be built",
	hCloseReportRedaction:   "the --report text holds a secret-like token or the run key",
	hCloseCoderTests:        "a coder tests line the Go engine cannot read",
	hCloseTestGate:          "a gates.test setting the Go engine cannot check",
	hCloseExcludedFile:      "a changed file under a review exclusion rule",
	hCloseQueuePins:         "a review queue pin the Go engine cannot check",
	hCloseAutoClaim:         "--auto-claim with an autopilot record",
	hCloseDecide:            "a --decide case the Go engine cannot decide",
	hCloseNoRequireReviews:  "--no-require-reviews at a terminal",
	hCloseAutopilotGrant:    "an autopilot grant in a form the Go engine cannot check",
	hClosePendingApproval:   "a pending autopilot approval",
	hCloseEndHook:           "an autopilot end hook",
	hLandHome:               "the home directory cannot be found",
	hLandReadRun:            "the run file cannot be read",
	hLandEntryShape:         "the block's entry in the run file is malformed",
	hLandStatusShape:        "the block status in the run file is malformed",
	hLandStateStorage:       "the run file or ledger is not a regular writable file",
	hLandWorktreeRepo:       "the block's worktree is not a git repository",
	hLandMainRepo:           "the workspace is not a git repository",
	hLandConfigDaemon:       "the run's config snapshot is unreadable or turns the daemon off",
	hLandMergeGate:          "a gates setting the Go merge gate cannot run",
	hLandStepHandoverJoin:   "a review join the Go engine cannot serve",
	hLandGatesParse:         "gates run arguments the Go engine cannot parse",
	hLandClaimFiles:         "auto-claim files that cannot be listed",
	hLandStepHandoverClose:  "a close case the Go engine cannot serve",
	hLandStepHandoverClaim:  "an auto-claim the Go engine cannot write",
	hLandAutopilotGrant:     "an autopilot grant in a form the Go engine cannot check",
	hLandPendingApproval:    "a pending autopilot approval",
	hLandEndHook:            "an autopilot end hook",
}

// blockRefusalFix is the one next step each refusal names.
func blockRefusalFix(sub string, reason handoverReason) string {
	switch reason {
	case hOpenHome, hClaimStopHome, hCloseHome, hLandHome:
		return "set HOME to your home directory and run it again"
	case hOpenStateChanged, hClaimStopStateChanged:
		return "nothing was written; run it again"
	case hOpenUnsupportedFlag:
		return "drop the flag; code-forge block open --help lists the flags"
	case hOpenWorktreeSettings:
		return "remove the empty proof.worktree setting or give it a value, then open again"
	case hStopDaemon:
		return "stop the daemon (code-forge daemon stop) and run it again"
	case hCloseNoRequireReviews:
		return "run the block's reviews (code-forge review-file --wait-block) and close without --no-require-reviews"
	case hCloseAutoClaim:
		return "claim the changed files with code-forge block claim, then close without --auto-claim"
	case hCloseHuman:
		return "close without --auto-claim after claiming the files with code-forge block claim"
	case hCloseReportRedaction:
		return "remove the token from the --report file and close again"
	case hCloseUnsupportedFlag:
		return "remove the token from the arguments and close again"
	}
	return "nothing was written; send this line to the code-forge maintainers so the Go engine can serve it"
}

// blockRefusalLine is the stderr text of a refusal, without its newline.
func blockRefusalLine(sub string, reason handoverReason) string {
	code := ""
	if int(reason) >= 0 && int(reason) < len(handoverReasons) {
		code = handoverReasons[reason]
	}
	cause, ok := blockRefusalCauses[reason]
	if !ok {
		cause = "a case the Go engine does not serve"
	}
	return fmt.Sprintf("block %s: refused: %s (%s); %s", sub, cause, code, blockRefusalFix(sub, reason))
}

// blockRefuse prints a refusal and logs it as a failed block verb, exit 1.
// land logs as Node's router does: no sub name, the sub first in the args.
func blockRefuse(sub string, args []string, reason handoverReason) int {
	text := blockRefusalLine(sub, reason)
	logSub, logArgs, crumb := sub, args, "block "+sub
	if sub == "land" {
		logSub, logArgs, crumb = "", append([]string{"land"}, args...), "block"
	}
	before := gatesBreadcrumbName(crumb)
	fmt.Fprintln(os.Stderr, text)
	nativeVerbFailure("block", logSub, logArgs, 1, strings.TrimSpace(text), before)
	return 1
}
