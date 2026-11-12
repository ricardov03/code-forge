package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A line Go cannot answer as Node does (a block id ICU must order, a number
// JSON.parse reads as Infinity, a lone surrogate escape, bytes that are not
// UTF-8) hands that line and the rest of stdin, byte for byte, to Node's
// companion; Go keeps the lines before it, prints the router line after it
// and records one native-status-line hand-over.
func TestB501NativeStatusHandsTheRestToNodesCompanion(t *testing.T) {
	served := `{"runs":[{"run_id":"a","status":null,"mode":"plan","spent_usd":0,"unknown_usd_sessions":0,"budget_usd":5,"blocks":[],"more":0,"closed":0,"stopped":0}]}` + "\n"
	for name, line := range map[string]string{
		"icu":       `{"record":{"blocks":{"é":{},"B1":{}}},"rows":[]}`,
		"infinity":  `{"record":{},"rows":[1e400]}`,
		"surrogate": `{"record":{"run_id":"\ud800"},"rows":[]}`,
		"not-utf8":  "{\"record\":{\"run_id\":\"\xff\"},\"rows\":[]}",
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
	b501Stdin(t, `{"record":{},"rows":[1e400]}`+"\n")
	code, out, errText := runCaptured(t, "native-status")
	if code != 127 || out != "" || !strings.HasPrefix(errText, "code-forge-native: node executable \"/nonexistent-b501-node\" not found") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errText)
	}
}

// With no HOME Node's os.homedir() reads the password file: the whole verb
// hands over before stdin is read (Node is missing here, so the hand-over
// exits 127 and no line is answered).
func TestB501NativeStatusNoHomeHandsOver(t *testing.T) {
	b501NativeStatusFixture(t, false)
	t.Setenv("HOME", "")
	b501Stdin(t, "{\"record\":{},\"rows\":[]}\n")
	code, out, errText := runCaptured(t, "native-status")
	if code != 127 || out != "" || !strings.HasPrefix(errText, "code-forge-native: node executable \"/nonexistent-b501-node\" not found") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errText)
	}
}
