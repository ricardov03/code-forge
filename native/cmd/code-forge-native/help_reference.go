package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// B801: the API reference of every command. help_list.go names each command
// and its one-line summary; helpRefs (help_reference_data.go) adds when to use
// it, examples with their real output, exit codes, the --json shape and the
// related commands. Flags come from the command's own usage text, so they
// never drift from what the command accepts; their meaning comes from
// helpRefFlagMeanings or a command's own flags map. `help --commands-doc`
// renders all of it into docs/reference/commands.md and commands.json, and
// TestB801DocExamplesRun runs every example against the real binary.

// helpRefExample is one example command. Its exit code and output are not
// written by hand: TestB801DocExamplesRun runs it and, with
// CODE_FORGE_B801_RECORD=1, records them in help_reference_examples.json.
type helpRefExample struct {
	// state is the sandbox step the example runs in (see helpRefStates).
	state string
	// args follow "code-forge"; {run}, {head}, {until} and {until2} are
	// filled in when the example runs.
	args string
}

// helpRefStates is the order the example states run in, in one sandbox repo:
// none (a repo with a config and no run), outside (a git repo with no config),
// start (the step that starts the run), run (a live run), open (the step that
// opens block B1), block (B1 open), apstart (the step that starts a grant),
// autopilot (a live grant), stop (grant and block stopped), end (the run ends).
var helpRefStates = []string{"none", "outside", "start", "run", "open", "block", "apstart", "autopilot", "stop", "end"}

// helpRef is the reference part of one command.
type helpRef struct {
	when    string
	related []string
	// flags gives this command's meaning of a flag where it differs from
	// helpRefFlagMeanings (or where that has none).
	flags map[string]string
	// exits gives the meaning of an exit code where the default does not fit.
	exits map[int]string
	// noJSONRun says why no example runs --json, for a command that takes it
	// but whose output would show the machine's own state.
	noJSONRun string
	examples  []helpRefExample
}

// helpRefRecord is one recorded example run.
type helpRefRecord struct {
	Args string `json:"args"`
	Exit int    `json:"exit"`
	Out  string `json:"out"`
}

// helpRefRecorded is help_reference_examples.json.
type helpRefRecorded struct {
	Version  int                               `json:"version"`
	Commands map[string]helpRefRecordedCommand `json:"commands"`
}

type helpRefRecordedCommand struct {
	Examples []helpRefRecord   `json:"examples"`
	JSON     *helpRefJSONShape `json:"json,omitempty"`
}

//go:embed help_reference_examples.json
var helpRefRecordedData []byte

// helpRefLoad reads the recorded examples (empty when the file is empty).
func helpRefLoad() helpRefRecorded {
	rec := helpRefRecorded{Commands: map[string]helpRefRecordedCommand{}}
	if len(bytes.TrimSpace(helpRefRecordedData)) > 0 {
		_ = json.Unmarshal(helpRefRecordedData, &rec)
	}
	if rec.Commands == nil {
		rec.Commands = map[string]helpRefRecordedCommand{}
	}
	return rec
}

// helpRefFind is the recorded run of one example.
func helpRefFind(rec helpRefRecorded, name, args string) (helpRefRecord, bool) {
	for _, r := range rec.Commands[name].Examples {
		if r.Args == args {
			return r, true
		}
	}
	return helpRefRecord{}, false
}

// helpRefTakesJSON reports whether a command's usage offers --json. When a
// usage line names the command itself, only those lines count (queue run
// shares the queue usage, but only queue status takes --json).
func helpRefTakesJSON(name, usage string) bool {
	lines, own := helpRefUsageLines(usage), []string{}
	for _, line := range lines {
		if strings.Contains(line+" ", "code-forge "+name+" ") {
			own = append(own, line)
		}
	}
	if len(own) > 0 && strings.Contains(name, " ") {
		lines = own
	}
	for _, line := range lines {
		if helpRefJSONFlagRe.MatchString(line) {
			return true
		}
	}
	return false
}

