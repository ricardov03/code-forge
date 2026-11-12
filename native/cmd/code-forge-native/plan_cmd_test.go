package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestB471PlanUsageAndReadsMatchNode(t *testing.T) {
	_, repo := smallProject(t, "")
	writeHomeFile(t, repo, "plan.md", "# plan\n")
	if err := os.MkdirAll(filepath.Join(repo, "adir"), 0o700); err != nil {
		t.Fatal(err)
	}
	checkSmallCases(t, []smallCase{
		{[]string{"plan", "--help"}, 0, planUsage, ""},
		{[]string{"plan", "check", "--help"}, 0, planUsage, ""},
		{[]string{"plan"}, 2, "", planUsage},
		{[]string{"plan", "check"}, 2, "", planUsage},
		{[]string{"plan", "bogus", "plan.md"}, 2, "", planUsage},
		{[]string{"plan", "check", "plan.md", "extra"}, 2, "", planUsage},
		{[]string{"plan", "check", "missing.md"}, 2, "", "plan: the plan file cannot be read\n"},
		{[]string{"plan", "check", "adir"}, 2, "", "plan: the plan file cannot be read\n"},
		{[]string{"plan", "check", "missing.md", "--facts", "missing"}, 2, "", "plan: the plan file cannot be read\n"},
		{[]string{"plan", "check", "plan.md", "--facts", "missing"}, 2, "", "plan: --facts cannot be read\n"},
		{[]string{"plan", "check", "plan.md", "--facts"}, 2, "", "plan: --facts needs a value\n"},
		{[]string{"plan", "check", "plan.md", "--facts="}, 2, "", "plan: --facts needs a value\n"},
		{[]string{"plan", "check", "plan.md", "--bogus"}, 2, "", "plan: unknown flag \"--bogus\"\n"},
		{[]string{"plan", "check", "plan.md", "--help"}, 2, "", "plan: unknown flag \"--help\"\n"},
		{[]string{"plan", "check", "plan.md", "--slug", "a", "--slug", "b"}, 2, "", "plan: --slug given more than once\n"},
	})
}

// The check itself (facts, config, ledger lanes) goes to Node.
func TestB471PlanCheckHandsOver(t *testing.T) {
	_, repo := smallProject(t, "")
	writeHomeFile(t, repo, "plan.md", "# plan\n")
	writeHomeFile(t, repo, "facts.md", "x")
	expectHandover(t, "plan-check-unported", "plan", "check", "plan.md")
	expectHandover(t, "plan-check-unported", "plan", "check", "plan.md", "--facts", "facts.md", "--slug", "demo")
}
