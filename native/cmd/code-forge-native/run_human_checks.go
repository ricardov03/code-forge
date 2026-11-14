package main

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/ricardov03/code-forge/native/autopilot"
	"github.com/ricardov03/code-forge/native/blockclose"
	"github.com/ricardov03/code-forge/native/humanask"
	"github.com/ricardov03/code-forge/native/humancheck"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B631 (issue #37 B, part 1): the run's human-check ledger.
//
//	run human-checks list --run <id> [--json]
//	run human-checks clear --run <id> --block <id> --clause <C<n>> --note <text> [--json]
//
// list is for anyone. clear is the owner's: the same guard as block
// human-pass (a terminal yes, refused under an active autopilot grant) and
// one signed human_check.cleared row.
const rhcListUsage = "usage: code-forge run human-checks list --run <id> [--json]\n"
const rhcClearUsage = "usage: code-forge run human-checks clear --run <id> --block <id> --clause <C<n>> --note <text> [--json]\n"

// rhcNow is the clock seam for the grant check.
var rhcNow = time.Now

type rhcFail struct {
	code    int
	message string
}

func (f *rhcFail) Error() string { return f.message }

func rhcUsage(message string) *rhcFail { return &rhcFail{2, message} }

// rhcFlags reads --name value / --name=value pairs and --json. Each flag
// may appear once; positionals are refused.
func rhcFlags(args []string, allowed ...string) (map[string]string, bool, *rhcFail) {
	values := map[string]string{}
	asJSON := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--json" {
			if asJSON {
				return nil, false, rhcUsage("--json was given twice")
			}
			asJSON = true
			continue
		}
		if !strings.HasPrefix(a, "--") {
			return nil, false, rhcUsage("unexpected argument " + statusQuote(a))
		}
		name, value, inline := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		known := false
		for _, n := range allowed {
			known = known || n == name
		}
		if !known {
			return nil, false, rhcUsage("unknown flag --" + name)
		}
		if _, seen := values[name]; seen {
			return nil, false, rhcUsage("--" + name + " was given twice")
		}
		if !inline {
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return nil, false, rhcUsage("--" + name + " needs a value")
			}
			value = args[i+1]
			i++
		}
		values[name] = value
	}
	return values, asJSON, nil
}

// runHumanChecks serves `run human-checks <list|clear> ...`.
func runHumanChecks(home string, args []string, stdout, stderr io.Writer) int {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	var err *rhcFail
	switch sub {
	case "list":
		err = rhcList(home, args[1:], stdout)
	case "clear":
		err = rhcClear(home, args[1:], stdout, stderr)
	default:
		fmt.Fprint(stderr, runSubUsage["human-checks"])
		return 2
	}
	if err == nil {
		return 0
	}
	if err.message != "" {
		fmt.Fprintf(stderr, "run human-checks %s: %s\n", sub, err.message)
	}
	return err.code
}

// rhcOpen reads the record, the project ledger and the key, and derives the
// open checks.
func rhcOpen(home, run string) (ledger.Object, []humancheck.Entry, *rhcFail) {
	record, err := state.ReadRun(home, run)
	if err != nil {
		return nil, nil, &rhcFail{1, blockIOMessage(err)}
	}
	open, fail := rhcOpenFor(home, run, record)
	return record, open, fail
}

func rhcOpenFor(home, run string, record ledger.Object) ([]humancheck.Entry, *rhcFail) {
	slug, _ := record.Get("project").(string)
	if slug == "" {
		return nil, &rhcFail{1, fmt.Sprintf("run %s names no project", run)}
	}
	rows, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge", "ledger"), slug)
	if err != nil {
		return nil, &rhcFail{1, blockIOMessage(err)}
	}
	key, _ := blockclose.ReadKey(home, run) // no valid key: no clear row verifies
	return humancheck.Open(run, record, rows, key), nil
}

func rhcList(home string, args []string, stdout io.Writer) *rhcFail {
	values, asJSON, fail := rhcFlags(args, "run")
	if fail != nil {
		return fail
	}
	run := values["run"]
	if run == "" {
		return rhcUsage("--run is required")
	}
	_, open, fail := rhcOpen(home, run)
	if fail != nil {
		return fail
	}
	if asJSON {
		list := make([]any, len(open))
		for i, e := range open {
			list[i] = e.Object()
		}
		data, _ := ledger.Stringify(ledger.Object{{Key: "run", Value: run}, {Key: "open", Value: list}}, false)
		fmt.Fprintln(stdout, string(data))
		return nil
	}
	if len(open) == 0 {
		fmt.Fprintf(stdout, "run %s: no open human checks\n", run)
		return nil
	}
	fmt.Fprintf(stdout, "run %s: %d open human check(s)\n", run, len(open))
	for _, e := range open {
		waits := ""
		if s, ok := e.WaitsFor.(string); ok {
			waits = "; waits for: " + s
		}
		opened := ""
		if s, ok := e.OpenedAt.(string); ok && s != "" {
			opened = "; opened " + s
		}
		fmt.Fprintf(stdout, "%s %s %s%s%s\n", e.Block, e.Clause, statusQuote(e.Check), waits, opened)
	}
	return nil
}

