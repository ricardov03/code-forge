package review

import (
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B876: a failed review session keeps a safe stderr tail: packet lines and
// PEM blocks dropped, keys masked, the last 5 lines kept.
func TestB876FailureTail(t *testing.T) {
	prompt := "Review this file\nconst secretPlan = \"the quarter plan\"\n"
	stderr := "line 1\nline 2\nconst secretPlan = \"the quarter plan\"\n-----BEGIN PRIVATE KEY-----\nMIIabc\n-----END PRIVATE KEY-----\nline 3\nline 4\nline 5\nError: key sk-ant-api03-abcdefghijklmnop rejected\n"
	got := failureTail(ledger.Object{{Key: "stderr", Value: stderr}}, prompt)
	want := "line 2\nline 3\nline 4\nline 5\nError: key [REDACTED] rejected"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	// stderr_tail (made by the session layer) wins over stderr
	if got := failureTail(ledger.Object{{Key: "stderr_tail", Value: "stalled"}, {Key: "stderr", Value: "other"}}, ""); got != "stalled" {
		t.Fatalf("tail %q", got)
	}
	if got := failureTail(ledger.Object{}, prompt); got != "" {
		t.Fatalf("empty %q", got)
	}
}

// A session that exits non-zero keeps its verdict detail and the safe stderr
// tail in the result's session entry.
func TestB876EngineKeepsTheFailureTail(t *testing.T) {
	failed := func() ledger.Object {
		return ledger.Object{{Key: "status", Value: "failed"}, {Key: "provider", Value: "claude"}, {Key: "model", Value: "m2"},
			{Key: "exit_code", Value: 1.0}, {Key: "stderr", Value: "Error: quota exceeded for sk-ant-api03-abcdefghijklmnop\n"}}
	}
	run := b629Engine(t, b629Cfg(nil, nil), []any{failed(), failed(), failed()})
	sessions, _ := run.raw.Get("sessions").([]any)
	if len(sessions) == 0 {
		t.Fatalf("no sessions: %s", mustJSON(t, run.raw))
	}
	meta := sessions[len(sessions)-1].(ledger.Object)
	if meta.Get("reason") != "exit" || meta.Get("stderr_tail") != "Error: quota exceeded for [REDACTED]" || meta.Get("detail") == nil {
		t.Fatalf("session %s", mustJSON(t, meta))
	}
}
