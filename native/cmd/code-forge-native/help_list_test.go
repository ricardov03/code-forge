package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// b613Env runs the CLI in-process with an empty HOME, the repo as the install
// root and a Node that does not exist, so a hand-over shows as exit 127.
func b613Env(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-b613-node")
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "")
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	saved := metaPackageRoot
	metaPackageRoot = func() (string, error) { return root, nil }
	t.Cleanup(func() { metaPackageRoot = saved })
	return home
}

func b613Call(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	return runCaptured(t, args...)
}

// Every verb the router accepts and every sub-verb of the block and gates
// dispatchers has one summary; no summary is long or repeated.
func TestB613EveryCommandHasASummary(t *testing.T) {
	names, summaries := map[string]bool{}, map[string]string{}
	for _, entry := range helpCommands {
		if names[entry.name] {
			t.Errorf("%q is in the help table twice", entry.name)
		}
		names[entry.name] = true
		line := helpLine(entry)
		if strings.TrimSpace(entry.summary) == "" || len([]rune(line)) > 72 {
			t.Errorf("%q: summary %q must be 1..72 characters", entry.name, line)
		}
		if other, seen := summaries[entry.summary]; seen {
			t.Errorf("%q and %q share the summary %q", entry.name, other, entry.summary)
		}
		summaries[entry.summary] = entry.name
		verb, _, _ := strings.Cut(entry.name, " ")
		if _, routed := routes[verb]; !routed && !entry.hidden {
			t.Errorf("%q is listed but the router has no verb %q", entry.name, verb)
		}
	}
	for verb := range routes {
		if !names[verb] {
			t.Errorf("routed verb %q has no help summary", verb)
		}
	}
	for sub := range blockSubs {
		if !names["block "+sub] {
			t.Errorf("block sub-verb %q has no help summary", sub)
		}
	}
	for verb, subs := range map[string]map[string]string{
		"gates": gatesSubUsage, "keys": keysSubUsage, "ledger": ledgerSubUsage,
		"proof": proofSubUsage, "logs": logsSubUsage, "run": runSubUsage,
	} {
		for sub := range subs {
			if !names[verb+" "+sub] {
				t.Errorf("%s sub-verb %q has no help summary", verb, sub)
			}
		}
	}
	// Each listed command answers its own help form with usage text in Go:
	// a listed sub-verb that the dispatcher does not know fails here.
	b613Env(t)
	for _, entry := range helpCommands {
		if _, routed := routes[strings.Fields(entry.name)[0]]; !routed || entry.name == "block" {
			continue // block --help still hands over to Node; land is not routed
		}
		if usage := helpCommandUsage(entry.name); !helpIsUsage(usage) || !strings.Contains(usage, "code-forge "+strings.Fields(entry.name)[0]) {
			t.Errorf("%q: help form printed no usage: %q", entry.name, usage)
		}
	}
}

func TestB613ListPage(t *testing.T) {
	home := b613Env(t)
	code, stdout, stderr := b613Call(t, "--help")
	if code != 0 || stderr != "" {
		t.Fatalf("--help: code=%d stderr=%q", code, stderr)
	}
	data, err := os.ReadFile("../../../package.json")
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct{ Name, Version string }
	if err := json.Unmarshal(data, &pkg); err != nil {
		t.Fatal(err)
	}
	head := pkg.Name + " " + pkg.Version + "\n\nUsage:\n  code-forge <command> [options] [arguments]\n\nOptions:\n  -h, --help     Show help for a command\n"
	if !strings.HasPrefix(stdout, head) {
		t.Fatalf("list head:\n%s", stdout)
	}
	for _, want := range []string{
		"\nAvailable commands:\n  author                  Write or harden a plan draft from a brief\n",
		"\n  doctor                  Check this machine and project are ready\n",
		"\n block\n  block accept-hit        Accept one transcript rule hit as safe (owner)\n",
		"\n  block open              Start a block: owned files, acceptance tests, brief\n",
		"\n logs\n  logs                    Show the last local CLI errors\n  logs clear ",
		"\n gates\n  gates acceptance ",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("list lacks %q:\n%s", want, stdout)
		}
	}
	for _, hidden := range []string{"native-status", "\n  land ", "\x1b["} {
		if strings.Contains(stdout, hidden) {
			t.Errorf("list shows %q", hidden)
		}
	}
	// Top-level commands come before the first group heading.
	if strings.Index(stdout, "\n  worker ") > strings.Index(stdout, "\n autopilot\n") {
		t.Errorf("top-level verbs must come first:\n%s", stdout)
	}
	for _, args := range [][]string{{}, {"-h"}, {"help"}} {
		if c, out, _ := b613Call(t, args...); c != 0 || out != stdout {
			t.Errorf("%q differs from --help (code %d)", args, c)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".code-forge", "native", "handovers.jsonl")); err == nil {
		t.Fatal("the list handed over to Node")
	}
}

