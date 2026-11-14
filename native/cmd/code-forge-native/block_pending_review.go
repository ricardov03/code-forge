package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/ledger"
)

// B625: COMPLETE_PENDING_REVIEW. A block whose only close blockers are files
// with no review because the reviewer infrastructure failed (the latest signed
// review.result row for the file's current content is "unavailable": a
// timeout, a provider that is down, a spawn or worker error) is not a coder
// failure. block ready and block ready --preflight name the state and give the
// review command as each such file's fix; block close still refuses until the
// files are reviewed, and no coder session is needed.

// pendingReviewBlockers is the preflight's review_unavailable blocker for each
// unreviewed file whose review failed only on reviewer infrastructure.
func pendingReviewBlockers(files []gatecheck.File, unreviewed map[string]bool, rows []gatecheck.Row, run, id string, key []byte) []gatecheck.Blocker {
	out := []gatecheck.Blocker{}
	for _, f := range files {
		if !unreviewed[f.File] {
			continue
		}
		if reason, down := gatecheck.ReviewerDown(rows, run, id, f.File, f.Hash, key); down {
			out = append(out, gatecheck.Blocker{Code: "review_unavailable", File: f.File, Detail: pendingReviewDetail(f.File, reason)})
		}
	}
	return out
}

func pendingReviewDetail(file, reason string) string {
	if reason == "" {
		reason = "unavailable"
	}
	return "the reviewer could not review " + file + " (" + reason + "); review it again, the coder's work needs no new session"
}

// readyPendingReview returns the files of block ready's blockers when every
// blocker is an unreviewed file, or its missing review ticket in the coder
// report, whose review failed only on reviewer infrastructure. nil otherwise.
func readyPendingReview(blockers []gatecheck.Blocker, files []gatecheck.File, rows []gatecheck.Row, run, id string, key []byte) map[string]bool {
	if len(blockers) == 0 {
		return nil
	}
	hash := map[string]string{}
	for _, f := range files {
		hash[f.File] = f.Hash
	}
	down := map[string]bool{}
	for _, b := range blockers {
		reviewOnly := b.Code == "unreviewed" || b.Code == "coder_report_failed" && strings.HasPrefix(b.Detail, "coder report FAILED: no review-file ticket id for ")
		h, owned := hash[b.File]
		if !reviewOnly || b.File == "" || !owned {
			return nil
		}
		if _, ok := gatecheck.ReviewerDown(rows, run, id, b.File, h, key); !ok {
			return nil
		}
		down[b.File] = true
	}
	return down
}

// pendingReviewFix sets the review command as the fix of each formatted
// blocker of a reviewer-down file (codes review_unavailable, unreviewed and
// coder_report_failed) and returns those files in order when state is true.
func pendingReviewFix(formatted []any, id, run string, down func(code, file string) bool, state bool) []string {
	files := []string{}
	for _, v := range formatted {
		o := v.(ledger.Object)
		code, file := blockString(o.Get("code")), blockString(o.Get("file"))
		if file == "" || !down(code, file) {
			continue
		}
		o.Set("fix", closeCommand([]string{"code-forge", "review-file", file, "--block", id, "--run", run}))
		if !slices.Contains(files, file) {
			files = append(files, file)
		}
	}
	if !state || len(files) == 0 {
		return nil
	}
	return files
}

// pendingReviewJSON adds the state and its files to a ready JSON line.
func pendingReviewJSON(out ledger.Object, files []string) ledger.Object {
	if len(files) == 0 {
		return out
	}
	list := make([]any, len(files))
	for i, f := range files {
		list[i] = f
	}
	return append(out, ledger.Field{Key: "state", Value: gatecheck.PendingReviewState}, ledger.Field{Key: "pending_review", Value: list})
}

// pendingReviewLine is the text form's state line.
func pendingReviewLine(id string, files []string) {
	if len(files) == 0 {
		return
	}
	fmt.Printf("block %s %s: the coder's work is complete; only the review of %s is missing because the reviewer could not run; review again (fix lines above), no coder session needed\n", id, gatecheck.PendingReviewState, strings.Join(files, ", "))
}
