package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ricardov03/code-forge/native/autopilot"
	"github.com/ricardov03/code-forge/native/blockclose"
	"github.com/ricardov03/code-forge/native/daemonclient"
	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/humanask"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/queue"
	"github.com/ricardov03/code-forge/native/runstatus"
	"github.com/ricardov03/code-forge/native/state"
	"github.com/ricardov03/code-forge/native/ticket"
)

func init() { blockSubs["close"] = runBlockClose }

// ready stays with Node: a preview collects fatal and nonfatal blockers together,
// whereas a real close stops immediately on a failed pin or tampered gate row.
func runBlockClose(args []string) int { return closeWithFlags(args) }

func blockCloseStep(args []string, step *landStep) int {
	return blockCloseStepWithReason(args, step, nil)
}

// Preflight returns its reason to the caller, which records only the final
// whole-command handover. Shared land preflights keep their existing sentinel.
func blockCloseStepWithReason(args []string, step *landStep, handover *handoverReason, endHook ...*func()) int {
	return blockCloseHumanStep(args, step, handover, false, endHook...)
}

func blockCloseHumanStep(args []string, step *landStep, handover *handoverReason, confirmed bool, endHook ...*func()) int {
	return blockCloseReviewedStep(args, step, handover, confirmed, nil, endHook...)
}

