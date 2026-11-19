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
	// B715: the safe-output set is the process's (Node's redact.mjs set): a
	// verb starts with none registered. A real process runs one verb, so this
	// clears nothing there; in-process tests run many verbs one after another.
	autopilotSecretsMu.Lock()
	autopilotSecrets = nil
	autopilotSecretsMu.Unlock()
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
			// B738: bin/code-forge.mjs clears the reported error kind before a verb
			// runs (clearErrorKind) and takes it when the verb ends (takeErrorKind),
			// so one verb's kind or crash stack never reaches the next verb's
			// errors.jsonl entry when several run in one process. The warning
			// state starts as a new process's too (noteVerb, per process).
			nativeTakeErrorKind()
			defer nativeTakeErrorKind()
			spawnWarnReset()
			// B613: `<command> --help` adds the Description and Options around the usage.
			if name, ok := helpTarget(args); ok {
				return helpWrap(name, args[1:], route), nil
			}
			// B895: an unknown-flag refusal also prints the command's usage.
			usageReset()
			code := route(args[1:])
			usageAfterRefusal(args, code)
			return code, nil
		}
	}
	verb := ""
	if len(args) > 0 {
		verb = args[0]
	}
	return unknownVerb(verb), nil
}

// unknownVerbUsage is the usage line an unknown verb prints (B878).
const unknownVerbUsage = "usage: code-forge <command> [options] [arguments]\n"

// B690a: Go never starts Node. A verb that is not in the route table gets
// the unknown-verb text and writes nothing. Since B878 it is a usage error
// like an unknown flag or help topic: exit 2, the usage line, and the list
// of the visible verbs of the help table (hidden ones such as land and
// native-status still run but are not listed).
func unknownVerb(verb string) int {
	verbs := []string{}
	for _, entry := range helpCommands {
		if !entry.hidden && !strings.Contains(entry.name, " ") {
			verbs = append(verbs, entry.name)
		}
	}
	sort.Strings(verbs)
	var b strings.Builder
	b.WriteString("code-forge: unknown verb \"" + verb + "\"\n" + unknownVerbUsage + "Verbs:\n")
	for _, name := range verbs {
		b.WriteString("  " + name + "\n")
	}
	b.WriteString("Run code-forge help for what each one does.\n")
	// Written through redact(), as every stderr path: registered secrets are masked.
	fmt.Fprint(os.Stderr, autopilotRedact(b.String()))
	return 2
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
	return refuseUnportedDetailTo(w, args, reason, "")
}

// refuseUnportedDetailTo is refuseUnportedTo whose handovers.jsonl row keeps
// the redacted detail text (B884); the stderr line is the same.
func refuseUnportedDetailTo(w io.Writer, args []string, reason handoverReason, detail string) int {
	if refusal := untilDoneHandoverRefusal(args); refusal != "" {
		// B649: an until-done grant keeps its own refusal text
		recordHandover(args, handoverReasons[hUntilDoneGrant])
		fmt.Fprint(w, autopilotRedact(refusal))
		return 1
	}
	recordHandoverDetail(args, handoverReasons[reason], detail)
	name, sub := handoverCommand(args)
	if sub != "" && sub != "unknown" {
		name += " " + sub
	}
	// Like every Node stderr path, the line goes through redact() (registered secrets masked).
	fmt.Fprint(w, autopilotRedact(fmt.Sprintf("code-forge: %s is not supported in this case yet (%s). Please report it: code-forge logs report\n", name, handoverReasons[reason])))
	return 1
}
