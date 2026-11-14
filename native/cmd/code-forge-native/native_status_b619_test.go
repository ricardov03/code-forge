package main

import (
	"os"
	"path/filepath"
	"testing"
)

// B619: the routed `native-status` is the Go companion (B501). Every form the
// old metadata wrapper took (no arguments, a word, --help, -h) runs in Go with
// Node out of reach, and no hand-over row is written.
func TestB619NativeStatusNeverHandsOverToTheCompanion(t *testing.T) {
	home := b501NativeStatusFixture(t, false)
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	t.Setenv("CODE_FORGE_NATIVE", "")
	cases := []struct {
		args       []string
		code       int
		out, err   string
		stdinLines string
	}{
		{nil, 1, "", b501RouterLine, ""},
		{[]string{"x"}, 1, "{\"ok\":false,\"error\":\"status-unreadable\"}\n", b501RouterLine, "{}\n"},
		{[]string{"--help"}, 0, nativeStatusUsageText, "", "{}\n"},
		{[]string{"-h"}, 0, nativeStatusUsageText, "", "{}\n"},
	}
	for _, c := range cases {
		b501Stdin(t, c.stdinLines)
		code, out, errText := runCaptured(t, append([]string{"native-status"}, c.args...)...)
		want := c.out
		if c.code == 0 { // B613: --help and -h print the wrapped help page
			want = helpExpect(t, append([]string{"native-status"}, c.args...), 0, c.out)
		}
		if code != c.code || out != want || errText != c.err {
			t.Fatalf("%q: code=%d stdout=%q stderr=%q", c.args, code, out, errText)
		}
	}
	log := filepath.Join(home, ".code-forge", "native", "handovers.jsonl")
	if data, err := os.ReadFile(log); err == nil || !os.IsNotExist(err) {
		t.Fatalf("handovers.jsonl must not exist: err=%v data=%q", err, data)
	}
}