func TestB613ListColor(t *testing.T) {
	b613Env(t)
	saved := helpColor
	helpColor = func() bool { return true }
	defer func() { helpColor = saved }()
	_, stdout, _ := b613Call(t, "help")
	for _, want := range []string{"\x1b[33mAvailable commands:\x1b[39m", "  \x1b[32mblock open\x1b[39m  ", " \x1b[33mblock\x1b[39m\n"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("colored list lacks %q", want)
		}
	}
	helpColor = saved
	t.Setenv("NO_COLOR", "1")
	if helpColor() {
		t.Fatal("NO_COLOR must turn color off")
	}
}

func TestB613JSONList(t *testing.T) {
	b613Env(t)
	code, stdout, _ := b613Call(t, "help", "--json")
	if code != 0 || strings.Count(stdout, "\n") != 1 {
		t.Fatalf("help --json: code=%d %q", code, stdout)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatal(err)
	}
	byName := map[string]map[string]any{}
	for _, row := range rows {
		if len(row) != 4 {
			t.Fatalf("row keys %v; want name, summary, owner, group", row)
		}
		byName[row["name"].(string)] = row
	}
	if r := byName["block waive"]; r == nil || r["owner"] != true || r["group"] != "block" || r["summary"] != "Accept one review finding without a fix" {
		t.Errorf("block waive row %v", r)
	}
	if r := byName["doctor"]; r == nil || r["owner"] != false || r["group"] != "" {
		t.Errorf("doctor row %v", r)
	}
	if byName["native-status"] != nil || byName["land"] != nil {
		t.Error("hidden commands must not be in the JSON list")
	}
	if c, _, errText := b613Call(t, "help", "--json", "block"); c != 2 || !strings.Contains(errText, "usage: code-forge help") {
		t.Errorf("help --json block: %d %q", c, errText)
	}
}

func TestB613CommandPage(t *testing.T) {
	home := b613Env(t)
	code, page, _ := b613Call(t, "help", "block", "open")
	if code != 0 {
		t.Fatalf("help block open: %d", code)
	}
	_, usage, _ := runCapturedRoute(t, runBlock, "open", "--help")
	want := "Description:\n  Start a block: owned files, acceptance tests, brief\n\nUsage:\n  " + strings.ReplaceAll(strings.TrimRight(usage, "\n"), "\n", "\n  ") + "\n\nOptions:\n  --run <r>  "
	if !strings.HasPrefix(page, want) || !strings.HasSuffix(page, "  -h, --help           Show this help\n") {
		t.Fatalf("help block open page:\n%s\nwant prefix:\n%s", page, want)
	}
	if c, viaFlag, _ := b613Call(t, "block", "open", "--help"); c != 0 || viaFlag != page {
		t.Fatalf("block open --help differs from help block open (code %d):\n%s", c, viaFlag)
	}
	// The verb page lists its sub-commands; the owner marker shows there too.
	_, gates, _ := b613Call(t, "help", "gates")
	if !strings.HasPrefix(gates, "Description:\n  Run the project gates and the safety checks\n\nUsage:\n  usage: code-forge gates detect|run") || !strings.Contains(gates, "\nAvailable commands:\n  gates acceptance ") {
		t.Fatalf("help gates:\n%s", gates)
	}
	_, block, _ := b613Call(t, "help", "block")
	if !strings.Contains(block, "Usage:\n  code-forge block <command> [options] [arguments]\n") || !strings.Contains(block, "  block waive             Accept one review finding without a fix (owner)\n") {
		t.Fatalf("help block:\n%s", block)
	}
	// A verb that refuses --help keeps its refusal; help <verb> still shows its usage.
	if c, out, errText := b613Call(t, "status", "--help"); c != 2 || out != "" || !strings.HasPrefix(errText, "status: unknown flag \"--help\"\n") {
		t.Fatalf("status --help: %d %q %q", c, out, errText)
	}
	if c, out, _ := b613Call(t, "help", "status"); c != 0 || !strings.Contains(out, "Usage:\n  usage: code-forge status [--run <id>] [--json]\n") {
		t.Fatalf("help status: %d\n%s", c, out)
	}
	for _, args := range [][]string{{"help", "nope"}, {"help", "block", "nope"}, {"help", "block", "open", "x"}} {
		if c, out, errText := b613Call(t, args...); c != 2 || out != "" || !strings.HasPrefix(errText, "help: unknown command ") {
			t.Errorf("%q: %d %q %q", args, c, out, errText)
		}
	}
	if c, out, _ := b613Call(t, "help", "x", "--help"); c != 0 || out != helpUsage {
		t.Errorf("help x --help: %d %q", c, out)
	}
	// Nothing handed over, and the probe of `help status` left no error entry.
	data, _ := os.ReadFile(filepath.Join(home, ".code-forge", "native", "handovers.jsonl"))
	if len(bytes.TrimSpace(data)) != 0 {
		t.Fatalf("hand-over rows: %s", data)
	}
	errorsLog, _ := os.ReadFile(filepath.Join(home, ".code-forge", "logs", "errors.jsonl"))
	if bytes.Contains(errorsLog, []byte(`"verb":"status"`)) && bytes.Count(errorsLog, []byte("\n")) > 1 {
		t.Fatalf("help status logged a status error: %s", errorsLog)
	}
}

