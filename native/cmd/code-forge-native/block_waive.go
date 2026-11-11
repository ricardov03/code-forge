package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/blockclose"
	"github.com/ricardov03/code-forge/native/blockstate"
	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/proof"
	"github.com/ricardov03/code-forge/native/state"
)

func init() { blockSubs["waive"] = runBlockWaive }

// All three routes share the strict argument declarations and read-only boundary.
// The existing public reason is retained: the shared reason table is owned by B282.
type blockPreview struct {
	sub           string
	args          []string
	flags         map[string]any
	pos           []string
	home, run, id string
	record        ledger.Object
}

func (p *blockPreview) fallback() int {
	return blockHandover(append([]string{p.sub}, p.args...), hBlockUnportedSubcommand)
}
func (p *blockPreview) fail(err error) int {
	var se *state.Error
	if !errors.As(err, &se) {
		return p.fallback()
	}
	code := 1
	if se.Code == "usage" {
		code = 2
	}
	return p.errorText(code, fmt.Sprintf("block %s: %s\n", p.sub, err))
}
func (p *blockPreview) crumb() (name, sub string) {
	if p.sub == "waive" {
		return "block waive", "waive"
	}
	return "block", ""
}
func (p *blockPreview) errorText(code int, text string) int {
	if !closeTextSafe(text) || reviewWaitLogging() && (strings.ContainsFunc(text, func(r rune) bool { return r > 127 }) || p.sub == "waive" && strings.Contains(text, "'")) {
		return p.fallback()
	}
	name, sub := p.crumb()
	before := gatesBreadcrumbName(name)
	fmt.Fprint(os.Stderr, text)
	nativeVerbFailure("block", sub, append([]string{p.sub}, p.args...), code, strings.TrimSpace(text), before)
	return code
}
func (p *blockPreview) refusal(code, detail string) int {
	return p.fail(&state.Error{Code: code, Message: detail})
}
func (p *blockPreview) usage(detail string) int { return p.refusal("usage", detail) }
func prepareBlockPreview(sub string, args []string) (*blockPreview, int) {
	p := &blockPreview{sub: sub, args: args}
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			forms := map[string]string{"ready": "<id> --run <r> [--worker-pid <pid>] [--transcript <file>] [--report <file>]", "waive": "<id> <finding-id> --run <r> --file <path> --reason <text> [--yes]", "reopen": "<id> --run <r> --file <path> --reason <text> [--yes]"}
			fmt.Printf("usage: code-forge block %s %s [--json] [--help|-h]\n  --help, -h  Print this usage and exit without changing state.\n  --json      Print one JSON line on success instead of text.\n", sub, forms[sub])
			if sub == "ready" {
				fmt.Print("  Read-only close checks: list every blocker and its fix; exit 1 while any remain.\n")
			}
			return nil, 0
		}
	}
	var message string
	p.flags, p.pos, message = blockstate.Flags(args, sub != "ready")
	if message != "" {
		return nil, p.usage(message)
	}
	p.run = blockString(p.flags["run"])
	if len(p.pos) == 0 || p.pos[0] == "" || p.run == "" {
		return nil, p.usage("a block id and --run are required")
	}
	p.id = p.pos[0]
	if sub == "ready" {
		invalid := len(p.pos) != 1
		for k := range p.flags {
			invalid = invalid || !slices.Contains([]string{"run", "json", "worker-pid", "transcript", "report"}, k)
		}
		if invalid {
			return nil, p.usage("block ready takes one block id, --run and optional --worker-pid/--transcript/--report/--json")
		}
	}
	if p.flags["remove-worktree"] == true {
		return nil, p.usage("--remove-worktree is a block close flag")
	}
	if sub != "ready" {
		if _, ok := p.flags["reason"].(string); !ok {
			return nil, p.usage("block " + sub + " needs --reason")
		}
		if sub == "waive" && (len(p.pos) != 2 || p.pos[1] == "") {
			return nil, p.usage("block waive needs exactly one finding id")
		}
		if sub == "reopen" && len(p.pos) != 1 {
			return nil, p.usage("block reopen takes one block id (the file goes in --file)")
		}
		if _, ok := p.flags["file"].(string); !ok {
			suffix := ""
			if sub == "waive" {
				suffix = " (a finding id is scoped to one file)"
			}
			return nil, p.usage("block " + sub + " needs --file <path>" + suffix)
		}
	}
	var err error
	p.home, err = os.UserHomeDir()
	if err != nil {
		return nil, p.fallback()
	}
	p.record, err = state.ReadRun(p.home, p.run)
	if err != nil {
		return nil, p.fail(err)
	}
	return p, -1
}
func (p *blockPreview) rows() ([]gatecheck.Row, []byte, int) {
	values, err := ledger.ReadAllRows(filepath.Join(p.home, ".code-forge/ledger"), blockString(p.record.Get("project")))
	if err != nil {
		return nil, nil, p.fallback()
	}
	rows := []gatecheck.Row{}
	for _, v := range values {
		r, ok := v.(map[string]any)
		if !ok {
			return nil, nil, p.fallback()
		}
		if r["run"] == p.run {
			rows = append(rows, r)
		}
	}
	key, err := blockclose.ReadKey(p.home, p.run)
	if err != nil {
		return nil, nil, p.fail(err)
	}
	return rows, key, -1
}
func (p *blockPreview) human() int {
	// Without a terminal Node refuses before checking even the path form or
	// block identity. At one, waive asks here (B453); reopen's queue work
	// stays with Node.
	if blockAskAvailable() {
		if p.sub == "waive" {
			return p.waiveHuman()
		}
		return p.fallback()
	}
	suffix := "--yes works only with evidence and agreement from a fresh judge"
	if p.sub == "waive" {
		suffix = "--yes works only for a recorded warning or nit finding, with evidence"
	}
	return p.errorText(2, fmt.Sprintf("block %s needs the human at a terminal to confirm; nothing was recorded (%s)\n", p.sub, suffix))
}
func previewEntryMap(entry ledger.Object) map[string]any { m, _ := statusMap(entry); return m }
func (p *blockPreview) contentRoot(entry ledger.Object) (string, bool) {
	top, err := gitx.RepoTop(blockString(p.record.Get("workspace")))
	if err != nil {
		return "", false
	}
	wt, err := proof.BlockTree(blockString(p.record.Get("workspace")), entry)
	if err != nil {
		return "", false
	}
	if wt != nil {
		top = proof.Real(wt.Path)
	}
	return top, true
}
func runBlockWaive(args []string) int {
	p, code := prepareBlockPreview("waive", args)
	if p == nil {
		return code
	}
	if p.flags["yes"] != true {
		return p.human()
	}
	file, finding := blockString(p.flags["file"]), p.pos[1]
	if msg := blockstate.Finding(finding); msg != "" {
		return p.usage(msg)
	}
	if msg := blockstate.File(file, false); msg != "" {
		return p.usage(msg)
	}
	entry := blockObject(blockObject(p.record.Get("blocks")).Get(p.id))
	if entry == nil {
		return p.refusal("unknown_block", fmt.Sprintf("block %s is not in run %s", p.id, p.run))
	}
	top, err := gitx.RepoTop(blockString(p.record.Get("workspace")))
	if err != nil {
		return p.fallback()
	}
	if !gatecheck.Owns(gatecheck.Prefix(top, blockString(p.record.Get("workspace"))), entry, file) {
		return p.refusal("waive-not-owned", fmt.Sprintf("waive-not-owned: block %s does not own %s; nothing was recorded", p.id, file))
	}
	rows, key, code := p.rows()
	if code >= 0 {
		return code
	}
	severity := blockstate.Severity(rows, p.id, file, finding, key)
	grant := blockObject(p.record.Get("autopilot"))
	until, e := time.Parse(time.RFC3339Nano, blockString(grant.Get("until")))
	if severity == "critical" && p.record.Get("status") == "active" && grant.Get("status") == "active" && e != nil {
		return p.fallback()
	}
	active := p.record.Get("status") == "active" && grant.Get("status") == "active" && e == nil && time.Now().Before(until)
	if severity != "warning" && severity != "nit" && !(severity == "critical" && active) {
		return p.refusal("waive-yes-critical", fmt.Sprintf("waive-yes-critical: finding %s is %s; a critical finding needs an active autopilot grant; otherwise only warning or nit findings may use --yes; nothing was recorded", finding, severity))
	}
	root, ok := p.contentRoot(entry)
	if !ok {
		return p.fallback()
	}
	reason := blockString(p.flags["reason"])
	if severity == "critical" {
		if code := p.criticalTest(reason, root, entry, rows, key); code >= 0 {
			return code
		}
		if !slices.Contains(blockstate.Cited(reason, root), file) {
			return p.refusal("waive-yes-no-evidence", fmt.Sprintf("waive-yes-no-evidence: cite the finding's path %s with :line or :start-end; nothing was recorded", file))
		}
	} else if !blockstate.Evidence(reason, root, previewEntryMap(entry)) {
		return p.refusal("waive-yes-no-evidence", "waive-yes-no-evidence: cite an existing repo path with :line or :start-end, an existing test file, or a block acceptance clause number; nothing was recorded")
	}
	if _, ok := blockstate.ReadFile(root, file); !ok {
		return p.refusal("waive-yes-disagreed", "waive-yes-disagreed: The current file cannot be read; nothing was recorded")
	}
	return p.fallback() // fresh model judge; no writes before this boundary
}

