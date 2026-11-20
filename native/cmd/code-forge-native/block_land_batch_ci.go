package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/ledger"
)

// B910: with land.require_ci: github, GitHub CI already ran the full suite on
// a block's tip, so the batch skips the block's local test gate when the sha
// it lands is the sha CI proved. A rebase that made a new sha (main moved, or
// the block sits after another block of the batch) pushes the new sha to a
// new CI branch (never a force push) and waits for green CI on it first. The
// fast gates (lint, types, format and the other non-test gates) still run,
// and so does the batch merge gate.

// landBatchCITip makes sure green CI proved the block's rebased tip. It
// returns a refusal, or nil when the tip is proved (or the gate is off).
func landBatchCITip(flags map[string]any, run string, requested []string, main, base string, json bool, record ledger.Object, block *landBatchBlock) error {
	if block.ci == "" {
		return nil
	}
	out := io.Writer(safeStdout)
	if json {
		out = safeStderr
	}
	sha := strings.TrimSpace(block.head)
	if sha != block.ci {
		fmt.Fprintf(out, "ci: block %s rebased to %s; CI proved %s, so the new tip needs its own green CI (B910)\n", block.id, landCIShort(sha), landCIShort(block.ci))
		cfg, _ := blockSnapshot(record)
		notes, refusal, next := landCIGate(cfg, main, block.id, sha, base, landCIResume(requested, run, flags), out)
		if refusal != "" {
			if next != "" {
				refusal += "\nnext: " + next
			}
			return fmt.Errorf("%s", refusal)
		}
		for _, line := range notes {
			fmt.Fprintln(out, line)
		}
		block.ci = sha
	}
	fmt.Fprintf(out, "gates: block %s tip %s has green CI; local test gate skipped (B910)\n", block.id, landCIShort(sha))
	return nil
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
