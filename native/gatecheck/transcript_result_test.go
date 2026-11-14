package gatecheck

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// B628 (#37): the reporter's session log is one line, the final result object of
// `claude -p --output-format json`. Its result text quotes an owner note naming
// --no-require-reviews; the coder never ran it.
func b628Sample(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("testdata/b628_claude_result.json")
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSuffix(string(data), "\n")
	if strings.Contains(line, "\n") || !strings.Contains(line, "--no-require-reviews") {
		t.Fatalf("fixture is not the reporter's one line")
	}
	return line
}

const b628Hit = "transcript line 1: code-forge-no-require-reviews"

func b628Scan(t *testing.T, text string) []string {
	t.Helper()
	hits, ok := Transcript(text)
	if !ok {
		t.Fatalf("Transcript unsupported for %.80q", text)
	}
	return b416Details(hits)
}

func TestB628ClaudeResultObjectHasNoCommandHits(t *testing.T) {
	line := b628Sample(t)
	for name, text := range map[string]string{
		"exact line":        line,
		"trailing newline":  line + "\n",
		"blank lines round": "\n" + line + "\n\n",
		"two result lines":  line + "\n" + line,
	} {
		if got := b628Scan(t, text); len(got) != 0 {
			t.Errorf("%s: hits %v, want none", name, got)
		}
	}
	// The hook check reads the same modes: a result object runs no hook command either.
	hits, ok := HookTranscript([]string{"code-forge", "block"}, line)
	if !ok || len(hits) != 0 {
		t.Errorf("HookTranscript = %v, %v; want no hits", hits, ok)
	}
}

func TestB628NotAResultObjectStaysPlainText(t *testing.T) {
	line := b628Sample(t)
	var obj map[string]any
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		t.Fatal(err)
	}
	variant := func(edit func(m map[string]any)) string {
		m := map[string]any{}
		for k, v := range obj {
			m[k] = v
		}
		edit(m)
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	cases := map[string]struct {
		text string
		line string
	}{
		"plain-text sentence":       {"The owner will close this block with `--no-require-reviews`.", "1"},
		"result object + plain":     {line + "\nnote: code-forge block close B1 --no-require-reviews", "1"},
		"plain + result object":     {"hello\n" + line, "2"},
		"stdin banner first":        {"Reading additional input from stdin...\n" + line, "2"},
		"type not result":           {variant(func(m map[string]any) { m["type"] = "summary" }), "1"},
		"result not a string":       {variant(func(m map[string]any) { m["result"] = []any{m["result"]} }), "1"},
		"result key missing":        {variant(func(m map[string]any) { m["text"] = m["result"]; delete(m, "result") }), "1"},
		"result object + array row": {line + "\n[1]", "1"},
	}
	for name, c := range cases {
		got := b628Scan(t, c.text)
		want := "transcript line " + c.line + ": code-forge-no-require-reviews"
		if !slices.Contains(got, want) {
			t.Errorf("%s: hits %v, want %q", name, got, want)
		}
	}
}

func TestB628ClaudeStreamToolUseStillFlagged(t *testing.T) {
	line := b628Sample(t)
	var obj map[string]any
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		t.Fatal(err)
	}
	// The same final answer as assistant text in a Claude Code JSONL stream is not scanned...
	text, err := json.Marshal(map[string]any{"type": "assistant", "sessionId": "s1", "message": map[string]any{
		"role": "assistant", "content": []any{map[string]any{"type": "text", "text": obj["result"]}}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := b628Scan(t, string(text)); len(got) != 0 {
		t.Errorf("assistant text: hits %v, want none", got)
	}
	// ...while a Bash tool_use that runs the owner-only flag still is.
	stream := string(text) + "\n" + b416Claude("Bash", map[string]any{"command": "code-forge block close B1 --no-require-reviews"})
	if got := b628Scan(t, stream); !slices.Equal(got, []string{"transcript line 2: code-forge-no-require-reviews"}) {
		t.Errorf("Bash tool_use: hits %v", got)
	}
}

// Perf clause: a 5 MB result-object line scans within 2x of today's plain-text time for the
// same bytes (the plain-text grep of a non-JSON line of the same size).
func TestB628ResultObjectPerf(t *testing.T) {
	line := b628Sample(t)
	var obj map[string]any
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		t.Fatal(err)
	}
	base := obj["result"].(string)
	var b strings.Builder
	for b.Len() < 5<<20 {
		b.WriteString(base)
		b.WriteString("\n")
	}
	obj["result"] = b.String()
	big, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	plain := "x" + string(big) // not JSON: today's plain-text mode on the same bytes
	measure := func(text string) time.Duration {
		best := time.Duration(1 << 62)
		for range 3 {
			start := time.Now()
			if _, ok := Transcript(text); !ok {
				t.Fatal("unsupported")
			}
			best = min(best, time.Since(start))
		}
		return best
	}
	resultTime, plainTime := measure(string(big)), measure(plain)
	t.Logf("5 MB result object %v, plain text %v", resultTime, plainTime)
	if resultTime > 2*plainTime {
		t.Errorf("result object scan %v > 2x plain text %v", resultTime, plainTime)
	}
	if got := b628Scan(t, string(big)); len(got) != 0 {
		t.Errorf("big result object: hits %v", got)
	}
}
