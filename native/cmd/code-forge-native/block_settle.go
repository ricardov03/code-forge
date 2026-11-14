package main

import (
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
	"github.com/ricardov03/code-forge/native/proof"
	"github.com/ricardov03/code-forge/native/state"
)

// B626: block settle <id> --run <r> [--report <file>] [--transcript <file>]
// [--json] clears every close blocker an orchestrator may clear, and then says
// what (if anything) only the owner can do. Each round re-derives the close
// dry-run blocker list (B622) and runs the orchestrator fixes in one fixed
// order: rebase --accept-landed for a stale base, claim the changed files
// under a folder the block owns, run the gates, wait for the reviews. It never
// runs a human-only verb (waive, reopen, close --decide, human-pass, ...).
// It stops when no orchestrator blocker is left, when a fix fails, or when a
// round can make no progress (at most settleMaxRounds rounds), and writes one
// signed block.settle row. Exit 0: no blocker left; 3: only owner blockers
// left; 1: a fix failed or orchestrator blockers stay; 2: usage.

func init() { blockSubs["settle"] = runBlockSettle }

const settleUsageForm = "<id> --run <r> [--report <file>] [--transcript <file>]"

// settleMaxRounds bounds the loop: each round is one dry run and its fixes.
const settleMaxRounds = 4

// The settle fixes, in the order a round runs them.
const (
	settleFixRebase  = "rebase"
	settleFixClaim   = "claim"
	settleFixGates   = "gates"
	settleFixReviews = "reviews"
)

// settleOutcome values on the block.settle row.
const (
	settleSettled    = "settled"
	settleOwner      = "owner"
	settleFailed     = "failed"
	settleNoProgress = "no_progress"
)

type settleOptions struct {
	id, run, report, transcript string
	json                        bool
}

func settleParse(args []string) (settleOptions, bool, string) {
	o := settleOptions{}
	seen := map[string]bool{}
	pos := []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--help", "-h":
			return o, true, ""
		case "--json":
			if seen[arg] {
				return o, false, "--json given more than once"
			}
			seen[arg] = true
			o.json = true
		case "--run", "--report", "--transcript":
			if seen[arg] {
				return o, false, arg + " given more than once"
			}
			seen[arg] = true
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") || args[i+1] == "" {
				return o, false, arg + " requires a value"
			}
			i++
			switch arg {
			case "--run":
				o.run = args[i]
			case "--report":
				o.report = args[i]
			default:
				o.transcript = args[i]
			}
		default:
			if strings.HasPrefix(arg, "-") {
				return o, false, "unknown flag " + arg
			}
			pos = append(pos, arg)
		}
	}
	if len(pos) != 1 || o.run == "" {
		return o, false, "one block id and --run are required"
	}
	o.id = pos[0]
	return o, false, ""
}

func runBlockSettle(args []string) int { return blockSettle(args, os.Stdout, os.Stderr) }

// settleAction is one fix settle ran, as the block.settle row records it.
type settleAction struct {
	fix, code, command, result, detail string
	files                              []string
}

func (a settleAction) object() ledger.Object {
	o := ledger.Object{{Key: "fix", Value: a.fix}, {Key: "code", Value: a.code}, {Key: "command", Value: a.command}}
	if len(a.files) > 0 {
		files := []any{}
		for _, f := range a.files {
			files = append(files, f)
		}
		o.Set("files", files)
	}
	o.Set("result", a.result)
	if a.detail != "" {
		o.Set("detail", a.detail)
	}
	return o
}

// settleRun is one settle call: what it saw, ran and left.
type settleRun struct {
	o                   settleOptions
	home                string
	progress            io.Writer
	actions             []settleAction
	rounds              int
	remaining           []any
	outcome, failedFix  string
	stopDetail          string
	previous            string
	ownerOnly, orchestr []ledger.Object
}

func blockSettle(args []string, stdout, stderr io.Writer) int {
	o, help, message := settleParse(args)
	if help {
		fmt.Fprint(stdout, blockVerbUsage("settle", settleUsageForm))
		return 0
	}
	if message != "" {
		fmt.Fprintln(stderr, "block settle:", message)
		return 2
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(stderr, "block settle: the home folder cannot be found; set HOME")
		return 1
	}
	s := &settleRun{o: o, home: home, progress: stdout}
	if o.json {
		s.progress = stderr
	}
	if code := s.loop(stderr); code >= 0 {
		return code
	}
	if err := s.sign(); err != nil {
		fmt.Fprintln(stderr, "block settle:", err)
		return 1
	}
	return s.print(stdout)
}

