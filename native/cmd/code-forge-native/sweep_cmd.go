package main

import (
	"fmt"
	"io"
)

const sweepUsage = "usage: code-forge sweep [--apply] [--json]\nDry run by default: list stale workers, dead-run registry children and marked orphans with RSS and total MB.\n--apply stops proven stale processes with SIGTERM, then SIGKILL after a grace period.\nZombies are reported by parent only; neither they nor their parents are signalled. Live engines have no memory limit.\n"

func init() {
	routes["sweep"] = smallVerb{name: "sweep", usage: sweepUsage, prepare: prepareSweep}.run
}

// prepareSweep is src/cli/sweep.mjs up to its process scan: usage and help
// answer here. The scan (sweepProcesses in src/util/reaper.mjs with the idle
// worker check) stays in Node.
func prepareSweep(args []string) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
	flags, positionals, problem := nodeFlags(args, []string{"apply", "json", "help"}, nil)
	if problem != "" || len(positionals) > 0 {
		return func(_, stderr io.Writer) int {
			fmt.Fprint(stderr, sweepUsage)
			return 2
		}, 0, true
	}
	if _, help := flags["help"]; help {
		return func(stdout, _ io.Writer) int {
			fmt.Fprint(stdout, sweepUsage)
			return 0
		}, 0, true
	}
	return nil, hSweepUnported, false
}
