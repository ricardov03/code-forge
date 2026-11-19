package main

import (
	"strings"

	"github.com/ricardov03/code-forge/native/worker"
)

// B875 (B801 audit P08): a bare refusal ends with one `next:` line that names
// the command to run, so a person or an agent is never left at a dead end.

// refusalNext is the `next:` line for command.
func refusalNext(command string) string { return "next: " + command + "\n" }

// refusalSlugNext is the next step after "no ledger for slug": the report of
// a slug that has a ledger, or a run start when no ledger exists yet.
func refusalSlugNext(verb string, known []string) string {
	if len(known) == 0 {
		return "code-forge run start"
	}
	return "code-forge " + verb + " --slug " + known[0]
}

// refusalShellArg quotes a path for a shell line when it needs quoting.
func refusalShellArg(s string) string {
	if s != "" && keysShellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// refusalStripNext drops a final `next:` line, so the error log keeps the
// refusal itself (and its fingerprint) as the message.
func refusalStripNext(text string) string {
	i := strings.LastIndex(text, "\n")
	if i < 0 || !strings.HasPrefix(text[i+1:], "next: ") {
		return text
	}
	return strings.TrimRight(text[:i], " \t\r\n")
}

// workerRefusalNext names the worker that holds the queue and the command
// that shows it (B875).
func workerRefusalNext(werr *worker.Error) string {
	text := ""
	switch {
	case werr.HolderPid != "" && werr.HolderRun != "":
		text = "worker: held by pid " + werr.HolderPid + " (run " + werr.HolderRun + ")\n"
	case werr.HolderPid != "":
		text = "worker: held by pid " + werr.HolderPid + "\n"
	case werr.HolderFile != "":
		text = "worker: held by an unreadable entry " + werr.HolderFile + "\n"
	}
	if werr.HolderRun != "" {
		return text + refusalNext("code-forge run status --run "+werr.HolderRun)
	}
	return text + refusalNext("code-forge status")
}
