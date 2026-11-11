package main

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/rebase"
	"github.com/ricardov03/code-forge/native/state"
)

func init() { routes["gates"] = healWrap([]string{"gates"}, runGates) }

// gatesBaseCheckedEnv tells a Node handover that the stale-base check (B363)
// already ran in this process; Node removes it before any gate starts.
const gatesBaseCheckedEnv = "CODE_FORGE_GATES_BASE_CHECKED"

var gatesBaseChecked bool

func gatesHandover(args []string, reason handoverReason) int {
	if gatesBaseChecked {
		_ = os.Setenv(gatesBaseCheckedEnv, "1")
	}
	code, e := handover(append([]string{"gates"}, args...), reason)
	if e != nil {
		fmt.Fprintln(os.Stderr, "code-forge-native:", e)
	}
	return code
}
func gatesFlag(args []string, key string) (string, bool, bool) {
	for i, s := range args {
		if s == key {
			if i+1 == len(args) || strings.HasPrefix(args[i+1], "--") {
				return "", true, false
			}
			return args[i+1], true, true
		}
	}
	return "", false, true
}

type gatesOptions struct {
	cwd, run, block, slug        string
	cwdGiven, slugGiven, changed bool
	autoRebase, allowStaleBase   bool
	timeout                      float64
	timeoutGiven                 bool
	// full is an explicit gates run --full (B404): the only local full suite.
	full bool
	// scope is a test scope planned by a caller (the land merge gates) for a
	// run without --run/--block.
	scope *gates.Scope
}

func gatesParse(args []string) (gatesOptions, string) {
	o := gatesOptions{}
	o.cwd, _ = os.Getwd()
	v, given, ok := gatesFlag(args, "--cwd")
	if !ok {
		return o, "--cwd requires a value"
	}
	if given {
		o.cwd = v
		o.cwdGiven = true
	}
	v, given, ok = gatesFlag(args, "--timeout-ms")
	if !ok {
		return o, "--timeout-ms requires a value"
	}
	if given {
		n, e := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if strings.TrimSpace(v) == "" {
			n = 0
			e = nil
		}
		if e != nil || math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 {
			return o, fmt.Sprintf("--timeout-ms must be a finite number > 0, got \"%s\"", v)
		}
		o.timeout = n
		o.timeoutGiven = true
	}
	o.slug, o.slugGiven, ok = gatesFlag(args, "--slug")
	if !ok {
		return o, "--slug requires a value"
	}
	var r, b bool
	o.run, r, ok = gatesFlag(args, "--run")
	if !ok {
		return o, "--run <id> and --block <id> go together, each with a value"
	}
	o.block, b, ok = gatesFlag(args, "--block")
	if !ok || r != b {
		return o, "--run <id> and --block <id> go together, each with a value"
	}
	changedFlag := false
	for _, s := range args {
		switch s {
		case "--full":
			o.full, o.changed = true, true
		case "--changed":
			o.changed, changedFlag = true, true
		case "--auto-rebase":
			o.autoRebase = true
		case "--allow-stale-base":
			o.allowStaleBase = true
		}
	}
	if o.full && changedFlag {
		return o, "--full and --changed cannot go together"
	}
	if o.full && !r {
		return o, "--full needs --run <id> and --block <id>"
	}
	if o.changed && !r {
		return o, "--changed needs --run <id> and --block <id>"
	}
	if o.autoRebase && o.allowStaleBase {
		return o, "--auto-rebase and --allow-stale-base cannot go together"
	}
	return o, ""
}

