package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ricardov03/code-forge/native/blockopen"
	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
)

// A lane is a brief run in an existing checkout of a combine tip. Keep the
// policy in the run, rather than temporarily rewriting the owner's config.
func init() { routes["lane"] = runLane }

const laneStartUsage = "usage: code-forge lane start --base <combine-tip> [--cwd <dir>] [--run <id>] [--engine <e>] [--worker-pid <pid>]\n"
const laneReadyUsage = "usage: code-forge lane ready --run <lane-run> [--json]\n"
const laneCombineUsage = "usage: code-forge lane combine --run <combine-run> <lane-run>...\n"
const laneUsage = laneStartUsage + laneReadyUsage + laneCombineUsage

func runLane(args []string) int {
	if len(args) > 0 && (args[0] == "ready" || args[0] == "combine") {
		return runLaneIntegration(args)
	}
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h" || len(args) > 1 && args[0] == "start" && (args[1] == "--help" || args[1] == "-h")) {
		fmt.Fprint(safeStdout, laneUsage)
		return 0
	}
	fail := func(message string) int {
		fmt.Fprintln(safeStderr, "lane start: "+message)
		fmt.Fprint(safeStderr, laneUsage)
		return 2
	}
	if len(args) == 0 || args[0] != "start" {
		return fail("expected start")
	}
	// Only start's flags are accepted; positional garbage and run modes never
	// reach runParseFlags (which intentionally ignores positionals for run).
	base, forwarded := "", []string{}
	seen := map[string]bool{}
	for i := 1; i < len(args); i++ {
		name, value, inline := strings.Cut(strings.TrimPrefix(args[i], "--"), "=")
		if !strings.HasPrefix(args[i], "--") || !strings.Contains(" base cwd run engine worker-pid ", " "+name+" ") || name == "" {
			return fail("unknown argument " + args[i])
		}
		if seen[name] {
			return fail("--" + name + " given more than once")
		}
		seen[name] = true
		if !inline {
			if i+1 == len(args) || strings.HasPrefix(args[i+1], "--") {
				return fail("--" + name + " needs a value")
			}
			i++
			value = args[i]
		}
		if value == "" {
			return fail("--" + name + " needs a value")
		}
		if name == "base" {
			base = value
		} else {
			forwarded = append(forwarded, "--"+name, value)
		}
	}
	if base == "" {
		return fail("--base must name the newest combine tip (or main when no combine is open)")
	}
	flags, err := runParseFlags(forwarded, false)
	var pid float64
	if err == nil {
		err = runCheckFlags("start", flags, &pid)
	}
	if err != nil {
		return fail(err.Error())
	}
	flags.values["mode"] = "brief"
	flags.laneBase = base
	home, err := os.UserHomeDir()
	if err != nil {
		return fail(err.Error())
	}
	plan, reason := runStartPlan(home, flags, pid)
	if plan == nil {
		fmt.Fprintf(safeStderr, "lane start: cannot prepare the lane run (%s); no lane was started\n", handoverReasons[reason])
		return 1
	}
	return plan(&runOut{})
}

func laneStartPolicy(workspace, ref string) (ledger.Object, error) {
	base, err := blockopen.Resolve(workspace, ref)
	if err != nil {
		return nil, err
	}
	head, err := blockopen.Resolve(workspace, "HEAD")
	if err != nil {
		return nil, err
	}
	if head != base {
		return nil, fmt.Errorf("lane HEAD must equal --base %s; start from the newest combine tip without rebasing", base[:12])
	}
	branch, err := gitx.Run(workspace, "symbolic-ref", "--quiet", "--short", "HEAD")
	branch = strings.TrimSpace(branch)
	if err != nil || branch == "main" || branch == "master" || branch == "" {
		return nil, fmt.Errorf("start on a lane branch, separate from main")
	}
	status, err := gitx.Run(workspace, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(status) != "" {
		return nil, fmt.Errorf("lane start needs a clean worktree")
	}
	return ledger.Object{{Key: "base", Value: base}, {Key: "branch", Value: branch}}, nil
}

// Only these runners have a defined name-filter contract. An arbitrary shell
// wrapper may ignore CODE_FORGE_TEST_RUN and silently run a full suite.
func laneNamedRunner(p gates.Plan) bool {
	c := p.Commands["test"]
	if c == nil {
		return true
	}
	i, verb := gatesNamesGoVerb(c.Argv)
	return verb == "test" || verb == "run" && i+1 < len(c.Argv) && filepath.Clean(c.Argv[i+1]) == "cmd/cf-gotest"
}
