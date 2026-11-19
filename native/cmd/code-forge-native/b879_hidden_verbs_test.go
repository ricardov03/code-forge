package main

import (
	"strings"
	"testing"
)

// B879 (B801 audit P07): the hidden verbs land and native-status no longer
// print the Node router's `verb "<v>" has no default export function` with
// exit 1. The samples are the audit's cases: `land`, `land --bogus`,
// `native-status` (records on stdin) and `native-status --bogus`.

func TestB879LandIsAUsageError(t *testing.T) {
	for _, args := range [][]string{{"land"}, {"land", "--bogus"}} {
		home := b613Env(t)
		code, stdout, stderr := runCaptured(t, args...)
		if code != 2 || stdout != "" || stderr != b689Stderr || strings.Contains(stderr, "default export") {
			t.Fatalf("%v: code=%d stdout=%q stderr=%q", args, code, stdout, stderr)
		}
		if message, _, _ := b699LastEntry(t, home); message != b689Refusal {
			t.Fatalf("%v: logged %q", args, message)
		}
	}
}

func TestB879NativeStatusServesAndRefuses(t *testing.T) {
	home := b501NativeStatusFixture(t, true)
	// records piped in: answered, exit 0, nothing on stderr
	b501Stdin(t, `{"record":{"run_id":"r1"},"rows":[]}`+"\n")
	code, out, errText := runCaptured(t, "native-status")
	if code != 0 || !strings.HasPrefix(out, `{"runs":[{"run_id":"r1"`) || errText != "" {
		t.Fatalf("serve: code=%d stdout=%q stderr=%q", code, out, errText)
	}
	// an unknown flag: usage error before reading stdin
	b501Stdin(t, `{"record":{"run_id":"r1"},"rows":[]}`+"\n")
	code, out, errText = runCaptured(t, "native-status", "--bogus")
	if code != 2 || out != "" || errText != b501Refusal("--bogus") {
		t.Fatalf("--bogus: code=%d stdout=%q stderr=%q", code, out, errText)
	}
	if message, _, _ := b699LastEntry(t, home); message != "native-status: takes no arguments (got --bogus); it reads status records piped on stdin" {
		t.Fatalf("logged %q", message)
	}
	// a terminal on stdin: nothing to read, so refuse instead of waiting
	saved := nsStdinTTY
	nsStdinTTY = func() bool { return true }
	t.Cleanup(func() { nsStdinTTY = saved })
	code, out, errText = runCaptured(t, "native-status")
	want := nativeStatusUsageText + "native-status: reads status records piped on stdin; it takes no arguments\nnext: code-forge status\n"
	if code != 2 || out != "" || errText != want {
		t.Fatalf("tty: code=%d stdout=%q stderr=%q", code, out, errText)
	}
}
