package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type handoverRow struct {
	At      string `json:"at"`
	Verb    string `json:"verb"`
	Sub     string `json:"sub"`
	Reason  string `json:"reason"`
	Version string `json:"version"`
}

// Stable public reason vocabulary. Keep spellings here; callers use typed keys.
type handoverReason int

const (
	hUnrouted handoverReason = iota
	hBlockUnportedSubcommand
	hWaitResult
	hWaitSummary
	hWaitWorker
	hGatesUnportedSubcommand
	hGatesHome
	hGatesUnscoped
	hGatesTimeoutRadix
	hGatesTimeoutOverflow
	hGatesUnsupportedFlag
	hGatesEmptyScope
	hGatesCustom
	hLogsUnportedSubcommand
	hLogsHelp
	hLogsFlagsOrHome
	hLogsDecode
	hLogsFormat
	hLedgerUnportedSubcommand
	hLedgerTailFormat
	hLedgerErrorLog
	hOpenErrorLog
	hOpenHelp
	hOpenUnsupportedFlag
	hOpenNoLevel
	hOpenJsSemantics
	hOpenHome
	hOpenState
	hOpenAcceptanceRead
	hOpenAcceptanceFormat
	hOpenBriefResolve
	hOpenBriefAbsolute
	hOpenBriefRelative
	hOpenBriefRead
	hOpenWorktreeSettings
	hClaimStopErrorLog
	hClaimStopError
	hClaimStopAuto
	hClaimStopHome
	hClaimStopState
	hClaimStopStorage
	hStopDaemon
	hStopDoneFacts
	hCloseErrorLog
	hCloseError
	hCloseUnsupportedFlag
	hCloseHome
	hCloseStateStorage
	hCloseConfig
	hCloseHuman
	hCloseExternalReview
	hCloseLight
	hCloseAcceptanceRevision
	hCloseHumanChecksShape
	hCloseHumanChecksValue
	hCloseRepo
	hCloseWorktreeShape
	hCloseWorktreeResolve
	hCloseWorktreeStat
	hCloseWorktreeBranch
	hCloseWorktreeCommonDir
	hCloseWorktreeIdentity
	hCloseWorktreePrefix
	hCloseLedgerRead
	hCloseLedgerShape
	hCloseGatesFormat
	hCloseTranscriptRead
	hCloseTranscriptFormat
	hCloseHighPaths
	hCloseChangedFiles
	hCloseFileSet
	hCloseRequeue
	hCloseQueueRefresh
	hCloseQueueIdentity
	hCloseReportRedaction
	hCloseCoderTests
	hCloseTestGate
	hCloseExcludedFile
	hCloseQueuePins
	hLandErrorLog
	hLandHelp
	hLandHome
	hLandReadRun
	hLandEntryShape
	hLandStatusShape
	hLandStateStorage
	hLandWorktreeRepo
	hLandMainRepo
	hLandConfigDaemon
	hLandMergeGate
	hLandStepHandoverJoin
	hLandGatesParse
	hLandStepHandoverGates
	hLandClaimFiles
	hLandStepHandoverClose
	hLandStepHandoverClaim
	hWaitBlockConfig
	hWaitBlockAnswer
	hWaitBlockEncoding
	hWaitBlockStopped
	hWaitBlockLocal
	hReviewRoot
	hEnqueueEncoding
	hEnqueuePathError
	hEnqueueFlagRedaction
	hEnqueueRepo
	hEnqueueWorker
	hEnqueueHome
	hEnqueueExclusion
	hEnqueuePrivateRecord
	hEnqueueRunMirror
	hEnqueueWorktree
	hEnqueueWorktreeResolve
	hEnqueueRunRead
	hEnqueueIdentityRecord
	hEnqueueIdentity
	hEnqueueCached
	hEnqueuePublish
	hEnqueueCachedAfterPublish
	hCloseAutoClaim
	hCloseDecide
	hCloseNoRequireReviews
	hCloseRemoveWorktree
	hCloseHelp
	hEnqueueFresh
	hEnqueueAnswers
	hEnqueueOf
	hEnqueueRebaseCheck
	hClaimAuto
	hClaimAutoClaim
	hStopAuto
	hStopAutoClaim
	hOpenAfter
	hOpenAfterOutcome
	hOpenStateChanged
	hClaimStopStateChanged
	hClaimAutopilotGrant
	hClaimPendingApproval
	hClaimEndHook
	hStopAutopilotGrant
	hStopPendingApproval
	hStopEndHook
	hOpenAutopilotGrant
	hOpenPendingApproval
	hOpenEndHook
	hCloseAutopilotGrant
	hClosePendingApproval
	hCloseEndHook
	hLandAutopilotGrant
	hLandPendingApproval
	hLandEndHook
	hStatusUnsupported
	hRunStart
	hRunAddWorker
	hRunReload
	hRunHome
	hRunRecord
	hRunShape
	hRunLedger
	hRunRegistry
	hCloseAckDigestOrder
	hReadyHookCommand
	hSmallVerbUnported
	hResolveFallbackShape
	hConfigParseError
	hModelsRefreshUnwritable
	hModelsRefreshInvalidJSON
	hRemoveUnsafeTarget
	hRemoveUnfinishable
	hPipelineUnsupported
	hOutcomeUnsupported
	hOutcomeRecordUnported
)

