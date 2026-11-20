package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ricardov03/code-forge/native/autopilot"
	"github.com/ricardov03/code-forge/native/state"
)

// B649: Node cannot read an until-done autopilot grant. Its grantState does
// Date.parse(until), and Date.parse(null) is NaN, so Node reads the grant as
// active forever (and agent-waiver.mjs reads it as never active). Every Go
// hand-over goes through refuseUnported(); before the refusal, the guard looks
// at each run the command names (--run <id> or --run=<id>; review-file with
// no --run reads the queue's worker run as Node does) and refuses, exit 1,
// while one of them holds a live until-done grant. Nothing is written and
// Node never starts.

// hudWorkerRuns lists the runs of the workers registered for the repository
// at cwd (review-file with no --run uses its live worker's run); a test seam.
var hudWorkerRuns = hudRegisteredWorkerRuns

// hudRunIDs are the run ids a hand-over's arguments name, in order, once each.
func hudRunIDs(args []string) []string {
	ids := []string{}
	add := func(id string) {
		for _, have := range ids {
			if have == id {
				return
			}
		}
		ids = append(ids, id)
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if arg == "--run" && i+1 < len(args) {
			add(args[i+1])
			i++
		} else if strings.HasPrefix(arg, "--run=") {
			add(strings.TrimPrefix(arg, "--run="))
		}
	}
	if len(ids) == 0 && len(args) > 0 && args[0] == "review-file" {
		for _, id := range hudWorkerRuns() {
			add(id)
		}
	}
	return ids
}

// hudRegisteredWorkerRuns reads the worker entries of the queue at cwd's run
// root (the main tree, or a linked worktree with its own queue, B1005)
// (the registry and the legacy worker.json) without probing liveness: a
// stale entry can only make the guard stricter, never let Node read a grant.
func hudRegisteredWorkerRuns() []string {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	// B1005: a linked worktree with its own queue holds its own workers
	root, _, ok := reviewRunRoot(context.Background(), cwd, "")
	if !ok || root == "" {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(root, ".code-forge", "workers", "*.json"))
	files = append(files, filepath.Join(root, ".code-forge", "queue", "worker.json"))
	runs := []string{}
	for _, file := range files {
		if w, _ := reviewWaitJSON(file); w != nil {
			if run, isString := w.Get("run").(string); isString && run != "" {
				runs = append(runs, run)
			}
		}
	}
	return runs
}

// untilDoneHandoverRefusal is the refusal for a hand-over that would let Node
// read a live until-done grant, or "" when the hand-over may go ahead.
func untilDoneHandoverRefusal(args []string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	for _, run := range hudRunIDs(args) {
		record, err := state.ReadRun(home, run)
		if err != nil {
			continue // no record, a bad id or an unreadable one: the plain refusal follows
		}
		grant := autopilot.LiveUntilDone(record)
		if grant == nil {
			continue
		}
		name, sub := handoverCommand(args)
		if sub != "" && sub != "unknown" {
			name += " " + sub
		}
		id, _ := grant.Get("grant_id").(string)
		return fmt.Sprintf("code-forge %s: run %s has an until-done autopilot grant (%s); this case is not supported yet, and an until-done grant is never passed on, so nothing was run (%s). Run it again after the grant ends (code-forge autopilot status --run %s), or stop the grant first (code-forge autopilot stop --run %s).\n",
			name, run, id, handoverReasons[hUntilDoneGrant], run, run)
	}
	return ""
}
