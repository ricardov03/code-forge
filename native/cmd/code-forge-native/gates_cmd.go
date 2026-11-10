package main

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/rebase"
	"github.com/ricardov03/code-forge/native/state"
)

func init() { routes["gates"] = runGates }

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
	for _, s := range args {
		switch s {
		case "--changed":
			o.changed = true
		case "--auto-rebase":
			o.autoRebase = true
		case "--allow-stale-base":
			o.allowStaleBase = true
		}
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
	record, err := state.ReadRun(home, o.run)
	if err != nil {
		return 0, false // the gate runner diagnoses a missing or unreadable run
	}
	workspace, _ := record.Get("workspace").(string)
	stale := gates.StaleBase(workspace, gatesObj(gatesObj(record.Get("blocks")).Get(o.block)))
	if stale == nil {
		return 0, false
	}
	fix := fmt.Sprintf("code-forge block rebase %s --run %s --accept-landed", o.block, o.run)
	moved := fmt.Sprintf("%s moved %s → %s", stale.Branch, gatesShort(stale.Recorded), gatesShort(stale.Tip))
	switch {
	case o.allowStaleBase:
		fmt.Fprintf(os.Stderr, "gates run: WARN block %s of run %s gates on a stale base (%s) because of --allow-stale-base; fix: %s\n", o.block, o.run, moved, fix)
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

// gatesShort is a sha's first 12 characters, or the whole value when shorter.
func gatesShort(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func runGates(args []string) int {
	gatesLastMessage = nil
	if len(args) == 0 || args[0] != "run" {
		return gatesHandover(args, hGatesUnportedSubcommand)
	}
	if os.Getenv("HOME") == "" {
		return gatesHandover(args, hGatesHome)
	}
	_, hasRun, _ := gatesFlag(args, "--run")
	_, hasBlock, _ := gatesFlag(args, "--block")
	if !hasRun && !hasBlock {
		return gatesHandover(args, hGatesUnscoped)
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
		case "--changed", "--json", "--auto-rebase", "--allow-stale-base":
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
