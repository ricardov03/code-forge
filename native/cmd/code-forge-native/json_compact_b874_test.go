package main

import (
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// b874Compact is pretty JSON text (a Node-recorded golden) as the one
// compact line --json prints since B874, key order kept.
func b874Compact(t *testing.T, pretty string) string {
	t.Helper()
	value, err := ledger.DecodeValue([]byte(pretty))
	if err != nil {
		t.Fatalf("golden is not JSON: %v", err)
	}
	line, err := ledger.Stringify(value, false)
	if err != nil {
		t.Fatal(err)
	}
	return string(line) + "\n"
}

// b874OneLine fails unless out is exactly one line of JSON.
func b874OneLine(t *testing.T, what, out string) {
	t.Helper()
	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("%s is not one line:\n%s", what, out)
	}
	if _, err := ledger.DecodeValue([]byte(out)); err != nil {
		t.Fatalf("%s is not JSON: %v\n%s", what, err, out)
	}
}

// TestB874JSONIsOneCompactLine is the B801 audit's P03 sample set: autopilot
// status/log/binnacle --json, report --json and run status print one line.
func TestB874JSONIsOneCompactLine(t *testing.T) {
	t.Run("autopilot binnacle and log", func(t *testing.T) {
		newBinnacleFixture(t)
		for _, sub := range []string{"binnacle", "log"} {
			code, out, errText := autopilotCall(t, sub, "--run", "r-a", "--json")
			if code != 0 || errText != "" {
				t.Fatalf("%s = %d %q", sub, code, errText)
			}
			b874OneLine(t, "autopilot "+sub+" --json", out)
		}
	})
	t.Run("autopilot status", func(t *testing.T) {
		newBinnacleFixture(t)
		for _, run := range []string{"r-a", "r-b"} {
			code, out, _ := autopilotCall(t, "status", "--run", run, "--json")
			if code != 0 {
				t.Fatalf("status %s = %d", run, code)
			}
			b874OneLine(t, "autopilot status --json ("+run+")", out)
		}
	})
	t.Run("report", func(t *testing.T) {
		b456ReportHome(t, map[string]string{"t.jsonl": b456ReportLedger})
		code, out, _ := b456Report(t, "--slug", "t", "--json")
		if code != 0 {
			t.Fatalf("report = %d", code)
		}
		b874OneLine(t, "report --json", out)
	})
	t.Run("run status", func(t *testing.T) {
		f := newRunFixture(t)
		f.record(t, "r-one", "active", `,"blocks":{"B1":{"status":"open"}}`)
		code, out, errs := runVerbCall(t, "status", "--run", "r-one")
		if code != 0 {
			t.Fatalf("run status = %d %q", code, errs)
		}
		b874OneLine(t, "run status", out)
	})
}
