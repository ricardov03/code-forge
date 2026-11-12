package worker

// B497: the packet's facts slot (engine.mjs acceptanceExcerpt and factsFor).

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/ricardov03/code-forge/native/contextpack"
	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/review"
)

// AcceptanceUnavailable is engine.mjs ACCEPTANCE_UNAVAILABLE.
const AcceptanceUnavailable = "(acceptance unavailable)"

const acceptanceHead = "Acceptance clauses (what the change must do):"

// jsSpace is JavaScript's \s class (and String.prototype.trim's set).
const jsSpace = "\t\n\v\f\r \u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000\ufeff"

var jsSpaceRun = regexp.MustCompile(`[` + jsSpace + `]+`)

// AcceptanceExcerpt is engine.mjs acceptanceExcerpt: one `- <clause>` line
// per clause (whitespace folded, trimmed, redacted), cut at an eighth of the
// full_in budget in bytes with a marker naming the clauses left out.
func AcceptanceExcerpt(acceptance any, cfg map[string]any, secrets []string) string {
	clauses := []string{}
	if list, ok := acceptance.([]any); ok {
		for _, c := range list {
			text := ""
			if o, ok := c.(ledger.Object); ok {
				if s, ok := o.Get("clause").(string); ok {
					text = gates.Mask(strings.Trim(jsSpaceRun.ReplaceAllString(s, " "), jsSpace), secrets)
				}
			} else if m, ok := c.(map[string]any); ok {
				if s, ok := m["clause"].(string); ok {
					text = gates.Mask(strings.Trim(jsSpaceRun.ReplaceAllString(s, " "), jsSpace), secrets)
				}
			}
			if text != "" {
				clauses = append(clauses, text)
			}
		}
	}
	if len(clauses) == 0 {
		return acceptanceHead + "\n" + AcceptanceUnavailable
	}
	maxBytes := review.BudgetFor(cfg, "full_in") * 4 / 8
	if maxBytes < 256 {
		maxBytes = 256
	}
	lines := []string{acceptanceHead}
	used := len(acceptanceHead) + 1
	for _, clause := range clauses {
		line := "- " + clause
		size := len(line) + 1
		if used+size > maxBytes {
			if len(lines) == 1 {
				cut := maxBytes - used - 4
				if cut < 0 {
					cut = 0
				}
				if cut > len(line) {
					cut = len(line)
				}
				lines = append(lines, contextpack.DecodeUTF8([]byte(line[:cut]))+" …")
			}
			if left := len(clauses) - (len(lines) - 1); left > 0 {
				lines = append(lines, "(acceptance truncated: "+strconv.Itoa(left)+" more clause(s) over the packet budget)")
			}
			break
		}
		lines = append(lines, line)
		used += size
	}
	return strings.Join(lines, "\n")
}

// WorktreeEntry is engine.mjs WorktreeEntry.
type WorktreeEntry struct{ Path, Branch, BaseSHA string }

// FactsFor is engine.mjs factsFor: the acceptance excerpt, plus the line
// naming the block's worktree when it has one.
func FactsFor(acceptance any, tree *WorktreeEntry, cfg map[string]any, secrets []string) string {
	excerpt := AcceptanceExcerpt(acceptance, cfg, secrets)
	if tree == nil {
		return excerpt
	}
	return excerpt + "\nWorktree: the file is reviewed as it is in the block's worktree " + gates.Mask(tree.Path, secrets) +
		" (branch " + gates.Mask(tree.Branch, secrets) + "), against the block base."
}
