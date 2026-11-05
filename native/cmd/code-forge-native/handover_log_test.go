package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

func TestHandoverTelemetry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NODE", filepath.Join(home, "missing-node"))
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	t.Setenv("CODE_FORGE_NATIVE", "1")
	code, _ := run([]string{"doctor", "private-value"})
	if code != 127 {
		t.Fatalf("handover exit = %d, want 127", code)
	}
	data, err := os.ReadFile(filepath.Join(home, ".code-forge", "native", "handovers.jsonl"))
	if err != nil {
		t.Fatalf("one handover must write one row: %v", err)
	}
	var row map[string]string
	if err := json.Unmarshal(data, &row); err != nil {
		t.Fatal(err)
	}
	if len(row) != 5 || row["reason"] != "unrouted" || row["verb"] != "doctor" || row["sub"] != "" {
		t.Fatalf("unexpected private-safe row: %#v", row)
	}
	if _, err := time.Parse(time.RFC3339Nano, row["at"]); err != nil {
		t.Fatalf("invalid UTC timestamp %q: %v", row["at"], err)
	}
	seen := map[string]bool{}
	pattern := regexp.MustCompile(`^[a-z]+(?:-[a-z]+)*(?::[a-z]+(?:-[a-z]+)*)?$`)
	for _, reason := range handoverReasons {
		if seen[reason] || !pattern.MatchString(reason) {
			t.Fatalf("reason must be unique and kebab-case: %q", reason)
		}
		seen[reason] = true
		recordHandover([]string{"block", "close", "/private/customer", "--report", "secret-id"}, reason)
	}
	data, err = os.ReadFile(handoverLogPath())
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(lines) != len(handoverReasons)+1 {
		t.Fatalf("helper wrote %d rows, want %d", len(lines), len(handoverReasons)+1)
	}
	for i, line := range lines[1:] {
		var got handoverRow
		if err := json.Unmarshal(line, &got); err != nil {
			t.Fatal(err)
		}
		if got.Reason != handoverReasons[i] || got.Verb != "block" || got.Sub != "close" || got.Version != version {
			t.Fatalf("reason %d: incorrect helper row %#v", i, got)
		}
	}
	for _, args := range [][]string{{"review-file", "/private/customer"}, {"block", "--report", "close"}, {"doctor", "secret-id"}} {
		_, sub := handoverCommand(args)
		if sub != "" {
			t.Fatalf("private argument treated as subcommand for %q: %q", args, sub)
		}
	}
	for _, flag := range []string{"CODE_FORGE_HANDOVER_LOG", "CODE_FORGE_NATIVE"} {
		t.Setenv(flag, "0")
		recordHandover([]string{"doctor"}, "unrouted")
		after, _ := os.ReadFile(handoverLogPath())
		if !bytes.Equal(after, data) {
			t.Fatalf("%s=0 changed log", flag)
		}
		t.Setenv(flag, "1")
	}
}
