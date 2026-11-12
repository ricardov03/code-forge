package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ricardov03/code-forge/native/state"
)

// B460 (phase 1): `code-forge worker` in Go (src/cli/worker.mjs). Go answers
// --help, every usage error, and the refusals Node makes before the worker
// boots (a bad or unknown run id, a run that has ended). Serving the queue
// (boot lock, registry, daemon, pins, ticket reviews, signed rows, stop) keeps
// its hand-over to Node, so a ticket is never lost or reviewed twice: the Node
// worker is still the only process that takes tickets.
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
	return nil, hWorkerServe
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
	code := plan(o)
	if code != 0 {
		var message any
		if o.last != "" {
			message = strings.TrimSpace(o.last)
		}
		nativeVerbFailure("worker", "", args, code, message, before)
	}
	return code
}
