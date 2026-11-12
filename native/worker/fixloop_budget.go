package worker

// B496 step 2: the block lock key (engine.mjs blockLockKey) and the block's
// review budget (engine.mjs recordBlockBudget): the forecast over the
// block's changed owned files (gates/scope.mjs computeFileSet's `all`,
// filtered by ownsFile) plus the ticket's file, each tiered by its path
// floor, as one review.budget row (+ review.over_budget). FixLoopRound writes
// it once per block: the check-then-write runs under the block lock.

import (
	"os"
	"strings"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
	"github.com/ricardov03/code-forge/native/review"
)

// BlockLockKey is engine.mjs blockLockKey: the budget row and each file's
// L3 rung are taken under it (filelock shares Node's lock files).
func BlockLockKey(runID, block string) string { return "block:" + runID + "\x00" + block }

// BudgetRequest is recordBlockBudget's input. Base nil is Node's null.
type BudgetRequest struct {
	ContentRoot, Block, File string
	Base                     *string
	Owned                    []any
	OwnedKinds               ledger.Object
	Cfg                      map[string]any
}

// scopeEnv is scope.mjs gitEnv: inherited GIT_* removed, no system config,
// global config /dev/null.
func scopeEnv() []string {
	env := []string{}
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "GIT_") {
			env = append(env, v)
		}
	}
	return append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
}

func splitNul(text string) []string {
	out := []string{}
	for _, entry := range strings.Split(text, "\x00") {
		if entry != "" {
			out = append(out, entry)
		}
	}
	return out
}

// computeFileSet is scope.mjs computeFileSet's `all`: tracked changes since
// base union untracked files, sorted. ok false is Node's throw (a git
// failure, or the argv guard on a base starting with `-`); a name that is
// not UTF-8 is ErrUnsupported (Node decodes it with replacements).
func computeFileSet(cwd, base string) (all []string, ok bool, err error) {
	if base == "" || strings.HasPrefix(base, "-") {
		return nil, false, nil
	}
	env := scopeEnv()
	diff, derr := gitx.WithEnv(cwd, env, "diff", "--name-only", "-z", base)
	status, serr := gitx.WithEnv(cwd, env, "status", "--porcelain", "-z", "--untracked-files=all")
	if derr != nil || serr != nil {
		return nil, false, nil
	}
	if !utf8.ValidString(diff) || !utf8.ValidString(status) {
		return nil, false, unsupported("a changed file name that is not UTF-8")
	}
	seen := map[string]bool{}
	add := func(f string) {
		if !seen[f] {
			seen[f] = true
			all = append(all, f)
		}
	}
	for _, f := range splitNul(diff) {
		add(f)
	}
	for _, entry := range splitNul(status) {
		if strings.HasPrefix(entry, "?? ") {
			add(entry[3:])
		}
	}
	gitx.SortStrings(all)
	return all, true, nil
}

// BlockBudgetRows is recordBlockBudget without the writes: the rows to write
// in order, or nil when Node writes none (git failed on the base).
func BlockBudgetRows(r BudgetRequest) ([]ledger.Object, error) {
	changed := []string{}
	if r.Base != nil && len(r.Owned) > 0 {
		all, ok, err := computeFileSet(r.ContentRoot, *r.Base)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, nil
		}
		for _, f := range all {
			if owned.OwnsFile(r.Owned, r.OwnedKinds, f) {
				changed = append(changed, f)
			}
		}
	}
	names := []string{}
	seen := map[string]bool{}
	for _, f := range append(changed, r.File) {
		if !seen[f] {
			seen[f] = true
			names = append(names, f)
		}
	}
	gitx.SortStrings(names)
	files := []review.BudgetFile{}
	for _, f := range names {
		files = append(files, review.BudgetFile{File: f, Tier: review.TierOf(review.RulesRisk(f, 0, 0, r.Cfg))})
	}
	return review.BudgetRows(files, r.Cfg, r.Block), nil
}

// hasBudgetRow: a review.budget row of the block among the run's rows.
func hasBudgetRow(rows []ledger.Object, block string) bool {
	for _, r := range rows {
		if r.Get("event") == "review.budget" && r.Get("block") == block {
			return true
		}
	}
	return false
}

// writeBudget is planAndRecord's writes: best effort, the first failure ends them.
func writeBudget(rows []ledger.Object, write func(ledger.Object) error) {
	for _, row := range rows {
		if write(row) != nil {
			return
		}
	}
}