// helpRefShape reads the JSON shape of a command's output: the keys of the
// object (or of the first element of an array) with their JSON types, in
// output order. ok is false when the output is not JSON.
func helpRefShape(out string) (*helpRefJSONShape, bool) {
	text := strings.TrimSpace(out)
	if text == "" || (text[0] != '{' && text[0] != '[') {
		return nil, false
	}
	dec := json.NewDecoder(strings.NewReader(text))
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return nil, false
	}
	shape := &helpRefJSONShape{Kind: "object", Lines: "one", Keys: []helpRefKey{}}
	if strings.Contains(strings.TrimSpace(string(raw)), "\n") {
		shape.Lines = "several"
	}
	if raw[0] == '[' {
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return nil, false
		}
		shape.Kind = "array"
		if len(items) == 0 || items[0][0] != '{' {
			return shape, true
		}
		shape.Kind = "array of objects"
		raw = items[0]
	}
	keys, err := helpRefObjectKeys(raw)
	if err != nil {
		return nil, false
	}
	shape.Keys = keys
	return shape, true
}

// helpRefObjectKeys lists an object's keys and value types in order.
func helpRefObjectKeys(raw json.RawMessage) ([]helpRefKey, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	keys := []helpRefKey{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := tok.(string)
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		keys = append(keys, helpRefKey{Key: key, Type: helpRefValueType(value)})
	}
	return keys, nil
}

// helpRefValueType names a JSON value's type.
func helpRefValueType(value json.RawMessage) string {
	v := bytes.TrimSpace(value)
	switch {
	case len(v) == 0:
		return "null"
	case v[0] == '{':
		return "object"
	case v[0] == '[':
		if bytes.Equal(bytes.Join(bytes.Fields(v), nil), []byte("[]")) {
			return "array (empty in the example)"
		}
		return "array"
	case v[0] == '"':
		return "string"
	case v[0] == 't' || v[0] == 'f':
		return "boolean"
	case v[0] == 'n':
		return "null"
	}
	return "number"
}

// helpRefFlag is one flag of a command, read from its usage text.
type helpRefFlag struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Type    string `json:"type"`
	Default string `json:"default"`
	Meaning string `json:"meaning"`
}

var (
	helpRefFlagRe     = regexp.MustCompile(`--[a-z0-9][a-z0-9-]*`)
	helpRefJSONFlagRe = regexp.MustCompile(`(^|[ \[|])--json\b`)
	helpRefNumberRe   = regexp.MustCompile(`^(<n>|n|<count>|<pid>|<seconds>|<seconds>s|<usd>|<0-3>|<1-24>|<0\.\.1>)$`)
)

// helpRefUsageLines keeps the usage lines of a usage text: option notes and
// the list of sub-commands are not usage.
func helpRefUsageLines(usage string) []string {
	out := []string{}
	for _, line := range strings.Split(usage, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "Available commands:") {
			break
		}
		if strings.HasPrefix(trim, "usage:") || strings.HasPrefix(trim, "code-forge ") || strings.HasPrefix(trim, "[") || strings.HasPrefix(trim, "|") {
			out = append(out, trim)
		}
	}
	return out
}

// helpRefValue reads the value placeholder after a flag at text[at:].
func helpRefValue(text string, at int) string {
	if at >= len(text) || (text[at] != ' ' && text[at] != '=') {
		return ""
	}
	rest := text[at+1:]
	end, depth, square := 0, 0, 0
	for end < len(rest) {
		c := rest[end]
		if c == '<' {
			depth++
		} else if c == '>' && depth > 0 {
			depth--
		} else if c == '[' && end > 0 {
			square++
		} else if c == ']' && square > 0 {
			square--
		} else if depth == 0 && (c == ' ' || c == ']' || (c == '|' && end+1 < len(rest) && rest[end+1] == '-')) {
			break
		}
		end++
	}
	value := rest[:end]
	switch {
	case value == "" || value == "|" || strings.HasPrefix(value, "-"):
		return ""
	case strings.Contains(value, "<"), strings.HasPrefix(value, "\""), strings.ContainsAny(value, "|,=…"):
		return value
	case value[0] >= '0' && value[0] <= '9':
		return value
	case strings.ToUpper(value) == value:
		return value
	}
	return ""
}

