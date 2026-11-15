package main

import (
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B712: Node writes every stdout and stderr line of a verb through writeSafe
// (src/util/redact.mjs), so once loadKey registered the run's signer key a
// failure that names it prints [REDACTED]. Here block open loads the key to
// read the block's signed claim rows, then refuses a claim whose path is not
// a string; the path holds the key's hex. Node's line (runBlock's catch,
// err(`block ${sub}: ${message}\n`) through writeSafe) is the golden.
func TestB712BlockOpenFailurePrintsSignerKeyMasked(t *testing.T) {
	f, acceptance := b581Fixture(t)
	saved := autopilotSecrets
	t.Cleanup(func() { autopilotSecrets = saved })
	autopilotSecrets = nil
	key, err := os.ReadFile(filepath.Join(f.home, ".code-forge/runs/r-close-fixture.key"))
	if err != nil || len(key) != 32 {
		t.Fatalf("key %d bytes, %v", len(key), err)
	}
	secret := hex.EncodeToString(key)
	row := ledger.Object{{Key: "event", Value: "block.claim"}, {Key: "run", Value: "r-close-fixture"}, {Key: "block", Value: "B2"}, {Key: "path", Value: []any{secret}}}
	if _, err := state.WriteSigned(f.home, "r-close-fixture", row); err != nil {
		t.Fatal(err)
	}
	autopilotSecrets = nil // the fixture's own signing registered it: start clean
	res := b451Run(t, f.home, append([]string{"open"}, b581Open(acceptance, "--attempt", "2")...)...)
	want := "block open: owned files: invalid path [\"[REDACTED]\"]: must be a non-empty string\n"
	if res.code != 1 || res.stdout != "" || res.stderr != want {
		t.Fatalf("exit=%d stdout=%q stderr=%q\nwant stderr %q", res.code, res.stdout, res.stderr, want)
	}
	if strings.Contains(res.stderr, secret) {
		t.Fatalf("stderr holds the signer key: %q", res.stderr)
	}
}

// b712Capture runs fn with os.Stdout and os.Stderr on pipes and returns what
// each received.
func b712Capture(t *testing.T, fn func()) (string, string) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	outC, errC := make(chan string), make(chan string)
	go func() { data, _ := io.ReadAll(outR); outC <- string(data) }()
	go func() { data, _ := io.ReadAll(errR); errC <- string(data) }()
	fn()
	os.Stdout, os.Stderr = stdout, stderr
	_ = outW.Close()
	_ = errW.Close()
	return <-outC, <-errC
}

// B712: safeStdout and safeStderr are writeSafe: each write masks every
// registered secret with redactString's rules (overlapping secrets merge
// into one mask), passes text through unchanged when nothing is registered,
// and goes to whatever os.Stdout and os.Stderr are at the time of the write.
// Goldens are redactString's output for the same secrets and text.
func TestB712SafeStreamsMaskLikeWriteSafe(t *testing.T) {
	saved := autopilotSecrets
	t.Cleanup(func() { autopilotSecrets = saved })

	autopilotSecrets = nil
	out, errText := b712Capture(t, func() {
		_, _ = io.WriteString(safeStdout, "plain abcdef\n")
		_, _ = io.WriteString(safeStderr, "plain cdef\n")
	})
	if out != "plain abcdef\n" || errText != "plain cdef\n" {
		t.Fatalf("no secrets: stdout=%q stderr=%q", out, errText)
	}

	autopilotSecrets = nil
	autopilotRegisterSecret("abcd")
	autopilotRegisterSecret("cdef")
	out, errText = b712Capture(t, func() {
		n, err := io.WriteString(safeStdout, "x abcdef y cdef abcd\n")
		if n != len("x abcdef y cdef abcd\n") || err != nil {
			t.Errorf("write returned %d, %v", n, err)
		}
		_, _ = io.WriteString(safeStderr, "e abcdefabcd\n")
		// A secret split across two writes is not caught, as in Node.
		_, _ = io.WriteString(safeStderr, "ab")
		_, _ = io.WriteString(safeStderr, "cd\n")
	})
	if want := "x [REDACTED] y [REDACTED] [REDACTED]\n"; out != want {
		t.Fatalf("stdout %q want %q", out, want)
	}
	if want := "e [REDACTED]\nabcd\n"; errText != want {
		t.Fatalf("stderr %q want %q", errText, want)
	}
}
