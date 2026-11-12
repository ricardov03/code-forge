package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/ricardov03/code-forge/native/config"
)

// B555: runFactsVerb is runFactsVerb in src/cli/facts.mjs. Go serves every
// refusal Node makes before the session starts (usage, the project config, an
// unreadable --brief or --sources path, a bad --timeout, a bad --run id):
// same stderr, exit 2, breadcrumb and error-log row, and no file written.
// B556: a call that passes those checks runs buildFacts in Go (facts_build.go)
// for anthropic and xai; the cases Go does not serve byte-equal hand over
// with a named reason decided before any write.
func runFactsVerb(args []string) int {
	if metaHelp(args, factsUsage, nil) {
		return 0
	}
	spec := metaFlagSpec{values: []string{"brief", "out", "run", "timeout"}, multi: []string{"sources"}}
	flags, positionals, message := metaParseFlags(args, spec)
	brief, hasBrief := flags["brief"].(string)
	if message != "" || len(positionals) > 0 || !hasBrief {
		return metaServe("facts", "", args, func(_, stderr io.Writer) int {
			if message != "" {
				fmt.Fprintf(stderr, "facts: %s\n", message)
			} else {
				fmt.Fprint(stderr, factsUsage)
			}
			return 2
		})
	}
	refusal, reason := fcPreRunRefusal(flags, brief)
	if refusal == "" && reason != hFactsSession {
		return metaHandover("facts", args, reason)
	}
	if refusal == "" {
		// B556: buildFacts in Go; every hand-over is decided here, before any write
		plan, reason := fcPlanFacts(flags, brief)
		if plan == nil {
			return metaHandover("facts", args, reason)
		}
		crumbFile := filepath.Join(os.Getenv("HOME"), ".code-forge", "logs", "breadcrumbs.json")
		crumbBytes, crumbErr := os.ReadFile(crumbFile)
		before := gatesBreadcrumbName("facts")
		stderr := &contextErrWriter{}
		code, handover := fcRunFacts(plan, os.Stdout, stderr)
		if handover {
			fcRestoreCrumbs(crumbBytes, crumbErr == nil)
			return metaHandover("facts", args, hFactsSession)
		}
		if code != 0 {
			var message any
			if stderr.last != "" {
				message = stderr.last
			}
			nativeVerbFailure("facts", "", args, code, message, before)
		}
		return code
	}
	return metaServe("facts", "", args, func(_, stderr io.Writer) int {
		fmt.Fprint(stderr, refusal)
		return 2
	})
}

// fcPreRunRefusal makes Node's pre-run checks in Node's order. It returns the
// stderr line of the first one that fails, or "" and the hand-over reason.
func fcPreRunRefusal(flags map[string]any, brief string) (string, handoverReason) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", hFactsConfig
	}
	if _, err := config.LoadUnchecked(cwd); err != nil {
		var cfgErr *config.Error
		// a YAML parse error: Go's parser words it differently from Node's
		// yaml package; any other kind is a check Node's loader does not make
		if !errors.As(err, &cfgErr) || (cfgErr.Kind != "not-found" && cfgErr.Kind != "read-error" && cfgErr.Kind != "migrate-error") {
			return "", hFactsConfig
		}
		return "facts: " + cfgErr.Message + "\n", 0
	}
	if !fcReadable(spawnResolve(cwd, brief), true) {
		return "facts: --brief cannot be read\n", 0
	}
	sources, _ := flags["sources"].([]string)
	for _, source := range sources {
		if !fcReadable(spawnResolve(cwd, source), false) {
			return "facts: --sources path cannot be read\n", 0
		}
	}
	if raw, ok := flags["timeout"].(string); ok {
		if _, bad := cliIntFlag(raw, "timeout"); bad != "" {
			return "facts: " + bad + "\n", 0
		}
	}
	// runRootFor checks the id before it makes the run's temp root; a good
	// id is left to Node so the root and its owner.json are made only once.
	if run, ok := flags["run"].(string); ok && !contextRunID.MatchString(run) {
		return "facts: a run id is one path segment of letters, digits, \".\", \"_\" or \"-\"\n", 0
	}
	return "", hFactsSession
}

// fcReadable is readable() in src/cli/facts.mjs: access(R_OK) and a stat that
// follows links; a directory counts unless fileOnly.
func fcReadable(file string, fileOnly bool) bool {
	if syscall.Access(file, 4) != nil {
		return false
	}
	st, err := os.Stat(file)
	if err != nil {
		return false
	}
	if fileOnly {
		return st.Mode().IsRegular()
	}
	return st.Mode().IsRegular() || st.IsDir()
}
