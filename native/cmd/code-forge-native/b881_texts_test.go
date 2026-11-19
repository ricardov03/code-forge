package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B881 (B801 audit P11): block rebase --accept-landed on a stopped block
// refuses with the "block rebase:" prefix, not the internal binary name and
// the raw no-block code.
func TestB881RebaseStoppedBlockPrefix(t *testing.T) {
	f := newRebaseCmdFixture(t, map[string]string{"a.go": "package a\n"})
	f.save("a.go")
	f.setBlock(func(block *ledger.Object) { block.Set("status", "stopped") })
	for _, args := range [][]string{
		{"A", "--run", "r-rebase-unit"},
		{"A", "--run", "r-rebase-unit", "--accept-landed"},
		{"A", "--run", "r-rebase-unit", "--accept-landed", "--json"},
	} {
		code, stdout, stderr := f.call(args...)
		if code != 1 || stdout != "" || stderr != "block rebase: block A is not open in run r-rebase-unit\n" {
			t.Fatalf("%v: code=%d stdout=%q stderr=%q", args, code, stdout, stderr)
		}
	}
}

// B881 (P13): run end --json with open blocks prints one ok:false line that
// names the next command; the text form names it too; nothing changes.
func TestB881RunEndJSONOpenBlocks(t *testing.T) {
	f := newRunFixture(t)
	f.record(t, "r-test", "active", `,"blocks":{"B3":{"status":"open"},"B1":{"status":"done"},"B0":{"status":"open"}}`)
	runFile := filepath.Join(f.home, ".code-forge", "runs", "r-test.json")
	before, _ := os.ReadFile(runFile)
	code, out, errs := runVerbCall(t, "end", "--run", "r-test", "--json")
	text := "run end: run r-test still has open blocks: B3, B0; stop or close each first: code-forge block stop B3 --run r-test"
	if code != 1 || errs != text+"\n" || strings.Count(out, "\n") != 1 {
		t.Fatalf("end --json = %d %q %q", code, out, errs)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout is not JSON: %q", out)
	}
	if got["ok"] != false || got["error"] != text || got["next"] != "code-forge block stop B3 --run r-test" || len(got) != 3 {
		t.Fatalf("json = %v", got)
	}
	if after, _ := os.ReadFile(runFile); !bytes.Equal(before, after) {
		t.Fatal("the run record changed")
	}
}

// B881 (P14): each heal subcommand has its own usage line.
func TestB881HealUsagePerSubcommand(t *testing.T) {
	want := map[string]string{
		"list": "usage: code-forge heal list [--run R] [--json]\n",
		"show": "usage: code-forge heal show <id> [--run R] [--json]\n",
		"run":  "usage: code-forge heal run --run R [--once|--watch] [--max-tries N] [--json]\n",
	}
	for sub, usage := range want {
		code, stdout, _ := b613Call(t, "heal", sub, "--help")
		if code != 0 || !strings.Contains(stdout, "Usage:\n  "+usage) {
			t.Fatalf("heal %s --help = %d %q", sub, code, stdout)
		}
		for other := range want {
			if other != sub && strings.Contains(stdout, "code-forge heal "+other) {
				t.Fatalf("heal %s --help names heal %s: %q", sub, other, stdout)
			}
		}
	}
	code, _, stderr := b613Call(t, "heal", "show")
	if code != 2 || stderr != "heal: show takes one item id, --run and --json\n"+want["show"] {
		t.Fatalf("heal show = %d %q", code, stderr)
	}
	code, stdout, _ := b613Call(t, "heal", "--help")
	if code != 0 || !strings.Contains(stdout, healUsage) {
		t.Fatalf("heal --help = %d %q", code, stdout)
	}
}

// B881 (P17): block open of a block that is already open, with no --level,
// says it is already open (not "no autopilot level recorded").
func TestB881BlockOpenAlreadyOpenFirst(t *testing.T) {
	f := newCloseReviewFixture(t, 1)
	real, _ := filepath.EvalSymlinks(f.top)
	b470SetRun(t, f, func(record, _ *ledger.Object) { record.Set("workspace", real) })
	acceptance := filepath.Join(f.home, "acceptance.yml")
	closeFixtureWrite(t, acceptance, []byte("- clause: the card shows\n  tests: [card_test.go]\n"))
	brief := filepath.Join(f.top, "brief.md")
	closeFixtureWrite(t, brief, []byte("# B1\n"))
	runFile := filepath.Join(f.home, ".code-forge/runs/r-close-fixture.json")
	before, _ := os.ReadFile(runFile)
	wd, _ := os.Getwd()
	if err := os.Chdir(f.top); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	res := b451Run(t, f.home, "open", "B1", "--run", "r-close-fixture", "--owned", "file0.txt", "--acceptance", acceptance, "--brief", brief)
	if res.code != 1 || res.stdout != "" || res.stderr != "block open: block B1 is already open\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", res.code, res.stdout, res.stderr)
	}
	if after, _ := os.ReadFile(runFile); !bytes.Equal(before, after) {
		t.Fatal("the run record changed")
	}
}
