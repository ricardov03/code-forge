package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/landmerge"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/proof"
	"github.com/ricardov03/code-forge/native/state"
)

// Combine is the local integration stage. Existing land helpers perform the
// merge, named gates, regeneration and red-tip reporting. Lane refs never move.
// Keep CI/review/main promotion outside this stage until their evidence can be
// bound to the generated combined tip rather than to individual lane tips.
func laneCombine(home, run string, ids []string) error {
	// Serialize combines of this run, without holding any lane's lock or
	// changing lane records. Every lane is reverified before its merge.
	return state.WithBlockLock(home, run, "lane-combine", func() error {
		record, err := state.ReadRun(home, run)
		if err != nil {
			return err
		}
		if record.Get("status") != "active" || record.Get("lane") != nil {
			return fmt.Errorf("combine needs an active integration run, separate from a lane")
		}
		cwd := blockString(record.Get("workspace"))
		top, err := gitx.RepoTop(cwd)
		if err != nil {
			return err
		}
		if proof.Real(cwd) != proof.Real(top) {
			return fmt.Errorf("combine workspace must be the repository root")
		}
		branch, err := gitx.Run(top, "symbolic-ref", "--quiet", "--short", "HEAD")
		branch = strings.TrimSpace(branch)
		if err != nil || branch == "main" || branch == "master" {
			return fmt.Errorf("combine on a separate integration branch")
		}
		if status, err := gitx.Run(top, "status", "--porcelain"); err != nil || strings.TrimSpace(status) != "" {
			return fmt.Errorf("combine checkout is dirty or unreadable")
		}
		original, err := gitx.Run(top, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		original = strings.TrimSpace(original)
		lanes := []*laneVerified{}
		seen := map[string]bool{}
		common, err := gitx.Run(top, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil {
			return err
		}
		for _, id := range ids {
			if seen[id] || id == run {
				return fmt.Errorf("duplicate or self lane run %s", id)
			}
			seen[id] = true
			lane, err := laneVerify(home, id)
			if err != nil {
				return fmt.Errorf("%s: %w", id, err)
			}
			other, err := gitx.Run(lane.workspace, "rev-parse", "--path-format=absolute", "--git-common-dir")
			if err != nil || proof.Real(strings.TrimSpace(other)) != proof.Real(strings.TrimSpace(common)) {
				return fmt.Errorf("lane %s belongs to another repository", id)
			}
			if _, err := gitx.Run(top, "merge-base", "--is-ancestor", lane.base, original); err != nil {
				return fmt.Errorf("lane %s base is not in the combine history", id)
			}
			if _, err := gitx.Run(top, "merge-base", "--is-ancestor", lane.head, original); err == nil {
				return fmt.Errorf("lane %s is already combined", id)
			}
			lanes = append(lanes, lane)
		}
		cfg, err := config.Load(top)
		if err != nil {
			return err
		}
		commands := landRegenerateCommands(cfg)
		auto := landmerge.FromConfig(cfg)
		// Use the same sanitized git environment as commit-tree and the
		// generator commit, before creating any trial checkout or merge objects.
		for _, identity := range []string{"GIT_AUTHOR_IDENT", "GIT_COMMITTER_IDENT"} {
			if _, err := gitx.Run(top, "var", identity); err != nil {
				return fmt.Errorf("configure repository-local git identity before combining: git config user.name 'Your Name' and git config user.email 'you@example.com'")
			}
		}
		temporary, err := os.MkdirTemp("", "code-forge-lane-combine-")
		if err != nil {
			return err
		}
		if _, err := gitx.Run(top, "worktree", "add", "--detach", temporary, original); err != nil {
			os.Remove(temporary)
			return err
		}
		// Never force-remove a dirty failed generator: preserve its output and print
		// the path so it is reviewable. Clean trials are removed normally.
		defer func() {
			if _, err := gitx.Run(top, "worktree", "remove", temporary); err != nil {
				fmt.Fprintf(safeStderr, "lane combine: kept trial checkout %s: %v\n", temporary, err)
			}
		}()
		ensureUnionDriver(top)
		tip := original
		merged := []*laneVerified{}
		for _, expected := range lanes {
			lane, err := laneVerify(home, expected.run)
			if err != nil {
				return err
			}
			if lane.head != expected.head {
				return fmt.Errorf("lane %s moved during combine", lane.run)
			}
			b := &landBatchBlock{id: lane.run, head: lane.head, base: lane.base}
			kept, parked, err := landBatchPrecheck(top, tip, []*landBatchBlock{b}, auto, func(_ *landBatchBlock, files []string, _ []landmerge.File) {
				fmt.Fprintf(safeStderr, "lane %s conflicts: %s\n", lane.run, strings.Join(files, ", "))
			})
			if err != nil {
				return err
			}
			if len(parked) > 0 || len(kept) != 1 {
				return fmt.Errorf("lane %s conflicts", lane.run)
			}
			if _, err := gitx.Run(temporary, "checkout", "--detach", b.merged); err != nil {
				return err
			}
			merged = append(merged, lane)
			for _, prior := range merged {
				for _, block := range prior.blocks {
					result, err := laneCombinedTests(temporary, run, block)
					if err != nil {
						// This prefix adds lane.run. An earlier block's failing
						// test identifies the regression, not the new diff's owner.
						if red := landRedTipOf(top, []string{lane.run}, []string{tip, b.merged}, result, err.Error(), lane.run); red != nil {
							landRedRecord(home, run, red, false, nil, safeStderr)
						}
						return err
					}
				}
			}
			tip = b.merged
			fmt.Fprintf(safeStdout, "lane %s merged and named tests passed at %s\n", lane.run, tip)
		}
		if len(commands) > 0 {
			if err := landRegenerateAt(temporary, commands, ids, false); err != nil {
				return err
			}
		}
		// A generator may add objects; apply the same fixed fast-lane size guard.
		final, err := gitx.Run(temporary, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		final = strings.TrimSpace(final)
		big, err := landBigFiles(top, landBigFileDefaultMB*1000000, final, original)
		if err != nil {
			return err
		}
		if len(big) > 0 {
			return fmt.Errorf("combined size exceeds %d MB: %s", landBigFileDefaultMB, big[0].path)
		}
		// Refuse a concurrent checkout/head/dirty change before the only branch write.
		now, err := gitx.Run(top, "rev-parse", "HEAD")
		if err != nil || strings.TrimSpace(now) != original {
			return fmt.Errorf("combine HEAD moved; no trial promoted")
		}
		current, _ := gitx.Run(top, "symbolic-ref", "--quiet", "--short", "HEAD")
		status, err := gitx.Run(top, "status", "--porcelain")
		if err != nil || strings.TrimSpace(status) != "" || strings.TrimSpace(current) != branch {
			return fmt.Errorf("combine checkout changed; no trial promoted")
		}
		if _, err := gitx.Run(top, "merge", "--ff-only", "--", final); err != nil {
			return err
		}
		if _, err := state.WriteSigned(home, run, ledger.Object{{Key: "event", Value: "lane.combine"}, {Key: "lanes", Value: ids}, {Key: "base", Value: original}, {Key: "head", Value: final}}); err != nil {
			return err
		}
		fmt.Fprintf(safeStdout, "combined %d lane(s) at %s; next: review the combined tip, run one CI, then fast-forward main\n", len(lanes), final)
		return nil
	})
}

func laneCombinedTests(cwd, run string, b *laneVerifiedBlock) (landResult, error) {
	result := landResult{}
	cfg, err := config.Load(cwd)
	if err != nil {
		return result, err
	}
	p, err := gates.ResolveErr(cwd, blockMap(cfg["gates"]))
	if err != nil {
		return result, err
	}
	if !laneNamedRunner(p) {
		return result, fmt.Errorf("combine gates require a Go named-test runner")
	}
	module := gatesNamesModule(cwd, p)
	if module == "" && p.Commands["test"] != nil {
		return result, fmt.Errorf("combine gates require a Go module")
	}
	listed, err := gates.List(cwd)
	if err != nil {
		return result, err
	}
	selection, err := gatesNamesSelect(cwd, module, b.id, b.entry, listed, b.files)
	if err != nil {
		return result, err
	}
	timeout := 300000.0
	if n, ok := blockMap(cfg["gates"])["timeout_s"].(float64); ok {
		timeout = n * 1000
	}
	results, ok, err := gatesNamesRun(context.Background(), p, selection, cwd, run, timeout, gates.Scope{Kind: "related", Files: b.files, Reason: gatesNamesReason})
	result.data = ledger.Object{{Key: "results", Value: results}}
	for _, value := range results {
		row := blockObject(value)
		result.output += blockString(row.Get("stdout")) + blockString(row.Get("stderr"))
	}
	if err != nil {
		return result, err
	}
	if !ok {
		return result, fmt.Errorf("block %s named tests failed:\n%s", b.id, result.output)
	}
	fmt.Fprintf(safeStdout, "block %s: %s\n", b.id, strings.Join(selection.Names, ", "))
	if dirty, err := gitx.Run(filepath.Clean(cwd), "status", "--porcelain"); err != nil || strings.TrimSpace(dirty) != "" {
		return result, fmt.Errorf("named gates changed the trial checkout")
	}
	return result, nil
}