func blockCloseReviewedStep(args []string, step *landStep, handover *handoverReason, confirmed bool, waited closeReviewWait, endHook ...*func()) int {
	stdout, stderr := step.writers()

	original := append([]string{"close"}, args...)
	var expiryOnFailure func()
	fallback := func(reason handoverReason) int {
		if waited != nil {
			fmt.Fprintln(stderr, "block close: review_pending: state changed while waiting for reviews; close again")
			return 1
		}
		if handover != nil {
			*handover = reason
		}
		if confirmed || step != nil {
			return landHandover
		}
		return blockHandover(original, reason)
	}
	fail := func(err error) int {
		// B451: a filesystem error prints in Node's errno form; the caller logs it.
		var se *state.Error
		if !errors.As(err, &se) {
			fmt.Fprintln(stderr, "block close:", blockIOMessage(err))
			return 1
		}
		if expiryOnFailure != nil {
			expiryOnFailure()
		}
		fmt.Fprintln(stderr, "block close:", err)
		if se.Code == "usage" {
			return 2
		}
		return 1
	}
	usage := func(text string) int { return fail(&state.Error{Code: "usage", Message: text}) }
	for _, arg := range args {
		// Node prints the verb usage for an exact --help or -h anywhere in args.
		if arg == "--help" || arg == "-h" {
			fmt.Fprint(stdout, blockVerbUsage("close", "<id> --run <r> [--worker-pid <pid>] [--transcript <file>] [--report <file>] [--no-require-reviews] [--remove-worktree] [--auto-claim] [--decide]"))
			return 0
		}
	}
	flags, pos, message := closeFlags(args)
	if message != "" {
		return usage(message)
	}
	run := blockString(flags["run"])
	if len(pos) == 0 || pos[0] == "" || run == "" {
		return usage("a block id and --run are required")
	}
	id := pos[0]
	home, err := os.UserHomeDir()
	if err != nil {
		return fallback(hCloseHome)
	}
	record, err := state.ReadRun(home, run)
	if err != nil {
		return fail(err)
	}
	deciding := flags["decide"] == true
	// B390: an active grant lets a non-TTY --decide stand in for the owner.
	var decideGrant ledger.Object
	if deciding {
		reason, ok := flags["reason"].(string)
		if len(pos) != 1 || !ok || humanTrim(reason) == "" || strings.ContainsFunc(reason, func(r rune) bool { return r < 32 || r == 127 }) {
			return usage("--decide needs one block id and a nonblank --reason without control characters")
		}
		if flags["no-require-reviews"] == true {
			return usage("--decide cannot skip reviews")
		}
		active := false
		if grant := blockObject(record.Get("autopilot")); grant != nil {
			s, err := autopilot.GrantState(grant, time.Now())
			if err != nil {
				return fallback(hCloseDecide)
			}
			if active = s == "active" && record.Get("status") == "active"; active {
				decideGrant = grant
			}
		}
		if !humanask.Available() && !active {
			return humanNoTTY(stderr, "close")
		}
		if active && humanask.Available() {
			// The owner at a terminal is not the autopilot; keep their decide
			// apart from the grant as before.
			return fail(&state.Error{Code: "autopilot-active", Message: "block close --decide is refused while an autopilot grant is active"})
		}
	}
	autoDecide := decideGrant != nil
	refuseAuto := func(why string) int {
		fmt.Fprintln(stderr, autopilotDecideRefusal(why))
		return 1
	}
	if step != nil && step.preflight && step.record != nil {
		record = step.record
	}
	if record.Get("autopilot") != nil && flags["auto-claim"] == true {
		return fallback(hCloseAutoClaim)
	}
	// This is the hand-over boundary. Everything through preflight is read-only;
	// it must finish before a transcript warning or a stopped-block row is signed.
	safeGrant := blockGrantEndSafe(record)
	if step != nil && handover == nil {
		safeGrant = blockEndSafe(record)
	}
	if !safeGrant || !blockStorageSafe(home, run, record) {
		return fallback(blockStateHandoverReason(record, "close", hCloseStateStorage))
	}
	cfg, ok := blockSnapshot(record)
	if !ok {
		return fallback(hCloseConfig)
	}
	requireHuman := blockMap(cfg["close"])["require_human"] == true
	if autoDecide && requireHuman {
		grant, _ := autopilot.Read(record, time.Now())
		if why := autopilot.RoutineRefusal(grant); why != "" {
			return refuseAuto("close.require_human is set and the grant does not allow close:routine (" + why + ")")
		}
	}
	if requireHuman && step != nil && handover == nil {
		return fallback(hCloseHuman)
	}
	// Auto-claim's speculative close must not sign claims before a human can
	// cancel the ordinary require_human prompt. Retain that composed boundary.
	if !deciding && requireHuman && flags["auto-claim"] == true && humanask.Available() {
		return fallback(hCloseHuman)
	}
	if !deciding && !requireHuman && flags["no-require-reviews"] != true && (step == nil || !step.preflight) {
		expiryOnFailure = func() { blockGrantExpiry(home, run, stderr) }
	}
	var routineGrant ledger.Object
	external := closeExternalReviewer(cfg)
	// Land's composed close has no reason receiver. Keep its existing whole-
	// command handover; only the direct close and close-flags caller opt in.
	if external != "" && step != nil && handover == nil {
		return fallback(hCloseExternalReview)
	}
	entry := blockObject(blockObject(record.Get("blocks")).Get(id))
	if entry == nil {
		return fail(&state.Error{Code: "unknown_block", Message: fmt.Sprintf("block %s is not in run %s", id, run)})
	}
	if entry.Get("status") != "open" {
		return fail(&state.Error{Code: "no-block", Message: fmt.Sprintf("block %s is not open in run %s", id, run)})
	}
	if flags["remove-worktree"] == true && entry.Get("worktree") == nil {
		return usage(fmt.Sprintf("block %s has no worktree (proof.isolation is not worktree); --remove-worktree does not apply", id))
	}
	if checks := entry.Get("human_checks"); checks != nil {
		values, ok := checks.([]any)
		if !ok {
			return fallback(hCloseHumanChecksShape)
		}
		for _, value := range values {
			if _, ok := value.(string); !ok {
				return fallback(hCloseHumanChecksValue)
			}
		}
	}
	workspace := blockString(record.Get("workspace"))
	mainTop, err := gitx.RepoTop(workspace)
	if err != nil {
		return fallback(hCloseRepo)
	}
	top, base, inTree := mainTop, blockString(entry.Get("base_sha")), false
	if wt := entry.Get("worktree"); wt != nil {
		tree := blockObject(wt)
		p, branch, sha := blockString(tree.Get("path")), blockString(tree.Get("branch")), blockString(tree.Get("base_sha"))
		if !filepath.IsAbs(p) || branch == "" || !regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`).MatchString(sha) || strings.ContainsAny(p+branch, "\x00\r\n\t") {
			return fallback(hCloseWorktreeShape)
		}
		real, e := filepath.EvalSymlinks(p)
		if e != nil {
			if !os.IsNotExist(e) {
				return fallback(hCloseWorktreeResolve)
			}
			return fail(&state.Error{Code: "worktree-missing", Message: fmt.Sprintf("block %s: its worktree %s is gone (removed or moved), so its work cannot be measured — restore it on branch %s (git worktree prune, then git worktree add %s %s) and close again; nothing was recorded", id, p, branch, p, branch)})
		}
		st, e := os.Stat(real)
		if e != nil || !st.IsDir() {
			return fallback(hCloseWorktreeStat)
		}
		actual, e := gitx.Run(real, "symbolic-ref", "--quiet", "--short", "HEAD")
		if e != nil || strings.TrimSpace(actual) != branch {
			return fallback(hCloseWorktreeBranch)
		}
		common, e := gitx.Run(real, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if e != nil {
			return fallback(hCloseWorktreeCommonDir)
		}
		mainCommon, e := gitx.Run(workspace, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if e != nil || statusReal(strings.TrimSpace(common)) != statusReal(strings.TrimSpace(mainCommon)) {
			return fallback(hCloseWorktreeIdentity)
		}
		prefix := gatecheck.Prefix(mainTop, workspace)
		if st, e := os.Stat(filepath.Join(real, prefix)); e != nil || !st.IsDir() {
			return fallback(hCloseWorktreePrefix)
		}
		top, base, inTree = real, sha, true
	}
	if !deciding && requireHuman && flags["no-require-reviews"] != true {
		grant, _ := autopilot.Read(record, time.Now())
		active := false
		if grant.Grant != nil {
			s, _ := autopilot.GrantState(grant.Grant, time.Now())
			active = s == "active"
		}
		if active {
			if why := autopilot.RoutineRefusal(grant); why != "" {
				return fail(&state.Error{Code: "close-not-routine", Message: "close-not-routine: " + why})
			}
			routineGrant = grant.Grant
		} else {
			if !humanask.Available() {
				return humanNoTTY(stderr, "close")
			}
			if !confirmed && (step == nil || !step.preflight) {
				if code := humanConfirm(stderr, "close", id, fmt.Sprintf("Close block %s of run %s? Only the owner may say yes.", id, run)); code != 0 {
					return code
				}
				code := blockCloseHumanStep(args, step, handover, true, endHook...)
				if code == landHandover {
					fmt.Fprintln(stderr, "block close: state changed after human confirmation")
					return 1
				}
				return code
			}
		}
	}
	if flags["no-require-reviews"] == true {
		// A terminal confirmation remains a human close, owned by Node. Pipes
		// refuse before confirmation, expiry, claims, or any signed writes.
		if closeMayHaveTerminal() {
			return fallback(hCloseNoRequireReviews)
		}
		return usage("--no-require-reviews needs the human at a terminal to confirm; the block stays open (there is no --yes)")
	}
	if record.Get("status") != "active" {
		return fail(&state.Error{Code: "run-ended", Message: "run " + run + " has ended"})
	}
	all, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge/ledger"), blockString(record.Get("project")))
	if err != nil {
		return fallback(hCloseLedgerRead)
	}
	rows := []gatecheck.Row{}
	for _, v := range all {
		r, ok := v.(map[string]any)
		if !ok {
			return fallback(hCloseLedgerShape)
		}
		if r["run"] == run {
			rows = append(rows, r)
		}
	}
	// Node's checks read only this block's rows; another block's row never
	// sends the close to Node (B451).
	mineRows := []gatecheck.Row{}
	for _, r := range rows {
		if r["block"] == id {
			mineRows = append(mineRows, r)
		}
	}
	if !gatecheck.Supported(mineRows) {
		return fallback(hCloseGatesFormat)
	}
	key, err := blockclose.ReadKey(home, run)
	if err != nil {
		return fail(err)
	}
	transcript, missing, err := closeTranscript(record, entry, id, blockString(flags["transcript"]))
	if err != nil {
		// B466: Node's readFile error ends the close (exit 1, no state written).
		return fail(errors.New(closeTranscriptReadMessage(err)))
	}
	hits, supported := gatecheck.Transcript(transcript)
	if !supported {
		return fallback(hCloseTranscriptFormat)
	}
	highPaths, ok := closeHighPaths(workspace, base)
	if !ok {
		return fallback(hCloseHighPaths)
	}
	prefix := gatecheck.Prefix(mainTop, workspace)
	changed, err := gitx.ChangedFiles(top, base)
	if err != nil {
		return fallback(hCloseChangedFiles)
	}
	files, reviewed, err := gatecheck.FileSet(top, base, prefix, id, entry, rows, changed)
	if err != nil {
		return fallback(hCloseFileSet)
	}
	// A durable or legacy stopped state can requeue even without a cap row.
	if !closeNoRequeue(home, run, id, top, rows, cfg) {
		return fallback(hCloseRequeue)
	}
	qdir := filepath.Join(mainTop, ".code-forge/queue")
	idx := queue.Index{Dir: qdir}
	if idx.Refresh() != nil {
		return fallback(hCloseQueueRefresh)
	}
	for _, f := range files {
		p := ticket.Parts{Run: run, Block: id, File: f.File, ContentHash: f.Hash}
		if inTree {
			p.Tree = &top
		}
		if rev, ok := entry.Get("acceptance_revision").(string); ok {
			p.AcceptanceRevision = &rev
		}
		if _, e := queue.Identity(qdir, p); e != nil {
			return fallback(hCloseQueueIdentity)
		}
	}
	var live *float64
	if text, has := flags["worker-pid"].(string); has {
		if !regexp.MustCompile(`^[1-9][0-9]*$`).MatchString(text) {
			return usage("--worker-pid must be a positive integer")
		}
		n, e := strconv.ParseFloat(text, 64)
		if e != nil || n < 1 || n > 9007199254740991 {
			return usage("--worker-pid must be a positive integer")
		}
		live = &n
	}
	// Verify all read-only report and test prerequisites before the first write.
	var reportData []byte
	var reportErr error
	if report, has := flags["report"].(string); has {
		reportData, reportErr = os.ReadFile(report)
	}
	reportText := string(reportData)
	if !closeTextSafe(reportText) || strings.Contains(reportText, hex.EncodeToString(key)) {
		return fallback(hCloseReportRedaction)
	}
	checks, closeRows := []closeCheck{}, []ledger.Object{}
	if external != "" {
		closeRows = append(closeRows, ledger.Object{{Key: "event", Value: "gate.reviews_waived"}, {Key: "block", Value: id}, {Key: "by", Value: "config"}, {Key: "reviewer", Value: external}})
	}
	if blockMap(cfg["context"])["require_coder_tests"] == true {
		command, reason, supported := closeCoderTests(flags["report"], id, reportData, reportErr)
		if !supported {
			return fallback(hCloseCoderTests)
		}
		if reason != "" {
			checks = append(checks, closeCheck{reason, []gatecheck.Blocker{{Code: "coder_report_failed", Detail: reason}}})
		} else {
			closeRows = append(closeRows, ledger.Object{{Key: "event", Value: "coder.tests"}, {Key: "block", Value: id}, {Key: "command", Value: command}, {Key: "exit", Value: 0}})
		}
	}
	// Node's acknowledgement check runs after coder-tests and before the test gate.
	acked, ackKnown := gatecheck.AgentWaiversAcked(blockMap(blockMap(cfg["review"])["agent_waivers"])["require_ack"] == true, run, id, rows, key)
	if !ackKnown {
		return fallback(hCloseGatesFormat)
	}
	if !acked {
		if autoDecide && !confirmed {
			return refuseAuto("agent waivers are not acknowledged; the owner acknowledges them")
		}
		if deciding && !confirmed {
			return fallback(hCloseDecide)
		}
		checks = append(checks, closeCheck{"agent-waivers-not-acked", []gatecheck.Blocker{{Code: "agent-waivers-not-acked", Detail: "agent-waivers-not-acked"}}})
	}
	if blockMap(cfg["gates"])["test"] != nil {
		check, supported := closeTestGate(top, base, run, id, entry, rows)
		if !supported {
			return fallback(hCloseTestGate)
		}
		checks = append(checks, check)
	}
	// B451: Node checks a light block's size after the test gate, before reviews.
	if entry.Get("kind") == "light" {
		limit, ok := lightLimits(top, base, prefix, entry, changed, cfg)
		if !ok {
			return fallback(hCloseLight)
		}
		if len(limit) > 0 {
			checks = append(checks, closeCheck{"light-limits " + strings.TrimSuffix(limit[0].Detail, " exceed light block limits"), limit})
		}
	}
	// Review exclusions include lock-file validation. Keep that dialect with Node.
	for _, f := range files {
		if closeExcluded(cfg, f.File) {
			return fallback(hCloseExcludedFile)
		}
	}
	unowned, scopeWarnings, scopeChecked := gatecheck.Scope(top, prefix, id, run, blockObject(record.Get("blocks")), changed, rows, key, inTree)
	noChanges := len(files) == 0 && !slices.ContainsFunc(gatecheck.ReviewedWork(top, rows, id), func(file string) bool { return gatecheck.Owns(prefix, entry, file) })
	if autoDecide && !confirmed && (!scopeChecked || len(scopeWarnings) > 0 || len(reviewed) > 0 && !slices.ContainsFunc(changed, func(file string) bool { return gatecheck.Owns(prefix, entry, file) })) {
		return refuseAuto("the block's scope has warnings or reviewed work without changes; the owner decides it")
	}
	if deciding && !confirmed && (!scopeChecked || len(scopeWarnings) > 0 || len(reviewed) > 0 && !slices.ContainsFunc(changed, func(file string) bool { return gatecheck.Owns(prefix, entry, file) })) {
		return fallback(hCloseDecide)
	}
	gateInput := gatecheck.Input{Run: run, Block: id, Files: files, Rows: rows, Key: key, HighPaths: highPaths, Unowned: unowned, NoChanges: noChanges, Hits: hits, Transcript: transcript, ResultFiles: gatecheck.LoadResultFiles(mainTop, run, id, rows, key)}
	if external != "" {
		// Node judges an external reviewer's close with no review files; the
		// files still prove the block changed something.
		gateInput.Files, gateInput.ProofFiles = []gatecheck.File{}, files
	}
	gate := gatecheck.Evaluate(gateInput)
	if gate.Unknown {
		return fallback(hCloseGatesFormat)
	}
	var decisions []humanDecision
	if deciding && !confirmed {
		var supported bool
		if autoDecide {
			var why string
			decisions, rows, why, supported = autopilotDecisions(run, id, blockString(flags["reason"]), transcript, decideGrant, gate, rows, key, time.Now())
			if supported && why != "" {
				return refuseAuto(why)
			}
		} else {
			decisions, rows, supported = humanDecisions(run, id, blockString(flags["reason"]), gate, rows, key)
		}
		if !supported {
			return fallback(hCloseDecide)
		}
		gateInput.Rows = rows
		gate = gatecheck.Evaluate(gateInput)
		if gate.Unknown {
			return fallback(hCloseGatesFormat)
		}
	}
	if waited != nil {
		gate.Blockers = waited.blockers(gate.Blockers)
	}
	gateReason := gatecheck.Reason(gate.Blockers)
	gateIndex := len(checks)
	checks = append(checks, closeCheck{gateReason, gate.Blockers})
	lines, lineWarnings := closeReviewedLines(run, id, top, qdir, entry, files, gate.Waived, inTree, external == "" && len(gate.Blockers) == 0)
	if _, has := flags["report"].(string); has && external == "" {
		if reportErr != nil {
			reason := "coder report FAILED: --report cannot be read"
			checks = append(checks, closeCheck{reason, []gatecheck.Blocker{{Code: "coder_report_failed", Detail: reason}}})
		} else {
			check, warnings := closeReport(reportText, run, id, qdir, files, lines, rows, key)
			checks = append(checks, check)
			lineWarnings = append(lineWarnings, warnings...)
		}
	}
	for _, w := range gate.Waived {
		r := ledger.Object{{Key: "event", Value: "gate.review_waived_all"}, {Key: "block", Value: id}}
		for _, f := range w.Object(false) {
			r.Set(f.Key, f.Value)
		}
		closeRows = append(closeRows, r)
	}
	if routineGrant != nil {
		problem := closeRoutineProblem(external, run, id, rows, key, files, gate, hits)
		if problem != "" {
			checks = append(checks, closeCheck{problem, []gatecheck.Blocker{{Code: "close-not-routine", Detail: problem}}})
		}
	}
	event, reason := blockclose.Pin(record, live, statusStart)
	if !closeQueuePins(home, run, id, record, rows, &event, &reason, mainTop) {
		return fallback(hCloseQueuePins)
	}
	if event == "" {
		if why := blockclose.Tamper(run, id, rows, key); why != "" {
			event, reason = "ledger.tamper", why
		}
	}
	if deciding && !confirmed {
		// Decision dialects not yet understood must hand over before asking or
		// signing. The existing B282 code describes that boundary.
		if step == nil || !step.preflight {
			print := io.Writer(os.Stdout)
			if flags["json"] == true {
				print = os.Stderr
			}
			for i, decision := range decisions {
				fmt.Fprintf(print, "%d. %s\n", i+1, decision.label)
			}
			why := ""
			for _, check := range checks {
				if why == "" {
					why = check.Reason
				}
			}
			orphans, _ := record.Get("orphans").([]any)
			if why == "" && len(orphans) > 0 {
				names := []string{}
				for _, v := range orphans {
					names = append(names, blockString(v))
				}
				why = "orphans: " + strings.Join(names, ", ")
			}
			if event != "" {
				why = reason
			}
			if why != "" {
				fmt.Fprintln(stderr, "cannot decide here: "+why)
				return 1
			}
			if autoDecide {
				// The grant must still be active when the rows are signed.
				now, err := state.ReadRun(home, run)
				if err != nil {
					return fail(err)
				}
				g := blockObject(now.Get("autopilot"))
				if g == nil || blockString(g.Get("grant_id")) != blockString(decideGrant.Get("grant_id")) {
					return refuseAuto("the autopilot grant changed")
				}
				if s, err := autopilot.GrantState(g, time.Now()); err != nil || s != "active" {
					return refuseAuto("the autopilot grant is no longer active")
				}
			} else if code := humanConfirm(stderr, "close", id, fmt.Sprintf("Apply these decisions and close block %s of run %s? Only the owner may say yes.", id, run)); code != 0 {
				return code
			}
			for _, decision := range decisions {
				if _, err := state.WriteSigned(home, run, decision.row); err != nil {
					fmt.Fprintln(stderr, "block close:", blockIOMessage(err))
					return 1
				}
			}
			code := blockCloseHumanStep(args, step, handover, true, endHook...)
			if code == landHandover {
				if autoDecide {
					fmt.Fprintln(stderr, "block close: state changed after the autopilot decision was recorded")
					return 1
				}
				fmt.Fprintln(stderr, "block close: state changed after human confirmation")
				return 1
			}
			return code
		}
	}
	audit, auditOK := closeAuditOf(id, rows, key, gate.Accepted, flags["json"] == true)
	if !auditOK {
		return fallback(hCloseGatesFormat)
	}
	// No deferred read may hand over past here. From the first append onward an
	// error is reported directly, never retried through Node.
	if step != nil && step.preflight {
		return 0
	}
	if waited == nil && external == "" && event == "" {
		prerequisites := true
		for _, check := range checks[:gateIndex] {
			prerequisites = prerequisites && check.Reason == ""
		}
		if prerequisites {
			print := stdout
			if flags["json"] == true {
				print = stderr
			}
			waited = closeWaitReviews(run, id, top, qdir, files, changed, gate.Blockers, cfg, inTree, print)
			if len(waited) > 0 {
				// Re-measure everything, including hashes, signatures, worker pins,
				// report, human requirements and grant rules after the single wait.
				// Queue publication is a write: this pass must never hand over.
				return blockCloseReviewedStep(args, step, handover, confirmed, waited, endHook...)
			}
		}
	}
	if !deciding && !requireHuman {
		blockGrantExpiry(home, run, stderr)
	}
	if step == nil {
		blockBreadcrumb("close")
	}
	publishError := func(err error) int { fmt.Fprintln(stderr, "block close:", blockIOMessage(err)); return 1 }
	var result blockclose.Result
	if routineGrant != nil {
		preview := blockclose.Result{Status: "open", Blockers: []gatecheck.Blocker{}}
		for _, check := range checks {
			if preview.Reason == "" && check.Reason != "" {
				preview.Reason = check.Reason
			}
			preview.Blockers = append(preview.Blockers, check.Blockers...)
		}
		if event != "" {
			preview.Reason, preview.Blockers = reason, []gatecheck.Blocker{}
		}
		if preview.Reason != "" {
			blockers := closeBlockersJSON(preview.Blockers, id, run, flags, entry)
			if flags["json"] == true {
				out := ledger.Object{{Key: "block", Value: id}, {Key: "ok", Value: false}, {Key: "reason", Value: preview.Reason}, {Key: "blockers", Value: blockers}}
				data, _ := ledger.Stringify(out, false)
				fmt.Fprintln(stdout, string(data))
			} else {
				for _, value := range blockers {
					o := value.(ledger.Object)
					fmt.Fprintf(stdout, "%s %s %s\n  fix: %s\n", o.Get("code"), closeDash(o.Get("file")), o.Get("detail"), o.Get("fix"))
				}
			}
			fmt.Fprintf(stderr, "close-not-routine: %s\n", preview.Reason)
			return 1
		}
		// Node replaces preview rows, then the real close re-runs coderTests.
		// Our checks are already measured, so retain that row after the grant row.
		audit := []ledger.Object{{{Key: "event", Value: "autopilot.close"}, {Key: "block", Value: id}, {Key: "grant", Value: routineGrant.Get("grant_id")}}}
		for _, row := range closeRows {
			if row.Get("event") == "coder.tests" {
				audit = append(audit, row)
			}
		}
		closeRows = audit
	}
	if event != "" {
		result, err = blockclose.Stop(home, run, id, event, reason)
		if err != nil {
			return publishError(err)
		}
	} else {
		collectOnly := false
		// The review load is after coder-tests and the configured test gate.
		for _, check := range checks[:gateIndex] {
			if check.Reason != "" {
				collectOnly = true
			}
		}
		if missing && !collectOnly {
			_, err = state.WriteSigned(home, run, ledger.Object{{Key: "event", Value: "gate.transcript_missing"}, {Key: "block", Value: id}})
			if err != nil {
				return publishError(err)
			}
			fmt.Fprintf(stderr, "WARN block %s: no coder transcript found (--transcript, run record, .code-forge/runs/%s/%s.log); the transcript grep did not run\n", id, run, id)
		}
		if !collectOnly {
			if len(reviewed) > 0 && !slices.ContainsFunc(changed, func(file string) bool { return gatecheck.Owns(prefix, entry, file) }) {
				where, likely := "the workspace", "committed before block open or done in another worktree"
				if inTree {
					where = "the worktree " + blockString(blockObject(entry.Get("worktree")).Get("path"))
					likely = "done outside the block's worktree (in the main checkout or another tree)"
				}
				fmt.Fprintf(stderr, "WARN block %s: no owned file differs from the block base in %s, but %d owned file(s) have review rows — the work was likely %s; they are checked at their content in %s\n", id, where, len(reviewed), likely, where)
			}
			if !scopeChecked {
				fmt.Fprintf(stderr, "WARN block %s: opened before code-forge recorded the tree at block open; unowned changes were not checked\n", id)
			}
			for _, w := range scopeWarnings {
				fmt.Fprint(stderr, w)
			}
		}
		result = blockclose.Result{Status: "open", Blockers: []gatecheck.Blocker{}}
		for _, check := range checks {
			if check.Reason != "" && result.Reason == "" {
				result.Reason = check.Reason
			}
			result.Blockers = append(result.Blockers, check.Blockers...)
		}
		orphans, _ := record.Get("orphans").([]any)
		names := []string{}
		for _, v := range orphans {
			file := blockString(v)
			names = append(names, file)
			result.Blockers = append(result.Blockers, gatecheck.Blocker{Code: "unowned_change", File: file, Detail: "orphan in run registry: " + file})
		}
		if len(names) > 0 && result.Reason == "" {
			result.Reason = "orphans: " + strings.Join(names, ", ")
		}
		if result.Reason == "" {
			result, err = blockclose.Close(home, run, id, closeRows)
			if err != nil {
				return publishError(err)
			}
		}
	}
	jsonFlag := flags["json"] == true
	blockers := closeBlockersJSON(result.Blockers, id, run, flags, entry)
	if !result.OK && !jsonFlag {
		for _, b := range blockers {
			o := b.(ledger.Object)
			fmt.Fprintf(stdout, "%s %s %s\n  fix: %s\n", o.Get("code"), closeDash(o.Get("file")), o.Get("detail"), o.Get("fix"))
		}
	}
	doneFile := ""
	ended := result.Status == "closed" || result.Status == "stopped"
	if ended {
		doneFile = closeWriteDone(home, run, id, record, result, lines, rows, key, stderr)
		if len(endHook) > 0 {
			// The flags caller flushes its buffered output before invoking this.
			*endHook[0] = func() { blockRunEndHook(home, run, id, result.Status, doneFile, record, os.Stderr) }
		} else {
			defer blockRunEndHook(home, run, id, result.Status, doneFile, record, stderr)
		}
	}
	if result.OK {
		closeWarnMovedBase(workspace, entry, id, run, stderr)
		for _, w := range lineWarnings {
			fmt.Fprint(stderr, w)
		}
		if !jsonFlag {
			for _, v := range lines {
				l := v.(ledger.Object)
				fmt.Fprintf(stdout, "reviewed %s ticket %s\n", l.Get("file"), l.Get("ticket"))
			}
			for _, w := range gate.Waived {
				parts := []string{}
				if len(w.Findings) > 0 {
					parts = append(parts, strings.Join(w.Findings, ", ")+"; by "+fmt.Sprint(w.By))
				}
				if len(w.Answered) > 0 {
					parts = append(parts, "answered "+strings.Join(w.Answered, ", "))
				}
				fmt.Fprintf(stdout, "block %s: %s reviewed_by_waiver (%s)\n", id, w.File, strings.Join(parts, "; "))
			}
			for _, line := range audit.text {
				fmt.Fprint(stdout, line)
			}
		}
	}
	if result.Status == "awaiting_human_check" {
		if !jsonFlag {
			for _, v := range entry.Get("human_checks").([]any) {
				fmt.Fprintf(stdout, "block %s awaiting human check: %s\n", id, v)
			}
			fmt.Fprintf(stdout, "next: code-forge block human-pass %s --run %s --note \"<device check note>\" [--proof <dir-or-file>]\n", id, run)
		}
	} else {
		text := fmt.Sprintf("block %s %s", id, result.Status)
		if result.Event != "" {
			text += " (" + result.Event + ")"
		}
		if result.Reason != "" {
			text += ": " + result.Reason
		}
		text += "\n"
		if !result.OK {
			fmt.Fprint(stderr, text)
		} else if !jsonFlag {
			fmt.Fprint(stdout, text)
		}
	}
	var removedTree ledger.Object
	if result.OK && result.Status != "awaiting_human_check" && flags["remove-worktree"] == true {
		tree := blockObject(entry.Get("worktree"))
		why := blockclose.RemoveWorktree(workspace, blockString(tree.Get("path")))
		row := ledger.Object{{Key: "event", Value: "worktree.removed"}, {Key: "block", Value: id}, {Key: "path", Value: tree.Get("path")}}
		if why == "" {
			row.Set("branch", tree.Get("branch"))
		} else {
			row.Set("event", "worktree.remove_failed")
			row.Set("reason", why)
		}
		if _, err := state.WriteSigned(home, run, row); err != nil {
			return publishError(err)
		}
		if why != "" {
			fmt.Fprintf(stderr, "block %s worktree NOT removed: %s — the block stays closed; the tree stays at %s (commit or clean it, then git worktree remove it)\n", id, why, tree.Get("path"))
			return 1
		}
		if !jsonFlag {
			fmt.Fprintf(stdout, "block %s worktree removed: %s (branch %s kept)\n", id, tree.Get("path"), tree.Get("branch"))
		}
		removedTree = ledger.Object{{Key: "removed", Value: true}, {Key: "path", Value: tree.Get("path")}, {Key: "branch", Value: tree.Get("branch")}}
	}
	if jsonFlag {
		var why any
		if result.Reason != "" {
			why = result.Reason
		}
		out := ledger.Object{{Key: "block", Value: id}}
		if !result.OK {
			out.Set("ok", false)
			out.Set("reason", why)
			if result.Status != "stopped" || len(blockers) > 0 {
				out.Set("blockers", blockers)
			}
		} else {
			out.Set("event", result.Status)
			out.Set("status", result.Status)
			out.Set("reason", why)
			var event any
			if result.Event != "" {
				event = result.Event
			}
			out.Set("close_event", event)
			if result.Status == "awaiting_human_check" {
				out.Set("human_checks", entry.Get("human_checks"))
				out.Set("next_command", fmt.Sprintf("code-forge block human-pass %s --run %s --note \"<device check note>\"", id, run))
			}
			if ended {
				blockDoneJSON(&out, record, doneFile)
			}
			if len(gate.Waived) > 0 {
				ws := []any{}
				for _, w := range gate.Waived {
					ws = append(ws, w.Object(true))
				}
				out.Set("reviewed_by_waiver", ws)
			}
			if len(lines) > 0 {
				out.Set("reviewed", lines)
			}
			for _, item := range audit.json {
				out.Set(item[0].(string), item[1])
			}
			if removedTree != nil {
				out.Set("worktree", removedTree)
			}
		}
		if !result.OK && ended {
			blockDoneJSON(&out, record, doneFile)
		}
		data, _ := ledger.Stringify(out, false)
		fmt.Fprintln(stdout, string(data))
	}
	if result.OK {
		return 0
	}
	return 1
}

type closeCheck struct {
	Reason   string
	Blockers []gatecheck.Blocker
}

// Node's routineCloseProblem runs after the ordinary review gate. Its policy
// never promotes a waived/answered file to an approved file.
func closeRoutineProblem(external, run, id string, rows []gatecheck.Row, key []byte, files []gatecheck.File, gate gatecheck.Result, hits []gatecheck.Blocker) string {
	if external != "" {
		return "external reviews"
	}
	problems := []string{}
	waiver, rule, green := false, false, false
	for _, row := range rows {
		if row["run"] != run || row["block"] != id {
			continue
		}
		event := blockString(row["event"])
		waiver = waiver || strings.Contains(event, "waiv")
		rule = rule || event == "rule_break" || event == "rule_break.accepted"
		green = green || event == "gate.done" && row["all_ok"] == true && gatecheck.Verified(row, key)
	}
	if waiver {
		problems = append(problems, "waiver rows for block")
	}
	if rule {
		problems = append(problems, "rule_break or accepted hit for block")
	}
	for _, hit := range hits {
		problems = append(problems, "rule_break "+hit.Detail)
	}
	if !green {
		problems = append(problems, "no signed green gate row")
	}
	for _, b := range gate.Blockers {
		if b.Code == "rule_break" && slices.ContainsFunc(hits, func(h gatecheck.Blocker) bool { return h.Detail == b.Detail }) {
			continue
		}
		problem := b.Code
		if b.File != "" {
			problem += " " + b.File
		}
		problems = append(problems, problem)
	}
	if len(gate.Waived) > 0 {
		problems = append(problems, "reviewed_by_waiver or answered-only file")
	}
	if len(files) == 0 {
		problems = append(problems, "no approved changed files")
	}
	return strings.Join(problems, "; ")
}

func closeTranscript(record, entry ledger.Object, id, given string) (string, bool, error) {
	if given != "" {
		data, err := os.ReadFile(given)
		return string(data), false, err
	}
	workspace, run := blockString(record.Get("workspace")), blockString(record.Get("run_id"))
	paths := []string{}
	if p := blockString(entry.Get("transcript")); p != "" {
		if !filepath.IsAbs(p) {
			p = filepath.Join(workspace, p)
		}
		paths = append(paths, p)
	}
	paths = append(paths, filepath.Join(workspace, ".code-forge/runs", run, id+".log"))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err == nil {
			return string(data), false, nil
		}
		if !os.IsNotExist(err) {
			return "", false, err
		}
	}
	return "", true, nil
}

// closeTranscriptReadMessage is Node's readFile error text: errno, reason and
// syscall, with the path only on open (a read of a directory has no path).
func closeTranscriptReadMessage(err error) string {
	message := blockIOMessage(err)
	var pathErr *os.PathError
	if errors.As(err, &pathErr) && pathErr.Op == "read" {
		message = strings.TrimSuffix(message, " '"+pathErr.Path+"'")
	}
	return message
}

func closeTextSafe(text string) bool {
	// Node redacts commands in coder.tests and done-file reasons. Delegate before
	// publication if a report can require the secret scrubber's larger vocabulary.
	return !regexp.MustCompile(`sk-|xai-|xox[baprs]-|gh[opsu]_|github_pat_|AKIA|AIza|eyJ`).MatchString(text)
}

func closeNoRequeue(home, run, id, top string, rows []gatecheck.Row, cfg map[string]any) bool {
	paths := []string{filepath.Join(home, ".code-forge/runs", run, "review-state", id)}
	root := blockString(blockMap(cfg["tmp"])["root"])
	if root == "" {
		root = os.Getenv("CODE_FORGE_TMP_ROOT")
	}
	if root == "" {
		root = filepath.Join(os.TempDir(), "code-forge")
	}
	paths = append(paths, filepath.Join(root, run, "review-state", id))
	for _, dir := range paths {
		entries, err := os.ReadDir(dir)
		if err != nil && !os.IsNotExist(err) {
			return false
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".json") {
				return false
			}
		}
	}
	return true
}

func closeHighPaths(workspace, base string) ([]any, bool) {
	return gatecheck.HighPaths(workspace, base, version)
}

func closeExcluded(cfg map[string]any, file string) bool {
	patterns := []string{"package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "bun.lock", "bun.lockb", "composer.lock", "Cargo.lock", "Gemfile.lock", "poetry.lock", "uv.lock", "go.sum", "Package.resolved", "Podfile.lock", "pubspec.lock", "mix.lock", "flake.lock"}
	if values, ok := blockMap(cfg["review"])["exclude"].([]any); ok {
		patterns = []string{}
		for _, v := range values {
			patterns = append(patterns, blockString(v))
		}
	}
	for _, p := range patterns {
		if p == file || p == "**/"+filepath.Base(file) || p == filepath.Base(file) {
			return true
		}
	}
	// Any configured complex pattern stays with Node, even if this file might not match.
	return len(patterns) > 0 && blockMap(cfg["review"])["exclude"] != nil
}

func closeQueuePins(home, run, id string, record ledger.Object, rows []gatecheck.Row, event, reason *string, top string) bool {
	pins, _ := record.Get("workers").([]any)
	if pins == nil {
		if p := record.Get("worker"); p != nil {
			pins = []any{p}
		}
	}
	history, _ := record.Get("worker_history").([]any)
	for _, r := range rows {
		if r["block"] == id && r["event"] == "review.result" && !gatecheck.NeverReviewed(r) && r["worker"] != nil {
			worker, _ := r["worker"].(map[string]any)
			matched := false
			for _, v := range append(slices.Clone(pins), history...) {
				p := blockObject(v)
				if p.Get("pid") == worker["pid"] && p.Get("started_at") == worker["started_at"] {
					matched = true
				}
			}
			if !matched {
				*event, *reason = "worker.replaced", "a counted review result does not name a pinned worker"
			}
		}
	}
	if *event != "" {
		return true
	}
	names, err := os.ReadDir(filepath.Join(top, ".code-forge/queue"))
	if err != nil && !os.IsNotExist(err) {
		return false
	}
	for _, name := range names {
		if !regexp.MustCompile(`^[0-9a-f]{24}\.json$`).MatchString(name.Name()) {
			continue
		}
		data, e := os.ReadFile(filepath.Join(top, ".code-forge/queue", name.Name()))
		if e != nil {
			return false
		}
		var queued map[string]any
		if json.Unmarshal(data, &queued) != nil {
			return false
		}
		if queued["run"] != run || queued["block"] != id {
			continue
		}
		ticket := strings.TrimSuffix(name.Name(), ".json")
		sum := sha256.Sum256([]byte("ticket\x00" + run + "\x00" + ticket))
		data, e = os.ReadFile(filepath.Join(home, ".code-forge/locks", hex.EncodeToString(sum[:])+".lock"))
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return false
		}
		var claim map[string]any
		if json.Unmarshal(data, &claim) != nil {
			return false
		}
		for _, v := range pins {
			p := blockObject(v)
			if p.Get("pid") == claim["pid"] && p.Get("started_at") == claim["start"] {
				_, why := blockclose.Pin(ledger.Object{{Key: "worker", Value: p}}, nil, statusStart)
				if why != "" {
					*event, *reason = "worker.down", fmt.Sprintf("dead pinned worker pid %v holds ticket %s", p.Get("pid"), ticket)
					return true
				}
			}
		}
	}
	return true
}

func closeReviewedLines(run, id, top, qdir string, entry ledger.Object, files []gatecheck.File, waived []gatecheck.Waived, inTree, passed bool) ([]any, []string) {
	lines, warnings := []any{}, []string{}
	if !passed {
		return lines, warnings
	}
	for _, f := range files {
		p := ticket.Parts{Run: run, Block: id, File: f.File, ContentHash: f.Hash}
		if inTree {
			p.Tree = &top
		}
		if rev, ok := entry.Get("acceptance_revision").(string); ok {
			p.AcceptanceRevision = &rev
		}
		hash, err := gitx.ContentHash(top, f.File)
		identity, e := queue.Identity(qdir, p)
		if err != nil || hash != f.Hash || e != nil {
			warnings = append(warnings, "WARN no ticket for "+f.File+"\n")
			continue
		}
		via := "approved"
		for _, w := range waived {
			if w.File == f.File && w.Hash == f.Hash {
				via = "waiver"
				if w.By == nil {
					via = "answered"
				}
			}
		}
		lines = append(lines, ledger.Object{{Key: "file", Value: f.File}, {Key: "ticket", Value: identity}, {Key: "via", Value: via}})
	}
	return lines, warnings
}

var closeHandLine = regexp.MustCompile(`^(?:(?:[-*]|[0-9]{1,9}\.)\s+)?reviewed\s+(\S+)\s+ticket\s+([0-9a-fA-F]{24})$`)

func closeReport(text, run, id, qdir string, files []gatecheck.File, lines []any, rows []gatecheck.Row, key []byte) (closeCheck, []string) {
	tickets := map[string]string{}
	order := []string{}
	current := map[string]string{}
	for _, f := range files {
		current[f.File] = f.Hash
	}
	for _, v := range lines {
		l := v.(ledger.Object)
		file := blockString(l.Get("file"))
		tickets[file] = blockString(l.Get("ticket"))
		order = append(order, file)
	}
	for _, r := range rows {
		file := blockString(r["file"])
		hash, has := current[file]
		if r["event"] == "review.result" && r["run"] == run && r["block"] == id && r["status"] == "reviewed" && r["approved"] == true && has && r["content_hash"] == hash && regexp.MustCompile(`^[0-9a-f]{24}$`).MatchString(blockString(r["ticket"])) && gatecheck.Verified(r, key) {
			if _, had := tickets[file]; !had {
				order = append(order, file)
			}
			tickets[file] = blockString(r["ticket"])
		}
	}
	hand, other := closeReportTickets(text, files), map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		m := closeHandLine.FindStringSubmatch(strings.TrimSpace(strings.ReplaceAll(line, "`", "")))
		if m != nil {
			if ticket, ok := tickets[m[1]]; ok && ticket != m[2] {
				other[m[1]] = true
			}
		}
	}
	// B405: a file with neither a report line nor a tool or ledger ticket is
	// credited by its latest done review-file ticket for the current content.
	need := map[string]string{}
	for _, f := range files {
		if !hand[f.File] && tickets[f.File] == "" {
			need[f.File] = f.Hash
		}
	}
	for file, ticket := range closeDoneTickets(qdir, run, id, need) {
		tickets[file] = ticket
	}
	missing := []string{}
	blockers := []gatecheck.Blocker{}
	for _, f := range files {
		if !hand[f.File] && tickets[f.File] == "" {
			missing = append(missing, f.File)
			blockers = append(blockers, gatecheck.Blocker{Code: "coder_report_failed", File: f.File, Detail: "coder report FAILED: no review-file ticket id for " + f.File})
		}
	}
	if len(missing) > 0 {
		return closeCheck{"coder report FAILED: no review-file ticket id for " + strings.Join(missing, ", "), blockers}, nil
	}
	warnings := []string{}
	for _, file := range order {
		if other[file] {
			warnings = append(warnings, "WARN report ticket mismatch "+file+"\n")
		}
	}
	return closeCheck{}, warnings
}

