package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// B613: the one table behind `code-forge help` (the Artisan-style list),
// `code-forge help <command>`, `<command> --help` and `code-forge help --json`.
// Every routed verb and every sub-verb of a dispatcher has one plain line here
// (help_list_test.go fails on a verb without one).

// helpOption is one line of a command's Options section.
type helpOption struct{ flag, text string }

// helpEntry is one command: a verb ("status") or a verb and sub-verb ("block open").
type helpEntry struct {
	name    string
	summary string
	// owner marks a command only the human owner runs (it asks at a terminal).
	owner bool
	// hidden keeps an internal command out of the list; it still runs.
	hidden bool
	// namespace marks a verb that only dispatches to its sub-verbs: it gives
	// the group heading in the list and is not a row of its own.
	namespace bool
	options   []helpOption
	// usage is the usage text of a command Go does not route (B648: land);
	// a routed command's usage comes from its own help form.
	usage string
}

var helpJSONOption = helpOption{"--json", "Print one JSON line instead of text"}
var helpRunOption = helpOption{"--run <r>", "The run id"}

var helpCommands = []helpEntry{
	{name: "author", summary: "Write or harden a plan draft from a brief"},
	{name: "autopilot", summary: "Let a delegate decide for the owner within a grant", namespace: true},
	{name: "autopilot start", summary: "Grant the autopilot a time window, scopes and budget"},
	{name: "autopilot status", summary: "Show the grant, spend per category and pauses"},
	{name: "autopilot stop", summary: "End the autopilot grant now"},
	{name: "autopilot extend", summary: "Move the grant end time or deadline later", owner: true},
	{name: "autopilot ask", summary: "Ask the delegate one question inside the grant"},
	{name: "autopilot waive", summary: "Waive a warning or nit finding by a decision", owner: true},
	{name: "autopilot round", summary: "Record one extra review round for a file"},
	{name: "autopilot level", summary: "Record the lane (level) the delegate chose"},
	{name: "autopilot approve", summary: "Change one setting until a set time", owner: true},
	{name: "autopilot binnacle", summary: "Print or write the autopilot run summary"},
	{name: "autopilot log", summary: "Print every autopilot event of a run"},
	{name: "block", summary: "Open, check, close and land the blocks of a run", namespace: true,
		options: []helpOption{helpJSONOption}},
	{name: "block open", summary: "Start a block: owned files, acceptance tests, brief",
		options: []helpOption{helpRunOption, {"--owned <paths…>", "The files the block may change"}, {"--acceptance <file>", "The clauses and their tests"}, {"--brief <file>", "The brief file in the repository"}, helpJSONOption}},
	{name: "block attempt", summary: "Start the next attempt of an open block"},
	{name: "block rebase", summary: "Move a block onto the new base of its branch",
		options: []helpOption{helpRunOption, {"--accept-landed", "Rebase onto what landed on the base branch"}, {"--accept-changed <file>…", "Keep a changed file (owner)"}, helpJSONOption}},
	{name: "block claim", summary: "Add one more owned path to an open block"},
	{name: "block ready", summary: "List what still blocks the close of a block"},
	{name: "block settle", summary: "Run the orchestrator fixes that clear a block, then report"},
	{name: "block close", summary: "Run the final checks and close the block",
		options: []helpOption{helpRunOption, {"--transcript <file>", "The coder transcript to check"}, {"--report <file>", "The coder report"}, {"--decide", "Clear a transcript hit by a decision (owner)"}, helpJSONOption}},
	{name: "block land", summary: "Join reviews, run gates, close, then merge",
		options: []helpOption{helpRunOption, {"--merge", "Run the merge gate and merge with --no-ff"}, {"--push", "Push main after the merge"}, {"--watch-ci", "Wait for CI on the pushed commit"}, helpJSONOption}},
	{name: "block stop", summary: "Stop a block and record why"},
	{name: "block waive", summary: "Accept one review finding without a fix", owner: true},
	{name: "block reopen", summary: "Reopen a stopped file review for a new round", owner: true},
	{name: "block accept-hit", summary: "Accept one transcript rule hit as safe", owner: true},
	{name: "block unwaive", summary: "Undo an agent waiver of a finding", owner: true},
	{name: "block ack-waivers", summary: "Acknowledge the agent waivers of a block", owner: true},
	{name: "block human-pass", summary: "Record the owner's device check and close", owner: true},
	{name: "block amend-acceptance", summary: "Change the acceptance clauses of a block", owner: true},
	{name: "ci", summary: "Watch the CI runs of one commit", namespace: true},
	{name: "ci watch", summary: "Wait for the CI runs of a commit; name failed tests"},
	{name: "cloud-coder", summary: "Send a block to the cloud coder and commit its patch"},
	{name: "context", summary: "Build the context a coder gets", namespace: true},
	{name: "context pack", summary: "Build the context pack of a block"},
	{name: "decisions", summary: "Show the decisions the autopilot signed", namespace: true},
	{name: "decisions list", summary: "List the close decisions the autopilot signed"},
	{name: "docs", summary: "Get the docs of the installed version", namespace: true},
	{name: "docs sync", summary: "Fetch the docs for the installed version"},
	{name: "docs path", summary: "Print the path of the best local docs copy"},
	{name: "doctor", summary: "Check this machine and project are ready",
		options: []helpOption{{"--quick", "Run the quick checks only"}, helpJSONOption, {"--cwd <dir>", "The project folder"}}},
	{name: "facts", summary: "Build a facts sheet from a brief and the sources"},
	{name: "gates", summary: "Run the project gates and the safety checks", namespace: true},
	{name: "gates detect", summary: "Find the test, lint, type and format commands"},
	{name: "gates run", summary: "Run the gates for a block or the project",
		options: []helpOption{helpRunOption, {"--block <id>", "The block to gate"}, {"--changed", "Run only the tests the change touches"}, {"--full", "Run the full suite"}}},
	{name: "gates secret-scan", summary: "Check files for secrets"},
	{name: "gates safe-edit", summary: "Check the change keeps protected files safe"},
	{name: "gates scope", summary: "Check the change stays inside the owned files"},
	{name: "gates acceptance", summary: "Check each acceptance clause has a passing test"},
	{name: "gates transcript-grep", summary: "Check a coder transcript for forbidden commands"},
	{name: "heal", summary: "Retry failures that pass by themselves", namespace: true},
	{name: "heal list", summary: "List the heal queue"},
	{name: "heal show", summary: "Show one heal item"},
	{name: "heal run", summary: "Retry the heal items of a run"},
	{name: "help", summary: "Show the commands, or the help of one command",
		options: []helpOption{{"--json", "Print the command list as one JSON line"}}},
	{name: "init", summary: "Set up code-forge in this project"},
	{name: "jev", summary: "Ask Jev a recorded decision question", namespace: true},
	{name: "jev ask", summary: "Ask Jev one decision question"},
	{name: "keys", summary: "Store, list, test or remove credential references", namespace: true},
	{name: "keys list", summary: "List the stored credential references"},
	{name: "keys set", summary: "Store a credential reference"},
	{name: "keys test", summary: "Check a credential reference works"},
	{name: "keys remove", summary: "Remove a stored credential reference"},
	{name: "land", summary: "Landing code that block land uses", hidden: true,
		usage: "usage: code-forge land\n  Used internally by code-forge block land; this module has no standalone handler.\n"},
	{name: "ledger", summary: "Read the ledger or record outcomes and spend", namespace: true},
	{name: "ledger calibration", summary: "Show how reliable the recorded answers were"},
	{name: "ledger outcome", summary: "Record the CI result of a pull request"},
	{name: "ledger tail", summary: "Print the last ledger rows"},
	{name: "ledger add", summary: "Add coder spend that code-forge did not see"},
	{name: "ledger outside-review", summary: "Record an outside review of a pull request"},
	{name: "list", summary: "List the skill installations and harness paths"},
	{name: "logs", summary: "Show the last local CLI errors"},
	{name: "logs summary", summary: "Count the CLI errors of the last days"},
	{name: "logs clear", summary: "Delete the local CLI error log"},
	{name: "logs path", summary: "Print the path of the CLI error log"},
	{name: "logs report", summary: "Report CLI errors as a GitHub issue"},
	{name: "models", summary: "List the provider model catalogs"},
	{name: "native-status", summary: "Daemon companion for run status", hidden: true},
	{name: "outcome", summary: "Seal, check or record a specialist outcome file", namespace: true},
	{name: "outcome seal", summary: "Fill the file hashes of an outcome file"},
	{name: "outcome check", summary: "Check an outcome file and its files"},
	{name: "outcome record", summary: "Check an outcome and save it in the ledger"},
	{name: "pipeline", summary: "Show the steps of a pipeline", namespace: true},
	{name: "pipeline status", summary: "Show the state of each pipeline step"},
	{name: "pipeline next", summary: "List the pipeline steps ready to run"},
	{name: "plan", summary: "Check a plan before coding", namespace: true},
	{name: "plan check", summary: "Check a plan against its facts and rules"},
	{name: "proof", summary: "Prove the tests: tiers, exports and red-green", namespace: true},
	{name: "proof tier", summary: "Show the proof tier of a file"},
	{name: "proof export", summary: "Export a block's tests to a clean copy"},
	{name: "proof lock", summary: "Lock a block's test files"},
	{name: "proof unlock", summary: "Unlock a block's test files"},
	{name: "proof restore", summary: "Put back the files a proof changed"},
	{name: "proof red-green", summary: "Prove a test fails without the fix"},
	{name: "queue", summary: "Show the machine-wide test queue"},
	{name: "queue status", summary: "Show the test queue (the same as queue)"},
	{name: "queue run", summary: "Run a test command in a queue slot"},
	{name: "remove", summary: "Remove skill installations"},
	{name: "report", summary: "Show or export the project ledger report"},
	{name: "resolve", summary: "Show the model, effort and CLI of a level"},
	{name: "review", summary: "Review changed files against intent or acceptance"},
	{name: "review-file", summary: "Queue a file review or wait for the results"},
	{name: "run", summary: "Start, check, reload or end a run", namespace: true},
	{name: "run start", summary: "Start a run, or add or reattach a worker"},
	{name: "run status", summary: "Show the state of a run"},
	{name: "run reload", summary: "Load the changed settings into a run"},
	{name: "run end", summary: "End a run"},
	{name: "run human-checks", summary: "List the open human checks of a run, or clear one (owner)"},
	{name: "s2", summary: "Answer a decision packet with a System 2 session"},
	{name: "setup", summary: "Check the Go binary for this system"},
	{name: "spawn", summary: "Run a model session at a level and role"},
	{name: "status", summary: "Show the active runs and their blocks",
		options: []helpOption{helpRunOption, helpJSONOption}},
	{name: "sweep", summary: "Find stale processes; stop them with --apply"},
	{name: "tools", summary: "List the tools code-forge found"},
	{name: "tools install", summary: "Install selected tools after you say yes"},
	{name: "upgrade", summary: "Refresh the skill installations"},
	{name: "validate", summary: "Check the project configuration"},
	{name: "version", summary: "Show the version"},
	{name: "worker", summary: "Serve a run's review queue"},
}

