package main

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
)

// B895: an unknown flag is a usage error everywhere (B878), and since B895
// every command prints its usage with the refusal, as gates did first:
// `<command>: unknown flag "<flag>"`, the command's usage, exit 2. The usage
// is the one `code-forge <command> --help` prints, so the two never differ.
//
// Every verb writes its stderr through safeStderr, so the router sees the
// refusal there: when the verb ends with exit 2 after an unknown-flag line
// and printed no usage itself, the router adds the usage of the command the
// arguments name. A verb that already prints its usage is left alone.

var (
	usageRefusalMu   sync.Mutex
	usageSawRefusal  bool
	usageSawUsage    bool
	usageRefusalLine = regexp.MustCompile(`(?m)^[a-z][a-z0-9 -]*: unknown flag `)
	usageLine        = regexp.MustCompile(`(?mi)^\s*usage:`)
)

// usageNote records what one stderr write says (called by safeStream).
func usageNote(p []byte) {
	text := string(p)
	refusal, usage := usageRefusalLine.MatchString(text), usageLine.MatchString(text)
	if !refusal && !usage {
		return
	}
	usageRefusalMu.Lock()
	defer usageRefusalMu.Unlock()
	usageSawRefusal = usageSawRefusal || refusal
	usageSawUsage = usageSawUsage || usage
}

// usageReset starts a verb with nothing seen.
func usageReset() {
	usageRefusalMu.Lock()
	defer usageRefusalMu.Unlock()
	usageSawRefusal, usageSawUsage = false, false
}

// usageAfterRefusal prints the usage of the command args name when the verb
// just refused an unknown flag (exit 2) without printing a usage.
func usageAfterRefusal(args []string, code int) {
	usageRefusalMu.Lock()
	need := code == 2 && usageSawRefusal && !usageSawUsage
	usageRefusalMu.Unlock()
	if !need {
		return
	}
	if usage := usageText(usageCommand(args)); usage != "" {
		fmt.Fprint(safeStderr, usage)
	}
}

// usageCommand is the help-table command args name: "block open" for
// `block open --x`, else the verb.
func usageCommand(args []string) string {
	if len(args) >= 2 && !strings.HasPrefix(args[1], "-") {
		if _, ok := helpLookup(args[0] + " " + args[1]); ok {
			return args[0] + " " + args[1]
		}
	}
	return args[0]
}

// usageText is the usage of one command of the help table, from its first
// "usage:" line up to the first blank line, ending in a newline; "" when
// the command prints none.
func usageText(name string) string {
	lines := []string{}
	for _, line := range strings.Split(helpCommandUsage(name), "\n") {
		if strings.TrimSpace(line) == "" {
			break
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 || !helpIsUsage(lines[0]) {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// usageUnknownFlag is the refusal of verbs whose other usage errors keep
// Node's "[code-forge:error]" form (models, validate, resolve): the line
// every verb prints, exit 2; the router adds the usage.
func usageUnknownFlag(verb, flag string) func(stdout, stderr io.Writer) int {
	return func(_, stderr io.Writer) int {
		fmt.Fprintf(stderr, "%s: unknown flag %s\n", verb, statusQuote(flag))
		return 2
	}
}
