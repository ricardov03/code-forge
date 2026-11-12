package main

import (
	"testing"
)

// TestB560InstallHintsFollowToolOS: the grok hint follows the pinned platform,
// like Node's process.platform: a Mac with no brew asks for Homebrew, Linux
// gets the manual link (the CI runner case B552's golden missed).
func TestB560InstallHintsFollowToolOS(t *testing.T) {
	b552SealApps(t)
	t.Setenv("PATH", t.TempDir())
	var grok toolRow
	for _, r := range toolRows {
		if r.id == "grok" {
			grok = r
		}
	}
	if grok.id == "" {
		t.Fatal("no grok row")
	}
	if _, hint := toolInstallPlan(grok); hint != "install Homebrew first: https://brew.sh" {
		t.Fatalf("darwin hint %q", hint)
	}
	toolOS = "linux"
	if _, hint := toolInstallPlan(grok); hint != grok.manual || hint == "" {
		t.Fatalf("linux hint %q, want %q", hint, grok.manual)
	}
}
