package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/state"
)

// B920: never build on a red base. `block open` reads the CI runs of the
// base a new block builds on (`gh run list --commit <base>`, the workflow of
// land.ci_workflow when set). With land.require_ci: github a red base, a base
// with no run or a base whose CI still runs refuses the open before anything
// is recorded, with the next step. With the gate off, a red base or a base
// with no run prints a warning and the open goes on; no gh, or a gh that
// cannot answer (no GitHub remote), stays silent.

// openBaseCIVerdict is the CI state of one base sha.
type openBaseCIVerdict struct {
	runs    int
	running []string
	red     []ciRun
	err     string
}

// openBaseCIRead reads the CI runs of sha once, grouped per workflow name as
// the land gate does: a workflow is green when any completed run of it is.
func openBaseCIRead(land map[string]any, cwd, sha string) openBaseCIVerdict {
	args := []string{"run", "list", "--commit", sha}
	if workflow, _ := land["ci_workflow"].(string); workflow != "" {
		args = append(args, "--workflow", workflow)
	}
	args = append(args, "--limit", "100", "--json", "databaseId,status,conclusion,name")
	out, err := ciGh(cwd, args...)
	if err != nil {
		return openBaseCIVerdict{err: strings.SplitN(err.Error(), "\n", 2)[0]}
	}
	runs := []ciRun{}
	if json.Unmarshal([]byte(out), &runs) != nil {
		return openBaseCIVerdict{err: "gh run list: unreadable JSON"}
	}
	v := openBaseCIVerdict{runs: len(runs)}
	order, byName := []string{}, map[string][]ciRun{}
	for _, r := range runs {
		if _, ok := byName[r.Name]; !ok {
			order = append(order, r.Name)
		}
		byName[r.Name] = append(byName[r.Name], r)
	}
	for _, name := range order {
		green, going := false, false
		var failed *ciRun
		for i, r := range byName[name] {
			switch {
			case r.Status != "completed":
				going = true
			case ciGreen(r.Conclusion):
				green = true
			case failed == nil || r.DatabaseID > failed.DatabaseID:
				failed = &byName[name][i]
			}
		}
		switch {
		case green:
		case going:
			v.running = append(v.running, name)
		case failed != nil:
			v.red = append(v.red, *failed)
		}
	}
	return v
}

// openBaseCICheck is the BaseCheck of `block open`: nil when the open may go
// on (warnings go to warn), else a refusal with its next step.
func openBaseCICheck(cfg map[string]any, cwd string, warn io.Writer) func(string) error {
	return func(sha string) error {
		provider, bad := landCIRequired(cfg)
		if bad != "" {
			// The land gate reports a bad land.require_ci; open does not.
			provider = ""
		}
		required := provider == "github"
		short := landCIShort(sha)
		refuse := func(message, next string) error {
			return &state.Error{Code: "base-not-green", Message: message + "\n" + strings.TrimSuffix(refusalNext(next), "\n")}
		}
		watch := closeCommand([]string{"code-forge", "ci", "watch", "--sha", sha, "--cwd", cwd})
		if !required {
			// gate off: a repository without an origin remote has no CI to
			// read, so gh is not run at all (it would only write its state)
			if url, err := gitx.Run(cwd, "remote", "get-url", "origin"); err != nil || strings.TrimSpace(url) == "" {
				return nil
			}
		}
		if _, err := exec.LookPath("gh"); err != nil {
			if required {
				return refuse("base "+short+": land.require_ci: github reads CI runs with the gh CLI, and gh is not on PATH; install gh and run gh auth login (B920)", "gh auth status")
			}
			return nil
		}
		v := openBaseCIRead(blockMap(cfg["land"]), cwd, sha)
		switch {
		case v.err != "":
			if required {
				return refuse("cannot read CI runs for base "+short+": "+v.err+"; a coder never builds on an unchecked base (B920)", "gh auth status")
			}
			return nil
		case len(v.red) > 0:
			red := []string{}
			for _, r := range v.red {
				red = append(red, fmt.Sprintf("%s: %s, run %d", r.Name, r.Conclusion, r.DatabaseID))
			}
			what := fmt.Sprintf("base %s CI is red (%s)", short, strings.Join(red, "; "))
			next := closeCommand([]string{"gh", "run", "view", fmt.Sprint(v.red[0].DatabaseID), "--log-failed"})
			if required {
				return refuse(what+"; a coder never builds on a red base (B920): fix main first, or open on a green commit with --base <sha>; nothing was recorded", next)
			}
			fmt.Fprintf(warn, "warning: %s; a coder should not build on a red base (B920) — fix main first (%s)\n", what, next)
		case v.runs == 0:
			if required {
				return refuse("base "+short+" has no CI run; a coder never builds on an unchecked base (B920): push it and wait for green CI, or open on a green commit with --base <sha>; nothing was recorded", watch)
			}
			fmt.Fprintf(warn, "warning: base %s has no CI run; a coder should build on a green base (B920) — check it with %s\n", short, watch)
		case len(v.running) > 0:
			if required {
				return refuse(fmt.Sprintf("base %s has no green CI run yet (running: %s); wait for green CI before a coder builds on it (B920); nothing was recorded", short, strings.Join(v.running, ", ")), watch)
			}
		}
		return nil
	}
}
