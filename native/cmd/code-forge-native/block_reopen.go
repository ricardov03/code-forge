package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ricardov03/code-forge/native/blockstate"
	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/gitx"
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
	file, reason := blockString(p.flags["file"]), blockString(p.flags["reason"])
	if blockstate.Trim(reason) == "" {
		return p.usage("block reopen needs --reason")
	}
	if msg := blockstate.File(file, true); msg != "" {
		return p.usage(msg)
	}
	entry := blockObject(blockObject(p.record.Get("blocks")).Get(p.id))
	if entry == nil {
		return p.refusal("unknown_block", fmt.Sprintf("block %s is not in run %s", p.id, p.run))
	}
	if entry.Get("status") != "open" {
		return p.refusal("reopen-block-not-open", fmt.Sprintf("reopen-block-not-open: block %s is not open in run %s; nothing was recorded", p.id, p.run))
	}
	root, ok := p.contentRoot(entry)
	if !ok {
		return p.fallback()
	}
	configError := func() int {
		return p.refusal("reopen-config", fmt.Sprintf("reopen-config: the run's config snapshot or its tmp.root cannot be read, so the review state of %s cannot be found; nothing was recorded", file))
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
	rows, key, code := p.rows()
	if code >= 0 {
		return code
	}
	top, err := gitx.RepoTop(blockString(p.record.Get("workspace")))
	if err != nil {
		return p.fallback()
	}
	if !gatecheck.Owns(gatecheck.Prefix(top, blockString(p.record.Get("workspace"))), entry, file) {
		return p.refusal("reopen-not-owned", fmt.Sprintf("reopen-not-owned: block %s does not own %s; nothing was recorded", p.id, file))
	}
	loaded := blockstate.Load(p.home, legacy, p.run, p.id, file, rows, key)
	if loaded.Status == "tampered" {
		return p.refusal("reopen-tampered", fmt.Sprintf("reopen-tampered: the review state of %s does not verify (a security event, not a stop); the owner's way out is block close --no-require-reviews; nothing was recorded", file))
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
					return p.refusal("reopen-same-content", fmt.Sprintf("reopen-same-content: the content the stop of %s reviewed is unknown and its current content has already been reviewed; change the file and run block reopen, or the owner can run block close --no-require-reviews; nothing was recorded", file))
				}
			}
		}
		if fromHash == current {
			return p.refusal("reopen-same-content", fmt.Sprintf("reopen-same-content: %s still has the content its stop (%s) reviewed; change the file first, or waive its findings (block waive); nothing was recorded", file, stopReason))
		}
	case loaded.State["status"] == "open" && len(blockstate.Array(loaded.State["open"])) > 0:
		from = "open"
		fromHash = current
	default:
		what := "has no review state"
		if loaded.State != nil {
			what = "is " + blockstate.Text(loaded.State["status"])
		}
		return p.refusal("reopen-not-stopped", fmt.Sprintf("reopen-not-stopped: %s %s; only a stopped or lost file is reopened; nothing was recorded", file, what))
	}
	if loaded.Seq < 0 {
		return p.refusal("reopen-tampered", fmt.Sprintf("reopen-tampered: the review state of %s has no valid anchor seq; nothing was recorded", file))
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
			return p.refusal("reopen-already-recorded", fmt.Sprintf("reopen-already-recorded: %s was already reopened at state seq %.0f for other content; run review-file, or the content changed since the reopen; nothing was recorded", file, loaded.Seq))
		}
		earlier = r
		break
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
		return p.refusal("reopen-yes-no-evidence", "reopen-yes-no-evidence: "+detail+"; nothing was recorded")
	}
	for _, r := range rows {
		if r["event"] == "review.reopened" && r["block"] == p.id && r["file"] == file && r["by"] == "agent" && gatecheck.Verified(r, key) {
			return p.refusal("reopen-yes-limit", fmt.Sprintf("reopen-yes-limit: %s already has an agent reopen in this run; nothing was recorded", file))
		}
	}
	if !readable {
		return p.refusal("reopen-yes-disagreed", "reopen-yes-disagreed: The current file cannot be read; nothing was recorded")
	}
	// The judge runs even when an earlier reopen has the same hash.
	return p.bjReopen(bjReopenCase{top: top, root: root, file: file, reason: reason, from: from, stopReason: stopReason, fromHash: fromHash, current: current, content: bytes, loaded: loaded, earlier: earlier, cfg: cfg})
}