// helpRefFlags lists the flags of a command in usage order (--help left out).
func helpRefFlags(name, usage string) []helpRefFlag {
	flags, seen := []helpRefFlag{}, map[string]int{}
	for _, line := range helpRefUsageLines(usage) {
		for _, loc := range helpRefFlagRe.FindAllStringIndex(line, -1) {
			if loc[0] > 0 && line[loc[0]-1] != ' ' && line[loc[0]-1] != '[' && line[loc[0]-1] != '|' && line[loc[0]-1] != '(' {
				continue
			}
			flag := line[loc[0]:loc[1]]
			if flag == "--help" {
				continue
			}
			value := helpRefValue(line, loc[1])
			if i, ok := seen[flag]; ok {
				if flags[i].Value == "" && value != "" {
					flags[i].Value = value
				}
				continue
			}
			seen[flag] = len(flags)
			flags = append(flags, helpRefFlag{Name: flag, Value: value})
		}
	}
	for i := range flags {
		flags[i].Type = helpRefFlagType(flags[i].Value)
		flags[i].Default = "not set"
		if flags[i].Type == "bool" {
			flags[i].Default = "off"
		}
		flags[i].Meaning = helpRefFlagMeaning(name, flags[i].Name)
	}
	return flags
}

// helpRefFlagType names the kind of value a flag takes.
func helpRefFlagType(value string) string {
	v := strings.ToLower(value)
	switch {
	case value == "":
		return "bool"
	case strings.Contains(v, "iso-8601"):
		return "time"
	case strings.Contains(v, "…") || strings.Contains(v, "...") || strings.Contains(v, "a,b"):
		return "list"
	case v == "<dur>" || v == "30m":
		return "duration"
	case v == "<dot.path>":
		return "string"
	case strings.Contains(v, "file") || strings.Contains(v, "dir") || strings.Contains(v, "path"):
		return "path"
	case helpRefNumberRe.MatchString(v):
		return "number"
	case !strings.HasPrefix(value, "<") && strings.Contains(value, "|"):
		return "enum"
	case strings.HasPrefix(value, "<") && strings.Contains(value, "|"):
		return "enum"
	}
	return "string"
}

// helpRefFlagMeaning is a command's own meaning of a flag, else the shared one.
func helpRefFlagMeaning(name, flag string) string {
	if ref, ok := helpRefs[name]; ok {
		if text, ok := ref.flags[flag]; ok {
			return text
		}
	}
	return helpRefFlagMeanings[flag]
}

// helpRefDefaultExits are the exit codes most commands share.
var helpRefDefaultExits = map[int]string{
	0: "Success.",
	1: "Refused or failed; the message says why.",
	2: "Usage error: a missing, unknown or bad argument (the usage is printed).",
}

