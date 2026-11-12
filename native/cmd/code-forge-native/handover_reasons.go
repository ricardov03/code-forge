package main

// Hand-over reasons live in this file alone so .gitattributes can merge it
// with merge=union (B510, B513): blocks only append one constant at the end of
// the const block and one entry at the end of the handoverReasons array.
// Functions that use the reasons stay in handover_log.go.

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
	hLedgerRowsFormat
	hLedgerRunRecord
	hLedgerGit
	hLedgerJsValue
	hLedgerErrorHint
	hLogsReport
	hLogsClearPrompt
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
	hToolsUnsupported
	hToolsConfirm
	hValidateUnported
	hValidateSchemaIssues
	hValidateSecretValues
	hValidateSandboxDirs
	hSweepUnported
	hUpgradeRelink
	hUpgradeControlChars
	hPlanCheckUnported
	hDoctorFullRun
	hDoctorConfigSchema
	hDoctorConfigSecrets
	hDoctorSandboxDirs
	hDoctorKeyStore
	hDoctorKeyRefresh
	hDoctorLedgerShape
	hDoctorNoHome
	hPlanCheckNodeThrows
	hPlanCheckLimits
	hPlanCheckLedgerRead
	hUpgradePackageRoot
	hUpgradeUnsafeTarget
	hUpgradeDocsSync
	hUpgradeNoHome
	hUpgradeUnfinishable
	hUpgradeLedgerEncoding
	hUpgradeVersionOrder
	hUpgradeCopyEntry
	hUpgradePathEncoding
	hWorkerServe
	hWorkerHome
	hWorkerRecord
	hWorkerShape
	hWorkerConfig
	hWorkerWatch
	hWorkerJev
	hWorkerBoot
	hWorkerTicket
	hSpawnSession
	hSpawnHome
	hSpawnConfig
	hSpawnSchema
	hReviewFlow
	hReviewGit
	hAutopilotHome
	hAutopilotRecord
	hAutopilotShape
	hAutopilotLedger
	hAutopilotFiles
	hAutopilotPrompt
	hAutopilotAsk
	hAutopilotAction
	hAutopilotApprove
	hAutopilotBinnacle
	hAutopilotRestore
	hAutopilotRestoreReload
	hAutopilotRestoreWrite
	hAutopilotRestoreConfig
	hAutopilotApproveWrite
	hAutopilotApproveConfig
	hAutopilotApproveValue
	hAutopilotApproveChanged
	hHelpVerbs
	hVersionPackage
	hInitWizard
	hNativeStatusCompanion
	hFactsSession
	hAuthorSession
	hS2Session
	hJevAsk
	hProofConfig
	hProofRun
	hProofRunner
	hProofSnapshot
	hProofRedGreen
	hGatesDetectConfig
	hGatesAcceptanceJSON
	hGatesTapEncoding
	hGatesTranscriptExtras
	hGatesTranscriptUnsup
	hGatesGitRace
	hGatesGit
	hGatesFsError
	hGatesRunIDMismatch
	hGatesRunNoWorkspace
	hNativeStatusNoHome
	hNativeStatusTTY
	hNativeStatusLine
	hRunSweep
	hSweepHome
	hSweepRoot
	hSweepIdle
	hDoctorLiveProbes
	hDoctorFullNoConfig
	hRouterHintState
	hRunUnportedSubcommand
	hRunReloadRecordUnreadable
	hRunReloadWorkspace
	hRunReloadYAMLTag
	hRunReloadConfigError
	hRunReloadSchemaIssues
	hRunReloadSecretValues
	hRunReloadSandboxDirs
	hRunReloadRecordShape
	hRunReloadAutopilotGrant
	hRunReloadReviewShape
	hInitInteractive
	hInitSetup
	hInitArgEncoding
	hInitJevLink
	hInitConfigSyntax
	hInitAnswers
	hInitOutput
	hDoctorFullInvalidConfig
	hDoctorFullLiveWorker
	hDoctorNativeBinary
	hDoctorWorkerRegistry
	hDoctorReviewStateRecord
	hInitJevKey
	hInitJevOp
	hInitTools
	hInitModels
	hInitDoctor
	hInitLinks
	hInitConfigShape
	hInitConfigCheck
	hInitUserConfig
	hDoctorRunTmpRoot
	hDoctorRunRoot
	hDoctorRunJev
	hDoctorRunPing
	hDoctorRunIsolation
	hDoctorRunPathDeny
	hDoctorRunCodexRules
	hDoctorRunCliVersion
	hDoctorRunWorkerProbe
	hWorkerJevRef
	hWorkerJevStore
	hWorkerJevKeychain
	hWorkerWatchShape
	hAutopilotApproveYAMLShape
	hAutopilotApproveReloadShape
	hAutopilotRestoreReloadShape
	hInitWizardShape
	hInitWizardEOF
	hInitWizardTools
	hInitWizardModels
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
	hLedgerRowsFormat:          "ledger-rows-format",
	hLedgerRunRecord:           "ledger-run-record",
	hLedgerGit:                 "ledger-git",
	hLedgerJsValue:             "ledger-js-value",
	hLedgerErrorHint:           "ledger-error-hint",
	hLogsReport:                "logs-report",
	hLogsClearPrompt:           "logs-clear-prompt",
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
	hToolsUnsupported:          "tools-unsupported",
	hToolsConfirm:              "tools-confirm",
	hValidateUnported:          "validate-unported",
	hValidateSchemaIssues:      "validate-schema-issues",
	hValidateSecretValues:      "validate-secret-values",
	hValidateSandboxDirs:       "validate-sandbox-dirs",
	hSweepUnported:             "sweep-unported",
	hUpgradeRelink:             "upgrade-relink",
	hUpgradeControlChars:       "upgrade-control-chars",
	hPlanCheckUnported:         "plan-check-unported",
	hDoctorFullRun:             "doctor-full-run",
	hDoctorConfigSchema:        "doctor-config-schema",
	hDoctorConfigSecrets:       "doctor-config-secrets",
	hDoctorSandboxDirs:         "doctor-sandbox-dirs",
	hDoctorKeyStore:            "doctor-key-store",
	hDoctorKeyRefresh:          "doctor-key-refresh",
	hDoctorLedgerShape:         "doctor-ledger-shape",
	hDoctorNoHome:              "doctor-no-home",
	hPlanCheckNodeThrows:       "plan-check-node-throws",
	hPlanCheckLimits:           "plan-check-limits",
	hPlanCheckLedgerRead:       "plan-check-ledger-read",
	hUpgradePackageRoot:        "upgrade-package-root",
	hUpgradeUnsafeTarget:       "upgrade-unsafe-target",
	hUpgradeDocsSync:           "upgrade-docs-sync",
	hUpgradeNoHome:             "upgrade-no-home",
	hUpgradeUnfinishable:       "upgrade-unfinishable",
	hUpgradeLedgerEncoding:     "upgrade-ledger-encoding",
	hUpgradeVersionOrder:       "upgrade-version-order",
	hUpgradeCopyEntry:          "upgrade-copy-entry",
	hUpgradePathEncoding:       "upgrade-path-encoding",
	hWorkerServe:               "worker-serve",
	hWorkerHome:                "worker-home",
	hWorkerRecord:              "worker-record",
	hWorkerShape:               "worker-shape",
	hWorkerConfig:              "worker-config",
	hWorkerWatch:               "worker-watch",
	hWorkerJev:                 "worker-jev-key",
	hWorkerBoot:                "worker-boot",
	hWorkerTicket:              "worker-ticket-unported",
	hSpawnSession:              "spawn-session",
	hSpawnHome:                 "spawn-home",
	hSpawnConfig:               "spawn-config",
	hSpawnSchema:               "spawn-schema",
	hReviewFlow:                "review-flow",
	hReviewGit:                 "review-git",
	hAutopilotHome:             "autopilot-home",
	hAutopilotRecord:           "autopilot-record",
	hAutopilotShape:            "autopilot-shape",
	hAutopilotLedger:           "autopilot-ledger",
	hAutopilotFiles:            "autopilot-files",
	hAutopilotPrompt:           "autopilot-prompt",
	hAutopilotAsk:              "autopilot-ask",
	hAutopilotAction:           "autopilot-action",
	hAutopilotApprove:          "autopilot-approve",
	hAutopilotBinnacle:         "autopilot-binnacle",
	hAutopilotRestore:          "autopilot-restore",
	hAutopilotRestoreReload:    "autopilot-restore-reload",
	hAutopilotRestoreWrite:     "autopilot-restore-write",
	hAutopilotRestoreConfig:    "autopilot-restore-config",
	hAutopilotApproveWrite:     "autopilot-approve-write",
	hAutopilotApproveConfig:    "autopilot-approve-config",
	hAutopilotApproveValue:     "autopilot-approve-value",
	hAutopilotApproveChanged:   "autopilot-approve-changed",
	hHelpVerbs:                 "help-verbs",
	hVersionPackage:            "version-package",
	hInitWizard:                "init-wizard",
	hNativeStatusCompanion:     "native-status-companion",
	hFactsSession:              "facts-session",
	hAuthorSession:             "author-session",
	hS2Session:                 "system-two-session",
	hJevAsk:                    "jev-ask",
	hProofConfig:               "proof-config",
	hProofRun:                  "proof-run",
	hProofRunner:               "proof-runner",
	hProofSnapshot:             "proof-snapshot",
	hProofRedGreen:             "proof-red-green",
	hGatesDetectConfig:         "gates-detect-config",
	hGatesAcceptanceJSON:       "gates-acceptance-json",
	hGatesTapEncoding:          "gates-tap-encoding",
	hGatesTranscriptExtras:     "gates-transcript-extras",
	hGatesTranscriptUnsup:      "gates-transcript-unsupported",
	hGatesGitRace:              "gates-git-race",
	hGatesGit:                  "gates-git-spawn",
	hGatesFsError:              "gates-fs-error",
	hGatesRunIDMismatch:        "gates-run-id-mismatch",
	hGatesRunNoWorkspace:       "gates-run-no-workspace",
	hNativeStatusNoHome:        "native-status-no-home",
	hNativeStatusTTY:           "native-status-tty",
	hNativeStatusLine:          "native-status-line",
	hRunSweep:                  "run-sweep",
	hSweepHome:                 "sweep-home",
	hSweepRoot:                 "sweep-root-unreadable",
	hSweepIdle:                 "sweep-idle-unknown",
	hDoctorLiveProbes:          "doctor-live-probes",
	hDoctorFullNoConfig:        "doctor-full-no-config",
	hRouterHintState:           "router-hint-state",
	hRunUnportedSubcommand:     "run-unported-subcommand",
	hRunReloadRecordUnreadable: "run-reload-record-unreadable",
	hRunReloadWorkspace:        "run-reload-workspace",
	hRunReloadYAMLTag:          "run-reload-yaml-tag",
	hRunReloadConfigError:      "run-reload-config-error",
	hRunReloadSchemaIssues:     "run-reload-schema-issues",
	hRunReloadSecretValues:     "run-reload-secret-values",
	hRunReloadSandboxDirs:      "run-reload-sandbox-dirs",
	hRunReloadRecordShape:      "run-reload-record-shape",
	hRunReloadAutopilotGrant:   "run-reload-autopilot-grant",
	hRunReloadReviewShape:      "run-reload-review-shape",
	hInitInteractive:           "init-interactive",
	hInitSetup:                 "init-setup",
	hInitArgEncoding:           "init-arg-encoding",
	hInitJevLink:               "init-jev-link",
	hInitConfigSyntax:          "init-config-syntax",
	hInitAnswers:               "init-answers",
	hInitOutput:                "init-output",
	hDoctorFullInvalidConfig:   "doctor-full-invalid-config",
	hDoctorFullLiveWorker:      "doctor-full-live-worker",
	hDoctorNativeBinary:        "doctor-native-binary",
	hDoctorWorkerRegistry:      "doctor-worker-registry",
	hDoctorReviewStateRecord:   "doctor-review-state-record",
	hInitJevKey:                "init-jev-key",
	hInitJevOp:                 "init-jev-op",
	hInitTools:                 "init-tools",
	hInitModels:                "init-models",
	hInitDoctor:                "init-doctor",
	hInitLinks:                 "init-links",
	hInitConfigShape:           "init-config-shape",
	hInitConfigCheck:           "init-config-check",
	hInitUserConfig:            "init-user-config",
	hDoctorRunTmpRoot:          "doctor-run-tmp-root",
	hDoctorRunRoot:             "doctor-run-root",
	hDoctorRunJev:              "doctor-run-jev",
	hDoctorRunPing:             "doctor-run-ping",
	hDoctorRunIsolation:        "doctor-run-isolation",
	hDoctorRunPathDeny:         "doctor-run-path-deny",
	hDoctorRunCodexRules:       "doctor-run-codex-rules",
	hDoctorRunCliVersion:       "doctor-run-cli-version",
	hDoctorRunWorkerProbe:      "doctor-run-worker-probe",
	hWorkerJevRef:              "worker-jev-key-ref",
	hWorkerJevStore:            "worker-jev-key-store",
	hWorkerJevKeychain:         "worker-jev-keychain",
	hWorkerWatchShape:          "worker-watch-shape",

	// B536
	hAutopilotApproveYAMLShape:   "autopilot-approve-yaml-shape",
	hAutopilotApproveReloadShape: "autopilot-approve-reload-shape",
	hAutopilotRestoreReloadShape: "autopilot-restore-reload-shape",
	hInitWizardShape:           "init-wizard-shape",
	hInitWizardEOF:             "init-wizard-eof",
	hInitWizardTools:           "init-wizard-tools",
	hInitWizardModels:          "init-wizard-models",
}
