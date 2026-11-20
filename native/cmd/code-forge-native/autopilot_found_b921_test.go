package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// B921: scope fixed. A found row goes to the backlog (the next release) by
// default; only severity customer-breaking joins the current run.

func TestB921NormalFindingGoesToBacklog(t *testing.T) {
	f := b800Fixture(t, nil)
	code, out, errText := autopilotCall(t, "found", "--run", "r-test", "--source", "coder", "--title", "help text names an old flag", "--evidence", "help.go:12 says --old")
	if code != 0 || !strings.Contains(out, "found F1 coder → backlog (next release; not customer-breaking, B921): help text names an old flag") {
		t.Fatalf("found = %d %q %q", code, out, errText)
	}
	found := b800Found(t, f)
	if len(found) != 1 || found[0]["state"] != "backlog" || found[0]["block"] != nil || found[0]["brief"] != nil || found[0]["severity"] != "normal" {
		t.Fatalf("found = %v", found)
	}
	if got := b800Planned(t, f); !slices.Equal(got, []string{"B1"}) {
		t.Fatalf("planned = %v, want the backlog finding outside the run", got)
	}
	if _, err := os.Stat(filepath.Join(f.home, ".code-forge", "runs", "r-test", "found")); !os.IsNotExist(err) {
		t.Fatalf("brief dir written for a backlog finding: %v", err)
	}
	if rows := b800Rows(t, f); len(rows) != 1 || rows[0]["state"] != "backlog" || rows[0]["severity"] != "normal" {
		t.Fatalf("rows = %v", rows)
	}
	code, out, _ = autopilotCall(t, "found", "--run", "r-test", "--list")
	if code != 0 || !strings.Contains(out, "F1 coder → - → backlog (next release) (backlog: not customer-breaking") {
		t.Fatalf("list = %d %q", code, out)
	}
	code, out, errText = autopilotCall(t, "binnacle", "--run", "r-test", "--markdown")
	if data, err := os.ReadFile(strings.TrimSpace(out)); err == nil {
		out = string(data)
	}
	if code != 0 || !strings.Contains(out, "| F1 | coder | help text names an old flag | - | backlog (next release) |") || strings.Contains(out, "Decide on found F1") {
		t.Fatalf("binnacle = %d %q %q", code, out, errText)
	}
}

func TestB921CustomerBreakingJoinsRun(t *testing.T) {
	f := b800Fixture(t, nil)
	report := b800Report(t, "FOUND land [customer-breaking] land loses the PR url :: land.go:40 drops it\nFOUND [docs] typo in the guide :: guide.md:3\n")
	if code, out, errText := autopilotCall(t, "found", "--run", "r-test", "--report", report, "--from", "B1"); code != 0 {
		t.Fatalf("report = %d %q %q", code, out, errText)
	}
	if code, out, errText := autopilotCall(t, "found", "--run", "r-test", "--source", "gate", "--title", "install fails on a clean machine", "--severity", "customer-breaking", "--evidence", "install.sh:9 exit 1"); code != 0 || !strings.Contains(out, "B3 queued") {
		t.Fatalf("flag = %d %q %q", code, out, errText)
	}
	found := b800Found(t, f)
	if len(found) != 3 {
		t.Fatalf("found = %v", found)
	}
	if e := found[0]; e["state"] != "queued" || e["block"] != "B2" || e["severity"] != "customer-breaking" || len(e["labels"].([]any)) != 0 {
		t.Fatalf("found[0] = %v", e)
	}
	if e := found[1]; e["state"] != "backlog" || e["block"] != nil {
		t.Fatalf("found[1] = %v", e)
	}
	if e := found[2]; e["state"] != "queued" || e["block"] != "B3" {
		t.Fatalf("found[2] = %v", e)
	}
	if got := b800Planned(t, f); !slices.Equal(got, []string{"B1", "B2", "B3"}) {
		t.Fatalf("planned = %v", got)
	}
	code, _, errText := autopilotCall(t, "found", "--run", "r-test", "--source", "gate", "--title", "x", "--severity", "urgent", "--evidence", "y")
	if code != 2 || !strings.Contains(errText, "--severity must be normal or customer-breaking") {
		t.Fatalf("bad severity = %d %q", code, errText)
	}
}
