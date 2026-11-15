package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A line Go cannot answer as Node does (a listed block id outside printable
// ASCII among two or more, which ICU's localeCompare orders; B687 serves
// Infinity, lone surrogates and bytes that are not UTF-8 in Go) hands that
// line and the rest of stdin, byte for byte, to Node's companion; Go keeps
// the lines before it, prints the router line after it and records one
// native-status-line hand-over.
func TestB501NativeStatusHandsTheRestToNodesCompanion(t *testing.T) {
	served := `{"runs":[{"run_id":"a","status":null,"mode":"plan","spent_usd":0,"unknown_usd_sessions":0,"budget_usd":5,"blocks":[],"more":0,"closed":0,"stopped":0}]}` + "\n"
	for name, line := range map[string]string{
		"icu":           `{"record":{"blocks":{"é":{},"B1":{}}},"rows":[]}`,
		"icu-surrogate": `{"record":{"blocks":{"\ud800":{},"B1":{}}},"rows":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			home := b501NativeStatusFixture(t, false)
			t.Setenv("CODE_FORGE_HANDOVER_LOG", "")
			rest := line + "\r\n{\"record\":{\"run_id\":\"after\"},\"rows\":[]}\nbad"
			var got string
			saved := nativeStatusCompanion
			nativeStatusCompanion = func(stdin io.Reader, stdout io.Writer) (int, error) {
				data, err := io.ReadAll(stdin)
				got = string(data)
				_, _ = io.WriteString(stdout, "NODE\n")
				return 0, err
			}
			t.Cleanup(func() { nativeStatusCompanion = saved })
			b501Stdin(t, "{\"record\":{\"run_id\":\"a\"},\"rows\":[]}\n"+rest)
			code, out, errText := runCaptured(t, "native-status")
			if code != 1 || out != served+"NODE\n" || errText != b501RouterLine || got != rest {
				t.Fatalf("code=%d stdout=%q stderr=%q node stdin=%q", code, out, errText, got)
			}
			log, _ := os.ReadFile(filepath.Join(home, ".code-forge", "native", "handovers.jsonl"))
			if strings.Count(string(log), "\n") != 1 || !strings.Contains(string(log), `"verb":"native-status","sub":"","reason":"native-status-line"`) {
				t.Fatalf("handovers.jsonl = %s", log)
			}
		})
	}
}

// A companion that cannot start ends with its own exit code and no router line.
func TestB501NativeStatusCompanionThatCannotStart(t *testing.T) {
	b501NativeStatusFixture(t, false)
	b501Stdin(t, `{"record":{"blocks":{"é":{},"B1":{}}},"rows":[]}`+"\n")
	code, out, errText := runCaptured(t, "native-status")
	if code != 127 || out != "" || !strings.HasPrefix(errText, "code-forge-native: node executable \"/nonexistent-b501-node\" not found") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errText)
	}
}
