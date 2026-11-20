package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B983: `autopilot ask --speed` no longer trusts the option order. The asker
// names the fast option with --fastest; the place in --options does not
// matter, and an ask without a named fast option is refused.

func b983DecisionRows(t *testing.T, f b536Fixture) []string {
	t.Helper()
	data, _ := os.ReadFile(filepath.Join(f.home, ".code-forge", "ledger", "ws.jsonl"))
	rows := []string{}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, `"event":"autopilot.decision"`) {
			rows = append(rows, line)
		}
	}
	return rows
}

func TestB983SpeedTakesMarkedOption(t *testing.T) {
	f := b536Setup(t)
	marker := b922Claude(t, f)
	// the slow option is listed first: the decision still follows --fastest
	code, out, errText := autopilotCall(t, "ask", "--run", "r-fix", "--scope", "round:extra", "--question", "land one by one or the batch?", "--options", "one-by-one,batch", "--fastest", "batch", "--speed", "--block", "B1")
	if code != 0 || !strings.Contains(out, "decision  batch · the fastest option that keeps every gate") {
		t.Fatalf("ask = %d %q %q", code, out, errText)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("a delegate session ran for a speed ask: %v", err)
	}
	rows := b983DecisionRows(t, f)
	if len(rows) != 1 || !strings.Contains(rows[0], `"decision":"batch"`) || !strings.Contains(rows[0], `"options":["one-by-one","batch"]`) {
		t.Fatalf("decision rows = %q", rows)
	}
}

func TestB983SpeedNeedsMarkedOption(t *testing.T) {
	f := b536Setup(t)
	b922Claude(t, f)
	code, out, errText := autopilotCall(t, "ask", "--run", "r-fix", "--scope", "round:extra", "--question", "q", "--options", "one-by-one,batch", "--speed")
	if code != 2 || !strings.Contains(errText, "--speed needs --fastest <option>") {
		t.Fatalf("speed without --fastest = %d %q %q", code, out, errText)
	}
	code, out, errText = autopilotCall(t, "ask", "--run", "r-fix", "--scope", "round:extra", "--question", "q", "--options", "one-by-one,batch", "--fastest", "parallel", "--speed")
	if code != 2 || !strings.Contains(errText, "--fastest must be one of --options") {
		t.Fatalf("unknown --fastest = %d %q %q", code, out, errText)
	}
	if rows := b983DecisionRows(t, f); len(rows) != 0 {
		t.Fatalf("a refused speed ask wrote decision rows: %q", rows)
	}
}
