package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B922: a speed-tagged ask under autopilot is decided at once (the first,
// fastest option), with no delegate session and no wait on the owner; the
// binnacle tells the owner after. A non-speed ask keeps the session
// (TestB536AskDelegateSessionMatchesNode).

// b922Claude puts a claude on PATH that records any call, so a test sees
// whether a delegate session ran.
func b922Claude(t *testing.T, f b536Fixture) string {
	t.Helper()
	bin := filepath.Join(filepath.Dir(f.home), "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(filepath.Dir(f.home), "claude-called")
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\necho called >> "+marker+"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	return marker
}

func TestB922SpeedAskDecidedAtOnce(t *testing.T) {
	f := b536Setup(t)
	marker := b922Claude(t, f)
	code, out, errText := autopilotCall(t, "ask", "--run", "r-fix", "--scope", "round:extra", "--question", "land the batch now or one by one?", "--options", "one-by-one,batch", "--fastest", "batch", "--speed", "--block", "B1")
	if code != 0 || !strings.Contains(out, "acted · decision ad-") || !strings.Contains(out, "decision  batch · the fastest option that keeps every gate") {
		t.Fatalf("ask = %d %q %q", code, out, errText)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("a delegate session ran for a speed ask: %v", err)
	}
	rows, _ := os.ReadFile(filepath.Join(f.home, ".code-forge", "ledger", "ws.jsonl"))
	var row string
	for _, line := range strings.Split(string(rows), "\n") {
		if strings.Contains(line, `"event":"autopilot.decision"`) {
			row = line
		}
	}
	for _, want := range []string{`"decision":"batch"`, `"acted":true`, `"to_owner":false`, `"speed":true`, `"mac":`} {
		if !strings.Contains(row, want) {
			t.Fatalf("decision row lacks %s: %q", want, row)
		}
	}
	code, out, errText = autopilotCall(t, "binnacle", "--run", "r-fix", "--markdown")
	if data, err := os.ReadFile(strings.TrimSpace(out)); err == nil {
		out = string(data)
	}
	if code != 0 || !strings.Contains(out, `round:extra: speed choice "batch" taken at once`) || strings.Contains(out, "land the batch now or one by one? (round:extra") {
		t.Fatalf("binnacle = %d %q %q", code, out, errText)
	}
	code, _, errText = autopilotCall(t, "ask", "--run", "r-fix", "--scope", "round:extra", "--question", "which?", "--speed")
	if code != 2 || !strings.Contains(errText, "--speed needs --options and --fastest <option>") {
		t.Fatalf("speed without options = %d %q", code, errText)
	}
	// B983: no --fastest, or one that is not an option: exit 2, no new decision row
	for _, extra := range [][]string{{}, {"--fastest", "c"}} {
		args := append([]string{"ask", "--run", "r-fix", "--scope", "round:extra", "--question", "which?", "--options", "a,b", "--speed"}, extra...)
		if code, _, errText = autopilotCall(t, args...); code != 2 {
			t.Fatalf("speed %v = %d %q", extra, code, errText)
		}
	}
	if rows := b983DecisionRows(t, f); len(rows) != 1 {
		t.Fatalf("refused speed asks wrote decision rows: %q", rows)
	}
}

func TestB922SpeedAskJSON(t *testing.T) {
	f := b536Setup(t)
	b922Claude(t, f)
	code, out, errText := autopilotCall(t, "ask", "--run", "r-fix", "--scope", "round:extra", "--question", "q", "--options", "slow,fast", "--fastest", "fast", "--speed", "--json")
	if code != 0 || !strings.Contains(out, `"acted":true,"to_owner":false,"reason":null,"decision":"fast"`) || !strings.Contains(out, `"speed":true`) {
		t.Fatalf("ask = %d %q %q", code, out, errText)
	}
}
