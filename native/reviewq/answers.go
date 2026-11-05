package reviewq

import (
	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/ledger"
)

// MissingAnswers is the unverified, coder-side fast check. The worker still
// verifies the signed rows. Complex clearing dialects stay with Node before
// publication; never guess whether a finding remains open.
func MissingAnswers(rows []ledger.Row, run, block, file, hash, byFile, byHash, ticket string, ids []string) ([]string, bool) {
	mine := []map[string]any{}
	for _, raw := range rows {
		r, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		_, runGiven := r["run"]
		if r["block"] != block || runGiven && r["run"] != run {
			continue
		}
		switch r["event"] {
		case "block.acceptance_amended", "review.cap", "review.reopened", "review.unwaived", "review.state", "review.state_restored":
			return nil, false
		case "review.waived":
			if r["by"] != "human" {
				return nil, false
			}
		}
		mine = append(mine, r)
	}
	named := map[string]string{}
	for _, r := range mine {
		_, hashGiven := r["content_hash"]
		if r["file"] != file || hashGiven && r["content_hash"] != hash {
			continue
		}
		denial := false
		open := map[string]string{}
		switch r["event"] {
		case "review.round":
			denial = r["open_after"] != float64(0)
		case "review.result":
			if gatecheck.NeverReviewed(r) {
				continue
			}
			approved, ok := r["approved"].(bool)
			if !ok {
				return nil, false
			}
			denial = !approved
			for _, v := range answerArray(r["open"]) {
				f, ok := v.(map[string]any)
				if !ok {
					return nil, false
				}
				id, ok := f["id"].(string)
				if !ok {
					return nil, false
				}
				severity, _ := f["severity"].(string)
				if severity == "critical" {
					denial = true
				}
				if severity != "critical" && severity != "warning" && severity != "nit" {
					severity = "unknown"
				}
				rank := map[string]int{"nit": 1, "warning": 2, "critical": 3, "unknown": 4}
				if rank[open[id]] >= rank[severity] {
					continue
				}
				open[id] = severity
			}
		}
		if denial {
			named = map[string]string{}
			if r["content_hash"] == hash {
				named = open
			}
		}
	}
	for _, r := range mine {
		if r["file"] != file {
			continue
		}
		id, _ := r["finding"].(string)
		if r["event"] == "review.waived" && r["by"] == "human" {
			delete(named, id)
		}
		if r["event"] == "review.answered" && r["run"] == run && r["content_hash"] == hash && r["by_file"] == byFile && r["by_hash"] == byHash && r["ticket"] != ticket {
			delete(named, id)
		}
	}
	missing := []string{}
	for _, id := range ids {
		severity := named[id]
		if id == "proof" || severity != "critical" && severity != "warning" && severity != "nit" {
			missing = append(missing, id)
		}
	}
	return missing, true
}
func answerArray(v any) []any { a, _ := v.([]any); return a }