// closeDoneTickets returns, per wanted file, the latest (by enqueued_at, then
// id) review-file ticket of this run and block for the wanted content hash that
// has a done marker for a real review. It only reads the queue; an unreadable
// queue, ticket or marker credits nothing.
func closeDoneTickets(qdir, run, id string, want map[string]string) map[string]string {
	found := map[string]string{}
	if len(want) == 0 || qdir == "" {
		return found
	}
	names, err := os.ReadDir(qdir)
	if err != nil {
		return found
	}
	at := map[string]string{}
	for _, name := range names {
		if !closeTicketFile.MatchString(name.Name()) {
			continue
		}
		ticket := strings.TrimSuffix(name.Name(), ".json")
		var t map[string]any
		data, e := os.ReadFile(filepath.Join(qdir, name.Name()))
		if e != nil || json.Unmarshal(data, &t) != nil || t == nil {
			continue
		}
		file := blockString(t["file"])
		hash, ok := want[file]
		if !ok || t["run"] != run || t["block"] != id || t["content_hash"] != hash || t["kind"] != nil {
			continue
		}
		var marker map[string]any
		data, e = os.ReadFile(filepath.Join(qdir, ticket+".done"))
		if e != nil || json.Unmarshal(data, &marker) != nil || marker == nil || marker["ticket"] != ticket || blockString(marker["status"]) == "" || gatecheck.NeverReviewed(marker) || marker["status"] == "refused" {
			continue
		}
		when := blockString(t["enqueued_at"])
		if prev, had := found[file]; !had || when > at[file] || when == at[file] && ticket > prev {
			found[file], at[file] = ticket, when
		}
	}
	return found
}