var handoverReasons = [...]string{
	hUnrouted:                  "unrouted",
	hBlockUnportedSubcommand:   "block-unported-subcommand",
	hWaitResult:                "wait-result",
	hWaitSummary:               "wait-summary",
	hWaitWorker:                "wait-worker",
	hGatesUnportedSubcommand:   "gates-unported-subcommand",
	hGatesHome:                 "gates-home",
	hGatesUnscoped:             "gates-unscoped",
	hGatesTimeoutRadix:         "gates-timeout-radix",
	hGatesTimeoutOverflow:      "gates-timeout-overflow",
	hGatesUnsupportedFlag:      "gates-unsupported-flag",
	hGatesEmptyScope:           "gates-empty-scope",
	hGatesCustom:               "gates-custom",
	hLogsUnportedSubcommand:    "logs-unported-subcommand",
	hLogsHelp:                  "logs-help",
	hLogsFlagsOrHome:           "logs-flags-or-home",
	hLogsDecode:                "logs-decode",
	hLogsFormat:                "logs-format",
	hLedgerUnportedSubcommand:  "ledger-unported-subcommand",
	hLedgerTailFormat:          "ledger-tail-format",
	hLedgerErrorLog:            "ledger-error-log",
	hOpenErrorLog:              "open-error-log",
	hOpenHelp:                  "open-help",
	hOpenUnsupportedFlag:       "open-unsupported-flag",
	hOpenNoLevel:               "open-no-level",
	hOpenJsSemantics:           "open-js-semantics",
	hOpenHome:                  "open-home",
	hOpenState:                 "open-state",
	hOpenAcceptanceRead:        "open-acceptance-read",
	hOpenAcceptanceFormat:      "open-acceptance-format",
	hOpenBriefResolve:          "open-brief-resolve",
	hOpenBriefAbsolute:         "open-brief-absolute",
	hOpenBriefRelative:         "open-brief-relative",
	hOpenBriefRead:             "open-brief-read",
	hOpenWorktreeSettings:      "open-worktree-settings",
	hClaimStopErrorLog:         "claim-stop-error-log",
	hClaimStopError:            "claim-stop-error",
	hClaimStopAuto:             "claim-stop-auto",
	hClaimStopHome:             "claim-stop-home",
	hClaimStopState:            "claim-stop-state",
	hClaimStopStorage:          "claim-stop-storage",
	hStopDaemon:                "stop-daemon",
	hStopDoneFacts:             "stop-done-facts",
	hCloseErrorLog:             "close-error-log",
	hCloseError:                "close-error",
	hCloseUnsupportedFlag:      "close-unsupported-flag",
	hCloseHome:                 "close-home",
	hCloseStateStorage:         "close-state-storage",
	hCloseConfig:               "close-config",
	hCloseHuman:                "close-human",
	hCloseExternalReview:       "close-external-review",
	hCloseLight:                "close-light",
	hCloseAcceptanceRevision:   "close-acceptance-revision",
	hCloseHumanChecksShape:     "close-human-checks-shape",
	hCloseHumanChecksValue:     "close-human-checks-value",
	hCloseRepo:                 "close-repo",
	hCloseWorktreeShape:        "close-worktree-shape",
	hCloseWorktreeResolve:      "close-worktree-resolve",
	hCloseWorktreeStat:         "close-worktree-stat",
	hCloseWorktreeBranch:       "close-worktree-branch",
	hCloseWorktreeCommonDir:    "close-worktree-common-dir",
	hCloseWorktreeIdentity:     "close-worktree-identity",
	hCloseWorktreePrefix:       "close-worktree-prefix",
	hCloseLedgerRead:           "close-ledger-read",
	hCloseLedgerShape:          "close-ledger-shape",
	hCloseGatesFormat:          "close-gates-format",
	hCloseTranscriptRead:       "close-transcript-read",
	hCloseTranscriptFormat:     "close-transcript-format",
	hCloseHighPaths:            "close-high-paths",
	hCloseChangedFiles:         "close-changed-files",
	hCloseFileSet:              "close-file-set",
	hCloseRequeue:              "close-requeue",
	hCloseQueueRefresh:         "close-queue-refresh",
	hCloseQueueIdentity:        "close-queue-identity",
	hCloseReportRedaction:      "close-report-redaction",
	hCloseCoderTests:           "close-coder-tests",
	hCloseTestGate:             "close-test-gate",
	hCloseExcludedFile:         "close-excluded-file",
	hCloseQueuePins:            "close-queue-pins",
	hLandErrorLog:              "land-error-log",
	hLandHelp:                  "land-help",
	hLandHome:                  "land-home",
	hLandReadRun:               "land-read-run",
	hLandEntryShape:            "land-entry-shape",
	hLandStatusShape:           "land-status-shape",
	hLandStateStorage:          "land-state-storage",
	hLandWorktreeRepo:          "land-worktree-repo",
	hLandMainRepo:              "land-main-repo",
	hLandConfigDaemon:          "land-config-daemon",
	hLandMergeGate:             "land-merge-gate",
	hLandStepHandoverJoin:      "land-step-handover:join",
	hLandGatesParse:            "land-gates-parse",
	hLandStepHandoverGates:     "land-step-handover:gates",
	hLandClaimFiles:            "land-claim-files",
	hLandStepHandoverClose:     "land-step-handover:close",
	hLandStepHandoverClaim:     "land-step-handover:claim",
	hWaitBlockConfig:           "wait-block-config",
	hWaitBlockAnswer:           "wait-block-answer",
	hWaitBlockEncoding:         "wait-block-encoding",
	hWaitBlockStopped:          "wait-block-stopped",
	hWaitBlockLocal:            "wait-block-local",
	hReviewRoot:                "review-root",
	hEnqueueEncoding:           "enqueue-encoding",
	hEnqueuePathError:          "enqueue-path-error",
	hEnqueueFlagRedaction:      "enqueue-flag-redaction",
	hEnqueueRepo:               "enqueue-repo",
	hEnqueueWorker:             "enqueue-worker",
	hEnqueueHome:               "enqueue-home",
	hEnqueueExclusion:          "enqueue-exclusion",
	hEnqueuePrivateRecord:      "enqueue-private-record",
	hEnqueueRunMirror:          "enqueue-run-mirror",
	hEnqueueWorktree:           "enqueue-worktree",
	hEnqueueWorktreeResolve:    "enqueue-worktree-resolve",
	hEnqueueRunRead:            "enqueue-run-read",
	hEnqueueIdentityRecord:     "enqueue-identity-record",
	hEnqueueIdentity:           "enqueue-identity",
	hEnqueueCached:             "enqueue-cached",
	hEnqueuePublish:            "enqueue-publish",
	hEnqueueCachedAfterPublish: "enqueue-cached-after-publish",
	hCloseAutoClaim:            "close-auto-claim",
	hCloseDecide:               "close-decide",
	hCloseNoRequireReviews:     "close-no-require-reviews",
	hCloseRemoveWorktree:       "close-remove-worktree",
	hCloseHelp:                 "close-help",
	hEnqueueFresh:              "enqueue-fresh",
	hEnqueueAnswers:            "enqueue-answers",
	hEnqueueOf:                 "enqueue-of",
	hEnqueueRebaseCheck:        "enqueue-rebase-check",
	hClaimAuto:                 "claim-auto",
	hClaimAutoClaim:            "claim-auto-claim",
	hStopAuto:                  "stop-auto",
	hStopAutoClaim:             "stop-auto-claim",
	hOpenAfter:                 "open-after",
	hOpenAfterOutcome:          "open-after-outcome",
	hOpenStateChanged:          "open-state-changed",
	hClaimStopStateChanged:     "claim-stop-state-changed",
	hClaimAutopilotGrant:       "claim-autopilot-grant",
	hClaimPendingApproval:      "claim-pending-approval",
	hClaimEndHook:              "claim-end-hook",
	hStopAutopilotGrant:        "stop-autopilot-grant",
	hStopPendingApproval:       "stop-pending-approval",
	hStopEndHook:               "stop-end-hook",
	hOpenAutopilotGrant:        "open-autopilot-grant",
	hOpenPendingApproval:       "open-pending-approval",
	hOpenEndHook:               "open-end-hook",
	hCloseAutopilotGrant:       "close-autopilot-grant",
	hClosePendingApproval:      "close-pending-approval",
	hCloseEndHook:              "close-end-hook",
	hLandAutopilotGrant:        "land-autopilot-grant",
	hLandPendingApproval:       "land-pending-approval",
	hLandEndHook:               "land-end-hook",
	hStatusUnsupported:         "status-unsupported",
	hRunStart:                  "run-start",
	hRunAddWorker:              "run-add-worker",
	hRunReload:                 "run-reload",
	hRunHome:                   "run-home",
	hRunRecord:                 "run-record",
	hRunShape:                  "run-shape",
	hRunLedger:                 "run-ledger",
	hRunRegistry:               "run-registry",
	hCloseAckDigestOrder:       "close-ack-digest-order",
	hReadyHookCommand:          "ready-hook-command",
	hSmallVerbUnported:         "small-verb-unported",
	hResolveFallbackShape:      "resolve-fallback-shape",
	hConfigParseError:          "config-parse-error",
	hModelsRefreshUnwritable:   "models-refresh-unwritable",
	hModelsRefreshInvalidJSON:  "models-refresh-invalid-json",
	hRemoveUnsafeTarget:        "remove-unsafe-target",
	hRemoveUnfinishable:        "remove-unfinishable",
	hPipelineUnsupported:       "pipeline-unsupported",
	hOutcomeUnsupported:        "outcome-unsupported",
	hOutcomeRecordUnported:     "outcome-record-unported",
}

