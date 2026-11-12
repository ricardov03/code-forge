package main

import (
	"testing"
)

// Node's sweep output for every case that does not scan processes. No case
// here reaches the scan, so no real process is listed or signalled.
func TestB471SweepUsageMatchesNode(t *testing.T) {
	smallProject(t, "")
	checkSmallCases(t, []smallCase{
		{[]string{"sweep", "--help"}, 0, sweepUsage, ""},
		{[]string{"sweep", "-h"}, 0, sweepUsage, ""},
		{[]string{"sweep", "--json", "--help"}, 0, sweepUsage, ""},
		{[]string{"sweep", "--bogus"}, 2, "", sweepUsage},
		{[]string{"sweep", "x"}, 2, "", sweepUsage},
		{[]string{"sweep", "--json", "--json"}, 2, "", sweepUsage},
		{[]string{"sweep", "--apply=1"}, 2, "", sweepUsage},
		{[]string{"sweep", "--apply", "-h"}, 2, "", sweepUsage},
	})
}

// The scan itself goes to Node with its own reason.
func TestB471SweepScanHandsOver(t *testing.T) {
	smallProject(t, "")
	expectHandover(t, "sweep-unported", "sweep")
	expectHandover(t, "sweep-unported", "sweep", "--apply", "--json")
}