var closeTicketFile = regexp.MustCompile(`^[0-9a-f]{24}\.json$`)

func closeWord(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
}
func closeTokenBoundary(line string, start, end int) bool {
	if start > 0 && (closeWord(line[start-1]) || strings.ContainsRune("./-", rune(line[start-1]))) {
		return false
	}
	if end < len(line) && (closeWord(line[end]) || strings.ContainsRune("/-", rune(line[end])) || line[end] == '.' && end+1 < len(line) && closeWord(line[end+1])) {
		return false
	}
	return true
}

var closeInlineTicket = regexp.MustCompile(`[0-9a-f]{24}`)

// missingReviewTickets accepts an inline ticket when exactly one owned path is
// named on the line; the stricter hand-line grammar is only for mismatch warnings.
func closeReportTickets(text string, files []gatecheck.File) map[string]bool {
	ticketed := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		hits := []string{}
		replaced := ""
		for _, f := range files {
			remaining := line
			offset := 0
			found := false
			var clean strings.Builder
			for offset < len(line) {
				at := strings.Index(line[offset:], f.File)
				if at < 0 {
					break
				}
				start := offset + at
				end := start + len(f.File)
				if closeTokenBoundary(line, start, end) {
					clean.WriteString(line[offset:start])
					clean.WriteByte(' ')
					found = true
				} else {
					clean.WriteString(line[offset:end])
				}
				offset = end
			}
			if found {
				clean.WriteString(line[offset:])
				remaining = clean.String()
				hits = append(hits, f.File)
				replaced = remaining
			}
		}
		if len(hits) != 1 {
			continue
		}
		for _, span := range closeInlineTicket.FindAllStringIndex(replaced, -1) {
			if closeTokenBoundary(replaced, span[0], span[1]) {
				ticketed[hits[0]] = true
			}
		}
	}
	return ticketed
}

