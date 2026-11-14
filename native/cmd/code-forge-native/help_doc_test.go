package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// b648Root is a scratch package root with an empty docs/reference folder.
func b648Root(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs", "reference"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// b648FakeVerb adds a Go-only verb with its own help form and help entry for
// the test (B634-style: a flag only Go knows).
func b648FakeVerb(t *testing.T, name, usage string) {
	t.Helper()
	routes[name] = func(args []string) int {
		if len(args) > 0 && metaIsHelp(args[0]) {
			fmt.Fprint(os.Stdout, usage)
			return 0
		}
		return 2
	}
	saved := helpCommands
	helpCommands = append(append([]helpEntry{}, saved...), helpEntry{name: name, summary: "A Go-only test verb"})
	t.Cleanup(func() {
		delete(routes, name)
		helpCommands = saved
	})
}

// The committed page is exactly what the Go help table renders, and the CLI
// check says so.
func TestB648CommittedCommandsDocIsCurrent(t *testing.T) {
	b613Env(t)
	root, _ := filepath.Abs("../../..")
	code, stdout, stderr := b613Call(t, "help", "--commands-doc", "--check", "--root", root)
	if code != 0 || stdout != "gen-commands-doc --check: command reference is up to date\n" || stderr != "" {
		t.Fatalf("check: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	data, err := os.ReadFile(filepath.Join(root, "docs", "reference", "commands.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	// Every Go verb (hidden ones too) has its section and one contents link.
	for _, entry := range helpCommands {
		if strings.Contains(entry.name, " ") {
			continue
		}
		at := strings.Index(doc, "\n## "+entry.name+"\n\n"+entry.summary+"\n\n```text\n")
		if at < 0 || strings.Count(doc, "- ["+entry.name+"](#"+entry.name+")\n") != 1 {
			t.Errorf("%s: no section with its Go summary or no single contents link", entry.name)
		}
	}
	// The block section is Node's block USAGE, now served by Go.
	if !strings.Contains(doc, "## block\n\nOpen, check, close and land the blocks of a run\n\n```text\n"+strings.TrimSuffix(blockUsage, "\n")+"\n```\n") {
		t.Error("block section does not hold the block usage")
	}
	for _, want := range []string{
		"### plan check\n", "### ci watch\n", "### decisions list\n", "### tools install\n",
		"### block open\n\nStart a block: owned files, acceptance tests, brief\n\n```text\nusage: code-forge block open ",
		"## help\n\nShow the commands, or the help of one command\n\n```text\n" + strings.TrimSuffix(helpUsage, "\n") + "\n```\n",
		"## land\n\nLanding code that block land uses\n\n```text\nusage: code-forge land\n",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	// Sub-commands that only repeat their verb's usage get no section.
	for _, absent := range []string{"### heal list\n", "### queue run\n", "### docs sync\n", "\x1b["} {
		if strings.Contains(doc, absent) {
			t.Errorf("page has %q", absent)
		}
	}
}

// Red before B648: a help change made only in Go never reached the page (the
// Node generator read src/cli). Now it does, in write and in check mode.
func TestB648GoOnlyHelpChangeReachesCommandsDoc(t *testing.T) {
	b613Env(t)
	root := b648Root(t)
	code, stdout, stderr := b613Call(t, "help", "--commands-doc", "--root", root)
	if code != 0 || stdout != "wrote docs/reference/commands.md\n" || stderr != "" {
		t.Fatalf("write: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	page := filepath.Join(root, "docs", "reference", "commands.md")
	before, _ := os.ReadFile(page)
	b648FakeVerb(t, "zz-b648", "usage: code-forge zz-b648 [--until-done]\n")
	code, _, stderr = b613Call(t, "help", "--commands-doc", "--check", "--root", root)
	if code != 1 || !strings.Contains(stderr, "command reference is stale") {
		t.Fatalf("check after a Go-only change: code=%d stderr=%q", code, stderr)
	}
	if code, _, _ = b613Call(t, "help", "--commands-doc", "--root", root); code != 0 {
		t.Fatalf("rewrite: code=%d", code)
	}
	after, _ := os.ReadFile(page)
	for _, want := range []string{"- [zz-b648](#zz-b648)\n", "## zz-b648\n\nA Go-only test verb\n\n```text\nusage: code-forge zz-b648 [--until-done]\n```\n"} {
		if strings.Contains(string(before), want) || !strings.Contains(string(after), want) {
			t.Errorf("page does not gain %q", want)
		}
	}
	if code, stdout, _ = b613Call(t, "help", "--commands-doc", "--check", "--root", root); code != 0 || !strings.Contains(stdout, "up to date") {
		t.Fatalf("check after rewrite: code=%d stdout=%q", code, stdout)
	}
}

// A one-byte change to a copy makes --check exit 1 and name line 1.
func TestB648CheckNamesTheFirstDifference(t *testing.T) {
	b613Env(t)
	root, _ := filepath.Abs("../../..")
	data, err := os.ReadFile(filepath.Join(root, "docs", "reference", "commands.md"))
	if err != nil {
		t.Fatal(err)
	}
	data[0] ^= 1
	copyFile := filepath.Join(t.TempDir(), "commands.md")
	if err := os.WriteFile(copyFile, data, 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := b613Call(t, "help", "--commands-doc", "--check", copyFile, "--root", root)
	want := "gen-commands-doc --check: command reference is stale (first difference at line 1)\n" +
		"  on disk:    \"\\\" Command reference\"\n  generated:  \"# Command reference\"\n"
	if code != 1 || stdout != "" || stderr != want {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, _, stderr = b613Call(t, "help", "--commands-doc", "--check", filepath.Join(t.TempDir(), "none.md"))
	if code != 1 || stderr != "gen-commands-doc --check: command reference does not exist\n" {
		t.Fatalf("missing copy: code=%d stderr=%q", code, stderr)
	}
}

// A command with no usage text stops the generator before it writes.
func TestB648MissingUsageRefusesWithoutWriting(t *testing.T) {
	b613Env(t)
	root := b648Root(t)
	page := filepath.Join(root, "docs", "reference", "commands.md")
	if err := os.WriteFile(page, []byte("existing page\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b648FakeVerb(t, "zz-b648-empty", "")
	code, stdout, stderr := b613Call(t, "help", "--commands-doc", "--root", root)
	if code != 1 || stdout != "" || stderr != "Missing CLI help metadata: zz-b648-empty.USAGE\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if data, _ := os.ReadFile(page); string(data) != "existing page\n" {
		t.Fatalf("page changed: %q", data)
	}
	if code, _, stderr = b613Call(t, "help", "--commands-doc", "--bogus"); code != 2 || !strings.Contains(stderr, "unknown argument") {
		t.Fatalf("bad flag: code=%d stderr=%q", code, stderr)
	}
}

// block --help and -h ran in Node before B648; Go now prints the same usage
// inside the help page, with no hand-over.
func TestB648BlockHelpRunsInGo(t *testing.T) {
	home := b613Env(t)
	for _, flag := range []string{"--help", "-h"} {
		code, stdout, stderr := b613Call(t, "block", flag)
		if code != 0 || stderr != "" || stdout != helpWrapped(t, "block", blockUsage) {
			t.Fatalf("block %s: code=%d stderr=%q stdout=%q", flag, code, stderr, stdout)
		}
	}
	if code, _, _ := runCapturedRoute(t, runBlock, "--help"); code != 0 {
		t.Fatalf("runBlock --help: code=%d", code)
	}
	if _, stdout, _ := runCapturedRoute(t, runBlock, "-h"); stdout != blockUsage {
		t.Fatalf("runBlock -h printed %q", stdout)
	}
	if _, err := os.Stat(filepath.Join(home, ".code-forge", "native", "handovers.jsonl")); err == nil {
		t.Fatal("block --help handed over to Node")
	}
	// help land shows the land usage although land has no Go route.
	if code, stdout, _ := b613Call(t, "help", "land"); code != 0 || !strings.Contains(stdout, "\nUsage:\n  usage: code-forge land\n") {
		t.Fatalf("help land: code=%d stdout=%q", code, stdout)
	}
	// queue --help prints Node's full queue usage.
	if _, stdout, _ := runCapturedRoute(t, runQueue, "--help"); !strings.Contains(stdout, "Gates take a slot by themselves;\n  wrap any other test command in `queue run`.") {
		t.Fatalf("queue --help: %q", stdout)
	}
}
