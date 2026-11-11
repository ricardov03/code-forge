package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ricardov03/code-forge/native/blockclose"
	"github.com/ricardov03/code-forge/native/blockstate"
	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B392: the preflight lists, in seconds and without any gate result, the close
// blockers a gate cannot fix: a stale base, open review findings on the current
// content, rule breaks (ledger rows and the transcript scan) and, with a coder
// report, missing review ticket ids. A file that is not reviewed yet is not a
// preflight blocker: review runs in parallel with the gate.

// preflightCodes are the gatecheck codes the preflight keeps from the review gate.
var preflightCodes = []string{"not_approved", "late_unruled", "autopilot_waiver_invalid", "rule_break"}

type preflightInput struct {
	home, run, id string
	// stale includes the stale_base check; gates run leaves it to its B363
	// handling when --auto-rebase or --allow-stale-base is given.
	stale      bool
	transcript string
	report     *string
}

type preflightResult struct {
	record, entry ledger.Object
	blockers      []gatecheck.Blocker
	// skipped names each check the preflight could not decide, and why.
	skipped []string
}

// blockPreflight reads only: it never writes a row, a ticket or a file. The
// error is a state.Error for an unreadable run or a block that is not open.
func blockPreflight(in preflightInput) (preflightResult, error) {
	out := preflightResult{blockers: []gatecheck.Blocker{}, skipped: []string{}}
	record, err := state.ReadRun(in.home, in.run)
	if err != nil {
		return out, err
	}
	out.record = record
	entry := blockObject(blockObject(record.Get("blocks")).Get(in.id))
	if entry == nil {
		return out, &state.Error{Code: "unknown_block", Message: fmt.Sprintf("block %s is not in run %s", in.id, in.run)}
	}
	if entry.Get("status") != "open" {
		return out, &state.Error{Code: "no-block", Message: fmt.Sprintf("block %s is not open in run %s", in.id, in.run)}
	}
	out.entry = entry
	workspace := blockString(record.Get("workspace"))
	skip := func(check, why string) { out.skipped = append(out.skipped, check+": "+why) }
	if in.stale {
		if s := gates.StaleBase(workspace, entry); s != nil {
			out.blockers = append(out.blockers, gatecheck.Blocker{Code: "stale_base", Detail: fmt.Sprintf("%s moved %s → %s since the block's base; a gate on this base tests an old %s", s.Branch, gatesShort(s.Recorded), s.Tip, s.Branch)})
		}
	}
	mainTop, err := gitx.RepoTop(workspace)
	if err != nil {
		skip("review", "the workspace is not a git repository")
		return out, nil
	}
	p := &blockPreview{record: record}
	top, ok := p.contentRoot(entry)
	if !ok {
		skip("review", "the block tree cannot be read")
		return out, nil
	}
	inTree := entry.Get("worktree") != nil
	base := blockString(entry.Get("base_sha"))
	if inTree {
		base = blockString(blockObject(entry.Get("worktree")).Get("base_sha"))
	}
	rows, key, why := preflightRows(in.home, in.run, blockString(record.Get("project")))
	if why != "" {
		skip("review", why)
		return out, nil
	}
	if !gatecheck.Supported(rows) {
		skip("review", "a ledger row needs the Node close check")
		return out, nil
	}
	transcript, _, err := closeTranscript(record, entry, in.id, in.transcript)
	var hits []gatecheck.Blocker
	if err != nil {
		skip("transcript", "cannot be read: "+err.Error())
		transcript = ""
	} else if found, supported := gatecheck.Transcript(transcript); !supported {
		skip("transcript", "its text needs the Node rule scan; block ready (without --preflight) runs it at close")
		transcript = ""
	} else {
		hits = found
	}
	changed, err := gitx.ChangedFiles(top, base)
	if err != nil {
		skip("review", "the changed files cannot be listed")
		return out, nil
	}
	prefix := gatecheck.Prefix(mainTop, workspace)
	files, _, err := gatecheck.FileSet(top, base, prefix, in.id, entry, rows, changed)
	if err != nil {
		skip("review", "the owned files cannot be hashed")
		return out, nil
	}
	highPaths, _ := closeHighPaths(workspace, base)
	cfg, _ := blockSnapshot(record)
	external := blockMap(blockMap(cfg["review"])["external"])
	externalReview := external["required"] == true && blockstate.Trim(blockString(external["name"])) != ""
	input := gatecheck.Input{Run: in.run, Block: in.id, Files: files, Rows: rows, Key: key, HighPaths: highPaths, Hits: hits, Transcript: transcript, ResultFiles: gatecheck.LoadResultFiles(mainTop, in.run, in.id, rows, key)}
	if externalReview {
		input.Files, input.ProofFiles = []gatecheck.File{}, files
	}
	gate := gatecheck.Evaluate(input)
	if gate.Unknown {
		skip("review", "a review row needs the Node close check")
		return out, nil
	}
	for _, b := range gate.Blockers {
		if slices.Contains(preflightCodes, b.Code) {
			out.blockers = append(out.blockers, b)
		}
	}
	if in.report == nil || externalReview {
		return out, nil
	}
	data, readErr := os.ReadFile(*in.report)
	if blockMap(cfg["context"])["require_coder_tests"] == true {
		if _, why, supported := closeCoderTests(*in.report, in.id, data, readErr); !supported {
			skip("coder report", "its TESTS line needs the Node check")
		} else if why != "" {
			out.blockers = append(out.blockers, gatecheck.Blocker{Code: "coder_report_failed", Detail: why})
		}
	}
	if readErr != nil {
		out.blockers = append(out.blockers, gatecheck.Blocker{Code: "coder_report_failed", Detail: "coder report FAILED: --report cannot be read"})
		return out, nil
	}
	lines, _ := closeReviewedLines(in.run, in.id, top, filepath.Join(mainTop, ".code-forge/queue"), entry, files, gate.Waived, inTree, len(gate.Blockers) == 0)
	check, _ := closeReport(string(data), in.run, in.id, files, lines, rows, key)
	out.blockers = append(out.blockers, check.Blockers...)
	return out, nil
}

