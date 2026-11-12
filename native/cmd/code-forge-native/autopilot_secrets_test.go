package main

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// secretFixture: runs whose ledger text carries the run's signer key (hex),
// the one secret Node registers in an autopilot process.
func secretFixture(t *testing.T) (runFixture, string) {
	t.Helper()
	f := newAutopilotFixture(t)
	ownerBefore := bnGitOwner
	bnGitOwner = func(any) string { return "Test Owner" }
	t.Cleanup(func() { bnGitOwner = ownerBefore })
	secret := hex.EncodeToString(f.key)
	blocks := `,"blocks":{"B2":{"status":"open"}}`
	f.record(t, "ra", "active", blocks+grantJSON("active", "2026-11-11T12:30:00.000Z", "null", "null"))
	f.record(t, "rx", "active", blocks+grantJSON("active", "2026-11-11T09:30:00.000Z", "null", "null"))
	rows := []string{}
	for _, run := range []string{"ra", "rx"} {
		row, _ := json.Marshal(map[string]any{"run": run, "event": "autopilot.decision", "acted": false, "scope": "waive:nit", "question": "keys " + secret + " end", "decision_id": "dq-" + run, "subject": map[string]any{"block": "B2", "file": "src/" + secret + ".go"}, "ts": "2026-11-11T09:16:45.000Z"})
		rows = append(rows, string(row))
	}
	writeLedger(t, f, rows...)
	return f, secret
}

func secretFiles(t *testing.T, f runFixture, run string) string {
	t.Helper()
	text := ""
	for _, name := range []string{"autopilot-binnacle.md", "autopilot-log.md"} {
		data, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "runs", run, name))
		if err != nil {
			t.Fatal(err)
		}
		text += string(data)
	}
	return text
}

// B478: Node masks the run key in every stdout/stderr write and every
// binnacle field once loadKey has read it (a signed write, or the decision
// check of round and level), and not before. Go does the same; the
// expectations are Node's output on the same fixture.
func TestAutopilotSecretsRedactedInGo(t *testing.T) {
	f, secret := secretFixture(t)

	// no signed write: the key was never loaded, Node prints the text as is
	_, out, _ := autopilotCall(t, "binnacle", "--run", "ra")
	if !strings.Contains(out, "keys "+secret+" end") || strings.Contains(out, "[REDACTED]") {
		t.Fatalf("binnacle without a key load masked the text:\n%s", out)
	}

	// the expire row loads the key: the binnacle masks it
	code, out, _ := autopilotCall(t, "binnacle", "--run", "rx")
	if code != 0 || strings.Contains(out, secret) || !strings.Contains(out, "keys [REDACTED] end") || !strings.Contains(out, "src/[REDACTED].go") {
		t.Fatalf("binnacle after the expire row = %d:\n%s", code, out)
	}
	_, out, _ = autopilotCall(t, "binnacle", "--run", "rx")
	if !strings.Contains(out, "keys "+secret+" end") {
		t.Fatalf("a later process registers nothing until it loads the key:\n%s", out)
	}

	// stop signs a row: stdout and the binnacle files mask the key
	code, out, errText := autopilotCall(t, "stop", "--run", "ra")
	if code != 0 || errText != "" || strings.Contains(out, secret) {
		t.Fatalf("stop = %d %q %q", code, out, errText)
	}
	if files := secretFiles(t, f, "ra"); strings.Contains(files, secret) || !strings.Contains(files, "keys [REDACTED] end") {
		t.Fatalf("stop's binnacle files:\n%s", files)
	}

	// round: the decision check loads the key, so its refusal masks it; a
	// refusal before that point does not
	f.record(t, "rr", "active", `,"blocks":{"B2":{"status":"open"}}`+grantJSON("active", "2026-11-11T12:30:00.000Z", "null", "null"))
	_, _, errText = autopilotCall(t, "round", "--run", "rr", "--block", "B2", "--file", "src/a.mjs", "--decision", secret)
	if errText != "autopilot round: refused — no autopilot.decision row [REDACTED] in this run; the question goes to the owner (autopilot ask, then --decision <id>)\n" {
		t.Fatalf("round after the key load: %q", errText)
	}
	_, _, errText = autopilotCall(t, "round", "--run", "rr", "--block", secret, "--file", "a", "--decision", "d")
	if errText != "autopilot round: block "+secret+" is not in run rr\n" {
		t.Fatalf("round before the key load: %q", errText)
	}
}

// B478: redactString merges overlapping and adjacent secrets into one mask.
func TestAutopilotRedactMerges(t *testing.T) {
	before := autopilotSecrets
	t.Cleanup(func() { autopilotSecrets = before })
	autopilotSecrets = nil
	autopilotRegisterSecret("abcd")
	autopilotRegisterSecret("cdef")
	autopilotRegisterSecret("")
	for in, want := range map[string]string{
		"xabcdefy":     "x[REDACTED]y",
		"abcd abcdabc": "[REDACTED] [REDACTED]abc",
		"cdefabcd":     "[REDACTED]",
		"plain":        "plain",
	} {
		if got := autopilotRedact(in); got != want {
			t.Fatalf("redact(%q) = %q, want %q", in, got, want)
		}
	}
}
