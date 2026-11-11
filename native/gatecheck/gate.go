// Package gatecheck evaluates the block review gate (Node's checkBlockReviews)
// without publishing rows. Shapes Go cannot read like Node are rejected by
// Supported, or flagged by Result.Unknown, before the close can write.
package gatecheck

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/ricardov03/code-forge/native/ledger"
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
	Accepted []AcceptedHit
	// Unknown: a value needed a JavaScript engine detail Go does not carry.
	Unknown bool
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

// Supported is false when a row has a shape whose Node reading Go does not
// reproduce byte for byte (a late row or cap row that names no file or
// finding). The close passes only the block's own rows, the rows Node reads;
// it hands the rest over before any write.
func Supported(rows []Row) bool {
	for _, r := range rows {
		event := Text(r["event"])
		if event == "review.late_finding" || event == "review.late_ruling" {
			if Text(r["file"]) == "" || Text(r["finding"]) == "" {
				return false
			}
		}
		if event == "review.cap" {
			if Text(r["file"]) == "" {
				return false
			}
			for _, id := range array(r["open"]) {
				if Text(id) == "" {
					return false
				}
			}
		}
	}
	return true
}

var findingID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)

func Check(run, block string, files []File, rows []Row, key []byte, highPaths []any, unowned []string, noChanges bool) Result {
	return CheckTranscript(run, block, files, rows, key, highPaths, unowned, noChanges, nil)
}

func CheckTranscript(run, block string, files []File, rows []Row, key []byte, highPaths []any, unowned []string, noChanges bool, hits []Blocker) Result {
	return Evaluate(Input{Run: run, Block: block, Files: files, Rows: rows, Key: key, HighPaths: highPaths, Unowned: unowned, NoChanges: noChanges, Hits: hits})
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