func rhcTextOK(s string) bool {
	return strings.TrimSpace(s) != "" && !strings.ContainsFunc(s, unicode.IsControl)
}

func rhcClear(home string, args []string, stdout, stderr io.Writer) *rhcFail {
	values, asJSON, fail := rhcFlags(args, "run", "block", "clause", "note")
	if fail != nil {
		return fail
	}
	run, block, clause, note := values["run"], values["block"], values["clause"], values["note"]
	if run == "" || block == "" || clause == "" {
		return rhcUsage("--run, --block and --clause are required")
	}
	if !rhcTextOK(note) {
		return rhcUsage("--note must be nonblank text without control characters")
	}
	if !closeTextSafe(note) {
		return rhcUsage("--note must not contain a secret-like token")
	}
	note = humanTrim(note)
	record, open, fail := rhcOpen(home, run)
	if fail != nil {
		return fail
	}
	if !humanTTY() {
		fmt.Fprintln(stderr, "run human-checks clear needs the human at a terminal to confirm; nothing was recorded (there is no --yes)")
		return &rhcFail{2, ""}
	}
	if record.Get("status") != "active" {
		return &rhcFail{1, fmt.Sprintf("run %s has ended", run)}
	}
	entry, ok := humancheck.Find(open, block, clause)
	if !ok {
		return &rhcFail{1, fmt.Sprintf("human-check-not-open: block %s has no open human check %s in run %s; nothing was recorded", block, clause, run)}
	}
	activeGrant := &rhcFail{1, fmt.Sprintf("run human-checks clear is refused while an autopilot grant is active on run %s", run)}
	grant, err := autopilot.CheckExpiry(home, run, rhcNow())
	if err != nil {
		return &rhcFail{1, blockIOMessage(err)}
	}
	if grant.State == "active" {
		return activeGrant
	}
	question := fmt.Sprintf("Confirm human check %s of block %s (run %s) passed: %s? Only the owner may say yes.", clause, block, run, statusQuote(entry.Check))
	result, askErr := humanAsk(question)
	if result == humanask.NoTTY {
		fmt.Fprintln(stderr, "run human-checks clear needs the human at a terminal to confirm; nothing was recorded (there is no --yes)")
		return &rhcFail{2, ""}
	}
	if askErr != nil || result != humanask.Confirmed {
		fmt.Fprintf(stderr, "run human-checks clear %s %s: cancelled — nothing was recorded\n", block, clause)
		return &rhcFail{1, ""}
	}
	var row ledger.Object
	err = state.UpdateBlock(home, run, "", func(current, _ *ledger.Object) ([]ledger.Object, error) {
		if g := blockObject(current.Get("autopilot")); g != nil {
			s, err := autopilot.GrantState(g, rhcNow())
			if err != nil || s == "active" {
				return nil, &state.Error{Code: "autopilot-active", Message: activeGrant.message}
			}
		}
		again, fail := rhcOpenFor(home, run, *current)
		if fail != nil {
			return nil, &state.Error{Code: "io", Message: fail.message}
		}
		now, ok := humancheck.Find(again, block, clause)
		if !ok || now.Check != entry.Check {
			return nil, &state.Error{Code: "human-check-not-open", Message: fmt.Sprintf("human-check-not-open: block %s has no open human check %s in run %s; nothing was recorded", block, clause, run)}
		}
		row = ledger.Object{{Key: "event", Value: humancheck.ClearedEvent}, {Key: "block", Value: block}, {Key: "clause", Value: clause}, {Key: "check", Value: now.Check}, {Key: "waits_for", Value: now.WaitsFor}, {Key: "note", Value: note}, {Key: "by", Value: "human"}}
		return []ledger.Object{row}, nil
	})
	if err != nil {
		var se *state.Error
		if errors.As(err, &se) {
			return &rhcFail{1, se.Message}
		}
		return &rhcFail{1, blockIOMessage(err)}
	}
	if asJSON {
		data, _ := ledger.Stringify(row, false)
		fmt.Fprintln(stdout, string(data))
	} else {
		fmt.Fprintf(stdout, "run %s: human check %s %s cleared (by: human)\n", run, block, clause)
	}
	return nil
}

// runOutWriter sends a writer's text through runOut (stdout or stderr), so
// the failure log sees the last stderr line as for the other run verbs.
type runOutWriter struct {
	o     *runOut
	isErr bool
}

func (w runOutWriter) Write(p []byte) (int, error) {
	if w.isErr {
		w.o.err(string(p))
	} else {
		w.o.out(string(p))
	}
	return len(p), nil
}
