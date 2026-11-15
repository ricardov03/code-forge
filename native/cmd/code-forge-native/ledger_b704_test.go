package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// b704LedgerRows are ledger rows with lone surrogate escapes (in values, map
// keys of the outcome lookup and nested arrays).
var b704LedgerRows = strings.Join([]string{
	`{"event":"decision","confidence":0.9,"question":"q\ud83d","decision_id":"d\udc00"}`,
	`{"event":"outcome","decision_id":"d\udc00","outcome":"correct"}`,
	`{"event":"x","nested":{"b":"\ud800","a":[1,"\udfff z"]}}`,
}, "\n") + "\n"

// TestB704LedgerLoneSurrogateInGo: ledger tail and calibration read rows
// with lone surrogates as JSON.parse does and print Node's bytes (taken from
// CODE_FORGE_NATIVE=0 code-forge ledger tail|calibration --slug b704 on the
// same file) instead of handing over.
func TestB704LedgerLoneSurrogateInGo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-b704-node")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	dir := filepath.Join(home, ".code-forge", "ledger")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b704.jsonl"), []byte(b704LedgerRows), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errText := runCaptured(t, "ledger", "tail", "--slug", "b704")
	if code != 0 || out != b704LedgerRows || errText != "" {
		t.Fatalf("tail: code=%d stdout=%q stderr=%q", code, out, errText)
	}
	code, out, errText = runCaptured(t, "ledger", "calibration", "--slug", "b704")
	bucket := func(id string, correct int) string {
		return "    \"" + id + "\": {\n      \"correct\": " + string(rune('0'+correct)) + ",\n      \"wrong\": 0,\n      \"unknown\": 0\n    }"
	}
	ids := []string{"0.50-0.60", "0.60-0.70", "0.70-0.80", "0.80-0.85", "0.85-0.90", "0.90-0.95", "0.95-0.99", "0.99-1.00"}
	parts := []string{}
	for _, id := range ids {
		n := 0
		if id == "0.90-0.95" {
			n = 1
		}
		parts = append(parts, bucket(id, n))
	}
	want := "{\n  \"buckets\": {\n" + strings.Join(parts, ",\n") + "\n  },\n  \"unbacked\": 0,\n  \"unmapped\": 0\n}\n"
	if code != 0 || out != want || errText != "" {
		t.Fatalf("calibration: code=%d stdout=%q stderr=%q", code, out, errText)
	}
}