// dryArgs are the close dry-run arguments of this settle.
func (s *settleRun) dryArgs() []string {
	args := []string{s.o.id, "--run", s.o.run}
	if s.o.report != "" {
		args = append(args, "--report", s.o.report)
	}
	if s.o.transcript != "" {
		args = append(args, "--transcript", s.o.transcript)
	}
	return args
}

// loop runs the rounds. A code >= 0 ends settle at once (usage, no row).
func (s *settleRun) loop(stderr io.Writer) int {
	for {
		_, rows, _, code := closeDryRows(s.dryArgs(), stderr)
		if code == 2 {
			return 2
		}
		if code >= 0 {
			s.outcome, s.stopDetail = settleFailed, "the close dry run could not list the blockers"
			s.failedFix = "dry-run"
			return -1
		}
		s.remaining = rows
		s.ownerOnly, s.orchestr = nil, nil
		ownerFiles := map[string]bool{}
		for _, v := range rows {
			row := v.(ledger.Object)
			if row.Get("clears_by") == clearsByOwner {
				s.ownerOnly = append(s.ownerOnly, row)
				if file, ok := row.Get("file").(string); ok {
					ownerFiles[file] = true
				}
			}
		}
		for _, v := range rows {
			row := v.(ledger.Object)
			if row.Get("clears_by") == clearsByOwner {
				continue
			}
			// A missing approval of a file whose findings only the owner can
			// waive (or reopen) clears with the owner's verb: no new review
			// can approve the content the owner must rule on.
			code := blockString(row.Get("code"))
			if file, ok := row.Get("file").(string); ok && ownerFiles[file] && (code == "unreviewed" || code == "unsigned") {
				continue
			}
			s.orchestr = append(s.orchestr, row)
		}
		if len(s.orchestr) == 0 {
			s.outcome = settleSettled
			if len(s.ownerOnly) > 0 {
				s.outcome = settleOwner
			}
			return -1
		}
		signature := settleSignature(s.orchestr)
		if s.rounds >= settleMaxRounds || signature == s.previous {
			s.outcome, s.stopDetail = settleNoProgress, "the last round's fixes left the same orchestrator blockers"
			if s.rounds >= settleMaxRounds {
				s.outcome, s.stopDetail = settleNoProgress, fmt.Sprintf("orchestrator blockers remain after %d rounds", settleMaxRounds)
			}
			return -1
		}
		s.previous = signature
		plan := s.plan()
		if len(plan) == 0 {
			codes := []string{}
			for _, row := range s.orchestr {
				codes = append(codes, blockString(row.Get("code")))
			}
			s.outcome, s.stopDetail = settleNoProgress, "settle has no fix for "+strings.Join(settleUnique(codes), ", ")+"; run each fix line yourself"
			return -1
		}
		s.rounds++
		for _, fix := range []string{settleFixRebase, settleFixClaim, settleFixGates, settleFixReviews} {
			step, ok := plan[fix]
			if !ok {
				continue
			}
			action := s.runFix(step)
			s.actions = append(s.actions, action)
			if action.result != "ok" {
				s.outcome, s.failedFix, s.stopDetail = settleFailed, fix, action.detail
				// The blocker list after the failed fix is the one left.
				if _, rows, _, code := closeDryRows(s.dryArgs(), io.Discard); code < 0 {
					s.remaining = rows
				}
				return -1
			}
			if fix == settleFixClaim {
				// The claimed files need their reviews in this round too.
				if _, ok := plan[settleFixReviews]; !ok {
					plan[settleFixReviews] = settleStep{fix: settleFixReviews, code: "unreviewed"}
				}
			}
		}
	}
}

// settleStep is one planned fix of a round.
type settleStep struct {
	fix, code string
	files     []string
}

