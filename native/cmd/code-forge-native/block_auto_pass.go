package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/autopilot"
	"github.com/ricardov03/code-forge/native/blockclose"
	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/humancheck"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/proof"
	"github.com/ricardov03/code-forge/native/state"
)

const autoPassUsage = "usage: code-forge block auto-pass <id> --run <r> --proof <file> --note <text> [--clause C<n>]… [--json] [--dry-run]\n"
const autoPassOff = "human-pass-evidence-off: enable human_check.autopilot in .code-forge.yml and start a new run"

func init() { blockSubs["auto-pass"] = runBlockAutoPass }

var autoPassBeforeUpdate = func() {} // tests change state after fast fail, before the authoritative lock
var autoPassDesignText = regexp.MustCompile(`(?i)design|diseño|board|tablero|mock`)
var autoPassControl = regexp.MustCompile(`[\x00-\x1f\x7f]`)
var autoPassClauseID = regexp.MustCompile(`^C[1-9][0-9]*$`)

type autoPassOptions struct {
	run, id, file, note string
	clauses             []string
	json, dry           bool
}
type autoPassCheck struct {
	Name   string `json:"check"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}
type autoPassPlan struct {
	checks        []autoPassCheck
	targets       []humancheck.Entry
	remaining     int
	grantID       string
	proof         autoPassProof
	coderSessions []any
}

func (p autoPassPlan) err() error {
	for _, c := range p.checks {
		if !c.OK {
			return fmt.Errorf("%s: %s", c.Name, c.Detail)
		}
	}
	return nil
}
func autoPassConfig(record ledger.Object) map[string]any {
	cfg, _ := blockSnapshot(record)
	return blockMap(blockMap(cfg["human_check"])["autopilot"])
}
func autoPassEnabled(record ledger.Object) bool { return autoPassConfig(record)["enabled"] == true }

func autoPassParse(args []string) (o autoPassOptions, err error) {
	values := map[string]string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			if o.id != "" {
				return o, fmt.Errorf("one block id is required")
			}
			o.id = a
			continue
		}
		name, v, inline := strings.Cut(a[2:], "=")
		if name == "json" || name == "dry-run" {
			if inline || name == "json" && o.json || name == "dry-run" && o.dry {
				return o, fmt.Errorf("invalid or duplicate --%s", name)
			}
			if name == "json" {
				o.json = true
			} else {
				o.dry = true
			}
			continue
		}
		if !slices.Contains([]string{"run", "proof", "note", "clause"}, name) {
			return o, fmt.Errorf("unknown flag --%s", name)
		}
		if !inline {
			if i+1 == len(args) || strings.HasPrefix(args[i+1], "--") {
				return o, fmt.Errorf("--%s needs a value", name)
			}
			i++
			v = args[i]
		}
		if name == "clause" {
			if !autoPassClauseID.MatchString(v) || slices.Contains(o.clauses, v) {
				return o, fmt.Errorf("--clause needs a distinct C<n>")
			}
			o.clauses = append(o.clauses, v)
			continue
		}
		if _, seen := values[name]; seen {
			return o, fmt.Errorf("--%s given more than once", name)
		}
		values[name] = v
	}
	o.run, o.file, o.note = values["run"], values["proof"], values["note"]
	if o.run == "" || o.id == "" {
		return o, fmt.Errorf("a block id and --run are required")
	}
	return o, nil
}

func runBlockAutoPass(args []string) int {
	if slices.Contains(args, "--help") || slices.Contains(args, "-h") {
		fmt.Fprint(safeStdout, autoPassUsage)
		return 0
	}
	o, e := autoPassParse(args)
	if e != nil {
		fmt.Fprintln(safeStderr, "block auto-pass:", e)
		return 2
	}
	return blockAutoPass(o, safeStdout, safeStderr)
}
func blockAutoPass(o autoPassOptions, stdout, stderr io.Writer) int {
	fail := func(e error) int { fmt.Fprintln(stderr, "block auto-pass:", e); return 1 }
	home, e := os.UserHomeDir()
	if e != nil {
		return fail(e)
	}
	record, e := state.ReadRun(home, o.run)
	if e != nil {
		return fail(e)
	}
	p := autoPassEvaluate(home, o, record)
	if o.dry {
		ok := p.err() == nil
		if o.json {
			b, _ := ledger.Stringify(map[string]any{"block": o.id, "dry_run": true, "ok": ok, "checks": p.checks}, false)
			fmt.Fprintln(stdout, string(b))
		} else {
			for _, c := range p.checks {
				status := "pass"
				if !c.OK {
					status = "fail"
				}
				fmt.Fprintf(stdout, "%s %s: %s\n", status, c.Name, c.Detail)
			}
		}
		if !ok {
			return 1
		}
		return 0
	}
	if e := p.err(); e != nil {
		return fail(e)
	}
	autoPassBeforeUpdate()
	var status, event string
	e = state.UpdateBlock(home, o.run, "", func(current, _ *ledger.Object) ([]ledger.Object, error) {
		p = autoPassEvaluate(home, o, *current)
		if e := p.err(); e != nil {
			return nil, e
		}
		decisionID, e := autopilotDecisionID()
		if e != nil {
			return nil, e
		}
		ts := autopilotStamp(time.Now())
		clauses := []any{}
		for _, c := range p.targets {
			clauses = append(clauses, c.Object())
		}
		decision := ledger.Object{{Key: "event", Value: "autopilot.decision"}, {Key: "decision_id", Value: decisionID}, {Key: "grant_id", Value: p.grantID}, {Key: "scope", Value: autopilot.EvidenceHumanScope}, {Key: "subject", Value: ledger.Object{{Key: "block", Value: o.id}}}, {Key: "block", Value: o.id}, {Key: "clauses", Value: clauses}, {Key: "decision", Value: "pass"}, {Key: "acted", Value: true}, {Key: "within_scope", Value: true}, {Key: "to_owner", Value: true}, {Key: "proof_sha256", Value: p.proof.Hash}, {Key: "coder_sessions", Value: p.coderSessions}, {Key: "prover", Value: p.proof.Prover}, {Key: "ts", Value: ts}}
		rows := []ledger.Object{decision}
		checks := []any{}
		for _, c := range p.targets {
			checks = append(checks, c.Check)
			rows = append(rows, ledger.Object{{Key: "event", Value: humancheck.ClearedEvent}, {Key: "block", Value: o.id}, {Key: "clause", Value: c.Clause}, {Key: "check", Value: c.Check}, {Key: "waits_for", Value: c.WaitsFor}, {Key: "note", Value: o.note}, {Key: "by", Value: "autopilot"}, {Key: "passed_by", Value: "autopilot"}, {Key: "grant_id", Value: p.grantID}, {Key: "decision_id", Value: decisionID}, {Key: "proof", Value: p.proof.Path}, {Key: "proof_sha256", Value: p.proof.Hash}, {Key: "evidence", Value: p.proof.Evidence}, {Key: "prover", Value: p.proof.Prover}, {Key: "coder_sessions", Value: p.coderSessions}, {Key: "ts", Value: ts}})
		}
		blocks := blockObject(current.Get("blocks"))
		entry := blockObject(blocks.Get(o.id))
		status = blockString(entry.Get("status"))
		if p.remaining == len(p.targets) {
			event = "block.human_pass"
			if status == humancheck.DeferredStatus {
				event = humancheck.DeferredEvent
			}
			status = "closed"
			rows = append(rows, ledger.Object{{Key: "event", Value: event}, {Key: "block", Value: o.id}, {Key: "status", Value: status}, {Key: "note", Value: o.note}, {Key: "proof", Value: p.proof.Path}, {Key: "proof_sha256", Value: p.proof.Hash}, {Key: "checks", Value: checks}, {Key: "by", Value: "autopilot"}, {Key: "grant_id", Value: p.grantID}, {Key: "decision_id", Value: decisionID}, {Key: "ts", Value: ts}})
			entry.Set("status", status)
			blocks.Set(o.id, entry)
			current.Set("blocks", blocks)
		}
		return rows, nil
	})
	if e != nil {
		return fail(e)
	}
	done := ""
	if status == "closed" {
		key, _ := blockclose.ReadKey(home, o.run)
		done = closeWriteDone(home, o.run, o.id, record, blockclose.Result{OK: true, Status: status, Event: event}, nil, nil, key, stderr)
	}
	if o.json {
		b, _ := ledger.Stringify(map[string]any{"block": o.id, "status": status, "passed_by": "autopilot", "clauses": p.targets, "proof_sha256": p.proof.Hash}, false)
		fmt.Fprintln(stdout, string(b))
	} else {
		fmt.Fprintf(stdout, "block %s %s (%d checks passed by autopilot)\n", o.id, status, len(p.targets))
	}
	if status == "closed" {
		blockRunEndHook(home, o.run, o.id, status, done, record, stderr)
	}
	return 0
}

// Every check is read-only and is repeated with the current record under
// UpdateBlock. In particular no lazy grant expiry may write during a refusal.
func autoPassEvaluate(home string, o autoPassOptions, record ledger.Object) autoPassPlan {
	p := autoPassPlan{coderSessions: []any{}}
	check := func(name, detail string, ok bool) { p.checks = append(p.checks, autoPassCheck{name, ok, detail}) }
	cfg := autoPassConfig(record)
	check("human-pass-evidence-off", strings.TrimPrefix(autoPassOff, "human-pass-evidence-off: "), cfg["enabled"] == true)
	check("auto-pass-run", "run must be active", record.Get("status") == "active")
	grant, e := autopilot.ReadGrantNode(record, time.Now())
	why := autopilot.ScopeRefusal(grant, autopilot.EvidenceHumanScope)
	check("auto-pass-grant", "active grant must allow human-pass:evidence ("+why+")", e == nil && why == "")
	p.grantID = blockString(grant.Grant.Get("grant_id"))
	entry := blockObject(blockObject(record.Get("blocks")).Get(o.id))
	check("auto-pass-status", "block must await human checks or be closed_deferred_human", entry.Get("status") == "awaiting_human_check" || entry.Get("status") == humancheck.DeferredStatus)
	key, keyErr := blockclose.ReadKey(home, o.run)
	all, readErr := ledger.ReadAllRows(filepath.Join(home, ".code-forge/ledger"), blockString(record.Get("project")))
	rows := []gatecheck.Row{}
	for _, v := range all {
		if r, ok := v.(map[string]any); ok && r["run"] == o.run {
			rows = append(rows, r)
		}
	}
	check("auto-pass-signed-grant", "grant needs its signed autopilot.grant row", keyErr == nil && readErr == nil && autopilotGrantRow(rows, o.run, p.grantID, key) != nil)
	open := humancheck.Open(o.run, record, all, key)
	known := map[string]bool{}
	designs := []string{}
	acceptance, _ := entry.Get("acceptance").([]any)
	for _, c := range open {
		if c.Block != o.id {
			continue
		}
		p.remaining++
		known[c.Clause] = true
		requested := len(o.clauses) == 0 || slices.Contains(o.clauses, c.Clause)
		if !requested {
			continue
		}
		clause := ledger.Object{}
		for i, v := range acceptance {
			if humancheck.ClauseID(i) == c.Clause {
				clause = blockObject(v)
			}
		}
		approval := clause.Get("approval")
		excluded := slices.Contains(autoPassArray(cfg["exclude_waits_for"]), c.WaitsFor)
		heuristic := strings.HasPrefix(strings.ToUpper(o.id), "D") || autoPassDesignText.MatchString(c.Check)
		design := approval == "design" || excluded || approval != "device" && heuristic
		if design {
			designs = append(designs, c.Clause)
			continue
		}
		p.targets = append(p.targets, c)
	}
	bad := ""
	for _, id := range o.clauses {
		if !known[id] {
			bad = "clause " + id + " is not an open human check"
		}
	}
	if len(o.clauses) > 0 && len(designs) > 0 {
		bad = "clause " + designs[0] + " is a design approval; only the owner can pass it"
	}
	if len(p.targets) == 0 && bad == "" {
		bad = "no open device checks"
		if len(designs) > 0 {
			bad = "clause " + designs[0] + " is a design approval; only the owner can pass it"
		}
	}
	check("auto-pass-design", bad, bad == "")
	workspace := blockString(record.Get("workspace"))
	tree := workspace
	treeInfo, treeErr := proof.BlockTree(workspace, entry)
	if treeInfo != nil {
		tree = treeInfo.Path
	}
	p.proof, e = autoPassReadProof(o.file, workspace, tree, o.run, o.id, autoPassArray(cfg["proof_dirs"]), p.targets)
	proofWhy := "proof header and evidence verified"
	if e != nil {
		proofWhy = e.Error()
	}
	requireProof, present := cfg["require_proof"]
	if present && requireProof != true {
		proofWhy = "require_proof must be true in v1"
	}
	if treeErr != nil {
		proofWhy = "cannot resolve worktree: " + treeErr.Error()
	}
	check("auto-pass-proof", proofWhy, e == nil && (!present || requireProof == true) && treeErr == nil)
	// Current ledgers have role:coder session rows without an id; keep [] in
	// that case. Recognize session ids when adapters record them additively.
	for _, r := range rows {
		if r["block"] != o.id || r["role"] != "coder" || !gatecheck.Verified(r, key) {
			continue
		}
		for _, field := range []string{"session_id", "sessionId", "coder_session", "coder_session_id", "session"} {
			if id, ok := r[field].(string); ok && id != "" && !slices.Contains(p.coderSessions, any(id)) {
				p.coderSessions = append(p.coderSessions, id)
			}
		}
	}
	separate := cfg["require_separate_prover"] != false
	check("auto-pass-self-proof", "prover must differ from every coder session", !separate || p.proof.Prover != "" && !slices.Contains(p.coderSessions, any(p.proof.Prover)))
	waived, hits := false, false
	grantIndex := len(rows)
	for i, r := range rows {
		if r["event"] == "autopilot.grant" && r["grant_id"] == p.grantID && gatecheck.Verified(r, key) {
			grantIndex = i
		}
	}
	for i, r := range rows {
		if r["block"] != o.id {
			continue
		}
		ev := blockString(r["event"])
		waived = waived || strings.Contains(ev, "waiv") && (r["grant_id"] == p.grantID || i > grantIndex)
		hits = hits || ev == "rule_break" || ev == "rule_break.accepted"
	}
	check("auto-pass-waived", "no waiver rows under the current grant", !waived)
	transcript, missing, transcriptErr := closeTranscript(record, entry, o.id, "")
	transcriptHits, supported := gatecheck.Transcript(transcript)
	check("auto-pass-transcript", "no transcript hits or accepted hits", !hits && transcriptErr == nil && supported && len(transcriptHits) == 0 && !missing)
	machineWhy := autoPassMachine(entry, tree, o.run, o.id, rows, key)
	check("auto-pass-machine", machineWhy, machineWhy == "")
	check("auto-pass-note", "--note must be non-blank and contain no control characters", humanTrim(o.note) != "" && closeTextSafe(o.note) && !autoPassControl.MatchString(o.note))
	return p
}
func autoPassArray(v any) []any { a, _ := v.([]any); return a }
func autoPassMachine(entry ledger.Object, tree, run, id string, rows []gatecheck.Row, key []byte) string {
	top, e := gitx.RepoTop(tree)
	if e != nil {
		return "cannot read worktree"
	}
	base := blockString(entry.Get("base_sha"))
	changed, e := gitx.ChangedFiles(top, base)
	if e != nil {
		return "cannot list changed files"
	}
	prefix := gatecheck.Prefix(top, tree)
	files, _, e := gatecheck.FileSet(top, base, prefix, id, entry, rows, changed)
	if e != nil {
		return "cannot hash changed files"
	}
	if len(files) == 0 {
		return "no approved changed files"
	}
	gate := gatecheck.Check(run, id, files, rows, key, nil, nil, false)
	if gate.Unknown {
		return "unknown review state"
	}
	if len(gate.Waived) > 0 {
		return "waived or answered-only review checks"
	}
	if len(gate.Blockers) > 0 {
		return "review checks failed: " + gatecheck.Reason(gate.Blockers)
	}
	for _, f := range files {
		approved := false
		for _, r := range rows {
			approved = approved || r["event"] == "review.approved" && r["block"] == id && r["file"] == f.File && r["content_hash"] == f.Hash && gatecheck.Verified(r, key)
		}
		if !approved {
			return "no signed approval for " + f.File
		}
	}
	// Use close:routine's signed-green policy, after independently enforcing
	// this scope's current-grant waiver rule and all review checks above.
	green := false
	for _, r := range rows {
		green = green || r["event"] == "gate.done" && r["block"] == id && r["all_ok"] == true && gatecheck.Verified(r, key)
	}
	if !green {
		return "no signed green gate row"
	}
	return ""
}
