package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/config"
)

// B460 (phase 1): `code-forge spawn` in Go (src/cli/spawn.mjs). Go answers
// --help, every usage error, a config that does not load, a bad --timeout,
// an unreadable or non-JSON --schema, a bad --run id, and a bad --level or
// --role when no --run is given. B477: a foreground closed-book session with
// no --run (spawn_session.go) runs in Go too, and so does --print-argv for it.
// B482: so does one with --run (the run root, then session's budget.usd and
// autopilot gate). --run with the coder role, --background, the facts role,
// a schema other than finding.schema.json and every case session.Check
// refuses hand over.
func init() { routes["spawn"] = runSpawnVerb }

var spawnRoles = []string{"coder", "reviewer", "judge", "s2", "author", "facts", "delegate"}
var spawnLevels = []string{"L0", "L1", "L2", "L3"}

var spawnUsage = "usage: code-forge spawn --level L0|L1|L2|L3 --role " + strings.Join(spawnRoles, "|") + " --brief <file> [--schema <file>] [--cwd <dir>] [--run <id>] [--block <id>] [--timeout <seconds>] [--background] [--print-argv]\n"

// runRootFor's run id rule (src/session/spawn.mjs).
var spawnRunID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func spawnHandover(args []string, reason handoverReason) int {
	code, err := handover(append([]string{"spawn"}, args...), reason)
	if err != nil {
		fmt.Fprintln(os.Stderr, "code-forge-native:", err)
	}
	return code
}

func spawnPlan(args []string) (func(o *runOut) int, handoverReason) {
	fail := func(code int, text string) (func(o *runOut) int, handoverReason) {
		return func(o *runOut) int { o.err(text); return code }, 0
	}
	parsed, bad := cliParseFlags(args, cliSpec{values: []string{"level", "role", "brief", "schema", "cwd", "run", "block", "timeout"}, booleans: []string{"background", "print-argv"}})
	if bad != "" {
		return fail(2, "spawn: "+bad+"\n")
	}
	level, hasLevel := parsed.values["level"]
	role, hasRole := parsed.values["role"]
	_, hasBrief := parsed.values["brief"]
	if len(parsed.positionals) > 0 || !hasLevel || !hasRole || !hasBrief || (parsed.booleans["print-argv"] && parsed.booleans["background"]) {
		return fail(2, spawnUsage)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, hSpawnConfig
	}
	if _, err := config.Load(cwd); err != nil {
		var cfgErr *config.Error
		if !errors.As(err, &cfgErr) || (cfgErr.Kind != "not-found" && cfgErr.Kind != "read-error" && cfgErr.Kind != "parse-error" && cfgErr.Kind != "migrate-error") {
			return nil, hSpawnConfig // a check Node's loader does not make
		}
		return fail(2, "spawn: "+cfgErr.Message+"\n")
	}
	if raw, ok := parsed.values["timeout"]; ok {
		if _, bad := cliIntFlag(raw, "timeout"); bad != "" {
			return fail(2, "spawn: "+bad+"\n")
		}
	}
	if file, ok := parsed.values["schema"]; ok {
		data, err := os.ReadFile(spawnResolve(cwd, file))
		if err != nil {
			return fail(2, "spawn: --schema cannot be read\n")
		}
		if !utf8.Valid(data) {
			return nil, hSpawnSchema // Node decodes bad bytes to U+FFFD first
		}
		if !json.Valid(data) {
			return fail(2, "spawn: --schema is not a valid JSON file\n")
		}
	}
	if runID, ok := parsed.values["run"]; ok {
		if !spawnRunID.MatchString(runID) {
			return fail(2, "spawn: a run id is one path segment of letters, digits, \".\", \"_\" or \"-\"\n")
		}
		// runRootFor makes the run's temp root before the level and role are checked.
		// B482: a coder (checkRunSandbox) and --background still hand over.
		if role == "coder" || parsed.booleans["background"] {
			return nil, hSpawnSession
		}
		runRoot, ok := spawnRunRoot(cwd, runID)
		if !ok {
			return nil, hSpawnSession
		}
		if !cliContains(spawnLevels, level) {
			return fail(2, "spawn: level must be one of "+strings.Join(spawnLevels, ", ")+"\n")
		}
		if !cliContains(spawnRoles, role) {
			return fail(2, "spawn: role must be one of "+strings.Join(spawnRoles, ", ")+"\n")
		}
		return spawnSessionPlan(cwd, parsed, runRoot)
	}
	if !cliContains(spawnLevels, level) {
		return fail(2, "spawn: level must be one of "+strings.Join(spawnLevels, ", ")+"\n")
	}
	if !cliContains(spawnRoles, role) {
		return fail(2, "spawn: role must be one of "+strings.Join(spawnRoles, ", ")+"\n")
	}
	return spawnSessionPlan(cwd, parsed, "")
}

// spawnResolve is path.resolve(file) against the process cwd.
func spawnResolve(cwd, file string) string {
	if filepath.IsAbs(file) {
		return filepath.Clean(file)
	}
	return filepath.Join(cwd, file)
}

func runSpawnVerb(args []string) int {
	if len(args) > 0 && cliHelp(args[0]) {
		fmt.Fprint(os.Stdout, spawnUsage)
		return 0
	}
	home := os.Getenv("HOME")
	if home == "" || !filepath.IsAbs(home) {
		return spawnHandover(args, hSpawnHome)
	}
	plan, reason := spawnPlan(args)
	if plan == nil {
		return spawnHandover(args, reason)
	}
	before := gatesBreadcrumbName("spawn")
	spawnWarnBefore = before
	o := &runOut{}
	code := plan(o)
	if code != 0 {
		var message any
		if o.last != "" {
			message = strings.TrimSpace(o.last)
		}
		nativeVerbFailure("spawn", "", args, code, message, before)
	}
	return code
}