// helpLookup returns the entry of one command name.
func helpLookup(name string) (helpEntry, bool) {
	for _, entry := range helpCommands {
		if entry.name == name {
			return entry, true
		}
	}
	return helpEntry{}, false
}

// helpGroup is the namespace of a command: its verb when the verb has
// sub-verbs, "" for a top-level verb.
func helpGroup(name string) string {
	verb, _, _ := strings.Cut(name, " ")
	if entry, ok := helpLookup(verb); ok && entry.namespace {
		return verb
	}
	for _, entry := range helpCommands {
		if strings.HasPrefix(entry.name, verb+" ") {
			return verb
		}
	}
	return ""
}

// helpColor is Artisan's coloring: only for a terminal and with NO_COLOR unset.
var helpColor = func() bool {
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return false
	}
	info, err := os.Stdout.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

type helpPainter bool

func (p helpPainter) heading(s string) string {
	if p {
		return "\x1b[33m" + s + "\x1b[39m"
	}
	return s
}

func (p helpPainter) name(s string) string {
	if p {
		return "\x1b[32m" + s + "\x1b[39m"
	}
	return s
}

// helpRow pads a name to width before its text.
func helpRow(p helpPainter, name string, width int, text string) string {
	return "  " + p.name(name) + strings.Repeat(" ", width-len([]rune(name))+2) + text + "\n"
}

