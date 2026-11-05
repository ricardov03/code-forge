// Package gatecheck evaluates the plain agent review gate without publishing rows.
// Unsupported ledger dialects are rejected by Supported before the close can write.
package gatecheck

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
	"github.com/ricardov03/code-forge/native/sign"
)

type Row = map[string]any
type File struct{ File, Hash string }
type Blocker struct{ Code, File, Finding, Detail string }
type Waived struct {
	File, Hash         string
	Findings, Answered []string
	By                 any
}
type Result struct {
	Blockers []Blocker
	Waived   []Waived
}

func Text(v any) string { s, _ := v.(string); return s }
func array(v any) []any { a, _ := v.([]any); return a }
func Verified(row Row, key []byte) bool {
	result, err := sign.VerifyRow(row, key)
	return err == nil && result.OK
}
func NeverReviewed(r Row) bool {
	return slices.Contains([]string{"stale", "unavailable", "answers"}, Text(r["status"])) || r["status"] == "refused" && r["reason"] == "other-run"
}

// The old result-file dialect, automatic restarts, agent decisions and accepted
// transcript hits remain with Node. Checking the entire run also protects sibling
// ownership and done-file counts from partially implemented clearing rules.
func Supported(rows []Row) bool {
	for _, r := range rows {
		event := Text(r["event"])
		if event == "rule_break" {
			for _, name := range []string{"id", "rule"} {
				if r[name] != nil {
					if _, ok := r[name].(string); !ok {
						return false
					}
				}
			}
		}
		if event == "review.late_finding" || event == "review.late_ruling" {
			if Text(r["file"]) == "" || Text(r["finding"]) == "" {
				return false
			}
		}
		if slices.Contains([]string{"review.cap", "review.reopened", "review.state", "review.state_restored", "review.unwaived", "rule_break.accepted", "block.waivers_acked"}, event) {
			return false
		}
		if event == "review.waived" && r["by"] != "human" {
			return false
		}
		if event == "review.result" && !NeverReviewed(r) {
			if _, ok := r["approved"].(bool); !ok {
				return false
			}
		}
	}
	return true
}

var findingID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)

type finding struct {
	id       string
	severity any
}
type denial struct {
	event string
	open  []finding
}

func denialOf(r Row) *denial {
	event := Text(r["event"])
	if event == "review.round" {
		if r["open_after"] == float64(0) {
			return nil
		}
		return &denial{event, nil}
	}
	if event != "review.result" || NeverReviewed(r) {
		return nil
	}
	open := []finding{}
	rank := map[string]int{"nit": 0, "warning": 1, "critical": 2, "unknown": 3}
	for i, value := range array(r["open"]) {
		f, _ := value.(map[string]any)
		id, severity := Text(f["id"]), Text(f["severity"])
		if !findingID.MatchString(id) {
			id = fmt.Sprintf("X%d", i+1)
		}
		if _, ok := rank[severity]; !ok {
			severity = "unknown"
		}
		at := slices.IndexFunc(open, func(f finding) bool { return f.id == id })
		if at < 0 {
			open = append(open, finding{id, severity})
		} else if rank[severity] > rank[Text(open[at].severity)] {
			open[at].severity = severity
		}
	}
	if r["approved"] == true && !slices.ContainsFunc(open, func(f finding) bool { return f.severity == "critical" }) {
		return nil
	}
	return &denial{event, open}
}

func Check(run, block string, files []File, rows []Row, key []byte, highPaths []any, unowned []string, noChanges bool) Result {
	return CheckTranscript(run, block, files, rows, key, highPaths, unowned, noChanges, nil)
}

