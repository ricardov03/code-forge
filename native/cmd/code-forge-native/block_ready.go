package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/autopilot"
	"github.com/ricardov03/code-forge/native/blockclose"
	"github.com/ricardov03/code-forge/native/blockend"
	"github.com/ricardov03/code-forge/native/blockstate"
	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

func init() { blockSubs["ready"] = runBlockReady }

// readyNow is the clock of the routine-close preview (a test seam).
var readyNow = time.Now

func runBlockReady(args []string) int {
	// B392: --preflight lists the close blockers a gate cannot fix, before any gate.
	if i := slices.Index(args, "--preflight"); i >= 0 {
		return runBlockPreflight(append(slices.Clone(args[:i]), args[i+1:]...))
	}
	p, code := prepareBlockPreview("ready", args)
	if p == nil {
		return code
	}
	entry := blockObject(blockObject(p.record.Get("blocks")).Get(p.id))
	if entry == nil {
		return p.refusal("unknown_block", fmt.Sprintf("block %s is not in run %s", p.id, p.run))
	}
	if entry.Get("status") != "open" {
		return p.refusal("no-block", fmt.Sprintf("block %s is not open in run %s", p.id, p.run))
	}
	cfg, ok := blockSnapshot(p.record)
	if !ok {
		return p.fallback()
	}
	// Preview does not run lazy expiry, so active grants alone need no effects.
	// B449: under close.require_human an active grant previews the routine-close
	// policy, as Node's routinePreview does.
	external := blockMap(blockMap(cfg["review"])["external"])
	externalReview := external["required"] == true && blockstate.Trim(blockString(external["name"])) != ""
	routine := false
	if blockMap(cfg["close"])["require_human"] == true && p.record.Get("autopilot") != nil {
		grant, ok := p.record.Get("autopilot").(ledger.Object)
		if !ok {
			return p.fallback()
		}
		s, err := autopilot.GrantState(grant, readyNow())
		if err != nil {
			return p.fallback()
		}
		routine = s == "active"
	}
	workspace := blockString(p.record.Get("workspace"))
	mainTop, err := gitx.RepoTop(workspace)
	if err != nil {
		return p.fallback()
	}
	top, ok := p.contentRoot(entry)
	if !ok {
		wt := blockObject(entry.Get("worktree"))
		where, branch := blockString(wt.Get("path")), blockString(wt.Get("branch"))
		if !filepath.IsAbs(where) || branch == "" || regexp.MustCompile(`[\x00-\x1f\x7f]`).MatchString(where+branch) || !blockCommit.MatchString(blockString(wt.Get("base_sha"))) {
			return p.fallback()
		}
		if _, e := os.Stat(where); os.IsNotExist(e) {
			return p.refusal("worktree-missing", fmt.Sprintf("block %s: its worktree %s is gone (removed or moved), so its work cannot be measured — restore it on branch %s (git worktree prune, then git worktree add %s %s) and close again; nothing was recorded", p.id, where, branch, where, branch))
		}
		return p.fallback()
	}
	base := blockString(entry.Get("base_sha"))
	inTree := entry.Get("worktree") != nil
	if inTree {
		base = blockString(blockObject(entry.Get("worktree")).Get("base_sha"))
	}
	rows, key, code := p.rows()
	if code >= 0 {
		return code
	}
	if !gatecheck.Supported(rows) {
		return p.fallback()
	}
	transcript, _, err := closeTranscript(p.record, entry, p.id, blockString(p.flags["transcript"]))
	if err != nil {
		return p.fallback()
	}
	hits, supported := gatecheck.Transcript(transcript)
	if !supported {
		return p.fallback()
	}
	// B493: Node's B64b hook check is the first close check.
	hookCheck, ok := closeHookCheck(cfg, transcript)
	if !ok {
		return p.fallback()
	}
	highPaths, ok := closeHighPaths(workspace, base)
	if !ok {
		return p.fallback()
	}
	prefix := gatecheck.Prefix(mainTop, workspace)
	changed, err := gitx.ChangedFiles(top, base)
	if err != nil {
		return p.fallback()
	}
	files, reviewed, err := gatecheck.FileSet(top, base, prefix, p.id, entry, rows, changed)
	if err != nil {
		return p.fallback()
	}
	for _, f := range files {
		if closeExcluded(cfg, f.File) {
			return p.fallback()
		}
	}
	var live *float64
	if raw, has := p.flags["worker-pid"].(string); has {
		n, e := strconv.ParseFloat(raw, 64)
		if e != nil || !regexp.MustCompile(`^[1-9][0-9]*$`).MatchString(raw) || n > 9007199254740991 {
			return p.usage("--worker-pid must be a positive integer")
		}
		live = &n
	}
	// closeBlock rereads the record immediately before checking pins and orphans.
	current, err := state.ReadRun(p.home, p.run)
	if err != nil {
		return p.fail(err)
	}
	if blockObject(blockObject(current.Get("blocks")).Get(p.id)).Get("status") != "open" {
		return p.refusal("no-block", fmt.Sprintf("block %s is not open in run %s", p.id, p.run))
	}
	blockers := []gatecheck.Blocker{}
	event, why := blockclose.Pin(current, live, statusStart)
	if !closeQueuePins(p.home, p.run, p.id, current, rows, &event, &why, mainTop) {
		return p.fallback()
	}
	if event != "" {
		blockers = append(blockers, gatecheck.Blocker{Code: event, Detail: why})
	}
	if why := blockclose.Tamper(p.run, p.id, rows, key); why != "" {
		blockers = append(blockers, gatecheck.Blocker{Code: "ledger.tamper", Detail: why})
	}
	reportText := ""
	var reportErr error
	if report, has := p.flags["report"].(string); has {
		data, e := os.ReadFile(report)
		reportErr = e
		reportText = string(data)
	}
	blockers = append(blockers, hookCheck.Blockers...)
	if blockMap(cfg["context"])["require_coder_tests"] == true {
		_, why, supported := closeCoderTests(p.flags["report"], p.id, []byte(reportText), reportErr)
		if !supported {
			return p.fallback()
		}
		if why != "" {
			blockers = append(blockers, gatecheck.Blocker{Code: "coder_report_failed", Detail: why})
		}
	}
	acked, ackKnown := gatecheck.AgentWaiversAcked(blockMap(blockMap(cfg["review"])["agent_waivers"])["require_ack"] == true, p.run, p.id, rows, key)
	if !ackKnown {
		return p.fallback()
	}
	if !acked {
		blockers = append(blockers, gatecheck.Blocker{Code: "agent-waivers-not-acked", Detail: "agent-waivers-not-acked"})
	}
	if blockMap(cfg["gates"])["test"] != nil {
		check, supported := closeTestGate(top, base, p.run, p.id, entry, rows)
		if !supported {
			return p.fallback()
		}
		blockers = append(blockers, check.Blockers...)
	}
	if entry.Get("kind") == "light" {
		limit, ok := lightLimits(top, base, prefix, entry, changed, cfg)
		if !ok {
			return p.fallback()
		}
		blockers = append(blockers, limit...)
	}
	collectOnly := len(blockers) > 0
	unowned, scopeWarnings, scopeChecked := gatecheck.Scope(top, prefix, p.id, p.run, blockObject(p.record.Get("blocks")), changed, rows, key, inTree)
	noChanges := len(files) == 0 && !slices.ContainsFunc(gatecheck.ReviewedWork(top, rows, p.id), func(f string) bool { return gatecheck.Owns(prefix, entry, f) })
	gateInput := gatecheck.Input{Run: p.run, Block: p.id, Files: files, Rows: rows, Key: key, HighPaths: highPaths, Unowned: unowned, NoChanges: noChanges, Hits: hits, Transcript: transcript, ResultFiles: gatecheck.LoadResultFiles(mainTop, p.run, p.id, rows, key)}
	if externalReview {
		gateInput.Files, gateInput.ProofFiles = []gatecheck.File{}, files
	}
	gate := gatecheck.Evaluate(gateInput)
	if gate.Unknown {
		return p.fallback()
	}
	blockers = append(blockers, gate.Blockers...)
	if _, has := p.flags["report"].(string); has && !externalReview {
		if reportErr != nil {
			blockers = append(blockers, gatecheck.Blocker{Code: "coder_report_failed", Detail: "coder report FAILED: --report cannot be read"})
		} else {
			// Report credit can inspect tickets but never enqueues or writes them.
			lines, _ := closeReviewedLines(p.run, p.id, top, filepath.Join(mainTop, ".code-forge/queue"), entry, files, gate.Waived, inTree, len(gate.Blockers) == 0)
			check, _ := closeReport(reportText, p.run, p.id, filepath.Join(mainTop, ".code-forge/queue"), files, lines, rows, key)
			blockers = append(blockers, check.Blockers...)
		}
	}
	warnReviewed := len(reviewed) > 0 && !slices.ContainsFunc(changed, func(f string) bool { return gatecheck.Owns(prefix, entry, f) })
	if routine {
		// Node loads the gate again for this check; with no blocker before it
		// that second load repeats the gate's warnings.
		if len(blockers) == 0 && (warnReviewed || !scopeChecked || len(scopeWarnings) > 0) {
			return p.fallback()
		}
		if problem := closeRoutineProblem(closeExternalReviewer(cfg), p.run, p.id, rows, key, gateInput.Files, gate, hits); problem != "" {
			blockers = append(blockers, gatecheck.Blocker{Code: "close-not-routine", Detail: problem})
		}
	}
	orphans, ok := current.Get("orphans").([]any)
	if !ok {
		return p.fallback()
	}
	for _, v := range orphans {
		file, ok := v.(string)
		if !ok {
			return p.fallback()
		}
		blockers = append(blockers, gatecheck.Blocker{Code: "unowned_change", File: file, Detail: "orphan in run registry: " + file})
	}
	// Preserve Node's error-log capture for unusual warning diagnostics.
	encoded, _ := ledger.Stringify(closeBlockersJSON(blockers, p.id, p.run, p.flags, entry), false)
	if !closeTextSafe(p.id + workspace + string(encoded) + strings.Join(scopeWarnings, "")) {
		return p.fallback()
	}
	if reviewWaitLogging() && len(blockers) > 0 && !collectOnly && (warnReviewed || !scopeChecked || len(scopeWarnings) > 0) {
		return p.fallback()
	}
	// All fallback checks precede breadcrumbs and advisory stderr output.
	before := gatesBreadcrumbName("block")
	if !collectOnly {
		if len(reviewed) > 0 && !slices.ContainsFunc(changed, func(f string) bool { return gatecheck.Owns(prefix, entry, f) }) {
			where, likely := "the workspace", "committed before block open or done in another worktree"
			if inTree {
				where = "the worktree " + blockString(blockObject(entry.Get("worktree")).Get("path"))
				likely = "done outside the block's worktree (in the main checkout or another tree)"
			}
			fmt.Fprintf(os.Stderr, "WARN block %s: no owned file differs from the block base in %s, but %d owned file(s) have review rows — the work was likely %s; they are checked at their content in %s\n", p.id, where, len(reviewed), likely, where)
		}
		if !scopeChecked {
			fmt.Fprintf(os.Stderr, "WARN block %s: opened before code-forge recorded the tree at block open; unowned changes were not checked\n", p.id)
		}
		for _, w := range scopeWarnings {
			fmt.Fprint(os.Stderr, w)
		}
	}
	formatted := closeBlockersJSON(blockers, p.id, p.run, p.flags, entry)
	for _, value := range formatted {
		o := value.(ledger.Object)
		if o.Get("code") == "light_limits" {
			o.Set("fix", "no automatic fix: light_limits; stop this attempt, then reopen with --kind code --attempt <n+1> and the original base, ownership and acceptance, or split the block within light limits")
		}
	}
	ready := len(blockers) == 0
	if p.flags["json"] == true {
		out := ledger.Object{{Key: "block", Value: p.id}, {Key: "ready", Value: ready}, {Key: "blockers", Value: formatted}}
		data, _ := ledger.Stringify(out, false)
		fmt.Println(string(data))
	} else {
		for _, b := range formatted {
			o := b.(ledger.Object)
			fmt.Printf("%s %s %s\n  fix: %s\n", o.Get("code"), closeDash(o.Get("file")), o.Get("detail"), o.Get("fix"))
		}
		if ready {
			fmt.Printf("block %s ready\n", p.id)
		} else {
			fmt.Printf("block %s not ready: %d blockers\n", p.id, len(formatted))
		}
	}
	if ready {
		return 0
	}
	nativeVerbFailure("block", "", append([]string{"ready"}, args...), 1, nil, before)
	return 1
}

// closeHookCheck is Node's B64b extra check: with hooks.on_block_end set, the
// transcript lines that run the hook's command are rule_break blockers; the
// reason names the first. ok false: text Go cannot scan like Node.
func closeHookCheck(cfg map[string]any, transcript string) (closeCheck, bool) {
	hook := blockend.FromConfig(cfg)
	if hook == nil {
		return closeCheck{}, true
	}
	hits, ok := gatecheck.HookTranscript(hook.Argv, transcript)
	if !ok || len(hits) == 0 {
		return closeCheck{}, ok
	}
	return closeCheck{"rule_break " + hits[0].Detail, hits}, true
}