func handoverFlagReason(prefix, flag string, fallback handoverReason) handoverReason {
	if flag == "-h" {
		flag = "--help"
	}
	code := prefix + "-" + strings.TrimLeft(flag, "-")
	for i, known := range handoverReasons {
		if code == known {
			return handoverReason(i)
		}
	}
	return fallback
}

func defaultHandoverReason(args []string) handoverReason {
	// The status route predates reason-aware wrappers and has one fallback.
	if len(args) > 0 && args[0] == "status" {
		return hStatusUnsupported
	}
	return hUnrouted
}

func handoverLogPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".code-forge", "native", "handovers.jsonl")
}

// Only public verbs from routes and src/cli/*.mjs, and their own known
// subcommands, may be recorded. Other tokens can be private argument values;
// store "unknown" for them, or an empty field when the argument is absent.
var handoverCommands = map[string]string{
	"author":        "",
	"autopilot":     "start status stop ask waive round level approve binnacle log",
	"block":         "open attempt rebase claim ready close land stop waive reopen accept-hit unwaive ack-waivers human-pass amend-acceptance",
	"ci":            "watch",
	"cloud-coder":   "",
	"context":       "pack",
	"decisions":     "list",
	"docs":          "sync path",
	"doctor":        "",
	"facts":         "",
	"gates":         "detect run secret-scan safe-edit scope acceptance transcript-grep",
	"heal":          "list show run",
	"help":          "",
	"init":          "",
	"jev":           "ask",
	"keys":          "list set test remove",
	"land":          "",
	"ledger":        "calibration outcome tail add outside-review",
	"list":          "",
	"logs":          "summary clear path report handovers",
	"models":        "",
	"native-status": "",
	"outcome":       "seal check record",
	"pipeline":      "status next",
	"plan":          "check",
	"proof":         "tier export lock unlock restore red-green baseline",
	"queue":         "status run",
	"remove":        "",
	"report":        "",
	"resolve":       "",
	"review-file":   "",
	"review":        "",
	"run":           "start status reload end",
	"s2":            "",
	"spawn":         "",
	"status":        "",
	"sweep":         "",
	"tools":         "install",
	"upgrade":       "",
	"validate":      "",
	"version":       "",
	"worker":        "",
}