var closeComplete = regexp.MustCompile(`^===BLOCK\s+(\S+)\s+COMPLETE===$`)
var closeFailed = regexp.MustCompile(`^===BLOCK\s+(\S+)\s+FAILED:\s*(.*?)\s*===$`)
var closeTests = regexp.MustCompile(`^TESTS\s+(\S.*?)\s+exit=(0|[1-9][0-9]{0,2})$`)

func closeCoderTests(report any, id string, data []byte, readErr error) (command, reason string, supported bool) {
	if _, ok := report.(string); !ok {
		return "", "coder report FAILED: context.require_coder_tests needs --report", true
	}
	if readErr != nil {
		return "", "coder report FAILED: --report cannot be read", true
	}
	text := string(data)
	// parseSentinel decodes JSON envelopes, but its malformed-line scan does not.
	// Delegate that uncommon dialect rather than merging the two segments.
	if json.Valid(data) {
		return "", "", false
	}
	complete, lastCommand, lastMalformed, malformed := false, "", "", ""
	exit, lastExit := -1, -1
	pending := ""
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		c, f := closeComplete.FindStringSubmatch(line), closeFailed.FindStringSubmatch(line)
		if c != nil || f != nil {
			block := ""
			if c != nil {
				block = c[1]
			} else {
				block = f[1]
			}
			if block == id {
				complete = c != nil
				lastCommand = pending
				lastExit = exit
				lastMalformed = malformed
			}
			pending, exit, malformed = "", -1, ""
			continue
		}
		m := closeTests.FindStringSubmatch(line)
		valid := false
		if m != nil {
			n, _ := strconv.Atoi(m[2])
			if n <= 255 {
				pending, exit = m[1], n
				valid = true
			}
		}
		if strings.HasPrefix(line, "TESTS ") && !valid {
			malformed = line
			if len(malformed) > 120 {
				malformed = malformed[:120]
			}
		}
	}
	if !complete {
		return "", "coder report FAILED: no ===BLOCK " + id + " COMPLETE=== sentinel", true
	}
	if lastMalformed != "" {
		return "", "coder report FAILED: TESTS line malformed: " + lastMalformed, true
	}
	if lastCommand == "" {
		return "", "coder report FAILED: no TESTS line", true
	}
	if lastExit != 0 {
		return "", fmt.Sprintf("coder report FAILED: TESTS exit=%d", lastExit), true
	}
	return lastCommand, "", true
}