// plan picks this round's fixes from the orchestrator blockers.
func (s *settleRun) plan() map[string]settleStep {
	plan := map[string]settleStep{}
	claim := []string{}
	for _, row := range s.orchestr {
		code := blockString(row.Get("code"))
		switch code {
		case "stale_base":
			plan[settleFixRebase] = settleStep{fix: settleFixRebase, code: code}
		case "no_green_gates":
			plan[settleFixGates] = settleStep{fix: settleFixGates, code: code}
		case "unreviewed", "unsigned":
			if _, ok := plan[settleFixReviews]; !ok {
				plan[settleFixReviews] = settleStep{fix: settleFixReviews, code: code}
			}
		case "unowned_change":
			if file, ok := row.Get("file").(string); ok && file != "" {
				claim = append(claim, file)
			}
		}
	}
	if len(claim) > 0 {
		if files := s.splitFiles(claim); len(files) > 0 {
			plan[settleFixClaim] = settleStep{fix: settleFixClaim, code: "unowned_change", files: files}
		}
	}
	return plan
}

// splitFiles keeps the unowned files under a folder the block owns: the
// folder of an exact owned file, or an owned directory. The repository root
// is no such folder, and a glob entry names none.
func (s *settleRun) splitFiles(files []string) []string {
	record, err := state.ReadRun(s.home, s.o.run)
	if err != nil {
		return nil
	}
	entry := blockObject(blockObject(record.Get("blocks")).Get(s.o.id))
	ownedFiles, _ := entry.Get("owned_files").([]any)
	kinds := blockObject(entry.Get("owned_kinds"))
	folders := []string{}
	for _, v := range ownedFiles {
		name, ok := v.(string)
		if !ok || name == "" || !owned.IsExactEntry(name) {
			continue
		}
		folder := path.Dir(strings.TrimSuffix(name, "/"))
		if kinds.Get(name) == "dir" || strings.HasSuffix(name, "/") {
			folder = path.Clean(name)
		}
		if folder != "." && folder != "/" {
			folders = append(folders, folder)
		}
	}
	out := []string{}
	for _, file := range settleUnique(files) {
		for _, folder := range folders {
			if strings.HasPrefix(file, folder+"/") {
				out = append(out, file)
				break
			}
		}
	}
	return out
}

// settleTrees is the block's main checkout and worktree top.
func (s *settleRun) settleTrees() (record, entry ledger.Object, main, top, workspace string, err error) {
	record, err = state.ReadRun(s.home, s.o.run)
	if err != nil {
		return nil, nil, "", "", "", err
	}
	entry = blockObject(blockObject(record.Get("blocks")).Get(s.o.id))
	if entry == nil {
		return nil, nil, "", "", "", fmt.Errorf("block %s is not in run %s", s.o.id, s.o.run)
	}
	workspace = blockString(record.Get("workspace"))
	main, err = gitx.RepoTop(workspace)
	if err != nil {
		return nil, nil, "", "", "", fmt.Errorf("the run workspace is not a git checkout")
	}
	top = main
	tree, err := proof.BlockTree(workspace, entry)
	if err != nil {
		return nil, nil, "", "", "", err
	}
	if tree != nil {
		top = tree.Path
		workspace = tree.Workspace
	}
	return record, entry, main, top, workspace, nil
}

// runFix runs one fix and reports how it went.
func (s *settleRun) runFix(step settleStep) settleAction {
	a := settleAction{fix: step.fix, code: step.code, files: step.files}
	failed := func(detail string) settleAction {
		a.result, a.detail = "failed", strings.TrimSpace(detail)
		if a.detail == "" {
			a.detail = step.fix + " failed"
		}
		fmt.Fprintf(s.progress, "settle %s: %s failed: %s\n", s.o.id, step.fix, a.detail)
		return a
	}
	record, _, main, top, workspace, err := s.settleTrees()
	switch step.fix {
	case settleFixRebase:
		args := []string{s.o.id, "--run", s.o.run, "--accept-landed"}
		a.command = closeCommand(append([]string{"code-forge", "block", "rebase"}, args...))
		var out, errs strings.Builder
		code, reason := rebaseStep(args, &out, &errs)
		if reason != 0 {
			return failed("the Go engine cannot run this rebase (" + handoverReasons[reason] + "); run " + a.command)
		}
		if code != 0 {
			return failed(landFirst(errs.String(), out.String(), fmt.Sprintf("block rebase exits %d", code)))
		}
	case settleFixClaim:
		a.command = closeCommand(append(append([]string{"code-forge", "block", "claim", s.o.id}, step.files...), "--run", s.o.run))
		if err != nil {
			return failed(err.Error())
		}
		if err := closeAutoClaim(s.home, s.o.run, s.o.id, top, record, step.files, io.Discard, true); err != nil {
			return failed(err.Error())
		}
		if err := closeAutoClaim(s.home, s.o.run, s.o.id, top, record, step.files, s.progress, false); err != nil {
			return failed(err.Error())
		}
	case settleFixGates:
		args := []string{"run", "--run", s.o.run, "--block", s.o.id}
		a.command = closeCommand(append([]string{"code-forge", "gates"}, args...))
		if err != nil {
			return failed(err.Error())
		}
		args = append(args, "--cwd", workspace)
		options, message := gatesParse(args)
		if message != "" {
			return failed(message)
		}
		result := landCapture(func(st *landStep) int {
			code, safe := gatesNativeStep(args, options, st)
			if !safe {
				return landHandover
			}
			return code
		}, nil)
		if result.code == landHandover {
			return failed("the Go engine cannot run these gates; run " + a.command)
		}
		if result.code != 0 {
			return failed(settleLastLine(landFirst(result.error, result.output, fmt.Sprintf("gates run exits %d", result.code))))
		}
	case settleFixReviews:
		a.command = closeCommand([]string{"code-forge", "review-file", "--wait-block", s.o.id, "--run", s.o.run})
		if err != nil {
			return failed(err.Error())
		}
		if detail := s.waitReviews(main, top); detail != "" {
			return failed(detail)
		}
	}
	a.result = "ok"
	fmt.Fprintf(s.progress, "settle %s: %s ok\n", s.o.id, step.fix)
	return a
}

