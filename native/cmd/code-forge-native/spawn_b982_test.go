package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/session"
)

// B982: spawn of a coder with --run and --block checks the CI of the block's
// base_sha again, like block open (B920): the base may have turned red after
// the open. gh is a fake on PATH (landCIFakeGh); HOME is a temp dir.
func TestB982SpawnCoderRechecksBaseCI(t *testing.T) {
	run := "r-batch-unit"
	prompt := func(t *testing.T, land map[string]any, runs string) (*string, error, handoverReason, []string) {
		t.Helper()
		landCIBatch(t, land, "", "")
		gh := landCIFakeGh(t, runs, nil)
		cfg := map[string]any{"land": land}
		text, refusal, reason := spawnCoderPrompt(cfg, []byte("build B1\n"), "B1", &run)
		return text, refusal, reason, landCICalls(t, gh)
	}
	t.Run("gate-on-red", func(t *testing.T) {
		text, refusal, reason, calls := prompt(t, landCIOn, landCIRed)
		var sessionErr *session.Error
		if reason != 0 || text != nil || !errors.As(refusal, &sessionErr) || sessionErr.Code != "base-not-green" {
			t.Fatalf("got text=%v refusal=%v reason=%v, want a base-not-green refusal", text != nil, refusal, reason)
		}
		if !strings.Contains(sessionErr.Message, "CI is red (CI: failure, run 7); a coder never builds on a red base (B920)") || !strings.Contains(sessionErr.Message, "next: gh run view 7 --log-failed") {
			t.Errorf("refusal=%q, want the B920 red-base text and its next step", sessionErr.Message)
		}
		if len(calls) != 1 || !strings.HasPrefix(calls[0], "run list --commit ") {
			t.Errorf("gh calls=%q, want one run list for the base", calls)
		}
	})
	t.Run("gate-on-green", func(t *testing.T) {
		text, refusal, reason, calls := prompt(t, landCIOn, landCIGreen)
		if reason != 0 || refusal != nil || text == nil || !strings.Contains(*text, "===BLOCK B1 COMPLETE===") {
			t.Fatalf("got refusal=%v reason=%v, want the coder prompt", refusal, reason)
		}
		if len(calls) != 1 {
			t.Errorf("gh calls=%q, want the base read once", calls)
		}
	})
	t.Run("gate-off-red", func(t *testing.T) {
		// The gate off reads CI only with an origin remote.
		f := landCIBatch(t, map[string]any{}, "", "")
		f.bareRemote()
		gh := landCIFakeGh(t, landCIRed, nil)
		capture, err := os.CreateTemp(t.TempDir(), "stderr-")
		if err != nil {
			t.Fatal(err)
		}
		stderr := os.Stderr
		os.Stderr = capture
		text, refusal, reason := spawnCoderPrompt(map[string]any{}, []byte("build B1\n"), "B1", &run)
		os.Stderr = stderr
		if err := capture.Close(); err != nil {
			t.Fatal(err)
		}
		warned, err := os.ReadFile(capture.Name())
		if err != nil {
			t.Fatal(err)
		}
		if reason != 0 || refusal != nil || text == nil {
			t.Fatalf("got refusal=%v reason=%v, want the prompt (a red base only warns with the gate off)", refusal, reason)
		}
		if calls := landCICalls(t, gh); len(calls) != 1 || !strings.HasPrefix(calls[0], "run list --commit ") {
			t.Errorf("gh calls=%q, want one run list for the base", calls)
		}
		if !strings.Contains(string(warned), "CI is red (CI: failure, run 7); a coder should not build on a red base (B920)") {
			t.Errorf("stderr=%q, want the B920 red-base warning", warned)
		}
	})
}
