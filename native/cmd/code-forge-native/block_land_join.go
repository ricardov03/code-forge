package main

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
)

// landJoinStep is land's review join (Node's land ran runReviewFile
// --wait-block, which asks the run daemon when it is on and otherwise joins
// in-process). With the daemon on it keeps the daemon transport
// (reviewWaitBlockStep); with native.daemon: false it runs the same join as
// review-file --wait-block in this process (B789). duration is the --max text
// for the daemon request, maxWait the same deadline as a duration.
func landJoinStep(daemonOn bool, id, run, cwd, duration string, maxWait time.Duration, s *landStep) int {
	if daemonOn {
		return reviewWaitBlockStep([]string{"--wait-block", id, "--run", run, "--max", duration, "--cwd", cwd}, s)
	}
	stdout, stderr := s.writers()
	ctx := context.Background()
	real, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return landHandover
	}
	// B1005: the run's root, where its worker serves the queue
	root, ok := reviewRunRootOK(ctx, real, run)
	if !ok {
		return landHandover
	}
	if s.preflight {
		// Before any write: only a scope the join cannot read in Go stops
		// land here. Owned files appear with the auto-claim, so an
		// unreadable scope is checked again by the real join.
		if _, _, scope := reviewBlockConfig(ctx, root, real, run, id); scope == reviewScopeUnsupported {
			return landHandover
		}
		return 0
	}
	joined := reviewWaitBlockJoin(real, root, run, id, maxWait, stderr)
	switch {
	case joined.handed:
		fmt.Fprintf(stderr, "the review join is not supported in this case yet (%s)\n", handoverReasons[joined.handover])
		return 1
	case joined.refusal != nil:
		data, _ := ledger.Stringify(joined.refusal, false)
		fmt.Fprintln(stdout, string(data))
		return 1
	}
	data, err := ledger.Stringify(joined.answer, false)
	if err != nil {
		fmt.Fprintln(stderr, "the review join answer cannot be encoded")
		return 1
	}
	fmt.Fprintln(stdout, string(data))
	return joined.code
}
