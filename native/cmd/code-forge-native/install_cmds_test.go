package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const harnessTable = "\nHARNESSES\nID\tLABEL\tDETECTED\tVERIFIED\tPROJECT PATH\n" +
	"claude\tClaude Code\tyes\tyes\t.claude/skills\n" +
	"codex\tCodex CLI\tno\tyes\t.agents/skills\n" +
	"grok\tGrok CLI\tyes\tyes\t.agents/skills\n" +
	"gemini\tGemini CLI\tno\tUNVERIFIED\t.agents/skills\n" +
	"cursor\tCursor\tno\tyes\t.agents/skills\n" +
	"copilot\tCopilot\tno\tyes\t.agents/skills\n"

// installFixture seeds HOME with the rows Node's tests use: a copy, a
// symlink, a missing target, a malformed row and a null version.
func installFixture(t *testing.T) string {
	home, _ := smallProject(t, "")
	bin := filepath.Join(home, "bin")
	writeHomeFile(t, "/", filepath.Join(bin, "grok"), "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(bin, "grok"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeHomeFile(t, "/", filepath.Join(bin, "codex"), "not executable\n")
	t.Setenv("PATH", "::"+bin)
	writeHomeFile(t, "/", filepath.Join(home, ".claude", "skills", "code-forge", "SKILL.md"), "x\n")
	writeHomeFile(t, "/", filepath.Join(home, "src", "skill", "SKILL.md"), "s\n")
	writeHomeFile(t, "/", filepath.Join(home, "proj", ".agents", "skills", "other", "SKILL.md"), "keep\n")
	if err := os.Symlink(filepath.Join(home, "src", "skill"), filepath.Join(home, "proj", ".agents", "skills", "code-forge")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "nowhere"), filepath.Join(home, "broken")); err != nil {
		t.Fatal(err)
	}
	writeHomeFile(t, "/", installsFilePath(home), strings.ReplaceAll(`{
  "version": 1,
  "installs": [
    {"harness":"claude","scope":"global","method":"copy","source":"@/src/skill","target":"@/.claude/skills/code-forge","linked_at":"t","version":"0.1.0","extra":1.50},
    {"target":"@/proj/.agents/skills/code-forge","harness":"codex","scope":"project","method":"symlink","source":"@/src/skill","linked_at":"t"},
    {"harness":"bad","scope":"weird","method":"copy","source":"/a","target":"/b","linked_at":"t"},
    {"harness":"gemini","scope":"global","method":"copy","source":"@/src/skill","target":"@/gone","linked_at":"t","version":null},
    {"harness":"grok","scope":"global","method":"symlink","source":"@/src/skill","target":"@/broken","linked_at":"t"}
  ]
}
`, "@", home))
	return home
}

func TestB455ListMatchesNode(t *testing.T) {
	home := installFixture(t)
	checkSmallCases(t, []smallCase{
		{[]string{"list"}, 0, "INSTALLED\nHARNESS\tSCOPE\tMETHOD\tTARGET\tRESOLVES\n" +
			"claude\tglobal\tcopy\t" + home + "/.claude/skills/code-forge\tyes\n" +
			"codex\tproject\tsymlink\t" + home + "/proj/.agents/skills/code-forge\tyes\n" +
			"grok\tglobal\tsymlink\t" + home + "/broken\tno\n" + harnessTable, ""},
		{[]string{"list", "x"}, 2, "", listUsage},
		{[]string{"list", "--help"}, 0, listUsage, ""},
		{[]string{"list", "-h"}, 0, listUsage, ""},
	})
	for _, data := range []string{`[]`, `{bad`, `{"installs":{}}`, `"text"`} {
		writeHomeFile(t, "/", installsFilePath(home), data)
		checkSmallCases(t, []smallCase{{[]string{"list"}, 0, "INSTALLED\nHARNESS\tSCOPE\tMETHOD\tTARGET\tRESOLVES\n(none)\n" + harnessTable, ""}})
	}
	writeHomeFile(t, "/", installsFilePath(home), `[{"harness":"cursor","scope":"project","method":"copy","source":"/s","target":"/nope/x","linked_at":"t"}]`)
	checkSmallCases(t, []smallCase{{[]string{"list"}, 0, "INSTALLED\nHARNESS\tSCOPE\tMETHOD\tTARGET\tRESOLVES\ncursor\tproject\tcopy\t/nope/x\tno\n" + harnessTable, ""}})
}