// runCapturedRoute calls one route function directly (no router help wrap).
func runCapturedRoute(t *testing.T, route func([]string) int, args ...string) (int, string, string) {
	t.Helper()
	code, stdout, stderr := helpCapture(true, func() int { return route(args) })
	return code, stdout, stderr
}

// The command reference reads docs/reference/command-summaries.json; it must
// hold exactly what `code-forge help --json` prints. Rewrite it with
// CODE_FORGE_WRITE_SUMMARIES=1 go test -run TestB613SummariesFile.
func TestB613SummariesFile(t *testing.T) {
	lines := []string{}
	for _, row := range helpJSONRows() {
		data, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, "  "+string(data))
	}
	want := "[\n" + strings.Join(lines, ",\n") + "\n]\n"
	file := filepath.Join("..", "..", "..", "docs", "reference", "command-summaries.json")
	if os.Getenv("CODE_FORGE_WRITE_SUMMARIES") == "1" {
		if err := os.WriteFile(file, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(file)
	if err != nil || string(got) != want {
		t.Fatalf("%s is stale; rewrite it with CODE_FORGE_WRITE_SUMMARIES=1 go test -run TestB613SummariesFile (%v)", file, err)
	}
	var fromFile, fromCLI []helpJSONRow
	b613Env(t)
	_, stdout, _ := b613Call(t, "help", "--json")
	if json.Unmarshal(got, &fromFile) != nil || json.Unmarshal([]byte(stdout), &fromCLI) != nil || len(fromFile) != len(fromCLI) {
		t.Fatalf("summaries file and help --json differ")
	}
	for i := range fromFile {
		if fromFile[i] != fromCLI[i] {
			t.Fatalf("row %d: file %v, help --json %v", i, fromFile[i], fromCLI[i])
		}
	}
}

// helpWrapped is the page `<name> --help` prints since B613 when the verb
// itself prints usage: Description, that usage two spaces in, Options.
func helpWrapped(t *testing.T, name, usage string) string {
	t.Helper()
	entry, ok := helpLookup(name)
	if !ok {
		t.Fatalf("no help entry %q", name)
	}
	return helpPage(helpPainter(false), entry, usage)
}

// helpExpect turns the usage text a test expects from a full CLI call into
// the page that call prints since B613: a help form of a listed command
// (`<verb> [<sub>] --help`) that prints usage gets Description and Options.
func helpExpect(t *testing.T, args []string, code int, stdout string) string {
	t.Helper()
	routed := append([]string{}, args...)
	if len(routed) > 0 && (routed[0] == "--version" || routed[0] == "-v") {
		routed[0] = "version"
	}
	if name, ok := helpTarget(routed); ok && code == 0 && helpIsUsage(stdout) {
		return helpWrapped(t, name, stdout)
	}
	return stdout
}
