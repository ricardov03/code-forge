package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// Only verbs explicitly registered here run in Go. Internal accelerators are
// deliberately separate from this public route table.
var routes = map[string]func([]string) int{}

func run(args []string) (int, error) {
	if len(args) == 0 {
		// B458: no verb is help, as in bin/code-forge.mjs.
		args = []string{"help"}
	}
	if len(args) > 0 {
		if args[0] == "internal" {
			return runInternal(args[1:])
		}
		// B458: bin/code-forge.mjs aliases --help/-h to help and --version/-v to version.
		switch args[0] {
		case "--help", "-h":
			args = append([]string{"help"}, args[1:]...)
		case "--version", "-v":
			args = append([]string{"version"}, args[1:]...)
		}
		if route, ok := routes[args[0]]; ok {
			// B613: `<command> --help` adds the Description and Options around the usage.
			if name, ok := helpTarget(args); ok {
				return helpWrap(name, args[1:], route), nil
			}
			return route(args[1:]), nil
		}
	}
	verb := ""
	if len(args) > 0 {
		verb = args[0]
	}
	return unknownVerb(verb), nil
}

// B690a: Go never starts Node. A verb that is not in the route table gets
// Node's unknown-verb text (bin/code-forge.mjs: the verb list is src/cli's
// file names, which handoverCommands lists), exit 1, and writes nothing.
func unknownVerb(verb string) int {
	verbs := make([]string, 0, len(handoverCommands))
	for name := range handoverCommands {
		verbs = append(verbs, name)
	}
	sort.Strings(verbs)
	var b strings.Builder
	b.WriteString("code-forge: unknown verb \"" + verb + "\"\nVerbs:\n")
	for _, name := range verbs {
		b.WriteString("  " + name + "\n")
	}
	fmt.Fprint(os.Stderr, b.String())
	return 1
}

// refuseUnported is the B690a answer for every case Go does not serve yet
// (it used to start Node): one line on stderr naming the command and the
// reason, exit 1, nothing on stdout and no state, ledger or queue change.
// The handovers.jsonl row is still written (same fields as before), so
// `logs handovers` and doctor keep counting these cases. With no reason the
// verb's default reason applies.
func refuseUnported(args []string, reasons ...handoverReason) int {
	reason := defaultHandoverReason(args)
	if len(reasons) > 0 {
		reason = reasons[0]
	}
	return refuseUnportedTo(os.Stderr, args, reason)
}

// refuseUnportedTo writes the refusal to w (the panic guard passes the
// stderr the command started with).
func refuseUnportedTo(w io.Writer, args []string, reason handoverReason) int {
	if refusal := untilDoneHandoverRefusal(args); refusal != "" {
		// B649: an until-done grant keeps its own refusal text
		recordHandover(args, handoverReasons[hUntilDoneGrant])
		fmt.Fprint(w, refusal)
		return 1
	}
	recordHandover(args, handoverReasons[reason])
	name, sub := handoverCommand(args)
	if sub != "" && sub != "unknown" {
		name += " " + sub
	}
	fmt.Fprintf(w, "code-forge: %s is not supported in this case yet (%s). Please report it: code-forge logs report\n", name, handoverReasons[reason])
	return 1
}
