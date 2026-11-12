package main

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"github.com/ricardov03/code-forge/native/gosafe"
)

// panicSeam is nil in the product binary. Tests set it, and a binary built
// with -tags cfpanicseam sets it from CODE_FORGE_TEST_PANIC.
var panicSeam func(args []string)

// runGuarded is run with a guard for a panic on the main goroutine (B508).
// An unrecovered Go panic exits 2, the usage-error code, with a stack trace
// that can show argument text. Instead:
//   - a command that writes no state in Go (panicRerunSafe) goes to Node with
//     its original arguments and one go-panic hand-over row;
//   - any other command may already have changed state, so Node never runs it
//     a second time: one error line names the command and the panic site
//     (never the panic value or an argument), a go-panic-stopped row, exit 1.
func runGuarded(args []string) (code int, err error) {
	guardStderr = os.Stderr
	panicClaimed.Store(false)
	// B509: a panic in a goroutine the command started (gosafe.Go) gets the
	// same answer; the handler stays until the process ends.
	gosafe.SetHandler(func(_, site string) { goroutinePanic(args, site) })
	defer func() {
		value := recover()
		if value == nil {
			return
		}
		site := panicSite()
		if !panicClaimed.CompareAndSwap(false, true) {
			// A goroutine panic is already being handled; it ends the process.
			select {}
		}
		code, err = panicOutcome(args, site)
	}()
	if panicSeam != nil {
		panicSeam(args)
	}
	return run(args)
}

// guardStderr is the standard error the command started with. A command
// that copies its stderr through a pipe (healWrap) has moved os.Stderr; the
// panic line must still reach the terminal.
var guardStderr = os.Stderr

// panicClaimed makes sure one panic is handled: the first one, on the main
// goroutine or on any goroutine started with gosafe.Go.
var panicClaimed atomic.Bool

// goroutineExit ends the process after a goroutine panic; tests replace it.
var goroutineExit = os.Exit

// goroutinePanic handles a panic in a goroutine a command started (B509)
// with the B508 rules, then ends the process the way main does: a read-only
// command is handed to Node (exec replaces this process), any other command
// prints one error line, writes a go-panic-stopped row and exits 1.
func goroutinePanic(args []string, site string) {
	if !panicClaimed.CompareAndSwap(false, true) {
		select {}
	}
	code, err := panicOutcome(args, site)
	if err != nil {
		fmt.Fprintln(guardStderr, "code-forge-native:", err)
	}
	goroutineExit(code)
}

// panicOutcome is the B508 answer to a panic at site.
func panicOutcome(args []string, site string) (int, error) {
	if panicRerunSafe(args) {
		return handover(args, hGoPanic)
	}
	recordHandover(args, handoverReasons[hGoPanicStopped])
	name, sub := handoverCommand(args)
	if sub != "" && sub != "unknown" {
		name += " " + sub
	}
	if name == "" {
		name = "no command"
	}
	fmt.Fprintf(guardStderr, "code-forge: internal error in the Go engine (%s, at %s); Node did not re-run it because it may have changed state; check `code-forge status` before you retry\n", name, site)
	return 1, nil
}

// panicSite names the first frame outside the Go runtime below the panic:
// function, file base name and line. Called from the deferred recover.
func panicSite() string { return gosafe.Site() }

// panicRerunSafe lists the commands whose Go path writes no state (only
// read-only checks, plus the breadcrumb and error-log lines Node also
// writes), so running them again in Node after a panic is safe. Every other
// command, and any command added later, is treated as writing.
func panicRerunSafe(args []string) bool {
	if len(args) == 0 {
		return false
	}
	sub := ""
	if len(args) > 1 {
		sub = args[1]
	}
	switch args[0] {
	case "setup", "status":
		return true
	case "run":
		return sub == "status"
	case "queue":
		return sub != "run"
	case "heal":
		return sub == "list" || sub == "show"
	case "decisions":
		return sub == "list"
	case "docs":
		return sub == "path"
	case "logs":
		return sub == "" || sub == "summary" || sub == "path" || sub == "handovers" || strings.HasPrefix(sub, "-")
	case "ledger":
		return sub == "tail"
	}
	return false
}
