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
	hGatesCustom
	hLogsFlagsOrHome
	hLogsDecode
	hLogsFormat
	hLedgerTailFormat
	hLedgerRowsFormat
	hLedgerRunRecord
	hLedgerGit
	hLedgerJsValue
	hLedgerErrorHint
	hLogsReport
	hLogsClearPrompt
	hOpenState
	hClaimStopHome
	hClaimStopState
	hClaimStopStorage
	hStopDoneFacts
	hCloseError
	hCloseUnsupportedFlag
	hCloseHome
	hCloseStateStorage
	hCloseConfig
	hCloseHuman
	hCloseExternalReview
	hCloseLight
	hCloseHumanChecksShape
	hCloseHumanChecksValue
	hCloseRepo
	hCloseWorktreeBranch
	hCloseWorktreeCommonDir
	hCloseWorktreeIdentity
	hCloseLedgerRead
	hCloseGatesFormat
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
	hEnqueueRunRead
	hEnqueueIdentityRecord
	hEnqueueIdentity
	hEnqueueCached
	hEnqueuePublish
	hCloseAutoClaim
	hCloseDecide
	hCloseNoRequireReviews
	hEnqueueFresh
	hEnqueueAnswers
	hEnqueueRebaseCheck
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
	hUpgradeControlChars
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
	hWorkerBoot
	hWorkerTicket
	hSpawnSession
	hSpawnHome
	hSpawnConfig
	hSpawnSchema
	hReviewGit
	hAutopilotHome
	hAutopilotRecord
	hAutopilotShape
	hAutopilotLedger
	hAutopilotFiles
	hAutopilotPrompt
	hAutopilotAsk
	hAutopilotAction
	hAutopilotBinnacle
	hAutopilotRestoreWrite
	hAutopilotRestoreConfig
	hAutopilotApproveConfig
	hAutopilotApproveValue
	hAutopilotApproveChanged
	hHelpVerbs
	hVersionPackage
	hFactsSession
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
	hRouterHintState
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
	hInitModels
	hInitDoctor
	hInitLinks
	hInitConfigShape
	hInitConfigCheck
	hInitUserConfig
	hDoctorRunTmpRoot
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
	hInitJevKeychain
	hSpawnCoderRun
	hSpawnBackgroundStart
	hGoPanic
	hGoPanicStopped
	hFactsConfig
	// B554: logs report cases that stay in Node
	hLogsReportDoctor
	hLogsReportKnownFixes
	hLogsReportSystem
	hLogsReportPrompt
	// B550
	hAuthorProvider
	hAuthorInput
	hAuthorConfig
	hAuthorHome
	hS2Provider
	hS2Input
	hS2Config
	hS2Home
	hJevAskState
	hJevAskConfig
	hJevAskKey
	hJevAskPayload
	hJevAskAnswer
	hJevAskHome
	hReviewHome
	hReviewConfig
	hReviewAcceptanceShape
	hReviewRunRecord
	hReviewTmpRoot
	hReviewSelf
	// B551r
	hSpawnCodexSandbox
	hFactsGlobClaim
	// B578
	hRebaseHome
	hRebaseRunRecord
	hRebaseAutopilotShape
	hRebaseRecordShape
	hRebaseStorage
	hRebaseQueue
	hRebasePath
	hRebaseKey
	hRebaseLedger
	hRebaseContentHash
	// B581
	hOpenHomeLookup
	hOpenLevelLedger
	hOpenBriefCwd
	hOpenAcceptanceYAML
	hOpenPipelineForm
	hOpenOutcomeForm
	// B587
	hCloseLockfileFormat
)

