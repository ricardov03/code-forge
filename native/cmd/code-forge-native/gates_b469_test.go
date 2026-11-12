package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B469: an executable file with no #! and no binary header starts through
// /bin/sh in Node on Linux and throws "spawn ENOEXEC" on macOS. Go hands it
// over to Node before any write; a missing or non-executable command stays a
// red spawn-error gate in Go (B450).
func TestB469GatesRunShellScriptHandsOver(t *testing.T) {
	cfg := map[string]any{"gates": map[string]any{"test": []string{"./plain-script"}, "lint": nil, "types": nil, "format": nil, "rerun_failed": 0}}
	f := b450Setup(t, cfg, nil)
	f.write("plain-script", "printf shell-fallback\n", 0o755)
	before, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "runs", "fixture.json"))
	f.must(err)
	code, out, errOut := f.call([]string{"CODE_FORGE_TEST_QUEUE=off"}, "gates", "run", "--run", "fixture", "--block", "B1")
	if code != 127 || out != "" || !strings.Contains(errOut, "/nonexistent-code-forge-node") {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want the Node hand-over (127)", code, out, errOut)
	}
	after, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "runs", "fixture.json"))
	f.must(err)
	if string(after) != string(before) {
		t.Fatalf("the hand-over changed the run file")
	}
	if _, err := os.Stat(filepath.Join(f.home, ".code-forge", "ledger")); !os.IsNotExist(err) {
		t.Fatalf("the hand-over wrote a ledger: %v", err)
	}

	// A #! script with the same body runs in Go.
	f.write("plain-script", "#!/bin/sh\nprintf shell-fallback\n", 0o755)
	code, out, errOut = f.call([]string{"CODE_FORGE_TEST_QUEUE=off"}, "gates", "run", "--run", "fixture", "--block", "B1")
	if code != 0 || !strings.Contains(out, "shell-fallback") {
		t.Fatalf("#! script: exit=%d stdout=%q stderr=%q, want a green Go run", code, out, errOut)
	}
}
