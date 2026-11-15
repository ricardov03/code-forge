package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ricardov03/code-forge/native/blockstate"
	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

func init() { blockSubs["reopen"] = runBlockReopen }

func runBlockReopen(args []string) int {
	p, code := prepareBlockPreview("reopen", args)
	if p == nil {
		return code
	}
	if p.flags["yes"] != true {
		return p.human()
	}
	c, code := p.reopenChecks(true, reopenOut{refuse: p.refusal, fail: p.fail, fallback: p.fallback})
	if code >= 0 {
		return code
	}
	// The judge runs even when an earlier reopen has the same hash.
	return p.bjReopen(c)
}

// reopenOut is where a reopen check's refusal goes: the --yes verb's own
// printing, or the human verb's after its question.
type reopenOut struct {
	refuse   func(code, message string) int
	fail     func(error) int
	fallback func() int
}

// reopenChecks is block.mjs reopenBlockFile and reopen.mjs reopenFile up to
// their effects, in Node's order. agent adds the --yes checks (evidence, the
// one agent reopen per file, a readable file) and the open-review path; the
// human's form, block and ownership checks already ran before the question,
// so its blank-reason usage error comes where reopenFile has it.
func (p *blockPreview) reopenChecks(agent bool, o reopenOut) (bjReopenCase, int) {
	none := bjReopenCase{}
	file, reason := blockString(p.flags["file"]), blockString(p.flags["reason"])
	if agent {
		if blockstate.Trim(reason) == "" {
			return none, o.refuse("usage", "block reopen needs --reason")
		}
		if msg := blockstate.File(file, true); msg != "" {
			return none, o.refuse("usage", msg)
		}
	}
	entry := blockObject(blockObject(p.record.Get("blocks")).Get(p.id))
	if entry == nil {
		return none, o.refuse("unknown_block", fmt.Sprintf("block %s is not in run %s", p.id, p.run))
	}
	if entry.Get("status") != "open" {
		return none, o.refuse("reopen-block-not-open", fmt.Sprintf("reopen-block-not-open: block %s is not open in run %s; nothing was recorded", p.id, p.run))
	}
	root, ok := p.contentRoot(entry)
	if !ok {
		return none, o.fallback()
	}
	configError := func() (bjReopenCase, int) {
		return none, o.refuse("reopen-config", fmt.Sprintf("reopen-config: the run's config snapshot or its tmp.root cannot be read, so the review state of %s cannot be found; nothing was recorded", file))
	}
	cfg, ok := blockSnapshot(p.record)
	if !ok {
		return configError()
	}
	legacy := filepath.Join(os.TempDir(), "code-forge", p.run)
	if base := os.Getenv("CODE_FORGE_TMP_ROOT"); filepath.IsAbs(base) {
		legacy = filepath.Join(base, p.run)
	}
	if v := blockMap(cfg["tmp"])["root"]; v != nil {
		s, ok := v.(string)
		if !ok {
			return configError()
		}
		if !filepath.IsAbs(s) {
			return configError()
		}
		legacy = filepath.Join(s, p.run)
	}
	rows, key, code := p.rowsVia(o.fallback, o.fail)
	if code >= 0 {
		return none, code
	}
	if !agent && blockstate.Trim(reason) == "" {
		return none, o.refuse("usage", "block reopen needs --reason")
	}
	top, err := gitx.RepoTop(blockString(p.record.Get("workspace")))
	if err != nil {
		return none, o.fallback()
	}
	if !gatecheck.Owns(gatecheck.Prefix(top, blockString(p.record.Get("workspace"))), entry, file) {
		return none, o.refuse("reopen-not-owned", fmt.Sprintf("reopen-not-owned: block %s does not own %s; nothing was recorded", p.id, file))
	}
	loaded := blockstate.Load(p.home, legacy, p.run, p.id, file, rows, key)
	if loaded.Status == "tampered" {
		return none, o.refuse("reopen-tampered", fmt.Sprintf("reopen-tampered: the review state of %s does not verify (a security event, not a stop); the owner's way out is block close --no-require-reviews; nothing was recorded", file))
	}
	bytes, readable := blockstate.ReadFile(root, file)
	current := blockstate.Hash(bytes)
	if !readable {
		current = "deleted"
	}
	from, stopReason, fromHash := "", "", ""
	switch {
	case loaded.Status == "lost":
		from = "lost"
	case loaded.State["status"] == "stopped":
		from = "stopped"
		stopReason = "stopped"
		if s, ok := blockstate.Map(loaded.State["next"])["reason"].(string); ok {
			stopReason = s
		}
		fromHash = blockstate.Text(loaded.State["stop_content_hash"])
		if fromHash == "" {
			for i := len(rows) - 1; i >= 0; i-- {
				r := rows[i]
				if r["block"] != p.id || r["file"] != file || !gatecheck.Verified(r, key) {
					continue
				}
				if r["event"] == "review.reopened" {
					break
				}
				if r["event"] != "review.cap" {
					continue
				}
				if r["round"] == loaded.State["round"] && r["reason"] == blockstate.Map(loaded.State["next"])["reason"] {
					fromHash = blockString(r["content_hash"])
				}
				break
			}
		}
		if fromHash == "" {
			for _, r := range rows {
				if r["event"] == "review.round" && r["block"] == p.id && r["file"] == file && r["content_hash"] == current && gatecheck.Verified(r, key) {
					return none, o.refuse("reopen-same-content", fmt.Sprintf("reopen-same-content: the content the stop of %s reviewed is unknown and its current content has already been reviewed; change the file and run block reopen, or the owner can run block close --no-require-reviews; nothing was recorded", file))
				}
			}
		}
		if fromHash == current {
			return none, o.refuse("reopen-same-content", fmt.Sprintf("reopen-same-content: %s still has the content its stop (%s) reviewed; change the file first, or waive its findings (block waive); nothing was recorded", file, stopReason))
		}
	case agent && loaded.State["status"] == "open" && len(blockstate.Array(loaded.State["open"])) > 0:
		from = "open"
		fromHash = current
	default:
		what := "has no review state"
		if loaded.State != nil {
			what = "is " + blockstate.Text(loaded.State["status"])
		}
		return none, o.refuse("reopen-not-stopped", fmt.Sprintf("reopen-not-stopped: %s %s; only a stopped or lost file is reopened; nothing was recorded", file, what))
	}
	if loaded.Seq < 0 {
		return none, o.refuse("reopen-tampered", fmt.Sprintf("reopen-tampered: the review state of %s has no valid anchor seq; nothing was recorded", file))
	}
	var earlier gatecheck.Row
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		if r["event"] != "review.reopened" || r["block"] != p.id || r["file"] != file || r["at_seq"] != loaded.Seq || !gatecheck.Verified(r, key) {
			continue
		}
		automatic := r["by"] == "auto" && r["reason"] == "auto-requeue: content changed after stopped review" && r["from"] == "stopped" && blockString(r["from_hash"]) != "" && blockString(r["to_hash"]) != "" && r["from_hash"] != r["to_hash"]
		if r["by"] != "human" && !(r["by"] == "agent" && blockstate.Map(r["verdict"])["verdict"] == "agree") && !automatic {
			continue
		}
		if r["to_hash"] != current {
			return none, o.refuse("reopen-already-recorded", fmt.Sprintf("reopen-already-recorded: %s was already reopened at state seq %.0f for other content; run review-file, or the content changed since the reopen; nothing was recorded", file, loaded.Seq))
		}
		earlier = r
		break
	}
	c := bjReopenCase{top: top, root: root, file: file, reason: reason, from: from, stopReason: stopReason, fromHash: fromHash, current: current, content: bytes, loaded: loaded, earlier: earlier, cfg: cfg, human: !agent}
	if !agent {
		return c, -1
	}
	evidence := blockstate.Evidence(reason, root, previewEntryMap(entry))
	if from == "open" {
		evidence = len(blockstate.Cited(reason, root)) > 0
	}
	if !evidence {
		detail := "cite an existing repo path with :line or :start-end, an existing test file, or a block acceptance clause number"
		if from == "open" {
			detail = "cite an existing repo path with :line or :start-end for an open review"
		}
		return none, o.refuse("reopen-yes-no-evidence", "reopen-yes-no-evidence: "+detail+"; nothing was recorded")
	}
	for _, r := range rows {
		if r["event"] == "review.reopened" && r["block"] == p.id && r["file"] == file && r["by"] == "agent" && gatecheck.Verified(r, key) {
			return none, o.refuse("reopen-yes-limit", fmt.Sprintf("reopen-yes-limit: %s already has an agent reopen in this run; nothing was recorded", file))
		}
	}
	if !readable {
		return none, o.refuse("reopen-yes-disagreed", "reopen-yes-disagreed: The current file cannot be read; nothing was recorded")
	}
	return c, -1
}