var handoverReasons = [...]string{
	hUnrouted:                 "unrouted",
	hBlockUnportedSubcommand:  "block-unported-subcommand",
	hWaitResult:               "wait-result",
	hWaitSummary:              "wait-summary",
	hWaitWorker:               "wait-worker",
	hGatesCustom:              "gates-custom",
	hLogsFlagsOrHome:          "logs-flags-or-home",
	hLogsDecode:               "logs-decode",
	hLogsFormat:               "logs-format",
	hLedgerTailFormat:         "ledger-tail-format",
	hLedgerRowsFormat:         "ledger-rows-format",
	hLedgerRunRecord:          "ledger-run-record",
	hLedgerGit:                "ledger-git",
	hLedgerJsValue:            "ledger-js-value",
	hLedgerErrorHint:          "ledger-error-hint",
	hLogsReport:               "logs-report",
	hLogsClearPrompt:          "logs-clear-prompt",
	hOpenState:                "open-state",
	hClaimStopHome:            "claim-stop-home",
	hClaimStopState:           "claim-stop-state",
	hClaimStopStorage:         "claim-stop-storage",
	hStopDoneFacts:            "stop-done-facts",
	hCloseError:               "close-error",
	hCloseUnsupportedFlag:     "close-unsupported-flag",
	hCloseHome:                "close-home",
	hCloseStateStorage:        "close-state-storage",
	hCloseConfig:              "close-config",
	hCloseHuman:               "close-human",
	hCloseExternalReview:      "close-external-review",
	hCloseLight:               "close-light",
	hCloseHumanChecksShape:    "close-human-checks-shape",
	hCloseHumanChecksValue:    "close-human-checks-value",
	hCloseRepo:                "close-repo",
	hCloseWorktreeBranch:      "close-worktree-branch",
	hCloseWorktreeCommonDir:   "close-worktree-common-dir",
	hCloseWorktreeIdentity:    "close-worktree-identity",
	hCloseLedgerRead:          "close-ledger-read",
	hCloseGatesFormat:         "close-gates-format",
	hCloseTranscriptFormat:    "close-transcript-format",
	hCloseHighPaths:           "close-high-paths",
	hCloseChangedFiles:        "close-changed-files",
	hCloseFileSet:             "close-file-set",
	hCloseRequeue:             "close-requeue",
	hCloseQueueRefresh:        "close-queue-refresh",
	hCloseQueueIdentity:       "close-queue-identity",
	hCloseReportRedaction:     "close-report-redaction",
	hCloseCoderTests:          "close-coder-tests",
	hCloseTestGate:            "close-test-gate",
	hCloseExcludedFile:        "close-excluded-file",
	hCloseQueuePins:           "close-queue-pins",
	hLandHome:                 "land-home",
	hLandReadRun:              "land-read-run",
	hLandEntryShape:           "land-entry-shape",
	hLandStatusShape:          "land-status-shape",
	hLandStateStorage:         "land-state-storage",
	hLandWorktreeRepo:         "land-worktree-repo",
	hLandMainRepo:             "land-main-repo",
	hLandConfigDaemon:         "land-config-daemon",
	hLandMergeGate:            "land-merge-gate",
	hLandStepHandoverJoin:     "land-step-handover:join",
	hLandGatesParse:           "land-gates-parse",
	hLandStepHandoverGates:    "land-step-handover:gates",
	hLandClaimFiles:           "land-claim-files",
	hLandStepHandoverClose:    "land-step-handover:close",
	hLandStepHandoverClaim:    "land-step-handover:claim",
	hWaitBlockConfig:          "wait-block-config",
	hWaitBlockAnswer:          "wait-block-answer",
	hWaitBlockEncoding:        "wait-block-encoding",
	hWaitBlockStopped:         "wait-block-stopped",
	hWaitBlockLocal:           "wait-block-local",
	hReviewRoot:               "review-root",
	hEnqueueEncoding:          "enqueue-encoding",
	hEnqueuePathError:         "enqueue-path-error",
	hEnqueueFlagRedaction:     "enqueue-flag-redaction",
	hEnqueueRepo:              "enqueue-repo",
	hEnqueueWorker:            "enqueue-worker",
	hEnqueueHome:              "enqueue-home",
	hEnqueueExclusion:         "enqueue-exclusion",
	hEnqueuePrivateRecord:     "enqueue-private-record",
	hEnqueueRunMirror:         "enqueue-run-mirror",
	hEnqueueWorktree:          "enqueue-worktree",
	hEnqueueRunRead:           "enqueue-run-read",
	hEnqueueIdentityRecord:    "enqueue-identity-record",
	hEnqueueIdentity:          "enqueue-identity",
	hEnqueueCached:            "enqueue-cached",
	hEnqueuePublish:           "enqueue-publish",
	hCloseAutoClaim:           "close-auto-claim",
	hCloseDecide:              "close-decide",
	hCloseNoRequireReviews:    "close-no-require-reviews",
	hEnqueueFresh:             "enqueue-fresh",
	hEnqueueAnswers:           "enqueue-answers",
	hEnqueueRebaseCheck:       "enqueue-rebase-check",
	hOpenStateChanged:         "open-state-changed",
	hClaimStopStateChanged:    "claim-stop-state-changed",
	hClaimAutopilotGrant:      "claim-autopilot-grant",
	hClaimPendingApproval:     "claim-pending-approval",
	hClaimEndHook:             "claim-end-hook",
	hStopAutopilotGrant:       "stop-autopilot-grant",
	hStopPendingApproval:      "stop-pending-approval",
	hStopEndHook:              "stop-end-hook",
	hOpenAutopilotGrant:       "open-autopilot-grant",
	hOpenPendingApproval:      "open-pending-approval",
	hOpenEndHook:              "open-end-hook",
	hCloseAutopilotGrant:      "close-autopilot-grant",
	hClosePendingApproval:     "close-pending-approval",
	hCloseEndHook:             "close-end-hook",
	hLandAutopilotGrant:       "land-autopilot-grant",
	hLandPendingApproval:      "land-pending-approval",
	hLandEndHook:              "land-end-hook",
	hStatusUnsupported:        "status-unsupported",
	hRunStart:                 "run-start",
	hRunAddWorker:             "run-add-worker",
	hRunReload:                "run-reload",
	hRunHome:                  "run-home",
	hRunRecord:                "run-record",
	hRunShape:                 "run-shape",
	hRunLedger:                "run-ledger",
	hRunRegistry:              "run-registry",
	hCloseAckDigestOrder:      "close-ack-digest-order",
	hReadyHookCommand:         "ready-hook-command",
	hResolveFallbackShape:     "resolve-fallback-shape",
	hConfigParseError:         "config-parse-error",
	hModelsRefreshUnwritable:  "models-refresh-unwritable",
	hModelsRefreshInvalidJSON: "models-refresh-invalid-json",
	hRemoveUnsafeTarget:       "remove-unsafe-target",
	hRemoveUnfinishable:       "remove-unfinishable",
	hPipelineUnsupported:      "pipeline-unsupported",
	hOutcomeUnsupported:       "outcome-unsupported",
	hOutcomeRecordUnported:    "outcome-record-unported",
	hToolsUnsupported:         "tools-unsupported",
	hToolsConfirm:             "tools-confirm",
	hValidateUnported:         "validate-unported",
	hValidateSchemaIssues:     "validate-schema-issues",
	hValidateSecretValues:     "validate-secret-values",
	hValidateSandboxDirs:      "validate-sandbox-dirs",
	hUpgradeControlChars:      "upgrade-control-chars",
	hDoctorFullRun:            "doctor-full-run",
	hDoctorConfigSchema:       "doctor-config-schema",
	hDoctorConfigSecrets:      "doctor-config-secrets",
	hDoctorSandboxDirs:        "doctor-sandbox-dirs",
	hDoctorKeyStore:           "doctor-key-store",
	hDoctorKeyRefresh:         "doctor-key-refresh",
	hDoctorLedgerShape:        "doctor-ledger-shape",
	hDoctorNoHome:             "doctor-no-home",
	hPlanCheckNodeThrows:      "plan-check-node-throws",
	hPlanCheckLimits:          "plan-check-limits",
	hPlanCheckLedgerRead:      "plan-check-ledger-read",
	hUpgradePackageRoot:       "upgrade-package-root",
	hUpgradeUnsafeTarget:      "upgrade-unsafe-target",
	hUpgradeNoHome:            "upgrade-no-home",
	hUpgradeUnfinishable:      "upgrade-unfinishable",
	hUpgradeLedgerEncoding:    "upgrade-ledger-encoding",
	hUpgradeVersionOrder:      "upgrade-version-order",
	hUpgradeCopyEntry:         "upgrade-copy-entry",
	hUpgradePathEncoding:      "upgrade-path-encoding",
	hWorkerServe:              "worker-serve",
	hWorkerHome:               "worker-home",
	hWorkerRecord:             "worker-record",
	hWorkerShape:              "worker-shape",
	hWorkerConfig:             "worker-config",
	hWorkerBoot:               "worker-boot",
	hWorkerTicket:             "worker-ticket-unported",
	hSpawnSession:             "spawn-session",
	hSpawnHome:                "spawn-home",
	hSpawnConfig:              "spawn-config",
	hSpawnSchema:              "spawn-schema",
	hReviewGit:                "review-git",
	hAutopilotHome:            "autopilot-home",
	hAutopilotRecord:          "autopilot-record",
	hAutopilotShape:           "autopilot-shape",
	hAutopilotLedger:          "autopilot-ledger",
	hAutopilotFiles:           "autopilot-files",
	hAutopilotPrompt:          "autopilot-prompt",
	hAutopilotAsk:             "autopilot-ask",
	hAutopilotAction:          "autopilot-action",
	hAutopilotBinnacle:        "autopilot-binnacle",
	hAutopilotRestoreWrite:    "autopilot-restore-write",
	hAutopilotRestoreConfig:   "autopilot-restore-config",
	hAutopilotApproveConfig:   "autopilot-approve-config",
	hAutopilotApproveValue:    "autopilot-approve-value",
	hAutopilotApproveChanged:  "autopilot-approve-changed",
	hHelpVerbs:                "help-verbs",
	hVersionPackage:           "version-package",
	hFactsSession:             "facts-session",
	hProofConfig:              "proof-config",
	hProofRun:                 "proof-run",
	hProofRunner:              "proof-runner",
	hProofSnapshot:            "proof-snapshot",
	hProofRedGreen:            "proof-red-green",
	hGatesDetectConfig:        "gates-detect-config",
	hGatesAcceptanceJSON:      "gates-acceptance-json",
	hGatesTapEncoding:         "gates-tap-encoding",
	hGatesTranscriptExtras:    "gates-transcript-extras",
	hGatesTranscriptUnsup:     "gates-transcript-unsupported",
	hGatesGitRace:             "gates-git-race",
	hGatesGit:                 "gates-git-spawn",
	hGatesFsError:             "gates-fs-error",
	hGatesRunIDMismatch:       "gates-run-id-mismatch",
	hGatesRunNoWorkspace:      "gates-run-no-workspace",
	hNativeStatusNoHome:       "native-status-no-home",
	hNativeStatusTTY:          "native-status-tty",
	hNativeStatusLine:         "native-status-line",
	hRunSweep:                 "run-sweep",
	hSweepHome:                "sweep-home",
	hSweepRoot:                "sweep-root-unreadable",
	hSweepIdle:                "sweep-idle-unknown",
	hRouterHintState:          "router-hint-state",
	hRunReloadWorkspace:       "run-reload-workspace",
	hRunReloadYAMLTag:         "run-reload-yaml-tag",
	hRunReloadConfigError:     "run-reload-config-error",
	hRunReloadSchemaIssues:    "run-reload-schema-issues",
	hRunReloadSecretValues:    "run-reload-secret-values",
	hRunReloadSandboxDirs:     "run-reload-sandbox-dirs",
	hRunReloadRecordShape:     "run-reload-record-shape",
	hRunReloadAutopilotGrant:  "run-reload-autopilot-grant",
	hRunReloadReviewShape:     "run-reload-review-shape",
	hInitInteractive:          "init-interactive",
	hInitArgEncoding:          "init-arg-encoding",
	hInitJevLink:              "init-jev-link",
	hInitConfigSyntax:         "init-config-syntax",
	hInitAnswers:              "init-answers",
	hInitOutput:               "init-output",
	hDoctorFullInvalidConfig:  "doctor-full-invalid-config",
	hDoctorFullLiveWorker:     "doctor-full-live-worker",
	hDoctorNativeBinary:       "doctor-native-binary",
	hDoctorWorkerRegistry:     "doctor-worker-registry",
	hDoctorReviewStateRecord:  "doctor-review-state-record",
	hInitJevKey:               "init-jev-key",
	hInitJevOp:                "init-jev-op",
	hInitModels:               "init-models",
	hInitDoctor:               "init-doctor",
	hInitLinks:                "init-links",
	hInitConfigShape:          "init-config-shape",
	hInitConfigCheck:          "init-config-check",
	hInitUserConfig:           "init-user-config",
	hDoctorRunTmpRoot:         "doctor-run-tmp-root",
	hDoctorRunJev:             "doctor-run-jev",
	hDoctorRunPing:            "doctor-run-ping",
	hDoctorRunIsolation:       "doctor-run-isolation",
	hDoctorRunPathDeny:        "doctor-run-path-deny",
	hDoctorRunCodexRules:      "doctor-run-codex-rules",
	hDoctorRunCliVersion:      "doctor-run-cli-version",
	hDoctorRunWorkerProbe:     "doctor-run-worker-probe",
	hWorkerJevRef:             "worker-jev-key-ref",
	hWorkerJevStore:           "worker-jev-key-store",
	hWorkerJevKeychain:        "worker-jev-keychain",
	hWorkerWatchShape:         "worker-watch-shape",

	// B536
	hAutopilotApproveYAMLShape:   "autopilot-approve-yaml-shape",
	hAutopilotApproveReloadShape: "autopilot-approve-reload-shape",
	hAutopilotRestoreReloadShape: "autopilot-restore-reload-shape",
	hInitWizardShape:             "init-wizard-shape",
	hInitWizardEOF:               "init-wizard-eof",
	hInitWizardTools:             "init-wizard-tools",
	hInitWizardModels:            "init-wizard-models",

	// B548
	hInitJevKeychain: "init-jev-keychain",
	// B549
	hSpawnCoderRun:        "spawn-coder-run",
	hSpawnBackgroundStart: "spawn-background-start",
	hGoPanic:              "go-panic",
	hGoPanicStopped:       "go-panic-stopped",
	// B555
	hFactsConfig: "facts-config",

	// B554
	hLogsReportDoctor:     "logs-report-doctor",
	hLogsReportKnownFixes: "logs-report-known-fixes",
	hLogsReportSystem:     "logs-report-system",
	hLogsReportPrompt:     "logs-report-prompt",
	// B550
	hAuthorProvider: "author-provider",
	hAuthorInput:    "author-input",
	hAuthorConfig:   "author-config",
	hAuthorHome:     "author-home",
	hS2Provider:     "system-two-provider",
	hS2Input:        "system-two-input",
	hS2Config:       "system-two-config",
	hS2Home:         "system-two-home",
	hJevAskState:    "jev-ask-state",
	hJevAskConfig:   "jev-ask-config",
	hJevAskKey:      "jev-ask-key",
	hJevAskPayload:  "jev-ask-payload",
	hJevAskAnswer:   "jev-ask-answer",
	hJevAskHome:     "jev-ask-home",
	// B553
	hReviewHome:            "review-home",
	hReviewConfig:          "review-config",
	hReviewAcceptanceShape: "review-acceptance-shape",
	hReviewRunRecord:       "review-run-record",
	hReviewTmpRoot:         "review-tmp-root",
	hReviewSelf:            "review-self-binary",
	// B551r
	hSpawnCodexSandbox: "spawn-codex-sandbox",
	// B556
	hFactsGlobClaim: "facts-glob-claim",
	// B578
	hRebaseHome:           "rebase-home",
	hRebaseRunRecord:      "rebase-run-record",
	hRebaseAutopilotShape: "rebase-autopilot-shape",
	hRebaseRecordShape:    "rebase-record-shape",
	hRebaseStorage:        "rebase-storage",
	hRebaseQueue:          "rebase-queue-unreadable",
	hRebasePath:           "rebase-path-unresolved",
	hRebaseKey:            "rebase-key-unreadable",
	hRebaseLedger:         "rebase-ledger-unreadable",
	hRebaseContentHash:    "rebase-content-hash",
	// B581
	hOpenHomeLookup:     "open-home-lookup",
	hOpenLevelLedger:    "open-level-ledger",
	hOpenBriefCwd:       "open-brief-cwd",
	hOpenAcceptanceYAML: "open-acceptance-yaml",
	hOpenPipelineForm:   "open-pipeline-form",
	hOpenOutcomeForm:    "open-outcome-form",
	// B587
	hCloseLockfileFormat: "close-lockfile-format",
}