func (p *blockPreview) criticalTest(reason, root string, entry ledger.Object, rows []gatecheck.Row, key []byte) int {
	refuse := func(detail string) int {
		return p.refusal("waive-yes-critical-no-test", "waive-yes-critical-no-test: "+detail+"; nothing was recorded")
	}
	cited := blockstate.CitedTests(reason, root, blockstate.Array(previewEntryMap(entry)["acceptance"]))
	if len(cited) == 0 {
		return refuse("cite an existing test file named in this block's acceptance tests")
	}
	var latest gatecheck.Row
	for _, r := range rows {
		if r["block"] == p.id && (r["event"] == "gate.done" || r["event"] == "gate.red") {
			latest = r
		}
	}
	passed := false
	for _, v := range blockstate.Array(latest["results"]) {
		r := blockstate.Map(v)
		passed = passed || r["gate"] == "test" && r["ok"] == true && r["skipped"] == false
	}
	wt := blockString(blockObject(entry.Get("worktree")).Get("path"))
	_, hasTree := latest["worktree"]
	if latest == nil || !gatecheck.Verified(latest, key) || latest["event"] != "gate.done" || latest["all_ok"] != true || !passed || (wt == "" && hasTree) || (wt != "" && latest["worktree"] != wt) {
		return refuse("the cited acceptance test needs a passing test result in this block's latest signed gate record")
	}
	listed, err := gates.List(root)
	if err != nil {
		return p.fallback()
	}
	baseline := gates.Baseline(root, entry, listed)
	recorded, ok := latest["files"].(map[string]any)
	match := baseline != nil && ok && len(baseline) == len(recorded)
	for _, field := range baseline {
		match = match && recorded[field.Key] == field.Value
	}
	if !match {
		return refuse("rerun the acceptance test: the latest gate record does not cover the current block content")
	}
	// The agent-waiver rule uses the declared entry count, not expanded ownership.
	scope := gates.TestScope(entry, listed, int(^uint(0)>>1))
	if len(blockstate.Array(entry.Get("owned_files"))) > 10 {
		scope.Kind = "full"
	}
	for _, file := range cited {
		if recorded[file] != nil && (latest["scope"] == "full" || latest["scope"] == "related" && scope.Kind == "related" && slices.Contains(scope.Files, file)) {
			return -1
		}
	}
	return refuse("the latest passing gate record must cover the cited acceptance test file and its current content")
}