func handoverCommand(args []string) (string, string) {
	if len(args) == 0 {
		return "", ""
	}
	verb := "unknown"
	subs, known := handoverCommands[args[0]]
	if known {
		verb = args[0]
	}
	if len(args) == 1 {
		return verb, ""
	}
	for _, sub := range strings.Fields(subs) {
		if args[1] == sub {
			return verb, sub
		}
	}
	return verb, "unknown"
}

// Best effort only: no diagnostics, no changes to process inputs. A separate
// inode protects append AND rename, so concurrent trimming cannot lose a row.
func recordHandover(args []string, reason string) {
	if os.Getenv("CODE_FORGE_HANDOVER_LOG") == "0" || os.Getenv("CODE_FORGE_NATIVE") == "0" {
		return
	}
	file := handoverLogPath()
	if file == "" {
		return
	}
	dir := filepath.Dir(file)
	if os.MkdirAll(dir, 0700) != nil {
		return
	}
	lock, err := os.OpenFile(file+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return
	}
	defer lock.Close()
	// Bound contention: an unavailable logger must never hold up a command.
	deadline := time.Now().Add(100 * time.Millisecond)
	for syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		if !time.Now().Before(deadline) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	verb, sub := handoverCommand(args)
	data, err := json.Marshal(handoverRow{time.Now().UTC().Format(time.RFC3339Nano), verb, sub, reason, version})
	if err != nil {
		return
	}
	f, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	if f.Chmod(0600) != nil {
		_ = f.Close()
		return
	}
	_, err = f.Write(append(data, '\n'))
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return
	}
	all, err := os.ReadFile(file)
	if err != nil {
		return
	}
	lines := bytes.Split(bytes.TrimSuffix(all, []byte("\n")), []byte("\n"))
	if len(lines) <= 5000 {
		return
	}
	tmp, err := os.CreateTemp(dir, "handovers.tmp-*")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(append(bytes.Join(lines[len(lines)-4000:], []byte("\n")), '\n'))
	closeErr = tmp.Close()
	if err == nil && closeErr == nil {
		_ = os.Rename(tmp.Name(), file)
	}
}

