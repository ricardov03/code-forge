package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/humancheck"
	"github.com/ricardov03/code-forge/native/proof"
	"github.com/ricardov03/code-forge/native/state"
)

// B910: with land.require_ci: github, GitHub CI already ran the full suite on
// a block's tip, so the batch skips the block's local test gate when the sha
// it lands is the sha CI proved. A rebase that made a new sha (main moved, or
// the block sits after another block of the batch) pushes the new sha to a
// new CI branch (never a force push) and waits for green CI on it first. The
// fast gates (lint, types, format and the other non-test gates) still run,
// and so does the batch merge gate.
//
// B803: every CI wait runs with no lock held. landBatchCIPre reads and waits
// for each block's CI before the batch takes its block locks; a block whose
// rebase inside the lock made a new sha ends the round (the blocks before it
// land), and the next round waits for CI on that sha, again with no lock.

// landBatchMaxRounds caps the CI rounds of one land: each round lands at
// least one block unless main moved meanwhile.
const landBatchMaxRounds = 10

// landBatch lands closed blocks as one batch (see landBatchRounds).
func landBatch(flags map[string]any, home, run string, ids []string) int {
	return landBatchRounds(flags, home, run, ids, 0, nil)
}

// landBatchNextRound lands the blocks that wait for CI on a rebased tip;
// earlier are the blocks the rounds before landed (pushed after the last).
func landBatchNextRound(flags map[string]any, home, run string, ids []string, round int, earlier []string) int {
	if round+1 >= landBatchMaxRounds {
		fmt.Fprintf(safeStderr, "block %s land stopped at ci: main moved in each of %d CI rounds; land again (B803)\n", ids[0], landBatchMaxRounds)
		if pushed, err := landBatchPush(flags, home, run, earlier); err != nil {
			fmt.Fprintf(safeStderr, "block %s land stopped at push: %s\n", pushed, strings.TrimSpace(err.Error()))
		}
		return 1
	}
	return landBatchRounds(flags, home, run, ids, round+1, earlier)
}

// landBatchCIOut is where the CI lines go: stderr under --json.
func landBatchCIOut(json bool) io.Writer {
	if json {
		return safeStderr
	}
	return safeStdout
}

// landBatchCIPre runs the land.require_ci gate on each block's current tip
// with no lock held and returns the proved sha per block. It stops (without
// error) at the first block the locked validation will refuse, so that
// refusal keeps its order. A CI refusal returns the block id and the reason.
func landBatchCIPre(flags map[string]any, home, run string, ids []string, json bool) (map[string]string, string, error) {
	record, err := state.ReadRun(home, run)
	if err != nil || record.Get("status") != "active" {
		return nil, "", nil
	}
	cfg, _ := blockSnapshot(record)
	if provider, _ := landCIRequired(cfg); provider == "" {
		return nil, "", nil
	}
	workspace := blockString(record.Get("workspace"))
	main, err := gitx.RepoTop(workspace)
	if err != nil {
		return nil, "", nil
	}
	blocks := blockObject(record.Get("blocks"))
	proved, out := map[string]string{}, landBatchCIOut(json)
	for _, id := range ids {
		entry := blockObject(blocks.Get(id))
		if _, dup := proved[id]; dup || entry == nil || entry.Get("landed") != nil || entry.Get("status") != "closed" && entry.Get("status") != humancheck.DeferredStatus {
			break
		}
		tree, err := proof.BlockTree(workspace, entry)
		if err != nil || tree == nil || gatesCleanTree(tree.Path) == "" {
			break
		}
		head, err := gitx.Run(tree.Path, "rev-parse", "HEAD")
		if err != nil {
			break
		}
		sha := strings.TrimSpace(head)
		notes, refusal, next := landCIGate(cfg, main, id, sha, blockString(entry.Get("base_branch")), landCIResume(ids, run, flags), out)
		if refusal != "" {
			if next != "" {
				refusal += "\nnext: " + next
			}
			return nil, id, fmt.Errorf("%s", refusal)
		}
		for _, line := range notes {
			fmt.Fprintln(out, line)
		}
		proved[id] = sha
	}
	return proved, "", nil
}

// landBatchCITip says whether batch[i], just rebased, must wait for CI on its
// new tip (true: the round stops before it). A tip CI proved skips the local
// test gate (B910).
func landBatchCITip(json bool, batch []*landBatchBlock, i int) bool {
	block := batch[i]
	if block.ci == "" {
		return false
	}
	out := landBatchCIOut(json)
	sha := strings.TrimSpace(block.head)
	if sha != block.ci {
		fmt.Fprintf(out, "ci: block %s rebased to %s; CI proved %s, so the new tip needs its own green CI (B910)\n", block.id, landCIShort(sha), landCIShort(block.ci))
		if i > 0 {
			fmt.Fprintf(out, "land: blocks before %s land first; %s waits for CI with no land lock held (B803)\n", block.id, block.id)
		} else {
			fmt.Fprintf(out, "land: %s waits for CI with no land lock held (B803)\n", block.id)
		}
		return true
	}
	fmt.Fprintf(out, "gates: block %s tip %s has green CI; local test gate skipped (B910)\n", block.id, landCIShort(sha))
	return false
}

// landBatchNoTest is the gate commands without the test gate.
func landBatchNoTest(commands map[string]*gates.Command) map[string]*gates.Command {
	kept := map[string]*gates.Command{}
	for name, command := range commands {
		if name != "test" {
			kept[name] = command
		}
	}
	return kept
}
