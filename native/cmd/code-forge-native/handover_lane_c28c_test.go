package main

import "testing"

func TestC28cLaneHandoverVocabulary(t *testing.T) {
	for _, sub := range []string{"start", "ready", "combine", "private-project"} {
		want := sub
		if sub == "private-project" {
			want = "unknown"
		}
		verb, got := handoverCommand([]string{"lane", sub, "private-run"})
		if verb != "lane" || got != want {
			t.Errorf("lane %s recorded as (%q, %q), want (lane, %s)", sub, verb, got, want)
		}
	}
}
