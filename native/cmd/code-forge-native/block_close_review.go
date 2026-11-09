package main

import (
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/queue"
	"github.com/ricardov03/code-forge/native/ticket"
)

// Progress is deliberately not approval. The caller reloads the signed ledger
// and runs the ordinary review gate after these queue markers finish.
type closeReviewWait map[string]string

func closeWaitReviews(run, id, top, qdir string, files []gatecheck.File, changed []string, blockers []gatecheck.Blocker, cfg map[string]any, inTree bool, print io.Writer) closeReviewWait {
	waited := closeReviewWait{}
	pending := map[string]string{}
	idx := queue.Index{Dir: qdir}
	seconds := 600.0
	if n, ok := blockMap(cfg["review"])["close_wait_s"].(float64); ok && n >= 1 && n <= 86400 {
		seconds = n
	}
	deadline := time.Now().Add(time.Duration(seconds * float64(time.Second)))
	// Publish the whole batch before polling any result. The worker pool reviews
	// files concurrently; close has one deadline, independent of the file count.
	for _, file := range files {
		if !slices.Contains(changed, file.File) || !slices.ContainsFunc(blockers, func(b gatecheck.Blocker) bool {
			return b.File == file.File && (b.Code == "unreviewed" || b.Code == "unsigned")
		}) || slices.ContainsFunc(blockers, func(b gatecheck.Blocker) bool {
			return b.File == file.File && b.Code == "not_approved"
		}) {
			continue
		}
		p := ticket.Parts{Run: run, Block: id, File: file.File, ContentHash: file.Hash}
		if inTree {
			p.Tree = &top
		}
		identity, err := queue.Identity(qdir, p)
		if err == nil {
			err = queue.Publish(qdir, p, identity, time.Now())
		}
		if err != nil {
			waited[file.File] = "review could not be queued: " + err.Error()
			continue
		}
		fmt.Fprintf(print, "close: queued review %s ticket %s\n", file.File, identity)
		waited[file.File] = "no signed approval after review completed (ticket " + identity + ")"
		pending[file.File] = identity
	}
	if err := idx.Refresh(); err != nil {
		for file := range pending {
			waited[file] = "review queue unavailable: " + err.Error()
		}
		return waited
	}
	for len(pending) > 0 {
		for file, identity := range pending {
			marker, err := idx.Marker(identity)
			if err != nil {
				waited[file] = "review result unavailable (ticket " + identity + "): " + err.Error()
				delete(pending, file)
			} else if marker != nil {
				if marker["status"] != "reviewed" {
					waited[file] = fmt.Sprintf("reviewer unavailable: %v (ticket %s)", marker["status"], identity)
				}
				delete(pending, file)
			}
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			for file, identity := range pending {
				waited[file] = fmt.Sprintf("review wait timed out after %gs; reviewer may be unavailable (ticket %s)", seconds, identity)
			}
			break
		}
		if len(pending) > 0 {
			time.Sleep(min(50*time.Millisecond, remaining))
		}
	}
	return waited
}

func (waited closeReviewWait) blockers(blockers []gatecheck.Blocker) []gatecheck.Blocker {
	out := make([]gatecheck.Blocker, 0, len(blockers))
	for _, b := range blockers {
		if detail, queued := waited[b.File]; queued && (b.Code == "unreviewed" || b.Code == "unsigned") {
			if slices.ContainsFunc(blockers, func(other gatecheck.Blocker) bool { return other.File == b.File && other.Code == "not_approved" }) {
				continue
			}
			b.Code, b.Detail = "review_pending", detail
		}
		out = append(out, b)
	}
	return out
}