// gatesStaleBase (B363) runs before any gate command: a worktree block whose
// base branch moved past its recorded base is refused at once with the fix
// line, rebased first with --auto-rebase, or gated with a WARN line with
// --allow-stale-base. stop reports that the run ends here with code.
func gatesStaleBase(o gatesOptions) (code int, stop bool) {
	gatesBaseChecked = true
	home := os.Getenv("HOME")
	stale := gatesStaleLookup(home, o.run, o.block)
	if stale == nil {
		return 0, false
	}
	fix, moved := gatesStaleText(o.run, o.block, stale)
	switch {
	case o.allowStaleBase:
		fmt.Fprint(os.Stderr, gatesStaleWarnLine(o.run, o.block, stale))
	case o.autoRebase:
		result, err := rebase.Run(home, o.run, o.block)
		if err != nil {
			gatesErrf("gates run: block %s of run %s: --auto-rebase failed (%s): %s; no gate was run; fix by hand: %s\n", o.block, o.run, moved, strings.ReplaceAll(err.Error(), "\n", " "), fix)
			return 1, true
		}
		fmt.Fprintf(os.Stderr, "gates run: block %s rebased %s → %s (--auto-rebase)\n", o.block, gatesShort(result.OldBase), gatesShort(result.NewBase))
	default:
		gatesErrf("gates run: block %s of run %s has a stale base (%s); no gate was run; fix: %s\n", o.block, o.run, moved, fix)
		return 1, true
	}
	return 0, false
}

// gatesStaleLookup reads the run and reports the block's stale base, or nil
// when the run cannot be read (the gate runner diagnoses that) or the base is
// not stale.
func gatesStaleLookup(home, run, block string) *gates.Stale {
	record, err := state.ReadRun(home, run)
	if err != nil {
		return nil
	}
	workspace, _ := record.Get("workspace").(string)
	return gates.StaleBase(workspace, gatesObj(gatesObj(record.Get("blocks")).Get(block)))
}

func gatesStaleText(run, block string, stale *gates.Stale) (fix, moved string) {
	fix = fmt.Sprintf("code-forge block rebase %s --run %s --accept-landed", block, run)
	moved = fmt.Sprintf("%s moved %s → %s", stale.Branch, gatesShort(stale.Recorded), gatesShort(stale.Tip))
	return fix, moved
}

func gatesStaleWarnLine(run, block string, stale *gates.Stale) string {
	fix, moved := gatesStaleText(run, block, stale)
	return fmt.Sprintf("gates run: WARN block %s of run %s gates on a stale base (%s) because of --allow-stale-base; fix: %s\n", block, run, moved, fix)
}

// gatesStaleWarning is the --allow-stale-base WARN line for a stale block, or
// "" when its base is current. block land (B363) gates a stale base on with it.
func gatesStaleWarning(home, run, block string) string {
	if stale := gatesStaleLookup(home, run, block); stale != nil {
		return gatesStaleWarnLine(run, block, stale)
	}
	return ""
}