// helpRefExitCodes lists 0, 2, every recorded example's code and the
// command's own.
func helpRefExitCodes(ref helpRef, recorded []helpRefRecord) []helpRefExit {
	codes := map[int]bool{0: true, 1: true, 2: true}
	for _, r := range recorded {
		codes[r.Exit] = true
	}
	for code := range ref.exits {
		codes[code] = true
	}
	out := []helpRefExit{}
	for code := range codes {
		text, ok := ref.exits[code]
		if !ok {
			text = helpRefDefaultExits[code]
		}
		out = append(out, helpRefExit{Code: code, Meaning: text})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

type helpRefExit struct {
	Code    int    `json:"code"`
	Meaning string `json:"meaning"`
}

type helpRefKey struct {
	Key  string `json:"key"`
	Type string `json:"type"`
}

// helpRefCommand is one command of commands.json.
type helpRefCommand struct {
	Name      string            `json:"name"`
	Purpose   string            `json:"purpose"`
	When      string            `json:"when"`
	Owner     bool              `json:"owner_only"`
	Hidden    bool              `json:"hidden"`
	Namespace bool              `json:"namespace"`
	Usage     string            `json:"usage"`
	Flags     []helpRefFlag     `json:"flags"`
	Examples  []helpRefDocEx    `json:"examples"`
	ExitCodes []helpRefExit     `json:"exit_codes"`
	JSON      *helpRefJSONShape `json:"json"`
	Related   []string          `json:"related"`
}

type helpRefDocEx struct {
	Command string `json:"command"`
	Exit    int    `json:"exit"`
	Output  string `json:"output"`
}

type helpRefJSONShape struct {
	Kind  string       `json:"kind"`
	Lines string       `json:"lines"`
	Keys  []helpRefKey `json:"keys"`
	Note  string       `json:"note,omitempty"`
}

// helpRefDocCommand is a command as the reference shows it.
func helpRefDocCommand(entry helpEntry, usage string, rec helpRefRecorded) helpRefCommand {
	ref := helpRefs[entry.name]
	recorded := []helpRefRecord{}
	for _, ex := range ref.examples {
		if r, ok := helpRefFind(rec, entry.name, ex.args); ok {
			recorded = append(recorded, r)
		}
	}
	cmd := helpRefCommand{Name: entry.name, Purpose: entry.summary + ".", When: ref.when, Owner: entry.owner,
		Hidden: entry.hidden, Namespace: entry.namespace, Usage: strings.TrimSuffix(usage, "\n"),
		Flags: []helpRefFlag{}, Examples: []helpRefDocEx{}, ExitCodes: helpRefExitCodes(ref, recorded), Related: ref.related}
	if cmd.Related == nil {
		cmd.Related = []string{}
	}
	if !entry.namespace {
		cmd.Flags = helpRefFlags(entry.name, usage)
	}
	for _, r := range recorded {
		cmd.Examples = append(cmd.Examples, helpRefDocEx{Command: helpRefShowArgs(r.Args), Exit: r.Exit, Output: r.Out})
	}
	if helpRefTakesJSON(entry.name, usage) {
		cmd.JSON = rec.Commands[entry.name].JSON
		if cmd.JSON == nil {
			cmd.JSON = &helpRefJSONShape{Kind: "none", Lines: "one", Keys: []helpRefKey{}}
			if ref.noJSONRun != "" {
				cmd.JSON.Note = "not run here: " + ref.noJSONRun
			}
		}
	}
	return cmd
}

// helpRefShowArgs is the example command line as the docs print it.
func helpRefShowArgs(args string) string {
	return "code-forge " + strings.NewReplacer("{run}", "<run>", "{head}", "<sha>", "{until}", "<time>", "{until2}", "<time>").Replace(args)
}

// helpRefAnchor is the GitHub heading anchor of a command section.
func helpRefAnchor(name string) string {
	return strings.ReplaceAll(name, " ", "-")
}

// helpRefMarkdown is the reference part of one command section, placed after
// its usage block. It uses #### headings only: the site reads "## " as a verb
// and "### " as a sub-command (native/cmd/cf-release/sitedata.go).
func helpRefMarkdown(cmd helpRefCommand) []string {
	lines := []string{"#### When to use", "", cmd.When, ""}
	if cmd.Owner {
		lines = append(lines, "Only the owner runs it, at a terminal.", "")
	}
	lines = append(lines, "#### Flags", "")
	if cmd.Namespace {
		lines = append(lines, "None of its own; see its sub-commands.", "")
	} else if len(cmd.Flags) == 0 {
		lines = append(lines, "None.", "")
	} else {
		lines = append(lines, "| Flag | Type | Default | Meaning |", "|---|---|---|---|")
		for _, f := range cmd.Flags {
			flag := f.Name
			if f.Value != "" {
				flag += " " + f.Value
			}
			lines = append(lines, "| `"+helpRefCell(flag)+"` | "+f.Type+" | "+f.Default+" | "+helpRefCell(f.Meaning)+" |")
		}
		lines = append(lines, "")
	}
	lines = append(lines, "#### Examples", "")
	for _, ex := range cmd.Examples {
		lines = append(lines, "```sh", ex.Command, "```", "", "Exit "+strconv.Itoa(ex.Exit)+". Output:", "")
		if ex.Output == "" {
			lines = append(lines, "(none)", "")
		} else {
			lines = append(lines, "```text", helpRefFenceSafe(ex.Output), "```", "")
		}
	}
	lines = append(lines, "#### Exit codes", "", "| Code | Meaning |", "|---|---|")
	for _, e := range cmd.ExitCodes {
		lines = append(lines, "| "+strconv.Itoa(e.Code)+" | "+helpRefCell(e.Meaning)+" |")
	}
	lines = append(lines, "")
	if cmd.JSON != nil {
		lines = append(lines, "#### JSON output", "")
		switch {
		case cmd.JSON.Kind == "none" && cmd.JSON.Note != "":
			lines = append(lines, "Takes `--json`; "+cmd.JSON.Note, "")
		case cmd.JSON.Kind == "none":
			lines = append(lines, "Takes `--json`; the examples above refuse, so no shape is recorded.", "")
		case len(cmd.JSON.Keys) == 0:
			lines = append(lines, "Prints JSON: an "+cmd.JSON.Kind+" (empty in the example).", "")
		default:
			text := "Prints one JSON line"
			if cmd.JSON.Lines == "several" {
				text = "Prints JSON over several lines (pretty-printed)"
			}
			lines = append(lines, text+": an "+cmd.JSON.Kind+" with these keys.", "", "| Key | Type |", "|---|---|")
			for _, k := range cmd.JSON.Keys {
				lines = append(lines, "| `"+helpRefCell(k.Key)+"` | "+k.Type+" |")
			}
			lines = append(lines, "")
		}
	}
	lines = append(lines, "#### Related", "")
	for _, name := range cmd.Related {
		lines = append(lines, "- [`"+name+"`](#"+helpRefAnchor(name)+")")
	}
	return append(lines, "")
}

// helpRefFenceSafe indents output lines that would end a fence or start a
// heading for the site parser.
func helpRefFenceSafe(out string) string {
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "#") {
			lines[i] = "  " + line
		}
	}
	return strings.Join(lines, "\n")
}

