package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ricardov03/code-forge/native/state"
	"github.com/ricardov03/code-forge/native/worker"
)

// B460 (phase 1): `code-forge worker` in Go (src/cli/worker.mjs). Go answers
// --help, every usage error, and the refusals Node makes before the worker
// boots (a bad or unknown run id, a run that has ended).
// B489: Go also serves the queue (boot lock, registry, daemon, pins, ticket
// reviews, signed rows, idle exit, stop) when every part it reaches is
// ported; see worker_serve.go for the cases that still go to Node and the
// ticket split rule that keeps a ticket from being lost or reviewed twice.
func init() { routes["worker"] = runWorkerVerb }

const workerUsage = "usage: code-forge worker --run <id> [--cwd <dir>] [--poll-ms <n>] [--once]\n"

func workerHandover(args []string, reason handoverReason) int {
	code, err := handover(append([]string{"worker"}, args...), reason)
	if err != nil {
		fmt.Fprintln(os.Stderr, "code-forge-native:", err)
	}
	return code
}

// workerPlan decides the invocation without writing anything. A nil plan
// means Node serves it, for the returned reason.
func workerPlan(home string, args []string) (func(o *runOut) int, handoverReason) {
	parsed, bad := cliParseFlags(args, cliSpec{values: []string{"run", "cwd", "poll-ms"}, booleans: []string{"once"}})
	if bad != "" {
		return func(o *runOut) int { o.err("worker: " + bad + "\n" + workerUsage); return 2 }, 0
	}
	runID, hasRun := parsed.values["run"]
	if len(parsed.positionals) > 0 || !hasRun {
		return func(o *runOut) int { o.err(workerUsage); return 2 }, 0
	}
	if raw, ok := parsed.values["poll-ms"]; ok {
		if _, bad := cliIntFlag(raw, "poll-ms"); bad != "" {
			return func(o *runOut) int { o.err("worker: " + bad + "\n"); return 2 }, 0
		}
	}
	record, err := state.ReadRun(home, runID)
	if err != nil {
		var stateErr *state.Error
		if errors.As(err, &stateErr) {
			// bad-run-id and no-run: Node's StateError, not `usage`, so exit 1.
			return func(o *runOut) int { o.err("worker: " + stateErr.Message + "\n"); return 1 }, 0
		}
		return nil, hWorkerRecord // unreadable or not JSON: Node reports its own error
	}
	if status, _ := record.Get("status").(string); status != "active" {
		return func(o *runOut) int { o.err("worker: run " + runID + " has ended\n"); return 1 }, 0
	}
	// B489: serve in Go; workerServe decides what still goes to Node.
	return func(*runOut) int { return 0 }, hWorkerServe
}

func runWorkerVerb(args []string) int {
	// The Node router answers help before breadcrumbs and handlers.
	if len(args) > 0 && cliHelp(args[0]) {
		fmt.Fprint(os.Stdout, workerUsage)
		return 0
	}
	home := os.Getenv("HOME")
	if home == "" || !filepath.IsAbs(home) {
		return workerHandover(args, hWorkerHome)
	}
	plan, reason := workerPlan(home, args)
	if plan == nil {
		return workerHandover(args, reason)
	}
	before := gatesBreadcrumbName("worker")
	o := &runOut{}
	var code int
	if reason == hWorkerServe {
		parsed, _ := cliParseFlags(args, cliSpec{values: []string{"run", "cwd", "poll-ms"}, booleans: []string{"once"}})
		a := workerServeArgs{runID: parsed.values["run"], pollMs: worker.DefaultPollMs, once: parsed.booleans["once"]}
		a.cwd, a.hasCwd = parsed.values["cwd"]
		if raw, ok := parsed.values["poll-ms"]; ok {
			n, _ := cliIntFlag(raw, "poll-ms")
			a.pollMs = int(n)
		}
		spawnWarnBefore = before
		var handTo handoverReason
		code, handTo = workerServe(home, args, a, o)
		if handTo != 0 {
			code := workerHandover(args, handTo)
			if undo := workerHandOverUndo; undo != nil {
				workerHandOverUndo = nil
				undo()
			}
			return code
		}
	} else {
		code = plan(o)
	}
	if code != 0 {
		var message any
		if o.last != "" {
			message = strings.TrimSpace(o.last)
		}
		nativeVerbFailure("worker", "", args, code, message, before)
	}
	return code
}