func helpLine(entry helpEntry) string {
	if entry.owner {
		return entry.summary + " (owner)"
	}
	return entry.summary
}

// helpTitle is "<name> <version>" from package.json, or the build version.
func helpTitle() string {
	if root, err := metaPackageRoot(); err == nil {
		var pkg struct{ Name, Version string }
		if data, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil && json.Unmarshal(data, &pkg) == nil && pkg.Name != "" && pkg.Version != "" {
			return pkg.Name + " " + pkg.Version
		}
	}
	return "code-forge " + version
}

// helpListText is `code-forge help`: the Artisan `list` page.
func helpListText(p helpPainter) string {
	var b strings.Builder
	b.WriteString(helpTitle() + "\n\n")
	b.WriteString(p.heading("Usage:") + "\n  code-forge <command> [options] [arguments]\n\n")
	b.WriteString(p.heading("Options:") + "\n")
	b.WriteString("  " + p.name("-h, --help") + "     Show help for a command\n")
	b.WriteString("  " + p.name("-v, --version") + "  Show the version\n")
	b.WriteString("      " + p.name("--json") + "     Print one JSON line (commands that support it)\n\n")
	groups := map[string][]helpEntry{}
	width := 0
	for _, entry := range helpCommands {
		if entry.hidden || entry.namespace {
			continue
		}
		group := helpGroup(entry.name)
		groups[group] = append(groups[group], entry)
		if n := len([]rune(entry.name)); n > width {
			width = n
		}
	}
	names := make([]string, 0, len(groups))
	for group := range groups {
		names = append(names, group)
	}
	sort.Strings(names)
	b.WriteString(p.heading("Available commands:") + "\n")
	for _, group := range names {
		entries := groups[group]
		sort.SliceStable(entries, func(i, j int) bool {
			// The bare verb (logs, queue, tools) comes first in its group.
			if (entries[i].name == group) != (entries[j].name == group) {
				return entries[i].name == group
			}
			return entries[i].name < entries[j].name
		})
		if group != "" {
			b.WriteString(" " + p.heading(group) + "\n")
		}
		for _, entry := range entries {
			b.WriteString(helpRow(p, entry.name, width, helpLine(entry)))
		}
	}
	b.WriteString("\nRun code-forge help <command> (or <command> --help) for its usage.\n")
	b.WriteString("Configuration fallback entries must be {provider, model} objects, not model-name strings (optional effort).\n")
	b.WriteString("Test queue: gates wait in one machine-wide FIFO queue; wrap other test commands in `code-forge queue run -- <cmd>`.\n")
	return b.String()
}

