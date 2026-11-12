package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	defer func() {
		value := recover()
		if value == nil {
			return
		}
		site := panicSite()
		if panicRerunSafe(args) {
			code, err = handover(args, hGoPanic)
			return
		}
		recordHandover(args, handoverReasons[hGoPanicStopped])
		name, sub := handoverCommand(args)
		if sub != "" && sub != "unknown" {
			name += " " + sub
		}
		if name == "" {
			name = "no command"
		}
		fmt.Fprintf(os.Stderr, "code-forge: internal error in the Go engine (%s, at %s); Node did not re-run it because it may have changed state; check `code-forge status` before you retry\n", name, site)
		code, err = 1, nil
	}()
	if panicSeam != nil {
		panicSeam(args)
	}
	return run(args)
}

// panicSite names the first frame outside the Go runtime below the panic:
// function, file base name and line. Called from the deferred recover.
func panicSite() string {
	pcs := make([]uintptr, 64)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(1, pcs)])
	panicking := false
	for {
		frame, more := frames.Next()
		if frame.Function == "runtime.gopanic" {
			panicking = true
		} else if panicking && !strings.HasPrefix(frame.Function, "runtime.") {
			name := frame.Function[strings.LastIndex(frame.Function, "/")+1:]
			name = strings.TrimPrefix(strings.TrimPrefix(name, "main."), "code-forge-native.")
			return fmt.Sprintf("%s %s:%d", name, filepath.Base(frame.File), frame.Line)
		}
		if !more {
			return "unknown site"
		}
	}
}

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