var closeGateTimestamp = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?Z$`)

func closeTestGate(top, base, run, id string, entry ledger.Object, rows []gatecheck.Row) (closeCheck, bool) {
	lastRebase := -1
	for i, r := range rows {
		if r["block"] == id && r["event"] == "block.rebase" {
			lastRebase = i
		}
	}
	green := []gatecheck.Row{}
	for i, r := range rows {
		if i > lastRebase && r["block"] == id && r["event"] == "gate.done" && r["all_ok"] == true {
			green = append(green, r)
		}
	}
	if len(green) > 0 {
		args := []string{"log", "-1", "--format=%ct", base + "..HEAD", "--"}
		for _, v := range entry.Get("owned_files").([]any) {
			args = append(args, blockString(v))
		}
		commit, err := gitx.Run(top, args...)
		if err == nil {
			commit = strings.TrimSpace(commit)
			seconds, e := strconv.ParseInt(commit, 10, 64)
			if commit == "" {
				return closeCheck{}, true
			}
			if e == nil {
				for _, r := range green {
					// Node explicitly accepts non-finite Date.parse results for legacy
					// rows. Missing/null and empty timestamps are known invalid dates.
					if r["ts"] == nil || r["ts"] == "" {
						return closeCheck{}, true
					}
					text, ok := r["ts"].(string)
					at, e := time.Parse(time.RFC3339Nano, text)
					if !ok || e != nil || !closeGateTimestamp.MatchString(text) {
						// Date.parse accepts more formats and coerces non-strings.
						// Let Node distinguish an invalid date from a stale one.
						return closeCheck{}, false
					}
					if at.UnixMilli() > seconds*1000 {
						return closeCheck{}, true
					}
				}
			}
		}
	}
	reason := fmt.Sprintf("no green gates run; run: code-forge gates run --run %s --block %s", run, id)
	return closeCheck{reason, []gatecheck.Blocker{{Code: "no_green_gates", Detail: reason}}}, true
}

var closeShellSafe = regexp.MustCompile(`^[\w./:@=-]+$`)

func closeCommand(args []string) string {
	out := []string{}
	for _, arg := range args {
		if !closeShellSafe.MatchString(arg) {
			arg = "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
		}
		out = append(out, arg)
	}
	return strings.Join(out, " ")
}
func closeDash(v any) any {
	if v == nil {
		return "-"
	}
	return v
}
func closeBlockersJSON(problems []gatecheck.Blocker, id, run string, flags map[string]any, entry ledger.Object) []any {
	out := []any{}
	seen := map[string]bool{}
	for _, p := range problems {
		detailKey := p.Detail
		if p.Code == "unowned_change" {
			detailKey = ""
		}
		encoded, _ := ledger.Stringify([]string{p.Code, p.File, p.Finding, detailKey}, false)
		if seen[string(encoded)] {
			continue
		}
		seen[string(encoded)] = true
		fix := "no automatic fix: " + p.Code
		var argv []string
		switch p.Code {
		case "unowned_change":
			if p.File != "" {
				argv = []string{"code-forge", "block", "claim", id, p.File, "--run", run}
			}
		case "unreviewed", "unsigned":
			argv = []string{"code-forge", "review-file", "--wait-block", id, "--run", run}
		case "no_green_gates":
			argv = []string{"code-forge", "gates", "run", "--run", run, "--block", id}
		case "light_limits":
			fix += "; stop this attempt, then reopen with --kind code --attempt <n+1> and the original base, ownership and acceptance, or split the block within light limits"
		case "no_changes":
			argv = []string{"code-forge", "block", "close", id, "--run", run, "--no-require-reviews"}
		case "not_approved", "late_unruled", "review_cap", "autopilot_waiver_invalid":
			if p.File != "" {
				if p.Finding != "" {
					argv = []string{"code-forge", "block", "waive", id, p.Finding, "--run", run, "--file", p.File, "--reason", "<reason>"}
				} else {
					argv = []string{"code-forge", "block", "reopen", id, "--run", run, "--file", p.File, "--reason", "<reason>"}
				}
			}
		case "unproven":
			test := "<test-file>"
			if tests, ok := entry.Get("test_files").([]any); ok && len(tests) > 0 {
				test = blockString(tests[0])
			}
			argv = []string{"code-forge", "proof", "red-green", id, "--run", run, "--test", test}
		case "rule_break", "close-not-routine":
			argv = []string{"code-forge", "block", "close", id, "--run", run, "--decide", "--reason", "<reason>"}
			for _, flag := range []string{"report", "transcript"} {
				if value, ok := flags[flag].(string); ok {
					argv = append(argv, "--"+flag, value)
				}
			}
		case "agent-waivers-not-acked":
			argv = []string{"code-forge", "block", "ack-waivers", id, "--run", run}
		case "git-failed":
			argv = []string{"code-forge", "block", "rebase", id, "--run", run}
		case "ledger.tamper":
			argv = []string{"code-forge", "doctor"}
		case "coder_report_failed":
			report := "<coder-report>"
			if value, ok := flags["report"].(string); ok {
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
			fix += "; " + repair + "; rerun block ready"
		default:
			if strings.HasPrefix(p.Code, "worker.") {
				argv = []string{"code-forge", "run", "start", "--reattach", "--run", run}
			}
		}
		if argv != nil {
			fix = closeCommand(argv)
		}
		var file any
		if p.File != "" {
			file = p.File
		}
		detail := p.Detail
		if detail == "" {
			detail = p.Finding
		}
		if detail == "" {
			detail = p.Code
		}
		o := ledger.Object{{Key: "code", Value: p.Code}, {Key: "file", Value: file}, {Key: "detail", Value: detail}}
		if p.Finding != "" {
			o.Set("finding", p.Finding)
		}
		o.Set("fix", fix)
		out = append(out, o)
	}
	return out
}

func closeWriteDone(home, run, id string, record ledger.Object, result blockclose.Result, lines []any, rows []gatecheck.Row, key []byte, writers ...io.Writer) string {
	stderr := io.Writer(os.Stderr)
	if len(writers) > 0 {
		stderr = writers[0]
	}
	at := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	if id == "." || id == ".." || strings.ContainsAny(id, "/\\\x00") {
		fmt.Fprintf(stderr, "warning: done file not written: block id %s cannot name a done file\n", statusQuote(id))
		return ""
	}
	var spent, unknown, findings any
	values, err := closeDoneRows(home, run, id, record)
	if err == nil {
		all := []gatecheck.Row{}
		sessions := []ledger.Row{}
		for _, v := range values {
			if r, ok := v.(map[string]any); ok {
				all = append(all, r)
				if r["block"] == id && (r["event"] == "session" || r["event"] == "session.background") {
					sessions = append(sessions, r)
				}
			}
		}
		mapping, e := statusMap(record)
		if e == nil {
			mapping["blocks"] = map[string]any{}
			if facts, ok := runstatus.Compute(mapping, sessions, home); ok {
				spent, unknown = facts.Get("spent_usd"), facts.Get("unknown_usd_sessions")
			}
		}
		findings = blockclose.Findings(run, id, all, key)
	}
	workspace := blockString(record.Get("workspace"))
	tree := blockString(blockObject(blockObject(blockObject(record.Get("blocks")).Get(id)).Get("worktree")).Get("path"))
	if tree == "" {
		tree = workspace
	}
	var commit any
	if out, e := gitx.Run(tree, "rev-parse", "--verify", "HEAD"); e == nil && blockCommit.MatchString(strings.TrimSpace(out)) {
		commit = strings.TrimSpace(out)
	}
	event := result.Event
	if event == "" {
		if result.Status == "stopped" {
			event = "block.stop"
		} else {
			event = "block.close"
		}
	}
	var reason any
	if result.Reason != "" {
		reason = result.Reason
	}
	o := ledger.Object{{Key: "block", Value: id}, {Key: "run_id", Value: run}, {Key: "status", Value: result.Status}, {Key: "event", Value: event}, {Key: "reason", Value: reason}, {Key: "commit", Value: commit}, {Key: "spent_usd", Value: spent}, {Key: "unknown_usd_sessions", Value: unknown}, {Key: "findings", Value: findings}, {Key: "at", Value: at}}
	if result.OK && len(lines) > 0 {
		o.Set("reviewed", lines)
	}
	file := filepath.Join(home, ".code-forge/runs", run, "blocks", id+".done.json")
	dir := filepath.Dir(file)
	err = os.MkdirAll(dir, 0700)
	if err != nil {
		if st, e := os.Stat(dir); e == nil && !st.IsDir() {
			err = syscall.EEXIST
		}
	}
	if err == nil {
		tmp := filepath.Join(dir, "."+filepath.Base(file)+"."+strconv.Itoa(os.Getpid())+"."+strconv.FormatInt(time.Now().UnixMilli(), 10)+".tmp")
		defer os.Remove(tmp)
		data, _ := ledger.Stringify(o, false)
		err = os.WriteFile(tmp, append(data, '\n'), 0600)
		if err == nil {
			err = os.Rename(tmp, file)
			if err != nil {
				if st, e := os.Stat(file); e == nil && st.IsDir() {
					err = syscall.EISDIR
				}
			}
		}
	}
	if err != nil {
		code := "UNKNOWN"
		var errno syscall.Errno
		if errors.As(err, &errno) {
			code = blockErrno(errno)
		}
		fmt.Fprintln(stderr, "warning: done file not written:", code)
		return ""
	}
	return file
}

func closeWarnMovedBase(workspace string, entry ledger.Object, id, run string, writers ...io.Writer) {
	stderr := io.Writer(os.Stderr)
	if len(writers) > 0 {
		stderr = writers[0]
	}
	branch := blockString(entry.Get("base_branch"))
	if branch == "" {
		return
	}
	head, err := gitx.Run(workspace, "rev-parse", "--verify", "--end-of-options", "refs/heads/"+branch+"^{commit}")
	if err != nil || strings.TrimSpace(head) == entry.Get("base_sha") {
		return
	}
	// See warnMovedBase in Node: this is advisory and runs only after publication.
	tree := blockString(blockObject(entry.Get("worktree")).Get("path"))
	if tree == "" {
		tree = workspace
	}
	blockHead, e := gitx.Run(tree, "rev-parse", "--verify", "HEAD^{commit}")
	if e != nil {
		return
	}
	_, e = gitx.Run(workspace, "merge-base", "--is-ancestor", strings.TrimSpace(head), strings.TrimSpace(blockHead))
	var command *gitx.CommandError
	if !errors.As(e, &command) || command.Code != 1 {
		return
	}
	count, e := gitx.Run(workspace, "rev-list", "--count", blockString(entry.Get("base_sha"))+".."+strings.TrimSpace(head))
	if e != nil || !regexp.MustCompile(`^[0-9]+$`).MatchString(strings.TrimSpace(count)) {
		return
	}
	fmt.Fprintf(stderr, "WARN block %s: base moved: %s is %s commit(s) ahead of the block base; run: code-forge block rebase %s --run %s\n", id, branch, strings.TrimSpace(count), id, run)
}

// Done-file facts use the daemon snapshot, independently of close's gate rows.
// An unavailable daemon returns no answer, so the ledger remains the fallback.
func closeDoneRows(home, run, id string, record ledger.Object) ([]any, error) {
	cfg, _ := blockSnapshot(record)
	if daemonclient.Enabled(cfg) {
		if reply, err := daemonclient.Request(run, map[string]any{"method": "rows", "block": id}, 6500*time.Millisecond); err == nil {
			if rows, ok := reply["rows"].([]any); ok {
				return rows, nil
			}
		}
	}
	return ledger.ReadAllRows(filepath.Join(home, ".code-forge/ledger"), blockString(record.Get("project")))
}

// blockVerbUsage is Node's verbUsage for a block verb with no extra lines.
func blockVerbUsage(sub, form string) string {
	return "usage: code-forge block " + sub + " " + form + " [--json] [--help|-h]\n  --help, -h  Print this usage and exit without changing state.\n  --json      Print one JSON line on success instead of text.\n"
}

// closeAudit is Node's agent waiver, agent reopen and accepted hit report of a
// close. ok is false when --json would print a value whose key order Go lost.
type closeAudit struct {
	text []string
	json [][2]any
}

func closeScalar(v any) bool {
	switch v.(type) {
	case nil, string, float64, bool:
		return true
	}
	return false
}

func closeAuditOf(id string, rows []gatecheck.Row, key []byte, accepted []gatecheck.AcceptedHit, jsonFlag bool) (closeAudit, bool) {
	a := closeAudit{}
	field := func(o *ledger.Object, r map[string]any, k string) bool {
		v, has := r[k]
		if has {
			o.Set(k, v)
		}
		return !has || closeScalar(v)
	}
	waivers := []any{}
	for _, r := range gatecheck.AgentWaivers(rows, id, key) {
		o := ledger.Object{}
		for _, k := range []string{"file", "finding", "severity", "reason", "verdict"} {
			if !field(&o, r, k) && jsonFlag {
				return a, false
			}
		}
		waivers = append(waivers, o)
		_, hasFile := r["file"]
		_, hasFinding := r["finding"]
		_, hasReason := r["reason"]
		_, hasVerdict := r["verdict"]
		a.text = append(a.text, fmt.Sprintf("block %s: agent waiver %s %s reason: %s; verdict: %s\n", id, gatecheck.JSString(r["file"], hasFile), gatecheck.JSString(r["finding"], hasFinding), gatecheck.JSString(r["reason"], hasReason), gatecheck.JSString(r["verdict"], hasVerdict)))
	}
	reopens := []any{}
	for _, r := range gatecheck.AgentReopens(rows, id, key) {
		o := ledger.Object{}
		for _, k := range []string{"file", "reason"} {
			if !field(&o, r, k) && jsonFlag {
				return a, false
			}
		}
		verdict, hasVerdict := r["verdict"]
		said, saidOK := any(nil), false
		if m, isMap := verdict.(map[string]any); isMap {
			said, saidOK = m["verdict"]
			// Node writes {verdict, reason}; any other shape keeps its order with Node.
			ordered := ledger.Object{}
			for k, v := range m {
				if (k != "verdict" && k != "reason") || !closeScalar(v) {
					if jsonFlag {
						return a, false
					}
				}
			}
			for _, k := range []string{"verdict", "reason"} {
				if v, has := m[k]; has {
					ordered.Set(k, v)
				}
			}
			o.Set("verdict", ordered)
		} else if hasVerdict {
			if !closeScalar(verdict) && jsonFlag {
				return a, false
			}
			o.Set("verdict", verdict)
		}
		reopens = append(reopens, o)
		_, hasFile := r["file"]
		_, hasReason := r["reason"]
		a.text = append(a.text, fmt.Sprintf("agent reopen %s: %s (verdict: %s)\n", gatecheck.JSString(r["file"], hasFile), gatecheck.JSString(r["reason"], hasReason), gatecheck.JSString(said, saidOK)))
	}
	hits := []any{}
	for _, h := range accepted {
		hit := ledger.Object{{Key: "rule", Value: h.Rule}, {Key: "line", Value: h.Line}, {Key: "reason", Value: h.Reason}}
		by := "human"
		if h.By == "autopilot" {
			// B390: Node's JSON shape for human acceptances stays unchanged.
			by = h.By
			hit.Set("by", by)
		}
		hits = append(hits, hit)
		a.text = append(a.text, fmt.Sprintf("accepted rule_break %s transcript line %d: %s (by: %s)\n", h.Rule, h.Line, gatecheck.JSString(h.Reason, true), by))
	}
	for _, item := range [][2]any{{"agent_waivers", waivers}, {"agent_reopens", reopens}, {"accepted_hits", hits}} {
		if len(item[1].([]any)) > 0 {
			a.json = append(a.json, item)
		}
	}
	return a, true
}