// reopenHuman is block.mjs's human reopen at a terminal (B680): reopenPrecheck
// (the --file form, the block, ownership), the question, the lazy expiry
// checks, then reopenBlockFile with by: human — clearDone, the signed
// review.reopened row and the enqueue. Every read the effects need is tried
// before the question, so a case Go cannot serve goes to Node unasked.
func (p *blockPreview) reopenHuman() int {
	h := newHumanVerb("reopen", p.args)
	h.noYes = "--yes works only with evidence and agreement from a fresh judge"
	h.json = p.flags["json"] == true
	if !blockExpirySafe(p.record) || !blockStorageSafe(p.home, p.run, p.record) {
		return h.fallback()
	}
	file := blockString(p.flags["file"])
	precheck := func() int {
		if msg := blockstate.File(file, true); msg != "" {
			return h.usage(msg)
		}
		entry := blockObject(blockObject(p.record.Get("blocks")).Get(p.id))
		if entry == nil {
			return h.refuse("unknown_block", fmt.Sprintf("block %s is not in run %s", p.id, p.run))
		}
		if entry.Get("status") != "open" {
			return h.refuse("reopen-block-not-open", fmt.Sprintf("reopen-block-not-open: block %s is not open in run %s; nothing was recorded", p.id, p.run))
		}
		workspace := blockString(p.record.Get("workspace"))
		top, err := gitx.RepoTop(workspace)
		if err != nil {
			return h.fallback()
		}
		if !gatecheck.Owns(gatecheck.Prefix(top, workspace), entry, file) {
			return h.refuse("reopen-not-owned", fmt.Sprintf("reopen-not-owned: block %s does not own %s; nothing was recorded", p.id, file))
		}
		if !p.reopenServable(entry, top, file) {
			return h.fallback()
		}
		return -1
	}
	if code := h.ask(p.id, fmt.Sprintf("Reopen the review of %s in block %s (run %s)? Only the owner may say yes.", file, p.id, p.run), precheck); code >= 0 {
		return code
	}
	blockGrantExpiry(p.home, p.run, os.Stderr)
	c, code := p.reopenChecks(false, reopenOut{refuse: h.refuse, fail: h.fail, fallback: h.fallback})
	if code >= 0 {
		return code
	}
	c.refuse = h.text
	mainTop, dir, parts, ok := p.bjReopenQueue(c)
	if !ok {
		return h.fallback()
	}
	var stopReason any
	if c.from == "stopped" {
		stopReason = c.stopReason
	}
	return p.bjReopenWrite(c, mainTop, dir, parts, stopReason, "", "")
}

// reopenServable is true when every read reopenChecks and the effects need
// works in Go: the block's tree, the ledger, the key (a refusal of its own
// is printed after the question, as Node does) and the queue.
func (p *blockPreview) reopenServable(entry ledger.Object, top, file string) bool {
	root, ok := p.contentRoot(entry)
	if !ok {
		return false
	}
	if _, _, code := p.rowsVia(func() int { return 1 }, func(err error) int {
		var se *state.Error
		if errors.As(err, &se) {
			return -1
		}
		return 1
	}); code > 0 {
		return false
	}
	_, _, _, ok = p.bjReopenQueue(bjReopenCase{top: top, root: root, file: file})
	return ok
}