type handoverCount struct {
	Verb   string `json:"verb"`
	Sub    string `json:"sub"`
	Reason string `json:"reason"`
	Count  int    `json:"count"`
	LastAt string `json:"last_at"`
}

func runHandoverLogs(args []string) int {
	usage := func(message string) int {
		fmt.Fprintln(os.Stderr, "logs handovers: "+message)
		fmt.Fprintln(os.Stderr, "usage: code-forge logs handovers [--json] [--since <ISO date>]")
		return 2
	}
	asJSON, seenSince := false, false
	var since time.Time
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			if asJSON {
				return usage("--json given more than once")
			}
			asJSON = true
		case "--since":
			if seenSince {
				return usage("--since given more than once")
			}
			seenSince = true
			i++
			if i == len(args) {
				return usage("--since requires an ISO date")
			}
			var err error
			since, err = time.Parse(time.RFC3339Nano, args[i])
			if err != nil {
				since, err = time.Parse("2006-01-02", args[i])
			}
			if err != nil {
				return usage("--since requires a valid ISO date")
			}
		default:
			return usage("unexpected argument; use --json or --since <ISO date>")
		}
	}
	groups := map[[3]string]*handoverCount{}
	total := 0
	data, err := os.ReadFile(handoverLogPath())
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, "logs handovers: cannot read hand-over log")
		return 1
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		var row handoverRow
		if json.Unmarshal(line, &row) != nil {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, row.At)
		if err != nil || at.Before(since) {
			continue
		}
		key := [3]string{row.Verb, row.Sub, row.Reason}
		group := groups[key]
		if group == nil {
			group = &handoverCount{Verb: row.Verb, Sub: row.Sub, Reason: row.Reason}
			groups[key] = group
		}
		group.Count++
		last, _ := time.Parse(time.RFC3339Nano, group.LastAt)
		if at.After(last) {
			group.LastAt = row.At
		}
		total++
	}
	rows := []handoverCount{}
	for _, group := range groups {
		rows = append(rows, *group)
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.Verb != b.Verb {
			return a.Verb < b.Verb
		}
		if a.Sub != b.Sub {
			return a.Sub < b.Sub
		}
		return a.Reason < b.Reason
	})
	if asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(struct {
			Total int             `json:"total"`
			Rows  []handoverCount `json:"rows"`
		}{total, rows})
	} else if total == 0 {
		fmt.Fprintln(os.Stdout, "no hand-overs recorded")
	} else {
		fmt.Fprintln(os.Stdout, "count  verb  sub  reason")
		for _, row := range rows {
			fmt.Fprintf(os.Stdout, "%d  %s  %s  %s\n", row.Count, row.Verb, row.Sub, row.Reason)
		}
		fmt.Fprintf(os.Stdout, "total  %d\n", total)
	}
	return 0
}