// preflightRows reads this run's ledger rows and its signer key, or says why not.
func preflightRows(home, run, project string) ([]gatecheck.Row, []byte, string) {
	values, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge/ledger"), project)
	if err != nil {
		return nil, nil, "the ledger cannot be read"
	}
	rows := []gatecheck.Row{}
	for _, v := range values {
		r, ok := v.(map[string]any)
		if !ok {
			return nil, nil, "the ledger holds a row that is not an object"
		}
		if r["run"] == run {
			rows = append(rows, r)
		}
	}
	key, err := blockclose.ReadKey(home, run)
	if err != nil {
		return nil, nil, "the run signer key cannot be read"
	}
	return rows, key, ""
}

// preflightFormatted is closeBlockersJSON with the stale_base fix line.
func preflightFormatted(blockers []gatecheck.Blocker, id, run string, flags map[string]any, entry ledger.Object) []any {
	formatted := closeBlockersJSON(blockers, id, run, flags, entry)
	for _, value := range formatted {
		o := value.(ledger.Object)
		if o.Get("code") == "stale_base" {
			o.Set("fix", closeCommand([]string{"code-forge", "block", "rebase", id, "--run", run, "--accept-landed"}))
		}
	}
	return formatted
}

// runBlockPreflight is block ready <id> --run <r> --preflight (args without
// --preflight). Exit 0 when clean, 1 with the blocker list, 2 on usage.
func runBlockPreflight(args []string) int {
	p, code := prepareBlockPreview("ready", args)
	if p == nil {
		return code
	}
	in := preflightInput{home: p.home, run: p.run, id: p.id, stale: true, transcript: blockString(p.flags["transcript"])}
	if report, has := p.flags["report"].(string); has {
		in.report = &report
	}
	result, err := blockPreflight(in)
	if err != nil {
		return p.fail(err)
	}
	formatted := preflightFormatted(result.blockers, p.id, p.run, p.flags, result.entry)
	ready := len(formatted) == 0
	if p.flags["json"] == true {
		skipped := make([]any, len(result.skipped))
		for i, s := range result.skipped {
			skipped[i] = s
		}
		data, _ := ledger.Stringify(ledger.Object{{Key: "block", Value: p.id}, {Key: "preflight", Value: true}, {Key: "ready", Value: ready}, {Key: "blockers", Value: formatted}, {Key: "skipped", Value: skipped}}, false)
		fmt.Println(string(data))
	} else {
		for _, b := range formatted {
			o := b.(ledger.Object)
			fmt.Printf("%s %s %s\n  fix: %s\n", o.Get("code"), closeDash(o.Get("file")), o.Get("detail"), o.Get("fix"))
		}
		for _, s := range result.skipped {
			fmt.Printf("skipped %s\n", s)
		}
		if ready {
			fmt.Printf("block %s preflight clean\n", p.id)
		} else {
			fmt.Printf("block %s preflight: %d blockers\n", p.id, len(formatted))
		}
	}
	if ready {
		return 0
	}
	return 1
}

// gatesPreflight (B392) runs before any gate command of gates run --run
// --block: each blocker is one stderr line and no gate runs. A run or block
// the preflight cannot read is left to the gate runner to diagnose.
func gatesPreflight(o gatesOptions) (code int, stop bool) {
	stale := !o.autoRebase && !o.allowStaleBase
	result, err := blockPreflight(preflightInput{home: os.Getenv("HOME"), run: o.run, id: o.block, stale: stale})
	if err != nil || len(result.blockers) == 0 {
		return 0, false
	}
	formatted := preflightFormatted(result.blockers, o.block, o.run, nil, result.entry)
	for i, value := range formatted {
		b := value.(ledger.Object)
		fix := blockString(b.Get("fix"))
		if b.Get("code") == "stale_base" {
			s := gates.StaleBase(blockString(result.record.Get("workspace")), result.entry)
			if s != nil {
				_, moved := gatesStaleText(o.run, o.block, s)
				gatesErrf("gates run: block %s of run %s has a stale base (%s); no gate was run; fix: %s\n", o.block, o.run, moved, fix)
				continue
			}
		}
		where := ""
		if file := blockString(b.Get("file")); file != "" {
			where = " " + file
		}
		finding := ""
		if f := blockString(b.Get("finding")); f != "" {
			finding = " " + f
		}
		detail := strings.ReplaceAll(blockString(b.Get("detail")), "\n", " ")
		gatesErrf("gates run: block %s of run %s preflight blocker %d/%d %s%s%s: %s; no gate was run; fix: %s\n", o.block, o.run, i+1, len(formatted), b.Get("code"), where, finding, detail, fix)
	}
	return 1, true
}