// helpJSONRow is one row of `code-forge help --json`.
type helpJSONRow struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
	Owner   bool   `json:"owner"`
	Group   string `json:"group"`
}

// helpJSONRows lists every command that is not hidden, sorted by name.
func helpJSONRows() []helpJSONRow {
	rows := []helpJSONRow{}
	for _, entry := range helpCommands {
		if entry.hidden {
			continue
		}
		rows = append(rows, helpJSONRow{Name: entry.name, Summary: entry.summary, Owner: entry.owner, Group: helpGroup(entry.name)})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows
}

// helpPage is the Artisan `help <command>` page: Description, the command's
// own usage text (unchanged, two spaces in) and its Options.
func helpPage(p helpPainter, entry helpEntry, usage string) string {
	var b strings.Builder
	b.WriteString(p.heading("Description:") + "\n  " + helpLine(entry) + "\n")
	if strings.TrimSpace(usage) == "" && entry.namespace {
		usage = "code-forge " + entry.name + " <command> [options] [arguments]\n"
	}
	if strings.TrimSpace(usage) != "" {
		b.WriteString("\n" + p.heading("Usage:") + "\n")
		for _, line := range strings.Split(strings.TrimRight(usage, "\n"), "\n") {
			if line == "" {
				b.WriteString("\n")
				continue
			}
			b.WriteString("  " + line + "\n")
		}
	}
	if !strings.Contains(entry.name, " ") {
		rows, width := []helpEntry{}, 0
		for _, sub := range helpCommands {
			if strings.HasPrefix(sub.name, entry.name+" ") && !sub.hidden {
				rows = append(rows, sub)
				if n := len([]rune(sub.name)); n > width {
					width = n
				}
			}
		}
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].name < rows[j].name })
		if len(rows) > 0 {
			b.WriteString("\n" + p.heading("Available commands:") + "\n")
			for _, sub := range rows {
				b.WriteString(helpRow(p, sub.name, width, helpLine(sub)))
			}
		}
	}
	options := append([]helpOption{}, entry.options...)
	options = append(options, helpOption{"-h, --help", "Show this help"})
	width := 0
	for _, option := range options {
		if n := len([]rune(option.flag)); n > width {
			width = n
		}
	}
	b.WriteString("\n" + p.heading("Options:") + "\n")
	for _, option := range options {
		b.WriteString(helpRow(p, option.flag, width, option.text))
	}
	return b.String()
}

