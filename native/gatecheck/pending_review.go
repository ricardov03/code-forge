package gatecheck

// PendingReviewState (B625) is the report state of a block whose coder work is
// complete and whose only missing piece is a review the reviewer could not
// run: a timeout, a provider that is down, a spawn or worker error. Its fix is
// a review, never a new coder session.
const PendingReviewState = "COMPLETE_PENDING_REVIEW"

// ReviewerDown says whether the review of file failed only on reviewer
// infrastructure. It reads the signed review.result rows of run and block for
// file (at hash when hash is not empty) that are a review outcome: status
// "reviewed" (a review ran) or "unavailable" (the reviewer could not run). The
// file is reviewer-down when the latest of them is "unavailable"; reason is
// that row's reason ("" when it has none). A review with findings
// (reviewed, not approved) or an approval as the latest outcome is not.
func ReviewerDown(rows []Row, run, block, file, hash string, key []byte) (reason string, down bool) {
	for _, r := range rows {
		if r["event"] != "review.result" || r["run"] != run || r["block"] != block || r["file"] != file {
			continue
		}
		if hash != "" && r["content_hash"] != hash {
			continue
		}
		status := Text(r["status"])
		if status != "reviewed" && status != "unavailable" {
			continue
		}
		if !Verified(r, key) {
			continue
		}
		down = status == "unavailable"
		reason = ""
		if down {
			reason = Text(r["reason"])
		}
	}
	return reason, down
}
