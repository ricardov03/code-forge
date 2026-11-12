package procguard

import (
	"syscall"
	"testing"
)

// B529: the descendant walk over a fixed table; a cycle or a missing parent
// is never a descendant.
func TestDescends(t *testing.T) {
	rows := map[int]proc{10: {1, 10}, 20: {10, 20}, 30: {20, 20}, 40: {1, 40}, 50: {60, 50}, 60: {50, 50}}
	for _, c := range []struct {
		pid, top int
		want     bool
	}{{20, 10, true}, {30, 10, true}, {10, 10, false}, {40, 10, false}, {50, 10, false}, {99, 10, false}} {
		if got := descends(rows, c.pid, c.top); got != c.want {
			t.Errorf("descends(%d, %d) = %v", c.pid, c.top, got)
		}
	}
}

// B529: a refused table read refuses every real signal; group targets are
// checked member by member.
func TestAllowedUsesTable(t *testing.T) {
	before := table
	t.Cleanup(func() { table = before })
	t.Setenv(Env, "10")
	table = func() (map[int]proc, bool) { return nil, false }
	if Allowed(20, syscall.SIGTERM) {
		t.Fatal("allowed without a process table")
	}
	table = func() (map[int]proc, bool) {
		return map[int]proc{10: {1, 10}, 20: {10, 20}, 21: {20, 20}, 22: {1, 20}}, true
	}
	if !Allowed(21, syscall.SIGTERM) || Allowed(22, syscall.SIGTERM) || Allowed(-20, syscall.SIGKILL) || !Allowed(22, 0) {
		t.Fatal("wrong verdicts")
	}
}