func TestB455RemoveMatchesNode(t *testing.T) {
	home := installFixture(t)
	for _, args := range [][]string{{"x", "--scope"}, {"--scope", "bad"}, {"a", "b"}, {"--x"}, {"claude", "--scope", "global", "extra"}} {
		checkSmallCases(t, []smallCase{{append([]string{"remove"}, args...), 2, "", removeUsage}})
	}
	checkSmallCases(t, []smallCase{
		{[]string{"remove", "--help"}, 0, removeUsage, ""},
		{[]string{"remove", "nobody"}, 0, "nothing to remove\n", ""},
		{[]string{"remove", "codex", "--scope", "global"}, 0, "nothing to remove\n", ""},
		{[]string{"remove", "codex", "--scope", "project"}, 0, "removed codex (project) " + home + "/proj/.agents/skills/code-forge\n", ""},
	})
	if _, err := os.Lstat(filepath.Join(home, "proj", ".agents", "skills", "code-forge")); !os.IsNotExist(err) {
		t.Fatalf("symlink target not removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "src", "skill", "SKILL.md")); err != nil {
		t.Fatalf("symlink source must survive: %v", err)
	}
	checkSmallCases(t, []smallCase{{[]string{"remove", "--scope", "global"}, 0,
		"removed claude (global) " + home + "/.claude/skills/code-forge\nremoved grok (global) " + home + "/broken\n", ""}})
	for _, gone := range []string{".claude/skills/code-forge", "broken"} {
		if _, err := os.Lstat(filepath.Join(home, gone)); !os.IsNotExist(err) {
			t.Fatalf("%s not removed: %v", gone, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "proj", ".agents", "skills", "other", "SKILL.md")); err != nil {
		t.Fatalf("unrecorded file must survive: %v", err)
	}
	data, _ := os.ReadFile(installsFilePath(home))
	if string(data) != "{\n  \"version\": 1,\n  \"installs\": []\n}\n" {
		t.Fatalf("installs.json = %q", data)
	}
}

func TestB455RemoveKeepsOrderAndCreatesLedger(t *testing.T) {
	home, _ := smallProject(t, "")
	checkSmallCases(t, []smallCase{{[]string{"remove"}, 0, "nothing to remove\n", ""}})
	data, _ := os.ReadFile(installsFilePath(home))
	if string(data) != "{\n  \"version\": 1,\n  \"installs\": []\n}\n" {
		t.Fatalf("installs.json = %q", data)
	}
	writeHomeFile(t, "/", installsFilePath(home), `[{"target":"/nope/a","harness":"claude","scope":"global","method":"copy","source":"/s","linked_at":"t","n":1e21,"o":{"z":1,"a":[true,null]}},`+
		`{"harness":"codex","scope":"global","method":"copy","source":"/s","target":"/nope/b","linked_at":"t"}]`)
	checkSmallCases(t, []smallCase{{[]string{"remove", "codex"}, 0, "removed codex (global) /nope/b\n", ""}})
	data, _ = os.ReadFile(installsFilePath(home))
	want := "{\n  \"version\": 1,\n  \"installs\": [\n    {\n      \"target\": \"/nope/a\",\n      \"harness\": \"claude\",\n      \"scope\": \"global\",\n" +
		"      \"method\": \"copy\",\n      \"source\": \"/s\",\n      \"linked_at\": \"t\",\n      \"n\": 1e+21,\n      \"o\": {\n        \"z\": 1,\n        \"a\": [\n          true,\n          null\n        ]\n      }\n    }\n  ]\n}\n"
	if string(data) != want {
		t.Fatalf("installs.json = %q\nwant %q", data, want)
	}
}

// Node skips a target that is HOME, an ancestor of it or of its own source,
// and Go leaves such a run, and one it could not finish, to Node untouched.
func TestB455RemoveHandsOverUnsafeOrUnfinishable(t *testing.T) {
	home, _ := smallProject(t, "")
	writeHomeFile(t, "/", filepath.Join(home, "a", "src", "f"), "s\n")
	writeHomeFile(t, "/", filepath.Join(home, "k", "f"), "k\n")
	for _, target := range []string{home, filepath.Join(home, "a"), strings.ToUpper(home[:2]) + home[2:], "/"} {
		writeHomeFile(t, "/", installsFilePath(home), `[{"harness":"grok","scope":"global","method":"copy","source":"`+home+`/k","target":"`+home+`/k","linked_at":"t"},`+
			`{"harness":"grok","scope":"global","method":"copy","source":"`+home+`/a/src","target":"`+target+`","linked_at":"t"}]`)
		expectHandover(t, "remove-unsafe-target", "remove")
		if _, err := os.Stat(filepath.Join(home, "k", "f")); err != nil {
			t.Fatalf("handed-over run deleted a safe target first: %v", err)
		}
	}
	locked := filepath.Join(home, "k", "locked")
	writeHomeFile(t, "/", filepath.Join(locked, "f"), "x\n")
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	writeHomeFile(t, "/", installsFilePath(home), `[{"harness":"grok","scope":"global","method":"copy","source":"`+home+`/a/src","target":"`+home+`/k","linked_at":"t"}]`)
	expectHandover(t, "remove-unfinishable", "remove")
	if err := os.Chmod(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(installsFilePath(home)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(installsFilePath(home), 0o700); err != nil {
		t.Fatal(err)
	}
	expectHandover(t, "remove-unfinishable", "remove")
}