// gatesShort is a sha's first 12 characters, or the whole value when shorter.
func gatesShort(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

const gatesClassesUsage = "usage: code-forge gates classes --run <id> --block <id>"

// gatesClasses (B335) prints the block's test-class filter from
// gates.test_class_map: the value {test_classes} takes in gates.test. An empty
// filter prints nothing on stdout and a note on stderr: the full suite runs.
func gatesClasses(args []string, home string, stdout, stderr io.Writer) int {
	run, hasRun, okRun := gatesFlag(args, "--run")
	block, hasBlock, okBlock := gatesFlag(args, "--block")
	if !hasRun || !hasBlock || !okRun || !okBlock || len(args) != 4 {
		fmt.Fprintln(stderr, gatesClassesUsage)
		return 2
	}
	if home == "" {
		fmt.Fprintln(stderr, "gates classes: HOME is not set")
		return 1
	}
	record, err := state.ReadRun(home, run)
	if err != nil {
		fmt.Fprintf(stderr, "gates classes: %s\n", err)
		return 1
	}
	entry, found := gatesObj(record.Get("blocks")).Get(block).(ledger.Object)
	if !found {
		fmt.Fprintf(stderr, "gates classes: run %s has no block %s\n", run, block)
		return 1
	}
	workspace, _ := record.Get("workspace").(string)
	loaded, err := config.Load(workspace)
	if err != nil {
		var missing *config.Error
		if !errors.As(err, &missing) || missing.Kind != "not-found" {
			fmt.Fprintf(stderr, "gates classes: %s\n", err)
			return 1
		}
	}
	configured, _ := loaded["gates"].(map[string]any)
	rules, err := gates.ParseClassMap(configured["test_class_map"])
	if err != nil {
		fmt.Fprintf(stderr, "gates classes: %s\n", err)
		return 1
	}
	filter := gates.BlockClassFilter(rules, entry)
	if filter == "" {
		fmt.Fprintf(stderr, "gates classes: block %s has no test class in gates.test_class_map; {test_classes} is dropped and the full suite runs\n", block)
		return 0
	}
	fmt.Fprintln(stdout, filter)
	return 0
}

func runGates(args []string) int {
	gatesLastMessage = nil
	gatesBaseChecked = false
	_ = os.Unsetenv(gatesBaseCheckedEnv)
	if len(args) > 0 && args[0] == "classes" {
		return gatesClasses(args[1:], os.Getenv("HOME"), os.Stdout, os.Stderr)
	}
	if len(args) == 0 || args[0] != "run" {
		return gatesHandover(args, hGatesUnportedSubcommand)
	}
	// B393: wait for a run lock another verb holds (block land holds it for
	// its whole merge gate) instead of failing after Node's 5 s.
	args, lockWait, lockMessage := lockWaitFlag(args)
	if lockMessage != "" {
		gatesErrf("gates run: %s\n", lockMessage)
		gatesFinish(args, 2)
		return 2
	}
	useLockWait("gates run", lockWait, os.Stderr)
	if os.Getenv("HOME") == "" {
		return gatesHandover(args, hGatesHome)
	}
	if run, given, ok := gatesFlag(args, "--run"); given && ok && run != "" {
		if err := state.AwaitRunLock(os.Getenv("HOME"), run); err != nil {
			var stateErr *state.Error
			if errors.As(err, &stateErr) && stateErr.Code == "locked" {
				gatesErrf("gates run: %s\n", err)
				gatesFinish(args, 1)
				return 1
			}
		}
	}
	_, hasRun, _ := gatesFlag(args, "--run")
	_, hasBlock, _ := gatesFlag(args, "--block")
	if !hasRun && !hasBlock {
		// A run without --run/--block is the Node full suite already; Node
		// does not know --full, so it is dropped on the way.
		plain := []string{}
		for _, a := range args {
			if a != "--full" {
				plain = append(plain, a)
			}
		}
		return gatesHandover(plain, hGatesUnscoped)
	}
	if v, given, ok := gatesFlag(args, "--timeout-ms"); given && ok {
		trimmed := strings.TrimSpace(v)
		if strings.HasPrefix(trimmed, "0x") || strings.HasPrefix(trimmed, "0X") || strings.HasPrefix(trimmed, "0b") || strings.HasPrefix(trimmed, "0B") || strings.HasPrefix(trimmed, "0o") || strings.HasPrefix(trimmed, "0O") {
			return gatesHandover(args, hGatesTimeoutRadix)
		}
		if n, e := strconv.ParseFloat(trimmed, 64); e == nil && n > 2147483647 && !math.IsInf(n, 0) {
			return gatesHandover(args, hGatesTimeoutOverflow)
		}
	}
	for i := 1; i < len(args); i++ {
		s := args[i]
		switch s {
		case "--run", "--block", "--cwd", "--slug", "--timeout-ms":
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				i++
			}
		case "--changed", "--full", "--json", "--auto-rebase", "--allow-stale-base":
		default:
			// Node's router passes argv through unchanged. Ordinary runs ignore
			// --cwd=; worktree runs explicitly reject it in gatesNative.
			if !strings.HasPrefix(s, "--cwd=") {
				return gatesHandover(args, hGatesUnsupportedFlag)
			}
		}
	}
	o, message := gatesParse(args)
	if message != "" {
		gatesErrf("gates run: %s\n", message)
		gatesFinish(args, 2)
		return 2
	}
	if o.run == "" && o.block == "" {
		return gatesHandover(args, hGatesEmptyScope)
	}
	if code, stop := gatesStaleBase(o); stop {
		gatesFinish(args, code)
		return code
	}
	reason := hGatesCustom
	code, safe := gatesNativeStep(args, o, nil, &reason)
	if !safe {
		return gatesHandover(args, reason)
	}
	gatesFinish(args, code)
	return code
}