func CheckTranscript(run, block string, files []File, rows []Row, key []byte, highPaths []any, unowned []string, noChanges bool, hits []Blocker) Result {
	result := Result{Blockers: []Blocker{}, Waived: []Waived{}}
	add := func(code, file, finding, detail string) {
		result.Blockers = append(result.Blockers, Blocker{code, file, finding, detail})
	}
	for _, file := range unowned {
		add("unowned_change", file, "", file+" changed but no block of the run owns it — block claim it, or put it back")
	}
	if noChanges {
		add("no_changes", "", "", "block "+block+" changed no owned file and has no reviews")
	}
	amended := -1
	for i, row := range rows {
		if row["event"] == "block.acceptance_amended" && row["run"] == run && row["block"] == block && Verified(row, key) {
			amended = i
		}
	}
	mine := []Row{}
	for i, row := range rows {
		if row["block"] == block && (row["run"] == nil || row["run"] == run) && !(i < amended && strings.HasPrefix(Text(row["event"]), "review.")) {
			mine = append(mine, row)
		}
	}
	current := map[string]string{}
	for _, f := range files {
		current[f.File] = f.Hash
	}
	waiver := func(file, id string, severity any) string {
		for _, r := range mine {
			if r["event"] == "review.waived" && r["file"] == file && r["finding"] == id && r["by"] == "human" && Verified(r, key) {
				return "human"
			}
		}
		if id == "proof" || !(severity == nil || severity == "critical" || severity == "warning" || severity == "nit") {
			return ""
		}
		for _, r := range mine {
			by := Text(r["by_file"])
			if r["event"] == "review.answered" && r["run"] == run && r["file"] == file && r["finding"] == id && by != "" && by != file && Verified(r, key) {
				source, ok := current[file]
				target, has := current[by]
				if ok && has && r["content_hash"] == source && r["by_hash"] == target {
					return "answer"
				}
			}
		}
		return ""
	}
	type staleFile struct {
		file string
		d    *denial
	}
	stale := []staleFile{}
	for _, f := range files {
		approvals, signed, lastApproval, lastDenial := 0, false, -1, -1
		var latest *denial
		var latestRow Row
		realRound := false
		for i, r := range mine {
			if r["file"] != f.File {
				continue
			}
			if r["event"] == "review.round" && r["content_hash"] == f.Hash && Verified(r, key) {
				realRound = true
			}
			if r["event"] == "review.approved" {
				if r["content_hash"] == f.Hash {
					approvals++
					if Verified(r, key) {
						signed = true
						lastApproval = i
					}
				}
				continue
			}
			if r["content_hash"] != nil && r["content_hash"] != f.Hash {
				continue
			}
			if d := denialOf(r); d != nil {
				latest = d
				latestRow = r
				lastDenial = i
			}
		}
		if approvals == 0 {
			var all *Waived
			if realRound && latest != nil && latestRow["content_hash"] == f.Hash && Verified(latestRow, key) && len(latest.open) > 0 {
				w := Waived{File: f.File, Hash: f.Hash, Findings: []string{}, Answered: []string{}}
				ok := true
				for _, finding := range latest.open {
					by := waiver(f.File, finding.id, finding.severity)
					if by == "" {
						ok = false
						break
					}
					if by == "answer" {
						w.Answered = append(w.Answered, finding.id)
					} else {
						w.Findings = append(w.Findings, finding.id)
						w.By = "human"
					}
				}
				if ok {
					gitx.SortStrings(w.Findings)
					gitx.SortStrings(w.Answered)
					all = &w
				}
			}
			if all != nil {
				result.Waived = append(result.Waived, *all)
			} else {
				detail := "no review.approved row for " + f.File + " at its current content"
				if amended >= 0 {
					detail += " (acceptance amended)"
				}
				add("unreviewed", f.File, "", detail)
			}
		} else if !signed {
			add("unsigned", f.File, "", "the review.approved row for "+f.File+" has no valid MAC")
		}
		if latest != nil && (signed && lastDenial > lastApproval || !signed && len(latest.open) > 0) {
			stale = append(stale, staleFile{f.File, latest})
		}
	}
	for _, r := range mine {
		if r["event"] == "rule_break" {
			id := r["id"]
			if id == nil {
				id = r["rule"]
			}
			if id == nil {
				id = "unnamed"
			}
			add("rule_break", "", "", "rule_break row: "+fmt.Sprint(id))
		}
	}
	result.Blockers = append(result.Blockers, hits...)
	for _, s := range stale {
		if len(s.d.open) == 0 {
			add("not_approved", s.file, "", fmt.Sprintf("a later review of %s's current content did not approve it (%s) and names no finding; review it again", s.file, s.d.event))
			continue
		}
		for _, f := range s.d.open {
			if waiver(s.file, f.id, f.severity) == "" {
				add("not_approved", s.file, f.id, fmt.Sprintf("the latest review of %s's current content left %s open and it is not waived", s.file, f.id))
			}
		}
	}
	for _, r := range mine {
		if r["event"] == "review.late_finding" {
			file, id := Text(r["file"]), Text(r["finding"])
			ruled := false
			if r["severity"] == "warning" || r["severity"] == "nit" {
				for _, ruling := range mine {
					if ruling["event"] == "review.late_ruling" && ruling["file"] == file && ruling["finding"] == id && ruling["ruling"] == "nit" && Verified(ruling, key) {
						ruled = true
					}
				}
			}
			severity := r["severity"]
			if _, ok := severity.(string); !ok {
				severity = "unknown"
			}
			if !ruled && waiver(file, id, severity) == "" {
				add("late_unruled", file, id, fmt.Sprintf("late finding %s in %s has no ruling", id, file))
			}
		}
	}
	for _, f := range files {
		if f.Hash == gitx.DeletedHash {
			continue
		}
		high := owned.OwnsFile(highPaths, nil, f.File)
		for _, r := range mine {
			if r["file"] == f.File {
				risk, _ := r["risk"].(float64)
				high = high || !Verified(r, key) || risk >= 2 || r["security_sensitive"] == true
			}
		}
		if !high {
			continue
		}
		covered := false
		for _, r := range mine {
			if r["event"] == "proof" && r["run"] == run && r["step"] == "red-green" && r["proven"] == true && r["red_kind"] == "assertion" && Verified(r, key) && slices.Contains(array(r["covers"]), any(f.File)) {
				covered = true
			}
		}
		if !covered && waiver(f.File, "proof", "unknown") == "" {
			add("unproven", f.File, "", f.File+" is high tier and has no proven red→green row covering it")
		}
	}
	if len(result.Blockers) > 0 {
		result.Waived = []Waived{}
	}
	return result
}

