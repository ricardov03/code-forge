package main

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/user"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

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
	// B392: the preflight flags are Go only; Node never sees them.
	args = slices.DeleteFunc(slices.Clone(args), func(s string) bool { return s == "--preflight" || s == "--no-preflight" })
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
	preflight, noPreflight       bool
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
		n, valid := gatesJSNumber(v)
		if !valid || math.IsInf(n, 0) || n <= 0 {
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
		case "--preflight":
			o.preflight = true
		case "--no-preflight":
			o.noPreflight = true
		}
	}
	if o.full && changedFlag {
		return o, "--full and --changed cannot go together"
	}
	if o.full && !r {
		return o, "--full needs --run <id> and --block <id>"
	}
	if o.preflight && o.noPreflight {
		return o, "--preflight and --no-preflight cannot go together"
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
	// B402: refuse gates.extra.invariants before any hand-over; Node would
	// skip its script silently too.
	cwd, given, _ := gatesFlag(args, "--cwd")
	if !given {
		cwd, _ = os.Getwd()
	}
	if loaded, err := config.Load(cwd); err == nil {
		if err := config.CheckInvariants(loaded); err != nil {
			gatesErrf("gates run: %s; no gate was run\n", err)
			gatesFinish(args, 2)
			return 2
		}
	}
	// B450: every gates run form runs in Go; none hands over to Node.
	// Node's os.homedir() reads the user database when HOME is unset.
	if os.Getenv("HOME") == "" {
		u, err := user.Current()
		if err != nil || u.HomeDir == "" {
			gatesErrf("gates run: HOME is not set and the user database has no home folder, so the run records and the ledger cannot be found; set HOME to your home folder and rerun; no gate was run\n")
			gatesFinish(args, 1)
			return 1
		}
		_ = os.Setenv("HOME", u.HomeDir)
	}
	_, hasRun, _ := gatesFlag(args, "--run")
	_, hasBlock, _ := gatesFlag(args, "--block")
	if !hasRun && !hasBlock {
		// A run without --run/--block runs every configured gate; Node reads
		// no --full, --preflight or --no-preflight there, so they are dropped.
		args = slices.DeleteFunc(slices.Clone(args), func(s string) bool {
			return s == "--full" || s == "--preflight" || s == "--no-preflight"
		})
	}
	o, message := gatesParse(args)
	if message != "" {
		gatesErrf("gates run: %s\n", message)
		gatesFinish(args, 2)
		return 2
	}
	// Node reads an empty --run as a run id and refuses it (assertRunId).
	if hasRun && o.run == "" {
		gatesErrf("gates run: run id must match /^[a-z0-9][a-z0-9-]{0,63}$/\n")
		gatesFinish(args, 1)
		return 1
	}
	// B392: the preflight is on by default (it reads the run, the ledger and
	// the block's files, no gate result); --no-preflight skips it.
	if o.run != "" && o.block != "" && !o.noPreflight {
		if code, stop := gatesPreflight(o); stop {
			gatesFinish(args, code)
			return code
		}
	}
	if code, stop := gatesStaleBase(o); stop {
		gatesFinish(args, code)
		return code
	}
	code, safe := gatesNativeStep(args, o, nil)
	if !safe {
		return gatesHandover(args, hGatesCustom)
	}
	gatesFinish(args, code)
	return code
}

// gatesJSNumber is JavaScript's Number(text) (B450): surrounding white space
// is dropped, "" is 0, 0x/0o/0b prefixes read hex, octal and binary, and any
// other text is a decimal literal or Infinity. valid is false where Number
// gives NaN.
func gatesJSNumber(text string) (float64, bool) {
	s := strings.TrimFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || r == '\ufeff' || unicode.Is(unicode.Zs, r)
	})
	if s == "" {
		return 0, true
	}
	if len(s) > 2 && s[0] == '0' {
		base := 0
		switch s[1] {
		case 'x', 'X':
			base = 16
		case 'o', 'O':
			base = 8
		case 'b', 'B':
			base = 2
		}
		if base != 0 {
			n := 0.0
			for _, r := range s[2:] {
				d := strings.IndexRune("0123456789abcdef", unicode.ToLower(r))
				if d < 0 || d >= base {
					return math.NaN(), false
				}
				n = n*float64(base) + float64(d)
			}
			return n, true
		}
	}
	switch s {
	case "Infinity", "+Infinity":
		return math.Inf(1), true
	case "-Infinity":
		return math.Inf(-1), true
	}
	if !gatesDecimal.MatchString(s) {
		return math.NaN(), false
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return math.NaN(), false
	}
	return n, true
}

var gatesDecimal = regexp.MustCompile(`^[+-]?(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)
