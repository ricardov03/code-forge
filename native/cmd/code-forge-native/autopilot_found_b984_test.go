package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B984: a backlog finding (B921: not customer-breaking) is also written to
// the workspace's next-release plan file, .code-forge/next-release.md, so
// the next release's planning sees it after the run record is gone.

func b984Plan(t *testing.T, f *hvFixture) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.workspace, ".code-forge", "next-release.md"))
	if err != nil {
		t.Fatalf("next-release plan: %v", err)
	}
	return string(data)
}

func TestB984BacklogFindingWritesNextReleasePlan(t *testing.T) {
	f := b800Fixture(t, nil)
	code, out, errText := autopilotCall(t, "found", "--run", "r-test", "--source", "coder", "--title", "help text names an old flag", "--evidence", "help.go:12 says --old\nsecond line")
	if code != 0 || !strings.Contains(out, "→ backlog (next release") || !strings.Contains(out, "plan       "+filepath.Join(f.workspace, ".code-forge", "next-release.md")) {
		t.Fatalf("found = %d %q %q", code, out, errText)
	}
	plan := b984Plan(t, f)
	if !strings.HasPrefix(plan, "# Next release (code-forge backlog)\n") {
		t.Fatalf("plan heading = %q", plan)
	}
	fp := b800Found(t, f)[0]["fingerprint"].(string)
	want := "- [ ] F1 · run r-test · coder · 2026-11-19: help text names an old flag — help.go:12 says --old <!-- cf-found " + fp + " -->\n"
	if !strings.Contains(plan, want) || strings.Contains(plan, "second line") {
		t.Fatalf("plan = %q, want line %q", plan, want)
	}
	code, out, errText = autopilotCall(t, "found", "--run", "r-test", "--source", "gate", "--title", "lint gate slow", "--evidence", "lint 90 s", "--json")
	if code != 0 || !strings.Contains(out, `"state":"backlog"`) || !strings.Contains(out, `"next_release":`+autopilotQuote(filepath.Join(f.workspace, ".code-forge", "next-release.md"))) {
		t.Fatalf("found --json = %d %q %q", code, out, errText)
	}
	if plan = b984Plan(t, f); strings.Count(plan, "\n- [ ] ") != 2 || !strings.Contains(plan, "F2 · run r-test · gate · 2026-11-19: lint gate slow — lint 90 s") {
		t.Fatalf("plan after 2 = %q", plan)
	}
}

func TestB984NextReleasePlanNoDuplicatesNoQueued(t *testing.T) {
	f := b800Fixture(t, nil)
	args := []string{"found", "--run", "r-test", "--source", "coder", "--title", "help text names an old flag", "--evidence", "help.go:12 says --old"}
	for i := 0; i < 2; i++ {
		if code, out, errText := autopilotCall(t, args...); code != 0 {
			t.Fatalf("found %d = %d %q %q", i, code, out, errText)
		}
	}
	// customer-breaking joins the run; an owner-scope finding waits: neither goes to the plan
	if code, out, errText := autopilotCall(t, "found", "--run", "r-test", "--source", "gate", "--title", "install fails on a clean machine", "--severity", "customer-breaking", "--evidence", "install.sh:9 exit 1"); code != 0 || strings.Contains(out, "plan       ") {
		t.Fatalf("breaking = %d %q %q", code, out, errText)
	}
	if code, out, errText := autopilotCall(t, "found", "--run", "r-test", "--source", "coder", "--title", "npm publish step is wrong", "--evidence", "release.sh:3"); code != 0 || !strings.Contains(out, "waits for the owner") {
		t.Fatalf("owner = %d %q %q", code, out, errText)
	}
	plan := b984Plan(t, f)
	if strings.Count(plan, "\n- [ ] ") != 1 || strings.Contains(plan, "install fails") || strings.Contains(plan, "npm publish") {
		t.Fatalf("plan = %q", plan)
	}
	// a later run that finds the same thing adds no second line
	if err := state.UpdateRun(f.home, "r-test", func(record *ledger.Object) error {
		record.Set("found", []any{})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if code, out, errText := autopilotCall(t, args...); code != 0 {
		t.Fatalf("found again = %d %q %q", code, out, errText)
	}
	if again := b984Plan(t, f); again != plan {
		t.Fatalf("plan changed for a known fingerprint:\n%q\n%q", plan, again)
	}
}