func Reason(blockers []Blocker) string {
	reasons := []string{}
	ids := map[string]bool{}
	for _, b := range blockers {
		if b.Code == "rule_break" && strings.HasPrefix(b.Detail, "transcript line ") {
			parts := strings.SplitN(b.Detail, ": ", 2)
			if len(parts) == 2 {
				if ids[parts[1]] {
					continue
				}
				ids[parts[1]] = true
				if len(ids) <= 5 {
					reasons = append(reasons, "rule_break "+b.Detail)
				}
				continue
			}
		}
		text := b.Code
		if b.File != "" {
			text += " " + b.File
		}
		if b.Finding != "" {
			text += " " + b.Finding
		}
		reasons = append(reasons, text)
	}
	if len(ids) > 5 {
		reasons = append(reasons, fmt.Sprintf("+%d more rule_break", len(ids)-5))
	}
	return strings.Join(reasons, "; ")
}

func (w Waived) Object(status bool) ledger.Object {
	o := ledger.Object{{Key: "file", Value: w.File}, {Key: "content_hash", Value: w.Hash}}
	if status {
		o.Set("status", "reviewed_by_waiver")
	}
	o.Set("findings", w.Findings)
	o.Set("by", w.By)
	if len(w.Answered) > 0 {
		o.Set("answered", w.Answered)
	}
	return o
}
