package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B884: a worker hand-over keeps its detail text, redacted, in
// handovers.jsonl, and `logs handovers` shows the newest one per reason
// (text and --json); a row without a detail keeps the old 5-field shape.
func TestB884LogsHandoversKeepTheDetail(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NODE", filepath.Join(home, "missing-node"))
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	secret := "b884-run-secret-0123456789abcdef"
	autopilotRegisterSecret(secret)
	recordHandoverDetail([]string{"worker", "--run", "r1"}, "worker-ticket-unported", "ticket aaa: the review session: key "+secret+" refused")
	recordHandoverDetail([]string{"worker", "--run", "r1"}, "worker-ticket-unported", "ticket bbb: worker: ticket case not ported to Go: the reviewed file is not UTF-8")

	code, out, errText := b460Call(t, "logs", "handovers")
	if code != 0 || !strings.Contains(out, "2  worker  unknown  worker-ticket-unported\n   last: ticket bbb: worker: ticket case not ported to Go: the reviewed file is not UTF-8\n") {
		t.Fatalf("logs handovers = %d %q %q", code, out, errText)
	}
	code, out, _ = b460Call(t, "logs", "handovers", "--json")
	if code != 0 || strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Fatalf("--json = %d %q", code, out)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("--json %q: %v", out, err)
	}
	if !strings.Contains(out, `"last_detail":"ticket bbb: worker: ticket case not ported to Go: the reviewed file is not UTF-8"`) {
		t.Fatalf("--json %s", out)
	}
	data := b884ReadFile(t, handoverLogPath())
	if strings.Contains(data, secret) || !strings.Contains(data, `"detail":"ticket aaa: the review session: key `) {
		t.Fatalf("handovers.jsonl %s", data)
	}
}

func b884ReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
