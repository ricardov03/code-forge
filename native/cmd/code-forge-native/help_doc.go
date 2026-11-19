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

// helpDocJSONPath is the same reference as data (B801), for agents.
const helpDocJSONPath = "docs/reference/commands.json"

// helpDocSub is one sub-command section of the page.
type helpDocSub struct {
	entry   helpEntry
	name    string
	summary string
	usage   string
}

// helpDocVerb is one verb section of the page.
type helpDocVerb struct {
	entry   helpEntry
	verb    string
	purpose string
	usage   string
	subs    []helpDocSub
}

// helpDocCollect reads every verb (hidden ones too) and its sub-commands from
// helpCommands, with the usage each prints. Every sub-command gets its own
// section (B801: each command has its own reference, even when its usage is
// its verb's). missing lists commands with no usage.
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
		verb := helpDocVerb{entry: entry, verb: entry.name, purpose: entry.summary, usage: usage}
		subs := []helpEntry{}
		for _, sub := range helpCommands {
			if strings.HasPrefix(sub.name, entry.name+" ") {
				subs = append(subs, sub)
			}
		}
		for _, sub := range subs {
			text := helpCommandUsage(sub.name)
			if strings.TrimSpace(text) == "" {
				missing = append(missing, sub.name+".USAGE")
			}
			summary := ""
			if !sub.hidden {
				summary = sub.summary
			}
			verb.subs = append(verb.subs, helpDocSub{entry: sub, name: sub.name, summary: summary, usage: text})
		}
		verbs = append(verbs, verb)
	}
	sort.SliceStable(verbs, func(i, j int) bool { return verbs[i].verb < verbs[j].verb })
	return verbs, missing
}

// helpDocRender is the page: the layout scripts/gen-commands-doc.mjs wrote,
// and since B801 each section's reference after its usage block. The same
// data goes to commands.json (the second value).
func helpDocRender(verbs []helpDocVerb) (string, string) {
	rec := helpRefLoad()
	cmds := []helpRefCommand{}
	lines := []string{
		"# Command reference", "",
		"This page lists every CLI command: its purpose, when to use it, its usage and flags, " +
			"examples with their real output, exit codes, the `--json` shape and related commands. " +
			"It is generated from the Go help table; agents read the same data in " +
			"[`commands.json`](commands.json). Regenerate both with `sh scripts/gen-commands-doc.sh`.", "",
	}
	addRef := func(entry helpEntry, usage string) {
		if _, ok := helpRefs[entry.name]; !ok {
			return
		}
		cmd := helpRefDocCommand(entry, usage, rec)
		cmds = append(cmds, cmd)
		lines = append(lines, helpRefMarkdown(cmd)...)
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
		addRef(verb.entry, verb.usage)
		for _, sub := range verb.subs {
			lines = append(lines, "### "+sub.name, "")
			if sub.summary != "" {
				lines = append(lines, sub.summary, "")
			}
			addUsage(sub.usage)
			addRef(sub.entry, sub.usage)
		}
	}
	return strings.Join(lines, "\n"), helpRefJSONDoc(cmds)
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
	generated, generatedJSON := helpDocRender(verbs)
	jsonOut := filepath.Join(root, filepath.FromSlash(helpDocJSONPath))
	if !check {
		for _, f := range []struct{ path, text, name string }{{out, generated, helpDocPath}, {jsonOut, generatedJSON, helpDocJSONPath}} {
			if err := os.WriteFile(f.path, []byte(f.text), 0o644); err != nil {
				fmt.Fprintf(stderr, "help --commands-doc: %v\n", err)
				return 1
			}
			fmt.Fprintln(stdout, "wrote "+f.name)
		}
		return 0
	}
	data, err := os.ReadFile(out)
	if err != nil {
		fmt.Fprintln(stderr, "gen-commands-doc --check: command reference does not exist")
		return 1
	}
	if string(data) == generated {
		// A copy given by path checks the page only; the package check
		// covers commands.json too.
		if file == "" {
			if have, err := os.ReadFile(jsonOut); err != nil || string(have) != generatedJSON {
				fmt.Fprintln(stderr, "gen-commands-doc --check: "+helpDocJSONPath+" is stale or missing; run sh scripts/gen-commands-doc.sh")
				return 1
			}
		}
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
