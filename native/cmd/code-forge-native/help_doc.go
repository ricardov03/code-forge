package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// B648: docs/reference/commands.md comes from Go's help table, not from Node's
// USAGE exports. `code-forge help --commands-doc [--root <dir>]` writes the
// page; `--check [<file>]` exits 1 when the page (or that copy) is stale. The
// flag is for maintainers and stays out of the help text; scripts/gen-commands-doc.sh
// builds the binary from source and runs it.

const helpDocPath = "docs/reference/commands.md"

// helpDocSub is one sub-command section of the page.
type helpDocSub struct {
	name    string
	summary string
	usage   string
}

// helpDocVerb is one verb section of the page.
type helpDocVerb struct {
	verb    string
	purpose string
	usage   string
	subs    []helpDocSub
}

// helpDocCollect reads every verb (hidden ones too) and its sub-commands from
// helpCommands, with the usage each prints. A sub-command gets its own section
// when its usage differs from its verb's, or when it is the verb's only
// sub-command (plan check, jev ask). missing lists commands with no usage.
func helpDocCollect() ([]helpDocVerb, []string) {
	verbs, missing := []helpDocVerb{}, []string{}
	for _, entry := range helpCommands {
		if strings.Contains(entry.name, " ") {
			continue
		}
		usage := helpCommandUsage(entry.name)
		if strings.TrimSpace(usage) == "" {
			missing = append(missing, entry.name+".USAGE")
		}
		verb := helpDocVerb{verb: entry.name, purpose: entry.summary, usage: usage}
		subs := []helpEntry{}
		for _, sub := range helpCommands {
			if strings.HasPrefix(sub.name, entry.name+" ") {
				subs = append(subs, sub)
			}
		}
		for _, sub := range subs {
			text := helpCommandUsage(sub.name)
			if len(subs) > 1 && text == usage {
				continue
			}
			if strings.TrimSpace(text) == "" {
				missing = append(missing, sub.name+".USAGE")
			}
			summary := ""
			if !sub.hidden {
				summary = sub.summary
			}
			verb.subs = append(verb.subs, helpDocSub{name: sub.name, summary: summary, usage: text})
		}
		verbs = append(verbs, verb)
	}
	sort.SliceStable(verbs, func(i, j int) bool { return verbs[i].verb < verbs[j].verb })
	return verbs, missing
}

// helpDocRender is the page: the same layout scripts/gen-commands-doc.mjs wrote.
func helpDocRender(verbs []helpDocVerb) string {
	lines := []string{
		"# Command reference", "",
		"This page lists the CLI commands and their usage. It is generated from the Go help table. " +
			"Regenerate it with `sh scripts/gen-commands-doc.sh`.", "",
	}
	for _, verb := range verbs {
		lines = append(lines, "- ["+verb.verb+"](#"+verb.verb+")")
	}
	lines = append(lines, "")
	addUsage := func(usage string) {
		lines = append(lines, "```text", strings.TrimSuffix(usage, "\n"), "```", "")
	}
	for _, verb := range verbs {
		lines = append(lines, "## "+verb.verb, "", verb.purpose, "")
		addUsage(verb.usage)
		for _, sub := range verb.subs {
			lines = append(lines, "### "+sub.name, "")
			if sub.summary != "" {
				lines = append(lines, sub.summary, "")
			}
			addUsage(sub.usage)
		}
	}
	return strings.Join(lines, "\n")
}

// helpDocQuote is JSON.stringify of one line.
func helpDocQuote(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

// runHelpDoc is `help --commands-doc [--root <dir>] [--check [<file>]]`.
func runHelpDoc(args []string, stdout, stderr io.Writer) int {
	usage := "usage: code-forge help --commands-doc [--root <dir>] [--check [<file>]]\n"
	root, check, file := "", false, ""
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--root" && i+1 < len(args):
			i++
			root = args[i]
		case arg == "--check":
			check = true
		case check && file == "" && !strings.HasPrefix(arg, "-"):
			file = arg
		default:
			fmt.Fprintf(stderr, "help --commands-doc: unknown argument %s\n%s", statusQuote(arg), usage)
			return 2
		}
	}
	if root == "" {
		found, err := metaPackageRoot()
		if err != nil {
			fmt.Fprintf(stderr, "help --commands-doc: cannot find the package root: %v\n", err)
			return 1
		}
		root = found
	}
	out := filepath.Join(root, filepath.FromSlash(helpDocPath))
	if file != "" {
		out = file
	}
	verbs, missing := helpDocCollect()
	if len(missing) > 0 {
		fmt.Fprintf(stderr, "Missing CLI help metadata: %s\n", strings.Join(missing, ", "))
		return 1
	}
	generated := helpDocRender(verbs)
	if !check {
		if err := os.WriteFile(out, []byte(generated), 0o644); err != nil {
			fmt.Fprintf(stderr, "help --commands-doc: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "wrote "+helpDocPath)
		return 0
	}
	data, err := os.ReadFile(out)
	if err != nil {
		fmt.Fprintln(stderr, "gen-commands-doc --check: command reference does not exist")
		return 1
	}
	if string(data) == generated {
		fmt.Fprintln(stdout, "gen-commands-doc --check: command reference is up to date")
		return 0
	}
	actual, expected := strings.Split(string(data), "\n"), strings.Split(generated, "\n")
	index := 0
	for index < len(actual) && index < len(expected) && actual[index] == expected[index] {
		index++
	}
	line := func(lines []string) string {
		if index < len(lines) {
			return helpDocQuote(lines[index])
		}
		return helpDocQuote("<eof>")
	}
	fmt.Fprintf(stderr, "gen-commands-doc --check: command reference is stale (first difference at line %d)\n  on disk:    %s\n  generated:  %s\n",
		index+1, line(actual), line(expected))
	return 1
}
