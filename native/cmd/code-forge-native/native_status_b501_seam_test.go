package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B690b: Node's companion is gone. A line whose listed block ids hold a
// character outside printable ASCII (B501 handed it and the rest of stdin to
// Node) is answered in Go: printable ASCII ids first in their usual order,
// the other ids after them by UTF-16 code units; the lines after it are
// answered too, and no hand-over is recorded.
func TestB690bNativeStatusAnswersNonASCIIBlockIDsInGo(t *testing.T) {
	first := `{"runs":[{"run_id":"a","status":null,"mode":"plan","spent_usd":0,"unknown_usd_sessions":0,"budget_usd":5,"blocks":[],"more":0,"closed":0,"stopped":0}]}` + "\n"
	after := `{"runs":[{"run_id":"after","status":null,"mode":"plan","spent_usd":0,"unknown_usd_sessions":0,"budget_usd":5,"blocks":[],"more":0,"closed":0,"stopped":0}]}` + "\n"
	unreadable := `{"ok":false,"error":"status-unreadable"}` + "\n"
	block := func(id string) string {
		return `{"id":"` + id + `","state":"open","level":null,"model":null,"gate":{"result":"none","name":null},"findings":0}`
	}
	for name, tc := range map[string]struct{ line, blocks string }{
		"non-ascii": {`{"record":{"blocks":{"é":{},"B1":{}}},"rows":[]}`, block("B1") + "," + block("é")},
		"surrogate": {`{"record":{"blocks":{"\ud800":{},"B1":{}}},"rows":[]}`, block("B1") + "," + block(`\ud800`)},
		"utf16-order": {`{"record":{"blocks":{"ｚ":{},"😀":{},"B2":{},"é":{},"B10":{}}},"rows":[]}`,
			block("B2") + "," + block("B10") + "," + block("é") + "," + block("😀") + "," + block("ｚ")},
	} {
		t.Run(name, func(t *testing.T) {
			home := b501NativeStatusFixture(t, false)
			t.Setenv("CODE_FORGE_HANDOVER_LOG", "")
			b501Stdin(t, "{\"record\":{\"run_id\":\"a\"},\"rows\":[]}\n"+tc.line+"\r\n{\"record\":{\"run_id\":\"after\"},\"rows\":[]}\nbad")
			code, out, errText := runCaptured(t, "native-status")
			want := first + `{"runs":[{"run_id":"undefined","status":null,"mode":"plan","spent_usd":0,"unknown_usd_sessions":0,"budget_usd":5,"blocks":[` + tc.blocks + `],"more":0,"closed":0,"stopped":0}]}` + "\n" + after + unreadable
			if code != 0 || out != want || errText != "" {
				t.Fatalf("code=%d\nstdout=%q\n  want=%q\nstderr=%q", code, out, want, errText)
			}
			if log, err := os.ReadFile(filepath.Join(home, ".code-forge", "native", "handovers.jsonl")); err == nil && strings.Contains(string(log), "native-status") {
				t.Fatalf("a hand-over was recorded: %s", log)
			}
		})
	}
}