// helpTarget names the command of a help form: `<verb> --help` or
// `<verb> <sub> --help` (also -h), the forms that print usage.
func helpTarget(args []string) (string, bool) {
	if len(args) >= 2 && metaIsHelp(args[1]) {
		if _, ok := helpLookup(args[0]); ok {
			return args[0], true
		}
	}
	if len(args) >= 3 && metaIsHelp(args[2]) {
		if _, ok := helpLookup(args[0] + " " + args[1]); ok {
			return args[0] + " " + args[1], true
		}
	}
	return "", false
}

// helpCapture runs fn with os.Stdout (and os.Stderr when both) sent to pipes
// and returns what it wrote. A hand-over inside fn execs Node on the real file
// descriptors, so nothing is lost there.
func helpCapture(both bool, fn func() int) (int, string, string) {
	read := func(r *os.File, out *strings.Builder, done chan struct{}) {
		_, _ = io.Copy(out, r)
		close(done)
	}
	var outText, errText strings.Builder
	outR, outW, err := os.Pipe()
	if err != nil {
		return fn(), "", ""
	}
	outDone := make(chan struct{})
	go read(outR, &outText, outDone)
	savedOut, savedErr := os.Stdout, os.Stderr
	os.Stdout = outW
	var errR, errW *os.File
	errDone := make(chan struct{})
	if both {
		if errR, errW, err = os.Pipe(); err == nil {
			go read(errR, &errText, errDone)
			os.Stderr = errW
		} else {
			both = false
		}
	}
	code := fn()
	os.Stdout, os.Stderr = savedOut, savedErr
	_ = outW.Close()
	<-outDone
	_ = outR.Close()
	if both {
		_ = errW.Close()
		<-errDone
		_ = errR.Close()
	}
	return code, outText.String(), errText.String()
}

// helpIsUsage is true for text a verb's help form printed: it starts with "usage:".
func helpIsUsage(text string) bool {
	return strings.HasPrefix(strings.ToLower(text), "usage:")
}

// helpWrap runs a routed help form and adds the Description and Options
// around the usage text the verb prints. Any other result (an error, a
// refusal) is passed through unchanged.
func helpWrap(name string, args []string, route func([]string) int) int {
	entry, _ := helpLookup(name)
	code, stdout, _ := helpCapture(false, func() int { return route(args) })
	if code != 0 || !helpIsUsage(stdout) {
		fmt.Fprint(os.Stdout, stdout)
		return code
	}
	fmt.Fprint(os.Stdout, helpPage(helpPainter(helpColor()), entry, stdout))
	return code
}

// helpCommandUsage is the usage text of one command, read from its help form
// with logging off, so `help <command>` leaves no entry of that command.
func helpCommandUsage(name string) string {
	words := strings.Fields(name)
	route, ok := routes[words[0]]
	if !ok {
		entry, _ := helpLookup(name)
		return entry.usage
	}
	// Logging off, and a hand-over (block --help) fails here instead of
	// running Node: the page then shows the sub-commands without usage text.
	for key, value := range map[string]string{"CODE_FORGE_NO_ERROR_LOG": "1", "CODE_FORGE_HANDOVER_LOG": "0", "CODE_FORGE_NODE": "/nonexistent/code-forge-help-probe"} {
		saved, had := os.LookupEnv(key)
		os.Setenv(key, value)
		defer func(key, saved string, had bool) {
			if had {
				os.Setenv(key, saved)
			} else {
				os.Unsetenv(key)
			}
		}(key, saved, had)
	}
	_, stdout, stderr := helpCapture(true, func() int { return route(append(words[1:], "--help")) })
	if helpIsUsage(stdout) {
		return stdout
	}
	// A verb that refuses --help (status) still prints its usage on stderr.
	for i, line := range strings.SplitAfter(stderr, "\n") {
		if helpIsUsage(line) {
			return strings.Join(strings.SplitAfter(stderr, "\n")[i:], "")
		}
	}
	return ""
}
