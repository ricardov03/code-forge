package main

import "testing"

// B513: the reason list is append-only and merged with merge=union, so a bad
// merge or a hand edit can leave a constant without its array entry or two
// constants with the same code. Constants are the array's keys (iota), so a
// constant with no entry shows up as an empty code at its index; a repeated
// code would make two reasons record the same row.
func TestHandoverReasonsOneCodePerConstant(t *testing.T) {
	if len(handoverReasons) == 0 {
		t.Fatal("handoverReasons is empty")
	}
	seen := map[string]handoverReason{}
	for i, code := range handoverReasons {
		r := handoverReason(i)
		if code == "" {
			t.Errorf("reason constant %d has no entry in handoverReasons", i)
			continue
		}
		if prev, ok := seen[code]; ok {
			t.Errorf("code %q is used by constants %d and %d", code, prev, r)
			continue
		}
		seen[code] = r
	}
	// Spot-check both ends so a shifted iota block shows up.
	if got := handoverReasons[hUnrouted]; got != "unrouted" {
		t.Errorf("handoverReasons[hUnrouted] = %q, want %q", got, "unrouted")
	}
	if got := handoverReasons[hBlockUnportedSubcommand]; got != "block-unported-subcommand" {
		t.Errorf("handoverReasons[hBlockUnportedSubcommand] = %q, want %q", got, "block-unported-subcommand")
	}
}