func helpRefCell(s string) string { return strings.ReplaceAll(s, "|", "\\|") }

// helpRefJSONDoc is docs/reference/commands.json.
func helpRefJSONDoc(cmds []helpRefCommand) string {
	doc := struct {
		Version  int              `json:"version"`
		Source   string           `json:"source"`
		Commands []helpRefCommand `json:"commands"`
	}{1, "Generated from the Go help table by scripts/gen-commands-doc.sh; do not edit.", cmds}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(doc)
	return b.String()
}

// helpRefProblems lists what the reference lacks: a command with no "when",
// no example, no --json example where it takes --json, an example with no
// recorded run, a flag or exit code with no meaning, a related command that is
// not in the table. TestB801EveryCommandHasAReference fails on any of them.
func helpRefProblems() []string {
	rec := helpRefLoad()
	problems := []string{}
	names := map[string]bool{}
	for _, entry := range helpCommands {
		names[entry.name] = true
	}
	for _, entry := range helpCommands {
		ref, ok := helpRefs[entry.name]
		if !ok {
			problems = append(problems, entry.name+": no reference entry")
			continue
		}
		if strings.TrimSpace(ref.when) == "" {
			problems = append(problems, entry.name+": no when-to-use text")
		}
		if len(ref.examples) == 0 {
			problems = append(problems, entry.name+": no example")
		}
		usage := helpCommandUsage(entry.name)
		jsonExample := false
		recorded := []helpRefRecord{}
		for _, ex := range ref.examples {
			if !strings.HasPrefix(ex.args+" ", entry.name+" ") {
				problems = append(problems, fmt.Sprintf("%s: example %q does not run this command", entry.name, ex.args))
			}
			if !helpRefKnownState(ex.state) {
				problems = append(problems, fmt.Sprintf("%s: example %q has an unknown state %q", entry.name, ex.args, ex.state))
			}
			if strings.Contains(" "+ex.args+" ", " --json ") {
				jsonExample = true
			}
			if r, ok := helpRefFind(rec, entry.name, ex.args); ok {
				recorded = append(recorded, r)
			} else {
				problems = append(problems, fmt.Sprintf("%s: example %q has no recorded run (CODE_FORGE_B801_RECORD=1 go test -run TestB801DocExamplesRun)", entry.name, ex.args))
			}
		}
		if !entry.namespace && helpRefTakesJSON(entry.name, usage) && !jsonExample && ref.noJSONRun == "" {
			problems = append(problems, entry.name+": takes --json but no example uses it")
		}
		if len(ref.related) == 0 {
			problems = append(problems, entry.name+": no related command")
		}
		for _, name := range ref.related {
			if !names[name] {
				problems = append(problems, fmt.Sprintf("%s: related %q is not a command", entry.name, name))
			}
		}
		if !entry.namespace {
			for _, f := range helpRefFlags(entry.name, usage) {
				if f.Meaning == "" {
					problems = append(problems, entry.name+": flag "+f.Name+" has no meaning")
				}
			}
		}
		for _, e := range helpRefExitCodes(ref, recorded) {
			if e.Meaning == "" {
				problems = append(problems, fmt.Sprintf("%s: exit %d has no meaning", entry.name, e.Code))
			}
		}
	}
	for name := range helpRefs {
		if !names[name] {
			problems = append(problems, name+": reference entry for a command not in the table")
		}
	}
	sort.Strings(problems)
	return problems
}

func helpRefKnownState(state string) bool {
	for _, s := range helpRefStates {
		if s == state {
			return true
		}
	}
	return false
}