// waitReviews joins the block's reviews (the run daemon, else the local
// join close --auto-claim uses). "" means every file is approved or excluded.
func (s *settleRun) waitReviews(main, top string) string {
	joined := landCapture(func(st *landStep) int {
		return reviewWaitBlockStep([]string{"--wait-block", s.o.id, "--run", s.o.run, "--max", "600s", "--cwd", top}, st)
	}, nil)
	if joined.code == landHandover {
		data, code, err := closeLocalJoin(s.home, s.o.run, s.o.id, main, top, nil, false)
		if err != nil {
			return err.Error()
		}
		joined = landResult{code: code, data: data}
	}
	if joined.code == 3 || joined.data.Get("status") == "worker_down" {
		return "the run worker is down; run code-forge run start --reattach --run " + s.o.run
	}
	if joined.code != 0 {
		return landFirst(joined.error, joined.output, "the review join failed")
	}
	if joined.data.Get("done") != true {
		why := landFirst(blockString(joined.data.Get("reason")), blockString(joined.data.Get("status")), "not finished")
		return "reviews did not finish: " + why
	}
	values, _ := joined.data.Get("files").([]any)
	for _, v := range values {
		file := blockObject(v)
		if file.Get("status") == "excluded" || file.Get("outcome") == "approved" {
			continue
		}
		return fmt.Sprintf("review of %s ended %s", blockString(file.Get("file")), landFirst(blockString(file.Get("outcome")), blockString(file.Get("status")), "unknown"))
	}
	return ""
}

func settleLastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func settleUnique(values []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// settleSignature names a blocker set, to see a round that changed nothing.
func settleSignature(rows []ledger.Object) string {
	parts := []string{}
	for _, row := range rows {
		parts = append(parts, blockString(row.Get("code"))+"\x00"+blockString(row.Get("file"))+"\x00"+blockString(row.Get("detail")))
	}
	return strings.Join(parts, "\x01")
}

// ownerCommands are the fix commands of the owner blockers left.
func (s *settleRun) ownerCommands() []string {
	commands := []string{}
	for _, row := range s.ownerOnly {
		commands = append(commands, blockString(row.Get("fix")))
	}
	return settleUnique(commands)
}

func (s *settleRun) exitCode() int {
	switch s.outcome {
	case settleSettled:
		return 0
	case settleOwner:
		return 3
	}
	return 1
}

// sign writes the one block.settle row.
func (s *settleRun) sign() error {
	actions := []any{}
	for _, a := range s.actions {
		actions = append(actions, a.object())
	}
	remaining := s.remaining
	if remaining == nil {
		remaining = []any{}
	}
	row := ledger.Object{{Key: "event", Value: "block.settle"}, {Key: "block", Value: s.o.id}, {Key: "outcome", Value: s.outcome},
		{Key: "exit", Value: s.exitCode()}, {Key: "rounds", Value: s.rounds}, {Key: "actions", Value: actions}, {Key: "remaining", Value: remaining}}
	if s.failedFix != "" {
		row.Set("failed_fix", s.failedFix)
	}
	if s.stopDetail != "" {
		row.Set("detail", s.stopDetail)
	}
	if s.outcome == settleOwner {
		commands := []any{}
		for _, c := range s.ownerCommands() {
			commands = append(commands, c)
		}
		row.Set("owner_commands", commands)
	}
	_, err := state.WriteSigned(s.home, s.o.run, row)
	return err
}

// print writes the remaining blockers and the summary; it returns the exit code.
func (s *settleRun) print(stdout io.Writer) int {
	code := s.exitCode()
	if s.o.json {
		actions := []any{}
		for _, a := range s.actions {
			actions = append(actions, a.object())
		}
		remaining := s.remaining
		if remaining == nil {
			remaining = []any{}
		}
		out := ledger.Object{{Key: "block", Value: s.o.id}, {Key: "outcome", Value: s.outcome}, {Key: "exit", Value: code}, {Key: "rounds", Value: s.rounds},
			{Key: "actions", Value: actions}, {Key: "remaining", Value: remaining}}
		if s.failedFix != "" {
			out.Set("failed_fix", s.failedFix)
		}
		if s.stopDetail != "" {
			out.Set("detail", s.stopDetail)
		}
		if s.outcome == settleOwner {
			out.Set("owner_line", settleOwnerText(s.o.id, s.ownerCommands()))
		}
		data, _ := ledger.Stringify(out, false)
		fmt.Fprintln(stdout, string(data))
		return code
	}
	for _, v := range s.remaining {
		o := v.(ledger.Object)
		fmt.Fprintf(stdout, "%s %s %s\n  fix: %s\n  clears by: %s\n", o.Get("code"), closeDash(o.Get("file")), o.Get("detail"), o.Get("fix"), o.Get("clears_by"))
	}
	switch s.outcome {
	case settleSettled:
		fmt.Fprintf(stdout, "block %s settled: no blocker left; block close %s --run %s would pass\n", s.o.id, s.o.id, s.o.run)
	case settleOwner:
		fmt.Fprintf(stdout, "block %s settled: only owner blockers left\n%s\n", s.o.id, settleOwnerText(s.o.id, s.ownerCommands()))
	case settleFailed:
		fmt.Fprintf(stdout, "block %s not settled: the %s fix failed: %s\n", s.o.id, s.failedFix, s.stopDetail)
	default:
		fmt.Fprintf(stdout, "block %s not settled: %s\n", s.o.id, s.stopDetail)
	}
	return code
}

// settleOwnerText is the one owner line: the exact owner commands.
func settleOwnerText(id string, commands []string) string {
	return "owner: block " + id + " needs you: " + strings.Join(commands, " ; ")
}

// settleOwnerLines are the owner lines of a run's open blocks: a block whose
// last ledger row is a block.settle row with outcome owner (every blocker
// left is owner-only). rows are the run's ledger rows in order; open says
// whether a block is still open.
func settleOwnerLines(rows []any, run string, open func(id string) bool) []string {
	last := map[string]map[string]any{}
	order := []string{}
	for _, v := range rows {
		row, ok := v.(map[string]any)
		if !ok || row["run"] != run {
			continue
		}
		id, ok := row["block"].(string)
		if !ok || id == "" {
			continue
		}
		if _, seen := last[id]; !seen {
			order = append(order, id)
		}
		last[id] = row
	}
	lines := []string{}
	for _, id := range order {
		row := last[id]
		if row["event"] != "block.settle" || row["outcome"] != settleOwner || !open(id) {
			continue
		}
		list, _ := row["owner_commands"].([]any)
		commands := []string{}
		for _, c := range list {
			if text, ok := c.(string); ok && text != "" {
				commands = append(commands, text)
			}
		}
		if len(commands) > 0 {
			lines = append(lines, settleOwnerText(id, commands))
		}
	}
	return lines
}

// settleOpenBlock says whether a block of a run record (a plain map or an
// ordered object) is still open: present, not closed and not landed.
func settleOpenBlock(record any) func(id string) bool {
	field := func(v any, key string) any {
		switch o := v.(type) {
		case map[string]any:
			return o[key]
		case ledger.Object:
			return o.Get(key)
		}
		return nil
	}
	return func(id string) bool {
		entry := field(field(record, "blocks"), id)
		if entry == nil {
			return false
		}
		return field(entry, "status") != "closed" && field(entry, "landed") == nil
	}
}
