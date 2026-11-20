package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B962 tests: land.require_main_ci. The fake gh answers `run list --branch`
// (the base's push runs) from main.json and `run list --commit` (the block
// tip) from runs.json, so the tip is green and only the base decides.

var landCIMainOn = map[string]any{"require_ci": "github", "ci_wait": "off", "require_main_ci": true}

const landCIMainRed = `[{"databaseId":42,"headSha":"abcdef0123456789","conclusion":"failure","name":"CI"}]`

func landCIMainFakeGh(t *testing.T, mainRuns string, logs map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	q := func(name string) string { return closeCommand([]string{filepath.Join(dir, name)}) }
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + q("calls") + "\n" +
		"case \"$1 $2 $3\" in\n" +
		"  'run list --branch') cat " + q("main.json") + " ;;\n" +
		"  'run list --commit') cat " + q("runs.json") + " ;;\n" +
		"  'run view '*) cat " + closeCommand([]string{dir}) + "/log-\"$3\".txt ;;\n" +
		"  'pr list '*) echo '[]' ;;\n" +
		"  *) echo \"fake gh: unexpected $*\" >&2; exit 9 ;;\n" +
		"esac\n"
	landWrite(t, filepath.Join(dir, "gh"), script)
	if err := os.Chmod(filepath.Join(dir, "gh"), 0o700); err != nil {
		t.Fatal(err)
	}
	landWrite(t, filepath.Join(dir, "main.json"), mainRuns)
	landWrite(t, filepath.Join(dir, "runs.json"), landCIGreen)
	for id, text := range logs {
		landWrite(t, filepath.Join(dir, "log-"+id+".txt"), text)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	return dir
}

func TestB962MainRedRefused(t *testing.T) {
	f := landCIBatch(t, landCIMainOn, landCIKnownReds, "B9")
	gh := landCIMainFakeGh(t, landCIMainRed, map[string]string{"42": landCINewLog})
	output := landCIRefused(t, f, "main CI is red (CI: failure, push run 42 at abcdef01)")
	for _, want := range []string{"  TestNew -> NO BLOCK\n", "  TestOld -> B9\n", "a failing test has no block in CODE_FORGE_CI_RED_BLOCKS=B9", "next: gh run view 42 --log-failed"} {
		if !strings.Contains(output, want) {
			t.Errorf("output must contain %q: %s", want, output)
		}
	}
	calls := landCICalls(t, gh)
	if len(calls) == 0 || calls[0] != "run list --branch main --event push --status completed --limit 1 --json databaseId,headSha,conclusion,name" {
		t.Errorf("gh calls=%q, want the newest completed push run of main first", calls)
	}
}

func TestB962MainRedUnnamedRefused(t *testing.T) {
	f := landCIBatch(t, landCIMainOn, "", "")
	landCIMainFakeGh(t, landCIMainRed, map[string]string{"42": landCIRedLog})
	output := landCIRefused(t, f, "main CI is red (CI: failure, push run 42 at abcdef01); failing tests:\n  TestOld -> NO BLOCK\n")
	if !strings.Contains(output, "fix main first, or name the fix blocks of its reds in CODE_FORGE_CI_RED_BLOCKS") {
		t.Errorf("output must name the fix: %s", output)
	}
}

func TestB962MainRedTrackedLands(t *testing.T) {
	// The landing block itself may cover main's red: landing the fix is how
	// main turns green.
	f := landCIBatch(t, landCIMainOn, "B1 ^TestOld$\n", "B1")
	landCIMainFakeGh(t, landCIMainRed, map[string]string{"42": landCIRedLog})
	code, output := f.land("B1", "B2")
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (main red only on a known red): %s", code, output)
	}
	f.checkMain([]string{"B1", "B2"})
	if !strings.Contains(output, "  TestOld -> B1\n") || !strings.Contains(output, "ci: main red only on known reds (B1), landing allowed\n") {
		t.Errorf("output must show main's known red: %s", output)
	}
}

func TestB962MainGreenLands(t *testing.T) {
	f := landCIBatch(t, landCIMainOn, "", "")
	landCIMainFakeGh(t, `[{"databaseId":41,"headSha":"0123456789abcdef","conclusion":"success","name":"CI"}]`, nil)
	code, output := f.land("B1", "B2")
	if code != 0 {
		t.Fatalf("exit=%d, want 0: %s", code, output)
	}
	f.checkMain([]string{"B1", "B2"})
	if !strings.Contains(output, "ci: main push run 41 green (01234567)\n") {
		t.Errorf("output must say main's push run is green: %s", output)
	}
}

func TestB962MainOffNotRead(t *testing.T) {
	f := landCIBatch(t, landCIOn, "", "")
	gh := landCIMainFakeGh(t, landCIMainRed, nil)
	if code, output := f.land("B1", "B2"); code != 0 {
		t.Fatalf("exit=%d, want 0 (main check off): %s", code, output)
	}
	for _, call := range landCICalls(t, gh) {
		if strings.Contains(call, "--branch") {
			t.Errorf("gh call %q reads main's runs without land.require_main_ci", call)
		}
	}
}

func TestB962MainBadValueRefused(t *testing.T) {
	f := landCIBatch(t, map[string]any{"require_ci": "github", "ci_wait": "off", "require_main_ci": "yes"}, "", "")
	landCIMainFakeGh(t, landCIMainRed, nil)
	landCIRefused(t, f, "land.require_main_ci must be true or false, got yes")
}
