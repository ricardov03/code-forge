package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/state"
)

// B920 tests: block open checks the CI of the base a new block builds on.
// gh is a fake on PATH (landCIFakeGh); HOME is a temp dir.

// b920Open opens block B4 in the batch fixture's run and returns the exit
// code and the combined output.
func b920Open(t *testing.T, f *landBatchFixture, extra ...string) (int, string) {
	t.Helper()
	f.write(f.home, "acc.yml", `[{"clause":"works","tests":["TestB4"]}]`)
	capture, err := os.CreateTemp(f.home, "output-")
	if err != nil {
		t.Fatal(err)
	}
	out, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = capture, capture
	args := append([]string{"open", "B4", "--run", "r-batch-unit", "--owned", "src/B4.mjs", "--acceptance", filepath.Join(f.home, "acc.yml"), "--level", "L1"}, extra...)
	code := runBlock(args)
	os.Stdout, os.Stderr = out, stderr
	if err := capture.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(capture.Name())
	if err != nil {
		t.Fatal(err)
	}
	return code, string(data)
}

func b920Status(t *testing.T, f *landBatchFixture) any {
	t.Helper()
	record, err := state.ReadRun(f.home, "r-batch-unit")
	if err != nil {
		t.Fatal(err)
	}
	return blockObject(blockObject(record.Get("blocks")).Get("B4")).Get("status")
}

func TestB920OpenRefusesRedBase(t *testing.T) {
	f := landCIBatch(t, landCIOn, "", "")
	gh := landCIFakeGh(t, landCIRed, nil)
	code, output := b920Open(t, f)
	if code != 1 {
		t.Fatalf("exit=%d, want 1: %s", code, output)
	}
	short := f.base[:8]
	if !strings.Contains(output, "block open: base "+short+" CI is red (CI: failure, run 7); a coder never builds on a red base (B920)") || !strings.Contains(output, "next: gh run view 7 --log-failed\n") {
		t.Errorf("output=%q, want the red-base refusal with its next step", output)
	}
	if got := b920Status(t, f); got != nil {
		t.Errorf("B4 status=%v, want nothing recorded", got)
	}
	calls := landCICalls(t, gh)
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "run list --commit "+f.base+" ") {
		t.Errorf("gh calls=%q, want one run list of the base %s", calls, f.base)
	}
}

func TestB920OpenRefusesBaseWithoutRun(t *testing.T) {
	f := landCIBatch(t, landCIOn, "", "")
	landCIFakeGh(t, "[]", nil)
	code, output := b920Open(t, f)
	if code != 1 || !strings.Contains(output, "has no CI run; a coder never builds on an unchecked base (B920)") || !strings.Contains(output, "next: code-forge ci watch --sha "+f.base) {
		t.Fatalf("exit=%d output=%q, want the no-run refusal", code, output)
	}
	if got := b920Status(t, f); got != nil {
		t.Errorf("B4 status=%v, want nothing recorded", got)
	}
}

func TestB920OpenGreenBaseOpens(t *testing.T) {
	f := landCIBatch(t, landCIOn, "", "")
	landCIFakeGh(t, landCIGreen, nil)
	code, output := b920Open(t, f)
	if code != 0 {
		t.Fatalf("exit=%d, want 0: %s", code, output)
	}
	if strings.Contains(output, "warning") || !strings.Contains(output, "block B4 open") {
		t.Errorf("output=%q, want a plain open", output)
	}
	if got := b920Status(t, f); got != "open" {
		t.Errorf("B4 status=%v, want open", got)
	}
}

func TestB920OpenWarnsWhenGateOff(t *testing.T) {
	f := landCIBatch(t, map[string]any{"require_ci": "off"}, "", "")
	b920Origin(t, f)
	landCIFakeGh(t, landCIRed, nil)
	code, output := b920Open(t, f)
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (gate off): %s", code, output)
	}
	if !strings.Contains(output, "warning: base "+f.base[:8]+" CI is red (CI: failure, run 7); a coder should not build on a red base (B920)") {
		t.Errorf("output=%q, want the red-base warning", output)
	}
	if got := b920Status(t, f); got != "open" {
		t.Errorf("B4 status=%v, want open", got)
	}
}

// b920Origin gives the fixture repo an origin remote (an empty bare repo:
// the newest-base fetch finds no branch there and keeps HEAD).
func b920Origin(t *testing.T, f *landBatchFixture) {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "origin.git")
	for _, args := range [][]string{{"init", "-q", "--bare", bare}, {"-C", f.repo, "remote", "add", "origin", bare}} {
		cmd := exec.Command("git", args...)
		cmd.Env = gitx.ChildEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
}

// With the gate off and no origin remote there is no CI to read: gh is
// never run (it would write its own state under HOME) and the open goes on.
func TestB920OpenGateOffNoRemoteSkipsGh(t *testing.T) {
	f := landCIBatch(t, map[string]any{"require_ci": "off"}, "", "")
	gh := landCIFakeGh(t, landCIRed, nil)
	code, output := b920Open(t, f)
	if code != 0 || strings.Contains(output, "warning") {
		t.Fatalf("exit=%d output=%q, want a plain open", code, output)
	}
	if calls := landCICalls(t, gh); len(calls) != 0 {
		t.Errorf("gh calls=%q, want none without an origin remote", calls)
	}
}
